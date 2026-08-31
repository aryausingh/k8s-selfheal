package classifier

import (
	"testing"
	"time"
)

func TestClassifierConfigFields(t *testing.T) {
	config := ClassifierConfig{
		Provider: ProviderMock,
		Model:    ProviderMock,
		Timeout:  5 * time.Second,
	}

	if config.Provider != ProviderMock {
		t.Fatalf(
			"expected provider %q, got %q",
			ProviderMock,
			config.Provider,
		)
	}

	if config.Model != ProviderMock {
		t.Fatalf(
			"expected model %q, got %q",
			ProviderMock,
			config.Model,
		)
	}

	if config.Timeout != 5*time.Second {
		t.Fatalf(
			"expected timeout %s, got %s",
			5*time.Second,
			config.Timeout,
		)
	}
}

func TestClaudeModelDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv(ClaudeModelEnv, "")

	classifier, err := NewClaudeClassifier()
	if err != nil {
		t.Fatalf("expected Claude classifier: %v", err)
	}

	if classifier.ModelName() != defaultClaudeModel {
		t.Fatalf(
			"expected default Claude model %q, got %q",
			defaultClaudeModel,
			classifier.ModelName(),
		)
	}

	if classifier.ProviderName() != ProviderClaude {
		t.Fatalf(
			"expected Claude provider %q, got %q",
			ProviderClaude,
			classifier.ProviderName(),
		)
	}
}

func TestClaudeModelOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv(ClaudeModelEnv, "claude-test-model")

	classifier, err := NewClaudeClassifier()
	if err != nil {
		t.Fatalf("expected Claude classifier: %v", err)
	}

	if classifier.ModelName() != "claude-test-model" {
		t.Fatalf(
			"expected overridden Claude model, got %q",
			classifier.ModelName(),
		)
	}
}

func TestMistralModelDefault(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "test-key")
	t.Setenv(MistralModelEnv, "")

	classifier, err := NewMistralClassifier()
	if err != nil {
		t.Fatalf("expected Mistral classifier: %v", err)
	}

	if classifier.ModelName() != defaultMistralModel {
		t.Fatalf(
			"expected default Mistral model %q, got %q",
			defaultMistralModel,
			classifier.ModelName(),
		)
	}

	if classifier.ProviderName() != ProviderMistral {
		t.Fatalf(
			"expected Mistral provider %q, got %q",
			ProviderMistral,
			classifier.ProviderName(),
		)
	}
}

func TestMistralModelOverride(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "test-key")
	t.Setenv(MistralModelEnv, "mistral-test-model")

	classifier, err := NewMistralClassifier()
	if err != nil {
		t.Fatalf("expected Mistral classifier: %v", err)
	}

	if classifier.ModelName() != "mistral-test-model" {
		t.Fatalf(
			"expected overridden Mistral model, got %q",
			classifier.ModelName(),
		)
	}
}

func TestClassifierConfigPreservesAPIKeyValidation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("MISTRAL_API_KEY", "")

	if _, err := NewClaudeClassifier(); err == nil {
		t.Fatal("expected missing Anthropic API key to be rejected")
	}

	if _, err := NewMistralClassifier(); err == nil {
		t.Fatal("expected missing Mistral API key to be rejected")
	}
}
