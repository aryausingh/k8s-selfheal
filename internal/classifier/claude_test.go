package classifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClaudeResponseUsageJSONParses(t *testing.T) {
	body := []byte(`{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-5",
		"content": [
			{
				"type": "text",
				"text": "{}"
			}
		],
		"usage": {
			"input_tokens": 1000,
			"output_tokens": 200
		}
	}`)

	var response claudeResponse

	if err := json.Unmarshal(
		body,
		&response,
	); err != nil {
		t.Fatalf("decode Claude response: %v", err)
	}

	metadata := claudeCallMetadata(response.Usage)

	if metadata.InputTokens != 1000 {
		t.Fatalf(
			"expected 1000 input tokens, got %d",
			metadata.InputTokens,
		)
	}

	if metadata.OutputTokens != 200 {
		t.Fatalf(
			"expected 200 output tokens, got %d",
			metadata.OutputTokens,
		)
	}

	if metadata.TotalTokens != 1200 {
		t.Fatalf(
			"expected 1200 total tokens, got %d",
			metadata.TotalTokens,
		)
	}
}

func TestClaudeClassifierClassifyWithMetadataReturnsUsage(t *testing.T) {
	proposalJSON, err := json.Marshal(
		claudeLiveTransientProposal(),
	)
	if err != nil {
		t.Fatalf("marshal proposal: %v", err)
	}

	responseBody, err := json.Marshal(
		map[string]any{
			"id":          "msg_test",
			"type":        "message",
			"role":        "assistant",
			"model":       claudeSonnet5Model,
			"stop_reason": "end_turn",
			"content": []map[string]string{
				{
					"type": "text",
					"text": string(proposalJSON),
				},
			},
			"usage": map[string]int{
				"input_tokens":  1000,
				"output_tokens": 200,
			},
		},
	)
	if err != nil {
		t.Fatalf("marshal Claude response: %v", err)
	}

	requests := 0
	client := &http.Client{
		Transport: roundTripFunc(
			func(req *http.Request) (*http.Response, error) {
				requests++

				if req.Method != http.MethodPost {
					t.Fatalf(
						"expected POST request, got %s",
						req.Method,
					)
				}

				if req.URL.String() != claudeMessagesURL {
					t.Fatalf(
						"expected Claude URL %s, got %s",
						claudeMessagesURL,
						req.URL.String(),
					)
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(
						strings.NewReader(
							string(responseBody),
						),
					),
					Header: make(http.Header),
				}, nil
			},
		),
	}

	classifier := &ClaudeClassifier{
		apiKey: "test-key",
		model:  claudeSonnet5Model,
		client: client,
	}

	proposal, metadata, err := classifier.ClassifyWithMetadata(
		context.Background(),
		claudeLiveTransientIncident(),
	)
	if err != nil {
		t.Fatalf("classify with metadata: %v", err)
	}

	if requests != 1 {
		t.Fatalf(
			"expected one Claude request, got %d",
			requests,
		)
	}

	if proposal.SubCause != "transient_failure" {
		t.Fatalf(
			"expected transient_failure, got %s",
			proposal.SubCause,
		)
	}

	if metadata.InputTokens != 1000 ||
		metadata.OutputTokens != 200 ||
		metadata.TotalTokens != 1200 {

		t.Fatalf(
			"unexpected metadata: %#v",
			metadata,
		)
	}
}

func TestClaudeParsePreservesTargetAndUsage(t *testing.T) {
	responseBody := `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-5",
		"stop_reason": "end_turn",
		"content": [
			{
				"type": "text",
				"text": "{\"sub_cause\":\"transient_failure\",\"recommended_action\":\"restart_pod\",\"target\":{\"kind\":\"Pod\",\"namespace\":\"default\",\"name\":\"checkoutservice-abc123\"},\"safe_for_automation\":true,\"reasoning\":\"temporary dependency failure\"}"
			}
		],
		"usage": {
			"input_tokens": 1771,
			"output_tokens": 252
		}
	}`

	requests := 0
	client := &http.Client{
		Transport: roundTripFunc(
			func(req *http.Request) (*http.Response, error) {
				requests++

				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(
						strings.NewReader(
							responseBody,
						),
					),
					Header: make(http.Header),
				}, nil
			},
		),
	}

	classifier := &ClaudeClassifier{
		apiKey: "test-key",
		model:  claudeSonnet5Model,
		client: client,
	}

	proposal, metadata, err := classifier.ClassifyWithMetadata(
		context.Background(),
		claudeLiveTransientIncident(),
	)
	if err != nil {
		t.Fatalf("classify with metadata: %v", err)
	}

	if requests != 1 {
		t.Fatalf(
			"expected one Claude request, got %d",
			requests,
		)
	}

	if proposal.SubCause != "transient_failure" {
		t.Fatalf(
			"expected transient_failure, got %s",
			proposal.SubCause,
		)
	}

	if proposal.RecommendedAction != ActionRestartPod {
		t.Fatalf(
			"expected restart_pod, got %s",
			proposal.RecommendedAction,
		)
	}

	if proposal.Target.Kind != "Pod" {
		t.Fatalf(
			"expected target kind Pod, got %s",
			proposal.Target.Kind,
		)
	}

	if proposal.Target.Namespace != "default" {
		t.Fatalf(
			"expected target namespace default, got %s",
			proposal.Target.Namespace,
		)
	}

	if proposal.Target.Name != "checkoutservice-abc123" {
		t.Fatalf(
			"expected target name checkoutservice-abc123, got %s",
			proposal.Target.Name,
		)
	}

	if metadata.InputTokens != 1771 {
		t.Fatalf(
			"expected 1771 input tokens, got %d",
			metadata.InputTokens,
		)
	}

	if metadata.OutputTokens != 252 {
		t.Fatalf(
			"expected 252 output tokens, got %d",
			metadata.OutputTokens,
		)
	}

	if metadata.TotalTokens != 2023 {
		t.Fatalf(
			"expected 2023 total tokens, got %d",
			metadata.TotalTokens,
		)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	return f(req)
}

func claudeLiveTransientIncident() IncidentInput {
	return IncidentInput{
		DetectionEvent: DetectionEvent{
			PodName:         "checkoutservice-abc123",
			Namespace:       "default",
			ContainerName:   "checkoutservice",
			RestartCount:    5,
			OwnerDeployment: "checkoutservice",
			Timestamp:       time.Now(),
		},
		Logs: `
connection refused while contacting payment service
temporary network failure
`,
		Events: []string{
			"Back-off restarting failed container",
			"Container terminated with exit code 1",
		},
	}
}

func claudeLiveTransientProposal() Proposal {
	return Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: "default",
			Name:      "checkoutservice-abc123",
		},
		SafeForAutomation: true,
		Reasoning:         "The evidence indicates a transient dependency or connectivity failure.",
	}
}
