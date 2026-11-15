// Package casdoor provides Casdoor approval platform integration.
//
// This client implements the Application API surface of Casdoor's approval
// base capability: create tasks, query status, cancel tasks. Human approval
// decisions are made in the Casdoor console; results land via webhook callbacks.
//
// Reference: Marketing's lib/casdoor-approval.js (248 lines, production-ready).
package casdoor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Config holds Casdoor approval client configuration.
type Config struct {
	Enabled        bool
	Endpoint       string // e.g. https://casdoor.kx.com
	ClientID       string
	ClientSecret   string
	CallbackSecret string // independent webhook signing key
	CallbackKid    string // key identifier
	CallbackURL    string // https://llm-gateway.kx.com/api/webhooks/casdoor/approval
	TaskOwner      string // casdoor organization, defaults to "admin"
	DefaultFlow    string // flow reference, e.g. "@admin/sensitive-content-approval"
	TTLMinutes     int    // task expiration, defaults to 30
}

// Client is a Casdoor approval platform client.
type Client struct {
	config     Config
	httpClient *http.Client
}

// NewClient creates a new Casdoor approval client.
func NewClient(cfg Config) *Client {
	if cfg.TaskOwner == "" {
		cfg.TaskOwner = "admin"
	}
	if cfg.TTLMinutes == 0 {
		cfg.TTLMinutes = 30
	}
	return &Client{
		config: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// IsEnabled returns whether the client is enabled.
func (c *Client) IsEnabled() bool {
	return c.config.Enabled && c.config.Endpoint != "" &&
		c.config.ClientID != "" && c.config.ClientSecret != ""
}

// CreateTaskRequest is the request to create an approval task.
type CreateTaskRequest struct {
	Owner        string `json:"owner"`        // casdoor organization
	Application  string `json:"application"`  // auto-filled by client
	ExternalID   string `json:"externalId"`   // caller's correlation id
	Applicant    string `json:"applicant"`    // casdoor user id "org/username"
	Type         string `json:"type"`         // e.g. "sensitive_content"
	ResourceType string `json:"resourceType"` // e.g. "llm_session"
	ResourceID   string `json:"resourceId"`   // e.g. session id
	Title        string `json:"title"`
	Payload      string `json:"payload"`     // caller context JSON
	Steps        string `json:"steps"`       // JSON or flow reference "@owner/name"
	CallbackURL  string `json:"callbackUrl"` // auto-filled by client
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

// Task represents a Casdoor approval task.
type Task struct {
	ID          int    `json:"id"`
	Status      string `json:"status"` // pending/approved/rejected/cancelled/expired
	ExternalID  string `json:"externalId"`
	Applicant   string `json:"applicant"`
	DecidedBy   string `json:"decidedBy,omitempty"`
	DecidedTime string `json:"decidedTime,omitempty"`
	Comment     string `json:"comment,omitempty"`
	CreatedTime string `json:"createdTime"`
}

// CreateTask creates an approval task via Casdoor Application API.
func (c *Client) CreateTask(ctx context.Context, req *CreateTaskRequest) (*Task, error) {
	if !c.IsEnabled() {
		return nil, fmt.Errorf("casdoor approval client is not enabled")
	}

	// Auto-fill fields
	req.Application = c.config.ClientID
	if req.Owner == "" {
		req.Owner = c.config.TaskOwner
	}
	if req.CallbackURL == "" {
		req.CallbackURL = c.config.CallbackURL
	}
	if req.ExpiresAt == "" && c.config.TTLMinutes > 0 {
		req.ExpiresAt = time.Now().Add(time.Duration(c.config.TTLMinutes) * time.Minute).Format(time.RFC3339)
	}
	if req.Steps == "" && c.config.DefaultFlow != "" {
		req.Steps = c.config.DefaultFlow
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.config.Endpoint+"/api/add-approval-task-from-app",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	// HTTP Basic Auth (Casdoor spec: query-string credentials are rejected)
	httpReq.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("casdoor returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Status  string `json:"status"`
		Data    *Task  `json:"data"`
		Message string `json:"msg,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Status != "ok" || result.Data == nil {
		return nil, fmt.Errorf("casdoor error: %s", result.Message)
	}

	return result.Data, nil
}

// GetTaskStatus queries task status by externalId.
func (c *Client) GetTaskStatus(ctx context.Context, externalID string) (*Task, error) {
	if !c.IsEnabled() {
		return nil, fmt.Errorf("casdoor approval client is not enabled")
	}

	url := fmt.Sprintf("%s/api/get-approval-task-status?externalId=%s",
		c.config.Endpoint, externalID)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil // not found
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("casdoor returned %d", resp.StatusCode)
	}

	var result struct {
		Status string `json:"status"`
		Data   *Task  `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Data, nil
}

// CancelTask cancels a pending task by externalId.
func (c *Client) CancelTask(ctx context.Context, externalID string) error {
	if !c.IsEnabled() {
		return fmt.Errorf("casdoor approval client is not enabled")
	}

	body := map[string]string{"externalId": externalID}
	bodyBytes, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.config.Endpoint+"/api/cancel-approval-task-from-app",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	httpReq.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("casdoor returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// BuildConsoleURL returns the Casdoor console deep link for a task.
func (c *Client) BuildConsoleURL(taskID int) string {
	return fmt.Sprintf("%s/management/approval-tasks?taskId=%d",
		c.config.Endpoint, taskID)
}
