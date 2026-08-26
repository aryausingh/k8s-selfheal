package classifier

import "strings"

const claudeSonnet5Model = "claude-sonnet-5"

type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

type ModelPricing struct {
	InputPerMillionUSD  float64
	OutputPerMillionUSD float64
}

func (u TokenUsage) TotalTokens() int {
	return nonNegativeInt(u.InputTokens) +
		nonNegativeInt(u.OutputTokens)
}

func EstimateCostUSD(
	usage TokenUsage,
	pricing ModelPricing,
) float64 {
	inputCost :=
		float64(nonNegativeInt(usage.InputTokens)) /
			1_000_000 *
			nonNegativeFloat(pricing.InputPerMillionUSD)

	outputCost :=
		float64(nonNegativeInt(usage.OutputTokens)) /
			1_000_000 *
			nonNegativeFloat(pricing.OutputPerMillionUSD)

	return inputCost + outputCost
}

func PricingForModel(model string) (ModelPricing, bool) {
	switch strings.TrimSpace(model) {
	case claudeSonnet5Model:
		return ModelPricing{
			InputPerMillionUSD:  2,
			OutputPerMillionUSD: 10,
		}, true
	default:
		return ModelPricing{}, false
	}
}

func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}

	return value
}

func nonNegativeFloat(value float64) float64 {
	if value < 0 {
		return 0
	}

	return value
}
