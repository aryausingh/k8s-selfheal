package classifier

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClaudeLiveClassificationSanity(t *testing.T) {
	if os.Getenv("RUN_CLAUDE_LIVE") != "1" {
		t.Skip(
			"set RUN_CLAUDE_LIVE=1 to run live Claude API test",
		)
	}

	if strings.TrimSpace(
		os.Getenv("ANTHROPIC_API_KEY"),
	) == "" {
		t.Skip(
			"set ANTHROPIC_API_KEY to run live Claude API test",
		)
	}

	configuredModel := strings.TrimSpace(
		os.Getenv(ClaudeModelEnv),
	)
	if configuredModel != claudeSonnet5Model {
		t.Skipf(
			"set %s=%s to run the live Claude Sonnet 5 cost sanity test",
			ClaudeModelEnv,
			claudeSonnet5Model,
		)
	}

	claudeClassifier, err := NewClaudeClassifier()
	if err != nil {
		t.Fatalf("create Claude classifier: %v", err)
	}

	if claudeClassifier.ModelName() != configuredModel {
		t.Fatalf(
			"expected configured model %q, got %q",
			configuredModel,
			claudeClassifier.ModelName(),
		)
	}

	service := NewClassificationService(
		claudeClassifier,
		60*time.Second,
	)

	outcome := service.ClassifyIncident(
		context.Background(),
		claudeLiveTransientIncident(),
	)

	t.Logf("Provider: %s", outcome.ClassifierProvider)
	t.Logf("Model: %s", outcome.ClassifierModel)
	t.Logf("Sub-cause: %s", outcome.Proposal.SubCause)
	t.Logf("Action: %s", outcome.Proposal.RecommendedAction)
	t.Logf("Decision: %s", outcome.Validation.Decision)
	t.Logf("Original sub-cause: %s", outcome.OriginalProposal.SubCause)
	t.Logf("Original action: %s", outcome.OriginalProposal.RecommendedAction)
	t.Logf("Original target kind: %s", outcome.OriginalProposal.Target.Kind)
	t.Logf("Original target namespace: %s", outcome.OriginalProposal.Target.Namespace)
	t.Logf("Original target name: %s", outcome.OriginalProposal.Target.Name)
	t.Logf("Latency: %s", outcome.ClassifierDuration)
	t.Logf("Input tokens: %d", outcome.InputTokens)
	t.Logf("Output tokens: %d", outcome.OutputTokens)
	t.Logf("Total tokens: %d", outcome.TotalTokens)
	t.Logf("Estimated cost: $%.6f", outcome.EstimatedCostUSD)

	if outcome.ClassifierError != nil {
		t.Fatalf(
			"Claude API call failed: %v",
			outcome.ClassifierError,
		)
	}

	if outcome.ClassifierProvider != ProviderClaude {
		t.Fatalf(
			"expected provider %q, got %q",
			ProviderClaude,
			outcome.ClassifierProvider,
		)
	}

	if outcome.ClassifierModel == "" {
		t.Fatal("expected non-empty classifier model")
	}

	if outcome.ClassifierModel != configuredModel {
		t.Fatalf(
			"expected model %q, got %q",
			configuredModel,
			outcome.ClassifierModel,
		)
	}

	if proposalIsEmpty(outcome.Proposal) {
		t.Fatal("expected non-empty proposal")
	}

	if outcome.FallbackUsed {
		t.Fatalf(
			"expected fallback to be false; reason=%q reason_code=%q original_proposal=%+v fallback_proposal=%+v validation=%+v",
			outcome.FallbackReason,
			outcome.FallbackReasonCode,
			outcome.OriginalProposal,
			outcome.Proposal,
			outcome.Validation,
		)
	}

	if !outcome.Validation.Valid {
		t.Fatalf(
			"expected proposal to pass validator; reason=%q reason_code=%q proposal=%+v",
			outcome.Validation.Reason,
			outcome.Validation.ReasonCode,
			outcome.Proposal,
		)
	}

	if outcome.Validation.Decision != DecisionAutomate {
		t.Fatalf(
			"expected validator decision %q, got %q; reason=%q proposal=%+v",
			DecisionAutomate,
			outcome.Validation.Decision,
			outcome.Validation.Reason,
			outcome.Proposal,
		)
	}

	if outcome.Proposal.SubCause != "transient_failure" {
		t.Fatalf(
			"expected transient_failure, got %q; proposal=%+v",
			outcome.Proposal.SubCause,
			outcome.Proposal,
		)
	}

	if outcome.Proposal.RecommendedAction != ActionRestartPod {
		t.Fatalf(
			"expected action %q, got %q; proposal=%+v",
			ActionRestartPod,
			outcome.Proposal.RecommendedAction,
			outcome.Proposal,
		)
	}

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
			"classifier completion is before start: start=%s completed=%s",
			outcome.ClassifierStartedAt,
			outcome.ClassifierCompletedAt,
		)
	}

	if outcome.ClassifierDuration < 0 ||
		outcome.ClassifierDuration > 2*time.Minute {

		t.Fatalf(
			"expected sensible classifier duration, got %s",
			outcome.ClassifierDuration,
		)
	}

	if outcome.InputTokens <= 0 {
		t.Fatalf(
			"expected positive input tokens, got %d",
			outcome.InputTokens,
		)
	}

	if outcome.OutputTokens <= 0 {
		t.Fatalf(
			"expected positive output tokens, got %d",
			outcome.OutputTokens,
		)
	}

	if outcome.TotalTokens != outcome.InputTokens+
		outcome.OutputTokens {

		t.Fatalf(
			"expected total tokens to equal input + output, got input=%d output=%d total=%d",
			outcome.InputTokens,
			outcome.OutputTokens,
			outcome.TotalTokens,
		)
	}

	if !outcome.CostKnown {
		t.Fatal("expected known cost for Claude Sonnet 5")
	}

	if outcome.EstimatedCostUSD <= 0 {
		t.Fatalf(
			"expected positive estimated cost, got %.6f",
			outcome.EstimatedCostUSD,
		)
	}
}

func proposalIsEmpty(proposal Proposal) bool {
	return strings.TrimSpace(proposal.SubCause) == "" &&
		strings.TrimSpace(proposal.RecommendedAction) == "" &&
		strings.TrimSpace(proposal.Target.Kind) == "" &&
		strings.TrimSpace(proposal.Target.Namespace) == "" &&
		strings.TrimSpace(proposal.Target.Name) == "" &&
		strings.TrimSpace(proposal.Reasoning) == ""
}
