package controller

import (
	"context"
	"sync"
	"testing"
	"time"

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

// waitForGuardCleared polls until the in-flight guard for
// testNamespace/testDeploymentName clears. Every test in this file builds
// its pod via ownedPod, so that's the only key in play — not parameters.
func waitForGuardCleared(t *testing.T, r *PodReconciler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, stillInFlight := r.inFlight.Load(inFlightKey(testNamespace, testDeploymentName)); !stillInFlight {
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
	r := &PodReconciler{
		Client:     fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build(),
		Classifier: stubIncidentClassifier{outcome: classifier.ClassificationOutcome{Proposal: automateProposal()}},
		Actions:    map[string]safety.RemediationAction{}, // nothing registered for "restart_pod"
	}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !sink.has("no matching implementation") {
		t.Error("expected a log about the missing action, escalating instead of panicking or automating blind")
	}
	waitForGuardCleared(t, r)
}

// --- Task 4: action selection + dispatch into Remediate() ----------------

func TestReconcile_DispatchesRemediation_WhenSafeForAutomation(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	action := &stubRemediationAction{name: classifier.ActionRestartPod, called: make(chan contracts.DetectionEvent, 1)}
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

var errExecuteFailed = &stubExecuteError{"stub action execution failed"}

type stubExecuteError struct{ msg string }

func (e *stubExecuteError) Error() string { return e.msg }
