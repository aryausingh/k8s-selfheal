package classifier

import (
	"os"
	"strings"
	"time"
)

const (
	ProviderClaude  = "anthropic"
	ProviderMistral = "mistral"
	ProviderMock    = "mock"
)

const (
	ClaudeModelEnv  = "CLAUDE_MODEL"
	MistralModelEnv = "MISTRAL_MODEL"
)

type ClassifierConfig struct {
	Provider string
	Model    string
	Timeout  time.Duration
}

func modelFromEnv(
	envName string,
	defaultModel string,
) string {
	if model := strings.TrimSpace(
		os.Getenv(envName),
	); model != "" {
		return model
	}

	return defaultModel
}
