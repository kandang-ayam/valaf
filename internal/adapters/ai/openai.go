package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/valaf/valaf/internal/core"
)

// OpenAICompat talks to any OpenAI-compatible /chat/completions endpoint
// (OpenAI, vLLM, Ollama, LiteLLM, internal gateways). baseURL includes the API
// prefix, e.g. https://api.openai.com/v1 or http://localhost:11434/v1.
type OpenAICompat struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func NewOpenAICompat(baseURL, apiKey, model string) *OpenAICompat {
	return &OpenAICompat{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *OpenAICompat) Name() string  { return "openai_compat" }
func (p *OpenAICompat) Model() string { return p.model }

type openAIRequest struct {
	Model          string          `json:"model"`
	Temperature    float64         `json:"temperature"`
	Messages       []openAIMessage `json:"messages"`
	ResponseFormat *openAIFormat   `json:"response_format,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIFormat struct {
	Type string `json:"type"`
}

type openAIResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *OpenAICompat) Analyze(ctx context.Context, req core.AnalysisRequest) (core.AnalysisResult, error) {
	payload := openAIRequest{
		Model:       p.model,
		Temperature: 0,
		Messages: []openAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildUserPrompt(req)},
		},
		ResponseFormat: &openAIFormat{Type: "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return core.AnalysisResult{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return core.AnalysisResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return core.AnalysisResult{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return core.AnalysisResult{}, fmt.Errorf("openai_compat http %d: %s", resp.StatusCode, truncateBytes(raw, 300))
	}

	var out openAIResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return core.AnalysisResult{}, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil {
		return core.AnalysisResult{}, fmt.Errorf("provider error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return core.AnalysisResult{}, fmt.Errorf("provider returned no choices")
	}
	return parseResult(out.Choices[0].Message.Content)
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
