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
	"testing"
	"time"
)

const (
	runOllamaMistralEvalEnv = "RUN_OLLAMA_MISTRAL_EVAL"

	ollamaChatURL   = "http://localhost:11434/api/chat"
	ollamaMistral7B = "mistral:7b"

	// A local 7B model can be slow on a laptop.
	// Each individual classification gets its own timeout.
	ollamaPerCaseTimeout = 2 * time.Minute
)

type ollamaRequest struct {
	Model    string             `json:"model"`
	Messages []ollamaMessage    `json:"messages"`
	Stream   bool               `json:"stream"`
	Format   string             `json:"format"`
	Options  ollamaModelOptions `json:"options"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaModelOptions struct {
	Temperature float64 `json:"temperature"`
}

type ollamaResponse struct {
	Model         string        `json:"model"`
	Message       ollamaMessage `json:"message"`
	Done          bool          `json:"done"`
	TotalDuration int64         `json:"total_duration"`
}

type ollamaClassificationResult struct {
	Proposal       Proposal
	GoLatency      time.Duration
	OllamaDuration time.Duration
	Error          error
	Success        bool
}

func TestOllamaMistral7BLabelledClassification(t *testing.T) {
	if os.Getenv(runOllamaMistralEvalEnv) != "1" {
		t.Skip(
			"set RUN_OLLAMA_MISTRAL_EVAL=1 to run local Ollama Mistral labelled evaluation",
		)
	}

	cases := labelledClassificationCases()

	if len(cases) != 20 {
		t.Fatalf(
			"expected exactly 20 labelled cases, got %d",
			len(cases),
		)
	}

	client := &http.Client{
		// The request itself is protected by the per-case context.
		// This client timeout is a second safety boundary.
		Timeout: ollamaPerCaseTimeout + 10*time.Second,

		// Do not reuse potentially stuck persistent connections
		// between separate local Ollama evaluation requests.
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	evaluationResults := make(
		[]ClassificationEvaluationResult,
		0,
		len(cases),
	)

	var totalOllamaDuration time.Duration
	var successfulOllamaDurations int

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

		ctx, cancel := context.WithTimeout(
			context.Background(),
			ollamaPerCaseTimeout,
		)

		start := time.Now()

		result := classifyWithOllama(
			ctx,
			client,
			tc.Input,
		)

		goLatency := time.Since(start)

		cancel()

		evaluationResults = append(
			evaluationResults,
			ClassificationEvaluationResult{
				Case:     tc,
				Proposal: result.Proposal,
				Error:    result.Error,
				Latency:  goLatency,
			},
		)

		if result.Error != nil {
			t.Logf(
				"MODEL CALL FAILED: %v",
				result.Error,
			)

			t.Logf(
				"Go latency before failure: %s",
				goLatency.Round(time.Millisecond),
			)

			continue
		}

		proposal := result.Proposal

		if result.OllamaDuration > 0 {
			totalOllamaDuration += result.OllamaDuration
			successfulOllamaDurations++
		}

		subCauseOK :=
			proposal.SubCause == tc.ExpectedSubCause

		actionOK :=
			proposal.RecommendedAction == tc.ExpectedAction

		safetyOK :=
			proposal.SafeForAutomation ==
				tc.ExpectedSafeForAutomation

		allOK :=
			subCauseOK &&
				actionOK &&
				safetyOK

		t.Logf(
			"Expected: sub_cause=%q action=%q safe=%v",
			tc.ExpectedSubCause,
			tc.ExpectedAction,
			tc.ExpectedSafeForAutomation,
		)

		t.Logf(
			"Model:    sub_cause=%q action=%q safe=%v",
			proposal.SubCause,
			proposal.RecommendedAction,
			proposal.SafeForAutomation,
		)

		t.Logf(
			"Reasoning: %s",
			proposal.Reasoning,
		)

		t.Logf(
			"Go measured latency: %s",
			goLatency.Round(time.Millisecond),
		)

		if result.OllamaDuration > 0 {
			t.Logf(
				"Ollama reported duration: %s",
				result.OllamaDuration.Round(time.Millisecond),
			)
		}

		if allOK {
			t.Log("RESULT: CORRECT")
		} else {
			t.Log("RESULT: INCORRECT")
		}
	}

	summary := CalculateClassificationEvaluation(
		cases,
		evaluationResults,
	)

	averageOllamaDuration := time.Duration(0)

	if successfulOllamaDurations > 0 {
		averageOllamaDuration =
			totalOllamaDuration /
				time.Duration(successfulOllamaDurations)
	}

	t.Log("")
	t.Log("============================================================")
	t.Log("MISTRAL-7B OFFLINE EVALUATION")
	t.Log("============================================================")
	t.Logf(
		"Model: %s",
		ollamaMistral7B,
	)
	t.Log(
		"Execution mode: local Ollama",
	)
	t.Logf(
		"Ollama endpoint: %s",
		ollamaChatURL,
	)
	t.Log(FormatClassificationEvaluation(ollamaMistral7B, summary))

	if successfulOllamaDurations > 0 {
		t.Logf(
			"Average Ollama reported generation duration: %s",
			averageOllamaDuration.Round(time.Millisecond),
		)
	}

	t.Log("============================================================")

	if summary.FailedCalls > 0 {
		t.Logf(
			"WARNING: %d model calls failed or timed out.",
			summary.FailedCalls,
		)
	}
}

func classifyWithOllama(
	ctx context.Context,
	client *http.Client,
	input IncidentInput,
) ollamaClassificationResult {

	result := ollamaClassificationResult{}

	userPrompt, err := BuildUserPrompt(input)
	if err != nil {
		result.Error = fmt.Errorf(
			"build user prompt: %w",
			err,
		)
		return result
	}

	requestData := ollamaRequest{
		Model: ollamaMistral7B,

		Messages: []ollamaMessage{
			{
				Role:    "system",
				Content: BuildSystemPrompt(),
			},
			{
				Role:    "user",
				Content: userPrompt,
			},
		},

		Stream: false,
		Format: "json",

		Options: ollamaModelOptions{
			Temperature: 0,
		},
	}

	requestBody, err := json.Marshal(requestData)
	if err != nil {
		result.Error = fmt.Errorf(
			"marshal Ollama request: %w",
			err,
		)
		return result
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		ollamaChatURL,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		result.Error = fmt.Errorf(
			"create Ollama request: %w",
			err,
		)
		return result
	}

	req.Header.Set(
		"Content-Type",
		"application/json",
	)

	response, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Errorf(
			"call Ollama: %w",
			err,
		)
		return result
	}

	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		result.Error = fmt.Errorf(
			"read Ollama response: %w",
			err,
		)
		return result
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		result.Error = fmt.Errorf(
			"Ollama returned HTTP %d: %s",
			response.StatusCode,
			strings.TrimSpace(string(responseBody)),
		)

		return result
	}

	var ollamaResp ollamaResponse

	if err := json.Unmarshal(
		responseBody,
		&ollamaResp,
	); err != nil {
		result.Error = fmt.Errorf(
			"decode Ollama response: %w",
			err,
		)
		return result
	}

	outputText := strings.TrimSpace(
		ollamaResp.Message.Content,
	)

	if outputText == "" {
		result.Error = fmt.Errorf(
			"Ollama returned empty model output",
		)
		return result
	}

	var proposal Proposal

	if err := json.Unmarshal(
		[]byte(outputText),
		&proposal,
	); err != nil {
		result.Error = fmt.Errorf(
			"decode Mistral proposal JSON: %w; response: %s",
			err,
			outputText,
		)
		return result
	}

	result.Proposal = proposal
	result.Success = true

	if ollamaResp.TotalDuration > 0 {
		result.OllamaDuration =
			time.Duration(ollamaResp.TotalDuration)
	}

	return result
}
