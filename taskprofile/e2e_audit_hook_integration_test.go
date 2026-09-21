//go:build integration

// e2e_audit_hook_integration_test.go — R45（R43 §五#3 落地）真库钉桩：
// 四个变更端点中需要 DB 的三个（corrections create / import /
// apply-tier-config）的成功与失败路径都必须投递审计事件。reload 端点的
// 投递与"无尝试不发事件/panic 隔离"语义在默认门（handler_audit_test.go）
// 钉住；本文件补 DB 依赖的成功路径。
//
// Run:
//
//	go test -tags=integration -timeout 5m -count=1 ./taskprofile -run AuditHook_RealDB
package taskprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTaskProfileE2E_AuditHook_RealDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, cleanup := dispatchPostgresContainer(t, ctx, e2eSchema)
	defer cleanup()

	// 6 documentation selections so 4 corrections drive an escalation
	// (mirrors the main E2E loop; makes apply-tier-config write 1 row).
	mustAuditExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	auditDocID := func(i int) string {
		return fmt.Sprintf("req-audit-doc-%02d", i)
	}
	for i := 1; i <= 6; i++ {
		mustAuditExec(`INSERT INTO auto_route_selections_hot (request_id, task_type, confidence, profile)
			VALUES ($1, 'documentation', 0.92, 'cli')`, auditDocID(i))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM task_type_corrections WHERE request_id LIKE 'req-audit-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM auto_route_selections_hot WHERE request_id LIKE 'req-audit-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM task_type_tier_config WHERE task_type='documentation' AND tenant_id IS NULL`)
	})

	h := &Handlers{store: NewCorrectionStore(pool)}
	sink := &auditSink{}
	h.SetAuditHook(sink.record)

	post := func(path string, body string) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(http.MethodPost, path, nil)
		} else {
			req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		}
		rec := httptest.NewRecorder()
		switch path {
		case "/api/admin/task-profile/corrections":
			h.handleCreateCorrection(rec, req)
		case "/api/admin/task-profile/corrections/import":
			h.handleImportCorrections(rec, req)
		case "/api/admin/task-profile/apply-tier-config":
			h.handleApplyTierConfig(rec, req)
		}
		return rec
	}

	// ── corrections create：4 改错 + 2 确认 + 1 重复 ─────────────────────
	// （4/6 修正率凑齐 correctionMinSamples=5 的样本量，apply 才有升级可写）
	for i := 1; i <= 4; i++ {
		body, _ := json.Marshal(map[string]any{
			"request_id":      auditDocID(i),
			"human_task_type": "architecture",
			"annotator":       "audit-e2e",
			"reason":          "quality",
		})
		if rec := post("/api/admin/task-profile/corrections", string(body)); rec.Code != http.StatusOK {
			t.Fatalf("correction %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	for i := 5; i <= 6; i++ {
		body, _ := json.Marshal(map[string]any{
			"request_id":      auditDocID(i),
			"human_task_type": "documentation", // 确认：auto 分类正确
			"annotator":       "audit-e2e",
			"reason":          "correct",
		})
		if rec := post("/api/admin/task-profile/corrections", string(body)); rec.Code != http.StatusOK {
			t.Fatalf("confirm %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(sink.events) != 6 {
		t.Fatalf("after 6 corrections: events = %d, want 6", len(sink.events))
	}
	for i, ev := range sink.events {
		if ev.Action != AuditActionCorrectionCreate || ev.Outcome != "success" {
			t.Fatalf("event[%d] = %s/%s, want corrections-create/success", i, ev.Action, ev.Outcome)
		}
		if ev.Detail["annotator"] != "audit-e2e" {
			t.Fatalf("event[%d] detail lacks correction facts: %+v", i, ev.Detail)
		}
		if ev.Request == nil {
			t.Fatalf("event[%d] Request nil (actor extraction impossible)", i)
		}
	}

	dupBody, _ := json.Marshal(map[string]any{
		"request_id":      auditDocID(1),
		"human_task_type": "architecture",
		"annotator":       "audit-e2e",
		"reason":          "quality",
	})
	if rec := post("/api/admin/task-profile/corrections", string(dupBody)); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate correction: status %d, want 409", rec.Code)
	}
	if len(sink.events) != 7 || sink.events[6].Outcome != "failure" {
		t.Fatalf("duplicate must audit as failure: %+v", sink.events)
	}

	// ── import：1 新行成功导入 ──────────────────────────────────────────
	importPayload := CorrectionCSVHeader + "\n" +
		"req-audit-import-1,coding,testing,false,0.60,web,importer,quality,2026-09-18T12:00:00Z\n"
	if rec := post("/api/admin/task-profile/corrections/import", importPayload); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 8 {
		t.Fatalf("after import: events = %d, want 8", len(sink.events))
	}
	impEv := sink.events[7]
	if impEv.Action != AuditActionCorrectionImport || impEv.Outcome != "success" {
		t.Fatalf("import event = %s/%s", impEv.Action, impEv.Outcome)
	}
	if impEv.Detail["imported"] != 1 {
		t.Fatalf("import detail.imported = %v, want 1 (detail = %+v)", impEv.Detail["imported"], impEv.Detail)
	}

	// ── apply-tier-config：documentation c→b 升级写入 1 行 ───────────────
	if rec := post("/api/admin/task-profile/apply-tier-config", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 9 {
		t.Fatalf("after apply: events = %d, want 9", len(sink.events))
	}
	applyEv := sink.events[8]
	if applyEv.Action != AuditActionApplyTierConfig || applyEv.Outcome != "success" {
		t.Fatalf("apply event = %s/%s", applyEv.Action, applyEv.Outcome)
	}
	applied, ok := applyEv.Detail["applied"].([]AppliedTierConfig)
	if !ok || len(applied) != 1 || applied[0].TaskType != "documentation" {
		t.Fatalf("apply detail.applied = %+v, want 1 documentation row", applyEv.Detail["applied"])
	}
}
