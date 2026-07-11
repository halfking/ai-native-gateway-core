package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

const (
	defaultEmbeddingDimensions   = 1024
	maxAnalysisResponseBodyBytes = 64 << 20
)

// OpenAIClient implements the analysis chat and embedding interfaces.
type OpenAIClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	dimensions int
}

// CompletionRequest configures one non-streaming analysis completion.
type CompletionRequest struct {
	Model        string
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
}

// NewOpenAIClient creates an OpenAI-compatible analysis client.
func NewOpenAIClient(baseURL, apiKey string, timeout time.Duration) *OpenAIClient {
	return NewOpenAIClientWithNetworkPolicy(baseURL, apiKey, timeout, false)
}

// NewOpenAIClientWithNetworkPolicy creates a client with optional loopback access for tests.
func NewOpenAIClientWithNetworkPolicy(baseURL, apiKey string, timeout time.Duration, allowInsecureLocal bool) *OpenAIClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("analysis endpoint hostname cannot be resolved")
		}
		for _, resolved := range addresses {
			ip := resolved.IP
			if ip.IsLoopback() && allowInsecureLocal {
				continue
			}
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				return nil, fmt.Errorf("analysis endpoint resolves to a non-public address")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
	return &OpenAIClient{
		baseURL: strings.TrimSpace(baseURL),
		apiKey:  strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		dimensions: defaultEmbeddingDimensions,
	}
}

// Enabled reports whether the client has an endpoint and API key.
func (c *OpenAIClient) Enabled() bool {
	return c != nil && c.baseURL != "" && c.apiKey != ""
}

// Complete sends a short non-streaming completion request.
func (c *OpenAIClient) Complete(ctx context.Context, model, prompt string) (string, error) {
	return c.CompleteWithConfig(ctx, prompt, CompletionRequest{Model: model, MaxTokens: 256, Temperature: 0.1})
}

// CompleteWithConfig sends a configured non-streaming completion request.
func (c *OpenAIClient) CompleteWithConfig(ctx context.Context, prompt string, config CompletionRequest) (string, error) {
	if config.MaxTokens <= 0 {
		config.MaxTokens = 256
	}
	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(config.SystemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": config.SystemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": prompt})
	payload := map[string]any{
		"model":       config.Model,
		"messages":    messages,
		"max_tokens":  config.MaxTokens,
		"temperature": config.Temperature,
		"stream":      false,
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.postJSON(ctx, upstreamurl.ChatCompletionsURL(c.baseURL), payload, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("analysis completion returned no content")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}

// Embed generates one 1024-dimensional embedding.
func (c *OpenAIClient) Embed(ctx context.Context, model, text string) ([]float32, error) {
	vectors, err := c.EmbedBatch(ctx, model, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// EmbedBatch generates embeddings and restores provider results by index.
func (c *OpenAIClient) EmbedBatch(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	payload := map[string]any{
		"model":      model,
		"input":      texts,
		"dimensions": c.dimensions,
	}
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, upstreamurl.EmbeddingsURL(c.baseURL), payload, &response); err != nil {
		return nil, err
	}
	if len(response.Data) != len(texts) {
		return nil, fmt.Errorf("embedding count %d does not match input count %d", len(response.Data), len(texts))
	}
	vectors := make([][]float32, len(texts))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, fmt.Errorf("embedding index %d out of range", item.Index)
		}
		if len(item.Embedding) != c.dimensions {
			return nil, fmt.Errorf("embedding dimensions %d, want %d", len(item.Embedding), c.dimensions)
		}
		vectors[item.Index] = item.Embedding
	}
	for index, vector := range vectors {
		if len(vector) == 0 {
			return nil, fmt.Errorf("embedding index %d missing", index)
		}
	}
	return vectors, nil
}

func (c *OpenAIClient) postJSON(ctx context.Context, url string, payload, result any) error {
	if !c.Enabled() {
		return fmt.Errorf("analysis client not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Internal-Purpose", "session-analysis")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("analysis upstream HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAnalysisResponseBodyBytes))
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("decode analysis response: %w", err)
	}
	return nil
}
