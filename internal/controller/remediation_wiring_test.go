package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aryausingh/k8s-selfheal/internal/classifier"
	"github.com/aryausingh/k8s-selfheal/internal/contracts"
	"github.com/aryausingh/k8s-selfheal/internal/safety"
)

// --- test doubles for the classifier and safety seams -------------------
//
// These are Reconcile's collaborators, not the collaborators' own tests —
// Subhashini's classifier and Ananya's safety.Service already have their
// own unit tests in their own packages. What's tested here is the wiring:
// does Reconcile call ShouldEscalate on the right value, select the right
// action, and dispatch it correctly.

// stubIncidentClassifier returns a fixed ClassificationOutcome, bypassing
// the real classify/validate/fallback pipeline so tests can pin an exact
// Proposal without needing to satisfy the semantic guard's evidence rules.
type stubIncidentClassifier struct {
	outcome classifier.ClassificationOutcome
}

func (s stubIncidentClassifier) ClassifyIncident(context.Context, classifier.IncidentInput) classifier.ClassificationOutcome {
	return s.outcome
}

// stubRemediationAction records the event it was called with on a channel,
// so an async goroutine dispatch can be observed synchronously in a test.
type stubRemediationAction struct {
	name   string
	called chan contracts.DetectionEvent
	err    error
}

func (s *stubRemediationAction) Name() string { return s.name }

func (s *stubRemediationAction) Execute(_ context.Context, event contracts.DetectionEvent) error {
	if s.called != nil {
		s.called <- event
	}
	return s.err
}

type stubSnapshotStore struct{}

func (stubSnapshotStore) Capture(_ context.Context, ref types.NamespacedName) (safety.DeploymentSnapshot, error) {
	return safety.DeploymentSnapshot{Name: ref.Name, Namespace: ref.Namespace}, nil
}

func (stubSnapshotStore) Restore(context.Context, safety.DeploymentSnapshot) error { return nil }

// stubVerifier reports Recovered immediately, so Remediate() in these tests
// finishes in-process rather than actually polling for 30-90s.
type stubVerifier struct{ recovered bool }

func (v stubVerifier) CapturePreActionPodUIDs(
	context.Context,
	safety.VerificationTarget,
) (safety.PodUIDSet, error) {
	return safety.PodUIDSet{types.UID("original-uid"): {}}, nil
}

func (v stubVerifier) Verify(context.Context, safety.VerificationTarget) (safety.VerificationResult, error) {
	return safety.VerificationResult{Recovered: v.recovered}, nil
}

type stubAuditWriter struct {
	mu      sync.Mutex
	entries []safety.AuditEntry
}

func (w *stubAuditWriter) Append(entry safety.AuditEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
	return nil
}

// terminalLines returns incident-terminal LOGGED entries. A rolled_back entry
// is an attempt outcome and therefore is deliberately excluded.
func (w *stubAuditWriter) terminalLines() []safety.AuditEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	var terminal []safety.AuditEntry
	for _, entry := range w.entries {
		if entry.State == safety.StateLogged && entry.Result != string(safety.OutcomeRolledBack) {
			terminal = append(terminal, entry)
		}
	}
	return terminal
}

func (w *stubAuditWriter) snapshot() []safety.AuditEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]safety.AuditEntry(nil), w.entries...)
}

// automateProposal builds a Proposal that passes Subhashini's validator for
// an automatable restart_pod recommendation targeting testPodName.
func automateProposal() classifier.Proposal {
	return classifier.Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: classifier.ActionRestartPod,
		Target:            classifier.Target{Kind: "Pod", Namespace: testNamespace, Name: testPodName},
		SafeForAutomation: true,
		Reasoning:         "test: connection refused evidence",
	}
}

// escalateProposal builds a Proposal representing "not safe for automation".
func escalateProposal() classifier.Proposal {
	return classifier.Proposal{
		SubCause:          "unknown",
		RecommendedAction: classifier.ActionEscalateToHuman,
		Target:            classifier.Target{Kind: "Pod", Namespace: testNamespace, Name: testPodName},
		SafeForAutomation: false,
		Reasoning:         "test: no supporting evidence",
	}
}

// waitForGuardCleared polls until the in-flight claim for
// testNamespace/testDeploymentName is released. Every test in this file builds
// its pod via ownedPod, so that's the only key in play — not parameters.
func waitForGuardCleared(t *testing.T, r *PodReconciler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !r.claimHeld(incidentKey(testNamespace, testDeploymentName)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("in-flight guard was never released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- Task 6: classify + ShouldEscalate gate ------------------------------

const escalatedMsg = "ESCALATED — not safe for automation"

func TestReconcile_NilClassifier_EscalatesByDefaultAndReleasesGuard(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build()}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !sink.has("cannot classify incident") {
		t.Error("expected an error log about the missing classifier")
	}
	waitForGuardCleared(t, r)
}

func TestReconcile_Escalates_WhenNotSafeForAutomation(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	r := &PodReconciler{
		Client:     fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		Classifier: stubIncidentClassifier{outcome: classifier.ClassificationOutcome{Proposal: escalateProposal()}},
	}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !sink.has(escalatedMsg) {
		t.Error("expected the escalation gate to fire and log it")
	}
	waitForGuardCleared(t, r)
}

func TestReconcile_Escalates_WhenNoMatchingActionRegistered(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	audit := &stubAuditWriter{}
	r := &PodReconciler{
		Client:        fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		Classifier:    stubIncidentClassifier{outcome: classifier.ClassificationOutcome{Proposal: automateProposal()}},
		Actions:       map[string]safety.RemediationAction{}, // nothing registered for "restart_pod"
		Audit:         audit,
		AuditMetadata: safety.AuditMetadata{Workload: "W2", ArmLabel: "enabled"},
	}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !sink.has("no matching implementation") {
		t.Error("expected a log about the missing action, escalating instead of panicking or automating blind")
	}
	terminal := audit.terminalLines()
	if len(terminal) != 1 || terminal[0].Result != OutcomeRejected {
		t.Fatalf("terminal audit = %+v, want one rejected LOGGED entry", terminal)
	}
	if terminal[0].AttemptNumber != 0 || terminal[0].Action != "" {
		t.Errorf("rejected terminal attempt/action = %d/%q, want 0/empty", terminal[0].AttemptNumber, terminal[0].Action)
	}
	if terminal[0].Workload != "W2" || terminal[0].ArmLabel != "enabled" {
		t.Errorf("rejected terminal metadata = %q/%q, want W2/enabled", terminal[0].Workload, terminal[0].ArmLabel)
	}
	waitForGuardCleared(t, r)
}

// --- Task 4: action selection + dispatch into Remediate() ----------------

func TestReconcile_DispatchesRemediation_WhenSafeForAutomation(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	action := &stubRemediationAction{name: classifier.ActionRestartPod, called: make(chan contracts.DetectionEvent, 1)}
	audit := &stubAuditWriter{}
	r := &PodReconciler{
		Client:        fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		ManagerCtx:    context.Background(),
		Classifier:    stubIncidentClassifier{outcome: classifier.ClassificationOutcome{Proposal: automateProposal()}},
		Actions:       map[string]safety.RemediationAction{classifier.ActionRestartPod: action},
		Snapshots:     stubSnapshotStore{},
		Verifier:      stubVerifier{recovered: true},
		Audit:         audit,
		Clock:         safety.RealClock{},
		AuditMetadata: safety.AuditMetadata{Workload: "W2", ArmLabel: "enabled"},
	}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Drain the dispatched goroutine's signal before inspecting sink or the
	// guard — Remediate() runs concurrently with the rest of this test, so
	// checking either one before this point would race the goroutine still
	// writing to them.
	select {
	case event := <-action.called:
		if event.PodName != testPodName || event.Namespace != testNamespace {
			t.Errorf("action executed with unexpected event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected the injected restart_pod action to be executed via the dispatched goroutine")
	}

	waitForGuardCleared(t, r)
	for _, entry := range audit.snapshot() {
		if entry.Workload != "W2" || entry.ArmLabel != "enabled" {
			t.Fatalf("audit metadata = %q/%q, want W2/enabled", entry.Workload, entry.ArmLabel)
		}
	}
	terminal := audit.terminalLines()
	if len(terminal) != 1 || terminal[0].Result != string(safety.OutcomeRecovered) {
		t.Fatalf("terminal audit = %+v, want exactly the Service's recovered LOGGED entry", terminal)
	}
	if sink.has(escalatedMsg) {
		t.Error("a safe-for-automation proposal must not be escalated")
	}
}

func TestReconcile_RemediationFailure_StillReleasesGuard(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	action := &stubRemediationAction{
		name:   classifier.ActionRestartPod,
		called: make(chan contracts.DetectionEvent, 1),
		err:    errExecuteFailed,
	}
	r := &PodReconciler{
		Client:     fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		ManagerCtx: context.Background(),
		Classifier: stubIncidentClassifier{outcome: classifier.ClassificationOutcome{Proposal: automateProposal()}},
		Actions:    map[string]safety.RemediationAction{classifier.ActionRestartPod: action},
		Snapshots:  stubSnapshotStore{},
		Verifier:   stubVerifier{recovered: true},
		Audit:      &stubAuditWriter{},
		Clock:      safety.RealClock{},
	}
	ctx, _ := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case <-action.called:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the injected action to be executed")
	}

	// Even though the action failed, finishRemediation must still run (it's
	// deferred) — otherwise this Deployment would be permanently stuck
	// in-flight after any single failed remediation.
	waitForGuardCleared(t, r)
}

// --- evidence reaches the classifier -------------------------------------

// capturingClassifier records the IncidentInput it was handed, so a test can
// assert on the evidence Reconcile assembled rather than only on the decision
// that came out the other end.
type capturingClassifier struct {
	mu      sync.Mutex
	input   classifier.IncidentInput
	outcome classifier.ClassificationOutcome
}

func (c *capturingClassifier) ClassifyIncident(_ context.Context, input classifier.IncidentInput) classifier.ClassificationOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = input
	return c.outcome
}

func (c *capturingClassifier) captured() classifier.IncidentInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.input
}

func TestReconcile_PassesCollectedEvidenceToTheClassifier(t *testing.T) {
	// The gap this closes: without Logs/Events populated, the semantic guard
	// rejects every sub-cause but "unknown" and nothing is ever remediated.
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	spy := &capturingClassifier{outcome: classifier.ClassificationOutcome{Proposal: escalateProposal()}}
	r := &PodReconciler{
		Client:     fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		Classifier: spy,
		Evidence: collectorWith(
			&stubLogFetcher{previous: "dial tcp 10.0.0.5:5432: connect: connection refused"},
			&stubEventLister{byObject: map[string][]corev1.Event{
				testDeploymentName: {testEvent("e1", "ScalingReplicaSet", "Scaled up replica set "+testReplicaSetName+" to 1", testDeploymentName, 10*time.Second)},
			}},
		),
	}
	ctx, sink := newTestContext()

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	got := spy.captured()
	if !strings.Contains(got.Logs, "connection refused") {
		t.Errorf("classifier received Logs = %q, want the collected crash log", got.Logs)
	}
	if len(got.Events) == 0 || !strings.Contains(strings.Join(got.Events, "\n"), "Scaled up replica set") {
		t.Errorf("classifier received Events = %v, want the collected rollout evidence", got.Events)
	}
	if !sink.has("Collected incident evidence") {
		t.Error("evidence collection should be observable in the logs")
	}
	waitForGuardCleared(t, r)
}

// --- ownerless pods ------------------------------------------------------

func TestReconcile_NoOwnerDeployment_EscalatesWithoutTakingTheGuard(t *testing.T) {
	// Every downstream stage needs the Deployment: Remediate() rejects an
	// event without one, rollback has nothing to snapshot, and the guard key
	// would collapse to "namespace/" for every ownerless pod in the namespace.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: testNamespace},
		Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{crashingContainerStatus("main", 4)}},
	}
	action := &stubRemediationAction{name: classifier.ActionRestartPod, called: make(chan contracts.DetectionEvent, 1)}
	r := &PodReconciler{
		Client:     fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build(),
		ManagerCtx: context.Background(),
		// Would automate if it got that far — proving the ownerless check,
		// not a passive absence of any decision.
		Classifier: stubIncidentClassifier{outcome: classifier.ClassificationOutcome{Proposal: automateProposal()}},
		Actions:    map[string]safety.RemediationAction{classifier.ActionRestartPod: action},
		Snapshots:  stubSnapshotStore{},
		Verifier:   stubVerifier{recovered: true},
		Audit:      &stubAuditWriter{},
		Clock:      safety.RealClock{},
	}
	ctx, sink := newTestContext()

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "bare"}}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !sink.has("ESCALATED — no owner Deployment resolved") {
		t.Error("expected an ownerless pod to escalate")
	}
	if r.claimHeld(incidentKey(testNamespace, "")) {
		t.Error("no incident may be opened for an ownerless pod — the empty key would block every other ownerless pod in the namespace")
	}
	select {
	case <-action.called:
		t.Error("no remediation may be dispatched for a pod with no owning Deployment")
	case <-time.After(100 * time.Millisecond):
	}
}

var errExecuteFailed = &stubExecuteError{"stub action execution failed"}

type stubExecuteError struct{ msg string }

func (e *stubExecuteError) Error() string { return e.msg }
