package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/taskprofile"
)

// captureLogRecords 捕获默认 slog 输出为结构化记录。
func captureLogRecords(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	fn()
	out := strings.TrimSpace(buf.String())
	if out == "" {
		return nil
	}
	records := make([]map[string]any, 0, 1)
	for _, line := range strings.Split(out, "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode slog record: %v", err)
		}
		records = append(records, m)
	}
	return records
}

// R46 F5: 生产 sink 的 actor 提取此前零测试执行（wiring 守卫只做文本
// 匹配，e2e 自挂 sink 绕过 admin 接线）——本测试让 actor 提取分支真实
// 运行：带鉴权上下文的请求归因到 username/tenant，无上下文回退
// unknown/default。
func TestTaskProfileAuditSink_AttributesActorFromAuthContext(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/admin/taskprofile/corrections", nil)
	req = SetAuthContext(req, &AuthContext{Username: "ops01", TenantID: "tenant-x"})

	records := captureLogRecords(t, func() {
		taskProfileAuditSink(taskprofile.AuditEvent{
			Action:  taskprofile.AuditActionCorrectionCreate,
			Outcome: "success",
			Detail:  map[string]any{"task_type": "documentation"},
			Request: req,
		})
	})
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 log record, got %d", len(records))
	}
	r := records[0]
	if r["msg"] != "taskprofile.audit" {
		t.Fatalf("unexpected msg: %v", r["msg"])
	}
	if r["actor"] != "ops01" {
		t.Fatalf("expected actor=ops01, got %v", r["actor"])
	}
	if r["tenant_id"] != "tenant-x" {
		t.Fatalf("expected tenant_id=tenant-x, got %v", r["tenant_id"])
	}
	if r["action"] != taskprofile.AuditActionCorrectionCreate || r["outcome"] != "success" {
		t.Fatalf("unexpected action/outcome: %v/%v", r["action"], r["outcome"])
	}
	if r["detail"] == nil {
		t.Fatal("expected detail to be logged")
	}
}

// 无鉴权上下文（nil AuthContext）→ actor/tenant 回退 unknown/default，
// 事件不丢。
func TestTaskProfileAuditSink_FallsBackToUnknownWithoutAuthContext(t *testing.T) {
	records := captureLogRecords(t, func() {
		taskProfileAuditSink(taskprofile.AuditEvent{
			Action:  taskprofile.AuditActionApplyTierConfig,
			Outcome: "failure",
			Request: httptest.NewRequest("POST", "/api/admin/taskprofile/apply-tier-config", nil),
		})
	})
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 log record, got %d", len(records))
	}
	r := records[0]
	if r["actor"] != "unknown" {
		t.Fatalf("expected actor=unknown fallback, got %v", r["actor"])
	}
	if r["tenant_id"] != "default" {
		t.Fatalf("expected tenant_id=default fallback, got %v", r["tenant_id"])
	}
}

// nil Request（防御分支）→ 不 panic，回退 unknown/default。
func TestTaskProfileAuditSink_NilRequestDoesNotPanic(t *testing.T) {
	records := captureLogRecords(t, func() {
		taskProfileAuditSink(taskprofile.AuditEvent{
			Action:  taskprofile.AuditActionReload,
			Outcome: "success",
		})
	})
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 log record, got %d", len(records))
	}
	if records[0]["actor"] != "unknown" {
		t.Fatalf("expected actor=unknown for nil request, got %v", records[0]["actor"])
	}
}

// R47：F8⑨ 契约钉桩——sink 禁止把原始请求的 headers/body 带进审计输出
// （Authorization 头会带出会话凭据）。请求携带敏感头与 body 后，断言
// 捕获的 slog 记录中两者均不出现；未来有人往 sink 加
// slog.Any("request", ev.Request) 之类会被此测试拦截。
func TestTaskProfileAuditSink_NeverLogsHeadersOrBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/admin/taskprofile/corrections",
		strings.NewReader(`{"task_type":"secret-payload"}`))
	req.Header.Set("Authorization", "Bearer super-secret-token")
	req = SetAuthContext(req, &AuthContext{Username: "ops01", TenantID: "tenant-x"})

	records := captureLogRecords(t, func() {
		taskProfileAuditSink(taskprofile.AuditEvent{
			Action:  taskprofile.AuditActionCorrectionCreate,
			Outcome: "failure",
			Detail:  map[string]any{"error": "boom"},
			Request: req,
		})
	})
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 log record, got %d", len(records))
	}
	blob, err := json.Marshal(records[0])
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	for _, forbidden := range []string{"super-secret-token", "secret-payload", "Authorization", "Bearer"} {
		if strings.Contains(string(blob), forbidden) {
			t.Fatalf("audit record leaked sensitive material %q: %s", forbidden, blob)
		}
	}
}

// R47：AuthContext 存在但字段为空串 → 保持 unknown/default 回退
// （ac != nil 但 Username/TenantID 均空的中间态分支补执行）。
func TestTaskProfileAuditSink_EmptyAuthContextFieldsFallBack(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/admin/taskprofile/corrections", nil)
	req = SetAuthContext(req, &AuthContext{Username: "", TenantID: ""})

	records := captureLogRecords(t, func() {
		taskProfileAuditSink(taskprofile.AuditEvent{
			Action:  taskprofile.AuditActionCorrectionCreate,
			Outcome: "success",
			Request: req,
		})
	})
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 log record, got %d", len(records))
	}
	if records[0]["actor"] != "unknown" || records[0]["tenant_id"] != "default" {
		t.Fatalf("expected unknown/default fallback for empty auth fields, got %v/%v",
			records[0]["actor"], records[0]["tenant_id"])
	}
}
