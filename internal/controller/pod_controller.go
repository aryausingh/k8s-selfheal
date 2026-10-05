package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aryausingh/k8s-selfheal/internal/classifier"
	"github.com/aryausingh/k8s-selfheal/internal/contracts"
	"github.com/aryausingh/k8s-selfheal/internal/safety"
)

// PodReconciler watches core Pods and detects CrashLoopBackOff.
type PodReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ManagerCtx is the manager-lifetime context (from ctrl.SetupSignalHandler
	// in cmd/main.go), not Reconcile's own ctx. Remediation dispatched into a
	// goroutine (Task 4) must be called with this context — Reconcile's ctx
	// is cancelled the moment that single reconcile returns, which would cut
	// off a still-running remediation (up to ~90s: readiness + stability
	// window) almost immediately.
	ManagerCtx context.Context

	// Classifier resolves a DetectionEvent into Subhashini's Proposal via her
	// ClassifyIncident seam. nil is treated as a misconfiguration, not a
	// silent no-op: Reconcile escalates by default rather than automating
	// blind (see the nil check in Reconcile).
	Classifier classifier.IncidentClassifier

	// Actions maps a RemediationAction's Name() ("restart_pod",
	// "rollout_undo") to the concrete action to inject into Ananya's
	// safety.Service. Reconcile selects an entry by matching against
	// proposal.RecommendedAction — Remediate() itself never decides which
	// action to run (confirmed with Ananya).
	Actions map[string]safety.RemediationAction

	// Evidence collects the pod logs and Kubernetes Events that populate
	// classifier.IncidentInput. Without it every sub-cause except "unknown"
	// fails Subhashini's semantic guard and the pipeline escalates every
	// incident, so a nil Evidence is a functioning but permanently
	// escalate-only controller — safe, just useless. See evidence.go.
	Evidence *EvidenceCollector

	// Snapshots, Verifier, Audit, and Clock are Owner 2's Service
	// dependencies. They're shared across calls because none of them hold
	// per-remediation mutable state; a fresh *safety.Service is built per
	// call with only Action varying, rather than mutating a shared Service
	// mid-flight (per Ananya's review note).
	Snapshots safety.SnapshotStore
	Verifier  safety.PodVerifier
	Audit     safety.AuditWriter
	Clock     safety.Clock

	// AuditMetadata is supplied by the experiment harness configuration and
	// copied onto every Owner 2 transition. The controller does not infer
	// workload or arm labels from remediation behavior.
	AuditMetadata safety.AuditMetadata

	// incidents holds one remediation record per Deployment, keyed by
	// "namespace/OwnerDeployment" — deliberately NOT pod UID. RestartPod
	// deletes the crash-looping pod and the ReplicaSet controller creates a
	// replacement with a brand-new UID; a UID-keyed record would not recognize
	// that replacement re-crashing as the same incident, which defeats the
	// whole point of tracking one.
	//
	// Guarded by mu rather than being a sync.Map: this used to be a
	// presence-only set, where LoadOrStore gave atomic claim-if-absent in a
	// single call. An incident is read-modify-write state instead — budget,
	// backoff and cooldown are evaluated against one snapshot and written
	// back — so the value needs a lock either way. See incident.go.
	mu        sync.Mutex
	incidents map[string]*incidentRecord
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch
// replicasets needs watch, not just get;list: ownerDeploymentName and Ananya's
// DeploymentPodResolver both read ReplicaSets through the manager's *cached*
// client, and a cache read starts an informer, which LISTs and then WATCHes.
// Without watch the reflector re-lists on every failed watch and logs a
// forbidden error every few seconds, leaving the ReplicaSet cache refreshed
// only by accident. Caught by running deployed rather than via `make run`.
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if req.Namespace == "kube-system" || req.Namespace == "kube-node-lease" || req.Namespace == "local-path-storage" {
		return ctrl.Result{}, nil
	}

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		// Pod is gone since the event fired — nothing to reconcile.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			deploymentName, err := r.ownerDeploymentName(ctx, &pod)
			if err != nil {
				logger.Error(err, "could not resolve owner deployment", "pod", pod.Name)
			}

			event := contracts.DetectionEvent{
				PodName:         pod.Name,
				Namespace:       pod.Namespace,
				ContainerName:   cs.Name,
				RestartCount:    cs.RestartCount,
				OwnerDeployment: deploymentName,
				Timestamp:       time.Now(),
			}

			logger.Info("DETECTED CrashLoopBackOff", "event", event)

			// Every downstream stage needs the owning Deployment: the
			// in-flight guard is keyed on it (an empty name would collide
			// across every ownerless pod in the namespace), Ananya's
			// Remediate() rejects an event without one, her snapshot/restore
			// rollback has nothing to capture, and her verifier has no
			// Deployment to resolve the replacement pod from. A bare pod is
			// therefore out of scope, not a failure — escalate and stop
			// before taking the guard.
			if event.OwnerDeployment == "" {
				logger.Info("ESCALATED — no owner Deployment resolved, cannot snapshot or roll back",
					"namespace", event.Namespace, "pod", event.PodName)
				break
			}

			// metadata.generation is read before the admission decision
			// because the decision depends on it: a changed generation is the
			// only thing that resurrects an incident we gave up on. A failed
			// read is non-fatal — generation 0 simply never matches a stored
			// non-zero one, so the record is left alone.
			generation, genErr := r.deploymentGeneration(ctx, event.Namespace, event.OwnerDeployment)
			if genErr != nil {
				logger.Error(genErr, "could not read deployment generation, proceeding without cooldown reset",
					"namespace", event.Namespace, "deployment", event.OwnerDeployment)
			}

			key := incidentKey(event.Namespace, event.OwnerDeployment)
			record, decision := r.beginAttempt(key, generation, time.Now())
			if decision == admitExhausted {
				// Logged exactly once: beginAttempt sets the terminal outcome
				// here, so every later reconcile takes the sticky-exhausted
				// branch and returns admitSkip instead.
				logger.Info("EXHAUSTED — attempt budget spent, going quiet until the deployment changes",
					"namespace", event.Namespace, "deployment", event.OwnerDeployment,
					"incidentID", record.id, "attempts", record.attemptCount)
				r.closeIncidentIfTerminal(ctx, key)
			}
			if decision != admitProceed {
				if decision == admitSkip {
					logger.Info("remediation already in flight for this deployment, skipping",
						"namespace", event.Namespace, "deployment", event.OwnerDeployment,
						"incidentID", record.id, "attempts", record.attemptCount,
						"inFlight", record.inFlight, "terminalOutcome", record.terminalOutcome)
				}
				break
			}

			event.IncidentID = record.id

			if r.Classifier == nil {
				logger.Error(fmt.Errorf("PodReconciler.Classifier is not configured"),
					"cannot classify incident — escalating by default rather than automating blind",
					"namespace", event.Namespace, "pod", event.PodName)
				r.endAttempt(key, OutcomeEscalated, time.Now())
				r.closeIncidentIfTerminal(ctx, key)
				break
			}

			// Task 6: collect evidence, classify, then gate on
			// safe_for_automation.
			//
			// The DetectionEvent says a container is crash-looping; the logs
			// and Events say why, and "why" is what the classifier actually
			// classifies. Collection is best-effort by design (see
			// evidence.go): when it comes back empty, the semantic guard
			// rejects every sub-cause but "unknown" and the incident
			// escalates — the fail-safe direction — rather than the
			// classifier guessing from a restart count alone.
			// Collected once per incident, not once per attempt — see
			// incidentEvidence in incident.go for why re-collecting poisons
			// classification on retries, and what freezing costs.
			logs, events, frozen := r.incidentEvidence(key)
			if !frozen {
				logs, events = r.Evidence.Collect(ctx, &pod, event.ContainerName, event.OwnerDeployment)
				r.freezeEvidence(key, logs, events)
			}
			logger.Info("Collected incident evidence",
				"pod", event.PodName, "logBytes", len(logs), "eventCount", len(events),
				"incidentID", event.IncidentID, "reusedFrozenBundle", frozen)

			incident := classifier.IncidentInput{
				DetectionEvent: classifier.DetectionEvent{
					PodName:         event.PodName,
					Namespace:       event.Namespace,
					ContainerName:   event.ContainerName,
					RestartCount:    event.RestartCount,
					OwnerDeployment: event.OwnerDeployment,
					Timestamp:       event.Timestamp,
				},
				Logs:   logs,
				Events: events,
			}
			classification := r.Classifier.ClassifyIncident(ctx, incident)
			proposal := classification.Proposal

			// classifyErr is always nil at this call site: ClassifyIncident
			// never returns an error — a failed or invalid classification is
			// already converted into a safe escalate_to_human Proposal
			// internally (classification.FallbackUsed records that this
			// happened). ShouldEscalate's classifyErr parameter models a
			// transport-style failure this API doesn't expose; it stays in
			// the signature in case a future classifier implementation does.
			if ShouldEscalate(proposal.SafeForAutomation, nil) {
				logger.Info("ESCALATED — not safe for automation",
					"namespace", event.Namespace, "pod", event.PodName,
					"subCause", proposal.SubCause,
					"recommendedAction", proposal.RecommendedAction,
					"reasoning", proposal.Reasoning,
					"fallbackUsed", classification.FallbackUsed,
					"fallbackReason", classification.FallbackReason)
				r.endAttempt(key, OutcomeEscalated, time.Now())
				r.closeIncidentIfTerminal(ctx, key)
				break
			}

			// Task 4: resolve which RemediationAction the proposal selected.
			// Arya picks the action and injects it into a per-call Service —
			// Remediate() itself never decides which action to run — matching
			// proposal.RecommendedAction against each action's Name().
			action, ok := r.Actions[proposal.RecommendedAction]
			if !ok {
				logger.Error(fmt.Errorf("no remediation action registered for %q", proposal.RecommendedAction),
					"classifier recommended an action with no matching implementation — escalating instead",
					"namespace", event.Namespace, "pod", event.PodName)
				r.endAttempt(key, OutcomeRejected, time.Now())
				r.closeIncidentIfTerminal(ctx, key)
				break
			}

			event.AttemptNumber = r.recordAttempt(key)

			service := &safety.Service{
				Snapshots: r.Snapshots,
				Verifier:  r.Verifier,
				Action:    action,
				Audit:     r.Audit,
				Clock:     r.Clock,
				Metadata:  r.AuditMetadata,
			}

			// Dispatched into a goroutine against ManagerCtx, not this
			// Reconcile call's ctx: Remediate() can run up to ~90s (30s
			// readiness + 60s stability window, per Ananya's verifier
			// constants), and Reconcile's ctx is cancelled the instant this
			// call returns — that would cut a still-running remediation off
			// almost immediately. The guard is released via defer inside the
			// goroutine so it stays held for the whole Remediate() lifetime,
			// per Ananya's review note, not released early as before.
			go func() {
				// result is read by the deferred endAttempt, so it must be
				// declared before it: "" (an errored attempt) and rolled_back
				// both leave the incident active to retry under backoff, and
				// the defer guarantees the in-flight claim is released even
				// if Remediate panics.
				result := ""
				defer func() {
					r.endAttempt(key, result, time.Now())
					// No-op unless this attempt ended the incident: a
					// rolled_back attempt with budget left leaves it active
					// and the next reconcile retries under backoff.
					r.closeIncidentIfTerminal(r.ManagerCtx, key)
				}()

				outcome, err := service.Remediate(r.ManagerCtx, event)
				if err != nil {
					logger.Error(err, "remediation failed",
						"namespace", event.Namespace, "deployment", event.OwnerDeployment,
						"action", action.Name(), "incidentID", event.IncidentID,
						"attempt", event.AttemptNumber)
					return
				}
				result = string(outcome.Result)
				logger.Info("remediation finished",
					"namespace", event.Namespace, "deployment", event.OwnerDeployment,
					"action", action.Name(), "result", outcome.Result, "mttr", outcome.MTTR,
					"incidentID", event.IncidentID, "attempt", event.AttemptNumber)
			}()
			break
		}
	}
	return ctrl.Result{}, nil
}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

// Owner-reference Kind values, shared with tests in this package.
const (
	kindReplicaSet = "ReplicaSet"
	kindDeployment = "Deployment"
)

func (r *PodReconciler) ownerDeploymentName(ctx context.Context, pod *corev1.Pod) (string, error) {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != kindReplicaSet {
			continue
		}
		var rs appsv1.ReplicaSet
		if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: ref.Name}, &rs); err != nil {
			return "", err
		}
		for _, rsRef := range rs.OwnerReferences {
			if rsRef.Kind == kindDeployment {
				return rsRef.Name, nil
			}
		}
	}
	return "", nil
}
