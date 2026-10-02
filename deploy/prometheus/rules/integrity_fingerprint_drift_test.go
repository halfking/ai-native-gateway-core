package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestIntegrityFingerprintDriftRulesCoverEveryMetric 钉住
// integrity-fingerprint-drift.yml 与 bg/integrity_fingerprint_drift_metrics.go
// 的一一对应（审计 §9.50）。
//
// 方向与 s4_scan_skip_test.go 一致：只断言「注册了的指标必须有告警消费」。
// §9.38 的教训：**没有告警读的指标在事实层面就是装饰**，而 drift 那三个指标在
// 建告警之前是**纯装饰**——`skippedTicks` 全仓只有 Add(1)、没有任何地方读它。
func TestIntegrityFingerprintDriftRulesCoverEveryMetric(t *testing.T) {
	data, err := os.ReadFile("integrity-fingerprint-drift.yml")
	require.NoError(t, err)

	var file struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert string            `yaml:"alert"`
				Expr  string            `yaml:"expr"`
				For   string            `yaml:"for"`
				Label map[string]string `yaml:"labels"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.Len(t, file.Groups, 1)
	require.Equal(t, "integrity_fingerprint_drift", file.Groups[0].Name)

	byAlert := make(map[string]string)
	waits := make(map[string]string)
	exprs := ""
	for _, r := range file.Groups[0].Rules {
		require.NotEmpty(t, r.Alert, "a rule without an alert name never fires")
		require.NotEmpty(t, r.Expr, "alert %s has an empty expr", r.Alert)
		require.Equal(t, "bg", r.Label["component"], "alert %s must be attributed to component=bg", r.Alert)
		require.NotEmpty(t, r.Label["severity"], "alert %s has no severity", r.Alert)
		require.NotContains(t, byAlert, r.Alert, "duplicate alert name %s", r.Alert)
		byAlert[r.Alert] = r.Expr
		waits[r.Alert] = r.For
		exprs += r.Expr + "\n"
	}

	registered := map[string]string{
		"llm_gateway_bg_fingerprint_drift_last_scan_unix": "BgFingerprintDriftNeverScanned",
		"llm_gateway_bg_fingerprint_drift_skipped_total":  "BgFingerprintDriftSkippedNoScan",
		"llm_gateway_bg_fingerprint_drift_scanned_total":  "BgFingerprintDriftSkippedNoScan",
	}
	for metric, alert := range registered {
		require.Contains(t, byAlert, alert, "no alert named %s for metric %s", alert, metric)
		require.Contains(t, exprs, metric,
			"metric %s is registered in bg/ but no rule references it — a metric with no consumer is decoration (§9.37/§9.50)", metric)
	}
	require.Len(t, byAlert, 2, "a rule referencing an unregistered metric was added")

	// 「一直不扫」必须建在 last_scan 上，且是**持续状态**（要有 for: 抑制，
	// 否则 worker 刚起来的一瞬就会响）。
	require.Contains(t, byAlert["BgFingerprintDriftNeverScanned"],
		"time() - llm_gateway_bg_fingerprint_drift_last_scan_unix",
		"the never-scanned alert must be built on the last-scan timestamp, not on skipped_total — "+
			"otherwise it fires whenever the scan interval is merely long")
	require.Equal(t, "10m", waits["BgFingerprintDriftNeverScanned"],
		"never-scanned is a sustained state; without a for: window it chatters on every restart")

	// 「在跳但不扫」必须同时用到两个计数器：只看 skipped 会漏掉「worker 没启动」，
	// 只看 scanned 会漏掉「扫得极少但还在扫」。
	require.Contains(t, byAlert["BgFingerprintDriftSkippedNoScan"],
		"increase(llm_gateway_bg_fingerprint_drift_skipped_total",
		"the skipping alert must be counter-based (an event), not gauge-based")
	require.Contains(t, byAlert["BgFingerprintDriftSkippedNoScan"],
		"increase(llm_gateway_bg_fingerprint_drift_scanned_total",
		"the skipping alert must ALSO require scanned_total to be flat — "+
			"otherwise it fires during normal operation where skips and scans both happen")

	// GW-00 低基数守卫：三个指标刻意不带标签。
	for _, banned := range []string{"worker=", "tenant=", "model=", "request_id=", "credential="} {
		require.NotContains(t, exprs, banned, "must not filter by %s", strings.TrimSuffix(banned, "="))
	}
	text := string(data)
	require.False(t, strings.Contains(text, `credential="`))
}

// TestIntegrityFingerprintDriftAlertExplainsTheStopWriteCause 让告警文案自己
// 说出「为什么」。
//
// 这不是文档洁癖：告警是运维在半夜读到的第一手材料，而这里的失效原因
// **反直觉**——检测器不是坏了也不是挂了，是它自己判定「没流量可扫」，而那个判定
// 所依据的表正在被退役。告警若只说「没扫描」，运维会去查 worker 是否卡死，
// 而那正是本条排除掉的假设。
func TestIntegrityFingerprintDriftAlertExplainsTheStopWriteCause(t *testing.T) {
	data, err := os.ReadFile("integrity-fingerprint-drift.yml")
	require.NoError(t, err)
	text := string(data)

	require.Contains(t, text, "fingerprintScanSkip",
		"the alert must name the exact short-circuit that causes this — "+
			"without it the reader has to go find it")
	require.Contains(t, text, "自己把自己关掉",
		"the alert must say the detector switches itself off; "+
			"\"the detector stopped scanning\" reads as a crash and sends the reader down the wrong path")
	require.Contains(t, text, "逃生口",
		"there IS an escape hatch (in-process telemetry re-arms it) and the alert must say so, "+
			"or the reader will conclude the detector is unconditionally dead")
}

// TestIntegrityFingerprintDriftAlertTracksTheArmFix 钉住 §9.52 之后的事实。
//
// 这道门**被反转过一次**，过程本身就是记录：
// §9.50 我写它是为了禁止文案承诺自愈（「逃生口也是关着的」）。
// §9.51 保留了它。§9.52 把 arm 移出门控之后，那句话变成假话——门随即变红，
// 迫使文案改口。**这个摩擦是故意留的**：文案与代码状态不一致时，最省事的
// 做法是两边都不改，而不是让其中一边提醒另一边。
//
// 现在的方向：arm 已在门控之外，所以文案**不得**再说它关着；
// 同时「上游一旦发指纹检测器就能自己醒」这句话必须留着，因为它是运维判断
// 「这条告警还值不值得留着」的唯一依据。
func TestIntegrityFingerprintDriftAlertTracksTheArmFix(t *testing.T) {
	data, err := os.ReadFile("integrity-fingerprint-drift.yml")
	require.NoError(t, err)
	text := string(data)

	// 必须包含：arm 已在门控之外的事实。
	require.True(t, strings.Contains(text, "门控之外"),
		"the alert must record that the in-process re-arm is now outside the stop-write gate "+
			"(audit §9.52) — otherwise the reader keeps a limitation that no longer exists")
	require.True(t, strings.Contains(text, "自己醒过来"),
		"the alert must say the detector can wake itself once upstream traffic appears; "+
			"that is the whole reason this alert is still worth keeping after §9.52")

	// 必须不再包含：arm 已死的两处措辞（§9.50.4 / §9.51 各写过一次）。
	for _, banned := range []string{
		"逃生口也是关着的",
		"不要指望它自愈",
		"这条链不读 v1",
		"所以恢复与否取决于当前进程",
	} {
		require.True(t, !strings.Contains(text, banned),
			"%q describes the arm as dead, but audit §9.52 moved it out of the stop-write gate. "+
				"The claim is now false.", banned)
	}
}

// TestIntegrityFingerprintDriftAlertNamesTheRealCause 钉住 §9.51 的实测订正。
//
// §9.50 的第二版文案把「最可能的原因」写成 S4 停写。真库实测否定了它：
// v1 全表 216 万行、会话族 168 万行、integrity 事件 JSONB 7081 条，
// `system_fingerprint` **非空均为 0** ⇒ 上游从不发 `X-System-Fingerprint`
// ⇒ 探针在停写**之前**就已经是空的。告警若继续把停写说成主因，运维会去查
// 切换时刻与 S4 读写门，而那正是本条排除掉的假设。
//
// 判据同时要求文案带上**可复现的实测查询**：一个只给结论不给量具的诊断，
// 下一个读它的人仍然只能猜。
func TestIntegrityFingerprintDriftAlertNamesTheRealCause(t *testing.T) {
	data, err := os.ReadFile("integrity-fingerprint-drift.yml")
	require.NoError(t, err)
	text := string(data)

	require.True(t, strings.Contains(text, "X-System-Fingerprint"),
		"the alert must name where the column actually comes from — the upstream response header — "+
			"otherwise the reader is left with \"the probe reads v1\" and no way to tell "+
			"\"v1 is retired\" from \"upstream never sends it\"")
	require.True(t, strings.Contains(text, "停写之前探针就已经是空的"),
		"the alert must state the measured fact that the probe was ALREADY empty before stop-write; "+
			"§9.51 retracted the \"stop-write is the likely cause\" version")
	require.True(t, strings.Contains(text, "FROM request_logs_hot"),
		"the alert must ship the query that reproduces the diagnosis — a conclusion with no "+
			"measuring stick leaves the next reader guessing")

	for _, banned := range []string{"最可能的原因就是 S4 停写", "若确为停写导致"} {
		require.True(t, !strings.Contains(text, banned),
			"%q is the §9.51 retracted claim: the real cause is that upstream never returns the "+
				"header, which is independent of stop-write", banned)
	}
}
