package ursm_test

// 部署面 **feature flag 透传** 的守卫（2026-10-21，R72f 轮新增；同日升级）
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
// # 本文件的门有四道（2026-10-21 R72f 同日升级）
//
//	① TestDeployEnvAllowListCarriesProviderCostReconciliationFlag
//	   —— 钉住「不许回退」：有人删掉那一行 ⇒ 功能又变回永久不可开启。
//	② TestDeployEnvFlagPassthroughKeepsDefaultEmpty
//	   —— 钉住「未设时不改变行为」：透传行必须是 `${KEY:-}` 形态。
//	③ TestDeployEnvFlagNoGapRemains
//	   —— ★ 由「只读清单（不红）」**升级为红灯**：属主决策项 #9 拍板
//	   「逐个补进白名单」⇒ ⇒ 补齐之后「缺口 = 0」就是**不变式** ⇒ ⇒
//	   以后新增 flag 若忘了补白名单 ⇒ **本门变红** ⇒ 而不是继续静默沉睡。
//	④ TestDeployEnvAllowListEnabledDefaultsAreExplicit
//	   —— 所有 `*ENABLED` 条目的默认值必须**显式**：要么空（`${KEY:-}`），
//	   要么在 `nonEmptyDefaultAllow` 里**带理由登记**（当前只有一条既有行为）。
//
// ★ 为什么 ③ 扩大红灯面是**被授权**的：它不是「顺手多查一处」，而是
//   属主对决策项 #9 的明确选择（2026-10-21，选项「逐个补进白名单」）。
//   ⇒ 补齐是**动作**，「补齐后缺口必须为 0」是这条决定的**不变式**。
//
// ★ 诚实边界：③ 只覆盖「代码用 `os.LookupEnv`/`os.Getenv` 读、且键名以
//   `ENABLED` 结尾」的那一族 ⇒ **不覆盖** settings 键、不覆盖非 `ENABLED`
//   形状的 env 键 ⇒ 它是**一族**的门，不是「所有 env 键」的门。

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

// dl_emit_env_line <KEY> "<值表达式>" —— 白名单条目（连值表达式一起抓）。
var allowListRe = regexp.MustCompile(`dl_emit_env_line\s+([A-Z0-9_]+)\s+("[^"]*")`)

// 代码里读 env 的 feature flag：os.LookupEnv("X") / os.Getenv("X")。
var envFlagRe = regexp.MustCompile(`os\.(?:LookupEnv|Getenv)\(\s*"([A-Z0-9_]*ENABLED)"`)

// nonEmptyDefaultAllow：白名单里**允许有非空默认值**的 `*ENABLED` 条目。
//
// ★ 为什么需要这张表：★ 白名单条目的默认值决定了「容器形态下这个开关
//
//	**默认**是什么」。★ `${KEY:-}` = 未设即「未设置」⇒ 走代码里的默认；
//	★ `${KEY:-true}` = 未设即「开」⇒ ⇒ **改的是默认行为**，不是「透传」。
//
// ⇒ ⇒ 所以非空默认必须**逐条登记 + 带理由**，否则「多写一个 true」就悄悄
//
//	改变了所有容器环境的行为。
//
// ⇒ ⇒ 当前只有一条：**SYSTEM_MONITOR** 是**既有行为**（本轮之前就在，
//
//	且已上线运行）⇒ 本轮**不擅自改**它（改它等于改变容器形态下系统监控
//	的开/关状态，属行为变更）⇒ ⇒ 登记在此，带理由。
var nonEmptyDefaultAllow = map[string]string{
	controlFlag: "既有行为（2026-10-21 之前就在白名单里且已上线）；" +
		"★ 改它的默认值 = 改变容器形态下系统监控的开/关状态 ⇒ 属行为变更，" +
		"本轮不擅自改 ⇒ 显式登记以免它成为「无意识的默认行为」。",
}

func readAllowList(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(deployLibFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", deployLibFile, err)
	}
	out := map[string]string{}
	for _, m := range allowListRe.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = m[2]
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

// TestDeployEnvFlagNoGapRemains ★ 由「只读清单」升级为**红灯**。
//
// ★ 升级依据：属主决策项 #9（2026-10-21）拍板「逐个补进白名单」⇒
//
//	补齐已完成 ⇒ 「白名单覆盖全部 env 式 feature flag」从此是**不变式**
//	⇒ ⇒ 以后新增 flag 忘了补白名单 ⇒ 本门**变红** ⇒ 不再静默沉睡。
//
// ★ 判据方向要写清：★ **红 = 有 flag 代码在读、但白名单里没有** ⇒
//
//	⇒ 那个 flag 在容器形态下**收不到**（静默失效）。
func TestDeployEnvFlagNoGapRemains(t *testing.T) {
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
	t.Logf("  白名单已覆盖 %d 个", len(covered))
	t.Logf("  ★ 白名单**未**覆盖 %d 个：%s", len(gap), strings.Join(gap, ", "))

	// ★ 防呆：扫描口径失效 ⇒ 清单会一直是空的 ⇒ 看起来一切正常 ⇒ 显式失败。
	if len(read) == 0 {
		t.Fatalf("★ 扫描不到任何 `os.LookupEnv(\"LLM_GATEWAY_*ENABLED\")` ⇒ " +
			"⇒ 本门的扫描口径可能失效（仓内结构变了）⇒ 清单会一直是空的 ⇒ 请更新扫描口径。")
	}

	if len(gap) > 0 {
		t.Fatalf("★ 有 %d 个「代码用 env 读、但白名单里没有」的 feature flag：\n    %s\n"+
			"   ⇒ ⇒ 容器形态下它们**静默失效**（不报错、不警告，只是配置不生效）\n"+
			"   ⇒ ⇒ 修法：scripts/deploy-local-lib.sh 的 dl_write_env() 里补一行\n"+
			"   ⇒ ⇒ `dl_emit_env_line <KEY> \"${<KEY>:-}\"`（★ 默认值必须为空 ⇒ 未设时行为不变）\n"+
			"   ⇒ ⇒ 见审计档 docs/audit/2026-10-21-R72f-…md §3.6 与 LESSONS §162/§166",
			len(gap), strings.Join(gap, ", "))
	}
}

// TestDeployEnvAllowListEnabledDefaultsAreExplicit 钉住「白名单条目的默认值必须显式」。
//
// ★ 两种合法形态：
//
//	① `"${KEY:-}"`     ⇒ 未设 ⇒ 「未设置」⇒ 走代码里的默认（**零语义风险**）
//	② `"${KEY:-<值>}"` ⇒ 未设 ⇒ **直接是 <值>** ⇒ ⇒ 改的是**默认行为**
//	   ⇒ ⇒ 所以它必须在 `nonEmptyDefaultAllow` 里**带理由登记**
//
// ★ 为什么这道门单独存在：★ 「补白名单」这件事天然会诱发「顺手给个 true」
//
//	⇒ ⇒ 而那**不是透传，是改默认行为** ⇒ ⇒ ⇒ 必须让「多写一个 true」变红。
func TestDeployEnvAllowListEnabledDefaultsAreExplicit(t *testing.T) {
	allow := readAllowList(t)

	emptyForm := func(key string) string { return `"${` + key + `:-}"` }

	var bad []string
	for key, expr := range allow {
		if !strings.HasSuffix(key, "ENABLED") {
			continue // 只管 feature flag 这一族
		}
		if expr == emptyForm(key) {
			continue
		}
		if reason, ok := nonEmptyDefaultAllow[key]; ok {
			t.Logf("★ %s 使用非空默认值 %s ⇒ 已登记：%s", key, expr, reason)
			continue
		}
		bad = append(bad, key+" "+expr)
	}
	sort.Strings(bad)

	if len(bad) > 0 {
		t.Fatalf("★ 以下 `*ENABLED` 白名单条目既不是空默认形态、也没在 nonEmptyDefaultAllow 里登记：\n    %s\n"+
			"   ⇒ ⇒ 写死默认值 = **改默认行为**（未设时该开关在所有容器环境里都是这个值）\n"+
			"   ⇒ ⇒ 要么改成 `\"${<KEY>:-}\"`（未设 ⇒ 未设置 ⇒ 行为不变），\n"+
			"   ⇒ ⇒ 要么在 nonEmptyDefaultAllow 里**带理由**登记（既有行为且已上线）。",
			strings.Join(bad, "\n    "))
	}
}
