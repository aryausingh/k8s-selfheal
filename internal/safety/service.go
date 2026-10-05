package safety

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// PodVerifier is the verification seam used by the Owner 2 service.
type PodVerifier interface {
	CapturePreActionPodUIDs(context.Context, VerificationTarget) (PodUIDSet, error)
	Verify(context.Context, VerificationTarget) (VerificationResult, error)
}

// RemediationAction represents Owner 1's real action without implementing it here.
type RemediationAction interface {
	Name() string
	Execute(context.Context, DetectionEvent) error
}

// Service connects Owner 2's lifecycle behind the frozen hand-off function.
type Service struct {
	Snapshots SnapshotStore
	Verifier  PodVerifier
	Action    RemediationAction
	Audit     AuditWriter
	Clock     Clock
	Metadata  AuditMetadata
}

// Remediate snapshots, invokes the injected action, verifies for the stability
// window, and restores the snapshot when verification fails.
func (s *Service) Remediate(ctx context.Context, event DetectionEvent) (Outcome, error) {
	if err := s.validate(); err != nil {
		return Outcome{}, err
	}
	if err := validateDetectionEvent(event); err != nil {
		return Outcome{}, err
	}

	machine := NewStateMachine()
	entries := make([]AuditEntry, 0, 8)
	record := func(state State, result string) (AuditEntry, error) {
		entry := AuditEntry{
			IncidentID:    event.IncidentID,
			AttemptNumber: event.AttemptNumber,
			Timestamp:     s.Clock.Now(),
			State:         state,
			Action:        s.Action.Name(),
			Result:        result,
			Workload:      s.Metadata.Workload,
			ArmLabel:      s.Metadata.ArmLabel,
		}
		if err := s.Audit.Append(entry); err != nil {
			return AuditEntry{}, err
		}
		entries = append(entries, entry)
		return entry, nil
	}
	transition := func(next State, result string) (AuditEntry, error) {
		if err := machine.Transition(next); err != nil {
			return AuditEntry{}, err
		}
		return record(next, result)
	}

	if _, err := record(StateDetected, "received"); err != nil {
		return Outcome{}, err
	}

	deploymentRef := types.NamespacedName{
		Name:      event.OwnerDeployment,
		Namespace: event.Namespace,
	}
	snapshot, err := s.Snapshots.Capture(ctx, deploymentRef)
	if err != nil {
		return Outcome{}, fmt.Errorf("capture pre-remediation snapshot: %w", err)
	}
	if _, err := transition(StateSnapshotted, "captured"); err != nil {
		return Outcome{}, err
	}

	_, err = transition(StateRemediating, "started")
	if err != nil {
		return Outcome{}, err
	}
	verificationTarget := VerificationTarget{
		OriginalPod: types.NamespacedName{
			Name:      event.PodName,
			Namespace: event.Namespace,
		},
		Deployment:    deploymentRef,
		ContainerName: event.ContainerName,
		RestartCount:  event.RestartCount,
	}
	preActionPodUIDs, err := s.Verifier.CapturePreActionPodUIDs(ctx, verificationTarget)
	if err != nil {
		return Outcome{}, fmt.Errorf("capture pre-action pod UIDs: %w", err)
	}
	verificationTarget.PreActionPodUIDs = preActionPodUIDs
	if err := s.Action.Execute(ctx, event); err != nil {
		return Outcome{}, fmt.Errorf("execute injected remediation action: %w", err)
	}

	if _, err := transition(StateVerifying, "started"); err != nil {
		return Outcome{}, err
	}
	verification, err := s.Verifier.Verify(ctx, verificationTarget)
	if err != nil {
		return Outcome{}, fmt.Errorf("verify remediation: %w", err)
	}

	if verification.Recovered {
		terminalEntry, err := transition(StateRecovered, "recovered")
		if err != nil {
			return Outcome{}, err
		}
		if _, err := transition(StateLogged, string(OutcomeRecovered)); err != nil {
			return Outcome{}, err
		}
		return s.outcome(event, OutcomeRecovered, terminalEntry.Timestamp, entries), nil
	}

	if _, err := transition(StateRollingBack, verification.Reason); err != nil {
		return Outcome{}, err
	}
	if err := s.Snapshots.Restore(ctx, snapshot); err != nil {
		return Outcome{}, fmt.Errorf("restore pre-remediation snapshot: %w", err)
	}
	terminalEntry, err := transition(StateRolledBack, "restored")
	if err != nil {
		return Outcome{}, err
	}
	if _, err := transition(StateLogged, string(OutcomeRolledBack)); err != nil {
		return Outcome{}, err
	}
	return s.outcome(event, OutcomeRolledBack, terminalEntry.Timestamp, entries), nil
}

func (s *Service) outcome(
	event DetectionEvent,
	result OutcomeResult,
	terminalTimestamp time.Time,
	entries []AuditEntry,
) Outcome {
	return Outcome{
		Result:       result,
		MTTR:         terminalTimestamp.Sub(event.Timestamp),
		AuditEntries: append([]AuditEntry(nil), entries...),
	}
}

func validateDetectionEvent(event DetectionEvent) error {
	if event.PodName == "" || event.Namespace == "" {
		return fmt.Errorf("remediate: pod name and namespace are required")
	}
	if event.ContainerName == "" {
		return fmt.Errorf("remediate: container name is required")
	}
	if event.RestartCount < 0 {
		return fmt.Errorf("remediate: restart count cannot be negative")
	}
	if event.OwnerDeployment == "" {
		return fmt.Errorf("remediate: owner deployment is required")
	}
	if event.Timestamp.IsZero() {
		return fmt.Errorf("remediate: event timestamp is required")
	}
	if event.IncidentID == "" {
		return fmt.Errorf("remediate: incident ID is required")
	}
	if event.AttemptNumber < 1 {
		return fmt.Errorf("remediate: attempt number must be at least 1")
	}
	return nil
}

func (s *Service) validate() error {
	if s.Snapshots == nil {
		return fmt.Errorf("remediate: snapshot store is required")
	}
	if s.Verifier == nil {
		return fmt.Errorf("remediate: verifier is required")
	}
	if s.Action == nil {
		return fmt.Errorf("remediate: injected action is required")
	}
	if s.Audit == nil {
		return fmt.Errorf("remediate: audit writer is required")
	}
	if s.Clock == nil {
		return fmt.Errorf("remediate: clock is required")
	}
	return nil
}
