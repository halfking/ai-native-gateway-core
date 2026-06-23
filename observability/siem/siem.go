// Package siem formats audit events into SIEM-friendly formats (CEF, LEEF)
// and forwards them to a Security Information and Event Management system
// (Splunk / QRadar / Elastic / syslog).
//
// Why this package exists:
//   Enterprise customers need to pipe LLM-gateway audit events into their
//   existing SIEM/SOC pipeline for compliance (等保 2.0, GDPR) and threat
//   hunting. CEF (Common Event Format) is the de-facto interchange standard
//   understood by Splunk/QRadar/Elastic out of the box.
//
// Domain boundary (refactor plan §2.2 ⑪ observability/):
//   siem/ OWNS: event formatting (CEF/LEEF) + forwarding (batch/retry/DLQ)
//   siem/ does NOT own: event generation (audit/audit.go), event storage
//   (audit pipeline writes to PG), or policy (what to forward — that is
//   configured in settings_kv, not hardcoded here).
//
// Anti-corruption layer: siem/ does NOT import the audit package. It defines
// a minimal EventRecord interface that audit.Event already satisfies. This
// keeps siem/ testable in isolation and avoids a cycle when audit/ is later
// relocated under observability/audit/.
//
// Safety defaults:
//   - Default endpoint is a LOCAL FILE PATH, not a remote URL. This guarantees
//     that even a misconfigured deployment never leaks audit events to an
//     unintended network destination. Operators must consciously set a remote
//     SIEM endpoint.
//   - CEF extension fields NEVER include prompt/completion content (only
//     metadata: model names, token counts, latency, error kind). The raw
//     request body is never serialized into a CEF line.
package siem

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// CEF version + vendor constants. These appear in every CEF header line.
// Changing Vendor/Product invalidates downstream SIEM parsers — treat as
// stable (see design_intent_test.go).
const (
	CEFVersion  = 0
	CEFVendor   = "Kaixuan"
	CEFProduct  = "LLM Gateway Go"
	CEFPVersion = "1.0.0"
)

// DefaultLocalEndpoint is the safe default: a local file, never a remote URL.
// Operators override this via settings_kv (siem.endpoint) to point at a real
// SIEM collector.
const DefaultLocalEndpoint = "/var/log/llm-gateway/siem-audit.log"

// EventRecord is the anti-corruption interface. audit.Event satisfies it
// without siem/ importing audit/. Fields map 1:1 to the most security-relevant
// audit columns.
type EventRecord interface {
	GetRequestID() string
	GetTimestamp() time.Time
	GetTenantID() string
	GetClientModel() string
	GetOutboundModel() string
	GetProviderID() int
	GetCredentialID() int
	GetLatencyMs() int
	GetSuccess() bool
	GetErrorKind() string
	GetPromptTokens() int
	GetCompletionTokens() int
	GetCostUSD() float64
	GetEventType() string // "" for request events; set for link-layer events
}

// Severity maps an event to a CEF severity (0-10, 10=critical). This is a
// heuristic: failures and state-change events rank higher than successful
// requests. SIEMs use this for alert prioritization.
func severityOf(e EventRecord) int {
	if !e.GetSuccess() {
		switch e.GetErrorKind() {
		case "auth", "quota":
			return 8 // permanent credential failure — likely needs human action
		case "rate_limit":
			return 6
		default:
			return 5
		}
	}
	if e.GetEventType() != "" {
		return 4 // link-layer state change (credential switch, pool degradation)
	}
	return 3 // routine successful request
}

// classNameOf maps an event to a human-readable CEF class name.
func classNameOf(e EventRecord) string {
	if et := e.GetEventType(); et != "" {
		return "GatewayStateChange"
	}
	if !e.GetSuccess() {
		return "LLMRequestFailed"
	}
	return "LLMRequest"
}

// FormatCEF renders a single event as one CEF line (no trailing newline).
// CEF spec: CEF:Version|Vendor|Product|DevVersion|SignatureID|Name|Severity|Extension
//
// The extension is a space-separated list of key=value pairs. Values
// containing spaces or special chars (=|\\) are escaped per CEF rules.
// CRITICAL: no prompt/completion content is ever included — only metadata.
func FormatCEF(e EventRecord) string {
	name := classNameOf(e)
	sev := severityOf(e)

	// Extension fields — metadata only, NEVER content.
	ext := []string{
		"requestId=" + cefEscape(e.GetRequestID()),
		"tenantId=" + cefEscape(e.GetTenantID()),
		"clientModel=" + cefEscape(e.GetClientModel()),
		"outboundModel=" + cefEscape(e.GetOutboundModel()),
		fmt.Sprintf("providerId=%d", e.GetProviderID()),
		fmt.Sprintf("credentialId=%d", e.GetCredentialID()),
		fmt.Sprintf("latencyMs=%d", e.GetLatencyMs()),
		fmt.Sprintf("success=%t", e.GetSuccess()),
		"errorKind=" + cefEscape(e.GetErrorKind()),
		fmt.Sprintf("promptTokens=%d", e.GetPromptTokens()),
		fmt.Sprintf("completionTokens=%d", e.GetCompletionTokens()),
		fmt.Sprintf("costUSD=%.6f", e.GetCostUSD()),
		"eventType=" + cefEscape(e.GetEventType()),
		fmt.Sprintf("ts=%d", e.GetTimestamp().UnixMilli()),
	}

	return fmt.Sprintf("CEF:%d|%s|%s|%s|%s|%s|%d|%s",
		CEFVersion, cefEscape(CEFVendor), cefEscape(CEFProduct), CEFPVersion,
		cefEscape(e.GetEventType()+":llm_gateway"), // SignatureID
		cefEscape(name), sev,
		strings.Join(ext, " "))
}

// cefEscape applies CEF extension-value escaping: backslash-escape =, |, \
// and newline. Without this, a model name containing "|" would corrupt the
// CEF header parse in downstream SIEMs.
func cefEscape(s string) string {
	if s == "" {
		return "-"
	}
	r := strings.NewReplacer(
		"\\", "\\\\",
		"=", "\\=",
		"|", "\\|",
		"\n", "\\n",
		"\r", "\\r",
	)
	return r.Replace(s)
}

// Sink is the forwarding abstraction. Implementations: FileSink (default,
// safe), HTTPSink (remote SIEM, Q4 B3-3).
type Sink interface {
	// Write forwards a formatted CEF line. Must be safe for concurrent use.
	// Returns an error if the line could not be delivered; the caller (a bg
	// worker) handles retry/DLQ.
	Write(ctx context.Context, line string) error
	// Close releases resources (file handle, HTTP conn pool). Idempotent.
	Close() error
}

// FileSink appends CEF lines to a local file. This is the DEFAULT sink: it
// never touches the network, so a misconfigured deployment cannot leak audit
// events. A log collector (fluentd/filebeat) typically tails this file into
// the real SIEM.
type FileSink struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// NewFileSink opens (creating if needed) the file at path. The file is
// opened with append mode so concurrent gateway restarts don't clobber history.
func NewFileSink(path string) (*FileSink, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultLocalEndpoint
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return nil, fmt.Errorf("siem: create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("siem: open log file: %w", err)
	}
	return &FileSink{f: f, path: path}, nil
}

// Write appends one CEF line + newline. Thread-safe via mutex.
func (s *FileSink) Write(ctx context.Context, line string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return fmt.Errorf("siem: file sink closed")
	}
	_, err := io.WriteString(s.f, line+"\n")
	return err
}

// Close closes the underlying file. Idempotent.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

func dirOf(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}
