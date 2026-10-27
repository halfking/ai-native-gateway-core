package v2

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// Turn body bounds (W1.4) — aligned with magpie SaveGatewayTurn:
//   - 1 MiB per turn JSON after scrub
//   - secret scrub before persist
//   - truncation marked via a synthetic system marker when cut

const (
	// MaxTurnBodyBytes is the hard ceiling for one turn's persisted payload
	// budget (split across request/response directions).
	MaxTurnBodyBytes = 1 << 20 // 1 MiB
)

// ErrRecordingConsentOff surfaces on read paths that must not silently empty.
var ErrRecordingConsentOff = errRecordingConsentOff{}

type errRecordingConsentOff struct{}

func (errRecordingConsentOff) Error() string {
	return "recording_unavailable: session_recording_consent is off"
}

// allowBodiesWriteHook, when non-nil, replaces the live consent check.
// Production leaves it nil. Tests set it via enableRecordingConsentForTest.
var allowBodiesWriteHook func(tenantID string, capturedGeneration int) bool

// RecordingConsentAllowsWrite is the write-path gate: consent on AND the
// caller's captured generation still matches the live generation (magpie
// disable-and-clear race).
func RecordingConsentAllowsWrite(tenantID string, capturedGeneration int) bool {
	if allowBodiesWriteHook != nil {
		return allowBodiesWriteHook(tenantID, capturedGeneration)
	}
	if !settings.RecordingConsentEnabled(tenantID) {
		return false
	}
	if capturedGeneration <= 0 {
		return true
	}
	return capturedGeneration == settings.RecordingConsentGeneration(tenantID)
}

// CaptureRecordingGeneration snapshots the generation at request start.
func CaptureRecordingGeneration(tenantID string) int {
	return settings.RecordingConsentGeneration(tenantID)
}

// boundBodiesRecord scrubs secrets and enforces the 1 MiB/turn ceiling.
// truncated is set when any direction was cut.
func boundBodiesRecord(rec *BodiesRecord) (truncated bool) {
	if rec == nil {
		return false
	}
	var cut bool
	rec.RequestDelta, cut = boundMessages(rec.RequestDelta, MaxTurnBodyBytes/2)
	truncated = truncated || cut
	rec.ResponseDelta, cut = boundMessages(rec.ResponseDelta, MaxTurnBodyBytes/2)
	truncated = truncated || cut
	rec.OutboundBody, cut = boundMessages(rec.OutboundBody, MaxTurnBodyBytes/2)
	truncated = truncated || cut
	return truncated
}

func boundMessages(msgs []Message, budget int) ([]Message, bool) {
	if len(msgs) == 0 || budget <= 0 {
		return msgs, false
	}
	out := make([]Message, 0, len(msgs))
	left := budget
	cut := false
	for _, m := range msgs {
		if left <= 0 {
			cut = true
			break
		}
		m = scrubMessage(m)
		size := messageWireSize(m)
		if size > left {
			m = truncateMessage(m, left)
			cut = true
			size = messageWireSize(m)
		}
		left -= size
		out = append(out, m)
	}
	return out, cut
}

func scrubMessage(m Message) Message {
	if m.Content != "" {
		m.Content = string(errorsx.RedactCredentialShapes([]byte(m.Content)))
	}
	if len(m.ContentRaw) > 0 {
		m.ContentRaw = errorsx.RedactCredentialShapes(m.ContentRaw)
	}
	if m.Name != "" {
		m.Name = string(errorsx.RedactCredentialShapes([]byte(m.Name)))
	}
	return m
}

func messageWireSize(m Message) int {
	b, err := json.Marshal(m)
	if err != nil {
		return len(m.Content) + len(m.ContentRaw) + 64
	}
	return len(b)
}

func truncateMessage(m Message, budget int) Message {
	if budget < 32 {
		m.Content = ""
		m.ContentRaw = nil
		m.RawContent = nil
		m.ToolCalls = nil
		return m
	}
	if m.Content != "" {
		n := budget - 24
		if n < 0 {
			n = 0
		}
		if n > len(m.Content) {
			n = len(m.Content)
		}
		for n > 0 && !utf8.ValidString(m.Content[:n]) {
			n--
		}
		m.Content = m.Content[:n]
		m.ContentRaw = nil
		m.RawContent = nil
	} else if len(m.ContentRaw) > budget-24 {
		m.ContentRaw = m.ContentRaw[:budget-24]
		m.RawContent = nil
	}
	return m
}

// markTruncation appends a synthetic system marker so readers see the cut.
func markTruncation(rec *BodiesRecord) {
	if rec == nil {
		return
	}
	marker := Message{
		Role:    "system",
		Content: "[truncated: turn body exceeded 1MiB bound]",
	}
	rec.ResponseDelta = append(rec.ResponseDelta, marker)
}
