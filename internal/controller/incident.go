package controller

import (
	"context"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aryausingh/k8s-selfheal/internal/safety"
)

// Attempt budget. These values appear in the report, so they are exported
// constants rather than literals — docs/measurement-definitions.md §5 is the
// source of truth and these must match it.
const (
	MaxAttempts        = 3
	AttemptBackoffBase = 30 * time.Second
	CooldownPeriod     = 5 * time.Minute
)

// Incident terminal outcomes. Note the distinction the measurement
// definitions depend on: an *attempt* ends recovered or rolled_back, an
// *incident* ends in one of these four. A rolled_back attempt does not end the
// incident — the next one retries under backoff until the budget is spent, at
// which point the incident ends exhausted.
const (
	OutcomeRecovered = "recovered"
	OutcomeExhausted = "exhausted"
	OutcomeEscalated = "escalated"
	OutcomeRejected  = "rejected"
)

// incidentRecord is the per-Deployment remediation record. One incident per
// Deployment at a time, keyed the same way the old in-flight guard was —
// "namespace/OwnerDeployment", never pod UID, because RestartPod deletes the
// pod and the replacement carries a brand-new UID.
type incidentRecord struct {
	id              string
	attemptCount    int
	firstDetectedAt time.Time
	lastAttemptAt   time.Time // when the last attempt FINISHED — backoff counts from here
	terminalOutcome string    // empty while the incident is still active
	terminalAt      time.Time
	generation      int64 // Deployment metadata.generation last seen
	inFlight        bool  // an attempt is running right now

	// The evidence bundle, captured once at detection and reused by every
	// attempt in this incident. See incidentEvidence.
	logs             string
	events           []string
	evidenceCaptured bool
}

// incidentClosure is the terminal snapshot the audit line is built from.
type incidentClosure struct {
	id       string
	outcome  string
	attempts int
}

// admission is what beginAttempt tells Reconcile to do.
type admission int

const (
	admitProceed   admission = iota // claim taken, run an attempt
	admitSkip                       // in flight, backing off, or gone quiet
	admitExhausted                  // budget just spent — log once, then go quiet
)

// incidentKey builds the per-Deployment record key.
func incidentKey(namespace, ownerDeployment string) string {
	return namespace + "/" + ownerDeployment
}

// beginAttempt decides whether an attempt may run for this Deployment and, if
// so, claims the incident so no concurrent reconcile can start a second one.
//
// A plain map under a mutex replaced the sync.Map the presence-only guard
// used. sync.Map's advantage was LoadOrStore giving atomic claim-if-absent in
// one call, but an incident is read-modify-write state — budget, backoff and
// cooldown all have to be evaluated against the same snapshot and then written
// back — so the value needs a lock regardless. One lock around the whole
// decision is both simpler and the only way the checks stay consistent with
// each other.
func (r *PodReconciler) beginAttempt(key string, generation int64, now time.Time) (*incidentRecord, admission) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.incidents == nil {
		r.incidents = make(map[string]*incidentRecord)
	}

	record := r.incidents[key]

	// A changed generation means a human edited the Deployment or a new
	// rollout landed, so whatever we concluded about the old spec no longer
	// applies. Drop the record entirely and start fresh — this is the only
	// thing that clears an exhausted incident.
	//
	// Our own remediation also moves the generation: rollout_undo rewrites
	// the pod template and the snapshot restore rewrites it back, so a
	// three-attempt incident bumps it six times (observed: 2 -> 8 on W3).
	// That does not resurrect the incident, for two reasons that have to hold
	// together. The comparison runs only once the incident is already
	// terminal, and the stored generation is refreshed on every admitted
	// attempt — so at the moment we go terminal it equals whatever our last
	// action left behind. From then on we take no actions at all, so nothing
	// but an external change can move it again.
	if record != nil && record.terminalOutcome != "" && generation != record.generation {
		delete(r.incidents, key)
		record = nil
	}

	if record != nil && record.terminalOutcome != "" {
		// Exhausted is sticky, every other terminal outcome expires after the
		// cooldown. Letting exhausted expire would re-arm the controller on a
		// Deployment we already gave up on, which is precisely the unbounded
		// remediation loop the budget exists to stop — the plan's acceptance
		// test is that a permanently-broken Deployment is silent at minute 10,
		// and a 5-minute cooldown would have it acting again at minute 8.
		if record.terminalOutcome == OutcomeExhausted || now.Sub(record.terminalAt) < CooldownPeriod {
			return record, admitSkip
		}
		delete(r.incidents, key)
		record = nil
	}

	if record == nil {
		record = &incidentRecord{
			id:              string(uuid.NewUUID()),
			firstDetectedAt: now,
		}
		r.incidents[key] = record
	}
	record.generation = generation

	if record.inFlight {
		return record, admitSkip
	}
	if record.attemptCount >= MaxAttempts {
		record.terminalOutcome = OutcomeExhausted
		record.terminalAt = now
		return record, admitExhausted
	}
	if record.attemptCount > 0 && now.Sub(record.lastAttemptAt) < attemptBackoff(record.attemptCount) {
		return record, admitSkip
	}

	record.inFlight = true
	return record, admitProceed
}

// recordAttempt increments the attempt counter and reports the new number. It
// is deliberately separate from beginAttempt and called only once an action is
// about to be dispatched: an incident that escalates or is rejected takes no
// action at all, so it must not consume budget (measurement-definitions.md §2
// — those outcomes are terminal at attempt 0).
func (r *PodReconciler) recordAttempt(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.incidents[key]
	if record == nil {
		return 0
	}
	record.attemptCount++
	// lastAttemptAt is deliberately not set here. Backoff runs from when an
	// attempt finishes, not when it starts — see endAttempt.
	return record.attemptCount
}

// endAttempt releases the in-flight claim and ends the incident when the
// outcome is terminal.
//
// rolled_back is NOT terminal for the incident, and neither is an attempt that
// errored (outcome ""): both leave the record active so the next reconcile
// retries under backoff, and the incident ends exhausted once the budget runs
// out. Anything else — recovered, escalated, rejected — ends it here.
func (r *PodReconciler) endAttempt(key, outcome string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.incidents[key]
	if record == nil {
		return
	}
	record.inFlight = false
	if outcome == "" || outcome == string(safety.OutcomeRolledBack) {
		// Backoff is measured from here, the moment the attempt finished,
		// rather than from when it started. An attempt takes about 30s of
		// wall clock (the verifier's readiness timeout) and measuring from
		// its start lets that duration eat the backoff: the first deployed
		// run showed a 12s gap between attempts where the definitions
		// document promises 30s. "30s after attempt 1" means after it
		// completes.
		record.lastAttemptAt = now
		return
	}
	record.terminalOutcome = outcome
	record.terminalAt = now
}

// attemptBackoff is how long to wait after an attempt finishes before the next
// one may start, given how many have already completed: 30s after the first,
// 60s after the second. The shift is bounded because MaxAttempts caps the
// input.
func attemptBackoff(completedAttempts int) time.Duration {
	return AttemptBackoffBase << (completedAttempts - 1)
}

// deploymentGeneration reads metadata.generation, which Kubernetes increments
// on every spec change. It is the signal that a human or a new rollout
// intervened, and the only thing that resurrects an exhausted incident.
func (r *PodReconciler) deploymentGeneration(ctx context.Context, namespace, name string) (int64, error) {
	var deployment appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &deployment); err != nil {
		return 0, err
	}
	return deployment.Generation, nil
}

// claimHeld reports whether an attempt is currently running for this key. Only
// used by tests, which cannot read the map without taking mu.
func (r *PodReconciler) claimHeld(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.incidents[key]
	return record != nil && record.inFlight
}

// incidentEvidence returns this incident's frozen evidence bundle, and whether
// one has been captured yet.
//
// Evidence is collected once, at detection, and reused for every attempt.
// Re-collecting per attempt is what poisoned classification on retries: the
// collector keeps the newest 25 Events, and remediation produces a burst of
// its own — Killing from the kubelet, SuccessfulDelete and SuccessfulCreate
// from the ReplicaSet controller, ScalingReplicaSet from the Deployment
// controller — so by attempt 2 the Events that explain the original failure
// have been pushed out of the window by the Events our own action caused. The
// classifier then reasons about the remediation instead of the fault.
//
// Note what is NOT done here: the plan called for filtering Events whose
// source is this controller. That filter would match nothing — SAGE emits no
// Kubernetes Events at all (there is no EventRecorder anywhere in the repo).
// The crowding Events are emitted by Kubernetes' own controllers reacting to
// our action, and filtering by their reporting component would also drop
// BackOff, which is reported by the kubelet and is the signal we most need.
// Freezing at detection is the filter: nothing that happened after we started
// acting can enter the bundle, whoever reported it.
//
// The tradeoff, stated rather than hidden: a cause that only becomes visible
// *after* the first attempt is never seen. A workload whose real failure
// surfaces only once restarted will be classified on pre-action evidence for
// all three attempts. That is the deliberate choice — it preserves
// attribution, because every attempt in an incident then reasons about the
// same fault rather than about our own remediation — but it is a real loss and
// it belongs in the threats-to-validity section.
func (r *PodReconciler) incidentEvidence(key string) (string, []string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.incidents[key]
	if record == nil || !record.evidenceCaptured {
		return "", nil, false
	}
	// Cloned so a caller cannot mutate the frozen bundle that later attempts
	// in this incident will be classified against.
	return record.logs, slices.Clone(record.events), true
}

// freezeEvidence stores the bundle for the rest of the incident. It is dropped
// with the record itself, so a new incident — after a cooldown or a generation
// change — collects fresh evidence.
func (r *PodReconciler) freezeEvidence(key, logs string, events []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.incidents[key]
	if record == nil {
		return
	}
	record.logs = logs
	record.events = slices.Clone(events)
	record.evidenceCaptured = true
}

// closedIncident reports the terminal snapshot for an incident that has just
// finished, or false while it is still active. Read under the lock so the
// caller never touches record fields directly.
func (r *PodReconciler) closedIncident(key string) (incidentClosure, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.incidents[key]
	if record == nil || record.terminalOutcome == "" {
		return incidentClosure{}, false
	}
	return incidentClosure{
		id:       record.id,
		outcome:  record.terminalOutcome,
		attempts: record.attemptCount,
	}, true
}

// auditIncidentClosed writes a LOGGED line for an incident outcome decided
// outside Service.Remediate: exhausted, escalated, or rejected.
//
// Recovered already has a LOGGED entry from Service.Remediate, while
// rolled_back is an attempt outcome rather than an incident outcome. The
// controller therefore only fills the three genuine gaps and does not invent
// a second state machine above Owner 2's frozen lifecycle.
func (r *PodReconciler) auditIncidentClosed(ctx context.Context, podRef string, closure incidentClosure) {
	if r.Audit == nil {
		return
	}
	entry := safety.AuditEntry{
		IncidentID:    closure.id,
		AttemptNumber: closure.attempts,
		Timestamp:     time.Now(),
		Pod:           podRef,
		State:         safety.StateLogged,
		Action:        "",
		Result:        closure.outcome,
		Workload:      r.AuditMetadata.Workload,
		ArmLabel:      r.AuditMetadata.ArmLabel,
	}
	if err := r.Audit.Append(entry); err != nil {
		log.FromContext(ctx).Error(err, "could not write the terminal incident audit line",
			"incidentID", closure.id, "outcome", closure.outcome)
	}
}

// closeIncidentIfTerminal writes the missing terminal line when the last call
// ended the incident, and does nothing for active incidents or recovered,
// which Service.Remediate already logged.
func (r *PodReconciler) closeIncidentIfTerminal(ctx context.Context, key, podRef string) {
	if closure, closed := r.closedIncident(key); closed {
		if closure.outcome == OutcomeRecovered {
			return
		}
		r.auditIncidentClosed(ctx, podRef, closure)
	}
}
