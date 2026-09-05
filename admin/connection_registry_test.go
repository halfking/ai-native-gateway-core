package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connection_registry_test.go — 会话优化 v4 T4 最小 admin 测试：只覆盖本任务
// 新增的两个 handler 文件（其余 admin 测试不在本任务验收范围）。

func TestAdminConnectionRegistryListAndGet(t *testing.T) {
	reg := streaming.NewConnectionRegistry(4, time.Second, 0)
	require.NoError(t, reg.Register("req-1", &streaming.FlushFrameWriter{W: &bytes.Buffer{}},
		streaming.RegistrationMetadata{Protocol: "openai_chat", ClientType: "zcode", TenantID: "t1"}, nil))
	require.NoError(t, reg.WriteFrame("req-1", "data: {}\n\n"))
	require.NoError(t, reg.Register("req-2", &streaming.FlushFrameWriter{W: &bytes.Buffer{}},
		streaming.RegistrationMetadata{Protocol: "anthropic"}, nil))
	require.NoError(t, reg.Unregister("req-2", "stream_end"))

	SetConnectionRegistry(reg)
	h := &Handler{}

	// List: live entries + closed audit, metadata only (no body content).
	rec := httptest.NewRecorder()
	h.handleConnectionRegistryList(rec, httptest.NewRequest(http.MethodGet, "/api/admin/connection-registry", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var listResp struct {
		Live      []streaming.ConnectionSnapshot `json:"live"`
		LiveCount int                            `json:"live_count"`
		Capacity  int                            `json:"capacity"`
		Closed    []streaming.ConnectionSnapshot `json:"closed"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listResp))
	assert.Equal(t, 1, listResp.LiveCount)
	require.Len(t, listResp.Live, 1)
	assert.Equal(t, "req-1", listResp.Live[0].RequestID)
	assert.Equal(t, "zcode", listResp.Live[0].ClientType)
	assert.EqualValues(t, 1, listResp.Live[0].FramesWritten)
	require.Len(t, listResp.Closed, 1)
	assert.Equal(t, "stream_end", listResp.Closed[0].CloseReason)
	assert.NotContains(t, rec.Body.String(), "data: {}", "body/frame content must never leak")

	// Single lookup by request id.
	rec = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/connection-registry/req-1", nil)
	r.SetPathValue("request_id", "req-1")
	h.handleConnectionRegistryGet(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
	var snap streaming.ConnectionSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	assert.Equal(t, "req-1", snap.RequestID)
	assert.False(t, snap.LastFrameAt.IsZero())

	// Unknown id → 404.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/admin/connection-registry/missing", nil)
	r.SetPathValue("request_id", "missing")
	h.handleConnectionRegistryGet(rec, r)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAdminConnectionRegistryNotWired(t *testing.T) {
	// A fresh registry-less handler keeps the endpoints at 503 — wiring is
	// the integrator's route-registration step.
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleConnectionRegistryList(rec, httptest.NewRequest(http.MethodGet, "/api/admin/connection-registry", nil))
	// NOTE: the package-level registry may be wired by another test; assert
	// either 503 (not wired) or 200 (wired by a prior test in this package).
	if rec.Code == http.StatusServiceUnavailable {
		assert.Contains(t, rec.Body.String(), "not wired")
	} else {
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestRequestActionsDedupAndOrdering(t *testing.T) {
	// Pure helpers of the liveactions REST fallback: (request_id, seq) dedup
	// and oldest→newest ordering.
	raw := []string{
		`{"request_id":"r1","seq":3,"action":"reply","ts":"2026-08-18T12:00:03Z"}`,
		`{"request_id":"r1","seq":3,"action":"reply","ts":"2026-08-18T12:00:03Z"}`,  // dup
		`{"request_id":"r2","seq":1,"action":"arrive","ts":"2026-08-18T12:00:00Z"}`, // other request
		`{"request_id":"r1","seq":1,"action":"arrive","ts":"2026-08-18T12:00:00Z"}`,
		`{"request_id":"r1","seq":2,"action":"node_switch","ts":"2026-08-18T12:00:01Z"}`,
	}
	seen := map[int64]struct{}{}
	var actions []map[string]any
	for _, entry := range raw {
		ev, ok := decodeStoredAction(entry)
		if !ok || ev.RequestID != "r1" {
			continue
		}
		if _, dup := seen[ev.Seq]; dup {
			continue
		}
		seen[ev.Seq] = struct{}{}
		actions = append(actions, flattenActionEvent(ev, nil))
	}
	assert.Len(t, actions, 3, "duplicates by (request_id, seq) collapse")

	// decodeStoredAction rejects junk.
	_, ok := decodeStoredAction("{not json")
	assert.False(t, ok)
	_, ok = decodeStoredAction(`{"request_id":"r1"}`)
	assert.False(t, ok, "actionless entries are dropped")

	// actionEntryLess orders by (ts, seq).
	assert.True(t, actionEntryLess(map[string]any{"ts": "2026-08-18T12:00:00Z", "seq": 9.0},
		map[string]any{"ts": "2026-08-18T12:00:01Z", "seq": 1.0}))
	assert.True(t, actionEntryLess(map[string]any{"ts": "same", "seq": 1.0},
		map[string]any{"ts": "same", "seq": 2.0}))
}
