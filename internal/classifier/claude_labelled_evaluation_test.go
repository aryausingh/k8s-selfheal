package classifier

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	runClaudeLabelledEvalEnv = "RUN_CLAUDE_LABELLED_EVAL"
	claudeLabelledTimeout    = 60 * time.Second
)

type claudeLabelledEvaluationStats struct {
	OriginalAccepted int
	OriginalRejected int
	FallbackUsed     int
	FinalEscalated   int

	TargetStructuralRejections int
	SemanticGuardRejections    int
	OtherRejections            int
	ReasonCodeCounts           map[string]int

	CallsWithUsage    int
	KnownCostCalls    int
	TotalInputTokens  int
	TotalOutputTokens int
	TotalTokens       int
	TotalCostUSD      float64
}

func TestClaudeLiveLabelledEvaluation(t *testing.T) {
	if os.Getenv("RUN_CLAUDE_LIVE") != "1" ||
		os.Getenv(runClaudeLabelledEvalEnv) != "1" {

		t.Skip(
			"set RUN_CLAUDE_LIVE=1 and RUN_CLAUDE_LABELLED_EVAL=1 to run live Claude labelled evaluation",
		)
	}

	if strings.TrimSpace(
		os.Getenv("ANTHROPIC_API_KEY"),
	) == "" {
		t.Skip(
			"set ANTHROPIC_API_KEY to run live Claude labelled evaluation",
		)
	}

	cases := labelledClassificationCases()
	if len(cases) == 0 {
		t.Fatal("labelled classification dataset is empty")
	}
	if len(cases) != 20 {
		t.Fatalf(
			"expected exactly 20 labelled cases, got %d",
			len(cases),
		)
	}

	claudeClassifier, err := NewClaudeClassifier()
	if err != nil {
		t.Fatalf("create Claude classifier: %v", err)
	}

	service := NewClassificationService(
		claudeClassifier,
		claudeLabelledTimeout,
	)

	evaluationResults := make(
		[]ClassificationEvaluationResult,
		0,
		len(cases),
	)
	stats := claudeLabelledEvaluationStats{}

	for i, tc := range cases {
		t.Log("")
		t.Log("============================================================")
		t.Logf(
			"CASE %d/%d: %s",
			i+1,
			len(cases),
			tc.Name,
		)
		t.Log("============================================================")

		outcome := service.ClassifyIncident(
			context.Background(),
			tc.Input,
		)

		result := claudeEvaluationResultFromOutcome(
			tc,
			outcome,
		)
		evaluationResults = append(
			evaluationResults,
			result,
		)

		originalValidation, hasOriginalValidation :=
			claudeOriginalValidation(
				outcome,
				tc.Input,
			)

		accumulateClaudeLabelledEvaluationStats(
			&stats,
			outcome,
			originalValidation,
			hasOriginalValidation,
		)

		logClaudeLabelledCase(
			t,
			tc,
			outcome,
			originalValidation,
			hasOriginalValidation,
			claudeEvaluationResultCorrect(tc, result),
		)
	}

	if len(evaluationResults) != len(cases) {
		t.Fatalf(
			"evaluation bookkeeping mismatch: got %d results for %d cases",
			len(evaluationResults),
			len(cases),
		)
	}

	summary := CalculateClassificationEvaluation(
		cases,
		evaluationResults,
	)

	if summary.TotalCases != len(cases) {
		t.Fatalf(
			"summary total mismatch: got %d, want %d",
			summary.TotalCases,
			len(cases),
		)
	}

	if summary.SuccessfulCalls+summary.FailedCalls !=
		summary.TotalCases {

		t.Fatalf(
			"call accounting mismatch: success=%d failed=%d total=%d",
			summary.SuccessfulCalls,
			summary.FailedCalls,
			summary.TotalCases,
		)
	}

	if stats.OriginalAccepted+stats.OriginalRejected !=
		summary.SuccessfulCalls {

		t.Fatalf(
			"validator accounting mismatch: accepted=%d rejected=%d successful=%d",
			stats.OriginalAccepted,
			stats.OriginalRejected,
			summary.SuccessfulCalls,
		)
	}

	if err := stats.validateRejectionAccounting(
		summary.SuccessfulCalls,
	); err != nil {
		t.Fatal(err)
	}

	if summary.SuccessfulCalls > 0 && stats.CallsWithUsage == 0 {
		t.Fatal(
			"successful Claude calls did not report any token usage metadata",
		)
	}

	t.Log("")
	t.Log("============================================================")
	t.Log("CLAUDE LIVE LABELLED EVALUATION")
	t.Log("============================================================")
	t.Logf(
		"Provider: %s",
		claudeClassifier.ProviderName(),
	)
	t.Logf(
		"Model: %s",
		claudeClassifier.ModelName(),
	)
	t.Log(FormatClassificationEvaluation(
		claudeClassifier.ModelName(),
		summary,
	))
	t.Logf(
		"Model call success rate: %.2f%%",
		classificationPercentage(
			summary.SuccessfulCalls,
			summary.TotalCases,
		),
	)
	t.Logf(
		"Correct sub-causes: %d",
		summary.CorrectSubCause,
	)
	t.Logf(
		"Correct actions: %d",
		summary.CorrectAction,
	)
	t.Logf(
		"Correct safety flags: %d",
		summary.CorrectSafety,
	)
	t.Logf(
		"Original proposals accepted: %d",
		stats.OriginalAccepted,
	)
	t.Logf(
		"Original proposals rejected: %d",
		stats.OriginalRejected,
	)
	t.Logf(
		"Rejected due to target/structural errors: %d",
		stats.TargetStructuralRejections,
	)
	t.Logf(
		"Rejected due to semantic guard: %d",
		stats.SemanticGuardRejections,
	)
	t.Logf(
		"Rejected for other reasons: %d",
		stats.OtherRejections,
	)
	t.Log("Validator rejection reason-code counts:")
	if len(stats.ReasonCodeCounts) == 0 {
		t.Log("none: 0")
	} else {
		for _, reasonCode := range stats.sortedReasonCodes() {
			t.Logf(
				"%s: %d",
				formatClaudeReasonCode(reasonCode),
				stats.ReasonCodeCounts[reasonCode],
			)
		}
	}
	t.Logf(
		"Classifications requiring fallback: %d",
		stats.FallbackUsed,
	)
	t.Logf(
		"Final escalated classifications: %d",
		stats.FinalEscalated,
	)
	t.Logf(
		"Total input tokens: %d",
		stats.TotalInputTokens,
	)
	t.Logf(
		"Total output tokens: %d",
		stats.TotalOutputTokens,
	)
	t.Logf(
		"Total tokens: %d",
		stats.TotalTokens,
	)
	t.Logf(
		"Calls with known cost: %d",
		stats.KnownCostCalls,
	)
	t.Logf(
		"Total estimated cost: $%.6f",
		stats.TotalCostUSD,
	)
	t.Logf(
		"Average estimated cost per known-cost call: $%.6f",
		stats.averageCostPerKnownCall(),
	)
	t.Log("============================================================")
}

func claudeEvaluationResultFromOutcome(
	tc labelledClassificationCase,
	outcome ClassificationOutcome,
) ClassificationEvaluationResult {
	return ClassificationEvaluationResult{
		Case:     tc,
		Proposal: outcome.OriginalProposal,
		Error:    outcome.ClassifierError,
		Latency:  outcome.ClassifierDuration,
	}
}

func claudeEvaluationResultCorrect(
	tc labelledClassificationCase,
	result ClassificationEvaluationResult,
) bool {
	if result.Error != nil {
		return false
	}

	return result.Proposal.SubCause == tc.ExpectedSubCause &&
		result.Proposal.RecommendedAction == tc.ExpectedAction &&
		result.Proposal.SafeForAutomation ==
			tc.ExpectedSafeForAutomation
}

func claudeOriginalValidation(
	outcome ClassificationOutcome,
	input IncidentInput,
) (ValidationResult, bool) {
	if outcome.ClassifierError != nil {
		return ValidationResult{}, false
	}

	return ValidateProposal(
		outcome.OriginalProposal,
		input,
	), true
}

func accumulateClaudeLabelledEvaluationStats(
	stats *claudeLabelledEvaluationStats,
	outcome ClassificationOutcome,
	originalValidation ValidationResult,
	hasOriginalValidation bool,
) {
	if stats == nil {
		return
	}

	if hasOriginalValidation {
		if originalValidation.Valid &&
			originalValidation.Decision != DecisionFallbackMiss {

			stats.OriginalAccepted++
		} else {
			stats.OriginalRejected++
			stats.recordRejectionReason(
				originalValidation.ReasonCode,
			)
		}
	}

	if outcome.FallbackUsed {
		stats.FallbackUsed++
	}

	if outcome.Validation.Decision == DecisionEscalate {
		stats.FinalEscalated++
	}

	stats.TotalInputTokens += outcome.InputTokens
	stats.TotalOutputTokens += outcome.OutputTokens
	stats.TotalTokens += outcome.TotalTokens

	if outcome.InputTokens > 0 ||
		outcome.OutputTokens > 0 ||
		outcome.TotalTokens > 0 {

		stats.CallsWithUsage++
	}

	if outcome.CostKnown {
		stats.KnownCostCalls++
		stats.TotalCostUSD += outcome.EstimatedCostUSD
	}
}

func (s *claudeLabelledEvaluationStats) recordRejectionReason(
	reasonCode string,
) {
	if s == nil {
		return
	}

	if s.ReasonCodeCounts == nil {
		s.ReasonCodeCounts = make(map[string]int)
	}

	s.ReasonCodeCounts[reasonCode]++

	switch classifyClaudeRejection(reasonCode) {
	case claudeRejectionTargetStructural:
		s.TargetStructuralRejections++
	case claudeRejectionSemanticGuard:
		s.SemanticGuardRejections++
	default:
		s.OtherRejections++
	}
}

func (s claudeLabelledEvaluationStats) validateRejectionAccounting(
	successfulCalls int,
) error {
	if s.OriginalAccepted+s.OriginalRejected != successfulCalls {
		return fmt.Errorf(
			"validator accounting mismatch: accepted=%d rejected=%d successful=%d",
			s.OriginalAccepted,
			s.OriginalRejected,
			successfulCalls,
		)
	}

	reasonCodeTotal := 0
	for _, count := range s.ReasonCodeCounts {
		reasonCodeTotal += count
	}

	if reasonCodeTotal != s.OriginalRejected {
		return fmt.Errorf(
			"rejection reason-code accounting mismatch: reason_code_total=%d rejected=%d",
			reasonCodeTotal,
			s.OriginalRejected,
		)
	}

	categoryTotal :=
		s.TargetStructuralRejections +
			s.SemanticGuardRejections +
			s.OtherRejections

	if categoryTotal != s.OriginalRejected {
		return fmt.Errorf(
			"rejection category accounting mismatch: category_total=%d rejected=%d",
			categoryTotal,
			s.OriginalRejected,
		)
	}

	return nil
}

func (s claudeLabelledEvaluationStats) sortedReasonCodes() []string {
	reasonCodes := make(
		[]string,
		0,
		len(s.ReasonCodeCounts),
	)

	for reasonCode := range s.ReasonCodeCounts {
		reasonCodes = append(
			reasonCodes,
			reasonCode,
		)
	}

	sort.Strings(reasonCodes)

	return reasonCodes
}

type claudeRejectionCategory string

const (
	claudeRejectionTargetStructural claudeRejectionCategory = "target_structural"
	claudeRejectionSemanticGuard    claudeRejectionCategory = "semantic_guard"
	claudeRejectionOther            claudeRejectionCategory = "other"
)

func classifyClaudeRejection(
	reasonCode string,
) claudeRejectionCategory {
	switch reasonCode {
	case ReasonCodeMissingTargetKind,
		ReasonCodeMissingTargetNamespace,
		ReasonCodeMissingTargetName,
		ReasonCodeNamespaceMismatch,
		ReasonCodeWrongTargetKind,
		ReasonCodeWrongTargetName,
		ReasonCodeMissingOwnerDeployment:

		return claudeRejectionTargetStructural

	case ReasonCodeMissingDeploymentEvidence,
		ReasonCodeSemanticGuardRejected:

		return claudeRejectionSemanticGuard

	default:
		return claudeRejectionOther
	}
}

func formatClaudeReasonCode(reasonCode string) string {
	if reasonCode == "" {
		return "(none)"
	}

	return reasonCode
}

func (s claudeLabelledEvaluationStats) averageCostPerKnownCall() float64 {
	if s.KnownCostCalls == 0 {
		return 0
	}

	return s.TotalCostUSD / float64(s.KnownCostCalls)
}

func logClaudeLabelledCase(
	t *testing.T,
	tc labelledClassificationCase,
	outcome ClassificationOutcome,
	originalValidation ValidationResult,
	hasOriginalValidation bool,
	correct bool,
) {
	t.Helper()

	t.Logf(
		"Expected: sub_cause=%s action=%s safe=%v",
		tc.ExpectedSubCause,
		tc.ExpectedAction,
		tc.ExpectedSafeForAutomation,
	)
	t.Logf(
		"Claude original: sub_cause=%s action=%s safe=%v target=%s",
		outcome.OriginalProposal.SubCause,
		outcome.OriginalProposal.RecommendedAction,
		outcome.OriginalProposal.SafeForAutomation,
		formatClaudeLabelledTarget(outcome.OriginalProposal.Target),
	)

	if hasOriginalValidation {
		t.Logf(
			"Validator: original_decision=%s original_valid=%v original_reason_code=%s final_decision=%s final_valid=%v final_reason_code=%s",
			originalValidation.Decision,
			originalValidation.Valid,
			originalValidation.ReasonCode,
			outcome.Validation.Decision,
			outcome.Validation.Valid,
			outcome.Validation.ReasonCode,
		)
	} else {
		t.Logf(
			"Validator: original_decision=not_run original_valid=false original_reason_code= final_decision=%s final_valid=%v final_reason_code=%s",
			outcome.Validation.Decision,
			outcome.Validation.Valid,
			outcome.Validation.ReasonCode,
		)
	}

	if outcome.ClassifierError != nil {
		t.Logf(
			"Classifier error: %v",
			outcome.ClassifierError,
		)
	}

	t.Logf(
		"Fallback: used=%v",
		outcome.FallbackUsed,
	)
	t.Logf(
		"Latency: %s",
		outcome.ClassifierDuration.Round(time.Millisecond),
	)
	t.Logf(
		"Tokens: input=%d output=%d total=%d",
		outcome.InputTokens,
		outcome.OutputTokens,
		outcome.TotalTokens,
	)
	t.Logf(
		"Estimated cost: $%.6f cost_known=%v",
		outcome.EstimatedCostUSD,
		outcome.CostKnown,
	)

	if correct {
		t.Log("RESULT: CORRECT")
	} else {
		t.Log("RESULT: INCORRECT")
	}
}

func formatClaudeLabelledTarget(target Target) string {
	return fmt.Sprintf(
		"%s/%s/%s",
		target.Kind,
		target.Namespace,
		target.Name,
	)
}

func TestClaudeEvaluationUsesOriginalProposalForAccuracy(t *testing.T) {
	tc := classificationEvaluationCase(
		"fallback must not inflate accuracy",
		"transient_failure",
		ActionRestartPod,
		true,
	)

	outcome := ClassificationOutcome{
		OriginalProposal: Proposal{
			SubCause:          "bad_config",
			RecommendedAction: ActionEscalateToHuman,
			SafeForAutomation: false,
		},
		Proposal: Proposal{
			SubCause:          tc.ExpectedSubCause,
			RecommendedAction: tc.ExpectedAction,
			SafeForAutomation: tc.ExpectedSafeForAutomation,
		},
		FallbackUsed:       true,
		ClassifierDuration: 10 * time.Millisecond,
	}

	result := claudeEvaluationResultFromOutcome(
		tc,
		outcome,
	)
	summary := CalculateClassificationEvaluation(
		[]labelledClassificationCase{tc},
		[]ClassificationEvaluationResult{result},
	)

	if result.Proposal.SubCause != "bad_config" {
		t.Fatalf(
			"expected original proposal to be evaluated, got %#v",
			result.Proposal,
		)
	}

	if summary.CorrectAll != 0 {
		t.Fatalf(
			"fallback proposal inflated accuracy: CorrectAll=%d",
			summary.CorrectAll,
		)
	}
}

func TestClaudeEvaluationAggregatesTokensCostAndValidatorCounts(t *testing.T) {
	stats := claudeLabelledEvaluationStats{}

	accepted := ValidationResult{
		Valid:    true,
		Decision: DecisionAutomate,
	}

	accumulateClaudeLabelledEvaluationStats(
		&stats,
		ClassificationOutcome{
			Validation: ValidationResult{
				Decision: DecisionAutomate,
			},
			InputTokens:      100,
			OutputTokens:     25,
			TotalTokens:      125,
			EstimatedCostUSD: 0.001,
			CostKnown:        true,
		},
		accepted,
		true,
	)

	rejected := ValidationResult{
		Valid:      false,
		Decision:   DecisionFallbackMiss,
		ReasonCode: ReasonCodeMissingTargetName,
	}

	accumulateClaudeLabelledEvaluationStats(
		&stats,
		ClassificationOutcome{
			Validation: ValidationResult{
				Decision: DecisionEscalate,
			},
			FallbackUsed:     true,
			InputTokens:      200,
			OutputTokens:     50,
			TotalTokens:      250,
			EstimatedCostUSD: 0.002,
			CostKnown:        true,
		},
		rejected,
		true,
	)

	if stats.OriginalAccepted != 1 {
		t.Fatalf(
			"OriginalAccepted: got %d, want 1",
			stats.OriginalAccepted,
		)
	}
	if stats.OriginalRejected != 1 {
		t.Fatalf(
			"OriginalRejected: got %d, want 1",
			stats.OriginalRejected,
		)
	}
	if stats.FallbackUsed != 1 {
		t.Fatalf(
			"FallbackUsed: got %d, want 1",
			stats.FallbackUsed,
		)
	}
	if stats.FinalEscalated != 1 {
		t.Fatalf(
			"FinalEscalated: got %d, want 1",
			stats.FinalEscalated,
		)
	}
	if stats.TotalInputTokens != 300 ||
		stats.TotalOutputTokens != 75 ||
		stats.TotalTokens != 375 {

		t.Fatalf(
			"unexpected token totals: input=%d output=%d total=%d",
			stats.TotalInputTokens,
			stats.TotalOutputTokens,
			stats.TotalTokens,
		)
	}
	if stats.KnownCostCalls != 2 {
		t.Fatalf(
			"KnownCostCalls: got %d, want 2",
			stats.KnownCostCalls,
		)
	}
	if stats.TotalCostUSD != 0.003 {
		t.Fatalf(
			"TotalCostUSD: got %.6f, want 0.003000",
			stats.TotalCostUSD,
		)
	}
	if stats.averageCostPerKnownCall() != 0.0015 {
		t.Fatalf(
			"average cost: got %.6f, want 0.001500",
			stats.averageCostPerKnownCall(),
		)
	}
}

func TestClaudeRejectionBreakdownCountsTargetReasonCodes(t *testing.T) {
	stats := claudeLabelledEvaluationStats{}

	for _, reasonCode := range []string{
		ReasonCodeMissingTargetName,
		ReasonCodeMissingTargetNamespace,
		ReasonCodeWrongTargetName,
		ReasonCodeWrongTargetKind,
		ReasonCodeNamespaceMismatch,
		ReasonCodeMissingOwnerDeployment,
	} {
		accumulateClaudeLabelledEvaluationStats(
			&stats,
			ClassificationOutcome{},
			ValidationResult{
				Valid:      false,
				Decision:   DecisionFallbackMiss,
				ReasonCode: reasonCode,
			},
			true,
		)
	}

	if stats.TargetStructuralRejections != 6 {
		t.Fatalf(
			"target/structural rejections: got %d, want 6",
			stats.TargetStructuralRejections,
		)
	}

	if stats.OriginalRejected != 6 {
		t.Fatalf(
			"OriginalRejected: got %d, want 6",
			stats.OriginalRejected,
		)
	}
}

func TestClaudeRejectionBreakdownCountsSemanticGuardReasonCodes(t *testing.T) {
	stats := claudeLabelledEvaluationStats{}

	for _, reasonCode := range []string{
		ReasonCodeSemanticGuardRejected,
		ReasonCodeMissingDeploymentEvidence,
	} {
		accumulateClaudeLabelledEvaluationStats(
			&stats,
			ClassificationOutcome{},
			ValidationResult{
				Valid:      false,
				Decision:   DecisionFallbackMiss,
				ReasonCode: reasonCode,
			},
			true,
		)
	}

	if stats.SemanticGuardRejections != 2 {
		t.Fatalf(
			"semantic guard rejections: got %d, want 2",
			stats.SemanticGuardRejections,
		)
	}

	if stats.ReasonCodeCounts[ReasonCodeSemanticGuardRejected] != 1 {
		t.Fatalf(
			"semantic_guard_rejected count: got %d, want 1",
			stats.ReasonCodeCounts[ReasonCodeSemanticGuardRejected],
		)
	}

	if stats.ReasonCodeCounts[ReasonCodeMissingDeploymentEvidence] != 1 {
		t.Fatalf(
			"missing_deployment_evidence count: got %d, want 1",
			stats.ReasonCodeCounts[ReasonCodeMissingDeploymentEvidence],
		)
	}
}

func TestClaudeRejectionBreakdownCountsOtherReasonCodes(t *testing.T) {
	stats := claudeLabelledEvaluationStats{}

	accumulateClaudeLabelledEvaluationStats(
		&stats,
		ClassificationOutcome{},
		ValidationResult{
			Valid:      false,
			Decision:   DecisionFallbackMiss,
			ReasonCode: ReasonCodeUnsupportedAction,
		},
		true,
	)

	if stats.OtherRejections != 1 {
		t.Fatalf(
			"other rejections: got %d, want 1",
			stats.OtherRejections,
		)
	}
}

func TestClaudeRejectionBreakdownAccounting(t *testing.T) {
	stats := claudeLabelledEvaluationStats{}

	accumulateClaudeLabelledEvaluationStats(
		&stats,
		ClassificationOutcome{},
		ValidationResult{
			Valid:    true,
			Decision: DecisionAutomate,
		},
		true,
	)

	accumulateClaudeLabelledEvaluationStats(
		&stats,
		ClassificationOutcome{},
		ValidationResult{
			Valid:      false,
			Decision:   DecisionFallbackMiss,
			ReasonCode: ReasonCodeMissingTargetName,
		},
		true,
	)

	if err := stats.validateRejectionAccounting(2); err != nil {
		t.Fatalf("expected valid accounting: %v", err)
	}

	stats.ReasonCodeCounts[ReasonCodeWrongTargetName] = 1

	if err := stats.validateRejectionAccounting(2); err == nil {
		t.Fatal(
			"expected reason-code total mismatch to be rejected",
		)
	}
}
