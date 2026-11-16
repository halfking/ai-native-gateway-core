package provider

// candidate_diag_dedup_cause_test.go —— 归因变化必须立刻放行（R-76-3 批判审计补）
//
// # 为什么补这条
//
// R-76-3 落地后，审计发现 provider 侧与 bg 侧**不对称**：
//
//   · bg 侧：闸的键 = `cause|outboundModel|providerID` ⇒ **根因在键里** ⇒
//     根因一变就是新键 ⇒ 立刻放行。有专门判据钉住：
//     `TestLogProbeRoundRootCauseRootCauseChangeNotGated`。
//   · provider 侧：闸的键 = `event|model|tenant` ⇒ **归因结论不在键里** ⇒
//     同一模型同一租户在冷却窗内归因从 A 变成 B 时，B 被**静默吞掉**，
//     最多压 5 分钟。
//
// 而 provider 侧自己的文件头写着：
//
//	「窗口外放行一条以确认归因未漂移（模型登记/状态闸被改时**会立即以新内容出现**）」
//
// ⇒ 这句话在当前实现下是**假的**：状态闸被改 ⇒ 归因变了 ⇒ 但键没变 ⇒
// 不会立即出现，要等冷却窗到期。
//
// 实际危害不是「日志少了」，而是**处置方向被误导**：旧归因是「模型未登记」，
// 新归因是「凭据配额耗尽 ⇒ 充值」，后者是可立即执行的运维动作；被压掉意味着
// 运营在 5 分钟里仍按旧结论排查（甚至去登记一个已经登记了的模型）。
//
// ★ 为什么键里放的是**有界的归因类**而不是 `likely_cause` 原文：
// `EmptyCandidateBreakdown.Summarize()` 把 offer **计数**也拼进句子
// （「模型已登记（3 offer），但 2 条…」）。若把原文塞进键，计数抖动
// （3↔4）会让键交替出现 ⇒ 每条都是「该键首次」⇒ **洪水反而压不住**。
// 所以键里放 `cause_class`：一个与 Summarize 分支一一对应的**有限枚举**。

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestLogCandidateDiagnosticDbEmptyCauseChangeNotGated —— 归因变化在冷却窗内
// 必须**立刻**放行（bg 侧同型判据的 provider 对照）。
//
// 这是本文件的**负控**：在修复前它必须红（R-76-3 原实现把 cause 排除在键外）。
func TestLogCandidateDiagnosticDbEmptyCauseChangeNotGated(t *testing.T) {
	orig := candidateDiagLogGateInstance
	candidateDiagLogGateInstance = newCandidateDiagLogGate(1 * time.Hour)
	defer func() { candidateDiagLogGateInstance = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	// 同一 (model, tenant)，归因从「模型未登记」变成「凭据配额耗尽」。
	logCandidateDiagnostic("db_empty",
		"model", "gemini-2.5-flash", "tenant_id", "default",
		"cause_class", CauseClassNotRegistered)
	logCandidateDiagnostic("db_empty",
		"model", "gemini-2.5-flash", "tenant_id", "default",
		"cause_class", CauseClassCredentialQuota)

	out := buf.String()
	if n := strings.Count(out, "candidate_diag] db_empty"); n != 2 {
		t.Fatalf("归因变化必须在冷却窗内立刻放行（期望 2 条日志，实得 %d 条）:\n%s\n"+
			"若只有 1 条，说明闸的键没含归因 ⇒ 新结论被静默吞掉最多 5 分钟，"+
			"运营会继续按旧结论排查。", n, out)
	}
	if !strings.Contains(out, CauseClassCredentialQuota) {
		t.Fatalf("放行的那条必须带新的归因类 %q:\n%s", CauseClassCredentialQuota, out)
	}
}

// TestLogCandidateDiagnosticDbEmptySameCauseStillSuppressed —— 反向对照：
// 归因**没变**时仍然要压（否则上面那条判据会变成恒真）。
func TestLogCandidateDiagnosticDbEmptySameCauseStillSuppressed(t *testing.T) {
	orig := candidateDiagLogGateInstance
	candidateDiagLogGateInstance = newCandidateDiagLogGate(1 * time.Hour)
	defer func() { candidateDiagLogGateInstance = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	for i := 0; i < 3; i++ {
		logCandidateDiagnostic("db_empty",
			"model", "gemini-2.5-flash", "tenant_id", "default",
			"cause_class", CauseClassNotRegistered)
	}
	if n := strings.Count(buf.String(), "candidate_diag] db_empty"); n != 1 {
		t.Fatalf("同归因重复必须压成 1 条，期望 1 实得 %d:\n%s", n, buf.String())
	}
}

// TestLogCandidateDiagnosticDbEmptyTenantStillIsolated —— 反向对照：
// 归因类进了键之后，**租户隔离不能被破坏**（不同租户仍各放行首条）。
func TestLogCandidateDiagnosticDbEmptyTenantStillIsolated(t *testing.T) {
	orig := candidateDiagLogGateInstance
	candidateDiagLogGateInstance = newCandidateDiagLogGate(1 * time.Hour)
	defer func() { candidateDiagLogGateInstance = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	logCandidateDiagnostic("db_empty", "model", "m", "tenant_id", "t1",
		"cause_class", CauseClassNotRegistered)
	logCandidateDiagnostic("db_empty", "model", "m", "tenant_id", "t2",
		"cause_class", CauseClassNotRegistered)

	if n := strings.Count(buf.String(), "candidate_diag] db_empty"); n != 2 {
		t.Fatalf("不同租户必须各放行首条，期望 2 实得 %d:\n%s", n, buf.String())
	}
}

// TestCauseClassAgreesWithSummarize —— 分类与摘要必须**说同一件事**。
//
// ★ 为什么钉：CauseClass() 是有限枚举，Summarize() 是人读句子。两者若分支顺序
// 不一致，运营会看到「摘要说配额、分类说未登记」这种自相矛盾的两套结论 ——
// 那比没有分类更坏（分类还进了日志冷却闸的键）。
// ⇒ 本判据逐个分支构造 breakdown，要求 CauseClass() 的取值与 Summarize()
//
//	句首的处置方向一致。
func TestCauseClassAgreesWithSummarize(t *testing.T) {
	cases := []struct {
		name  string
		b     EmptyCandidateBreakdown
		class string
		// wantSubstr 必须出现在 Summarize() 里，作为「同一结论」的可读锚点。
		wantSubstr string
	}{
		{"not_registered", EmptyCandidateBreakdown{OffersTotal: 0}, CauseClassNotRegistered, "模型未登记"},
		{"cred_missing", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialMissing: 1}, CauseClassCredentialMissing, "已删除/缺失的凭据"},
		{"cred_status", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialStatus: 1}, CauseClassCredentialStatus, "credentials.status"},
		{"cred_manual_disabled", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialManualDisabled: 1}, CauseClassCredentialManualDisabled, "manual_disabled"},
		{"cred_lifecycle", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialLifecycle: 1}, CauseClassCredentialLifecycle, "lifecycle_status"},
		{"cred_quota", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialQuota: 1}, CauseClassCredentialQuota, "配额"},
		{"provider_disabled", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByProviderDisabled: 1}, CauseClassProviderDisabled, "供应商被停用"},
		{"unavailable_reason", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByUnavailableReason: 1}, CauseClassUnavailableReason, "unavailable_reason"},
		{"provider_tenant", EmptyCandidateBreakdown{OffersTotal: 2, BlockedByProviderTenant: 1}, CauseClassProviderTenant, "其他租户"},
		{"gates_open", EmptyCandidateBreakdown{OffersTotal: 2}, CauseClassGatesOpen, "状态不是原因"},
	}
	for _, tc := range cases {
		got := tc.b.CauseClass()
		if got != tc.class {
			t.Errorf("%s: CauseClass()=%q, want %q", tc.name, got, tc.class)
		}
		if s := tc.b.Summarize(); !strings.Contains(s, tc.wantSubstr) {
			t.Errorf("%s: Summarize()=%q 不含锚点 %q（摘要与分类对不上）", tc.name, s, tc.wantSubstr)
		}
	}
}

// TestCauseClassValuesAreBounded —— 分类取值集合必须**有界**。
// 若有人往 CauseClass() 里塞 offer 计数或错误文本，键空间会炸开
// （计数抖动 ⇒ 键交替 ⇒ 洪水压不住）。
func TestCauseClassValuesAreBounded(t *testing.T) {
	allowed := map[string]bool{
		CauseClassNotRegistered: true, CauseClassCredentialMissing: true,
		CauseClassCredentialStatus: true, CauseClassCredentialManualDisabled: true,
		CauseClassCredentialLifecycle: true, CauseClassCredentialQuota: true,
		CauseClassProviderDisabled: true, CauseClassUnavailableReason: true,
		CauseClassProviderTenant: true, CauseClassGatesOpen: true,
		CauseClassDiagError: true,
	}
	// 计数抖动：同一模型 offer 数变了，分类不得跟着变。
	a := EmptyCandidateBreakdown{OffersTotal: 3, BlockedByCredentialQuota: 1}
	b := EmptyCandidateBreakdown{OffersTotal: 4, BlockedByCredentialQuota: 1}
	if a.CauseClass() != b.CauseClass() {
		t.Fatalf("offer 计数抖动不得改变归因类：%q vs %q", a.CauseClass(), b.CauseClass())
	}
	if !allowed[a.CauseClass()] {
		t.Fatalf("归因类 %q 不在有界集合内", a.CauseClass())
	}
	for k := range allowed {
		if strings.ContainsAny(k, "0123456789") {
			t.Errorf("归因类 %q 含数字 —— 疑似混入了计数，键空间会炸开", k)
		}
	}
}
