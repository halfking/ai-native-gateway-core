package streaming

// 2026-10-28 拦截反馈明确化轮：拦截错误的**客户端可用性**契约。
//
// ## 本轮的动因（用户原话）
//
// 「ZCode 报错信息 / Response blocked by output policy / TraceID: hydrate-trace」
// ——没有明确的原因，检查输入也找不到违规。根因有三层：
//
//  1. message 是常量串，rule/reason 放旁键——只显示 message 的客户端
//     （ZCode 的报错卡）一个字都看不到。
//  2. /v1/responses 与 /v1/messages 的非流式 403 连旁键都没有。
//  3. 流式终帧没有 request_id——客户端只剩自己的内部标签可显示。
//
// ## 契约（本文件钉住）
//
//  C1 message 文本携带规则/原因/位置/请求 ID（按最弱客户端设计），
//     且以稳定前缀 "Response blocked by output policy" 开头（既有子串
//     匹配不破）。
//  C2 有审核单时 message 与旁键都带「等待审核」与单号；没有审核单时
//     message 退化到「联系管理员」话术，不出现 review 键。
//  C3 流式终帧带 request_id / review 旁键，且帧仍是合法 JSON。
//  C4 登记审核单 ⇒ 批准 ⇒ 放行判定为真（同键放行，跨进程内闭环）。
//  C5 /v1/responses 与 /v1/messages 的错误写者带旁键（extras 通道）。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func TestPolicyBlockClientMessageCarriesActionableDetail(t *testing.T) {
	msg := policyBlockClientMessage("builtin.egress_sink",
		"the tool call appears to send data to an external destination", "$.command",
		"req-abc-123", "rv-7")
	for _, want := range []string{
		"Response blocked by output policy",
		"rule=builtin.egress_sink",
		"send data to an external destination",
		"$.command",
		"request=req-abc-123",
		"rv-7",
		"等待审核",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message 缺少 %q:\n%s", want, msg)
		}
	}
	if !strings.HasPrefix(msg, "Response blocked by output policy") {
		t.Errorf("message 前缀漂移（客户端子串匹配会破）: %s", msg)
	}
}

func TestPolicyBlockClientMessageFallbackWithoutReview(t *testing.T) {
	msg := policyBlockClientMessage("", "", "", "", "")
	if !strings.HasPrefix(msg, "Response blocked by output policy") {
		t.Errorf("message 前缀漂移: %s", msg)
	}
	if !strings.Contains(msg, "联系") {
		t.Errorf("无审核单时应提示联系管理员: %s", msg)
	}
}

func TestStreamPolicyFrameCarriesRequestAndReview(t *testing.T) {
	frame := string(nativeStreamPolicyFailureFrame(r1037ProtoDefault,
		"builtin.egress_sink", "req-frame-1", "rv-9", "sink destination"))
	for _, want := range []string{
		`"request_id":"req-frame-1"`,
		`"review":"rv-9"`,
		`"rule":"builtin.egress_sink"`,
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("终帧缺少 %s:\n%s", want, frame)
		}
	}
	// 帧必须是合法 JSON：message 里有中文与标点，坏帧会被客户端整帧丢掉。
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(payload), &probe); err != nil {
			t.Fatalf("终帧 data 不是合法 JSON: %v\n%s", err, payload)
		}
	}
}

func TestNonStreamBlockedJSONCarriesReviewAndDigest(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOutputPolicyBlockedJSON("openai", rec, 403, "req-ns-1",
		"Response blocked by output policy", "policy_violation", "output_policy_blocked",
		"builtin.egress_sink", "sink destination", "$.command", "digest-abc", "rv-11")
	errObj := r1039DecodeErrorBody(t, rec.Body.String())
	if got, _ := errObj["digest"].(string); got != "digest-abc" {
		t.Errorf("error.digest = %q，期望 digest-abc", got)
	}
	if got, _ := errObj["review"].(string); got != "rv-11" {
		t.Errorf("error.review = %q，期望 rv-11", got)
	}
	msg, _ := errObj["message"].(string)
	for _, want := range []string{"rv-11", "等待审核", "rule=builtin.egress_sink", "req-ns-1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message 缺少 %q:\n%s", want, msg)
		}
	}
}

func TestResponsesErrorWriterCarriesExtras(t *testing.T) {
	rec := httptest.NewRecorder()
	writeResponsesError(rec, 403, "Response blocked by output policy", "output_policy_blocked", "blocked",
		policyBlockErrorExtras("builtin.egress_sink", "sink destination", "$.command", "digest-x", "req-r-1", "rv-3"))
	errObj := r1039DecodeErrorBody(t, rec.Body.String())
	for key, want := range map[string]string{
		"rule":       "builtin.egress_sink",
		"request_id": "req-r-1",
		"review":     "rv-3",
	} {
		if got, _ := errObj[key].(string); got != want {
			t.Errorf("error.%s = %q，期望 %q", key, got, want)
		}
	}
}

func TestAnthropicErrorWriterCarriesExtras(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAnthropicError(rec, 403, "output_policy_blocked", "Response blocked by output policy",
		policyBlockErrorExtras("builtin.egress_sink", "", "", "", "req-m-1", "rv-4"))
	var probe struct {
		Type  string         `json:"type"`
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v", err)
	}
	if probe.Type != "error" {
		t.Errorf("顶层 type = %q，期望 error", probe.Type)
	}
	if got, _ := probe.Error["rule"].(string); got != "builtin.egress_sink" {
		t.Errorf("error.rule = %q，期望 builtin.egress_sink", got)
	}
	if got, _ := probe.Error["review"].(string); got != "rv-4" {
		t.Errorf("error.review = %q，期望 rv-4", got)
	}
	// 空值不进 extras（形状里不出现空键）。
	if _, ok := probe.Error["reason"]; ok {
		t.Errorf("空 reason 不应出现: %+v", probe.Error)
	}
}

// TestBlockReviewApproveReleasesSamePayload：登记 → 批准 → 同 (租户, 规则,
// 载荷摘要) 的放行判定为真。这是「审核完成后重试即返回内容」的闭环判据。
func TestBlockReviewApproveReleasesSamePayload(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(func() { sanitizerules.ResetBlockReviews() })
	prevReleases := sanitizerules.EgressReleases()
	sanitizerules.SetEgressReleases(nil)
	t.Cleanup(func() { sanitizerules.SetEgressReleases(prevReleases) })

	reviewID := registerPolicyBlockReview("tenant-a", "req-42",
		"builtin.egress_sink", "sink destination", "$.command", "digest-of-cmd")
	if !strings.HasPrefix(reviewID, "rv-") {
		t.Fatalf("审核单号形态不对: %q", reviewID)
	}
	pending := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "tenant-a", 10)
	if len(pending) != 1 {
		t.Fatalf("pending 记录数 = %d，期望 1", len(pending))
	}
	// 同键再拦（重试路径）不重复占单。
	if again := registerPolicyBlockReview("tenant-a", "req-42",
		"builtin.egress_sink", "sink destination", "$.command", "digest-of-cmd"); again != reviewID {
		t.Errorf("同键 pending 应复用同一单: got %q want %q", again, reviewID)
	}
	if sanitizerules.EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-of-cmd") {
		t.Fatal("批准前不应放行")
	}
	decided, err := sanitizerules.DecideBlockReview(pending[0].ID, true, "admin-test")
	if err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if decided.Status != sanitizerules.BlockReviewApproved {
		t.Fatalf("状态 = %q，期望 approved", decided.Status)
	}
	if !sanitizerules.EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-of-cmd") {
		t.Fatal("批准后同载荷应放行")
	}
	// 放行只对同载荷生效：另一个摘要不放行。
	if sanitizerules.EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-other") {
		t.Fatal("带摘要的放行不应覆盖其它载荷")
	}
	// 已决的单不能再次决定。
	if _, err := sanitizerules.DecideBlockReview(pending[0].ID, false, "admin-test"); err == nil {
		t.Error("已决记录应拒绝再决")
	}
}

// TestBlockReviewRejectKeepsBlock：拒绝不放行、状态可查。
func TestBlockReviewRejectKeepsBlock(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(func() { sanitizerules.ResetBlockReviews() })
	prevReleases := sanitizerules.EgressReleases()
	sanitizerules.SetEgressReleases(nil)
	t.Cleanup(func() { sanitizerules.SetEgressReleases(prevReleases) })

	registerPolicyBlockReview("tenant-b", "req-7", "builtin.secret_field.egress", "credential", "$.password", "")
	pending := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "", 10)
	if len(pending) != 1 {
		t.Fatalf("pending 记录数 = %d，期望 1", len(pending))
	}
	if _, err := sanitizerules.DecideBlockReview(pending[0].ID, false, "admin-test"); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	if sanitizerules.EgressReleaseAllowed("tenant-b", "egress", "builtin.secret_field.egress", "") {
		t.Fatal("拒绝后不应放行")
	}
	rejected := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewRejected, "", 10)
	if len(rejected) != 1 || rejected[0].ID != pending[0].ID {
		t.Fatalf("rejected 列表应包含该单: %+v", rejected)
	}
}
