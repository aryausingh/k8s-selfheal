package classifier

import (
	"math"
	"testing"
)

func TestEstimateCostUSDZeroTokens(t *testing.T) {
	cost := EstimateCostUSD(
		TokenUsage{},
		ModelPricing{
			InputPerMillionUSD:  3,
			OutputPerMillionUSD: 15,
		},
	)

	if cost != 0 {
		t.Fatalf("expected zero cost, got %.6f", cost)
	}
}

func TestEstimateCostUSDFakePricing(t *testing.T) {
	cost := EstimateCostUSD(
		TokenUsage{
			InputTokens:  500_000,
			OutputTokens: 250_000,
		},
		ModelPricing{
			InputPerMillionUSD:  2,
			OutputPerMillionUSD: 8,
		},
	)

	expected := 3.0

	if math.Abs(cost-expected) > 0.000001 {
		t.Fatalf(
			"expected cost %.6f, got %.6f",
			expected,
			cost,
		)
	}
}

func TestEstimateCostUSDClaudeSonnet5Pricing(t *testing.T) {
	pricing, ok := PricingForModel(
		claudeSonnet5Model,
	)
	if !ok {
		t.Fatal("expected Claude Sonnet 5 pricing")
	}

	cost := EstimateCostUSD(
		TokenUsage{
			InputTokens:  1000,
			OutputTokens: 200,
		},
		pricing,
	)

	expected := 0.004

	if math.Abs(cost-expected) > 0.000001 {
		t.Fatalf(
			"expected cost %.6f, got %.6f",
			expected,
			cost,
		)
	}
}

func TestPricingForModelUnknownClaudeModel(t *testing.T) {
	pricing, ok := PricingForModel(
		"claude-unconfigured-model",
	)

	if ok {
		t.Fatalf(
			"expected unknown Claude model to have no pricing, got %#v",
			pricing,
		)
	}
}

func TestCostTokenUsageTotalTokens(t *testing.T) {
	usage := TokenUsage{
		InputTokens:  123,
		OutputTokens: 456,
	}

	if usage.TotalTokens() != 579 {
		t.Fatalf(
			"expected total tokens 579, got %d",
			usage.TotalTokens(),
		)
	}
}

func TestEstimateCostUSDNegativeTokensAreClamped(t *testing.T) {
	usage := TokenUsage{
		InputTokens:  -100,
		OutputTokens: 200_000,
	}

	cost := EstimateCostUSD(
		usage,
		ModelPricing{
			InputPerMillionUSD:  10,
			OutputPerMillionUSD: 5,
		},
	)

	expected := 1.0

	if usage.TotalTokens() != 200_000 {
		t.Fatalf(
			"expected negative input tokens to be clamped in total, got %d",
			usage.TotalTokens(),
		)
	}

	if math.Abs(cost-expected) > 0.000001 {
		t.Fatalf(
			"expected cost %.6f, got %.6f",
			expected,
			cost,
		)
	}
}
