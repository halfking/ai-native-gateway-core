package activation

import (
	"encoding/json"
	"fmt"
)

// TrialRequest 试用申请请求
type TrialRequest struct {
	Email string `json:"email"`
}

// TrialResponse 试用申请响应
type TrialResponse struct {
	Success    bool   `json:"success"`
	LicenseKey string `json:"license_key,omitempty"`
	Message    string `json:"message,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

// RequestTrial 申请试用 license
func (c *Client) RequestTrial(email string) (*TrialResponse, error) {
	req := TrialRequest{Email: email}

	respBody, err := c.doRequest("POST", "/api/v1/license/trial", req)
	if err != nil {
		return nil, fmt.Errorf("trial request failed: %w", err)
	}

	var resp TrialResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("trial failed: %s", resp.Message)
	}

	return &resp, nil
}
