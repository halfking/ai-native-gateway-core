package taskprofile

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// handler_audit_test.go — R45（R43 §五#3 落地）钉桩：四个变更端点的审计
// hook 投递语义。
//
// 语义约定（见 handler.go AuditEvent 注释）：
//   - 只有真实变更尝试才发事件；校验拒绝（400）与 nil-pool 503 不发；
//   - success 与 failure 都发；
//   - hook panic 隔离、nil hook 零开销。
//
// 本文件覆盖默认门（无 DB）：reload 端点（唯一不需要 pool 的变更端点）的
// 成功/失败投递、无尝试不发事件、panic 隔离。apply/import/corrections 的
// 成功路径投递在 -tags=integration 的真库 E2E 中钉（见
// e2e_loop_integration_test.go TestTaskProfileE2E_AuditHook_RealDB）。

type auditSink struct {
	events []AuditEvent
}

func (s *auditSink) record(ev AuditEvent) { s.events = append(s.events, ev) }

func auditRecorder() (*Handlers, *auditSink) {
	h := NewHandlers(nil) // nil pool: apply/import/corrections 走 503 短路
	s := &auditSink{}
	h.SetAuditHook(s.record)
	return h, s
}

func postReq(t *testing.T, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/admin/task-profile/reload", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/admin/task-profile/reload", strings.NewReader(body))
	}
	return r
}

// reload 成功（overlay 未设置 → 回落默认档案）必须投递 success 事件，
// detail 携带 registry_version，Request 非 nil 供 sink 提取 actor。
func TestAuditHook_ReloadSuccessFires(t *testing.T) {
	h, sink := auditRecorder()
	w := httptest.NewRecorder()
	t.Setenv(OverlayEnvVar, "")
	h.handleReload(w, postReq(t, ""))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(sink.events), sink.events)
	}
	ev := sink.events[0]
	if ev.Action != AuditActionReload || ev.Outcome != "success" {
		t.Errorf("action/outcome = %s/%s, want reload/success", ev.Action, ev.Outcome)
	}
	if ev.Detail["registry_version"] == nil {
		t.Errorf("detail lacks registry_version: %+v", ev.Detail)
	}
	if ev.Request == nil {
		t.Error("Request must be non-nil for actor extraction")
	}
}

// reload 失败（overlay 指向坏文件）必须投递 failure 事件。
func TestAuditHook_ReloadFailureFires(t *testing.T) {
	h, sink := auditRecorder()
	// 非法 overlay：存在但内容不是合法 JSON。
	tmp := t.TempDir() + "/bad-overlay.json"
	if err := os.WriteFile(tmp, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write bad overlay: %v", err)
	}
	t.Setenv(OverlayEnvVar, tmp)

	w := httptest.NewRecorder()
	h.handleReload(w, postReq(t, ""))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1 (failure must be audited)", len(sink.events))
	}
	if ev := sink.events[0]; ev.Outcome != "failure" || ev.Detail["error"] == nil {
		t.Errorf("failure event malformed: %+v", ev)
	}
}

// nil-pool 503（apply/import/corrections）= 无变更尝试 → 不发事件。
func TestAuditHook_NoAttemptNoEvent_NilPool(t *testing.T) {
	h, sink := auditRecorder()

	w := httptest.NewRecorder()
	h.handleApplyTierConfig(w, httptest.NewRequest(http.MethodPost,
		"/api/admin/task-profile/apply-tier-config", strings.NewReader(`{}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("apply status = %d, want 503", w.Code)
	}

	w = httptest.NewRecorder()
	h.handleImportCorrections(w, httptest.NewRequest(http.MethodPost,
		"/api/admin/task-profile/corrections/import", strings.NewReader("col,row\n")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("import status = %d, want 503", w.Code)
	}

	w = httptest.NewRecorder()
	h.handleCreateCorrection(w, httptest.NewRequest(http.MethodPost,
		"/api/admin/task-profile/corrections", strings.NewReader(`{}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("corrections status = %d, want 503 (ensurePool precedes validation)", w.Code)
	}

	if len(sink.events) != 0 {
		t.Fatalf("no-attempt paths must not audit, got %+v", sink.events)
	}
}

// hook panic 不得打断端点（审计隔离）。
func TestAuditHook_PanicIsolated(t *testing.T) {
	h := NewHandlers(nil)
	h.SetAuditHook(func(AuditEvent) { panic("sink exploded") })
	t.Setenv(OverlayEnvVar, "")

	w := httptest.NewRecorder()
	h.handleReload(w, postReq(t, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("endpoint must survive hook panic, status = %d", w.Code)
	}
}

// nil hook 零开销（不 panic、不影响响应）。
func TestAuditHook_NilHookNoOp(t *testing.T) {
	h := NewHandlers(nil)
	t.Setenv(OverlayEnvVar, "")
	w := httptest.NewRecorder()
	h.handleReload(w, postReq(t, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
