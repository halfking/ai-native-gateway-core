package taskprofile

import (
	"os"
	"strings"
	"testing"
)

// R43 (2026-09-18): the FeedbackRecorder was originally attached to the
// routingopt-side CorrectionStore (cmd/gateway/routing_optimizer_init.go),
// but that store is read-only (CorrectionSource → CorrectionStats only), so
// RecordFeedback never fired and the Prometheus classification feedback
// counters stayed at zero. The production recorder must live on the admin
// handlers' store — the only write path (POST /corrections + CSV import).
// These pins keep both sides honest; if the wiring moves, update them in the
// same commit.
func TestAdminWiring_AttachesFeedbackRecorder(t *testing.T) {
	src, err := os.ReadFile("../admin/handler.go")
	if err != nil {
		t.Fatalf("read admin/handler.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "taskprofile.NewHandlers(h.db)") {
		t.Fatal("admin/handler.go no longer constructs taskprofile.NewHandlers; update this pin if the wiring moved")
	}
	if !strings.Contains(s, "SetRecorder(") {
		t.Fatal("admin/handler.go does not call SetRecorder on the taskprofile handlers — " +
			"correction writes will no longer feed the classification feedback counters (R43 leak)")
	}
}

func TestOptimizerSideStore_StaysReadOnly(t *testing.T) {
	src, err := os.ReadFile("../cmd/gateway/routing_optimizer_init.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/routing_optimizer_init.go: %v", err)
	}
	s := string(src)
	if strings.Contains(s, "correctionStore.SetRecorder(") {
		t.Fatal("routing_optimizer_init.go re-attached SetRecorder to the read-only CorrectionSource store — " +
			"that recorder can never fire (no write path reaches this store); attach it on the admin side instead")
	}
	if !strings.Contains(s, "WithCorrectionSource(taskprofileCorrectionSource{store: correctionStore})") {
		t.Fatal("routing_optimizer_init.go no longer wires the CorrectionSource; confidence damping would silently stop")
	}
}

// R45（R43 §五#3 落地）: 审计 hook 的生产接线钉桩——回调存在 ≠ 被调用
// （2135 MiniMax 事故定式：翻译存在但从未接线）。admin/handler.go 必须对
// taskprofile handlers 调用 SetAuditHook，否则四个变更端点的审计事件
// 投递到 nil sink，R43 发现的"零审计留痕"原样复活。
// R46 F5: sink 从 handler.go 内联闭包抽出为命名函数 admin.taskProfileAuditSink
// （行为可测），文本钉桩随之指向新文件；handler.go 钉 SetAuditHook 挂接点。
func TestAdminWiring_AttachesAuditHook(t *testing.T) {
	src, err := os.ReadFile("../admin/handler.go")
	if err != nil {
		t.Fatalf("read admin/handler.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "SetAuditHook(") {
		t.Fatal("admin/handler.go does not call SetAuditHook on the taskprofile handlers — " +
			"the four change endpoints would audit into a nil sink (R43 zero-audit-trail regression)")
	}
	// sink 必须是提取 actor 的生产 sink（GetAuthContext），否则审计事件无法
	// 归因到操作者。
	sink, err := os.ReadFile("../admin/taskprofile_audit_sink.go")
	if err != nil {
		t.Fatalf("read admin/taskprofile_audit_sink.go: %v", err)
	}
	sk := string(sink)
	if !strings.Contains(sk, "GetAuthContext(ev.Request)") {
		t.Fatal("audit sink no longer extracts the operator identity via GetAuthContext — " +
			"taskprofile.audit events would be unattributable (actor=unknown for every mutation)")
	}
	if !strings.Contains(s, "SetAuditHook(taskProfileAuditSink)") {
		t.Fatal("admin/handler.go no longer attaches taskProfileAuditSink as the audit hook — " +
			"restore the wiring or update this pin in the same commit")
	}
}
