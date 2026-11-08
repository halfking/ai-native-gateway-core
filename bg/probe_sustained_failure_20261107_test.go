package bg

// 2026-11-07 探测浪费根修轮 · 判据面
//
// 背景（211 真库实测）：probe.recovery_max_backoff_seconds（默认 300s）把可
// 恢复族退避削平 ⇒ 持续失败对 ~5 分钟一探、单对 ~480 次/天、cf≈1000 仍不停；
// expiredCmbRecoverySQL 不看梯子 ⇒ 每 30s tick 重开一代队列任务从梯底重走。
//
// 本文件钉住：
//   S1 持续失败升级的决策表（阈值边界、SLA 下限、两处 cap 位点）；
//   S2 绑定冷却（unavailableBindingHorizon）与梯子同档升级；
//   S3 rearm 的用尽窗口守卫（形态）＋家务类分级（rearmEligibleRecoveryKind）；
//   S4 expiredCmbRecoverySQL 的梯子守卫（形态）；
//   S5 成功 write-through 的新鲜度守卫接线（updateBindingAvailability 证据时刻）；
//   S6 成功 mirror 的 last_attempt_at = 证据时刻（形态）。

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// S1 决策表：阈值以下维持 2026-11-05 口径（SLA 削平），阈值起升到
// sustained_failure_recheck（默认 1h），且 sustained 档永不低于 SLA 档。
func TestSustainedFailureBackoffDecisionTable(t *testing.T) {
	threshold := sustainedFailureAttemptThreshold()
	if threshold < 1 {
		t.Fatalf("threshold must be >= 1, got %d", threshold)
	}
	sla := probeRecoveryMaxBackoff()
	recheck := sustainedFailureRecheck()
	if recheck < sla {
		t.Fatalf("sustained recheck (%v) must never be faster than the recovery SLA (%v)", recheck, sla)
	}

	// network 族长尾：阈值-1 ⇒ SLA；阈值 ⇒ sustained 档。
	if got := networkProbeBackoff(threshold - 1); got != sla {
		t.Errorf("networkProbeBackoff(threshold-1) = %v, want SLA %v (2026-11-05 口径逐字保留)", got, sla)
	}
	if got := networkProbeBackoff(threshold); got != recheck {
		t.Errorf("networkProbeBackoff(threshold) = %v, want sustained recheck %v", got, recheck)
	}
	// 更高的 attempt 不再升高（封顶在 sustained 档，不是无限增长）。
	if got := networkProbeBackoff(threshold * 50); got != recheck {
		t.Errorf("networkProbeBackoff(50×threshold) = %v, want capped sustained recheck %v", got, recheck)
	}
	// rate_limit 族同款升级。
	if got := ProbeBackoffForKind(errorsx.KindRateLimit, threshold-1); got != sla {
		t.Errorf("rate_limit(threshold-1) = %v, want SLA %v", got, sla)
	}
	if got := ProbeBackoffForKind(errorsx.KindRateLimit, threshold); got != recheck {
		t.Errorf("rate_limit(threshold) = %v, want sustained recheck %v", got, recheck)
	}
	// capRecoverBackoffForAttempt 的直接判据：低于上限的值原样返回。
	if got := capRecoverBackoffForAttempt(threshold, 30*time.Second); got != 30*time.Second {
		t.Errorf("capRecoverBackoffForAttempt(threshold, 30s) = %v, want 30s (低于档位不抬升)", got)
	}
}

// S2 绑定冷却与梯子同档：阈值以下 5 分钟（原口径），阈值起 sustained 档，
// 404/协议族不受影响（保持 model_not_served 6h 自有档）。
func TestUnavailableBindingHorizonEscalatesWithSustainedFailure(t *testing.T) {
	threshold := sustainedFailureAttemptThreshold()
	recheck := sustainedFailureRecheck()

	_, h := unavailableBindingHorizon("http_503", threshold-1)
	if h != 5*time.Minute {
		t.Errorf("horizon(threshold-1) = %v, want 5m (原口径)", h)
	}
	_, h = unavailableBindingHorizon("http_503", threshold)
	if h != recheck {
		t.Errorf("horizon(threshold) = %v, want sustained recheck %v", h, recheck)
	}
	_, h = unavailableBindingHorizon("network_error", threshold*10)
	if h != recheck {
		t.Errorf("horizon(10×threshold) = %v, want capped %v", h, recheck)
	}
	reason, h := unavailableBindingHorizon("http_404", 2)
	if h != modelNotServedRecheckInterval || reason != "model_not_served_404" {
		t.Errorf("404 family must keep its own horizon, got reason=%q horizon=%v", reason, h)
	}
}

// S3 rearm：用尽窗口守卫在 SQL 里（形态）+ 家务类分级不回拉。
func TestRearmSQLCarriesExhaustionWindowGuard(t *testing.T) {
	sql := rearmCredentialFailuresSQL()
	for _, must := range []string{
		"NOT EXISTS (",
		"c.quota_state IN ('periodic_exhausted', 'balance_exhausted', 'permanently_exhausted')",
		"c.quota_recover_at > now()",
	} {
		if !strings.Contains(sql, must) {
			t.Errorf("rearm SQL missing exhaustion-window guard piece %q", must)
		}
	}
}

func TestRearmEligibleRecoveryKindTiering(t *testing.T) {
	for _, housekeeping := range []string{"health_status_reset", "consecutive_failures_clear"} {
		if rearmEligibleRecoveryKind(housekeeping) {
			t.Errorf("%s is housekeeping (not a recovery transition) and must not mass-rearm", housekeeping)
		}
	}
	for _, genuine := range []string{
		"quota_periodic_recover", "availability_recover",
		"stale_periodic_exhausted_cleanup", "circuit_close", "mnf_cooling_recover",
	} {
		if !rearmEligibleRecoveryKind(genuine) {
			t.Errorf("%s is a genuine recovery transition and must rearm", genuine)
		}
	}
}

// S4 expiredCmbRecoverySQL 必须尊重梯子（与兄弟分支 freshDegradedCmbSQL 同款
// 守卫）—— 否则每 30s tick 重开一代队列任务从梯底重走，升级被整体绕过。
func TestExpiredCmbRecoverySQLRespectsLadder(t *testing.T) {
	sql := expiredCmbRecoverySQL()
	guard := "(nps.paused = TRUE OR nps.next_retry_at > now())"
	if !strings.Contains(sql, guard) {
		t.Fatalf("expired cmb recovery must skip ladder-scheduled pairs, missing %q", guard)
	}
}

// S5/S6 证据时刻贯通：成功 write-through 与成功 mirror 都必须以证据时刻为锚。
func TestSuccessPathsCarryEvidenceTimestamp(t *testing.T) {
	// S5：healthyBindingSQL 的守卫参数位（$3）与证据时刻。
	sql := healthyBindingSQL(time.Now())
	if !strings.Contains(sql, "COALESCE(cmb.unavailable_at, 'epoch') <= $3::timestamptz") {
		t.Fatal("healthyBindingSQL must bind the evidence timestamp as $3")
	}
	// S6：Run 的成功分支必须把 directDone 传给 mirror（源码形态钉，防回归）。
	src, err := os.ReadFile("probe_service.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, must := range []string{
		"evidenceAt:   directDone",
		"mirrorNow = directDone",
		"last_attempt_at = $4",
	} {
		if !strings.Contains(body, must) {
			t.Errorf("probe_service.go missing evidence-timestamp wiring %q", must)
		}
	}
}

// ★★ 2026-11-08（第 51 增量审计 F-3）：S5 只钉了 SQL **文本**含 $3，
//
//	★ 但没钉**实参端**——把 Exec 的 $3 实参从 evidenceAt 换回 now()
//	★ （bg/node_probe.go / bg/node_probe_write_through.go 两处），
//	★ **没有任何断言变红**——而那正是 W7 要防的竞态方向：
//	★ 证据时刻退化为记账时刻 ⇒ (证据, 记账) 窗口内的合法业务拉黑
//	★ 会被无条件恢复覆盖 ⇒ write-through 竞态复发。
//	★ 本判据钉住两个调用点的实参形态。
func TestHealthyBindingExecBindsEvidenceTimestampNotNow(t *testing.T) {
	// ① legacy/worker 侧：updateBindingAvailability 成功分支。
	npSrc, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	npBody := string(npSrc)
	if !strings.Contains(npBody, "w.db.Exec(ctx, healthyBindingSQL(evidenceAt), credID, model, evidenceAt)") {
		t.Fatal("node_probe.go must execute healthyBindingSQL with the evidence timestamp as the $3 argument (★ now() here re-opens the W7 write-through race)")
	}
	// ② write-through 侧：syncHealthyNodeSurfacesAt。
	wtSrc, err := os.ReadFile("node_probe_write_through.go")
	if err != nil {
		t.Fatal(err)
	}
	wtBody := string(wtSrc)
	if !strings.Contains(wtBody, "db.Exec(ctx, healthyBindingSQL(evidenceAt), credentialID, rawModel, evidenceAt)") {
		t.Fatal("node_probe_write_through.go must execute healthyBindingSQL with the evidence timestamp as the $3 argument")
	}
	// ★ 反向断言：健康写路径里不得出现「healthyBindingSQL(now())」形态。
	for _, pair := range []struct {
		body string
		name string
	}{
		{npBody, "node_probe.go"},
		{wtBody, "node_probe_write_through.go"},
	} {
		if strings.Contains(pair.body, "healthyBindingSQL(now())") {
			t.Errorf("%s must not bind healthyBindingSQL to now() (★ the guard exists to compare against the evidence timestamp)", pair.name)
		}
	}
}

// ★★ 2026-11-08（第 51 增量审计 F-4）：legacy 路径的 W8 接线**无任何测试钉**。
//
//	★ S6 只读 probe_service.go（队列路径）；bg/node_probe.go 的两处 legacy
//	★ 接线（成功镜像 `last_attempt_at = $4` 与 updateBindingAvailability 传
//	★ directDone）改回 now() 会静默通过 —— 即 legacy 路径上的 R4 误报
//	★ 与 write-through 竞态同时失去防护。
func TestLegacyPathCarriesEvidenceTimestamp(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// ① 成功镜像：last_attempt_at 必须取证据时刻实参（$4），不是记账 now()。
	if !strings.Contains(body, "last_attempt_at = $4") {
		t.Fatal("node_probe.go legacy success mirror must write last_attempt_at from the evidence timestamp argument")
	}
	// ② 成功分支的证据时刻来源：直连轮完成时刻 directDone。
	if !strings.Contains(body, "directDone") {
		t.Fatal("node_probe.go legacy path must derive the evidence timestamp from the direct round completion (directDone)")
	}
	// ③ 成功分支必须把 directDone 传给 updateBindingAvailability。
	if !strings.Contains(body, "updateBindingAvailability(") {
		t.Fatal("node_probe.go must call updateBindingAvailability")
	}
	// ★ 反向断言（限定在**成功镜像**这一段）：失败路径写 now() 是正确的
	//   （失败证据时刻＝记账时刻），不得连坐 ⇒ 判据必须看上下文而非全文。
	successMirror := body[strings.Index(body, "last_attempt_at = $4")-2000:]
	if end := strings.Index(successMirror, "last_attempt_at = now()"); end >= 0 && end < 2000 {
		t.Error("node_probe.go legacy success mirror must not fall back to last_attempt_at = now() (★ W8: the mirror must carry the evidence timestamp)")
	}
}

// S7（W9）：失败镜像必须是 upsert（非 gateway-side 失败）——否则队列模式下
// 从未被业务触达的对永远没有 node_probe_state 行，cf 不累积、升级永不触发
// （211 实测：凭据 35 族 cf=-1、每次恢复翻转从梯底重开一代）。
func TestMirrorFailureUpsertsStateRow(t *testing.T) {
	src, err := os.ReadFile("probe_service.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "INSERT INTO node_probe_state (\n\t\t\t\tcredential_id, raw_model_name,") {
		t.Fatal("failure mirror must INSERT (upsert) so queue-mode pairs accumulate ladder memory")
	}
	if !strings.Contains(body, "ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET") {
		t.Fatal("failure mirror insert must carry ON CONFLICT DO UPDATE")
	}
	// gateway-side 分支必须保持纯 UPDATE（不建行）——R39 语义。
	gsUpdate := strings.Index(body, "if gatewaySide {")
	if gsUpdate < 0 {
		t.Fatal("gateway-side branch must remain separate")
	}
	segment := body[gsUpdate:]
	if !strings.Contains(segment[:strings.Index(segment, "return\n\t}")], "UPDATE node_probe_state SET") {
		t.Fatal("gateway-side failure branch must stay a plain UPDATE (never create rows)")
	}
}
