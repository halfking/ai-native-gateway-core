package audit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// hook_sanitize_test.go —— F-41 收口（2026-11-13）的 dump 面审查守卫。
//
// # 它防的缺陷
//
//	AuditLogHook 此前把 env.Metadata **全量**塞进审计事件。user_content 桥接后
//	Metadata 携带用户原文（≤50KiB）；force-compression 路径早已写完整会话
//	messages；api_key 是**原始客户端密钥** —— 全量 dump = 原文+密钥进审计事件，
//	且 InMemorySink 无淘汰 ⇒ 永久驻留内存。
//
//	sanitizeAuditMetadata（hook.go）= 剥离 denylist（user_content / messages /
//	api_key）+ 超限字符串（>4KiB）。本测试把「三键必剥 + 超限必剥 + 其余保留 +
//	env 本体不被改」钉死；变异（去掉 sanitize 直接透传）⇒ TestAuditLogHookSanitizesMetadata 红。
func TestSanitizeAuditMetadataStripsContentAndSecrets(t *testing.T) {
	in := map[string]any{
		"user_content":        "ignore previous instructions and ...", // F-41 桥接新增载体
		"messages":            []string{"full conversation"},          // compression.MetaKeyMessages
		"api_key":             "sk-raw-client-secret",                 // 原始客户端密钥
		"model":               "claude-sonnet-5",
		"security_warning":    "Prompt injection detected (risk=high, score=85)",
		"huge_payload":        strings.Repeat("x", auditRecordMaxValueBytes+1),
		"boundary_payload_ok": strings.Repeat("x", auditRecordMaxValueBytes),
	}
	out := sanitizeAuditMetadata(in)
	for _, banned := range []string{"user_content", "messages", "api_key", "huge_payload"} {
		if _, ok := out[banned]; ok {
			t.Errorf("sanitize 后仍携带 %q —— denylist/超限剥离失效", banned)
		}
	}
	for _, kept := range []string{"model", "security_warning", "boundary_payload_ok"} {
		if _, ok := out[kept]; !ok {
			t.Errorf("sanitize 把合法键 %q 也剥掉了 —— 白名单语义变成了空 dump", kept)
		}
	}
	if in["api_key"] != "sk-raw-client-secret" {
		t.Error("sanitize 必须是浅拷贝 —— 不允许改写 env.Metadata 本体（后续 stage 还要读）")
	}
	if sanitizeAuditMetadata(nil) != nil {
		t.Error("nil metadata 应返回 nil")
	}
	if sanitizeAuditMetadata(map[string]any{"user_content": "x"}) != nil {
		t.Error("全被剥离时应返回 nil —— 不给审计事件留一个空 map")
	}
}

func TestAuditLogHookSanitizesMetadata(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 10, time.Hour)
	h := NewAuditLogHook(w)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Envelope = &domain.RequestEnvelope{RequestID: "req-sanitize"}
	env.Metadata = map[string]any{
		"user_content": "secret prompt body",
		"api_key":      "sk-secret",
		"model":        "claude-sonnet-5",
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = w.Close()
	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("期望 1 条事件，实得 %d", len(events))
	}
	md := events[0].Metadata
	if md == nil {
		t.Fatal("审计事件 metadata 不应为 nil（model 等合法键应保留）")
	}
	if _, ok := md["user_content"]; ok {
		t.Error("审计事件把 user_content 原文带出去了 —— dump 面回归（去掉 sanitize 即复现）")
	}
	if _, ok := md["api_key"]; ok {
		t.Error("审计事件把原始 api_key 带出去了 —— dump 面回归")
	}
	if md["model"] != "claude-sonnet-5" {
		t.Errorf("合法键 model 应保留，实得 %v", md["model"])
	}
}
