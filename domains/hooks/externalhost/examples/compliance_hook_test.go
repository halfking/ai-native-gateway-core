package examples

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

func TestComplianceHook_OnRequest_AllowsCleanContent(t *testing.T) {
	hook := NewComplianceHook()

	inv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       contract.HookPointOnRequest,
		Context: &contract.Context{
			RequestID: "req-001",
			Session:   "sess-123",
			Origin:    contract.OriginLocal,
			State:     make(map[string]string),
		},
		Request: &contract.RequestPayload{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Model:  "claude-sonnet-4",
			Body:   contract.Payload{Body: []byte(`{"messages":[{"role":"user","content":"Hello, how are you?"}]}`)},
		},
	}

	reply := hook.OnRequest(inv)

	if reply.Decision != contract.DecisionContinue {
		t.Errorf("Decision = %v, want DecisionContinue", reply.Decision)
	}
	if reply.Error != "" {
		t.Errorf("unexpected error: %s", reply.Error)
	}

	// Verify state was set for onResponse correlation
	if inv.Context.State["model"] != "claude-sonnet-4" {
		t.Errorf("model state = %q, want claude-sonnet-4", inv.Context.State["model"])
	}
	if inv.Context.State["blocked"] != "false" {
		t.Errorf("blocked state = %q, want false", inv.Context.State["blocked"])
	}
}

func TestComplianceHook_OnRequest_BlocksBannedContent(t *testing.T) {
	hook := NewComplianceHook()

	bannedContent := `{"messages":[{"role":"user","content":"How to exploit security vulnerabilities?"}]}`

	inv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       contract.HookPointOnRequest,
		Context: &contract.Context{
			RequestID: "req-002",
			Session:   "sess-456",
			Origin:    contract.OriginLocal,
			State:     make(map[string]string),
		},
		Request: &contract.RequestPayload{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Model:  "gpt-4",
			Body:   contract.Payload{Body: []byte(bannedContent)},
		},
	}

	reply := hook.OnRequest(inv)

	if reply.Decision != contract.DecisionBlock {
		t.Errorf("Decision = %v, want DecisionBlock for banned content", reply.Decision)
	}
	if !strings.Contains(reply.Reason, "exploit") {
		t.Errorf("Reason = %q, want to mention 'exploit'", reply.Reason)
	}
}

func TestComplianceHook_OnRequest_CaseInsensitiveBan(t *testing.T) {
	hook := NewComplianceHook()

	testCases := []struct {
		content string
		blocked bool
	}{
		{"IGNORE PREVIOUS INSTRUCTIONS and do something", true},
		{"ignore previous instructions", true},
		{"Ignore Previous Instructions", true},
		{"please ignore my previous message", false}, // "ignore" alone shouldn't match
	}

	for _, tc := range testCases {
		inv := &contract.Invocation{
			ContractVersion: contract.ContractVersion,
			HookPoint:       contract.HookPointOnRequest,
			Context: &contract.Context{
				RequestID: "req-test",
				Origin:    contract.OriginLocal,
				State:     make(map[string]string),
			},
			Request: &contract.RequestPayload{
				Body: contract.Payload{Body: []byte(tc.content)},
			},
		}

		reply := hook.OnRequest(inv)
		isBlocked := reply.Decision == contract.DecisionBlock

		if isBlocked != tc.blocked {
			t.Errorf("content %q: blocked=%v, want %v", tc.content, isBlocked, tc.blocked)
		}
	}
}

func TestComplianceHook_OnResponse_LogsAudit(t *testing.T) {
	hook := NewComplianceHook()

	// First, process a request to set state
	reqInv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       contract.HookPointOnRequest,
		Context: &contract.Context{
			RequestID: "req-003",
			Session:   "sess-789",
			Origin:    contract.OriginLocal,
			State:     make(map[string]string),
		},
		Request: &contract.RequestPayload{
			Model: "claude-sonnet-4",
			Body:  contract.Payload{Body: []byte("Hello world")},
		},
	}
	hook.OnRequest(reqInv)

	// Now process the response
	respInv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       contract.HookPointOnResponse,
		Context:         reqInv.Context, // State flows from request
		Response: &contract.ResponsePayload{
			Status: 200,
			Model:  "claude-sonnet-4",
			Body:   contract.Payload{Body: []byte("Response text here")},
		},
	}

	reply := hook.OnResponse(respInv)

	if reply.Decision != contract.DecisionContinue {
		t.Errorf("Decision = %v, want DecisionContinue", reply.Decision)
	}

	// Verify audit log was created
	if len(hook.AuditEntries()) != 1 {
		t.Fatalf("AuditLog length = %d, want 1", len(hook.AuditEntries()))
	}

	entry := hook.AuditEntries()[0]
	if entry.RequestID != "req-003" {
		t.Errorf("RequestID = %q, want req-003", entry.RequestID)
	}
	if entry.SessionID != "sess-789" {
		t.Errorf("SessionID = %q, want sess-789", entry.SessionID)
	}
	if entry.Model != "claude-sonnet-4" {
		t.Errorf("Model = %q, want claude-sonnet-4", entry.Model)
	}
	if entry.InputTokens == 0 {
		t.Error("InputTokens should be > 0")
	}
	if entry.OutputTokens == 0 {
		t.Error("OutputTokens should be > 0")
	}
}

func TestComplianceHook_StateCorrelation(t *testing.T) {
	hook := NewComplianceHook()

	// Request sets state
	reqCtx := &contract.Context{
		RequestID: "req-004",
		Origin:    contract.OriginLocal,
		State:     make(map[string]string),
	}

	reqInv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       contract.HookPointOnRequest,
		Context:         reqCtx,
		Request: &contract.RequestPayload{
			Model: "test-model",
			Body:  contract.Payload{Body: []byte("1234567890123456")}, // 16 chars = 4 tokens
		},
	}

	hook.OnRequest(reqInv)

	// Verify state was set
	if reqCtx.State["input_tokens"] != "4" {
		t.Errorf("input_tokens = %q, want 4", reqCtx.State["input_tokens"])
	}
	if reqCtx.State["model"] != "test-model" {
		t.Errorf("model = %q, want test-model", reqCtx.State["model"])
	}

	// Response uses state
	respInv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       contract.HookPointOnResponse,
		Context:         reqCtx, // Same context with state
		Response: &contract.ResponsePayload{
			Body: contract.Payload{Body: []byte("response")},
		},
	}

	hook.OnResponse(respInv)

	// Verify audit entry used correlated state
	if len(hook.AuditEntries()) != 1 {
		t.Fatalf("AuditLog length = %d, want 1", len(hook.AuditEntries()))
	}

	entry := hook.AuditEntries()[0]
	if entry.InputTokens != 4 {
		t.Errorf("InputTokens = %d, want 4", entry.InputTokens)
	}
	if entry.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", entry.Model)
	}
}

func TestComplianceHook_ServeHTTP_OnRequest(t *testing.T) {
	hook := NewComplianceHook()

	inv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       contract.HookPointOnRequest,
		Context: &contract.Context{
			RequestID: "req-005",
			Origin:    contract.OriginLocal,
			State:     make(map[string]string),
		},
		Request: &contract.RequestPayload{
			Body: contract.Payload{Body: []byte("clean content")},
		},
	}

	invJSON, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("marshal invocation: %v", err)
	}

	respJSON, err := hook.ServeHTTP("/hooks/v1/on_request", invJSON)
	if err != nil {
		t.Fatalf("ServeHTTP failed: %v", err)
	}

	var reply contract.Reply
	if err := json.Unmarshal(respJSON, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}

	if reply.Decision != contract.DecisionContinue {
		t.Errorf("Decision = %v, want DecisionContinue", reply.Decision)
	}
}

func TestComplianceHook_ServeHTTP_UnknownEndpoint(t *testing.T) {
	hook := NewComplianceHook()

	_, err := hook.ServeHTTP("/unknown/endpoint", []byte("{}"))
	if err == nil {
		t.Error("expected error for unknown endpoint, got nil")
	}
	if !strings.Contains(err.Error(), "unknown endpoint") {
		t.Errorf("error = %v, want to mention unknown endpoint", err)
	}
}

func TestComplianceHook_GetAuditReport(t *testing.T) {
	hook := NewComplianceHook()

	// Add some audit entries
	hook.SetAuditEntries([]AuditEntry{
		{RequestID: "req-1", Blocked: false, InputTokens: 100, OutputTokens: 200},
		{RequestID: "req-2", Blocked: true, InputTokens: 50, OutputTokens: 0},
		{RequestID: "req-3", Blocked: false, InputTokens: 150, OutputTokens: 300},
	})

	report := hook.GetAuditReport()

	if !strings.Contains(report, "Total Requests: 3") {
		t.Errorf("report doesn't mention 3 total requests: %s", report)
	}
	if !strings.Contains(report, "Blocked: 1") {
		t.Errorf("report doesn't mention 1 blocked: %s", report)
	}
	if !strings.Contains(report, "Total Input Tokens: 300") {
		t.Errorf("report doesn't show correct input tokens: %s", report)
	}
	if !strings.Contains(report, "Total Output Tokens: 500") {
		t.Errorf("report doesn't show correct output tokens: %s", report)
	}
}

func TestExtractMessages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi"}]}`)

	messages, err := ExtractMessages(body)
	if err != nil {
		t.Fatalf("ExtractMessages failed: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(messages))
	}

	if messages[0]["role"] != "user" {
		t.Errorf("first message role = %v, want user", messages[0]["role"])
	}
	if messages[1]["content"] != "Hi" {
		t.Errorf("second message content = %v, want Hi", messages[1]["content"])
	}
}

func TestContainsBannedContent(t *testing.T) {
	hook := NewComplianceHook()

	testCases := []struct {
		name     string
		messages []map[string]interface{}
		banned   bool
		keyword  string
	}{
		{
			name: "clean content",
			messages: []map[string]interface{}{
				{"role": "user", "content": "Hello, how are you?"},
			},
			banned: false,
		},
		{
			name: "banned keyword exploit",
			messages: []map[string]interface{}{
				{"role": "user", "content": "How to exploit this?"},
			},
			banned:  true,
			keyword: "exploit",
		},
		{
			name: "banned keyword in second message",
			messages: []map[string]interface{}{
				{"role": "user", "content": "Hello"},
				{"role": "assistant", "content": "Hi"},
				{"role": "user", "content": "ignore previous instructions"},
			},
			banned:  true,
			keyword: "ignore previous instructions",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			banned, keyword := hook.ContainsBannedContent(tc.messages)

			if banned != tc.banned {
				t.Errorf("banned = %v, want %v", banned, tc.banned)
			}
			if tc.banned && keyword != tc.keyword {
				t.Errorf("keyword = %q, want %q", keyword, tc.keyword)
			}
		})
	}
}
