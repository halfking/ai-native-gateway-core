package streaming

import (
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// TestEnrichObservabilityMetadata_Ingress tests observability context extraction at ingress
func TestEnrichObservabilityMetadata_Ingress(t *testing.T) {
	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.20.30.40:12345"
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	req.Header.Set("User-Agent", "claude-code/1.2.3")
	req.Header.Set("X-Gw-Task-Id", "TASK-456")

	// Extract at ingress (mimics handler.go line 639)
	obsCtx := telemetry.ExtractObservabilityContext(req)

	if obsCtx.ClientIP != "1.2.3.4" {
		t.Errorf("ClientIP = %q, want 1.2.3.4", obsCtx.ClientIP)
	}
	if obsCtx.ClientForwardedFor != "1.2.3.4, 5.6.7.8" {
		t.Errorf("ClientForwardedFor = %q, want full chain", obsCtx.ClientForwardedFor)
	}
	if obsCtx.AgentName != "claude-code" {
		t.Errorf("AgentName = %q, want claude-code", obsCtx.AgentName)
	}
	if obsCtx.AgentType != "cli" {
		t.Errorf("AgentType = %q, want cli", obsCtx.AgentType)
	}
}

// TestEnrichObservabilityMetadata_FailureEntry tests metadata propagation in failure path
func TestEnrichObservabilityMetadata_FailureEntry(t *testing.T) {
	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Real-IP", "192.168.1.100")
	req.Header.Set("User-Agent", "postman/10.0")
	req.Header.Set("X-Gw-Task-Id", "TASK-789")

	logCtx := &RequestLogContext{
		RequestID:        "req-fail-1",
		Request:          req,
		ObservabilityCtx: telemetry.ExtractObservabilityContext(req),
		KeyInfo: &authentication.KeyInfo{
			ID:        123,
			KeyPrefix: "sk-test-abc123",
			TenantID:  "test-tenant",
		},
		Session: &session.Session{
			Title:  "Test Failure Session",
			TaskID: "TASK-OVERRIDE",
		},
	}

	entry := logCtx.BuildFailureEntry("auth_failed", "invalid api key", nil, nil)

	// Verify ingress metadata propagated
	if entry.ClientIP == nil || *entry.ClientIP != "192.168.1.100" {
		t.Errorf("ClientIP not propagated: %v", entry.ClientIP)
	}
	if entry.AgentName == nil || *entry.AgentName != "postman" {
		t.Errorf("AgentName not propagated: %v", entry.AgentName)
	}
	if entry.AgentType == nil || *entry.AgentType != "api" {
		t.Errorf("AgentType = %v, want api", entry.AgentType)
	}

	// Verify API key fingerprint from KeyInfo
	if entry.APIKeyFingerprint == nil || *entry.APIKeyFingerprint != "sk-test-" {
		t.Errorf("APIKeyFingerprint = %v, want sk-test-", entry.APIKeyFingerprint)
	}

	// Verify session title
	if entry.SessionTitle == nil || *entry.SessionTitle != "Test Failure Session" {
		t.Errorf("SessionTitle = %v, want 'Test Failure Session'", entry.SessionTitle)
	}

	// Verify task_id (Session.TaskID takes precedence over header)
	if entry.TaskID == nil || *entry.TaskID != "TASK-OVERRIDE" {
		t.Errorf("TaskID = %v, want TASK-OVERRIDE", entry.TaskID)
	}
}

// TestEnrichObservabilityMetadata_APIKeyFingerprint tests safe API key masking
func TestEnrichObservabilityMetadata_APIKeyFingerprint(t *testing.T) {
	tests := []struct {
		name       string
		keyPrefix  string
		wantPrefix string
	}{
		{
			name:       "standard openai key",
			keyPrefix:  "sk-1234abcd5678efgh",
			wantPrefix: "sk-1234a",
		},
		{
			name:       "short key",
			keyPrefix:  "abc",
			wantPrefix: "***",
		},
		{
			name:       "empty key",
			keyPrefix:  "",
			wantPrefix: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
			logCtx := &RequestLogContext{
				RequestID:        "req-fp-test",
				Request:          req,
				ObservabilityCtx: telemetry.ExtractObservabilityContext(req),
			}

			if tt.keyPrefix != "" {
				logCtx.KeyInfo = &authentication.KeyInfo{
					ID:        1,
					KeyPrefix: tt.keyPrefix,
				}
			}

			entry := logCtx.BuildFailureEntry("test_error", "test", nil, nil)

			if tt.wantPrefix == "" {
				if entry.APIKeyFingerprint != nil {
					t.Errorf("Expected nil fingerprint for empty key, got %v", *entry.APIKeyFingerprint)
				}
			} else {
				if entry.APIKeyFingerprint == nil {
					t.Errorf("Expected fingerprint %q, got nil", tt.wantPrefix)
				} else if *entry.APIKeyFingerprint != tt.wantPrefix {
					t.Errorf("APIKeyFingerprint = %q, want %q", *entry.APIKeyFingerprint, tt.wantPrefix)
				}
			}
		})
	}
}

// TestEnrichObservabilityMetadata_SessionTaskPrecedence tests task_id precedence
func TestEnrichObservabilityMetadata_SessionTaskPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		sessionTaskID  string
		headerTaskID   string
		wantTaskID     string
		wantTaskIDNull bool
	}{
		{
			name:          "session task takes precedence",
			sessionTaskID: "SESSION-123",
			headerTaskID:  "HEADER-456",
			wantTaskID:    "SESSION-123",
		},
		{
			name:         "header fallback when no session",
			headerTaskID: "HEADER-789",
			wantTaskID:   "HEADER-789",
		},
		{
			name:           "null when neither present",
			wantTaskIDNull: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
			if tt.headerTaskID != "" {
				req.Header.Set("X-Gw-Task-Id", tt.headerTaskID)
			}

			logCtx := &RequestLogContext{
				RequestID:        "req-task-test",
				Request:          req,
				ObservabilityCtx: telemetry.ExtractObservabilityContext(req),
			}

			if tt.sessionTaskID != "" {
				logCtx.Session = &session.Session{
					TaskID: tt.sessionTaskID,
				}
			}

			entry := logCtx.BuildFailureEntry("test_error", "test", nil, nil)

			if tt.wantTaskIDNull {
				if entry.TaskID != nil {
					t.Errorf("Expected nil TaskID, got %v", *entry.TaskID)
				}
			} else {
				if entry.TaskID == nil {
					t.Errorf("Expected TaskID %q, got nil", tt.wantTaskID)
				} else if *entry.TaskID != tt.wantTaskID {
					t.Errorf("TaskID = %q, want %q", *entry.TaskID, tt.wantTaskID)
				}
			}
		})
	}
}
