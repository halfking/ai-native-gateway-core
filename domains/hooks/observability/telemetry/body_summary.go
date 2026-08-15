package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// CO-5 (docs/修订0811/19 §3, M2): when sessions_v2.enabled is on, session
// history reconstruction moves to the V2 tables (session_turns), so
// request_logs_bodies no longer needs to carry full request/response bodies.
// Summary mode replaces the full body persisted to request_logs_bodies_hot
// with a digest envelope: mode, byte length, sha256 of the full body and a
// bounded UTF-8 head prefix — enough for audit correlation while shrinking
// per-row storage. sessions_v2 disabled ⇒ behaviour is unchanged (full
// bodies), pinned by TestUpdateRequestLog_BodiesFullModeUnchanged.
//
// An explicit platform flag (sessions_v2.request_bodies_full, default false)
// restores full bodies under sessions_v2 for incident debugging.

const (
	// bodySummaryModeDigest marks the digest envelope persisted in place of
	// a full body.
	bodySummaryModeDigest = "digest"
	// bodySummaryHeadBytes bounds the retained head prefix of the raw body
	// inside the digest envelope. Aligned with the request_logs preview
	// policy (bounded audit head), but larger since this is the only body
	// retained in summary mode.
	bodySummaryHeadBytes = 2048
)

// gwBodySummary is the digest envelope written to request_logs_bodies_hot in
// summary mode. The outer JSON object carries a single "_gw_body_summary"
// key so consumers (admin log views, forensics export) can detect the
// downsampled form without colliding with real body fields.
type gwBodySummary struct {
	Mode          string `json:"mode"`
	Bytes         int    `json:"bytes"`
	SHA256        string `json:"sha256"`
	Head          string `json:"head"`
	HeadTruncated bool   `json:"head_truncated"`
}

type gwBodySummaryEnvelope struct {
	Summary gwBodySummary `json:"_gw_body_summary"`
}

// bodySummaryEnabledFn is the feature-flag seam consulted by the bodies
// write path. Stored in an atomic.Value (the telemetry worker flushes under
// load) so a hot-reload of settings and a test override never race on the
// function pointer. The settings-backed default preserves hot-reload; tests
// override it because settings.Global is nil in the unit-test binary
// (same seam pattern as internal/sessionv2mirror).
type bodySummaryEnabledFn func() bool

var bodySummaryEnabledPtr atomic.Value // holds bodySummaryEnabledFn

func init() {
	bodySummaryEnabledPtr.Store(bodySummaryEnabledFn(defaultBodiesSummaryEnabled))
}

// defaultBodiesSummaryEnabled reads the live platform settings: summary mode
// is active only when sessions_v2.enabled is true.
func defaultBodiesSummaryEnabled() bool {
	return settings.GetPlatformBool("sessions_v2.enabled", false)
}

// requestBodiesSummaryEnabled reports whether request_logs_bodies writes
// should be downsampled to digest envelopes.
func requestBodiesSummaryEnabled() bool {
	fn, _ := bodySummaryEnabledPtr.Load().(bodySummaryEnabledFn)
	if fn == nil {
		return false
	}
	return fn()
}

// requestBodiesSummaryEnabledFn returns the current seam so tests can
// restore it after overriding.
func requestBodiesSummaryEnabledFn() (bodySummaryEnabledFn, bool) {
	fn, ok := bodySummaryEnabledPtr.Load().(bodySummaryEnabledFn)
	if !ok || fn == nil {
		return nil, false
	}
	return fn, true
}

// setRequestBodiesSummaryEnabledForTest replaces the seam. Test-only;
// production never overrides.
func setRequestBodiesSummaryEnabledForTest(fn bodySummaryEnabledFn) {
	if fn == nil {
		fn = bodySummaryEnabledFn(func() bool { return false })
	}
	bodySummaryEnabledPtr.Store(fn)
}

// summarizeBodyJSON converts a full body JSON literal (the strPtrToJSON
// output bound for request_logs_bodies_hot) into the digest envelope.
//
// No-payload sentinels are passed through unchanged so summary mode cannot
// alter the NULL/{} semantics relied on by upsertRequestLogBodies ("missing
// bodies stay NULL; metadata-only updates cannot erase a captured body").
// Summarization must never fail the write: on any marshalling error the
// original body is returned.
func summarizeBodyJSON(bodyJSON string) string {
	switch bodyJSON {
	case "", "null", "{}":
		return bodyJSON
	}
	head := bodyJSON
	truncated := false
	if len(head) > bodySummaryHeadBytes {
		head = head[:bodySummaryHeadBytes]
		// Keep the retained head valid UTF-8 even if the cut lands inside
		// a multi-byte rune (bodies are valid JSON, hence valid UTF-8).
		for len(head) > 0 && !utf8.ValidString(head) {
			head = head[:len(head)-1]
		}
		truncated = true
	}
	digest := sha256.Sum256([]byte(bodyJSON))
	envelope, err := json.Marshal(gwBodySummaryEnvelope{
		Summary: gwBodySummary{
			Mode:          bodySummaryModeDigest,
			Bytes:         len(bodyJSON),
			SHA256:        hex.EncodeToString(digest[:]),
			Head:          head,
			HeadTruncated: truncated,
		},
	})
	if err != nil {
		return bodyJSON
	}
	return string(envelope)
}
