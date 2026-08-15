package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// bodySummaryEnvelope is the observer-side view of the digest envelope that
// summary mode writes into request_logs_bodies_hot in place of the full
// body. Tests decode the persisted payload into this shape instead of
// calling the production summarizer (no circular assertions).
type bodySummaryEnvelope struct {
	Mode          string `json:"mode"`
	Bytes         int    `json:"bytes"`
	SHA256        string `json:"sha256"`
	Head          string `json:"head"`
	HeadTruncated bool   `json:"head_truncated"`
}

type bodySummaryEnvelopeOuter struct {
	Summary bodySummaryEnvelope `json:"_gw_body_summary"`
}

// bodySummaryMatcher asserts that the value persisted to
// request_logs_bodies_hot is a digest envelope of rawBody (and NOT the full
// body itself).
type bodySummaryMatcher struct {
	rawBody string
}

func (m bodySummaryMatcher) Match(value interface{}) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	var outer bodySummaryEnvelopeOuter
	if err := json.Unmarshal([]byte(s), &outer); err != nil {
		return false
	}
	sum := outer.Summary
	if sum.Mode != "digest" {
		return false
	}
	if sum.Bytes != len(m.rawBody) {
		return false
	}
	digest := sha256.Sum256([]byte(m.rawBody))
	if sum.SHA256 != hex.EncodeToString(digest[:]) {
		return false
	}
	if !strings.HasPrefix(m.rawBody, sum.Head) {
		return false
	}
	if sum.HeadTruncated && len(sum.Head) >= len(m.rawBody) {
		return false
	}
	// The whole point of summary mode: the persisted payload must not
	// contain the tail of a large body.
	if len(m.rawBody) > len(sum.Head) {
		tail := m.rawBody[len(m.rawBody)-64:]
		if strings.Contains(s, tail) {
			return false
		}
	}
	return true
}

// longBodyForSummary builds a valid JSON body large enough to exceed the
// retained head window, with a unique tail marker so tests can prove the
// tail is dropped.
func longBodyForSummary(tailMarker string) string {
	var sb strings.Builder
	sb.WriteString(`{"model":"glm-5","messages":[{"role":"user","content":"`)
	sb.WriteString(strings.Repeat("summarize-me-", 512))
	sb.WriteString(`"}],"note":"`)
	sb.WriteString(tailMarker)
	sb.WriteString(`"}`)
	return sb.String()
}

func withBodiesSummaryMode(t *testing.T, enabled bool) {
	t.Helper()
	prev, hadPrev := requestBodiesSummaryEnabledFn()
	setRequestBodiesSummaryEnabledForTest(func() bool { return enabled })
	t.Cleanup(func() {
		if hadPrev {
			setRequestBodiesSummaryEnabledForTest(prev)
		}
	})
}

// TestUpdateRequestLog_BodiesSummaryModeWritesDigest (CO-5): when summary
// mode is active (sessions_v2.enabled), updateRequestLog must persist a
// digest envelope (mode/bytes/sha256/bounded head) into
// request_logs_bodies_hot instead of the full request/response body.
func TestUpdateRequestLog_BodiesSummaryModeWritesDigest(t *testing.T) {
	withBodiesSummaryMode(t, true)

	requestBody := longBodyForSummary("REQ-TAIL-MARKER")
	responseBody := longBodyForSummary("RESP-TAIL-MARKER")

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-summary-update", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	status := RequestStatusSuccess
	requestLogArgs := requestLogUpdateArgs(RequestLogEntry{
		Success:       true,
		RequestStatus: &status,
	})
	mockDB.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(requestLogArgs...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(
			pgxmock.AnyArg(),
			bodySummaryMatcher{rawBody: requestBody},
			bodySummaryMatcher{rawBody: responseBody},
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.updateRequestLog(&RequestLogEntry{
		RequestID:    "req-summary-update",
		Op:           RequestLogUpdate,
		Success:      true,
		RequestStatus: &status,
		RequestBody:  &requestBody,
		ResponseBody: &responseBody,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}
