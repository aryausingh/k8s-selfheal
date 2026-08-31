
package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const claudeMessagesURL = "https://api.anthropic.com/v1/messages"

const defaultClaudeModel = "claude-sonnet-4-6"

// ClaudeClassifier implements Classifier using the Claude API.
type ClaudeClassifier struct {
	apiKey string
	model  string
	client *http.Client
}

func (c *ClaudeClassifier) ProviderName() string {
	return ProviderClaude
}

func (c *ClaudeClassifier) ModelName() string {
	if c == nil ||
		strings.TrimSpace(c.model) == "" {

		return defaultClaudeModel
	}

	return c.model
}

// NewClaudeClassifier creates a Claude classifier using the
// ANTHROPIC_API_KEY environment variable.
func NewClaudeClassifier() (*ClaudeClassifier, error) {
	apiKey := strings.TrimSpace(
		os.Getenv("ANTHROPIC_API_KEY"),
	)

	if apiKey == "" {
		return nil, fmt.Errorf(
			"ANTHROPIC_API_KEY environment variable is not set",
		)
	}

	return &ClaudeClassifier{
		apiKey: apiKey,
		model: modelFromEnv(
			ClaudeModelEnv,
			defaultClaudeModel,
		),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// claudeRequest represents the request sent to the Claude Messages API.
type claudeRequest struct {
	Model        string             `json:"model"`
	MaxTokens    int                `json:"max_tokens"`
	System       string             `json:"system"`
	Messages     []claudeMessage    `json:"messages"`
	OutputConfig claudeOutputConfig `json:"output_config"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeOutputConfig struct {
	Format claudeJSONFormat `json:"format"`
}

type claudeJSONFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

// claudeResponse represents the relevant portion of the Claude API response.
type claudeResponse struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    []claudeContent `json:"content"`
	Usage      claudeUsage     `json:"usage"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// claudeContent represents a content block returned by Claude.
type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// claudeErrorResponse represents an error returned by Claude.
type claudeErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Classify sends the incident evidence to Claude and returns
// the structured Proposal.
func (c *ClaudeClassifier) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {
	proposal, _, err := c.ClassifyWithMetadata(
		ctx,
		input,
	)

	return proposal, err
}

// ClassifyWithMetadata sends the incident evidence to Claude and returns
// the structured Proposal plus token usage reported by Anthropic.
func (c *ClaudeClassifier) ClassifyWithMetadata(
	ctx context.Context,
	input IncidentInput,
) (
	Proposal,
	ClassifierCallMetadata,
	error,
) {
	if c == nil {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"Claude classifier is nil",
		)
	}

	if strings.TrimSpace(c.apiKey) == "" {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"Claude API key is empty",
		)
	}

	if err := validateIncidentInput(input); err != nil {
		return Proposal{}, ClassifierCallMetadata{}, err
	}

	userPrompt, err := BuildUserPrompt(input)
	if err != nil {
		return Proposal{}, ClassifierCallMetadata{}, err
	}

	model := c.ModelName()

	requestData := claudeRequest{
		Model:     model,
		MaxTokens: 1024,

		System: BuildSystemPrompt(),

		Messages: []claudeMessage{
			{
				Role:    "user",
				Content: userPrompt,
			},
		},

		OutputConfig: claudeOutputConfig{
			Format: claudeJSONFormat{
				Type:   "json_schema",
				Schema: proposalJSONSchema(),
			},
		},
	}

	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"marshal Claude request: %w",
			err,
		)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		claudeMessagesURL,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"create Claude request: %w",
			err,
		)
	}

	req.Header.Set(
		"Content-Type",
		"application/json",
	)

	req.Header.Set(
		"x-api-key",
		c.apiKey,
	)

	req.Header.Set(
		"anthropic-version",
		"2023-06-01",
	)

	client := c.client

	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	response, err := client.Do(req)
	if err != nil {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"call Claude API: %w",
			err,
		)
	}

	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"read Claude response: %w",
			err,
		)
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return Proposal{}, ClassifierCallMetadata{}, parseClaudeAPIError(
			response.StatusCode,
			responseBody,
		)
	}

	var claudeResp claudeResponse

	if err := json.Unmarshal(
		responseBody,
		&claudeResp,
	); err != nil {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"decode Claude response: %w",
			err,
		)
	}

	outputText, err := extractClaudeText(claudeResp)
	if err != nil {
		return Proposal{}, ClassifierCallMetadata{}, err
	}

	var proposal Proposal

	if err := json.Unmarshal(
		[]byte(outputText),
		&proposal,
	); err != nil {
		return Proposal{}, ClassifierCallMetadata{}, fmt.Errorf(
			"decode Claude proposal JSON: %w; response: %s",
			err,
			outputText,
		)
	}

	return proposal, claudeCallMetadata(claudeResp.Usage), nil
}

func claudeCallMetadata(
	usage claudeUsage,
) ClassifierCallMetadata {
	tokenUsage := TokenUsage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}

	return ClassifierCallMetadata{
		InputTokens:  nonNegativeInt(usage.InputTokens),
		OutputTokens: nonNegativeInt(usage.OutputTokens),
		TotalTokens:  tokenUsage.TotalTokens(),
	}
}

// validateIncidentInput prevents sending incomplete incidents.
func validateIncidentInput(input IncidentInput) error {

	if strings.TrimSpace(input.PodName) == "" {
		return fmt.Errorf("pod name is empty")
	}

	if strings.TrimSpace(input.Namespace) == "" {
		return fmt.Errorf("namespace is empty")
	}

	if strings.TrimSpace(input.ContainerName) == "" {
		return fmt.Errorf("container name is empty")
	}

	if strings.TrimSpace(input.Logs) == "" &&
		len(input.Events) == 0 {

		return fmt.Errorf(
			"both logs and events are empty",
		)
	}

	return nil
}

// extractClaudeText finds the JSON text inside Claude's response.
func extractClaudeText(
	response claudeResponse,
) (string, error) {

	for _, content := range response.Content {

		if content.Type == "text" &&
			strings.TrimSpace(content.Text) != "" {

			return strings.TrimSpace(content.Text), nil
		}
	}

	return "", fmt.Errorf(
		"Claude response did not contain text output",
	)
}

// parseClaudeAPIError converts Claude's HTTP error into
// a readable Go error.
func parseClaudeAPIError(
	statusCode int,
	body []byte,
) error {

	var apiError claudeErrorResponse

	if err := json.Unmarshal(
		body,
		&apiError,
	); err == nil &&
		apiError.Error.Message != "" {

		return fmt.Errorf(
			"Claude API returned HTTP %d: %s",
			statusCode,
			apiError.Error.Message,
		)
	}

	return fmt.Errorf(
		"Claude API returned HTTP %d: %s",
		statusCode,
		strings.TrimSpace(string(body)),
	)
}

// proposalJSONSchema defines the exact Proposal structure
// expected from Claude.
func proposalJSONSchema() map[string]any {

	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,

		"properties": map[string]any{

			"sub_cause": map[string]any{
				"type": "string",
				"enum": []string{
					"transient_failure",
					"bad_deploy",
					"bad_config",
					"application_panic",
					"oom_adjacent",
					"unknown",
				},
			},

			"recommended_action": map[string]any{
				"type": "string",
				"enum": []string{
					ActionRestartPod,
					ActionRolloutUndo,
					ActionEscalateToHuman,
				},
			},

			"target": map[string]any{
				"type":                 "object",
				"additionalProperties": false,

				"properties": map[string]any{

					"kind": map[string]any{
						"type":      "string",
						"minLength": 1,
						"enum": []string{
							"Pod",
							"Deployment",
						},
					},

					"namespace": map[string]any{
						"type":      "string",
						"minLength": 1,
					},

					"name": map[string]any{
						"type":      "string",
						"minLength": 1,
					},
				},

				"required": []string{
					"kind",
					"namespace",
					"name",
				},
			},

			"safe_for_automation": map[string]any{
				"type": "boolean",
			},

			"reasoning": map[string]any{
				"type": "string",
			},
		},

		"required": []string{
			"sub_cause",
			"recommended_action",
			"target",
			"safe_for_automation",
			"reasoning",
		},
	}
}
