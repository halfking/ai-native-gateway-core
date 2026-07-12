package activation

import (
	"encoding/json"
	"fmt"
)

// OnlineActivationRequest 在线激活请求
type OnlineActivationRequest struct {
	LicenseKey   string `json:"license_key"`
	HardwareHash string `json:"hardware_hash"`
	InstanceID   string `json:"instance_id"`
	DeviceName   string `json:"device_name"`
	Version      string `json:"version"`
}

// OnlineActivationResponse 在线激活响应
type OnlineActivationResponse struct {
	Success       bool   `json:"success"`
	SignedLicense string `json:"signed_license,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Message       string `json:"message,omitempty"`
}

// ActivateOnline 在线激活
func (c *Client) ActivateOnline(licenseKey, hardwareHash, instanceID, deviceName, version string) (*OnlineActivationResponse, error) {
	req := OnlineActivationRequest{
		LicenseKey:   licenseKey,
		HardwareHash: hardwareHash,
		InstanceID:   instanceID,
		DeviceName:   deviceName,
		Version:      version,
	}

	respBody, err := c.doRequest("POST", "/api/v1/license/activate", req)
	if err != nil {
		return nil, fmt.Errorf("activation failed: %w", err)
	}

	var resp OnlineActivationResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("activation failed: %s", resp.Message)
	}

	return &resp, nil
}
