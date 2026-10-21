package ursm_test

// 部署面 **feature flag 透传** 的守卫（2026-10-21，R72f 轮新增）
//
// # 背景：一条「静默丢弃」的配置链
//
// 本地/容器部署把运行期环境变量写进 `bundle/env` 的地方是
// `scripts/deploy-local-lib.sh` 的 `dl_write_env()`，而它是**白名单式**的：
// 只有被 `dl_emit_env_line <KEY>` 显式列出的键才会进容器。
//
// ★ 2026-10-21 实测：在 `.env.local` 里写
// `LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED=true` ⇒ **完全无效** ——
// 该键不在白名单 ⇒ 被静默丢弃 ⇒ 容器里根本没有这个变量 ⇒
// `costReconciliationEnabled()`（cmd/gateway/provider_profile_init.go:256，
// env 是它的**第一优先**读法）永远读不到 ⇒
// `/api/admin/provider-cost-reconciliation*` 永远 404 ⇒
// 移动端 `/m/reconciliation` 席（super_admin 可见）点进去必然失败。
//
// ★★ 为什么这条比看起来更隐蔽：
//
//	① **静默** —— 不报错、不警告，只是配置不生效；
//	② **「以为开了」比「没开」更危险** —— 操作员以为功能开了，实际没开；
//	③ settings 那条后备路**也是死的** ——
//	   `provider_profile.cost_reconciliation.enabled` **从未 Register 成 spec**
//	   ⇒ `settings.Global.Spec(key)` 返回 nil ⇒ `GetPlatformBool` 直接回退
//	   fallback(false) ⇒ ⇒ 往 `settings_kv` 里写也无效。
//
// ★ 而这不是孤例：仓内用 env 读取的 `LLM_GATEWAY_*_ENABLED` 有 14+ 个，
// 白名单只覆盖 3 个（SYSTEM_MONITOR / SESSIONS_V2 / HOTZONE）。
// 本门**只钉住本轮修的那一条**（不擅自扩大红灯面 ⇒ 其余缺口作为
// 「只读差异清单」打日志 + 属主决策项，见审计档 §3.6）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	deployLibFile = "../../scripts/deploy-local-lib.sh"

	// 本轮修好的那一条：费用对账子开关。
	reconFlag = "LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED"

	// 阳性对照：既有的 flag（若连它都不在白名单，说明白名单机制本身变了，
	// 那时本门的断言会变成「因为机制改名而假绿」⇒ 必须能识别）。
	controlFlag = "LLM_GATEWAY_SYSTEM_MONITOR_ENABLED"
)

// dl_emit_env_line <KEY> "${KEY:-}" —— 白名单条目。
var allowListRe = regexp.MustCompile(`dl_emit_env_line\s+([A-Z0-9_]+)`)

// 代码里读 env 的 feature flag：os.LookupEnv("X") / os.Getenv("X")。
var envFlagRe = regexp.MustCompile(`os\.(?:LookupEnv|Getenv)\(\s*"([A-Z0-9_]*ENABLED)"`)

func readAllowList(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(deployLibFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", deployLibFile, err)
	}
	out := map[string]string{}
	for _, m := range allowListRe.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = m[0]
	}
	return out
}

// TestDeployEnvAllowListCarriesProviderCostReconciliationFlag 钉住本轮修的 passthrough。
//
// ★ 这道门守的是「**不许回退**」：有人删掉这一行 ⇒ 该功能在容器形态下又变回
// 「永久不可开启」⇒ 移动端那一席又变回必然 404 ⇒ 而失败是**静默**的。
func TestDeployEnvAllowListCarriesProviderCostReconciliationFlag(t *testing.T) {
	allow := readAllowList(t)

	// 阳性对照：白名单机制本身没坏。
	if _, ok := allow[controlFlag]; !ok {
		t.Fatalf("阳性对照 %s 已不在 %s 的白名单里 ⇒ ★ 白名单机制可能改名了 ⇒ "+
			"本门的断言会变成「因为机制变了而假绿」⇒ 请先更新本门的读法。",
			controlFlag, deployLibFile)
	}

	if _, ok := allow[reconFlag]; !ok {
		t.Fatalf("★ %s 不在 %s 的白名单里 ⇒ ⇒ 容器形态下该 feature flag 永远收不到 ⇒ "+
			"「费用对账」在容器形态下永久不可开启 ⇒ 移动端 /m/reconciliation 席必然 404。\n"+
			"   （修复先例：同族的 LLM_GATEWAY_HOTZONE_* 也是这样补进白名单的，见同文件 914-922 行注释）",
			reconFlag, deployLibFile)
	}
}

// TestDeployEnvFlagPassthroughKeepsDefaultEmpty 钉住「未设时不改变行为」。
//
// ★ 这是本次改动的**零语义风险**承诺：白名单条目写成 `${KEY:-}` ⇒
// 没设时写空行 ⇒ gateway 侧视为未设置 ⇒ 行为与改动前完全一致。
func TestDeployEnvFlagPassthroughKeepsDefaultEmpty(t *testing.T) {
	raw, err := os.ReadFile(deployLibFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", deployLibFile, err)
	}
	want := `dl_emit_env_line ` + reconFlag + ` "${` + reconFlag + `:-}"`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("★ %s 的透传行不是 `%s` 形态 ⇒ ⇒ 它可能给了一个非空默认值 ⇒ "+
			"⇒ 那就**改了默认行为**（本轮承诺的是零语义风险：未设时行为不变）。",
			reconFlag, want)
	}
}

// TestDeployEnvFlagGapInventory 只读清单：**不红**，只把「代码读 env 的 flag
// 与白名单覆盖之间的缺口」打出来。
//
// ★ 为什么这道门不设成红灯：仓内还有 10+ 个同类缺口（见输出）⇒
// 把它们一次性设成红灯会**扩大失败面**，那属于「改部署契约」，是属主决策
// （审计档 §3.6 的决策项 #9）⇒ ⇒ 本轮只把它变成**机器可读的清单**，
// 让下一次有人动白名单时能立刻看到全貌。
//
// ★ 诚实边界：这道门**本身不提供任何保护**（它不会让任何东西变红）⇒
// 它的价值是「记录」而不是「守」。
func TestDeployEnvFlagGapInventory(t *testing.T) {
	allow := readAllowList(t)

	root := "../.."

	read := map[string]bool{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if strings.HasPrefix(top, ".") || top == "node_modules" || top == "vendor" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range envFlagRe.FindAllStringSubmatch(string(raw), -1) {
			read[m[1]] = true
		}
		return nil
	})

	var gap, covered []string
	for k := range read {
		if _, ok := allow[k]; ok {
			covered = append(covered, k)
		} else {
			gap = append(gap, k)
		}
	}
	sort.Strings(gap)
	sort.Strings(covered)

	t.Logf("代码用 env 读取的 feature flag：共 %d 个", len(read))
	t.Logf("  白名单已覆盖 %d 个：%s", len(covered), strings.Join(covered, ", "))
	t.Logf("  ★ 白名单**未**覆盖 %d 个（容器形态下收不到 ⇒ 属主决策项 #9）：%s",
		len(gap), strings.Join(gap, ", "))

	if len(read) == 0 {
		t.Fatalf("★ 扫描不到任何 `os.LookupEnv(\"LLM_GATEWAY_*ENABLED\")` ⇒ "+
			"⇒ 本门的扫描口径可能失效（仓内结构变了）⇒ 清单会一直是空的 ⇒ 请更新扫描口径。")
	}
}
