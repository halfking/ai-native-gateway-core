package outputcompliance

// 强制输出守卫的**待检缓冲越界**回归判据（2026-10-29 输出合规误报轮）。
//
// ## 这条用例钉的是什么
//
// 211 生产实测（24h）：强制输出守卫把「响应太长、缓冲装不下」当成
// 「策略拦截」上屏 —— 越界路径返回的 ShouldBlock **不带任何规则名/原因/
// 摘要，也不打任何日志** ⇒
//
//	① 客户端整条响应作废（已流出的 3806~10529 个 chunk 全丢，最终只拿到
//	   801 字节的 keepalive + 空错误帧）；
//	② 审核单是**空白**的（21 条记录的 rule_id / reason 全空）⇒ 审核员既
//	   说不出是哪条规则，也批不了（放行键是 (租户, 规则)，规则为空 ⇒ 批准
//	   等于什么都没做 ⇒ 同一个误报无限复发）。
//
// ⇒ 现在的契约：
//
//	默认（release）：**不杀请求** —— 放行已逐帧检查过的帧、后续帧逐帧放行，
//	并**留痕**（policy_rule=builtin.guard.capacity_exceeded + policy_degraded）。
//	block 模式（LLM_GATEWAY_OUTPUT_GUARD_CAP_MODE=block）：fail-closed 保留，
//	但**必须带规则名**（审核单不再是空白的）。

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

// neverBlockingChecker 从不命中任何规则（只回显文本）。
type neverBlockingChecker struct{}

func (neverBlockingChecker) Check(_ context.Context, _, text string) (*outputcompliance.ComplianceResult, error) {
	return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
}

// chatDeltaFrame 生成一条 chat 形态的 SSE 增量帧（content 长度 contentLen）。
func chatDeltaFrame(contentLen int) []byte {
	content := strings.Repeat("a", contentLen)
	payload := fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, content)
	return []byte("data: " + payload + "\n\n")
}

// TestGuardCapacityDegradesInsteadOfSilentlyBlocking 是主判据：
// 越界 ⇒ 不阻断 + 留痕（规则名 + 降级标记 + 摘录）。
//
// ★ 这条用例的**反向**是「静默阻断」——那正是 211 生产上的形态。
func TestGuardCapacityDegradesInsteadOfSilentlyBlocking(t *testing.T) {
	t.Setenv(OutputGuardCapModeEnv, "release")
	it := NewMandatoryOutputComplianceInterceptor(neverBlockingChecker{}, "repro.guard.overflow")
	meta := &response.StreamMeta{
		TenantID: "t", SessionID: "s", RequestID: "r1",
		State: response.NewStreamState(),
	}
	ctx := context.Background()

	degraded := false
	held := 0
	released := 0
	for i := 0; i < 400; i++ {
		frame := chatDeltaFrame(64 * 1024)
		res, err := it.InterceptStreamChunk(ctx, frame, meta)
		if err != nil {
			t.Fatalf("InterceptStreamChunk: %v", err)
		}
		if res == nil {
			continue
		}
		if res.ShouldBlock {
			t.Fatalf("第 %d 帧仍被阻断：越界**不是**规则命中，不该杀请求（%+v）", i, res)
		}
		if res.Metadata != nil && res.Metadata["policy_degraded"] == true {
			degraded = true
			released = len(res.ModifiedChunk)
			if res.Metadata["policy_rule"] != GuardCapacityRuleID {
				t.Errorf("降级必须点名合成规则名，实际 %v", res.Metadata["policy_rule"])
			}
			if _, ok := res.Metadata["policy_reason"]; !ok {
				t.Error("降级必须给出可读原因（审核员要看得出「不是规则命中」）")
			}
			if _, ok := res.Metadata[PolicyEvidenceMetadataKey]; !ok {
				t.Error("降级必须带摘录（原始会话内容）")
			}
			break
		}
		held += len(frame)
	}
	if !degraded {
		t.Fatalf("累计 %d 字节后仍未见降级留痕（越界必须可见、可审核）", held)
	}
	if released == 0 {
		t.Error("降级放行必须把已扣下的帧还给客户端（否则响应内容凭空消失）")
	}
	t.Logf("越界降级：累计 %d 字节后放行 %d 字节", held, released)
}

// TestGuardCapacityBlockModeCarriesRule 钉住严格模式：阻断保留，但**必须**
// 带规则名与原因（审核单不能是空白的）。
func TestGuardCapacityBlockModeCarriesRule(t *testing.T) {
	t.Setenv(OutputGuardCapModeEnv, "block")
	it := NewMandatoryOutputComplianceInterceptor(neverBlockingChecker{}, "repro.guard.overflow.block")
	meta := &response.StreamMeta{
		TenantID: "t", SessionID: "s", RequestID: "r2",
		State: response.NewStreamState(),
	}
	ctx := context.Background()

	blocked := false
	for i := 0; i < 400; i++ {
		frame := chatDeltaFrame(64 * 1024)
		res, err := it.InterceptStreamChunk(ctx, frame, meta)
		if err != nil {
			t.Fatalf("InterceptStreamChunk: %v", err)
		}
		if res == nil || !res.ShouldBlock {
			continue
		}
		blocked = true
		if res.Metadata == nil || res.Metadata["policy_rule"] != GuardCapacityRuleID {
			t.Fatalf("严格模式的阻断必须带规则名（审核单不能空白）: %+v", res.Metadata)
		}
		if len(res.ModifiedChunk) != 0 {
			t.Fatalf("严格模式不得放行任何字节: %d", len(res.ModifiedChunk))
		}
		break
	}
	if !blocked {
		t.Fatal("block 模式下越界没有阻断")
	}
}

// ★★ TestGuardCapacityDefaultCapIsTheAuditedOneMegabyte 钉住出厂上限**是**
// 1 MiB（★ 2026-10-29 审计轮更正：★ 出厂值曾被抬到 8 MiB，★ 同日改回 ★★）。
//
// ★ 为什么必须是 1 MiB：★ 它是 2026-10-02 存储审计定下的 **wire cap 契约**
//
//	（docs/audit/2026-10-02-conversation-storage-stream-audit.md）★ ⇒ ★★ 抬到
//	8 MiB = 每并发流 8 MiB ⇒ ★★那是**存储审计轮**该做的记忆体决定 ★★
//
// ★★ 而误报的修复**不依赖**抬上限（★ 修复点是**降级放行**）★★ ⇒ ★★ 抬上限
//
//	对修复**没有必要**，★ 却把记忆体预算放大 8 倍 ⇒ ★★ **纯风险、无收益** ★★
//
// ★★ ⇒ 出厂值必须保持 1 MiB；★ 需要更大缓冲的部署**自己 knowingly 地抬**
//
//	（env，★ 启动即打生效值）★★。
//
// ★★ 这条测试就是「别再顺手抬上限」的守门人 ★★
func TestGuardCapacityDefaultCapIsTheAuditedOneMegabyte(t *testing.T) {
	if defaultMaxCompliancePendingBytes != 1<<20 {
		t.Fatalf("出厂上限必须是 1 MiB（2026-10-02 存储审计的 wire cap 契约），实际 %d 字节",
			defaultMaxCompliancePendingBytes)
	}
}

// ★★ TestGuardCapacityMisreportFixDoesNotDependOnTheCap 钉住误报修复的**真正
// 支点**：★ **越界不再杀请求** ★★ —— ★★ 与上限多大**无关** ★★
// （★ 这正是「抬上限对修复没有必要」的实证 ★★）。
func TestGuardCapacityMisreportFixDoesNotDependOnTheCap(t *testing.T) {
	// ★ 出厂上限下（1 MiB）★ 越界 ⇒ **不许阻断**（★ 默认 release 模式）★★
	it := NewMandatoryOutputComplianceInterceptor(neverBlockingChecker{}, "repro.guard.overflow.release")
	meta := &response.StreamMeta{
		TenantID: "t", SessionID: "s", RequestID: "r3",
		State: response.NewStreamState(),
	}
	ctx := context.Background()
	degraded := false
	for i := 0; i < 400; i++ {
		frame := chatDeltaFrame(64 * 1024)
		res, err := it.InterceptStreamChunk(ctx, frame, meta)
		if err != nil {
			t.Fatalf("InterceptStreamChunk: %v", err)
		}
		if res != nil && res.ShouldBlock {
			t.Fatalf("越界不许阻断请求（误报修复点）: %+v", res)
		}
		if res != nil && res.Metadata != nil {
			if d, _ := res.Metadata["policy_degraded"].(bool); d {
				degraded = true
				if res.Metadata["policy_rule"] != GuardCapacityRuleID {
					t.Fatalf("降级必须带规则名: %+v", res.Metadata)
				}
				break
			}
		}
	}
	if !degraded {
		t.Fatal("越界应当降级放行并留痕（默认 release 模式）")
	}
}

// TestGuardCapacityEnvOverride 钉住上限可配置（运维要能按自己的响应规模调）。
func TestGuardCapacityEnvOverride(t *testing.T) {
	t.Setenv(OutputGuardPendingMaxBytesEnv, "123456")
	if got := compliancePendingCap(); got != 123456 {
		t.Fatalf("上限 env 未生效: %d", got)
	}
	// 非法值退回出厂值（拼错的 env 不能把守卫变成无界的）。
	t.Setenv(OutputGuardPendingMaxBytesEnv, "not-a-number")
	if got := compliancePendingCap(); got != defaultMaxCompliancePendingBytes {
		t.Fatalf("非法上限应退回出厂值 %d，实际 %d", defaultMaxCompliancePendingBytes, got)
	}
}
