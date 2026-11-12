package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxLLMResponse = 4 << 20
	llmTimeout     = 180 * time.Second
)

// Message is one OpenAI-style chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolSpec describes one function the model may call.
type ToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// Completion is the model's reply and the tokens it cost.
type Completion struct {
	Message     Message
	TotalTokens int
}

// LLM is the chat capability the agent needs; OpenAIChat implements it.
type LLM interface {
	Chat(ctx context.Context, messages []Message, tools []ToolSpec, temperature float64) (Completion, error)
}

// OpenAIChat talks to any OpenAI-compatible /chat/completions endpoint (Ollama by default).
type OpenAIChat struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

// NewOpenAIChat validates the endpoint. Redirects are never followed, so the key cannot be
// forwarded to another host.
func NewOpenAIChat(baseURL, model, apiKey string) (*OpenAIChat, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("FIXAGENT_BASE_URL must be an http(s) URL")
	}
	if model == "" {
		return nil, errors.New("a model name is required (FIXAGENT_MODEL)")
	}
	client := &http.Client{Timeout: llmTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &OpenAIChat{BaseURL: strings.TrimRight(baseURL, "/"), Model: model, APIKey: apiKey, HTTP: client}, nil
}

// Chat sends one completion request.
func (c *OpenAIChat) Chat(ctx context.Context, messages []Message, tools []ToolSpec, temperature float64) (Completion, error) {
	body, err := json.Marshal(map[string]any{"model": c.Model, "messages": messages, "tools": tools, "temperature": temperature, "stream": false})
	if err != nil {
		return Completion{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Completion{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		// The URL error text can include the endpoint but never the key (it is only in a header).
		return Completion{}, fmt.Errorf("calling the model endpoint: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLLMResponse+1))
	if err != nil {
		return Completion{}, fmt.Errorf("reading model response: %w", err)
	}
	if len(data) > maxLLMResponse {
		return Completion{}, errors.New("model response exceeds the size limit")
	}
	if response.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("model endpoint returned HTTP %d: %s", response.StatusCode, snippet(data))
	}
	var parsed struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(data, &parsed); err != nil || len(parsed.Choices) == 0 {
		return Completion{}, errors.New("model response is not a chat completion")
	}
	tokens := parsed.Usage.TotalTokens
	if tokens <= 0 {
		// Some local servers omit usage; estimate (about four characters per token) so budgets still bind.
		tokens = (len(body) + len(data)) / 4
	}
	message := parsed.Choices[0].Message
	message.Role = "assistant"
	return Completion{Message: message, TotalTokens: tokens}, nil
}

func snippet(data []byte) string {
	text := strings.Join(strings.Fields(string(data)), " ")
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}
