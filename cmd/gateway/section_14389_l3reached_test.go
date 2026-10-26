//go:build !integration

package main

// §143.89 的门：★ 本轮是【「走到 L3」服务侧埋点的落地轮】⇒ ⇒ 产出是【代码 + 观测开关】。
// ★★★★★★ 这个门要守的是【埋点不能被悄悄挪走 / 关掉 / 改名】：
//
//	① ★★★★ 埋点载体存在且形态正确（★ 开关常量 + 埋点函数）
//	② ★★★★★ 【开关默认关】且 **不得**用 `settings.IsEnabled`（★ 那是 kill-switch 语义）
//	③ ★★★★★★ 【埋点位置】＝ 在 `loadFromLegacyDB` 内，且顺序是
//	    G-2 守卫 → LogL3Reached → L3 查询 —— ★★★ 这条是本门最硬的
//	④ ★★★★ 埋点文本可 grep（★ 属主的对拍手段）
//	⑤ ★★★ 同包单元测试存在（★ 行为断言的所在地）
//	⑥ ★★★ 文档登记：§143.89 段 + LESSONS §283 + AGENTS R133
//
// ★ ★★ 判据③ 为什么必须【钉位置】而不只是【钉存在】：
// ★ ★★ 埋点挪到 G-2 守卫【之前】⇒ ⇒ 被 §143.86 短路的那些【也会被记成「走到 L3」】
// ★ ⇒ ⇒ 服务侧计数与库端 L3 Δcalls 【对不上】⇒ ⇒ §143.88 的对拍口径【当场失效】。
// ★ ⇒ ⇒ ★★ 所以「位置」本身就是一个语义判据，不是风格问题。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14389Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14389Lessons = "LESSONS.md"
	section14389Agents  = "AGENTS.md"
	section14389Carrier = "domains/hooks/compression/l3_reached_log.go"
	section14389Unit    = "domains/hooks/compression/l3_reached_log_test.go"
	section14389Cache   = "domains/hooks/compression/session_cache.go"
)

func repoRoot14389(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14389(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14389(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// code14389 返回一个文件的【可执行行】（★ 剥掉整行注释与行尾注释）。
//
// ★ ★★ 为什么要剥：★ 判据③ 要断言【埋点的位置】，★ 而 §143.89 的说明文字
// ★ ★★ 里就写着 `LogL3Reached` ⇒ ⇒ 若不剥注释，★ 只在注释里出现也会被判成「有埋点」。
func code14389(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14389(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if idx := strings.Index(line, "//"); idx >= 0 {
			trimmed = strings.TrimSpace(line[:idx])
		}
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// funcBody14389 取出某个函数的源码片段（★ 从函数声明到下一个顶层 `func ` 为止）。
func funcBody14389(t *testing.T, rel, sig string) string {
	t.Helper()
	lines := strings.Split(readRepoFile14389(t, rel), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), sig) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("★ %s 里找不到函数 %s", rel, sig)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "func ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// codeFuncBody14389 取函数体的【可执行行】（★ 剥注释）。
func codeFuncBody14389(t *testing.T, rel, sig string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(funcBody14389(t, rel, sig), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if idx := strings.Index(line, "//"); idx >= 0 {
			trimmed = strings.TrimSpace(line[:idx])
		}
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// TestSection14389CarrierExistsAndShape（★ 判据①）
func TestSection14389CarrierExistsAndShape(t *testing.T) {
	code := code14389(t, section14389Carrier)
	for _, needle := range []string{
		`const EnvL3ReachedLog = "ENABLE_SESSION_CACHE_L3_REACHED_LOG"`,
		"func LogL3Reached(ctx context.Context, sessionID, tenantID string)",
		"func l3ReachedLogEnabledDefault() bool",
	} {
		if !strings.Contains(code, needle) {
			t.Errorf("★ %s 里没有 %q —— "+
				"★ ★★ 埋点载体形态必须逐字对齐（★ 开关常量 + 埋点函数 + 纯函数开关）"+
				"★ ⇒ ⇒ ★★ 常量名与常量值都要钉住（★ 裸子串会被「改名成 …_V2」逃过）",
				section14389Carrier, needle)
		}
	}
}

// TestSection14389SwitchIsOptInAndDefaultsOff（★ 判据②）
//
// ★ ★★★★★ 这一条是本门的安全底线：★ 埋点若默认开 ⇒ ⇒ 部署瞬间就给生产加日志。
// ★ ⇒ ⇒ ★★ 而且 **不得**用 `settings.IsEnabled`（★ 那是 kill-switch ⇒ 未注册 ⇒ 默认「开」）。
func TestSection14389SwitchIsOptInAndDefaultsOff(t *testing.T) {
	code := code14389(t, section14389Carrier)

	if strings.Contains(code, "settings.IsEnabled") {
		t.Errorf("★ %s 的【可执行代码】里出现了 settings.IsEnabled —— "+
			"★ ★★★★★ 那是 kill-switch 语义（★ 未注册的名字 fail-open ⇒ 默认「开」）"+
			"★ ⇒ ⇒ ★★ 用它实现「默认关」【根本实现不了】⇒ ⇒ 必须用【独立的 opt-in env】",
			section14389Carrier)
	}
	idx := strings.Index(code, `if v == "" {`)
	if idx < 0 {
		t.Errorf("★ %s 的可执行代码里找不到 `if v == \"\" {` —— "+
			"★ ★★ 「空 env ⇒ 关」必须落在【代码】里（★ 注释不算）",
			section14389Carrier)
	} else {
		rest := strings.TrimSpace(code[idx+len(`if v == "" {`):])
		if !strings.HasPrefix(rest, "return false") {
			t.Errorf("★ %s 里「空 env」分支的下一条语句不是 `return false`（★ 实际是 %q）—— "+
				"★ ★★ 默认关是本轮的安全底线（★ 部署后行为逐字一致）",
				section14389Carrier, firstLine14389(rest))
		}
	}
}

// firstLine14389 取第一行（★ 报错信息用）。
func firstLine14389(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// TestSection14389ProbeIsInRightPlace（★ 判据③）—— ★★★ 本门最硬的一条
//
// ★★★★★★ 埋点必须【恰好】在 G-2 守卫之后、L3 查询之前：
//
//	★ 放在守卫【之前】⇒ ⇒ 被 §143.86 短路的那些【也被记成「走到 L3」】
//	  ⇒ ⇒ 服务侧计数与库端 L3 Δcalls 【对不上】⇒ ⇒ §143.88 的对拍口径当场失效；
//	★ 放在 L3 查询【之后】⇒ ⇒ 一次 L3 调用可能【不产生】日志（★ 查询失败也返回）
//	  ⇒ ⇒ 计数不再等于「L3 调用数」。
func TestSection14389ProbeIsInRightPlace(t *testing.T) {
	body := codeFuncBody14389(t, section14389Cache, "func (c *SessionCache) loadFromLegacyDB(")

	iGuard := strings.Index(body, "ShouldSkipLegacyLoad(ctx, gwSessionID)")
	iProbe := strings.Index(body, "LogL3Reached(ctx, gwSessionID, tenantID)")
	iQuery := strings.Index(body, "LastOutboundForSession(ctx, tenantID, gwSessionID)")

	if iGuard < 0 {
		t.Fatalf("★ %s 的 `loadFromLegacyDB` 里找不到 G-2 守卫 —— "+
			"★ ★★ 守卫不在 ⇒ ⇒ 判据③的顺序无从谈起（★ §143.86 的实现被改坏了）",
			section14389Cache)
	}
	if iProbe < 0 {
		t.Errorf("★ %s 的 `loadFromLegacyDB` 里没有埋点调用 —— "+
			"★ ★★★ 埋点必须放在【这里】：★ 它在 L3 查询【之前】⇒ ⇒ 每次真正发起 L3 查询"+
			"★ ⇒ ⇒ 【恰好一条】日志；★ 它在 G-2 守卫【之后】⇒ ⇒ 被短路的那些不算",
			section14389Cache)
		return
	}
	if iQuery < 0 {
		t.Fatalf("★ %s 的 `loadFromLegacyDB` 里找不到 L3 查询 —— "+
			"★ ★★ 判据③依赖 L3 查询的位置（★ %s）",
			section14389Cache, section14389Cache)
	}
	if !(iGuard < iProbe && iProbe < iQuery) {
		t.Errorf("★ 埋点位置不对（★ 守卫 %d / 埋点 %d / 查询 %d）—— "+
			"★ ★★★ 必须是【G-2 守卫 → 埋点 → L3 查询】这个顺序："+
			"★ ⇒ ⇒ 放守卫【之前】⇒ ⇒ 被短路的也被计数 ⇒ ⇒ 与库端 L3 Δcalls 对不上；"+
			"★ ⇒ ⇒ 放查询【之后】⇒ ⇒ 一次 L3 调用可能【不产生】日志",
			iGuard, iProbe, iQuery)
	}
}

// TestSection14389ProbeTextIsGreppable（★ 判据④）
func TestSection14389ProbeTextIsGreppable(t *testing.T) {
	code := code14389(t, section14389Carrier)
	if !strings.Contains(code, "session_cache: read chain reached L3") {
		t.Errorf("★ %s 里没有埋点文本 `session_cache: read chain reached L3` —— "+
			"★ ★★ 属主与服务侧的 v2 落空日志求交要靠它（★ grep）⇒ ⇒ 文本必须固定",
			section14389Carrier)
	}
}

// TestSection14389UnitTestsExist（★ 判据⑤）
func TestSection14389UnitTestsExist(t *testing.T) {
	code := code14389(t, section14389Unit)
	for _, needle := range []string{
		"TestL3ReachedLogDefaultIsOff",
		"TestL3ReachedLogEnvSemantics",
		"TestLogL3ReachedIgnoresEmptySessionID",
	} {
		if !strings.Contains(code, "func "+needle+"(") {
			t.Errorf("★ %s 里没有测试 %s —— "+
				"★ ★★ 【行为正确性】的断言地点是同包单元测试（★ cmd/gateway 看不见内部变量）"+
				"★ ⇒ ⇒ ★★ 「默认关」这条必须有测试钉住",
				section14389Unit, needle)
		}
	}
}

// TestSection14389HandoffSurfacesExist（★ 判据⑥）
func TestSection14389HandoffSurfacesExist(t *testing.T) {
	runbook := readRepoFile14389(t, section14389Runbook)
	i := strings.Index(runbook, "### §143.89")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.89 段 —— ★ 本轮的埋点必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"ENABLE_SESSION_CACHE_L3_REACHED_LOG", // ★ 开关名
		"session_cache: read chain reached L3", // ★ 埋点文本
		"默认关",                                // ★ 安全底线
		"闭合 s",                                // ★ 本轮目的
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.89 段里没有 %q —— "+
				"★ ★★ 开关名、★ 埋点文本、★ 安全底线与★ 本轮目的必须可复核"+
				"★ ⇒ ⇒ ★★ 属主要靠它判断「能不能开这个开关」",
				needle)
		}
	}
	if !strings.Contains(readRepoFile14389(t, section14389Lessons), "## §283") {
		t.Error("★ LESSONS.md 里没有 §283 —— " +
			"★ ⇒ ⇒ 本轮「埋点位置本身是语义判据」这条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14389(t, section14389Agents), "R133") {
		t.Error("★ AGENTS.md 里没有 R133 —— " +
			"★ ⇒ ⇒ 本轮「观测埋点要先闭合量具再谈收益」这条必须落在交接段")
	}
}
