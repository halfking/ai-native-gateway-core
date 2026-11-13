// Package ipc implements inter-process communication between plugins and core.
//
// It provides a Unix Socket based IPC layer with JSON message protocol,
// connection pooling, and timeout handling.
package ipc

import (
	"encoding/json"
)

// Message types
const (
	MessageTypeExtensionPoint = "extension_point"
	MessageTypeHealthCheck    = "health_check"
)

// ExtensionPointRequest represents a request to invoke an extension point.
type ExtensionPointRequest struct {
	Type    string          `json:"type"`    // Message type
	ID      string          `json:"id"`      // Request ID
	Phase   string          `json:"phase"`   // Extension point phase (e.g., "governance")
	Plugin  string          `json:"plugin"`  // Plugin name
	Context json.RawMessage `json:"context"` // Context data (JSON)
}

// ExtensionPointResponse represents the response from an extension point invocation.
type ExtensionPointResponse struct {
	Type    string          `json:"type"`    // Message type
	ID      string          `json:"id"`      // Request ID (matches request)
	Success bool            `json:"success"` // Whether the invocation succeeded
	Result  json.RawMessage `json:"result"`  // Result data (JSON)
	Error   string          `json:"error"`   // Error message if failed
}

// HealthCheckRequest represents a health check request.
type HealthCheckRequest struct {
	Type string `json:"type"` // Message type
	ID   string `json:"id"`   // Request ID
}

// HealthCheckResponse represents a health check response.
type HealthCheckResponse struct {
	Type    string `json:"type"`    // Message type
	ID      string `json:"id"`      // Request ID (matches request)
	Healthy bool   `json:"healthy"` // Whether the service is healthy
	Message string `json:"message"` // Health status message
}
