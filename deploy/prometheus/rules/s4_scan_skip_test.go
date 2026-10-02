package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestS4ScanSkipRulesCoverEveryRegisteredMetric 钉住 s4-scan-skip.yml 与
// bg/s4_scan_skip_metrics.go 的一一对应。
//
// 这道门存在的理由是 §9.37 的教训：新加的指标如果没有任何告警消费它，
// 它在事实层面就是装饰 —— 读不到它的字段和没人读的字段是同一件事。
// 方向上刻意**只**断言「注册了的指标必须有告警」，不反过来：
// 「没用到的指标就该删掉」证伪不了（一个指标可以只服务 Grafana 面板）。
func TestS4ScanSkipRulesCoverEveryRegisteredMetric(t *testing.T) {
	data, err := os.ReadFile("s4-scan-skip.yml")
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
	require.Equal(t, "s4_scan_skip", file.Groups[0].Name)
	require.Len(t, file.Groups[0].Rules, 3)

	byAlert := make(map[string]string)
	waits := make(map[string]string)
	exprs := ""
	for _, r := range file.Groups[0].Rules {
		require.NotEmpty(t, r.Alert, "a rule without an alert name never fires")
		require.NotEmpty(t, r.Expr, "alert %s has an empty expr", r.Alert)
		require.Equal(t, "bg", r.Label["component"], "alert %s must be attributed to component=bg", r.Alert)
		require.NotEmpty(t, r.Label["severity"], "alert %s has no severity", r.Alert)
		byAlert[r.Alert] = r.Expr
		waits[r.Alert] = r.For
		exprs += r.Expr + "\n"
	}

	// bg/s4_scan_skip_metrics.go 注册的三个指标，每个都必须有告警在消费。
	// 少一条 = 那个指标没有任何消费者 = 装饰。
	registered := map[string]string{
		"llm_gateway_bg_s4_scan_skipped_last_run":        "BgS4ScanSkipped",
		"llm_gateway_bg_s4_scan_last_run_unix":           "BgS4ScanStalled",
		"llm_gateway_bg_s4_scan_unregistered_skip_total": "BgS4ScanUnregisteredSkipReason",
	}
	for metric, alert := range registered {
		require.Contains(t, byAlert, alert, "no alert named %s for metric %s", alert, metric)
		require.Contains(t, exprs, metric,
			"metric %s is registered in bg/s4_scan_skip_metrics.go but no rule references it — "+
				"a metric with no consumer is decoration (§9.37)", metric)
	}
	require.Len(t, byAlert, len(registered), "a rule referencing an unregistered metric was added")

	// 语义断言：三条告警分别对应「跳过了 / 没跑 / 谎报」，不能互相顶替。
	require.Contains(t, byAlert["BgS4ScanSkipped"], "llm_gateway_bg_s4_scan_skipped_last_run == 1",
		"the skip alert must fire on the last-run gauge being 1")
	require.Contains(t, byAlert["BgS4ScanStalled"], "time() - llm_gateway_bg_s4_scan_last_run_unix",
		"the stall alert must be built on the last-run timestamp, not on the skip gauge — "+
			"otherwise a dead worker and a gated worker look identical")
	require.Contains(t, byAlert["BgS4ScanUnregisteredSkipReason"], "increase(llm_gateway_bg_s4_scan_unregistered_skip_total",
		"the unregistered-reason alert must be counter-based (an event), not gauge-based")

	// 未登记 reason 的告警不能有 for 抑制：它是「谎报正在发生」，不是趋势。
	// 按告警名定位而不是按 Rules[2] —— 规则增删会让位置索引指向另一条规则，
	// 而断言仍然「通过」（§9.37：判据不能钉在「第几个」上）。
	require.Empty(t, waits["BgS4ScanUnregisteredSkipReason"],
		"an unregistered reason means the gauge is actively lying right now; a for: delay "+
			"would let the lie stand unobserved for that long")
	// 反过来，跳过与停滞是**持续状态**，需要 for 抑制掉门控刚翻转时的瞬态。
	require.Equal(t, "10m", waits["BgS4ScanSkipped"],
		"a gated scan stays gated for hours; without a for: window the alert chatters on every gate flip")
	require.Equal(t, "15m", waits["BgS4ScanStalled"])

	// GW-00 低基数守卫。对整串 exprs 检查，而不是 range 逐行 —— 对字符串
	// 做 `for _, x := range` 得到的是 rune 不是行，断言会变成「对 int32 调
	// len()」的错误，一行都没真正检查过。
	require.NotContains(t, exprs, "model=", "must not filter by model")
	require.NotContains(t, exprs, "tenant=", "must not filter by tenant")
	require.NotContains(t, exprs, "request_id=", "must not filter by request_id")
	require.NotContains(t, exprs, "session_id=", "must not filter by session_id")
	text := string(data)
	require.False(t, strings.Contains(text, `model="`))
	require.False(t, strings.Contains(text, `tenant="`))
}
