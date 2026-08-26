package classifier

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSystemPromptTargetMappingRules(t *testing.T) {
	prompt := BuildSystemPrompt()

	required := []string{
		`target.kind MUST be "Pod"`,
		"target.namespace MUST exactly equal the incident namespace",
		"target.name MUST exactly equal the incident pod_name",
		`target.kind MUST be "Deployment"`,
		"target.name MUST exactly equal owner_deployment",
		"target should identify the affected Pod using its exact namespace and pod_name",
		"Never return an empty target field.",
	}

	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf(
				"expected system prompt to contain %q",
				want,
			)
		}
	}
}

func TestBuildUserPromptIncludesCanonicalResourceIdentifiers(t *testing.T) {
	input := promptTestIncident()

	prompt, err := BuildUserPrompt(input)
	if err != nil {
		t.Fatalf("build user prompt: %v", err)
	}

	required := []string{
		"Canonical resource identifiers:",
		"Affected Pod:",
		"- namespace: prod",
		"- name: checkoutservice-abc123",
		"Owning Deployment:",
		"- name: checkoutservice",
		"If recommended_action is restart_pod, copy the exact affected Pod identifiers into target.",
		"If recommended_action is rollout_undo, copy the exact owning Deployment identifiers into target.",
		"Do not invent resource names.",
	}

	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf(
				"expected user prompt to contain %q",
				want,
			)
		}
	}
}

func TestClaudeProposalSchemaRequiresNonEmptyTargetStrings(t *testing.T) {
	schema := proposalJSONSchema()

	properties := schema["properties"].(map[string]any)
	target := properties["target"].(map[string]any)
	targetProperties := target["properties"].(map[string]any)

	for _, field := range []string{
		"kind",
		"namespace",
		"name",
	} {
		property := targetProperties[field].(map[string]any)

		if property["type"] != "string" {
			t.Fatalf(
				"expected target.%s to be a string",
				field,
			)
		}

		if property["minLength"] != 1 {
			t.Fatalf(
				"expected target.%s minLength 1, got %#v",
				field,
				property["minLength"],
			)
		}
	}
}

func promptTestIncident() IncidentInput {
	return IncidentInput{
		DetectionEvent: DetectionEvent{
			PodName:         "checkoutservice-abc123",
			Namespace:       "prod",
			ContainerName:   "checkoutservice",
			RestartCount:    5,
			OwnerDeployment: "checkoutservice",
			Timestamp:       time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		},
		Logs: "connection refused while contacting payment service",
		Events: []string{
			"Back-off restarting failed container",
		},
	}
}
