package bg

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// retry 信号三态 + LATERAL 收窄条件移除的门（审计 §9.42）。
//
// # 这次改了什么
//
// 用户 2026-10-02 拍板「一次做完」：
//
//	① 去掉 LATERAL 里的 `s.canonical_id IS NOT NULL` 与
//	   `r2.canonical_id = s.canonical_id`——实测代价是 67.7% 的 selection
//	   测不到 retry 信号，收益只有 0.01% 的会话防跨模型污染；
//	② RetryRatio 显式三态，未测得落回中性 0.5 而不是满分 1.0。
//
// # 为什么需要这些门
//
// ① 是**删代码**。删掉一个「看起来是防御性收窄」的条件，下一个人很容易
// 觉得它必要而加回来——而没有任何测试能证明它不在了。
// ② 是**语义**。「未测得 = 满分」这个 bug 之所以能活这么久，正是因为
// `TestComputeRoutingReward_UnknownsAreNeutralNotZero` 把错误值写成了期望值
// （它叫「UnknownsAreNeutral」，retry 项却按 `0.10*1` 算）。
func TestSettleLATERALHasNoCanonicalIDNarrowing(t *testing.T) {
	// 只看 SQL 字面量：注释里讨论 canonical_id 的段落必须允许提到它。
	var sql strings.Builder
	for _, l := range sqlLiteralsOfSettleFile(t, "auto_route_settle_worker.go") {
		sql.WriteString(l)
		sql.WriteString("\n")
	}
	for _, banned := range []string{
		"s.canonical_id IS NOT NULL",
		"r2.canonical_id = s.canonical_id",
	} {
		if strings.Contains(sql.String(), banned) {
			t.Errorf("settleBatch 的 SQL 里又出现了 %q。\n"+
				"  它曾让 67.7%% 的 selection 因 canonical_id 为 NULL 而 model_reqs=0，\n"+
				"  retry 信号根本测不到（而旧语义让它白拿 0.10 权重满分）；\n"+
				"  它防住的跨模型污染实测只占 0.01%%（10 天 11,634 个会话里 1 个）。\n"+
				"  若确有理由加回来，必须附新的实测依据，并检查它是否会把 retry\n"+
				"  信号重新推回 unmeasured 态。", banned)
		}
	}
}

// TestSettleRetryStateIsRecorded 钉住三态的判定与指标接线。
//
// 判据是「三态判定代码在 + 指标在被 Inc」。两者缺一，三态就只是注释。
func TestSettleRetryStateIsRecorded(t *testing.T) {
	raw := readFileForSettleGate(t, "auto_route_settle_worker.go")

	states := []string{retryStateMeasured, retryStateUnmeasured, retryStateUnavailable}
	seen := map[string]bool{}
	for _, s := range states {
		if s == "" {
			t.Fatal("有一个 retry state 常量是空串")
		}
		if s != strings.TrimSpace(s) {
			t.Errorf("retry state %q 前后有空白——它同时是指标标签值", s)
		}
		if seen[s] {
			t.Errorf("retry state %q 重复出现：标签闭集退化成 %d 个值", s, len(seen))
		}
		seen[s] = true
	}

	// unavailable 优先于 unmeasured：「LATERAL 没能产出」与「会话确实没有
	// 可数的行」是两种不同的失败，合并成一个就丢掉了停写时唯一能看见的信号。
	if !strings.Contains(raw, "case p.modelReqsInSes == nil || p.retryCount == nil:") {
		t.Error("三态判定里没有 unavailable 分支（指针为 nil = LATERAL 无产出）")
	}
	if !strings.Contains(raw, "case *p.modelReqsInSes > 0:") {
		t.Error("三态判定里没有 measured 分支（model_reqs > 0）")
	}
	if !strings.Contains(raw, "in.RetryMeasured = true") {
		t.Error("measured 分支没有设置 in.RetryMeasured = true —— " +
			"设了 RetryRatio 却不标记 measured，重试项仍会落回中性")
	}
	if !strings.Contains(raw, "autoRouteSettleRetryState.WithLabelValues(retryState).Inc()") {
		t.Error("retry state 指标没有被接线。\n" +
			"  没有它，运维从 reward 分布上看不出多少样本的 retry 项是中性 0.5、\n" +
			"  多少是实测值——只能看到分布整体偏移。")
	}
}

// TestSettleRetryStateMetricStaysInClosedEnum 让指标名与文档引用保持一致，
// 并保证 state 维度是三条闭集序列（基数恒定，符合 GW-00）。
func TestSettleRetryStateMetricStaysInClosedEnum(t *testing.T) {
	states := []string{retryStateMeasured, retryStateUnmeasured, retryStateUnavailable}
	// 触碰每个取值，使序列在首轮运行前就存在于 /metrics。
	for _, s := range states {
		_ = testutil.ToFloat64(autoRouteSettleRetryState.WithLabelValues(s))
	}

	// 名字与序列数从**默认注册表**取：*CounterVec 本身不实现 Collector
	// （Collect 在内部的 counter 上，Vec 只暴露 WithLabelValues/Reset），
	// 所以只能走 Gather —— 与 metrics/label_cardinality_guard_test.go 同源。
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found *dto.MetricFamily
	for _, mf := range families {
		if mf.GetName() == "llmgw_autoroute_settle_retry_state_total" {
			found = mf
			break
		}
	}
	if found == nil {
		t.Fatal("/metrics 里没有 llmgw_autoroute_settle_retry_state_total —— " +
			"指标名与注释/文档里引用的不一致，或它没有被注册")
	}
	if n := len(found.GetMetric()); n != len(states) {
		t.Errorf("state 维度产出 %d 条序列，期望 %d（三个闭集取值各一条）", n, len(states))
	}
	// 闭集：标签值必须正好是那三个，不多不少。
	gotLabels := map[string]bool{}
	for _, m := range found.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "state" {
				gotLabels[lp.GetValue()] = true
			}
		}
	}
	for _, s := range states {
		if !gotLabels[s] {
			t.Errorf("指标里缺少 state=%q 序列", s)
		}
	}
	if len(gotLabels) != len(states) {
		t.Errorf("state 标签值集合 = %v，期望恰好 %v", gotLabels, states)
	}
}
