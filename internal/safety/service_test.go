//nolint:goconst // Repeated literals keep each lifecycle fixture self-contained.
package safety

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type trackingSnapshotStore struct {
	captured bool
	restored bool
	captures int
	restores int
	err      error
}

func (s *trackingSnapshotStore) Capture(context.Context, types.NamespacedName) (DeploymentSnapshot, error) {
	if s.err != nil {
		return DeploymentSnapshot{}, s.err
	}
	s.captured = true
	s.captures++
	return DeploymentSnapshot{Name: "checkout", Namespace: "shop"}, nil
}

func (s *trackingSnapshotStore) Restore(context.Context, DeploymentSnapshot) error {
	s.restored = true
	s.restores++
	return nil
}

// FAKE REMEDIATION ACTION
type checkingAction struct {
	store                 *trackingSnapshotStore
	preActionUIDsCaptured func() bool
	called                bool
	calls                 int
	name                  string
	execErr               error
	onExecute             func()
}

func (a *checkingAction) Name() string {
	return a.name
}

func (a *checkingAction) Execute(context.Context, DetectionEvent) error {
	if !a.store.captured {
		return fmt.Errorf("action ran before snapshot")
	}
	if a.preActionUIDsCaptured != nil && !a.preActionUIDsCaptured() {
		return fmt.Errorf("action ran before pre-action pod UIDs were captured")
	}
	a.called = true
	a.calls++
	if a.onExecute != nil {
		a.onExecute()
	}
	return a.execErr
}

type orderingVerifier struct {
	captured bool
}

func (v *orderingVerifier) CapturePreActionPodUIDs(
	context.Context,
	VerificationTarget,
) (PodUIDSet, error) {
	v.captured = true
	return PodUIDSet{types.UID("original-uid"): {}}, nil
}

func (v *orderingVerifier) Verify(
	context.Context,
	VerificationTarget,
) (VerificationResult, error) {
	return VerificationResult{Recovered: true}, nil
}

func TestRemediateCapturesPreActionPodUIDsBeforeAction(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	store := &trackingSnapshotStore{}
	verifier := &orderingVerifier{}
	action := &checkingAction{
		store: store,
		name:  "injected-owner1-action",
		preActionUIDsCaptured: func() bool {
			return verifier.captured
		},
	}
	service := &Service{
		Snapshots: store,
		Verifier:  verifier,
		Action:    action,
		Audit:     NewJSONLAuditWriter(&bytes.Buffer{}),
		Clock:     clock,
	}

	_, err := service.Remediate(context.Background(), DetectionEvent{
		PodName:         "checkout-pod",
		Namespace:       "shop",
		ContainerName:   "app",
		RestartCount:    1,
		OwnerDeployment: "checkout",
		Timestamp:       clock.Now(),
		IncidentID:      "incident-ordering",
		AttemptNumber:   1,
	})
	if err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}
	if !verifier.captured || !action.called {
		t.Fatalf("captured/action = %t/%t, want true/true", verifier.captured, action.called)
	}
}

func TestAuditTransitionTimestampsExposeApplyDuration(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	store := &trackingSnapshotStore{}
	verifier := &orderingVerifier{}
	action := &checkingAction{
		store:     store,
		name:      "restart_pod",
		onExecute: func() { clock.Advance(3 * time.Second) },
	}
	service := &Service{
		Snapshots: store,
		Verifier:  verifier,
		Action:    action,
		Audit:     NewJSONLAuditWriter(&bytes.Buffer{}),
		Clock:     clock,
		Metadata:  AuditMetadata{Workload: "W1", ArmLabel: "enabled"},
	}

	outcome, err := service.Remediate(context.Background(), DetectionEvent{
		PodName:         "checkout-pod",
		Namespace:       "shop",
		ContainerName:   "app",
		RestartCount:    1,
		OwnerDeployment: "checkout",
		Timestamp:       clock.Now(),
		IncidentID:      "incident-apply-timing",
		AttemptNumber:   1,
	})
	if err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}

	var remediatingAt, verifyingAt time.Time
	for _, entry := range outcome.AuditEntries {
		switch entry.State {
		case StateRemediating:
			remediatingAt = entry.Timestamp
		case StateVerifying:
			verifyingAt = entry.Timestamp
		}
	}
	if got := verifyingAt.Sub(remediatingAt); got != 3*time.Second {
		t.Fatalf("VERIFYING - REMEDIATING = %s, want 3s", got)
	}
}

func TestRemediateSecond45CrashFiresRollback(t *testing.T) {
	observations := make([]*corev1.Pod, 0, 10)
	for range 8 {
		observations = append(observations, readyPod(3))
	}
	observations = append(observations, notReadyPod(4))

	clock := &fakeClock{now: time.Unix(100, 0)}
	verifier := &Verifier{
		Reader:           &sequenceReader{pods: observations},
		Resolver:         &sequenceResolver{pods: []*corev1.Pod{readyPod(3)}},
		Clock:            clock,
		ReadinessTimeout: InitialReadinessTimeout,
		Window:           StabilityWindow,
		PollInterval:     DefaultPollInterval,
	}
	store := &trackingSnapshotStore{}
	action := &checkingAction{store: store, name: "injected-owner1-action"}
	var auditOutput bytes.Buffer
	service := &Service{
		Snapshots: store,
		Verifier:  verifier,
		Action:    action,
		Audit:     NewJSONLAuditWriter(&auditOutput),
		Clock:     clock,
		Metadata:  AuditMetadata{Workload: "W3", ArmLabel: "enabled"},
	}
	// DETECTION EVENT DATA
	event := DetectionEvent{
		PodName:         "checkout-pod",
		Namespace:       "shop",
		ContainerName:   "app",
		RestartCount:    3,
		OwnerDeployment: "checkout",
		Timestamp:       clock.Now(),
		IncidentID:      "incident-second-45",
		AttemptNumber:   1,
	}

	outcome, err := service.Remediate(context.Background(), event)
	if err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}
	if outcome.Result != OutcomeRolledBack {
		t.Fatalf("Result = %q, want %q", outcome.Result, OutcomeRolledBack)
	}
	if outcome.MTTR != 45*time.Second {
		t.Fatalf("MTTR = %s, want 45s in fake timeline", outcome.MTTR)
	}
	if !action.called {
		t.Fatal("injected remediation action was not called")
	}
	if !store.restored {
		t.Fatal("rollback did not restore the snapshot")
	}

	wantStates := []State{
		StateDetected,
		StateSnapshotted,
		StateRemediating,
		StateVerifying,
		StateRollingBack,
		StateRolledBack,
		StateLogged,
	}
	gotStates := make([]State, 0, len(outcome.AuditEntries))
	for _, entry := range outcome.AuditEntries {
		gotStates = append(gotStates, entry.State)
		if entry.IncidentID != event.IncidentID || entry.AttemptNumber != event.AttemptNumber {
			t.Fatalf("audit identity = %q/%d, want %q/%d", entry.IncidentID, entry.AttemptNumber, event.IncidentID, event.AttemptNumber)
		}
		if entry.Workload != "W3" || entry.ArmLabel != "enabled" {
			t.Fatalf("audit experiment labels = %q/%q, want W3/enabled", entry.Workload, entry.ArmLabel)
		}
	}
	if !reflect.DeepEqual(gotStates, wantStates) {
		t.Fatalf("audit states = %v, want %v", gotStates, wantStates)
	}
	if auditOutput.Len() == 0 {
		t.Fatal("JSON Lines audit output is empty")
	}
}

func TestRemediateDoesNotRunActionWhenSnapshotFails(t *testing.T) {
	store := &trackingSnapshotStore{err: fmt.Errorf("capture failed")}
	action := &checkingAction{store: store, name: "injected-owner1-action"}
	clock := &fakeClock{now: time.Unix(100, 0)}
	service := &Service{
		Snapshots: store,
		Verifier: &Verifier{
			Reader:           &sequenceReader{pods: []*corev1.Pod{readyPod(1)}},
			Resolver:         &sequenceResolver{pods: []*corev1.Pod{readyPod(1)}},
			Clock:            clock,
			ReadinessTimeout: InitialReadinessTimeout,
			Window:           StabilityWindow,
			PollInterval:     DefaultPollInterval,
		},
		Action: action,
		Audit:  NewJSONLAuditWriter(&bytes.Buffer{}),
		Clock:  clock,
	}

	_, err := service.Remediate(context.Background(), DetectionEvent{
		PodName:         "checkout-pod",
		Namespace:       "shop",
		ContainerName:   "app",
		RestartCount:    1,
		OwnerDeployment: "checkout",
		Timestamp:       clock.Now(),
		IncidentID:      "incident-snapshot-failure",
		AttemptNumber:   1,
	})
	if err == nil {
		t.Fatal("Remediate() succeeded after snapshot failure")
	}
	if action.called {
		t.Fatal("action ran despite snapshot failure")
	}
}

type fixedVerifier struct {
	result VerificationResult
}

func (v fixedVerifier) CapturePreActionPodUIDs(
	context.Context,
	VerificationTarget,
) (PodUIDSet, error) {
	return PodUIDSet{types.UID("original-uid"): {}}, nil
}

func (v fixedVerifier) Verify(context.Context, VerificationTarget) (VerificationResult, error) {
	return v.result, nil
}

func TestRemediateReturnsRecoveredOutcome(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	store := &trackingSnapshotStore{}
	action := &checkingAction{store: store, name: "injected-owner1-action"}
	service := &Service{
		Snapshots: store,
		Verifier:  fixedVerifier{result: VerificationResult{Recovered: true}},
		Action:    action,
		Audit:     NewJSONLAuditWriter(&bytes.Buffer{}),
		Clock:     clock,
	}

	outcome, err := service.Remediate(context.Background(), DetectionEvent{
		PodName:         "checkout-pod",
		Namespace:       "shop",
		ContainerName:   "app",
		RestartCount:    1,
		OwnerDeployment: "checkout",
		Timestamp:       clock.Now(),
		IncidentID:      "incident-recovered",
		AttemptNumber:   1,
	})
	if err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}
	if outcome.Result != OutcomeRecovered {
		t.Fatalf("Result = %q, want %q", outcome.Result, OutcomeRecovered)
	}
	if store.restored {
		t.Fatal("recovery path unexpectedly restored snapshot")
	}
}

type queuedVerifier struct {
	results []VerificationResult
}

func (v *queuedVerifier) CapturePreActionPodUIDs(
	context.Context,
	VerificationTarget,
) (PodUIDSet, error) {
	return PodUIDSet{types.UID("original-uid"): {}}, nil
}

func (v *queuedVerifier) Verify(context.Context, VerificationTarget) (VerificationResult, error) {
	if len(v.results) == 0 {
		return VerificationResult{}, fmt.Errorf("no verification result configured")
	}
	result := v.results[0]
	v.results = v.results[1:]
	return result, nil
}

func TestRemediateBackToBackIncidentsDoNotShareLifecycleState(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	store := &trackingSnapshotStore{}
	action := &checkingAction{store: store, name: "injected-owner1-action"}
	verifier := &queuedVerifier{results: []VerificationResult{
		{Reason: "first incident re-crashed"},
		{Recovered: true},
	}}
	service := &Service{
		Snapshots: store,
		Verifier:  verifier,
		Action:    action,
		Audit:     NewJSONLAuditWriter(&bytes.Buffer{}),
		Clock:     clock,
	}
	event := DetectionEvent{
		PodName:         "checkout-pod",
		Namespace:       "shop",
		ContainerName:   "app",
		RestartCount:    3,
		OwnerDeployment: "checkout",
		Timestamp:       clock.Now(),
		IncidentID:      "incident-repeat",
		AttemptNumber:   1,
	}

	first, err := service.Remediate(context.Background(), event)
	if err != nil {
		t.Fatalf("first Remediate() error = %v", err)
	}
	second, err := service.Remediate(context.Background(), event)
	if err != nil {
		t.Fatalf("second Remediate() error = %v", err)
	}

	if first.Result != OutcomeRolledBack || second.Result != OutcomeRecovered {
		t.Fatalf("results = %q then %q, want rolled_back then recovered", first.Result, second.Result)
	}
	if first.AuditEntries[0].State != StateDetected || second.AuditEntries[0].State != StateDetected {
		t.Fatal("a back-to-back remediation did not start from DETECTED")
	}
	if store.captures != 2 || store.restores != 1 || action.calls != 2 {
		t.Fatalf(
			"captures/restores/actions = %d/%d/%d, want 2/1/2",
			store.captures,
			store.restores,
			action.calls,
		)
	}
}

type advancingAuditWriter struct {
	clock   *fakeClock
	entries []AuditEntry
}

func (w *advancingAuditWriter) Append(entry AuditEntry) error {
	w.entries = append(w.entries, entry)
	w.clock.Advance(time.Second)
	return nil
}

func TestOutcomeMTTREndsAtTerminalStateNotLoggedState(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	store := &trackingSnapshotStore{}
	action := &checkingAction{store: store, name: "injected-owner1-action"}
	audit := &advancingAuditWriter{clock: clock}
	service := &Service{
		Snapshots: store,
		Verifier:  fixedVerifier{result: VerificationResult{Recovered: true}},
		Action:    action,
		Audit:     audit,
		Clock:     clock,
	}
	event := DetectionEvent{
		PodName:         "checkout-pod",
		Namespace:       "shop",
		ContainerName:   "app",
		RestartCount:    3,
		OwnerDeployment: "checkout",
		Timestamp:       clock.Now(),
		IncidentID:      "incident-mttr",
		AttemptNumber:   1,
	}

	outcome, err := service.Remediate(context.Background(), event)
	if err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}

	var terminalTimestamp time.Time
	for _, entry := range outcome.AuditEntries {
		if entry.State == StateRecovered {
			terminalTimestamp = entry.Timestamp
		}
	}
	if terminalTimestamp.IsZero() {
		t.Fatal("RECOVERED audit timestamp was not recorded")
	}
	if outcome.MTTR != terminalTimestamp.Sub(event.Timestamp) {
		t.Fatalf(
			"MTTR = %s, terminal audit duration = %s",
			outcome.MTTR,
			terminalTimestamp.Sub(event.Timestamp),
		)
	}
	if outcome.MTTR == clock.Now().Sub(event.Timestamp) {
		t.Fatal("MTTR incorrectly includes LOGGED audit time")
	}
}
