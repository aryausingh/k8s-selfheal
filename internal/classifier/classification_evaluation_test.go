package classifier

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type ClassificationEvaluationSummary struct {
	TotalCases int

	SuccessfulCalls int
	FailedCalls     int

	CorrectSubCause int
	CorrectAction   int
	CorrectSafety   int
	CorrectAll      int

	SubCauseAccuracy float64
	ActionAccuracy   float64
	SafetyAccuracy   float64
	OverallAccuracy  float64

	FalseAccepts int
	FalseRejects int

	FalseAcceptRate float64
	FalseRejectRate float64

	AverageLatency time.Duration
}

type ClassificationEvaluationResult struct {
	Case     labelledClassificationCase
	Proposal Proposal
	Error    error
	Latency  time.Duration
}

func CalculateClassificationEvaluation(
	cases []labelledClassificationCase,
	results []ClassificationEvaluationResult,
) ClassificationEvaluationSummary {

	summary := ClassificationEvaluationSummary{
		TotalCases: len(cases),
	}

	if summary.TotalCases == 0 {
		return summary
	}

	expectedAutomations := 0
	expectedNonAutomations := 0
	var totalLatency time.Duration

	for i, tc := range cases {
		expectedAutomation := expectedClassificationAutomation(tc)
		if expectedAutomation {
			expectedAutomations++
		} else {
			expectedNonAutomations++
		}

		result, hasResult := classificationEvaluationResultAt(results, i)
		if hasResult {
			totalLatency += result.Latency
		}

		if !hasResult || result.Error != nil {
			summary.FailedCalls++
			if expectedAutomation {
				summary.FalseRejects++
			}
			continue
		}

		summary.SuccessfulCalls++

		subCauseOK := result.Proposal.SubCause == tc.ExpectedSubCause
		actionOK := result.Proposal.RecommendedAction == tc.ExpectedAction
		safetyOK := result.Proposal.SafeForAutomation == tc.ExpectedSafeForAutomation

		if subCauseOK {
			summary.CorrectSubCause++
		}

		if actionOK {
			summary.CorrectAction++
		}

		if safetyOK {
			summary.CorrectSafety++
		}

		if subCauseOK && actionOK && safetyOK {
			summary.CorrectAll++
		}

		actualAutomation := actualClassificationAutomation(result.Proposal)
		if actualAutomation && !expectedAutomation {
			summary.FalseAccepts++
		}
		if !actualAutomation && expectedAutomation {
			summary.FalseRejects++
		}
	}

	summary.OverallAccuracy = classificationPercentage(
		summary.CorrectAll,
		summary.TotalCases,
	)
	summary.SubCauseAccuracy = classificationPercentage(
		summary.CorrectSubCause,
		summary.TotalCases,
	)
	summary.ActionAccuracy = classificationPercentage(
		summary.CorrectAction,
		summary.TotalCases,
	)
	summary.SafetyAccuracy = classificationPercentage(
		summary.CorrectSafety,
		summary.TotalCases,
	)
	summary.FalseAcceptRate = classificationPercentage(
		summary.FalseAccepts,
		expectedNonAutomations,
	)
	summary.FalseRejectRate = classificationPercentage(
		summary.FalseRejects,
		expectedAutomations,
	)
	summary.AverageLatency =
		totalLatency / time.Duration(summary.TotalCases)

	return summary
}

func FormatClassificationEvaluation(
	modelName string,
	summary ClassificationEvaluationSummary,
) string {

	return fmt.Sprintf(
		strings.Join(
			[]string{
				"Model: %s",
				"Measured on %d hand-labelled cases",
				"Total labelled cases: %d",
				"Successful model calls: %d",
				"Failed model calls: %d",
				"Correct complete proposals: %d",
				"Overall proposal accuracy: %.2f%%",
				"Sub-cause accuracy: %.2f%%",
				"Action accuracy: %.2f%%",
				"Safety-flag accuracy: %.2f%%",
				"False accepts: %d",
				"False rejects: %d",
				"False accept rate: %.2f%%",
				"False reject rate: %.2f%%",
				"Average Go measured latency per case: %s",
			},
			"\n",
		),
		modelName,
		summary.TotalCases,
		summary.TotalCases,
		summary.SuccessfulCalls,
		summary.FailedCalls,
		summary.CorrectAll,
		summary.OverallAccuracy,
		summary.SubCauseAccuracy,
		summary.ActionAccuracy,
		summary.SafetyAccuracy,
		summary.FalseAccepts,
		summary.FalseRejects,
		summary.FalseAcceptRate,
		summary.FalseRejectRate,
		summary.AverageLatency.Round(time.Millisecond),
	)
}

type ModelEvaluationCostSummary struct {
	Available             bool
	TotalCostUSD          float64
	AverageCostPerCallUSD float64
}

type ModelEvaluationValidatorSummary struct {
	Available        bool
	OriginalAccepted int
	OriginalRejected int
	Fallbacks        int
}

type ModelEvaluationComparisonInput struct {
	Model     string
	Summary   ClassificationEvaluationSummary
	Cost      ModelEvaluationCostSummary
	Validator ModelEvaluationValidatorSummary
}

type ModelEvaluationComparison struct {
	LeftModel  string
	RightModel string

	Left  ClassificationEvaluationSummary
	Right ClassificationEvaluationSummary

	LeftCost  ModelEvaluationCostSummary
	RightCost ModelEvaluationCostSummary

	LeftValidator  ModelEvaluationValidatorSummary
	RightValidator ModelEvaluationValidatorSummary
}

func CompareModelEvaluations(
	left ModelEvaluationComparisonInput,
	right ModelEvaluationComparisonInput,
) (ModelEvaluationComparison, error) {
	if left.Summary.TotalCases != right.Summary.TotalCases {
		return ModelEvaluationComparison{}, fmt.Errorf(
			"cannot compare model evaluations with different labelled dataset sizes: %s=%d %s=%d",
			left.Model,
			left.Summary.TotalCases,
			right.Model,
			right.Summary.TotalCases,
		)
	}

	return ModelEvaluationComparison{
		LeftModel:      left.Model,
		RightModel:     right.Model,
		Left:           left.Summary,
		Right:          right.Summary,
		LeftCost:       left.Cost,
		RightCost:      right.Cost,
		LeftValidator:  left.Validator,
		RightValidator: right.Validator,
	}, nil
}

func FormatModelComparison(
	comparison ModelEvaluationComparison,
) string {
	lines := []string{
		"MODEL COMPARISON",
		fmt.Sprintf(
			"Measured on %d hand-labelled cases",
			comparison.Left.TotalCases,
		),
		"",
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Metric",
			comparison.LeftModel,
			comparison.RightModel,
		),
		fmt.Sprintf(
			"%-28s %-20d %-20d",
			"Total labelled cases",
			comparison.Left.TotalCases,
			comparison.Right.TotalCases,
		),
		fmt.Sprintf(
			"%-28s %-20d %-20d",
			"Successful calls",
			comparison.Left.SuccessfulCalls,
			comparison.Right.SuccessfulCalls,
		),
		fmt.Sprintf(
			"%-28s %-20d %-20d",
			"Failed calls",
			comparison.Left.FailedCalls,
			comparison.Right.FailedCalls,
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Overall accuracy",
			modelComparisonPercent(
				comparison.Left.OverallAccuracy,
			),
			modelComparisonPercent(
				comparison.Right.OverallAccuracy,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Sub-cause accuracy",
			modelComparisonPercent(
				comparison.Left.SubCauseAccuracy,
			),
			modelComparisonPercent(
				comparison.Right.SubCauseAccuracy,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Action accuracy",
			modelComparisonPercent(
				comparison.Left.ActionAccuracy,
			),
			modelComparisonPercent(
				comparison.Right.ActionAccuracy,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Safety accuracy",
			modelComparisonPercent(
				comparison.Left.SafetyAccuracy,
			),
			modelComparisonPercent(
				comparison.Right.SafetyAccuracy,
			),
		),
		fmt.Sprintf(
			"%-28s %-20d %-20d",
			"False accepts",
			comparison.Left.FalseAccepts,
			comparison.Right.FalseAccepts,
		),
		fmt.Sprintf(
			"%-28s %-20d %-20d",
			"False rejects",
			comparison.Left.FalseRejects,
			comparison.Right.FalseRejects,
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"False accept rate",
			modelComparisonPercent(
				comparison.Left.FalseAcceptRate,
			),
			modelComparisonPercent(
				comparison.Right.FalseAcceptRate,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"False reject rate",
			modelComparisonPercent(
				comparison.Left.FalseRejectRate,
			),
			modelComparisonPercent(
				comparison.Right.FalseRejectRate,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Average latency",
			comparison.Left.AverageLatency.Round(
				time.Millisecond,
			).String(),
			comparison.Right.AverageLatency.Round(
				time.Millisecond,
			).String(),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Total cost",
			modelComparisonTotalCost(
				comparison.LeftCost,
			),
			modelComparisonTotalCost(
				comparison.RightCost,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Average cost per call",
			modelComparisonAverageCost(
				comparison.LeftCost,
			),
			modelComparisonAverageCost(
				comparison.RightCost,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Validator acceptance",
			modelComparisonValidatorAcceptance(
				comparison.LeftValidator,
			),
			modelComparisonValidatorAcceptance(
				comparison.RightValidator,
			),
		),
		fmt.Sprintf(
			"%-28s %-20s %-20s",
			"Fallback rate",
			modelComparisonFallbackRate(
				comparison.LeftValidator,
				comparison.Left.TotalCases,
			),
			modelComparisonFallbackRate(
				comparison.RightValidator,
				comparison.Right.TotalCases,
			),
		),
	}

	return strings.Join(lines, "\n")
}

func modelComparisonPercent(value float64) string {
	return fmt.Sprintf("%.2f%%", value)
}

func modelComparisonTotalCost(
	cost ModelEvaluationCostSummary,
) string {
	if !cost.Available {
		return "N/A"
	}

	return fmt.Sprintf("$%.6f", cost.TotalCostUSD)
}

func modelComparisonAverageCost(
	cost ModelEvaluationCostSummary,
) string {
	if !cost.Available {
		return "N/A"
	}

	return fmt.Sprintf("$%.6f", cost.AverageCostPerCallUSD)
}

func modelComparisonValidatorAcceptance(
	validator ModelEvaluationValidatorSummary,
) string {
	if !validator.Available {
		return "N/A"
	}

	total := validator.OriginalAccepted +
		validator.OriginalRejected

	return fmt.Sprintf(
		"%d/%d (%.2f%%)",
		validator.OriginalAccepted,
		total,
		classificationPercentage(
			validator.OriginalAccepted,
			total,
		),
	)
}

func modelComparisonFallbackRate(
	validator ModelEvaluationValidatorSummary,
	totalCases int,
) string {
	if !validator.Available {
		return "N/A"
	}

	return fmt.Sprintf(
		"%d/%d (%.2f%%)",
		validator.Fallbacks,
		totalCases,
		classificationPercentage(
			validator.Fallbacks,
			totalCases,
		),
	)
}

func classificationEvaluationResultAt(
	results []ClassificationEvaluationResult,
	index int,
) (ClassificationEvaluationResult, bool) {

	if index < 0 || index >= len(results) {
		return ClassificationEvaluationResult{}, false
	}

	return results[index], true
}

func classificationPercentage(numerator int, denominator int) float64 {
	if denominator == 0 {
		return 0
	}

	return float64(numerator) / float64(denominator) * 100
}

func expectedClassificationAutomation(tc labelledClassificationCase) bool {
	return tc.ExpectedSafeForAutomation &&
		isExecutableClassificationAction(tc.ExpectedAction)
}

func actualClassificationAutomation(proposal Proposal) bool {
	return proposal.SafeForAutomation &&
		isExecutableClassificationAction(proposal.RecommendedAction)
}

func isExecutableClassificationAction(action string) bool {
	switch action {
	case ActionRestartPod, ActionRolloutUndo:
		return true
	default:
		return false
	}
}

func TestCalculateClassificationEvaluationPerfectPredictions(t *testing.T) {
	cases := []labelledClassificationCase{
		classificationEvaluationCase(
			"restart",
			"transient_failure",
			ActionRestartPod,
			true,
		),
		classificationEvaluationCase(
			"escalate",
			"bad_config",
			ActionEscalateToHuman,
			false,
		),
	}

	results := []ClassificationEvaluationResult{
		classificationEvaluationResult(cases[0], nil, 10*time.Millisecond),
		classificationEvaluationResult(cases[1], nil, 20*time.Millisecond),
	}

	summary := CalculateClassificationEvaluation(cases, results)

	if summary.TotalCases != 2 {
		t.Fatalf("TotalCases: got %d, want 2", summary.TotalCases)
	}
	if summary.SuccessfulCalls != 2 {
		t.Fatalf("SuccessfulCalls: got %d, want 2", summary.SuccessfulCalls)
	}
	if summary.FailedCalls != 0 {
		t.Fatalf("FailedCalls: got %d, want 0", summary.FailedCalls)
	}
	if summary.CorrectAll != 2 {
		t.Fatalf("CorrectAll: got %d, want 2", summary.CorrectAll)
	}
	if summary.OverallAccuracy != 100 {
		t.Fatalf("OverallAccuracy: got %.2f, want 100", summary.OverallAccuracy)
	}
	if summary.SubCauseAccuracy != 100 {
		t.Fatalf("SubCauseAccuracy: got %.2f, want 100", summary.SubCauseAccuracy)
	}
	if summary.ActionAccuracy != 100 {
		t.Fatalf("ActionAccuracy: got %.2f, want 100", summary.ActionAccuracy)
	}
	if summary.SafetyAccuracy != 100 {
		t.Fatalf("SafetyAccuracy: got %.2f, want 100", summary.SafetyAccuracy)
	}
	if summary.FalseAccepts != 0 {
		t.Fatalf("FalseAccepts: got %d, want 0", summary.FalseAccepts)
	}
	if summary.FalseRejects != 0 {
		t.Fatalf("FalseRejects: got %d, want 0", summary.FalseRejects)
	}
	if summary.AverageLatency != 15*time.Millisecond {
		t.Fatalf(
			"AverageLatency: got %s, want %s",
			summary.AverageLatency,
			15*time.Millisecond,
		)
	}
}

func TestCalculateClassificationEvaluationFailedCallUsesTotalDenominator(t *testing.T) {
	cases := []labelledClassificationCase{
		classificationEvaluationCase(
			"restart",
			"transient_failure",
			ActionRestartPod,
			true,
		),
		classificationEvaluationCase(
			"escalate",
			"bad_config",
			ActionEscalateToHuman,
			false,
		),
	}

	results := []ClassificationEvaluationResult{
		classificationEvaluationResult(cases[0], nil, 10*time.Millisecond),
		classificationEvaluationResult(cases[1], errors.New("timeout"), 30*time.Millisecond),
	}

	summary := CalculateClassificationEvaluation(cases, results)

	if summary.TotalCases != 2 {
		t.Fatalf("TotalCases: got %d, want 2", summary.TotalCases)
	}
	if summary.SuccessfulCalls != 1 {
		t.Fatalf("SuccessfulCalls: got %d, want 1", summary.SuccessfulCalls)
	}
	if summary.FailedCalls != 1 {
		t.Fatalf("FailedCalls: got %d, want 1", summary.FailedCalls)
	}
	if summary.CorrectAll != 1 {
		t.Fatalf("CorrectAll: got %d, want 1", summary.CorrectAll)
	}
	if summary.OverallAccuracy != 50 {
		t.Fatalf(
			"OverallAccuracy: got %.2f, want 50",
			summary.OverallAccuracy,
		)
	}
}

func TestCalculateClassificationEvaluationWrongSubCause(t *testing.T) {
	tc := classificationEvaluationCase(
		"restart",
		"transient_failure",
		ActionRestartPod,
		true,
	)

	result := classificationEvaluationResult(tc, nil, time.Millisecond)
	result.Proposal.SubCause = "bad_config"

	summary := CalculateClassificationEvaluation(
		[]labelledClassificationCase{tc},
		[]ClassificationEvaluationResult{result},
	)

	if summary.CorrectSubCause != 0 {
		t.Fatalf("CorrectSubCause: got %d, want 0", summary.CorrectSubCause)
	}
	if summary.SubCauseAccuracy != 0 {
		t.Fatalf("SubCauseAccuracy: got %.2f, want 0", summary.SubCauseAccuracy)
	}
	if summary.CorrectAll != 0 {
		t.Fatalf("CorrectAll: got %d, want 0", summary.CorrectAll)
	}
	if summary.OverallAccuracy != 0 {
		t.Fatalf("OverallAccuracy: got %.2f, want 0", summary.OverallAccuracy)
	}
}

func TestCalculateClassificationEvaluationWrongAction(t *testing.T) {
	tc := classificationEvaluationCase(
		"escalate",
		"bad_config",
		ActionEscalateToHuman,
		false,
	)

	result := classificationEvaluationResult(tc, nil, time.Millisecond)
	result.Proposal.RecommendedAction = ActionRestartPod

	summary := CalculateClassificationEvaluation(
		[]labelledClassificationCase{tc},
		[]ClassificationEvaluationResult{result},
	)

	if summary.CorrectAction != 0 {
		t.Fatalf("CorrectAction: got %d, want 0", summary.CorrectAction)
	}
	if summary.ActionAccuracy != 0 {
		t.Fatalf("ActionAccuracy: got %.2f, want 0", summary.ActionAccuracy)
	}
	if summary.CorrectAll != 0 {
		t.Fatalf("CorrectAll: got %d, want 0", summary.CorrectAll)
	}
}

func TestCalculateClassificationEvaluationWrongSafety(t *testing.T) {
	tc := classificationEvaluationCase(
		"escalate",
		"bad_config",
		ActionEscalateToHuman,
		false,
	)

	result := classificationEvaluationResult(tc, nil, time.Millisecond)
	result.Proposal.SafeForAutomation = true

	summary := CalculateClassificationEvaluation(
		[]labelledClassificationCase{tc},
		[]ClassificationEvaluationResult{result},
	)

	if summary.CorrectSafety != 0 {
		t.Fatalf("CorrectSafety: got %d, want 0", summary.CorrectSafety)
	}
	if summary.SafetyAccuracy != 0 {
		t.Fatalf("SafetyAccuracy: got %.2f, want 0", summary.SafetyAccuracy)
	}
	if summary.CorrectAll != 0 {
		t.Fatalf("CorrectAll: got %d, want 0", summary.CorrectAll)
	}
}

func TestCalculateClassificationEvaluationFalseAccept(t *testing.T) {
	tc := classificationEvaluationCase(
		"escalate",
		"bad_config",
		ActionEscalateToHuman,
		false,
	)

	result := classificationEvaluationResult(tc, nil, time.Millisecond)
	result.Proposal.RecommendedAction = ActionRestartPod
	result.Proposal.SafeForAutomation = true

	summary := CalculateClassificationEvaluation(
		[]labelledClassificationCase{tc},
		[]ClassificationEvaluationResult{result},
	)

	if summary.FalseAccepts != 1 {
		t.Fatalf("FalseAccepts: got %d, want 1", summary.FalseAccepts)
	}
	if summary.FalseAcceptRate != 100 {
		t.Fatalf(
			"FalseAcceptRate: got %.2f, want 100",
			summary.FalseAcceptRate,
		)
	}
}

func TestCalculateClassificationEvaluationFalseReject(t *testing.T) {
	tc := classificationEvaluationCase(
		"restart",
		"transient_failure",
		ActionRestartPod,
		true,
	)

	result := classificationEvaluationResult(tc, nil, time.Millisecond)
	result.Proposal.SafeForAutomation = false

	summary := CalculateClassificationEvaluation(
		[]labelledClassificationCase{tc},
		[]ClassificationEvaluationResult{result},
	)

	if summary.FalseRejects != 1 {
		t.Fatalf("FalseRejects: got %d, want 1", summary.FalseRejects)
	}
	if summary.FalseRejectRate != 100 {
		t.Fatalf(
			"FalseRejectRate: got %.2f, want 100",
			summary.FalseRejectRate,
		)
	}
}

func TestCalculateClassificationEvaluationZeroCases(t *testing.T) {
	summary := CalculateClassificationEvaluation(nil, nil)

	if summary.TotalCases != 0 {
		t.Fatalf("TotalCases: got %d, want 0", summary.TotalCases)
	}
	if summary.OverallAccuracy != 0 {
		t.Fatalf("OverallAccuracy: got %.2f, want 0", summary.OverallAccuracy)
	}
	if summary.SubCauseAccuracy != 0 {
		t.Fatalf("SubCauseAccuracy: got %.2f, want 0", summary.SubCauseAccuracy)
	}
	if summary.ActionAccuracy != 0 {
		t.Fatalf("ActionAccuracy: got %.2f, want 0", summary.ActionAccuracy)
	}
	if summary.SafetyAccuracy != 0 {
		t.Fatalf("SafetyAccuracy: got %.2f, want 0", summary.SafetyAccuracy)
	}
	if summary.FalseAcceptRate != 0 {
		t.Fatalf("FalseAcceptRate: got %.2f, want 0", summary.FalseAcceptRate)
	}
	if summary.FalseRejectRate != 0 {
		t.Fatalf("FalseRejectRate: got %.2f, want 0", summary.FalseRejectRate)
	}
	if summary.AverageLatency != 0 {
		t.Fatalf("AverageLatency: got %s, want 0", summary.AverageLatency)
	}
}

func TestFormatClassificationEvaluationReportWording(t *testing.T) {
	report := FormatClassificationEvaluation(
		"test-model",
		ClassificationEvaluationSummary{
			TotalCases: 3,
		},
	)

	if !strings.Contains(report, "Measured on 3 hand-labelled cases") {
		t.Fatalf("report missing measured-case wording:\n%s", report)
	}
}

func TestModelComparisonSameDatasetComparesSuccessfully(t *testing.T) {
	comparison, err := CompareModelEvaluations(
		ModelEvaluationComparisonInput{
			Model:   "Claude Sonnet 5",
			Summary: modelComparisonSummary(20),
		},
		ModelEvaluationComparisonInput{
			Model:   "Mistral-7B",
			Summary: modelComparisonSummary(20),
		},
	)

	if err != nil {
		t.Fatalf("expected comparison: %v", err)
	}

	if comparison.LeftModel != "Claude Sonnet 5" {
		t.Fatalf(
			"LeftModel: got %q",
			comparison.LeftModel,
		)
	}

	if comparison.RightModel != "Mistral-7B" {
		t.Fatalf(
			"RightModel: got %q",
			comparison.RightModel,
		)
	}
}

func TestModelComparisonRejectsDifferentDatasetSizes(t *testing.T) {
	_, err := CompareModelEvaluations(
		ModelEvaluationComparisonInput{
			Model:   "Claude Sonnet 5",
			Summary: modelComparisonSummary(20),
		},
		ModelEvaluationComparisonInput{
			Model:   "Mistral-7B",
			Summary: modelComparisonSummary(19),
		},
	)

	if err == nil {
		t.Fatal(
			"expected different dataset sizes to be rejected",
		)
	}
}

func TestModelComparisonPreservesAccuracyValues(t *testing.T) {
	left := modelComparisonSummary(20)
	left.OverallAccuracy = 95
	left.SubCauseAccuracy = 90
	left.ActionAccuracy = 85
	left.SafetyAccuracy = 80

	right := modelComparisonSummary(20)
	right.OverallAccuracy = 75
	right.SubCauseAccuracy = 70
	right.ActionAccuracy = 65
	right.SafetyAccuracy = 60

	comparison, err := CompareModelEvaluations(
		ModelEvaluationComparisonInput{
			Model:   "Claude Sonnet 5",
			Summary: left,
		},
		ModelEvaluationComparisonInput{
			Model:   "Mistral-7B",
			Summary: right,
		},
	)
	if err != nil {
		t.Fatalf("expected comparison: %v", err)
	}

	if comparison.Left.OverallAccuracy != 95 ||
		comparison.Left.SubCauseAccuracy != 90 ||
		comparison.Right.ActionAccuracy != 65 ||
		comparison.Right.SafetyAccuracy != 60 {

		t.Fatalf(
			"comparison did not preserve accuracy values: %#v",
			comparison,
		)
	}
}

func TestModelComparisonPreservesFalseAcceptRejectValues(t *testing.T) {
	left := modelComparisonSummary(20)
	left.FalseAccepts = 1
	left.FalseRejects = 2
	left.FalseAcceptRate = 12.5
	left.FalseRejectRate = 25

	right := modelComparisonSummary(20)
	right.FalseAccepts = 3
	right.FalseRejects = 4
	right.FalseAcceptRate = 37.5
	right.FalseRejectRate = 50

	comparison, err := CompareModelEvaluations(
		ModelEvaluationComparisonInput{
			Model:   "Claude Sonnet 5",
			Summary: left,
		},
		ModelEvaluationComparisonInput{
			Model:   "Mistral-7B",
			Summary: right,
		},
	)
	if err != nil {
		t.Fatalf("expected comparison: %v", err)
	}

	if comparison.Left.FalseAccepts != 1 ||
		comparison.Left.FalseRejects != 2 ||
		comparison.Right.FalseAccepts != 3 ||
		comparison.Right.FalseRejects != 4 ||
		comparison.Right.FalseRejectRate != 50 {

		t.Fatalf(
			"comparison did not preserve false accept/reject values: %#v",
			comparison,
		)
	}
}

func TestModelComparisonUnavailableCostIsNotInvented(t *testing.T) {
	comparison, err := CompareModelEvaluations(
		ModelEvaluationComparisonInput{
			Model:   "Claude Sonnet 5",
			Summary: modelComparisonSummary(20),
			Cost: ModelEvaluationCostSummary{
				Available:             true,
				TotalCostUSD:          0.12,
				AverageCostPerCallUSD: 0.006,
			},
		},
		ModelEvaluationComparisonInput{
			Model:   "Mistral-7B",
			Summary: modelComparisonSummary(20),
		},
	)
	if err != nil {
		t.Fatalf("expected comparison: %v", err)
	}

	if comparison.RightCost.Available {
		t.Fatal("expected Mistral cost to be unavailable")
	}

	report := FormatModelComparison(comparison)
	if !strings.Contains(report, "N/A") {
		t.Fatalf(
			"expected unavailable cost to be formatted as N/A:\n%s",
			report,
		)
	}
}

func TestModelComparisonReportContainsMeasuredCasesAndModelNames(t *testing.T) {
	comparison, err := CompareModelEvaluations(
		ModelEvaluationComparisonInput{
			Model:   "Claude Sonnet 5",
			Summary: modelComparisonSummary(20),
			Validator: ModelEvaluationValidatorSummary{
				Available:        true,
				OriginalAccepted: 9,
				OriginalRejected: 11,
				Fallbacks:        11,
			},
		},
		ModelEvaluationComparisonInput{
			Model:   "Mistral-7B",
			Summary: modelComparisonSummary(20),
		},
	)
	if err != nil {
		t.Fatalf("expected comparison: %v", err)
	}

	report := FormatModelComparison(comparison)

	for _, want := range []string{
		"MODEL COMPARISON",
		"Measured on 20 hand-labelled cases",
		"Claude Sonnet 5",
		"Mistral-7B",
		"Overall accuracy",
		"Validator acceptance",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf(
				"comparison report missing %q:\n%s",
				want,
				report,
			)
		}
	}
}

func TestMeasuredClaudeVsMistralComparison(t *testing.T) {
	claude := measuredClaudeSonnet5Evaluation()
	mistral := measuredMistral7BEvaluation()

	comparison, err := CompareModelEvaluations(
		claude,
		mistral,
	)
	if err != nil {
		t.Fatalf("expected measured comparison: %v", err)
	}

	if comparison.Left.TotalCases != 20 ||
		comparison.Right.TotalCases != 20 {

		t.Fatalf(
			"expected both models measured on 20 cases, got left=%d right=%d",
			comparison.Left.TotalCases,
			comparison.Right.TotalCases,
		)
	}

	if comparison.Left.SuccessfulCalls+
		comparison.Left.FailedCalls != 20 {

		t.Fatalf(
			"Claude accounting mismatch: successful=%d failed=%d",
			comparison.Left.SuccessfulCalls,
			comparison.Left.FailedCalls,
		)
	}

	if comparison.Right.SuccessfulCalls+
		comparison.Right.FailedCalls != 20 {

		t.Fatalf(
			"Mistral accounting mismatch: successful=%d failed=%d",
			comparison.Right.SuccessfulCalls,
			comparison.Right.FailedCalls,
		)
	}

	if comparison.Left.CorrectAll != 20 ||
		comparison.Left.OverallAccuracy != 100 {

		t.Fatalf(
			"unexpected Claude accuracy: correct=%d accuracy=%.2f",
			comparison.Left.CorrectAll,
			comparison.Left.OverallAccuracy,
		)
	}

	if comparison.Right.CorrectAll != 13 ||
		comparison.Right.OverallAccuracy != 65 {

		t.Fatalf(
			"unexpected Mistral accuracy: correct=%d accuracy=%.2f",
			comparison.Right.CorrectAll,
			comparison.Right.OverallAccuracy,
		)
	}

	if comparison.LeftValidator.OriginalAccepted+
		comparison.LeftValidator.OriginalRejected != 20 {

		t.Fatalf(
			"Claude validator accounting mismatch: accepted=%d rejected=%d",
			comparison.LeftValidator.OriginalAccepted,
			comparison.LeftValidator.OriginalRejected,
		)
	}

	if comparison.LeftValidator.Fallbacks >
		comparison.Left.TotalCases {

		t.Fatalf(
			"Claude fallback count exceeds total cases: fallbacks=%d total=%d",
			comparison.LeftValidator.Fallbacks,
			comparison.Left.TotalCases,
		)
	}

	if comparison.LeftCost.TotalCostUSD != 0.117972 {
		t.Fatalf(
			"Claude total cost: got %.6f",
			comparison.LeftCost.TotalCostUSD,
		)
	}

	if comparison.LeftCost.AverageCostPerCallUSD != 0.005899 {
		t.Fatalf(
			"Claude average cost: got %.6f",
			comparison.LeftCost.AverageCostPerCallUSD,
		)
	}

	if comparison.RightCost.Available {
		t.Fatal("Mistral local inference cost must be N/A")
	}

	report := FormatMeasuredClaudeVsMistralComparison(
		comparison,
	)

	for _, want := range []string{
		"Measured on 20 hand-labelled cases",
		"Claude Sonnet 5",
		"Mistral-7B",
		"Successful calls",
		"Failed calls",
		"Overall accuracy",
		"Sub-cause accuracy",
		"Action accuracy",
		"Safety accuracy",
		"False accepts",
		"False rejects",
		"False accept rate",
		"False reject rate",
		"Average latency",
		"Average cost per call",
		"Total cost",
		"Original proposal acceptance: 9/20 = 45.00%",
		"Original proposal rejection: 11/20 = 55.00%",
		"Fallback: 11/20 = 55.00%",
		"Total input tokens: 35141",
		"Total output tokens: 4769",
		"Total tokens: 39910",
		"Known-cost calls: 20",
		"Final escalated classifications: 17",
		"Mistral-7B average Ollama reported generation duration: 69.202s",
		"N/A",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf(
				"measured report missing %q:\n%s",
				want,
				report,
			)
		}
	}

	t.Log("\n" + report)
}

func FormatMeasuredClaudeVsMistralComparison(
	comparison ModelEvaluationComparison,
) string {
	report := FormatModelComparison(comparison)

	totalCases := comparison.Left.TotalCases
	validator := comparison.LeftValidator

	lines := []string{
		report,
		"",
		"CLAUDE VALIDATOR/FALLBACK DETAIL",
		fmt.Sprintf(
			"Original proposal acceptance: %d/%d = %.2f%%",
			validator.OriginalAccepted,
			totalCases,
			classificationPercentage(
				validator.OriginalAccepted,
				totalCases,
			),
		),
		fmt.Sprintf(
			"Original proposal rejection: %d/%d = %.2f%%",
			validator.OriginalRejected,
			totalCases,
			classificationPercentage(
				validator.OriginalRejected,
				totalCases,
			),
		),
		fmt.Sprintf(
			"Fallback: %d/%d = %.2f%%",
			validator.Fallbacks,
			totalCases,
			classificationPercentage(
				validator.Fallbacks,
				totalCases,
			),
		),
		"",
		"CLAUDE TOKEN/COST DETAIL",
		fmt.Sprintf(
			"Total input tokens: %d",
			measuredClaudeSonnet5TotalInputTokens,
		),
		fmt.Sprintf(
			"Total output tokens: %d",
			measuredClaudeSonnet5TotalOutputTokens,
		),
		fmt.Sprintf(
			"Total tokens: %d",
			measuredClaudeSonnet5TotalTokens,
		),
		fmt.Sprintf(
			"Known-cost calls: %d",
			measuredClaudeSonnet5KnownCostCalls,
		),
		fmt.Sprintf(
			"Final escalated classifications: %d",
			measuredClaudeSonnet5FinalEscalated,
		),
		"",
		"MISTRAL-7B LOCAL DETAIL",
		fmt.Sprintf(
			"Mistral-7B average Ollama reported generation duration: %.3fs",
			measuredMistral7BAverageOllamaDuration.Seconds(),
		),
		"",
		"NEUTRAL SUMMARY",
		"- Claude had higher classification accuracy and lower measured latency in this environment.",
		"- Mistral-7B was local/offline and had no API cost.",
		"- Claude required deterministic fallback for 11/20 original proposals.",
	}

	return strings.Join(lines, "\n")
}

const (
	measuredClaudeSonnet5TotalInputTokens      = 35141
	measuredClaudeSonnet5TotalOutputTokens     = 4769
	measuredClaudeSonnet5TotalTokens           = 39910
	measuredClaudeSonnet5KnownCostCalls        = 20
	measuredClaudeSonnet5FinalEscalated        = 17
	measuredMistral7BAverageOllamaDuration     = 69202 * time.Millisecond
	measuredClaudeSonnet5TotalEstimatedCostUSD = 0.117972
	measuredClaudeSonnet5AverageCostPerCallUSD = 0.005899
)

// Values copied from completed labelled evaluation runs on 20
// hand-labelled cases. These are measured experimental results,
// not universal benchmark defaults.
func measuredClaudeSonnet5Evaluation() ModelEvaluationComparisonInput {
	return ModelEvaluationComparisonInput{
		Model: "Claude Sonnet 5",
		Summary: ClassificationEvaluationSummary{
			TotalCases:       20,
			SuccessfulCalls:  20,
			FailedCalls:      0,
			CorrectSubCause:  20,
			CorrectAction:    20,
			CorrectSafety:    20,
			CorrectAll:       20,
			SubCauseAccuracy: 100,
			ActionAccuracy:   100,
			SafetyAccuracy:   100,
			OverallAccuracy:  100,
			FalseAccepts:     0,
			FalseRejects:     0,
			FalseAcceptRate:  0,
			FalseRejectRate:  0,
			AverageLatency:   5276 * time.Millisecond,
		},
		Cost: ModelEvaluationCostSummary{
			Available:             true,
			TotalCostUSD:          measuredClaudeSonnet5TotalEstimatedCostUSD,
			AverageCostPerCallUSD: measuredClaudeSonnet5AverageCostPerCallUSD,
		},
		Validator: ModelEvaluationValidatorSummary{
			Available:        true,
			OriginalAccepted: 9,
			OriginalRejected: 11,
			Fallbacks:        11,
		},
	}
}

// Values copied from a completed local Ollama labelled evaluation
// run on the same 20 hand-labelled cases. Local/offline inference
// has no measured USD API cost here.
func measuredMistral7BEvaluation() ModelEvaluationComparisonInput {
	return ModelEvaluationComparisonInput{
		Model: "Mistral-7B",
		Summary: ClassificationEvaluationSummary{
			TotalCases:       20,
			SuccessfulCalls:  19,
			FailedCalls:      1,
			CorrectSubCause:  13,
			CorrectAction:    15,
			CorrectSafety:    17,
			CorrectAll:       13,
			SubCauseAccuracy: 65,
			ActionAccuracy:   75,
			SafetyAccuracy:   85,
			OverallAccuracy:  65,
			FalseAccepts:     1,
			FalseRejects:     2,
			FalseAcceptRate:  8.33,
			FalseRejectRate:  25,
			AverageLatency:   71843 * time.Millisecond,
		},
		Cost: ModelEvaluationCostSummary{
			Available: false,
		},
		Validator: ModelEvaluationValidatorSummary{
			Available: false,
		},
	}
}

func classificationEvaluationCase(
	name string,
	subCause string,
	action string,
	safe bool,
) labelledClassificationCase {

	return labelledClassificationCase{
		Name:                      name,
		ExpectedSubCause:          subCause,
		ExpectedAction:            action,
		ExpectedSafeForAutomation: safe,
	}
}

func classificationEvaluationResult(
	tc labelledClassificationCase,
	err error,
	latency time.Duration,
) ClassificationEvaluationResult {

	return ClassificationEvaluationResult{
		Case: tc,
		Proposal: Proposal{
			SubCause:          tc.ExpectedSubCause,
			RecommendedAction: tc.ExpectedAction,
			SafeForAutomation: tc.ExpectedSafeForAutomation,
			Reasoning:         "synthetic test proposal",
		},
		Error:   err,
		Latency: latency,
	}
}

func modelComparisonSummary(
	totalCases int,
) ClassificationEvaluationSummary {
	return ClassificationEvaluationSummary{
		TotalCases:       totalCases,
		SuccessfulCalls:  totalCases,
		OverallAccuracy:  90,
		SubCauseAccuracy: 91,
		ActionAccuracy:   92,
		SafetyAccuracy:   93,
		FalseAccepts:     1,
		FalseRejects:     2,
		FalseAcceptRate:  10,
		FalseRejectRate:  20,
		AverageLatency:   150 * time.Millisecond,
		CorrectSubCause:  18,
		CorrectAction:    18,
		CorrectSafety:    18,
		CorrectAll:       18,
		FailedCalls:      0,
	}
}
