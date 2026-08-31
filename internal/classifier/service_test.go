package classifier

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type fixedClassifier struct {
	proposal Proposal
}

func (c fixedClassifier) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {
	return c.proposal, nil
}

type errorClassifier struct {
	err error
}

func (c errorClassifier) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {
	return Proposal{}, c.err
}

type delayedClassifier struct {
	delay    time.Duration
	proposal Proposal
}

type countingClassifier struct {
	proposal Proposal
	calls    int
}

type metadataClassifierStub struct {
	proposal      Proposal
	metadata      ClassifierCallMetadata
	classifyCalls int
	metadataCalls int
	provider      string
	model         string
}

func (c *countingClassifier) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {
	c.calls++

	return c.proposal, nil
}

func (c *metadataClassifierStub) ProviderName() string {
	return c.provider
}

func (c *metadataClassifierStub) ModelName() string {
	return c.model
}

func (c *metadataClassifierStub) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {
	c.classifyCalls++

	return Proposal{}, errors.New(
		"plain Classify should not be called",
	)
}

func (c *metadataClassifierStub) ClassifyWithMetadata(
	ctx context.Context,
	input IncidentInput,
) (
	Proposal,
	ClassifierCallMetadata,
	error,
) {
	c.metadataCalls++

	return c.proposal, c.metadata, nil
}

func (c delayedClassifier) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {
	timer := time.NewTimer(c.delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return Proposal{}, ctx.Err()
	case <-timer.C:
		return c.proposal, nil
	}
}

func TestClassificationServiceSatisfiesIncidentClassifier(t *testing.T) {
	var incidentClassifier IncidentClassifier = NewClassificationService(
		MockClassifier{},
		time.Second,
	)

	if incidentClassifier == nil {
		t.Fatal("expected ClassificationService to satisfy IncidentClassifier")
	}
}

func TestClassifyIncidentDelegatesToClassifyAndValidate(t *testing.T) {
	input := serviceTransientIncident()

	service := NewClassificationService(
		fixedClassifier{
			proposal: serviceTransientProposal(input),
		},
		time.Second,
	)

	incidentOutcome := service.ClassifyIncident(
		context.Background(),
		input,
	)

	directOutcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if incidentOutcome.Validation.Decision !=
		directOutcome.Validation.Decision {

		t.Fatalf(
			"expected same decision, got incident=%s direct=%s",
			incidentOutcome.Validation.Decision,
			directOutcome.Validation.Decision,
		)
	}

	if incidentOutcome.Proposal.RecommendedAction !=
		directOutcome.Proposal.RecommendedAction {

		t.Fatalf(
			"expected same action, got incident=%s direct=%s",
			incidentOutcome.Proposal.RecommendedAction,
			directOutcome.Proposal.RecommendedAction,
		)
	}

	if incidentOutcome.FallbackUsed != directOutcome.FallbackUsed {
		t.Fatalf(
			"expected same fallback usage, got incident=%v direct=%v",
			incidentOutcome.FallbackUsed,
			directOutcome.FallbackUsed,
		)
	}
}

func TestClassifyIncidentFallbackUsesExistingService(t *testing.T) {
	input := serviceUnknownIncident()

	service := NewClassificationService(
		errorClassifier{
			err: errors.New("provider unavailable"),
		},
		time.Second,
	)

	outcome := service.ClassifyIncident(
		context.Background(),
		input,
	)

	if !outcome.FallbackUsed {
		t.Fatal("expected ClassifyIncident to use service fallback")
	}

	if outcome.Validation.Decision != DecisionEscalate {
		t.Fatalf(
			"expected fallback escalation, got: %s",
			outcome.Validation.Decision,
		)
	}
}

func TestClassifyIncidentDoesNotDuplicateClassificationCall(t *testing.T) {
	input := serviceTransientIncident()
	classifier := &countingClassifier{
		proposal: serviceTransientProposal(input),
	}

	service := NewClassificationService(
		classifier,
		time.Second,
	)

	outcome := service.ClassifyIncident(
		context.Background(),
		input,
	)

	if !outcome.Validation.Valid {
		t.Fatalf(
			"expected valid classification, got: %s",
			outcome.Validation.Reason,
		)
	}

	if classifier.calls != 1 {
		t.Fatalf(
			"expected exactly one classifier call, got %d",
			classifier.calls,
		)
	}
}

func TestClassificationServiceMetadataClassifierPopulatesUsageAndCost(t *testing.T) {
	input := serviceTransientIncident()
	classifier := &metadataClassifierStub{
		proposal: serviceTransientProposal(input),
		metadata: ClassifierCallMetadata{
			InputTokens:  1000,
			OutputTokens: 200,
			TotalTokens:  1200,
		},
		provider: ProviderClaude,
		model:    claudeSonnet5Model,
	}

	service := NewClassificationService(
		classifier,
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if outcome.FallbackUsed {
		t.Fatalf(
			"expected direct classification, fallback reason: %s",
			outcome.FallbackReason,
		)
	}

	if classifier.metadataCalls != 1 {
		t.Fatalf(
			"expected one metadata classifier call, got %d",
			classifier.metadataCalls,
		)
	}

	if classifier.classifyCalls != 0 {
		t.Fatalf(
			"expected plain Classify not to be called, got %d",
			classifier.classifyCalls,
		)
	}

	if outcome.InputTokens != 1000 ||
		outcome.OutputTokens != 200 ||
		outcome.TotalTokens != 1200 {

		t.Fatalf(
			"unexpected token usage: input=%d output=%d total=%d",
			outcome.InputTokens,
			outcome.OutputTokens,
			outcome.TotalTokens,
		)
	}

	if !outcome.CostKnown {
		t.Fatal("expected known cost for Claude Sonnet 5")
	}

	expectedCost := 0.004
	if math.Abs(outcome.EstimatedCostUSD-expectedCost) >
		0.000001 {

		t.Fatalf(
			"expected cost %.6f, got %.6f",
			expectedCost,
			outcome.EstimatedCostUSD,
		)
	}
}

func TestClassificationServiceMetadataPathPreservesTarget(t *testing.T) {
	input := serviceTransientIncident()
	classifier := &metadataClassifierStub{
		proposal: Proposal{
			SubCause:          "transient_failure",
			RecommendedAction: ActionRestartPod,
			Target: Target{
				Kind:      "Pod",
				Namespace: "default",
				Name:      "checkoutservice-abc123",
			},
			SafeForAutomation: true,
			Reasoning:         "temporary dependency failure",
		},
		metadata: ClassifierCallMetadata{
			InputTokens:  1771,
			OutputTokens: 252,
			TotalTokens:  2023,
		},
		provider: ProviderClaude,
		model:    claudeSonnet5Model,
	}

	service := NewClassificationService(
		classifier,
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if outcome.FallbackUsed {
		t.Fatalf(
			"expected direct classification, fallback reason: %s",
			outcome.FallbackReason,
		)
	}

	if outcome.Proposal.Target.Name != "checkoutservice-abc123" {
		t.Fatalf(
			"expected target name checkoutservice-abc123, got %s",
			outcome.Proposal.Target.Name,
		)
	}

	if outcome.OriginalProposal.Target.Name != "checkoutservice-abc123" {
		t.Fatalf(
			"expected original target name checkoutservice-abc123, got %s",
			outcome.OriginalProposal.Target.Name,
		)
	}

	if !outcome.Validation.Valid {
		t.Fatalf(
			"expected valid classification, got: %s",
			outcome.Validation.Reason,
		)
	}

	if outcome.Validation.Decision != DecisionAutomate {
		t.Fatalf(
			"expected automate, got: %s",
			outcome.Validation.Decision,
		)
	}
}

func TestClassificationServiceValidResponse(t *testing.T) {
	input := serviceTransientIncident()

	service := NewClassificationService(
		MockClassifier{},
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if outcome.FallbackUsed {
		t.Fatalf(
			"expected classifier proposal to be used directly, fallback reason: %s",
			outcome.FallbackReason,
		)
	}

	if !outcome.Validation.Valid {
		t.Fatalf(
			"expected valid response, got: %s",
			outcome.Validation.Reason,
		)
	}

	if outcome.Validation.Decision != DecisionAutomate {
		t.Fatalf(
			"expected automate, got: %s",
			outcome.Validation.Decision,
		)
	}

	if outcome.Proposal.RecommendedAction != ActionRestartPod {
		t.Fatalf(
			"expected restart_pod, got: %s",
			outcome.Proposal.RecommendedAction,
		)
	}

	assertClassificationObservability(t, outcome)

	if outcome.ClassifierProvider != "mock" {
		t.Fatalf(
			"expected mock provider, got: %s",
			outcome.ClassifierProvider,
		)
	}

	if outcome.ClassifierModel != "mock" {
		t.Fatalf(
			"expected mock model, got: %s",
			outcome.ClassifierModel,
		)
	}
}

func TestClassificationServiceTimeoutUsesSafeFallback(t *testing.T) {
	input := serviceTransientIncident()

	service := NewClassificationService(
		delayedClassifier{
			delay:    100 * time.Millisecond,
			proposal: serviceTransientProposal(input),
		},
		20*time.Millisecond,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if !outcome.FallbackUsed {
		t.Fatal("expected timeout to use fallback")
	}

	if !errors.Is(
		outcome.ClassifierError,
		context.DeadlineExceeded,
	) {
		t.Fatalf(
			"expected deadline exceeded error, got: %v",
			outcome.ClassifierError,
		)
	}

	if !outcome.Validation.Valid {
		t.Fatalf(
			"expected fallback to pass validation, got: %s",
			outcome.Validation.Reason,
		)
	}

	if outcome.Validation.Decision != DecisionAutomate {
		t.Fatalf(
			"expected transient fallback to automate, got: %s",
			outcome.Validation.Decision,
		)
	}

	if outcome.Proposal.RecommendedAction != ActionRestartPod {
		t.Fatalf(
			"expected transient fallback restart_pod, got: %s",
			outcome.Proposal.RecommendedAction,
		)
	}

	assertClassificationObservability(t, outcome)

	if outcome.ClassifierDuration < 10*time.Millisecond ||
		outcome.ClassifierDuration > 500*time.Millisecond {

		t.Fatalf(
			"expected timeout duration near configured timeout, got: %s",
			outcome.ClassifierDuration,
		)
	}
}

func TestClassificationServiceErrorFallbackAutomatesTransient(t *testing.T) {
	input := serviceTransientIncident()
	providerErr := errors.New("provider unavailable")

	service := NewClassificationService(
		errorClassifier{
			err: providerErr,
		},
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if !outcome.FallbackUsed {
		t.Fatal("expected classifier error to use fallback")
	}

	if !errors.Is(
		outcome.ClassifierError,
		providerErr,
	) {
		t.Fatalf(
			"expected provider error to be preserved, got: %v",
			outcome.ClassifierError,
		)
	}

	if outcome.Validation.Decision != DecisionAutomate {
		t.Fatalf(
			"expected transient error fallback to automate, got: %s",
			outcome.Validation.Decision,
		)
	}

	if outcome.Proposal.RecommendedAction != ActionRestartPod {
		t.Fatalf(
			"expected restart_pod fallback, got: %s",
			outcome.Proposal.RecommendedAction,
		)
	}

	assertClassificationObservability(t, outcome)
}

func TestClassificationServiceErrorFallbackEscalatesUnknownEvidence(t *testing.T) {
	input := serviceUnknownIncident()

	service := NewClassificationService(
		errorClassifier{
			err: errors.New("provider unavailable"),
		},
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if !outcome.FallbackUsed {
		t.Fatal("expected classifier error to use fallback")
	}

	if outcome.Validation.Decision != DecisionEscalate {
		t.Fatalf(
			"expected unknown evidence fallback to escalate, got: %s",
			outcome.Validation.Decision,
		)
	}

	if outcome.Proposal.RecommendedAction == ActionRestartPod {
		t.Fatal("unknown evidence fallback must not restart automatically")
	}

	if outcome.Proposal.RecommendedAction != ActionEscalateToHuman {
		t.Fatalf(
			"expected escalate_to_human fallback, got: %s",
			outcome.Proposal.RecommendedAction,
		)
	}

	assertClassificationObservability(t, outcome)
}

func TestClassificationServiceInvalidProposalUsesFallback(t *testing.T) {
	input := serviceTransientIncident()

	invalidProposal := serviceTransientProposal(input)
	invalidProposal.RecommendedAction = "delete_namespace"

	service := NewClassificationService(
		fixedClassifier{
			proposal: invalidProposal,
		},
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if !outcome.FallbackUsed {
		t.Fatal("expected invalid proposal to use fallback")
	}

	if !strings.Contains(
		outcome.FallbackReason,
		"fallback miss",
	) {
		t.Fatalf(
			"expected original validator reason to be preserved, got: %s",
			outcome.FallbackReason,
		)
	}

	if outcome.FallbackReasonCode != ReasonCodeUnsupportedAction {
		t.Fatalf(
			"expected fallback reason code %q, got %q",
			ReasonCodeUnsupportedAction,
			outcome.FallbackReasonCode,
		)
	}

	if outcome.Proposal.RecommendedAction == "delete_namespace" {
		t.Fatal("invalid classifier action must not pass through")
	}

	if outcome.Validation.Decision != DecisionAutomate {
		t.Fatalf(
			"expected transient fallback to automate, got: %s",
			outcome.Validation.Decision,
		)
	}

	if outcome.Proposal.RecommendedAction != ActionRestartPod {
		t.Fatalf(
			"expected restart_pod fallback, got: %s",
			outcome.Proposal.RecommendedAction,
		)
	}

	assertClassificationObservability(t, outcome)
}

func TestClassificationServiceNilClassifierUsesSafeFallback(t *testing.T) {
	input := serviceUnknownIncident()

	service := NewClassificationService(
		nil,
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if !outcome.FallbackUsed {
		t.Fatal("expected nil classifier to use fallback")
	}

	if !errors.Is(
		outcome.ClassifierError,
		errClassifierNotConfigured,
	) {
		t.Fatalf(
			"expected missing classifier error, got: %v",
			outcome.ClassifierError,
		)
	}

	if outcome.Validation.Decision != DecisionEscalate {
		t.Fatalf(
			"expected nil classifier fallback to escalate, got: %s",
			outcome.Validation.Decision,
		)
	}

	if outcome.Proposal.RecommendedAction != ActionEscalateToHuman {
		t.Fatalf(
			"expected escalate_to_human fallback, got: %s",
			outcome.Proposal.RecommendedAction,
		)
	}

	assertClassificationObservability(t, outcome)
}

func TestClassificationServiceUnknownMetadataDoesNotFail(t *testing.T) {
	input := serviceTransientIncident()

	service := NewClassificationService(
		fixedClassifier{
			proposal: serviceTransientProposal(input),
		},
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if outcome.FallbackUsed {
		t.Fatalf(
			"expected classifier proposal to be used directly, fallback reason: %s",
			outcome.FallbackReason,
		)
	}

	if !outcome.Validation.Valid {
		t.Fatalf(
			"expected valid response, got: %s",
			outcome.Validation.Reason,
		)
	}

	if outcome.ClassifierProvider != "unknown" {
		t.Fatalf(
			"expected unknown provider, got: %s",
			outcome.ClassifierProvider,
		)
	}

	if outcome.ClassifierModel != "unknown" {
		t.Fatalf(
			"expected unknown model, got: %s",
			outcome.ClassifierModel,
		)
	}

	assertClassificationObservability(t, outcome)
}

func TestClassificationServiceNonMetadataClassifierLeavesUsageEmpty(t *testing.T) {
	input := serviceTransientIncident()

	service := NewClassificationService(
		MockClassifier{},
		time.Second,
	)

	outcome := service.ClassifyAndValidate(
		context.Background(),
		input,
	)

	if outcome.FallbackUsed {
		t.Fatalf(
			"expected classifier proposal to be used directly, fallback reason: %s",
			outcome.FallbackReason,
		)
	}

	if outcome.InputTokens != 0 ||
		outcome.OutputTokens != 0 ||
		outcome.TotalTokens != 0 {

		t.Fatalf(
			"expected empty usage for non-metadata classifier, got input=%d output=%d total=%d",
			outcome.InputTokens,
			outcome.OutputTokens,
			outcome.TotalTokens,
		)
	}

	if outcome.CostKnown {
		t.Fatal("expected cost to be unknown without configured pricing")
	}

	if outcome.EstimatedCostUSD != 0 {
		t.Fatalf(
			"expected zero estimated cost, got %.6f",
			outcome.EstimatedCostUSD,
		)
	}
}

func serviceTransientIncident() IncidentInput {
	return serviceIncident(
		`
connection refused while contacting payment service
temporary network failure
`,
		"Back-off restarting failed container",
	)
}

func serviceUnknownIncident() IncidentInput {
	return serviceIncident(
		`
container exited unexpectedly and the cause is unclear
`,
		"Back-off restarting failed container",
	)
}

func serviceIncident(
	logs string,
	events ...string,
) IncidentInput {
	return IncidentInput{
		DetectionEvent: DetectionEvent{
			PodName:         "checkoutservice-abc123",
			Namespace:       "default",
			ContainerName:   "checkoutservice",
			RestartCount:    5,
			OwnerDeployment: "checkoutservice",
			Timestamp:       time.Now(),
		},
		Logs:   logs,
		Events: events,
	}
}

func serviceTransientProposal(input IncidentInput) Proposal {
	return Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "The evidence indicates a transient dependency or connectivity failure.",
	}
}

func assertClassificationObservability(
	t *testing.T,
	outcome ClassificationOutcome,
) {
	t.Helper()

	if outcome.ClassifierStartedAt.IsZero() {
		t.Fatal("expected classifier start timestamp")
	}

	if outcome.ClassifierCompletedAt.IsZero() {
		t.Fatal("expected classifier completion timestamp")
	}

	if outcome.ClassifierCompletedAt.Before(
		outcome.ClassifierStartedAt,
	) {
		t.Fatalf(
			"completion timestamp is before start: start=%s completed=%s",
			outcome.ClassifierStartedAt,
			outcome.ClassifierCompletedAt,
		)
	}

	if outcome.ClassifierDuration < 0 {
		t.Fatalf(
			"expected non-negative duration, got: %s",
			outcome.ClassifierDuration,
		)
	}

	expectedDuration := outcome.ClassifierCompletedAt.Sub(
		outcome.ClassifierStartedAt,
	)

	if outcome.ClassifierDuration != expectedDuration {
		t.Fatalf(
			"expected duration %s, got: %s",
			expectedDuration,
			outcome.ClassifierDuration,
		)
	}

	if outcome.ClassifierProvider == "" {
		t.Fatal("expected classifier provider")
	}

	if outcome.ClassifierModel == "" {
		t.Fatal("expected classifier model")
	}
}
