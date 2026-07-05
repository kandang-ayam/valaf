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

// Anthropic talks to the Anthropic Messages API (or an Anthropic-compatible
// gateway). baseURL is the host root, e.g. https://api.anthropic.com.
type Anthropic struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func NewAnthropic(baseURL, apiKey, model string) *Anthropic {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &Anthropic{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *Anthropic) Name() string  { return "anthropic" }
func (p *Anthropic) Model() string { return p.model }

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *Anthropic) Analyze(ctx context.Context, req core.AnalysisRequest) (core.AnalysisResult, error) {
	payload := anthropicRequest{
		Model:     p.model,
		MaxTokens: 4096,
		System:    systemPrompt,
		Messages:  []anthropicMessage{{Role: "user", Content: buildUserPrompt(req)}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return core.AnalysisResult{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return core.AnalysisResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if p.apiKey != "" {
		httpReq.Header.Set("x-api-key", p.apiKey)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return core.AnalysisResult{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return core.AnalysisResult{}, fmt.Errorf("anthropic http %d: %s", resp.StatusCode, truncateBytes(raw, 300))
	}

	var out anthropicResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return core.AnalysisResult{}, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil {
		return core.AnalysisResult{}, fmt.Errorf("provider error: %s", out.Error.Message)
	}

	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return parseResult(text.String())
}
