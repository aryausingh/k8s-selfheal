package classifier

import (
	"context"
	"errors"
	"strings"
	"time"
)

const defaultClassificationTimeout = 30 * time.Second

var errClassifierNotConfigured = errors.New("classifier is not configured")

const unknownClassifierMetadata = "unknown"

type ClassificationService struct {
	Classifier Classifier
	Timeout    time.Duration
}

type IncidentClassifier interface {
	ClassifyIncident(
		ctx context.Context,
		input IncidentInput,
	) ClassificationOutcome
}

type ClassificationOutcome struct {
	Proposal           Proposal
	OriginalProposal   Proposal
	Validation         ValidationResult
	FallbackUsed       bool
	FallbackReason     string
	FallbackReasonCode string
	ClassifierError    error

	ClassifierStartedAt   time.Time
	ClassifierCompletedAt time.Time
	ClassifierDuration    time.Duration
	ClassifierProvider    string
	ClassifierModel       string

	InputTokens      int
	OutputTokens     int
	TotalTokens      int
	EstimatedCostUSD float64
	CostKnown        bool
}

type ClassifierMetadata interface {
	ProviderName() string
	ModelName() string
}

func NewClassificationService(
	c Classifier,
	timeout time.Duration,
) *ClassificationService {
	return &ClassificationService{
		Classifier: c,
		Timeout:    timeout,
	}
}

func (s *ClassificationService) ClassifyAndValidate(
	ctx context.Context,
	input IncidentInput,
) ClassificationOutcome {
	if ctx == nil {
		ctx = context.Background()
	}

	classifier := s.classifier()
	provider, model := classifierMetadata(classifier)

	if classifier == nil {
		startedAt := time.Now()

		return fallbackClassificationOutcome(
			input,
			errClassifierNotConfigured.Error(),
			ReasonCodeNone,
			errClassifierNotConfigured,
			startedAt,
			startedAt,
			provider,
			model,
		)
	}

	callCtx, cancel := context.WithTimeout(
		ctx,
		s.timeout(),
	)
	defer cancel()

	resultCh := make(chan classificationCallResult, 1)
	startedAt := time.Now()

	go func() {
		proposal, callMetadata, err := classifyWithOptionalMetadata(
			callCtx,
			classifier,
			input,
		)

		completedAt := time.Now()

		resultCh <- classificationCallResult{
			proposal:    proposal,
			metadata:    callMetadata,
			err:         err,
			startedAt:   startedAt,
			completedAt: completedAt,
		}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			outcome := fallbackClassificationOutcome(
				input,
				classifierFailureReason(result.err),
				ReasonCodeNone,
				result.err,
				result.startedAt,
				result.completedAt,
				provider,
				model,
			)

			applyClassifierCallMetadata(
				&outcome,
				result.metadata,
				model,
			)
			outcome.OriginalProposal = result.proposal

			return outcome
		}

		validation := ValidateProposal(
			result.proposal,
			input,
		)

		if validation.Decision == DecisionFallbackMiss ||
			!validation.Valid {

			reason := strings.TrimSpace(
				validation.Reason,
			)
			if reason == "" {
				reason = "classifier proposal failed validation"
			}

			outcome := fallbackClassificationOutcome(
				input,
				reason,
				validation.ReasonCode,
				nil,
				result.startedAt,
				result.completedAt,
				provider,
				model,
			)

			applyClassifierCallMetadata(
				&outcome,
				result.metadata,
				model,
			)
			outcome.OriginalProposal = result.proposal

			return outcome
		}

		outcome := ClassificationOutcome{
			Proposal:              validation.Output,
			OriginalProposal:      result.proposal,
			Validation:            validation,
			ClassifierStartedAt:   result.startedAt,
			ClassifierCompletedAt: result.completedAt,
			ClassifierDuration: classifierDuration(
				result.startedAt,
				result.completedAt,
			),
			ClassifierProvider: provider,
			ClassifierModel:    model,
		}

		applyClassifierCallMetadata(
			&outcome,
			result.metadata,
			model,
		)

		return outcome

	case <-callCtx.Done():
		err := callCtx.Err()
		completedAt := time.Now()

		return fallbackClassificationOutcome(
			input,
			classifierFailureReason(err),
			ReasonCodeNone,
			err,
			startedAt,
			completedAt,
			provider,
			model,
		)
	}
}

func (s *ClassificationService) ClassifyIncident(
	ctx context.Context,
	input IncidentInput,
) ClassificationOutcome {
	return s.ClassifyAndValidate(
		ctx,
		input,
	)
}

func (s *ClassificationService) classifier() Classifier {
	if s == nil {
		return nil
	}

	return s.Classifier
}

func (s *ClassificationService) timeout() time.Duration {
	if s == nil ||
		s.Timeout <= 0 {

		return defaultClassificationTimeout
	}

	return s.Timeout
}

type classificationCallResult struct {
	proposal    Proposal
	metadata    ClassifierCallMetadata
	err         error
	startedAt   time.Time
	completedAt time.Time
}

func classifyWithOptionalMetadata(
	ctx context.Context,
	classifier Classifier,
	input IncidentInput,
) (
	Proposal,
	ClassifierCallMetadata,
	error,
) {
	if metadataClassifier, ok := classifier.(MetadataClassifier); ok {
		return metadataClassifier.ClassifyWithMetadata(
			ctx,
			input,
		)
	}

	proposal, err := classifier.Classify(
		ctx,
		input,
	)

	return proposal, ClassifierCallMetadata{}, err
}

func classifierFailureReason(err error) string {
	switch {
	case errors.Is(
		err,
		context.DeadlineExceeded,
	):
		return "classifier timeout: " + err.Error()

	case errors.Is(
		err,
		context.Canceled,
	):
		return "classifier canceled: " + err.Error()

	case err != nil:
		return "classifier error: " + err.Error()

	default:
		return "classifier result could not be used safely"
	}
}

func fallbackClassificationOutcome(
	input IncidentInput,
	reason string,
	reasonCode string,
	classifierErr error,
	startedAt time.Time,
	completedAt time.Time,
	provider string,
	model string,
) ClassificationOutcome {
	proposal := buildSafeFallback(
		input,
		reason,
	)

	validation := ValidateProposal(
		proposal,
		input,
	)

	return ClassificationOutcome{
		Proposal:              validation.Output,
		Validation:            validation,
		FallbackUsed:          true,
		FallbackReason:        reason,
		FallbackReasonCode:    reasonCode,
		ClassifierError:       classifierErr,
		ClassifierStartedAt:   startedAt,
		ClassifierCompletedAt: completedAt,
		ClassifierDuration: classifierDuration(
			startedAt,
			completedAt,
		),
		ClassifierProvider: provider,
		ClassifierModel:    model,
	}
}

func buildSafeFallback(
	input IncidentInput,
	reason string,
) Proposal {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "classifier result could not be used safely"
	}

	if hasTransientFailureEvidence(input) {
		return Proposal{
			SubCause:          "transient_failure",
			RecommendedAction: ActionRestartPod,
			Target: Target{
				Kind:      "Pod",
				Namespace: input.Namespace,
				Name:      input.PodName,
			},
			SafeForAutomation: true,
			Reasoning:         "safe fallback selected restart_pod because incident evidence supports a transient failure: " + reason,
		}
	}

	return Proposal{
		SubCause:          "unknown",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "safe fallback selected manual escalation: " + reason,
	}
}

func hasTransientFailureEvidence(input IncidentInput) bool {
	evidence := strings.ToLower(
		input.Logs + " " +
			strings.Join(input.Events, " "),
	)

	return containsAny(
		evidence,
		"connection refused",
		"failed to connect",
		"temporarily unavailable",
		"temporary network failure",
		"timeout",
		"network failure",
	)
}

func classifierMetadata(classifier Classifier) (string, string) {
	provider := unknownClassifierMetadata
	model := unknownClassifierMetadata

	metadata, ok := classifier.(ClassifierMetadata)
	if !ok {
		return provider, model
	}

	if value := strings.TrimSpace(
		metadata.ProviderName(),
	); value != "" {
		provider = value
	}

	if value := strings.TrimSpace(
		metadata.ModelName(),
	); value != "" {
		model = value
	}

	return provider, model
}

func applyClassifierCallMetadata(
	outcome *ClassificationOutcome,
	metadata ClassifierCallMetadata,
	model string,
) {
	if outcome == nil {
		return
	}

	usage := TokenUsage{
		InputTokens:  metadata.InputTokens,
		OutputTokens: metadata.OutputTokens,
	}

	outcome.InputTokens = nonNegativeInt(
		metadata.InputTokens,
	)
	outcome.OutputTokens = nonNegativeInt(
		metadata.OutputTokens,
	)
	outcome.TotalTokens = usage.TotalTokens()

	pricing, ok := PricingForModel(model)
	if !ok {
		outcome.EstimatedCostUSD = 0
		outcome.CostKnown = false
		return
	}

	outcome.EstimatedCostUSD = EstimateCostUSD(
		usage,
		pricing,
	)
	outcome.CostKnown = true
}

func classifierDuration(
	startedAt time.Time,
	completedAt time.Time,
) time.Duration {
	if startedAt.IsZero() ||
		completedAt.IsZero() ||
		completedAt.Before(startedAt) {

		return 0
	}

	return completedAt.Sub(startedAt)
}
