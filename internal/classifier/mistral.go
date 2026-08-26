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

const mistralChatURL = "https://api.mistral.ai/v1/chat/completions"

// mistral-large-latest is a current Mistral model suitable for
// structured classification tasks.
const defaultMistralModel = "mistral-large-latest"

// MistralClassifier implements Classifier using the Mistral API.
type MistralClassifier struct {
	apiKey string
	model  string
	client *http.Client
}

func (m *MistralClassifier) ProviderName() string {
	return ProviderMistral
}

func (m *MistralClassifier) ModelName() string {
	if m == nil ||
		strings.TrimSpace(m.model) == "" {

		return defaultMistralModel
	}

	return m.model
}

// NewMistralClassifier creates a Mistral classifier using the
// MISTRAL_API_KEY environment variable.
func NewMistralClassifier() (*MistralClassifier, error) {
	apiKey := strings.TrimSpace(
		os.Getenv("MISTRAL_API_KEY"),
	)

	if apiKey == "" {
		return nil, fmt.Errorf(
			"MISTRAL_API_KEY environment variable is not set",
		)
	}

	return &MistralClassifier{
		apiKey: apiKey,
		model: modelFromEnv(
			MistralModelEnv,
			defaultMistralModel,
		),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// mistralRequest represents the request sent to Mistral.
type mistralRequest struct {
	Model          string             `json:"model"`
	Messages       []mistralMessage   `json:"messages"`
	ResponseFormat mistralResponseFmt `json:"response_format"`
	Temperature    float64            `json:"temperature"`
}

// mistralMessage represents one message in the Mistral chat request.
type mistralMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// mistralResponseFmt enables Mistral JSON mode.
type mistralResponseFmt struct {
	Type string `json:"type"`
}

// mistralResponse represents the relevant part of the
// Mistral chat completion response.
type mistralResponse struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []mistralChoice `json:"choices"`
}

// mistralChoice represents one generated completion.
type mistralChoice struct {
	Index        int                    `json:"index"`
	FinishReason string                 `json:"finish_reason"`
	Message      mistralResponseMessage `json:"message"`
}

// mistralResponseMessage represents the assistant's response.
type mistralResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// mistralErrorResponse represents an error returned by Mistral.
type mistralErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Classify sends the incident evidence to Mistral and returns
// the structured Proposal.
func (m *MistralClassifier) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {

	if m == nil {
		return Proposal{}, fmt.Errorf(
			"Mistral classifier is nil",
		)
	}

	if strings.TrimSpace(m.apiKey) == "" {
		return Proposal{}, fmt.Errorf(
			"Mistral API key is empty",
		)
	}

	if err := validateIncidentInput(input); err != nil {
		return Proposal{}, err
	}

	userPrompt, err := BuildUserPrompt(input)
	if err != nil {
		return Proposal{}, err
	}

	systemPrompt := BuildSystemPrompt()

	requestData := mistralRequest{
		Model: m.model,

		Messages: []mistralMessage{
			{
				Role:    "system",
				Content: systemPrompt,
			},
			{
				Role:    "user",
				Content: userPrompt,
			},
		},

		ResponseFormat: mistralResponseFmt{
			Type: "json_object",
		},

		Temperature: 0,
	}

	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return Proposal{}, fmt.Errorf(
			"marshal Mistral request: %w",
			err,
		)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		mistralChatURL,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return Proposal{}, fmt.Errorf(
			"create Mistral request: %w",
			err,
		)
	}

	req.Header.Set(
		"Content-Type",
		"application/json",
	)

	req.Header.Set(
		"Authorization",
		"Bearer "+m.apiKey,
	)

	client := m.client

	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	response, err := client.Do(req)
	if err != nil {
		return Proposal{}, fmt.Errorf(
			"call Mistral API: %w",
			err,
		)
	}

	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return Proposal{}, fmt.Errorf(
			"read Mistral response: %w",
			err,
		)
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return Proposal{}, parseMistralAPIError(
			response.StatusCode,
			responseBody,
		)
	}

	var mistralResponseData mistralResponse

	if err := json.Unmarshal(
		responseBody,
		&mistralResponseData,
	); err != nil {
		return Proposal{}, fmt.Errorf(
			"decode Mistral response: %w",
			err,
		)
	}

	if len(mistralResponseData.Choices) == 0 {
		return Proposal{}, fmt.Errorf(
			"Mistral response contained no choices",
		)
	}

	outputText := strings.TrimSpace(
		mistralResponseData.Choices[0].Message.Content,
	)

	if outputText == "" {
		return Proposal{}, fmt.Errorf(
			"Mistral response contained empty model output",
		)
	}

	var proposal Proposal

	if err := json.Unmarshal(
		[]byte(outputText),
		&proposal,
	); err != nil {
		return Proposal{}, fmt.Errorf(
			"decode Mistral proposal JSON: %w; response: %s",
			err,
			outputText,
		)
	}

	return proposal, nil
}

// parseMistralAPIError converts a Mistral HTTP error
// into a readable Go error.
func parseMistralAPIError(
	statusCode int,
	body []byte,
) error {

	var apiError mistralErrorResponse

	if err := json.Unmarshal(
		body,
		&apiError,
	); err == nil &&
		apiError.Error.Message != "" {

		return fmt.Errorf(
			"Mistral API returned HTTP %d: %s",
			statusCode,
			apiError.Error.Message,
		)
	}

	return fmt.Errorf(
		"Mistral API returned HTTP %d: %s",
		statusCode,
		strings.TrimSpace(string(body)),
	)
}
