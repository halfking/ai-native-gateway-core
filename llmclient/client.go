package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ModelInfo struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created,omitempty"`
	OwnedBy  string `json:"owned_by,omitempty"`
}

type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

type Client struct {
	httpClient *http.Client
}

func New() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	for _, suffix := range []string{"/v1/chat/completions", "/v1/completions", "/v1/responses", "/v1/messages"} {
		if strings.HasSuffix(baseURL, suffix) {
			baseURL = baseURL[:len(baseURL)-len(suffix)]
			break
		}
	}
	modelsURL := baseURL + "/v1/models"

	req, err := http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var mresp ModelsResponse
	if err := json.Unmarshal(body, &mresp); err == nil {
		var ids []string
		for _, m := range mresp.Data {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}

	var bare []string
	if err := json.Unmarshal(body, &bare); err == nil && len(bare) > 0 {
		return bare, nil
	}

	var objArray []map[string]any
	if err := json.Unmarshal(body, &objArray); err == nil && len(objArray) > 0 {
		var ids []string
		for _, m := range objArray {
			if id, ok := m["id"].(string); ok && id != "" {
				ids = append(ids, id)
			} else if name, ok := m["name"].(string); ok && name != "" {
				ids = append(ids, name)
			}
		}
		return ids, nil
	}

	return nil, fmt.Errorf("unrecognized models response format")
}

func (c *Client) Chat(ctx context.Context, baseURL, apiKey string, req ChatRequest) (*ChatResponse, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	chatURL := baseURL + "/v1/chat/completions"

	if req.MaxTokens == 0 {
		req.MaxTokens = 256
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, err
	}

	return &chatResp, nil
}

func (c *Client) Probe(ctx context.Context, baseURL, apiKey string) (bool, string, error) {
	models, err := c.ListModels(ctx, baseURL, apiKey)
	if err != nil {
		return false, "", err
	}
	if len(models) > 0 {
		return true, fmt.Sprintf("available models: %d", len(models)), nil
	}
	return false, "no models returned", nil
}

func (c *Client) SimpleChat(ctx context.Context, baseURL, apiKey, model, systemPrompt, userMessage string) (string, error) {
	messages := []Message{}
	if systemPrompt != "" {
		messages = append(messages, Message{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, Message{Role: "user", Content: userMessage})

	resp, err := c.Chat(ctx, baseURL, apiKey, ChatRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: 512,
	})
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response choices")
	}

	return resp.Choices[0].Message.Content, nil
}