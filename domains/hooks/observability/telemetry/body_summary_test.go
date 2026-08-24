package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/settings"
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
	t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "")
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
	t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "digest-canary")

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
			pgxmock.AnyArg(), // tenant_id
			bodySummaryMatcher{rawBody: requestBody},
			bodySummaryMatcher{rawBody: responseBody},
			pgxmock.AnyArg(), // outbound_body (Phase 1)
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.updateRequestLog(&RequestLogEntry{
		RequestID:       "req-summary-update",
		Op:              RequestLogUpdate,
		Success:         true,
		RequestStatus:   &status,
		ApplicationCode: strptr("digest-canary"),
		RequestBody:     &requestBody,
		ResponseBody:    &responseBody,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// fullBodyMatcher asserts the persisted payload is exactly the expected
// string (the verbatim full body / sentinel).
type fullBodyMatcher struct {
	want string
}

func (m fullBodyMatcher) Match(value interface{}) bool {
	s, ok := value.(string)
	return ok && s == m.want
}

// TestUpdateRequestLog_BodiesFullModeUnchanged (CO-5 regression pin): with
// summary mode OFF (sessions_v2 not enabled — the default deployment), the
// bodies write path must keep its exact current behaviour: full bodies are
// persisted verbatim, missing bodies stay "null".
func TestUpdateRequestLog_BodiesFullModeUnchanged(t *testing.T) {
	longBody := longBodyForSummary("FULL-MODE-TAIL")
	smallBody := `{"messages":[{"role":"user","content":"hi"}]}`

	tests := []struct {
		name             string
		requestBody      *string
		responseBody     *string
		wantRequestBody  string
		wantResponseBody string
	}{
		{
			name:             "large bodies persisted verbatim",
			requestBody:      &longBody,
			responseBody:     &longBody,
			wantRequestBody:  longBody,
			wantResponseBody: longBody,
		},
		{
			name:             "small bodies persisted verbatim",
			requestBody:      &smallBody,
			responseBody:     &smallBody,
			wantRequestBody:  smallBody,
			wantResponseBody: smallBody,
		},
		{
			name:             "missing bodies stay null",
			requestBody:      nil,
			responseBody:     nil,
			wantRequestBody:  "null",
			wantResponseBody: "null",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withBodiesSummaryMode(t, false)

			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mockDB.Close()

			mockDB.ExpectBegin()
			mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
				WithArgs("req-full-mode", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
					pgxmock.AnyArg(), // tenant_id
					fullBodyMatcher{want: tc.wantRequestBody},
					fullBodyMatcher{want: tc.wantResponseBody},
					pgxmock.AnyArg(), // outbound_body (Phase 1)
				).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mockDB.ExpectCommit()

			client := &Client{requestLogDB: mockDB}
			err = client.updateRequestLog(&RequestLogEntry{
				RequestID:     "req-full-mode",
				Op:            RequestLogUpdate,
				Success:       true,
				RequestStatus: &status,
				RequestBody:   tc.requestBody,
				ResponseBody:  tc.responseBody,
			})
			require.NoError(t, err)
			require.NoError(t, mockDB.ExpectationsWereMet())
		})
	}
}

// TestInsertRequestLog_BodiesSummaryModeWritesDigest (CO-5): the initial
// request-log INSERT path (in_progress write) must also downsample bodies
// under summary mode — otherwise the first write would persist the full
// body and the digest envelope could never shrink the row.
func TestInsertRequestLog_BodiesSummaryModeWritesDigest(t *testing.T) {
	withBodiesSummaryMode(t, true)
	t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "digest-canary")

	requestBody := longBodyForSummary("INSERT-REQ-TAIL")
	responseBody := longBodyForSummary("INSERT-RESP-TAIL")

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	usageInsertArgs := make([]interface{}, 18)
	for index := range usageInsertArgs {
		usageInsertArgs[index] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO usage_ledger_hot`).
		WithArgs(usageInsertArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	requestInsertArgs := make([]interface{}, 99)
	for index := range requestInsertArgs {
		requestInsertArgs[index] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO request_logs_hot`).
		WithArgs(requestInsertArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(), // tenant_id
			bodySummaryMatcher{rawBody: requestBody},
			bodySummaryMatcher{rawBody: responseBody},
			pgxmock.AnyArg(), // outbound_body (Phase 1)
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.insertRequestLog(&RequestLogEntry{
		RequestID:       "req-summary-insert",
		Op:              RequestLogInsert,
		ApplicationCode: strptr("digest-canary"),
		RequestBody:     &requestBody,
		ResponseBody:    &responseBody,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// fakeSettingsBackend is a minimal in-memory settings backend (same pattern
// as domains/hooks/compression tests).
type fakeSettingsBackend struct {
	store map[string][]byte
}

func (f *fakeSettingsBackend) Get(scope settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}
func (f *fakeSettingsBackend) GetTenant(tenantID, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}

// withSessionsV2Settings swaps settings.Global for a registry carrying the
// real sessions_v2 specs plus the given platform overrides.
func withSessionsV2Settings(t *testing.T, overrides map[string]bool) {
	t.Helper()
	prevGlobal := settings.Global
	prevSeam, hadSeam := requestBodiesSummaryEnabledFn()
	t.Cleanup(func() {
		settings.Global = prevGlobal
		if hadSeam {
			setRequestBodiesSummaryEnabledForTest(prevSeam)
		}
	})

	store := map[string][]byte{}
	for key, val := range overrides {
		store[key] = []byte(strconvFormatBool(val))
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.SessionsV2Specs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
	// Pin the seam to the settings-backed default so the test exercises the
	// real flag combination rather than an override.
	setRequestBodiesSummaryEnabledForTest(defaultBodiesSummaryEnabled)
}

func strconvFormatBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestRequestBodiesSummaryEnabled_FlagCombination pins full body persistence
// regardless of historical settings_kv values. Summary writes are disabled.
func TestRequestBodiesSummaryEnabled_FlagCombination(t *testing.T) {
	tests := []struct {
		name                  string
		sessionsV2On          bool
		setBodiesFullOverride bool
		bodiesFullOn          bool
		wantSummaryOn         bool
	}{
		{name: "sessions_v2 disabled keeps full bodies", sessionsV2On: false, bodiesFullOn: false, wantSummaryOn: false},
		{name: "sessions_v2 enabled defaults to full bodies", sessionsV2On: true, wantSummaryOn: false},
		{name: "legacy explicit false still keeps full bodies", sessionsV2On: true, setBodiesFullOverride: true, bodiesFullOn: false, wantSummaryOn: false},
		{name: "explicit true keeps full bodies", sessionsV2On: true, setBodiesFullOverride: true, bodiesFullOn: true, wantSummaryOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			overrides := map[string]bool{"sessions_v2.enabled": tc.sessionsV2On}
			if tc.setBodiesFullOverride {
				overrides["sessions_v2.request_bodies_full"] = tc.bodiesFullOn
			}
			withSessionsV2Settings(t, overrides)

			require.Equal(t, tc.wantSummaryOn, requestBodiesSummaryEnabled())
		})
	}
}

func TestRequestBodiesSummaryEnabled_ApplicationCanary(t *testing.T) {
	withBodiesSummaryMode(t, true)

	t.Run("empty allowlist keeps application writes full", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "")
		require.False(t, requestBodiesSummaryEnabled("any-app"))
	})

	t.Run("non-canary application retains full body", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "canary-a, canary-b")
		require.False(t, requestBodiesSummaryEnabled("ordinary-app"))
	})

	t.Run("canary application enables digest", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "canary-a, canary-b")
		require.True(t, requestBodiesSummaryEnabled("canary-b"))
	})
}

// TestUpdateRequestLog_BodiesSummaryModeKeepsEmptySemantics (CO-5): summary
// mode must not alter the no-payload semantics of the bodies table — a nil
// body stays "null" (SQL NULL) and an empty/invalid body stays "{}" — and a
// small body is still downsampled uniformly (mode is row-uniform, no mixed
// full/digest rows depending on size).
func TestUpdateRequestLog_BodiesSummaryModeKeepsEmptySemantics(t *testing.T) {
	smallBody := `{"ok":true}`

	tests := []struct {
		name            string
		requestBody     *string
		wantRequestBody interface{} // string sentinel or bodySummaryMatcher
	}{
		{name: "nil body stays null sentinel", requestBody: nil, wantRequestBody: fullBodyMatcher{want: "null"}},
		// Pre-existing pipeline semantics: sanitizeJSONField drops an empty
		// body to nil before the bodies write, so it persists as "null".
		{name: "empty body dropped to null by sanitize", requestBody: strptr(""), wantRequestBody: fullBodyMatcher{want: "null"}},
		{name: "small body still downsampled", requestBody: &smallBody, wantRequestBody: bodySummaryMatcher{rawBody: smallBody}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withBodiesSummaryMode(t, true)
			t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "digest-canary")

			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mockDB.Close()

			mockDB.ExpectBegin()
			mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
				WithArgs("req-empty-semantics", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
					pgxmock.AnyArg(), // tenant_id
					tc.wantRequestBody,
					fullBodyMatcher{want: "null"},
					pgxmock.AnyArg(), // outbound_body (Phase 1)
				).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mockDB.ExpectCommit()

			client := &Client{requestLogDB: mockDB}
			err = client.updateRequestLog(&RequestLogEntry{
				RequestID:       "req-empty-semantics",
				Op:              RequestLogUpdate,
				Success:         true,
				RequestStatus:   &status,
				ApplicationCode: strptr("digest-canary"),
				RequestBody:     tc.requestBody,
				ResponseBody:    nil,
			})
			require.NoError(t, err)
			require.NoError(t, mockDB.ExpectationsWereMet())
		})
	}
}
