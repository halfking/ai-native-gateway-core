package rules_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// request-abandoned 告警（迁移 819，审计 §9.66 / §9.67）的门。
//
// 这组门存在的理由比「规则写得对」更强：**819 的不变式是
// 「表里有行 ⇔ 请求开始了且无终态记录」，而空表与「一切正常」在值上
// 完全不可区分。** 任何只测「规则语法正确」的检查都抓不到这一层——
// 这道门钉的是**失效形态被覆盖**，不是表达式能解析。
//
// 三个失效形态（缺任何一个都会留下静默失效面）：
//   ① 一次都没写   —— 没部署 / 迁移没上 / 接线被拆
//   ② 写了但失败   —— fail-open 分支（设计如此）
//   ③ 写了但删不掉 —— DELETE 半边坏了，表以全流量速率增长
const abandonedRuleFile = "request-abandoned.yml"

func loadAbandonedAlerts(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(abandonedRuleFile)
	require.NoError(t, err)

	var file struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert string            `yaml:"alert"`
				Expr  string            `yaml:"expr"`
				For   string            `yaml:"for"`
				Label map[string]string `yaml:"labels"`
				Anno  map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.Len(t, file.Groups, 1, "one rule group expected")
	require.Equal(t, "request_abandoned_marker", file.Groups[0].Name)

	byAlert := make(map[string]string)
	for _, r := range file.Groups[0].Rules {
		require.NotEmpty(t, r.Alert, "a rule without an alert name never fires")
		require.NotEmpty(t, r.Expr, "alert %s has an empty expr", r.Alert)
		require.NotEmpty(t, r.For, "alert %s has no for: window", r.Alert)
		require.NotEmpty(t, r.Label["severity"], "alert %s has no severity", r.Alert)
		require.NotEmpty(t, r.Anno["summary"], "alert %s has no summary", r.Alert)
		require.NotEmpty(t, r.Anno["description"], "alert %s has no description", r.Alert)
		require.NotEmpty(t, r.Anno["runbook"], "alert %s has no runbook", r.Alert)
		require.NotContains(t, byAlert, r.Alert, "duplicate alert name %s", r.Alert)
		byAlert[r.Alert] = r.Expr
	}
	return byAlert
}

// TestRequestAbandonedCoversAllThreeFailureModes 钉住 §9.67.6 记账的那件事：
// 「表会增长目前无人看」。三种坏法少一条，就留下一个静默失效面——
// 而这个面是**用完即焚的**（空表读起来和正常一模一样）。
func TestRequestAbandonedCoversAllThreeFailureModes(t *testing.T) {
	byAlert := loadAbandonedAlerts(t)
	require.Len(t, byAlert, 3,
		"three failure modes (never-written / write-failed / leaking) each need their own alert; "+
			"an empty table is indistinguishable from 'healthy'")

	for _, alert := range []string{
		"RequestAbandonedMarkerNeverWritten", // ①
		"RequestAbandonedMarkerWritesFailing", // ②
		"RequestAbandonedLeaking", // ③
	} {
		require.Contains(t, byAlert, alert,
			"alert %s is missing; without it one of the three failure modes is silent", alert)
	}
}

// TestRequestAbandonedNeverWrittenHandlesCounterVecAbsence 钉住那个
// CounterVec 陷阱。本仓已经有两处现成教训（auto-route-selection-output.yml
// 的注释、以及 §9.68/§9.69 在 252 上发现该指标连样本都没有），
// 所以这不是假想，是**本项目已经栽过两次**的坑。
//
// 判据：`or vector(0)` 形式的兜底必须在 ① 里，且 ① 的 expr 里必须有
// `up{job=~"llm-gateway.*"}` 的存活守卫（否则网关挂了会误报成「没接线」）。
func TestRequestAbandonedNeverWrittenHandlesCounterVecAbsence(t *testing.T) {
	byAlert := loadAbandonedAlerts(t)
	expr := byAlert["RequestAbandonedMarkerNeverWritten"]

	require.Regexp(t, `or\s*\n?\s*\(?\s*0\s*\*\s*max by \(job, instance\) \(up\{`,
		expr,
		"the never-written alert must fall back to a literal 0 when the labeled CounterVec "+
			"has no series yet — without it, `== 0` compares an EMPTY vector and the alert "+
			"never fires, which is the only case it exists to catch (audit §9.67/§9.68)")
	require.Regexp(t, `up\{job=~\"llm-gateway\.\*\"\}[^)]*\)\s*==\s*1`,
		expr,
		"the alert must be gated on the gateway actually being up and scraped — otherwise a "+
			"dead gateway reports as 'the landing pad is not wired'")
}

// TestRequestAbandonedLeakAlertUsesARatioNotAnAbsoluteCount 钉住阈值形态。
//
// 理由写在 yml 里：真正要抓的是 DELETE 半边**整体**坏掉，那时差值 ≈
// 全流量，与部署规模无关；而正常遗弃率实测 0.047%，永远达不到 50%。
// 若有人把它改成绝对条数，同一条规则在 154（大流量）与 252（小流量）
// 上的行为会完全相反——这正是判据必须钉住的地方。
func TestRequestAbandonedLeakAlertUsesARatioNotAnAbsoluteCount(t *testing.T) {
	byAlert := loadAbandonedAlerts(t)
	expr := byAlert["RequestAbandonedLeaking"]

	require.Contains(t, expr, `op="mark"`,
		"the leak alert must measure marks that were never cleared")
	require.Contains(t, expr, `op="clear"`,
		"the leak alert must compare against the clear half; clear-only tells you nothing")
	require.Regexp(t, `0\.5\s*\*`, expr,
		"the threshold must stay a RATIO of mark rate so the rule is independent of traffic scale")
	require.Regexp(t, `op="mark"\}\[30m\]\)\)\s*>\s*0\.05`, expr,
		"a noise floor on the mark rate is required — without it, a near-idle deployment "+
			"flaps on the 0/0 ratio and the alert becomes a permanently-red no-op (§9.37)")
	require.Contains(t, expr, `up{job=~"llm-gateway.*"}`,
		"the leak alert must also be gated on gateway liveness")
}

// TestRequestAbandonedStatesItsKnownLimits 门住「如实登记局限」。
//
// 这组告警有**四个**真实覆盖缺口，其中两个会让人彻底误判：
// ① 252 上没有 Prometheus ⇒ 三条一条都不响；
// ② 计数器是进程内存的，重启清零。
// 一段不声明边界的告警，会让值班的人以为它覆盖了它没覆盖的东西。
func TestRequestAbandonedStatesItsKnownLimits(t *testing.T) {
	data, err := os.ReadFile(abandonedRuleFile)
	require.NoError(t, err)
	text := string(data)

	require.Contains(t, text, "已知局限",
		"the rule file must carry a known-limits section — these alerts have four real "+
			"coverage gaps and an alert that does not say so sends the reader down the wrong path")
	require.Contains(t, text, "252",
		"the limits section must name 252, where §9.56 measured that no Prometheus is "+
			"deployed at all — none of these alerts can fire there, and that must be stated")
	require.Contains(t, text, "进程内存",
		"the limits section must state that the counters are process-memory and reset on restart")
	require.Contains(t, text, "不回填历史",
		"the limits section must state that 819 does not backfill history, so existing row "+
			"count is only visible to a manual SELECT")
}

// TestRequestAbandonedMetricsAreProducedInCode 钉住「指标有名字」不蕴含
// 「指标有生产者」。这是 §9.37「没有告警读的指标是装饰」的**反向形态**：
// 这里是有告警在读，**但如果 Go 侧没注册这个指标，告警就永远读到空**。
func TestRequestAbandonedMetricsAreProducedInCode(t *testing.T) {
	data, err := os.ReadFile("../../../domains/hooks/observability/telemetry/request_abandoned_metrics.go")
	require.NoError(t, err)
	text := string(data)

	const metricName = "llm_gateway_request_abandoned_marker_ops_total"
	require.Contains(t, text, metricName,
		"the Go side must register the exact metric name the rules query — a rename on "+
			"either side alone leaves the alerts reading an empty vector forever")

	// 规则里用到的每个 op 都必须在 Go 侧有对应的 record 站点，
	// 否则该 op 的序列永远不存在（CounterVec 子标签不会自动出现）。
	//
	// ⚠️ 这里必须解析 `op=` 选择器的**全部候选值**，不能只认 `op="x"` 整串：
	// 第一版写成 `strings.Contains(expr, 'op="'+op+'"')`，而 ② 那条规则用的是
	// `op=~"mark_failed|clear_failed"`（正则择一）⇒ `clear_failed` 被判定成
	// 「没被任何规则用到」而整个跳过，**删掉 Go 侧记录点它也不红**。
	// 该洞是变异 M4 实跑抓出来的，不是读代码看出来的。
	byAlert := loadAbandonedAlerts(t)
	client, err := os.ReadFile("../../../domains/hooks/observability/telemetry/client.go")
	require.NoError(t, err)
	clientText := string(client)

	selectorRe := regexp.MustCompile(`op\s*(?:=~|!=|=)\s*"([^"]*)"`)

	opsUsedByRules := map[string]bool{}
	for _, expr := range byAlert {
		for _, m := range selectorRe.FindAllStringSubmatch(expr, -1) {
			for _, v := range strings.Split(m[1], "|") {
				opsUsedByRules[strings.TrimSpace(v)] = true
			}
		}
	}
	require.NotEmpty(t, opsUsedByRules,
		"no op= selector found in any rule expr — the parser is broken, not the rules "+
			"(a test that silently selects nothing passes forever)")

	for op := range opsUsedByRules {
		require.Contains(t, clientText, `recordRequestAbandonedOp("`+op+`")`,
			"op %q is selected by the rules but never recorded in client.go — its series can "+
				"never exist, so any alert filtering on it is reading an empty vector", op)
	}
}
