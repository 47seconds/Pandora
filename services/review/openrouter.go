package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenRouter reviews through OpenRouter's OpenAI-compatible chat API, which lets one
// key reach Anthropic (and other) models. Model ids look like "anthropic/claude-sonnet-5.5".
type OpenRouter struct {
	key   string
	model string
	base  string
	http  *http.Client
}

func NewOpenRouter(key, model string) *OpenRouter {
	return &OpenRouter{key: key, model: model, base: "https://openrouter.ai/api/v1", http: &http.Client{Timeout: 4 * time.Minute}}
}

func (o *OpenRouter) Review(ctx context.Context, in Input) (*Result, error) {
	payload, _ := json.Marshal(map[string]any{
		"model":      o.model,
		"max_tokens": 2048,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage(in)},
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", o.base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "Pandora PR Review")
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openrouter: %s: %.500s", resp.Status, body)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("openrouter: %s", out.Error.Message) // errors can arrive with HTTP 200
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: empty response")
	}
	return parseResult(out.Choices[0].Message.Content)
}
