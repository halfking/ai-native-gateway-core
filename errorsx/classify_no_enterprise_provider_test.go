package errorsx

import "testing"

// 2026-10-13 Vapeur claude-haiku-5-5 流式事故判据（errorsx 层）。
//
// 事故链：流式请求 → Vapeur chat 腿内部转发 /v1/messages → 400
// {"error":"ClientRequestError","message":"No enterprise provider is
// available for this request path: POST /v1/messages. Required model:
// claude-haiku-5-5; model series: claude."}——此前不匹配任何正则 →
// KindTransient → 同 body 三连 400 + 节点遥测污染。语义是「该路径无后端
// 可服务」（能力/配额边界，对节点永久），归 KindNoAvailableChannel：
// 换兄弟凭据/换模型，不盲重试、不冷却。
const bodyVapeurNoEnterpriseProvider = `{"error":"ClientRequestError","message":"No enterprise provider is available for this request path: POST /v1/messages. Required model: claude-haiku-5-5; model series: claude.","requestId":"UyXzfBOfnqG"}`

func TestClassify_NoEnterpriseProviderIsNoAvailableChannel(t *testing.T) {
	for _, status := range []int{400, 403, 422, 502} {
		if got := ClassifyErrorWithBody(status, []byte(bodyVapeurNoEnterpriseProvider)); got != KindNoAvailableChannel {
			t.Fatalf("status %d: got %s, want no_available_channel", status, got)
		}
	}
	// 原有 channel 形态不回归。
	legacy := `no available channel for model gpt-5.6-luna under group Codex`
	if got := ClassifyErrorWithBody(503, []byte(legacy)); got != KindNoAvailableChannel {
		t.Fatalf("legacy channel form: got %s, want no_available_channel", got)
	}
	// 前提在场 + 负例：无关 provider 文案不得误吞。
	if !noEnterpriseProviderRe.MatchString("No enterprise provider is available for this request path") {
		t.Fatal("regex must match the incident wording")
	}
	for _, neg := range []string{"provider is great", "no providers were harmed"} {
		if noEnterpriseProviderRe.MatchString(neg) {
			t.Fatalf("regex must not match: %s", neg)
		}
	}
}
