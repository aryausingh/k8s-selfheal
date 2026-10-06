package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aryausingh/k8s-selfheal/internal/classifier"
	"github.com/aryausingh/k8s-selfheal/internal/contracts"
	"github.com/aryausingh/k8s-selfheal/internal/safety"
)

// --- test scaffolding -------------------------------------------------
//
// recordingSink stands in as a controller-runtime logr.LogSink that
// captures every Info()/Error() call, including the structured key/value
// pairs (e.g. "event", <DetectionEvent>), so tests can assert precisely on
// what Reconcile decided rather than just that it ran. The detection-only
// tests in this file predate Task 4/6 landing (see
// remediation_wiring_test.go for the classify/escalate/dispatch behavior)
// and still rely on it for that reason.

type logCall struct {
	msg   string
	kvs   []any
	isErr bool
}

// recordingSink is shared between the synchronous logger.Info/Error calls
// Reconcile makes directly and the goroutine it dispatches for Task 4
// remediation, which logs its own outcome after Reconcile has already
// returned. Those two writers run concurrently from the test's point of
// view, so this needs a real lock, not just append-and-hope.
type recordingSink struct {
	mu    sync.Mutex
	calls []logCall
}

func (s *recordingSink) Init(logr.RuntimeInfo) {}
func (s *recordingSink) Enabled(int) bool      { return true }
func (s *recordingSink) Info(_ int, msg string, kvs ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, logCall{msg: msg, kvs: kvs})
}
func (s *recordingSink) Error(_ error, msg string, kvs ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, logCall{msg: msg, kvs: kvs, isErr: true})
}
func (s *recordingSink) WithValues(...any) logr.LogSink { return s }
func (s *recordingSink) WithName(string) logr.LogSink   { return s }

func (s *recordingSink) has(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.Contains(c.msg, substr) {
			return true
		}
	}
	return false
}

// event returns the contracts.DetectionEvent logged alongside msg, if any.
func (s *recordingSink) event(msg string) (contracts.DetectionEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.msg != msg {
			continue
		}
		for i := 0; i+1 < len(c.kvs); i += 2 {
			if c.kvs[i] == "event" {
				if ev, ok := c.kvs[i+1].(contracts.DetectionEvent); ok {
					return ev, true
				}
			}
		}
	}
	return contracts.DetectionEvent{}, false
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding appsv1 to scheme: %v", err)
	}
	return scheme
}

// newTestContext returns a context wired to a fresh recordingSink so the
// caller can inspect what Reconcile logged.
func newTestContext() (context.Context, *recordingSink) {
	sink := &recordingSink{}
	return log.IntoContext(context.Background(), logr.New(sink)), sink
}

func crashingContainerStatus(name string, restarts int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         name,
		RestartCount: restarts,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}
}

func runningContainerStatus(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:  name,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}
}

const (
	detectedMsg = "DETECTED CrashLoopBackOff"
	skippedMsg  = "remediation already in flight for this deployment, skipping"

	testNamespace      = "default"
	testDeploymentName = "web"
	testReplicaSetName = "web-abc123"
	testPodName        = "web-abc123-xyz"
)

// ownedPod builds a Pod -> ReplicaSet -> Deployment owner-reference chain
// rooted at testPodName/testReplicaSetName/testDeploymentName, with the
// given container statuses on the pod. Shared by the tests that need
// OwnerDeployment resolution.
func ownedPod(statuses ...corev1.ContainerStatus) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: testDeploymentName, Namespace: testNamespace}}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: testReplicaSetName, Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: kindDeployment, Name: testDeploymentName, Controller: boolPtr(true)}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: testPodName, Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: kindReplicaSet, Name: testReplicaSetName, Controller: boolPtr(true)}},
		},
		Status: corev1.PodStatus{ContainerStatuses: statuses},
	}
	return deploy, rs, pod
}

func boolPtr(b bool) *bool { return &b }

// --- Reconcile: namespace guard ----------------------------------------

func TestReconcile_NamespaceGuard_SkipsExcludedNamespaces(t *testing.T) {
	const podName = "crasher"
	for _, ns := range []string{"kube-system", "kube-node-lease", "local-path-storage"} {
		t.Run(ns, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns},
				Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{crashingContainerStatus("main", 5)}},
			}
			r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()}
			ctx, sink := newTestContext()

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: podName}})

			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			// The pod IS crash-looping — if the guard didn't fire first, this
			// would detect. Its absence proves the namespace check runs
			// before anything else, not just that this namespace is quiet.
			if sink.has(detectedMsg) {
				t.Errorf("namespace guard did not prevent detection in excluded namespace %q", ns)
			}
		})
	}
}

// --- Reconcile: detection logic -----------------------------------------

func TestReconcile_HealthyPod_NoDetection(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: testNamespace},
		Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{runningContainerStatus("main")}},
	}
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "healthy"}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if sink.has(detectedMsg) {
		t.Error("expected no detection for a healthy pod")
	}
}

func TestReconcile_MultiContainerLoop_DetectsNonFirstContainer(t *testing.T) {
	// Sidecar (index 0) is healthy; the actual app container (index 1) is
	// crash-looping. If Reconcile only inspected ContainerStatuses[0], as a
	// naive implementation might, this would be missed entirely.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: testNamespace},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			runningContainerStatus("sidecar"),
			crashingContainerStatus("app", 7),
		}},
	}
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "multi"}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	ev, ok := sink.event(detectedMsg)
	if !ok {
		t.Fatal("expected detection of the crash-looping second container")
	}
	if ev.ContainerName != "app" {
		t.Errorf("ContainerName = %q, want %q (must report the crashing container, not the pod as a whole)", ev.ContainerName, "app")
	}
	if ev.RestartCount != 7 {
		t.Errorf("RestartCount = %d, want 7 (must be the crashing container's count)", ev.RestartCount)
	}
}

func TestReconcile_ResolvesOwnerDeployment(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build()}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	ev, ok := sink.event(detectedMsg)
	if !ok {
		t.Fatal("expected detection")
	}
	if ev.OwnerDeployment != testDeploymentName {
		t.Errorf("OwnerDeployment = %q, want %q", ev.OwnerDeployment, testDeploymentName)
	}
	if ev.PodName != testPodName || ev.Namespace != testNamespace {
		t.Errorf("unexpected PodName/Namespace on event: %+v", ev)
	}
}

func TestReconcile_PodGone_NoError(t *testing.T) {
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	ctx, _ := newTestContext()

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "gone"}})

	if err != nil {
		t.Fatalf("expected NotFound to be swallowed via client.IgnoreNotFound, got %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("expected zero-value Result, got %+v", res)
	}
}

// --- Reconcile: incident admission integration ---------------------------

func TestReconcile_InFlightGuard_SkipsAlreadyRemediatingDeployment(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build()}
	// Simulate an attempt already running for this Deployment, exactly as if
	// an earlier reconcile for a sibling pod under the same Deployment had
	// started it.
	key := incidentKey(testNamespace, testDeploymentName)
	if _, decision := r.beginAttempt(key, 1, time.Now()); decision != admitProceed {
		t.Fatalf("setup: expected to claim a fresh incident, got %v", decision)
	}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !sink.has(skippedMsg) {
		t.Error("expected the in-flight guard to log a skip")
	}
	if !r.incidents[key].inFlight {
		t.Error("the claim belongs to the earlier attempt and must survive a skipped reconcile — only whoever started it may release it")
	}
}

// --- incident budget, backoff and cooldown (Week 3 Task 1/2) -------------

// drive runs one full attempt — claim, record, finish with the given outcome —
// and reports whether it was admitted. Returns false when the budget, backoff
// or cooldown blocked it.
func drive(r *PodReconciler, key string, generation int64, now time.Time, outcome string) (admission, int) {
	_, decision := r.beginAttempt(key, generation, now)
	if decision != admitProceed {
		return decision, 0
	}
	attempt := r.recordAttempt(key)
	r.endAttempt(key, outcome, now)
	return decision, attempt
}

func TestIncident_BudgetStopsAtMaxAttemptsAndGoesQuiet(t *testing.T) {
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	start := time.Now()

	// Three failed attempts, each after its backoff has elapsed.
	for i := 1; i <= MaxAttempts; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Minute)
		decision, attempt := drive(r, key, 1, at, string(safety.OutcomeRolledBack))
		if decision != admitProceed {
			t.Fatalf("attempt %d: expected admitProceed, got %v", i, decision)
		}
		if attempt != i {
			t.Fatalf("attempt %d: recorded attempt number %d", i, attempt)
		}
	}

	// The fourth must not run; it ends the incident instead.
	_, decision := r.beginAttempt(key, 1, start.Add(time.Hour))
	if decision != admitExhausted {
		t.Fatalf("after %d attempts expected admitExhausted, got %v", MaxAttempts, decision)
	}
	if got := r.incidents[key].terminalOutcome; got != OutcomeExhausted {
		t.Fatalf("terminal outcome = %q, want %q", got, OutcomeExhausted)
	}

	// Exhausted is sticky, and reported exactly once. This is the acceptance
	// test from the Week 3 plan: a permanently-broken Deployment must be
	// silent at minute 10, so the cooldown must NOT re-arm it.
	for _, after := range []time.Duration{time.Minute, 10 * time.Minute, 24 * time.Hour} {
		if _, d := r.beginAttempt(key, 1, start.Add(time.Hour+after)); d != admitSkip {
			t.Fatalf("%v after exhausting the budget: got %v, want admitSkip", after, d)
		}
	}
}

func TestIncident_BackoffBlocksAnImmediateRetry(t *testing.T) {
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	start := time.Now()

	drive(r, key, 1, start, string(safety.OutcomeRolledBack))

	if _, d := r.beginAttempt(key, 1, start.Add(AttemptBackoffBase-time.Second)); d != admitSkip {
		t.Fatal("a retry inside the backoff window must be skipped")
	}
	if _, d := r.beginAttempt(key, 1, start.Add(AttemptBackoffBase)); d != admitProceed {
		t.Fatal("a retry once the backoff has elapsed must be admitted")
	}
}

func TestIncident_BackoffIsExponential(t *testing.T) {
	// 30s after the first attempt, 60s after the second — the values written
	// into docs/measurement-definitions.md §5.
	for completed, want := range map[int]time.Duration{1: 30 * time.Second, 2: 60 * time.Second} {
		if got := attemptBackoff(completed); got != want {
			t.Errorf("attemptBackoff(%d) = %v, want %v", completed, got, want)
		}
	}
}

func TestIncident_RecoveredEndsIncident_RolledBackDoesNot(t *testing.T) {
	r := &PodReconciler{}
	start := time.Now()

	rolled := incidentKey("ns1", "rolled")
	drive(r, rolled, 1, start, string(safety.OutcomeRolledBack))
	if got := r.incidents[rolled].terminalOutcome; got != "" {
		t.Errorf("a rolled_back attempt must leave the incident active to retry, got terminal %q", got)
	}

	recovered := incidentKey("ns1", "recovered")
	drive(r, recovered, 1, start, OutcomeRecovered)
	if got := r.incidents[recovered].terminalOutcome; got != OutcomeRecovered {
		t.Errorf("terminal outcome = %q, want %q", got, OutcomeRecovered)
	}
}

func TestIncident_ErroredAttemptRetriesRatherThanHangingTheGuard(t *testing.T) {
	// Remediate() returning an error leaves the outcome empty. That must
	// release the claim (otherwise the Deployment is blocked forever) without
	// ending the incident (the attempt genuinely failed and should retry).
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	start := time.Now()

	drive(r, key, 1, start, "")

	record := r.incidents[key]
	if record.inFlight {
		t.Fatal("an errored attempt must release the in-flight claim")
	}
	if record.terminalOutcome != "" {
		t.Fatalf("an errored attempt must not end the incident, got %q", record.terminalOutcome)
	}
	if _, d := r.beginAttempt(key, 1, start.Add(AttemptBackoffBase)); d != admitProceed {
		t.Fatal("the next attempt must be admitted once the backoff has elapsed")
	}
}

func TestIncident_CooldownExpiresForRecoveredButNeverForExhausted(t *testing.T) {
	start := time.Now()

	r := &PodReconciler{}
	recovered := incidentKey("ns1", "recovered")
	drive(r, recovered, 1, start, OutcomeRecovered)
	if _, d := r.beginAttempt(recovered, 1, start.Add(CooldownPeriod-time.Second)); d != admitSkip {
		t.Error("a recovered deployment must stay quiet for the cooldown")
	}
	if _, d := r.beginAttempt(recovered, 1, start.Add(CooldownPeriod)); d != admitProceed {
		t.Error("once the cooldown elapses, a fresh crash loop starts a new incident")
	}
}

func TestIncident_GenerationChangeClearsAnExhaustedIncident(t *testing.T) {
	// A changed generation means a human or a new rollout intervened, so the
	// thing we gave up on is no longer the thing running.
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	start := time.Now()

	for i := 1; i <= MaxAttempts; i++ {
		drive(r, key, 7, start.Add(time.Duration(i)*10*time.Minute), string(safety.OutcomeRolledBack))
	}
	if _, d := r.beginAttempt(key, 7, start.Add(time.Hour)); d != admitExhausted {
		t.Fatal("setup: expected the budget to be spent")
	}
	if _, d := r.beginAttempt(key, 7, start.Add(2*time.Hour)); d != admitSkip {
		t.Fatal("setup: expected exhausted to be sticky at the same generation")
	}

	_, decision := r.beginAttempt(key, 8, start.Add(2*time.Hour))
	if decision != admitProceed {
		t.Fatalf("a generation change must resurrect the deployment, got %v", decision)
	}
	if got := r.incidents[key].attemptCount; got != 0 {
		t.Errorf("the new incident must start with a fresh budget, got attemptCount %d", got)
	}
}

func TestIncident_EscalationConsumesNoAttemptBudget(t *testing.T) {
	// escalated and rejected are terminal at attempt 0 — no snapshot is taken
	// and no action runs, so they must not spend budget
	// (docs/measurement-definitions.md §2).
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	now := time.Now()

	if _, d := r.beginAttempt(key, 1, now); d != admitProceed {
		t.Fatal("setup: expected a fresh incident to be admitted")
	}
	r.endAttempt(key, OutcomeEscalated, now)

	record := r.incidents[key]
	if record.attemptCount != 0 {
		t.Errorf("attemptCount = %d, want 0 — recordAttempt is only called once an action is dispatched", record.attemptCount)
	}
	if record.terminalOutcome != OutcomeEscalated {
		t.Errorf("terminal outcome = %q, want %q", record.terminalOutcome, OutcomeEscalated)
	}
}

func TestIncident_KeyedByNamespaceAndDeployment_NotPodUID(t *testing.T) {
	// The locked design explicitly rejects a UID-keyed record: RestartPod
	// deletes the pod and the ReplicaSet controller recreates it with a new
	// UID, so a UID key would fail to recognize the replacement re-crashing
	// as the same incident. This pins the key format itself.
	if got, want := incidentKey("ns", "dep"), "ns/dep"; got != want {
		t.Errorf("incidentKey(%q, %q) = %q, want %q", "ns", "dep", got, want)
	}
}

func TestIncident_DistinctDeploymentsDoNotBlockEachOther(t *testing.T) {
	r := &PodReconciler{}
	now := time.Now()

	if _, d := r.beginAttempt(incidentKey("ns1", "dep1"), 1, now); d != admitProceed {
		t.Fatal("first deployment must be admitted")
	}
	if _, d := r.beginAttempt(incidentKey("ns1", "dep2"), 1, now); d != admitProceed {
		t.Fatal("an unrelated deployment must not be blocked by another's in-flight attempt")
	}
	if _, d := r.beginAttempt(incidentKey("ns1", "dep1"), 1, now); d != admitSkip {
		t.Fatal("the same deployment must be blocked while its attempt is in flight")
	}
}

// --- frozen evidence (Week 3 Task 3) -------------------------------------

func TestIncident_EvidenceIsCollectedOncePerIncident(t *testing.T) {
	// Attempt 2 must be classified against the evidence captured at detection,
	// not against evidence re-collected after attempt 1 — by then the 25-event
	// window is full of the fallout from our own remediation and the original
	// cause has been pushed out of it.
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	fetcher := &stubLogFetcher{previous: "freshly collected"}
	spy := &capturingClassifier{outcome: classifier.ClassificationOutcome{Proposal: escalateProposal()}}
	r := &PodReconciler{
		Client:     fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		Classifier: spy,
		Evidence:   collectorWith(fetcher, nil),
	}

	// Stand in for attempt 1 having already run and rolled back: the bundle is
	// frozen, the claim released, and the incident still active. attemptCount
	// stays 0 because no action was dispatched, so no backoff applies.
	key := incidentKey(testNamespace, testDeploymentName)
	if _, decision := r.beginAttempt(key, 1, time.Now()); decision != admitProceed {
		t.Fatalf("setup: expected a fresh incident to be admitted, got %v", decision)
	}
	r.freezeEvidence(key, "the original crash log", []string{"the original event"})
	r.endAttempt(key, string(safety.OutcomeRolledBack), time.Now())

	ctx, _ := newTestContext()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(fetcher.calls) != 0 {
		t.Errorf("evidence was re-collected on a later attempt (%d log reads); it must be frozen at detection", len(fetcher.calls))
	}
	if got := spy.captured(); got.Logs != "the original crash log" {
		t.Errorf("classifier received Logs = %q, want the frozen bundle", got.Logs)
	}
}

func TestIncident_EvidenceIsCapturedOnTheFirstAttempt(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	r := &PodReconciler{
		Client:     fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		Classifier: &capturingClassifier{outcome: classifier.ClassificationOutcome{Proposal: escalateProposal()}},
		Evidence:   collectorWith(&stubLogFetcher{previous: "connection refused"}, nil),
	}
	ctx, _ := newTestContext()

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	logs, _, frozen := r.incidentEvidence(incidentKey(testNamespace, testDeploymentName))
	if !frozen {
		t.Fatal("the first attempt must freeze its evidence bundle onto the incident")
	}
	if logs != "connection refused" {
		t.Errorf("frozen logs = %q, want the collected crash log", logs)
	}
}

func TestIncident_FrozenEvidenceCannotBeMutatedByACaller(t *testing.T) {
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	r.beginAttempt(key, 1, time.Now())

	original := []string{"cause"}
	r.freezeEvidence(key, "log", original)
	original[0] = "mutated by the producer"

	_, events, _ := r.incidentEvidence(key)
	events[0] = "mutated by the consumer"

	_, again, _ := r.incidentEvidence(key)
	if again[0] != "cause" {
		t.Errorf("frozen events = %q; the bundle every later attempt is classified against must be isolated from both sides", again[0])
	}
}

func TestIncident_NewIncidentCollectsFreshEvidence(t *testing.T) {
	// The bundle lives on the record, so a generation change — which drops the
	// record — must also drop the frozen evidence. Otherwise a Deployment that
	// a human has since fixed is classified against the old fault.
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	start := time.Now()

	for i := 1; i <= MaxAttempts; i++ {
		drive(r, key, 7, start.Add(time.Duration(i)*10*time.Minute), string(safety.OutcomeRolledBack))
	}
	r.freezeEvidence(key, "stale cause", nil)
	r.beginAttempt(key, 7, start.Add(time.Hour)) // exhausts the budget

	if _, decision := r.beginAttempt(key, 8, start.Add(2*time.Hour)); decision != admitProceed {
		t.Fatalf("setup: a generation change must start a new incident, got %v", decision)
	}
	if _, _, frozen := r.incidentEvidence(key); frozen {
		t.Error("a new incident must collect fresh evidence, not inherit the previous incident's bundle")
	}
}

func TestIncident_BackoffRunsFromAttemptEndNotAttemptStart(t *testing.T) {
	// An attempt occupies about 30s of wall clock — the verifier's readiness
	// timeout — and measuring backoff from its start lets that duration eat
	// the backoff. The first deployed run showed a 12s gap between attempts
	// where the definitions document promises 30s.
	r := &PodReconciler{}
	key := incidentKey("ns1", "dep1")
	started := time.Now()
	finished := started.Add(30 * time.Second) // a realistic attempt duration

	r.beginAttempt(key, 1, started)
	r.recordAttempt(key)
	r.endAttempt(key, string(safety.OutcomeRolledBack), finished)

	// Measured from the start, 30s of backoff would already have elapsed here.
	if _, d := r.beginAttempt(key, 1, finished.Add(AttemptBackoffBase-time.Second)); d != admitSkip {
		t.Error("backoff must run from when the attempt finished, not from when it started")
	}
	if _, d := r.beginAttempt(key, 1, finished.Add(AttemptBackoffBase)); d != admitProceed {
		t.Error("the next attempt must be admitted once a full backoff has elapsed since the previous one finished")
	}
}

// --- terminal incident audit lines (Week 3, Owner 3 metrics adapter) ------

func TestIncident_EscalationWritesATerminalLoggedLine(t *testing.T) {
	// escalated is decided before Remediate() runs, so Owner 2's Service
	// never writes anything for it. Without this LOGGED line the metrics
	// adapter cannot see the incident at all.
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	audit := &stubAuditWriter{}
	r := &PodReconciler{
		Client:        fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		Classifier:    &capturingClassifier{outcome: classifier.ClassificationOutcome{Proposal: escalateProposal()}},
		Audit:         audit,
		AuditMetadata: safety.AuditMetadata{Workload: "W2", ArmLabel: "enabled"},
	}
	ctx, _ := newTestContext()

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	terminal := audit.terminalLines()
	if len(terminal) != 1 {
		t.Fatalf("expected exactly one terminal LOGGED line, got %d", len(terminal))
	}
	if terminal[0].Result != OutcomeEscalated {
		t.Errorf("result = %q, want %q", terminal[0].Result, OutcomeEscalated)
	}
	if terminal[0].AttemptNumber != 0 {
		t.Errorf("attemptNumber = %d, want 0 — escalation consumes no budget", terminal[0].AttemptNumber)
	}
	if terminal[0].IncidentID == "" {
		t.Error("the terminal line must carry an incidentID; the adapter groups on it")
	}
	if terminal[0].State != safety.StateLogged || terminal[0].Action != "" {
		t.Errorf("state/action = %q/%q, want LOGGED with no dispatched action", terminal[0].State, terminal[0].Action)
	}
	if terminal[0].Workload != "W2" || terminal[0].ArmLabel != "enabled" {
		t.Errorf("terminal metadata = %q/%q, want W2/enabled", terminal[0].Workload, terminal[0].ArmLabel)
	}
}

func TestIncident_ExhaustionWritesATerminalLoggedLine(t *testing.T) {
	audit := &stubAuditWriter{}
	r := &PodReconciler{
		Audit:         audit,
		AuditMetadata: safety.AuditMetadata{Workload: "W3", ArmLabel: "enabled"},
	}
	key := incidentKey("ns1", "dep1")
	start := time.Now()

	for i := 1; i <= MaxAttempts; i++ {
		drive(r, key, 1, start.Add(time.Duration(i)*10*time.Minute), string(safety.OutcomeRolledBack))
	}
	if len(audit.terminalLines()) != 0 {
		t.Fatal("a rolled_back attempt with budget left must not close the incident")
	}

	ctx, _ := newTestContext()
	if _, decision := r.beginAttempt(key, 1, start.Add(time.Hour)); decision != admitExhausted {
		t.Fatalf("setup: expected the budget to be spent")
	}
	r.closeIncidentIfTerminal(ctx, key, "ns1/pod-x")

	terminal := audit.terminalLines()
	if len(terminal) != 1 {
		t.Fatalf("expected exactly one terminal LOGGED line, got %d", len(terminal))
	}
	if terminal[0].Result != OutcomeExhausted {
		t.Errorf("result = %q, want %q", terminal[0].Result, OutcomeExhausted)
	}
	if terminal[0].AttemptNumber != MaxAttempts {
		t.Errorf("attemptNumber = %d, want %d", terminal[0].AttemptNumber, MaxAttempts)
	}
	if terminal[0].Workload != "W3" || terminal[0].ArmLabel != "enabled" {
		t.Errorf("terminal metadata = %q/%q, want W3/enabled", terminal[0].Workload, terminal[0].ArmLabel)
	}
}
