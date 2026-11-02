package credential

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ── 第十一轮 · F-C 的断路器侧闸门（2026-11-02）────────────────────────────
//
// 断路器是**按 kind 字符串分档**的：三张表（defaultPolicies /
// freeTierPolicies / transientFamily）。任何进入写回路径的 kind 都必须
// 在这三张表里有明确档位，否则：
//
//   - defaultPolicies 缺项 ⇒ RecordFailure 走 `policy = b.coolingPolicy`
//     那条兜底 ⇒ 冷却时长变成「上一次用的策略」⇒ **不确定**；
//   - freeTierPolicies 缺项 ⇒ 免费档拿不到缩短档（15s）⇒ 白扔 4 倍时间；
//   - transientFamily 缺项 ⇒ 与瞬时族交替出现时被判成「族变化」
//     ⇒ consecutive 被清零 ⇒ 熔断要多一次失败才跳。
//
// ★ 第九轮（F-B）把执行器回写的 kind 换成了 no_available_channel，
//   而这三张表**都没有登记**它 ⇒ 三处行为漂移。
//
// ★ 本门把三张表钉死：no_available_channel 必须**与瞬时族同档**
//   （它的凭据级语义是「中转暂时没有通道」）。

// TestBreakerNoAvailableChannelRegisteredInAllPolicyTables 钉门 T：
// no_available_channel 在三张按字符串分档的表里都有登记，且档位与
// 瞬时族（KindTransient）一致。
func TestBreakerNoAvailableChannelRegisteredInAllPolicyTables(t *testing.T) {
	// 表一：defaultPolicies（付费档）—— 必须有明确档位，不能落到粘滞兜底。
	def, ok := defaultPolicies[errorsx.KindNoAvailableChannel]
	if !ok {
		t.Fatal("defaultPolicies 里没有登记 no_available_channel ⇒ " +
			"RecordFailure 会落到 `policy = b.coolingPolicy`（粘滞的「上次用的策略」）⇒ 冷却时长不确定")
	}
	want := defaultPolicies[errorsx.KindTransient]
	if def.InitialCooling != want.InitialCooling || def.MaxCooling != want.MaxCooling {
		t.Fatalf("defaultPolicies[no_available_channel] = %+v, want 与 KindTransient 同档 %+v",
			def, want)
	}

	// 表二：freeTierPolicies（免费档）—— 缺项会白扔 4 倍冷却时间。
	free, ok := freeTierPolicies[errorsx.KindNoAvailableChannel]
	if !ok {
		t.Fatal("freeTierPolicies 里没有登记 no_available_channel ⇒ " +
			"免费档拿不到缩短档（15s），会按付费档的 60s 冷却 ⇒ 免费额度被白扔")
	}
	wantFree := freeTierPolicies[errorsx.KindTransient]
	if free.InitialCooling != wantFree.InitialCooling {
		t.Fatalf("freeTierPolicies[no_available_channel] = %+v, want 与 KindTransient 同档 %+v",
			free, wantFree)
	}

	// 表三：transientFamily —— 缺项 ⇒ 与瞬时族交替时 consecutive 被清零。
	if !transientFamily[errorsx.KindNoAvailableChannel] {
		t.Fatal("transientFamily 里没有登记 no_available_channel ⇒ " +
			"它与 transient/timeout/network 交替出现时会被判成「族变化」⇒ " +
			"consecutive 被清零 ⇒ 熔断要多一次失败才跳")
	}
}

// TestBreakerNoAvailableChannelFreeTierUsesShortenedProfile 是门 T 的
// **行为级**一半：免费档断路器记这一档时，冷却必须走缩短档（15s），
// 而不是 defaultPolicies 的 60s。
//
// ★ 连败要记 **2 次**才进冷却窗口（failureConfirmationThreshold(policy)：
// RecoveryAuto 的平坦档 ⇒ autoRecoveryFailureThreshold = 2）。
func TestBreakerNoAvailableChannelFreeTierUsesShortenedProfile(t *testing.T) {
	mgr := NewManager()
	brk := mgr.GetOrCreate(3, 9)
	if brk == nil {
		t.Fatal("GetOrCreate 返回 nil")
	}
	brk.MarkFreeTier()
	for i := 0; i < 2; i++ {
		brk.RecordFailure(errorsx.KindNoAvailableChannel)
	}

	lead := time.Until(brk.CoolingUntil())
	if lead < 10*time.Second || lead > 20*time.Second {
		t.Fatalf("免费档冷却窗口 ≈%v, want ≈15s（freeTierPolicies 的缩短档）", lead)
	}
	if got := brk.Stats()["last_error_kind"]; got != string(errorsx.KindNoAvailableChannel) {
		t.Fatalf("last_error_kind=%v, want %q", got, errorsx.KindNoAvailableChannel)
	}
}

// TestBreakerNoAvailableChannelPaidUsesDefaultProfile 是门 T 的另一半
// （付费档）：必须走 defaultPolicies 的 60s 档。
//
// ★ 断路器的兜底 `b.coolingPolicy` 构造时就是 defaultPolicies[KindTransient]
// = 60s ⇒ 付费档在登记前后**恰好相同**。这道门的价值在于：把这条
// 「碰巧相同」变成**显式登记**，并在 freeTierPolicies 缺项时（那才是
// 真正的漂移）由门 T 的另一半兜住。
func TestBreakerNoAvailableChannelPaidUsesDefaultProfile(t *testing.T) {
	mgr := NewManager()
	brk := mgr.GetOrCreate(4, 11)
	if brk == nil {
		t.Fatal("GetOrCreate 返回 nil")
	}
	for i := 0; i < 2; i++ {
		brk.RecordFailure(errorsx.KindNoAvailableChannel)
	}

	lead := time.Until(brk.CoolingUntil())
	if lead < 50*time.Second || lead > 70*time.Second {
		t.Fatalf("付费档冷却窗口 ≈%v, want ≈60s（defaultPolicies 的档位）", lead)
	}
}
