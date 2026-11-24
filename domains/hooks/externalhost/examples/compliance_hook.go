// Package examples provides reference implementations of external hooks.
//
// compliance_hook demonstrates a simple content compliance checker that:
//   - Blocks requests containing banned keywords
//   - Logs response metadata for audit trails
//   - Shows W3.2/W3.3 semantics in practice
package examples

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// ComplianceHook is a simple external hook that enforces content policies.
//
// This demonstrates the minimal viable external hook:
//   - onRequest: check for banned content, block if found
//   - onResponse: log metadata for audit (no content modification)
//   - State flow: request metadata → response correlation
//
// ★★ 并发安全（2026-11-25 12h 修订审计轮 P1 修正）：★★
// ★★ onResponse 由 CaptureResponse 放在**每个响应一个 goroutine** 里调用
// ★★ （domains/hooks/externalhost/pipeline_integration.go）⇒ ★★ 同一个 hook 实例
// ★★ 会被**多个 goroutine 并发**调用 OnResponse ⇒ ★★ AuditLog 的 append 与
// ★★ GetAuditReport 的读取**必须**加锁。
// ★★ 旧实现无锁 ⇒ `go test -race` 实测报 2 处 DATA RACE
// ★★ （Read/Previous write 同指 AuditLog 的 append）⇒ ★★ 后果不止「丢审计条目」：
// ★★ slice 头（len/cap/ptr）并发改写会损坏 ⇒ ★★ 可能 panic。
// ★★ 本参考实现是 INTEGRATION_GUIDE 推荐的首个可部署钩子 ⇒ ★★ 竞争必须在这里消掉。
type ComplianceHook struct {
	// BannedKeywords are case-insensitive patterns that trigger blocks
	BannedKeywords []string

	// mu guards auditLog (append from concurrent OnResponse goroutines vs
	// read from AuditEntries/GetAuditReport).
	mu sync.RWMutex

	// auditLog collects request/response pairs for compliance reporting.
	//
	// ★★ **刻意不导出**：★★ 一个**导出的可变切片**无法在并发下自证安全
	// ★★ （外部代码可以无锁读写它 ⇒ race detector 仍会报竞争、且无法由本包修复）。
	// ★★ 外部一律通过 AuditEntries() 取**快照**。
	auditLog []AuditEntry
}

// AuditEntries 返回审计条目的**快照**（并发安全）。
//
// ★★ 这是读取审计日志的**唯一**入口：★★ 返回的是副本 ⇒ 调用方拿到后
// ★★ 即使此刻还有 onResponse goroutine 在追加，也不会与之竞争。
func (h *ComplianceHook) AuditEntries() []AuditEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]AuditEntry, len(h.auditLog))
	copy(out, h.auditLog)
	return out
}

// SetAuditEntries 用给定的条目**整体替换**审计日志（并发安全）。
//
// ★ 供测试/夹具预置审计条目使用（★ 取代旧实现里「直接给导出字段赋值」的做法
// ★ ——★ 那条路绕过了锁 ⇒ 与并发 onResponse 竞争）。
func (h *ComplianceHook) SetAuditEntries(entries []AuditEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.auditLog = make([]AuditEntry, len(entries))
	copy(h.auditLog, entries)
}

// AuditEntry records a request-response pair for compliance review.
type AuditEntry struct {
	RequestID    string
	SessionID    string
	Model        string
	Blocked      bool
	BlockReason  string
	InputTokens  int
	OutputTokens int
}

// NewComplianceHook creates a hook with default banned keywords.
func NewComplianceHook() *ComplianceHook {
	return &ComplianceHook{
		BannedKeywords: []string{
			"exploit", "hack", "bypass", "jailbreak",
			"ignore previous instructions", "disregard safety",
		},
		auditLog: make([]AuditEntry, 0),
	}
}

// OnRequest checks for banned content in the request body.
//
// W3.2 semantics demonstrated:
//   - Returns DecisionBlock if banned content found
//   - Sets ctx.State for onResponse correlation
//   - Does not modify request (no rewrite)
func (h *ComplianceHook) OnRequest(inv *contract.Invocation) *contract.Reply {
	if inv == nil || inv.Context == nil || inv.Request == nil {
		return &contract.Reply{
			ContractVersion: contract.ContractVersion,
			Decision:        contract.DecisionContinue,
			Error:           "invalid invocation",
		}
	}

	// Extract request body text
	bodyText := string(inv.Request.Body.Body)
	lowerBody := strings.ToLower(bodyText)

	// Check for banned keywords
	for _, keyword := range h.BannedKeywords {
		if strings.Contains(lowerBody, strings.ToLower(keyword)) {
			// Block the request
			return &contract.Reply{
				ContractVersion: contract.ContractVersion,
				Decision:        contract.DecisionBlock,
				Reason:          fmt.Sprintf("content policy violation: banned keyword '%s'", keyword),
			}
		}
	}

	// Extract metadata for audit correlation
	inputTokens := estimateTokens(bodyText)

	// Set state for onResponse correlation (W3.3)
	if inv.Context.State == nil {
		inv.Context.State = make(map[string]string)
	}
	inv.Context.State["input_tokens"] = fmt.Sprintf("%d", inputTokens)
	inv.Context.State["model"] = inv.Request.Model
	inv.Context.State["blocked"] = "false"

	return &contract.Reply{
		ContractVersion: contract.ContractVersion,
		Decision:        contract.DecisionContinue,
	}
}

// OnResponse logs metadata for compliance audit.
//
// W3.3 semantics demonstrated:
//   - Receives state from onRequest via ctx.State
//   - Does not modify response (audit only)
//   - Works even if client disconnected (fire-and-forget)
func (h *ComplianceHook) OnResponse(inv *contract.Invocation) *contract.Reply {
	if inv == nil || inv.Context == nil || inv.Response == nil {
		return &contract.Reply{
			ContractVersion: contract.ContractVersion,
			Decision:        contract.DecisionContinue,
			Error:           "invalid invocation",
		}
	}

	// Restore state from onRequest (W3.3 correlation)
	inputTokens := 0
	if val := inv.Context.State["input_tokens"]; val != "" {
		fmt.Sscanf(val, "%d", &inputTokens)
	}
	model := inv.Context.State["model"]
	blocked := inv.Context.State["blocked"] == "true"

	// Extract response metadata
	outputTokens := estimateTokens(string(inv.Response.Body.Body))

	// Log audit entry
	entry := AuditEntry{
		RequestID:    inv.Context.RequestID,
		SessionID:    inv.Context.Session,
		Model:        model,
		Blocked:      blocked,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	// ★ 并发安全：onResponse 会被多个 goroutine 并发调用（每个响应一个 goroutine）
	// ⇒ append 必须持写锁。
	h.mu.Lock()
	h.auditLog = append(h.auditLog, entry)
	h.mu.Unlock()

	// No response modification (audit only)
	return &contract.Reply{
		ContractVersion: contract.ContractVersion,
		Decision:        contract.DecisionContinue,
	}
}

// estimateTokens provides rough token count (1 token ≈ 4 chars)
func estimateTokens(text string) int {
	return len(text) / 4
}

// ExtractMessages attempts to parse OpenAI-style request body.
func ExtractMessages(body []byte) ([]map[string]interface{}, error) {
	var payload struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Messages, nil
}

// ContainsBannedContent checks if any message contains banned keywords.
func (h *ComplianceHook) ContainsBannedContent(messages []map[string]interface{}) (bool, string) {
	for _, msg := range messages {
		if content, ok := msg["content"].(string); ok {
			lowerContent := strings.ToLower(content)
			for _, keyword := range h.BannedKeywords {
				if strings.Contains(lowerContent, strings.ToLower(keyword)) {
					return true, keyword
				}
			}
		}
	}
	return false, ""
}

// ServeHTTP implements the HTTP handler interface for external hook hosting.
//
// This shows how an external hook host would expose the hooks via HTTP:
//   - POST /hooks/v1/on_request → OnRequest
//   - POST /hooks/v1/on_response → OnResponse
//
// The host (externalhost.Host) handles:
//   - JSON encoding/decoding
//   - Timeout enforcement
//   - Error handling
//
// The hook implementation only needs to implement the business logic.
func (h *ComplianceHook) ServeHTTP(endpoint string, body []byte) ([]byte, error) {
	var inv contract.Invocation
	if err := json.Unmarshal(body, &inv); err != nil {
		return nil, fmt.Errorf("decode invocation: %w", err)
	}

	var reply *contract.Reply
	switch endpoint {
	case "/hooks/v1/on_request":
		reply = h.OnRequest(&inv)
	case "/hooks/v1/on_response":
		reply = h.OnResponse(&inv)
	default:
		return nil, fmt.Errorf("unknown endpoint: %s", endpoint)
	}

	return json.Marshal(reply)
}

// GetAuditReport returns a formatted compliance audit report.
//
// ★ 并发安全：读取 AuditLog 必须持读锁（写侧是并发 OnResponse goroutine）。
func (h *ComplianceHook) GetAuditReport() string {
	// ★ 先在读锁内**快照**一份，再在锁外格式化（避免长时间持锁）。
	h.mu.RLock()
	entries := make([]AuditEntry, len(h.auditLog))
	copy(entries, h.auditLog)
	h.mu.RUnlock()

	var buf bytes.Buffer
	buf.WriteString("=== Compliance Audit Report ===\n")
	buf.WriteString(fmt.Sprintf("Total Requests: %d\n", len(entries)))

	blocked := 0
	totalInput := 0
	totalOutput := 0
	for _, entry := range entries {
		if entry.Blocked {
			blocked++
		}
		totalInput += entry.InputTokens
		totalOutput += entry.OutputTokens
	}

	buf.WriteString(fmt.Sprintf("Blocked: %d\n", blocked))
	buf.WriteString(fmt.Sprintf("Total Input Tokens: %d\n", totalInput))
	buf.WriteString(fmt.Sprintf("Total Output Tokens: %d\n", totalOutput))

	return buf.String()
}
