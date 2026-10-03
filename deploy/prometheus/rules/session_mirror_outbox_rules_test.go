package rules_test

import (
	"os"
	"path/filepath"
	"regexp"

	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// session_mirror_outbox 重放面告警的门（§9.92，2026-10-03）
//
// # 这道门为什么是「对账」而不是「检查文件存在」
//
// `session_mirror_outbox_replays_total` 与 `session_mirror_outbox_dead_total`
// **改过名**（去掉 `llmgw_` 前缀）。docs/db-changelog.md:744-751 把它记成
// 「改名即断流」，并明确写了：仓内 grep 无引用，**但仓外的 Grafana 面板 /
// 告警规则 / 采集配置不在该论证范围内**。
//
// ⇒ 「仓内没有引用」**不能**推出「没有断流风险」。唯一能推出的是：
// 仓内新增的告警必须跟着改名一起改，否则它会安静地指向一个不存在的指标。
//
// 本门做的正是这件事：**从 replay.go 的 `Name:` 字面量里解析出真实指标名**，
// 再要求告警规则文件里逐个出现。改名时若忘了改规则，这里会红，
// 而不是让那条告警在生产上永不触发、且没有任何人发现。
//
// # 为什么必须从源码解析、不能把名字写死在门里
//
// 门里写死名字 = 改名的同时顺手把门里的名字也改掉 ⇒ 又回到「两边都记得改」
// 的人肉同步。而**人肉同步正是 db-changelog 警告的那个失效模式**。
// 从源码推导，权威源只有一个。
const replayGoPath = "../../../internal/sessionv2mirror/replay.go"

// metricNamesFromSource 解析 replay.go 里 promauto 计数器的 Name: 字面量。
// 只认 `Name: "..."` 形式（prometheus.CounterOpts 的字段），不匹配 HELP 文本。
func metricNamesFromSource(t *testing.T, src string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err, "读不到 %s —— 这道门依赖它推导指标名", src)

	re := regexp.MustCompile(`Name:\s*"(session_[a-z0-9_]+)"`)
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		out[m[1]] = true
	}
	return out
}

func TestMirrorOutboxRulesUseLiveMetricNames(t *testing.T) {
	names := metricNamesFromSource(t, filepath.FromSlash(replayGoPath))
	require.NotEmpty(t, names, "从 replay.go 一个 Name: 字面量都没解析到 —— 解析器失效，"+
		"这道门会恒绿，**它现在什么也没验**")

	// 这两个是本组告警赖以存在的那两个，必须能从源码解析出来。
	for _, want := range []string{
		"session_mirror_outbox_replays_total",
		"session_mirror_outbox_dead_total",
		"session_v2_mirror_outbox_pending",
	} {
		require.Containsf(t, names, want,
			"replay.go 里已找不到 %q —— 要么计数器被改名/删除，要么解析器跟不上改法。"+
				"**告警规则里若还写着旧名字，它现在已经断流，且不会有人发现。**", want)
	}

	rules, err := os.ReadFile("session-mirror-outbox.yml")
	require.NoError(t, err)

	for _, metric := range []string{
		"session_mirror_outbox_dead_total",
		"session_v2_mirror_outbox_pending",
	} {
		require.Containsf(t, string(rules), metric,
			"告警规则未引用 %q —— 该指标有生产者、有值、**无消费者**（§9.37）", metric)
	}

	// 反向：规则里不得残留**改名前**的旧前缀。db-changelog 记录的旧名是
	// `llmgw_session_mirror_outbox_*`；若有人照抄旧名，告警会永不触发。
	require.NotContains(t, string(rules), "llmgw_session_mirror_outbox",
		"规则里出现了改名前的 llmgw_ 前缀指标名 —— 该告警指向不存在的指标，永不触发")
}

// TestMirrorOutboxRuleStructureIsWellFormed 防止「文件在但不生效」：
// 告警必须有 alert 名、expr、labels、annotations，且 for 不能漏
// （漏 for 的告警在瞬时抖动时会反复 fire/unfire）。
func TestMirrorOutboxRuleStructureIsWellFormed(t *testing.T) {
	data, err := os.ReadFile("session-mirror-outbox.yml")
	require.NoError(t, err)

	var file struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Expr        string            `yaml:"expr"`
				For         string            `yaml:"for"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.NotEmpty(t, file.Groups, "规则文件没有 groups —— 文件在但不生效")

	seen := map[string]bool{}
	for _, g := range file.Groups {
		for _, r := range g.Rules {
			require.NotEmpty(t, r.Alert, "rule without an alert name never fires")
			require.False(t, seen[r.Alert], "alert %s 重复定义", r.Alert)
			seen[r.Alert] = true
			require.NotEmpty(t, r.Expr, "alert %s has an empty expr", r.Alert)
			require.NotEmpty(t, r.For, "alert %s 缺 for —— 瞬时抖动会反复 fire/unfire", r.Alert)
			require.NotEmpty(t, r.Labels, "alert %s 缺 labels", r.Alert)
			require.Contains(t, r.Labels, "severity", "alert %s 缺 severity", r.Alert)
			require.NotEmpty(t, r.Annotations, "alert %s 缺 annotations", r.Alert)
			require.Contains(t, r.Annotations, "summary", "alert %s 缺 summary", r.Alert)
			require.Contains(t, r.Annotations, "description", "alert %s 缺 description", r.Alert)
		}
	}
	require.NotEmpty(t, seen, "没有任何告警被解析到")
}
