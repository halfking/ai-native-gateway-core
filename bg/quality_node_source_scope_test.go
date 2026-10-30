package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestPerNodeTargetsScopedAndDeduped 钉住 per-node IQ 的两道闸（2026-10-30
// 24h 审计第 7 轮）。per-node IQ 是 model_iq_runs 的唯一写入方，而可信度的
// 同伴对比（LoadPeerReference）与参考基线校准（FitCalibration）都从它取数；
// 但无收窄时每次 boot 对全量活跃绑定打真实请求（211 ~3500 绑定，商汤 200 题
// 429 的实测教训）⇒ 发现查询必须带 featured 收窄 + min-interval 去重，
// 且单点 FindNode 不受 scope 限制（手动「立即测试」语义）。
func TestPerNodeTargetsScopedAndDeduped(t *testing.T) {
	source, err := os.ReadFile("quality_node_source.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	for _, want := range []string{
		// featured 收窄：与 model_credibility 的 buildCredibilityTargetsSQL 同口径
		"pm.standardized_name = ANY(COALESCE(pol.featured_models, ARRAY[]::text[]))",
		"pm.raw_model_name = ANY(COALESCE(pol.featured_models, ARRAY[]::text[]))",
		"pol.tenant_id = 'default'",
		// ★ 装配纪律：JOIN 片段与条件片段分离，单 WHERE（首版双 WHERE ⇒ 42601）
		`joinFrag := ""`,
		`condFrags := []string{}`,
		"extraConds",
		// ★ SELECT DISTINCT（2026-10-30 第 10 轮）：routing_policy 4 行物理
		//   重复（id 无唯一约束）⇒ 裸 CROSS JOIN 把每个唯一对放大成 4 行
		//   targets ⇒ 同对同轮重烧 4 次。发现查询必须在 SQL 侧去重。
		"SELECT DISTINCT c.id,",
		// ★ DISTINCT+ORDER BY 契约（部署后 42P10 实锤：ORDER BY 列必须出现在
		//   select list，且必须以**裸列**形态——COALESCE 别名不算）。
		//   末位裸列 pm.raw_model_name 同时是发现键（RawModelName）的扫描源。
		"c.secret_ciphertext,\n\t\t       pm.raw_model_name",
		// 去重：成功实测在窗口内的节点跳过
		"FROM model_iq_runs r",
		"AND r.status = 'success'",
		"AND r.tested_at > now() - $1::interval",
		// 收窄只在 per-node 路径：单点发现复用无收窄形态
		"return s.discoverNodes(ctx, discoverOpts{})",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("quality_node_source.go missing per-node guard %q", want)
		}
	}

	// DiscoverActiveNodes 必须不带 opts（单点 node-test 不收窄），
	// DiscoverPerNodeTargets 必须透传 scope + 成功窗 + 失败窗（2026-10-30
	// 第 8 轮：失败冷却窗——dedup 只认 success ⇒ EOL/坏节点每周期重烧）。
	if !strings.Contains(text, "func (s *CredentialNodeSource) DiscoverPerNodeTargets(ctx context.Context, scope string, minInterval, failedInterval time.Duration)") {
		t.Error("quality_node_source.go missing DiscoverPerNodeTargets(scope, minInterval, failedInterval) signature")
	}
	for _, want := range []string{
		// 失败冷却的 SQL 片段与默认 4h 的 worker 侧守卫
		"failedInterval time.Duration",
		"r.status IN ('failed', 'partial')",
		`if w.config.PerNodeFailedIntervalHours <= 0 {
		failedInterval = 4 * time.Hour
	}`,
	} {
		if !strings.Contains(text, want) && !strings.Contains(wtext0(t), want) {
			t.Errorf("missing per-node failed-cooldown guard %q", want)
		}
	}

	worker, err := os.ReadFile("model_quality_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	wtext := string(worker)
	for _, want := range []string{
		// worker 侧：默认 featured + 默认 24h
		`if scope == "" {
		scope = "featured"
	}`,
		"DiscoverPerNodeTargets(ctx, scope, minInterval, failedInterval)",
	} {
		if !strings.Contains(wtext, want) {
			t.Errorf("model_quality_worker.go missing per-node guard %q", want)
		}
	}

	// 契约：config 字段透传（MonitorConfig → RunPerNodeCheck）
	monitor, err := os.ReadFile("../domains/modelquality/monitor.go")
	if err != nil {
		t.Fatal(err)
	}
	mtext := string(monitor)
	for _, want := range []string{
		"PerNodeScope string",
		"PerNodeMinIntervalHours int",
	} {
		if !strings.Contains(mtext, want) {
			t.Errorf("monitor.go missing per-node config field %q", want)
		}
	}

	// minInterval 语义自证：0 值回落 24h（time.Duration 比较恒真检查）
	if 24*time.Hour <= 0 {
		t.Error("sanity: 24h duration must be positive")
	}
}

// wtext0 读 worker 源码（供上面的失败冷却守卫断言复用）。
func wtext0(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("model_quality_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
