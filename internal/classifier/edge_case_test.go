package classifier

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ============================================================
// TEST HELPERS
// ============================================================

func edgeCaseIncident() IncidentInput {
	return IncidentInput{
		DetectionEvent: DetectionEvent{
			PodName:         "checkoutservice-abc123",
			Namespace:       "default",
			ContainerName:   "checkoutservice",
			RestartCount:    5,
			OwnerDeployment: "checkoutservice",
			Timestamp:       time.Now(),
		},
		Logs: "container crashed because of a temporary dependency failure",
		Events: []string{
			"Back-off restarting failed container",
		},
	}
}

func validTransientProposal() Proposal {
	return Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: "default",
			Name:      "checkoutservice-abc123",
		},
		SafeForAutomation: true,
		Reasoning:         "temporary dependency failure",
	}
}

// ============================================================
// INCIDENT INPUT VALIDATION
// ============================================================

func TestEdgeCaseEmptyPodName(t *testing.T) {
	input := edgeCaseIncident()
	input.PodName = ""

	err := validateIncidentInput(input)

	if err == nil {
		t.Fatal("expected empty pod name to be rejected")
	}

	if !strings.Contains(err.Error(), "pod name is empty") {
		t.Fatalf(
			"expected pod name error, got: %v",
			err,
		)
	}
}

func TestEdgeCaseEmptyNamespace(t *testing.T) {
	input := edgeCaseIncident()
	input.Namespace = ""

	err := validateIncidentInput(input)

	if err == nil {
		t.Fatal("expected empty namespace to be rejected")
	}

	if !strings.Contains(err.Error(), "namespace is empty") {
		t.Fatalf(
			"expected namespace error, got: %v",
			err,
		)
	}
}

func TestEdgeCaseEmptyContainerName(t *testing.T) {
	input := edgeCaseIncident()
	input.ContainerName = ""

	err := validateIncidentInput(input)

	if err == nil {
		t.Fatal("expected empty container name to be rejected")
	}

	if !strings.Contains(err.Error(), "container name is empty") {
		t.Fatalf(
			"expected container name error, got: %v",
			err,
		)
	}
}

func TestEdgeCaseNoLogsAndNoEvents(t *testing.T) {
	input := edgeCaseIncident()

	input.Logs = ""
	input.Events = nil

	err := validateIncidentInput(input)

	if err == nil {
		t.Fatal("expected incident with no evidence to be rejected")
	}

	if !strings.Contains(err.Error(), "logs and events are empty") {
		t.Fatalf(
			"expected missing evidence error, got: %v",
			err,
		)
	}
}

func TestEdgeCaseLogsOnlyIsAccepted(t *testing.T) {
	input := edgeCaseIncident()

	input.Events = nil

	err := validateIncidentInput(input)

	if err != nil {
		t.Fatalf(
			"logs alone should be valid evidence: %v",
			err,
		)
	}
}

func TestEdgeCaseEventsOnlyIsAccepted(t *testing.T) {
	input := edgeCaseIncident()

	input.Logs = ""

	err := validateIncidentInput(input)

	if err != nil {
		t.Fatalf(
			"events alone should be valid evidence: %v",
			err,
		)
	}
}

// ============================================================
// WHITESPACE / NORMALIZATION
// ============================================================

func TestEdgeCaseWhitespaceIsNormalized(t *testing.T) {
	input := edgeCaseIncident()
	input.Logs = "connection refused while contacting payment service"

	proposal := validTransientProposal()

	proposal.SubCause = "  transient_failure  "
	proposal.RecommendedAction = "  restart_pod  "
	proposal.Target.Kind = "  Pod  "
	proposal.Target.Namespace = "  default  "
	proposal.Target.Name = "  checkoutservice-abc123  "
	proposal.Reasoning = "  temporary dependency failure  "

	result := ValidateProposal(
		proposal,
		input,
	)

	if !result.Valid {
		t.Fatalf(
			"proposal with surrounding whitespace should be normalized: %s",
			result.Reason,
		)
	}

	if result.Decision != DecisionAutomate {
		t.Fatalf(
			"expected automate after normalization, got: %s",
			result.Decision,
		)
	}

	if result.Output.SubCause != "transient_failure" {
		t.Fatalf(
			"expected normalized sub-cause, got: %q",
			result.Output.SubCause,
		)
	}

	if result.Output.RecommendedAction != "restart_pod" {
		t.Fatalf(
			"expected normalized action, got: %q",
			result.Output.RecommendedAction,
		)
	}
}

// ============================================================
// UNSUPPORTED / MALFORMED MODEL OUTPUT
// ============================================================

func TestEdgeCaseUnsupportedSubCause(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()
	proposal.SubCause = "disk_failure"

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal("unsupported sub-cause must be rejected")
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback miss, got: %s",
			result.Decision,
		)
	}
}

func TestEdgeCaseUnsupportedAction(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()
	proposal.RecommendedAction = "delete_everything"

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal("unsupported action must be rejected")
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback miss, got: %s",
			result.Decision,
		)
	}
}

func TestEdgeCaseWhitespaceOnlySubCause(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()
	proposal.SubCause = "   "

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal("whitespace-only sub-cause must be rejected")
	}
}

func TestEdgeCaseWhitespaceOnlyAction(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()
	proposal.RecommendedAction = "   "

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal("whitespace-only action must be rejected")
	}
}

func TestEdgeCaseWhitespaceOnlyReasoning(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()
	proposal.Reasoning = "   "

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal("whitespace-only reasoning must be rejected")
	}
}

// ============================================================
// DANGEROUS AUTOMATION ATTEMPTS
// ============================================================

func TestEdgeCaseOOMTriesToRestart(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.SubCause = "oom_adjacent"
	proposal.RecommendedAction = ActionRestartPod
	proposal.Reasoning = "restart pod after OOM"

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"OOM must never be automatically restarted",
		)
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback miss, got: %s",
			result.Decision,
		)
	}
}

func TestEdgeCaseBadConfigTriesToRestart(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.SubCause = "bad_config"
	proposal.RecommendedAction = ActionRestartPod

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"bad configuration must not be automatically restarted",
		)
	}
}

func TestEdgeCaseApplicationPanicTriesToRestart(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.SubCause = "application_panic"
	proposal.RecommendedAction = ActionRestartPod

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"application panic must not be automatically restarted",
		)
	}
}

func TestEdgeCaseUnknownFailureTriesToRestart(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.SubCause = "unknown"
	proposal.RecommendedAction = ActionRestartPod

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"unknown failures must not be automatically restarted",
		)
	}
}

func TestEdgeCaseTransientFailureTriesRolloutUndo(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.RecommendedAction = ActionRolloutUndo
	proposal.Target.Kind = "Deployment"
	proposal.Target.Name = input.OwnerDeployment

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"transient failure must not automatically use rollout undo",
		)
	}
}

func TestEdgeCaseNamespaceDeletionAttempt(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.RecommendedAction = "delete_namespace"
	proposal.Reasoning = "delete namespace to recover service"

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"namespace deletion must never be accepted",
		)
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback miss, got: %s",
			result.Decision,
		)
	}
}

// ============================================================
// TARGET MANIPULATION
// ============================================================

func TestEdgeCaseWrongNamespace(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()
	proposal.Target.Namespace = "production"

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"proposal targeting another namespace must be rejected",
		)
	}
}

func TestEdgeCaseWrongPod(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()
	proposal.Target.Name = "paymentservice-xyz999"

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"proposal targeting another pod must be rejected",
		)
	}
}

func TestEdgeCaseRestartTargetsDeployment(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.Target.Kind = "Deployment"
	proposal.Target.Name = input.OwnerDeployment

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"restart_pod cannot target a Deployment",
		)
	}
}

func TestEdgeCaseRolloutTargetsPod(t *testing.T) {
	input := edgeCaseIncident()

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "bad deployment revision",
	}

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"rollout_undo cannot target a Pod",
		)
	}
}

// ============================================================
// MISSING OWNER DEPLOYMENT
// ============================================================

func TestEdgeCaseRolloutWithoutOwnerDeployment(t *testing.T) {
	input := edgeCaseIncident()

	input.OwnerDeployment = ""

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      "checkoutservice",
		},
		SafeForAutomation: true,
		Reasoning:         "bad deployment revision",
	}

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"rollout undo must be rejected when owner deployment is missing",
		)
	}
}

// ============================================================
// ESCALATION SAFETY
// ============================================================

func TestEdgeCaseUnsafeProposalMustEscalate(t *testing.T) {
	input := edgeCaseIncident()

	proposal := validTransientProposal()

	proposal.SafeForAutomation = false
	proposal.RecommendedAction = ActionRestartPod

	result := ValidateProposal(
		proposal,
		input,
	)

	if result.Valid {
		t.Fatal(
			"unsafe proposal must not execute restart_pod",
		)
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback miss, got: %s",
			result.Decision,
		)
	}

	if !strings.Contains(
		result.Reason,
		"requires escalate_to_human",
	) {
		t.Fatalf(
			"expected escalation requirement, got: %s",
			result.Reason,
		)
	}
}

func TestEdgeCaseUnsafeProposalCanEscalate(t *testing.T) {
	input := edgeCaseIncident()

	proposal := Proposal{
		SubCause:          "unknown",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "insufficient evidence for safe automation",
	}

	result := ValidateProposal(
		proposal,
		input,
	)

	if !result.Valid {
		t.Fatalf(
			"valid unsafe proposal should escalate: %s",
			result.Reason,
		)
	}

	if result.Decision != DecisionEscalate {
		t.Fatalf(
			"expected escalation, got: %s",
			result.Decision,
		)
	}
}

// ============================================================
// API CLIENT FAILURE / EDGE CASES
// ============================================================

func TestEdgeCaseNilMistralClassifier(t *testing.T) {
	var m *MistralClassifier

	_, err := m.Classify(
		context.Background(),
		edgeCaseIncident(),
	)

	if err == nil {
		t.Fatal("nil Mistral classifier must return an error")
	}

	if !strings.Contains(
		err.Error(),
		"Mistral classifier is nil",
	) {
		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestEdgeCaseMistralEmptyAPIKey(t *testing.T) {
	m := &MistralClassifier{}

	_, err := m.Classify(
		context.Background(),
		edgeCaseIncident(),
	)

	if err == nil {
		t.Fatal("empty Mistral API key must return an error")
	}

	if !strings.Contains(
		err.Error(),
		"Mistral API key is empty",
	) {
		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestEdgeCaseNilClaudeClassifier(t *testing.T) {
	var c *ClaudeClassifier

	_, err := c.Classify(
		context.Background(),
		edgeCaseIncident(),
	)

	if err == nil {
		t.Fatal("nil Claude classifier must return an error")
	}

	if !strings.Contains(
		err.Error(),
		"Claude classifier is nil",
	) {
		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestEdgeCaseClaudeEmptyAPIKey(t *testing.T) {
	c := &ClaudeClassifier{}

	_, err := c.Classify(
		context.Background(),
		edgeCaseIncident(),
	)

	if err == nil {
		t.Fatal("empty Claude API key must return an error")
	}

	if !strings.Contains(
		err.Error(),
		"Claude API key is empty",
	) {
		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

// ============================================================
// CANCELLED CONTEXT
// ============================================================

func TestEdgeCaseMistralCancelledContext(t *testing.T) {
	m := &MistralClassifier{
		apiKey: "test-key",
	}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	cancel()

	_, err := m.Classify(
		ctx,
		edgeCaseIncident(),
	)

	if err == nil {
		t.Fatal(
			"cancelled context should cause Mistral request to fail",
		)
	}
}

func TestEdgeCaseClaudeCancelledContext(t *testing.T) {
	c := &ClaudeClassifier{
		apiKey: "test-key",
	}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	cancel()

	_, err := c.Classify(
		ctx,
		edgeCaseIncident(),
	)

	if err == nil {
		t.Fatal(
			"cancelled context should cause Claude request to fail",
		)
	}
}
