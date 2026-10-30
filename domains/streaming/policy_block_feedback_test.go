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
	"time"

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

	reviewID := registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "tenant-a", ClientType: "zcode", RequestID: "req-42",
		Rule: "builtin.egress_sink", Reason: "sink destination",
		Field: "$.command", Digest: "digest-of-cmd",
	})
	if !strings.HasPrefix(reviewID, "rv-") {
		t.Fatalf("审核单号形态不对: %q", reviewID)
	}
	pending := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "tenant-a", 10)
	if len(pending) != 1 {
		t.Fatalf("pending 记录数 = %d，期望 1", len(pending))
	}
	// 同键再拦（重试路径）不重复占单。
	if again := registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "tenant-a", ClientType: "zcode", RequestID: "req-42",
		Rule: "builtin.egress_sink", Reason: "sink destination",
		Field: "$.command", Digest: "digest-of-cmd",
	}); again != reviewID {
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
	// 2026-10-29 拦截持久化轮的新契约：批准 = **规则级 7 天放行**（owner 口径
	// 「同一客户端一周内的同样的规则将同样放行」）——同规则任意载荷都放行，
	// 且带 7 天过期与 review 来源。
	if !sanitizerules.EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-of-cmd") {
		t.Fatal("批准后同规则应放行")
	}
	if !sanitizerules.EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-other") {
		t.Fatal("规则级放行应覆盖同规则的其它载荷（7 天窗口）")
	}
	// 别的租户与别的规则不受影响（放行边界仍在）。
	if sanitizerules.EgressReleaseAllowed("tenant-b", "egress", "builtin.egress_sink", "digest-of-cmd") ||
		sanitizerules.EgressReleaseAllowed("tenant-a", "egress", "builtin.secret_field.egress", "digest-of-cmd") {
		t.Fatal("规则级放行不得越租户/越规则")
	}
	var granted *sanitizerules.EgressRelease
	for _, rel := range sanitizerules.EgressReleases() {
		if rel.TenantID == "tenant-a" && rel.Rule == "builtin.egress_sink" {
			granted = &rel
			break
		}
	}
	if granted == nil {
		t.Fatal("进程内放行表必须有本次批准的记录")
	}
	if granted.Source != "review" {
		t.Errorf("放行来源 = %q，期望 review", granted.Source)
	}
	if granted.ExpiresAt.IsZero() || time.Until(granted.ExpiresAt) < 6*24*time.Hour || time.Until(granted.ExpiresAt) > 8*24*time.Hour {
		t.Errorf("7 天放行的过期时间不对: %v", granted.ExpiresAt)
	}
	// 已决的单不能再次决定。
	if _, err := sanitizerules.DecideBlockReview(pending[0].ID, false, "admin-test"); err == nil {
		t.Error("已决记录应拒绝再决")
	}
}

// TestBlockReviewApprovalExpiresAfterSevenDays：放行过期后不再生效
// （「一周内」的另一半——过期自动收紧）。
func TestBlockReviewApprovalExpiresAfterSevenDays(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(func() { sanitizerules.ResetBlockReviews() })
	prevReleases := sanitizerules.EgressReleases()
	sanitizerules.SetEgressReleases(nil)
	t.Cleanup(func() { sanitizerules.SetEgressReleases(prevReleases) })

	registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "tenant-ttl", ClientType: "zcode", RequestID: "req-ttl",
		Rule: "builtin.egress_sink", Reason: "sink",
		Field: "$.command", Digest: "digest-ttl",
	})
	pending := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "tenant-ttl", 10)
	if _, err := sanitizerules.DecideBlockReview(pending[0].ID, true, "admin"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	// 把放行的过期时间拨到过去 ⇒ 同载荷不再放行（EgressReleaseAllowed 跳过过期行）。
	stale := sanitizerules.EgressReleases()
	for i := range stale {
		if stale[i].TenantID == "tenant-ttl" {
			stale[i].ExpiresAt = time.Now().Add(-time.Minute)
		}
	}
	sanitizerules.SetEgressReleases(stale)
	if sanitizerules.EgressReleaseAllowed("tenant-ttl", "egress", "builtin.egress_sink", "digest-ttl") {
		t.Fatal("过期放行不得继续生效")
	}
}

// TestBlockReviewRejectKeepsBlock：拒绝不放行、状态可查。
func TestBlockReviewRejectKeepsBlock(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(func() { sanitizerules.ResetBlockReviews() })
	prevReleases := sanitizerules.EgressReleases()
	sanitizerules.SetEgressReleases(nil)
	t.Cleanup(func() { sanitizerules.SetEgressReleases(prevReleases) })

	registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "tenant-b", ClientType: "claude-code", RequestID: "req-7",
		Rule: "builtin.secret_field.egress", Reason: "credential", Field: "$.password",
	})
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

// TestDegradedReviewRegistrationCarriesFlag（2026-10-30 审计轮，迁移 926）：
// finish() 的降级路径登记审核单时必须带 Degraded 标记 —— 降级单是遥测，
// 不是待决拦截：211 实测 24h 内 226/226 条全是降级单却全挂着「待审」状态
// 与批准按钮（批准写出的放行行没有任何消费方）。
func TestDegradedReviewRegistrationCarriesFlag(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(func() { sanitizerules.ResetBlockReviews() })

	registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "tenant-deg", ClientType: "zcode", RequestID: "req-deg-1",
		Rule: "builtin.guard.capacity_exceeded",
		Reason: "output policy guard pending capacity exceeded (1048576 bytes held, cap 1048576)",
		Degraded: true,
	})
	pending := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "tenant-deg", 10)
	if len(pending) != 1 {
		t.Fatalf("pending 记录数 = %d，期望 1", len(pending))
	}
	if !pending[0].Degraded {
		t.Fatal("降级路径登记的审核单必须带 Degraded=true")
	}
	// 对照组：硬阻断路径（Degraded 缺省 false）不带标记。
	registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "tenant-deg", ClientType: "zcode", RequestID: "req-block-1",
		Rule: "builtin.egress_sink", Reason: "sink destination",
	})
	blocked := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "tenant-deg", 10)
	for _, rv := range blocked {
		if rv.Rule == "builtin.egress_sink" && rv.Degraded {
			t.Fatal("硬阻断记录不得带降级标记")
		}
	}
	// 待决断数只数硬阻断那一条（降级单不计）。
	if got := sanitizerules.CountPendingBlockDecisions("tenant-deg"); got != 1 {
		t.Fatalf("待决断数必须排除降级单（=1），实际 %d", got)
	}
}
