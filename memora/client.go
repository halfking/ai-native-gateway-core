// Package memora integrates the Memora / MemOS memory service as a
// context-compression oracle for llm-gateway-go.
//
// All operations are best-effort. A Memora outage MUST NOT fail the
// main request path. Errors are logged and counted, never propagated.
package memora

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a thin HTTP client for the Memora / MemOS product API.
type Client struct {
	baseURL       string
	apiKey        string
	http          *http.Client
	addTimeout    time.Duration
	searchTimeout time.Duration
}

// ClientConfig configures a new Client. All fields are optional.
type ClientConfig struct {
	BaseURL       string
	APIKey        string
	AddTimeout    time.Duration
	SearchTimeout time.Duration
}

// NewClient builds a Client from the given config.
func NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:8000"
	}
	if cfg.AddTimeout == 0 {
		cfg.AddTimeout = 5 * time.Second
	}
	if cfg.SearchTimeout == 0 {
		cfg.SearchTimeout = 200 * time.Millisecond
	}
	return &Client{
		baseURL:       cfg.BaseURL,
		apiKey:        cfg.APIKey,
		addTimeout:    cfg.AddTimeout,
		searchTimeout: cfg.SearchTimeout,
		http:          &http.Client{Timeout: cfg.AddTimeout},
	}
}

// Disabled reports whether the client is configured to no-op.
func (c *Client) Disabled() bool {
	return c == nil || c.baseURL == ""
}

// Message is the unit of conversation passed to MemOS /product/add.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Memory is a single fact returned by MemOS /product/search.
type Memory struct {
	ID     string
	Text   string
	Tags   []string
	Score  float64
	CubeID string
}

// AddMessage persists a message pair to Memora. Best-effort.
func (c *Client) AddMessage(ctx context.Context, userID string, messages []Message, info map[string]any) error {
	if c.Disabled() || len(messages) == 0 || userID == "" {
		return nil
	}
	if info == nil {
		info = map[string]any{}
	}
	payload := map[string]any{
		"user_id":  userID,
		"messages": messages,
		"info":     info,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/product/add", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	cctx, cancel := context.WithTimeout(ctx, c.addTimeout)
	defer cancel()
	req = req.WithContext(cctx)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("memos add status=%d body=%s", resp.StatusCode, string(body))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Search queries Memora for the top-K facts most relevant to query.
// Returns an empty slice (not an error) on 0 results or non-2xx.
func (c *Client) Search(ctx context.Context, userID, query string, topK int) ([]Memory, error) {
	if c.Disabled() || userID == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 8
	}
	payload := map[string]any{
		"user_id":             userID,
		"query":               query,
		"top_k":               topK,
		"memory_limit_number": topK,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		c.baseURL+"/product/search", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("memos search status=%d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TextMem []struct {
				CubeID   string `json:"cube_id"`
				Memories []struct {
					ID         string   `json:"id"`
					Memory     string   `json:"memory"`
					Background string   `json:"background,omitempty"`
					Key        string   `json:"key,omitempty"`
					Tags       []string `json:"tags,omitempty"`
					Score      float64  `json:"score,omitempty"`
					Relativity float64  `json:"relativity,omitempty"`
				} `json:"memories"`
			} `json:"text_mem"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Memory, 0, topK)
	for _, cube := range raw.Data.TextMem {
		for _, m := range cube.Memories {
			txt := m.Memory
			if txt == "" {
				txt = m.Key
			}
			if txt == "" {
				continue
			}
			if m.Background != "" && m.Background != txt {
				txt = m.Background + "\n→ " + txt
			}
			score := m.Relativity
			if score == 0 {
				score = m.Score
			}
			out = append(out, Memory{
				ID:     m.ID,
				Text:   txt,
				Tags:   m.Tags,
				Score:  score,
				CubeID: cube.CubeID,
			})
		}
	}
	return out, nil
}
