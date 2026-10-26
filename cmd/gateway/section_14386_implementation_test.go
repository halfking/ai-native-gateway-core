//go:build !integration

package main

// §143.86 的门：★ 这是 §143.85 §8.5 ⑤ 承诺的【实现门】—— ★ 与 §143.85 的【设计门】
// ★ **互相顶替不了**，★ 两道门都要有。
//
// ★★★★★★ 两道门的分工（★ 别搞混）：
//
//	★ §143.85 的【设计门】：★ 钉【设计所依赖的【前提事实】】+【文档登记】
//	  ⇒ ⇒ 它【不能】证明「落地后的代码是对的」
//	★ §143.86 的【实现门】（本文件）：★ 钉【实现确实存在 + 形态正确 + 开关默认关】
//	  ⇒ ⇒ ★★ 【行为正确性】由【同包单元测试】负责（★ domains/hooks/compression/
//	      auto_created_test.go）★ ⇒ ⇒ 因为 cmd/gateway 这个包【看不见】别的包的
//	      内部变量 ⇒ ⇒ 行为断言必须放在同包测试里。★★
//
// ★★★★ 本门要守的：
//
//	① ★★★★ 【载体存在且形态正确】`auto_created.go` 必须提供三个函数 + 一个私有 ctx 键
//	② ★★★★★ 【开关默认关】且 **不得**用 `settings.IsEnabled`（★ 那是 kill-switch 语义）
//	③ ★★★★ 【G-1 在 loadFromDB 内】短路点必须在正确的函数里（★ 覆盖 L-1 整条链）
//	④ ★★★★ 【G-2 在 loadFromLegacyDB 内】（★ 覆盖 L-2）
//	⑤ ★★★★★ 【注入点成对】`WithAutoCreatedSession` 必须与 `r = r.WithContext(ctx)` 同时出现
//	⑥ ★★★ 【跳过日志存在】（★ 属主的验收判据要靠它对拍）
//	⑦ ★★★ 【同包单元测试存在】（★ 行为断言的所在地）
//	⑧ ★★★ 【文档登记】§143.86 段 + LESSONS §277 + AGENTS R129
//
// ★ ★★ 判据② 是本门最重要的一条：★ §143.85 的设计原本写的是
// `settings.IsEnabled("session_cache_skip_auto_created")` ⇒ ⇒ ★★ 但那个 API 是
// 【kill-switch】语义（★ 未注册的名字【fail-open ⇒ 默认「开」】）⇒ ⇒ ★★ 用它实现
// 「默认关」【根本实现不了】⇒ ⇒ ★★ 若有人把实现改回那种写法 ⇒ ⇒ 门必须红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14386Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14386Lessons = "LESSONS.md"
	section14386Agents  = "AGENTS.md"

	section14386Carrier = "domains/hooks/compression/auto_created.go"
	section14386Unit    = "domains/hooks/compression/auto_created_test.go"
	section14386Cache   = "domains/hooks/compression/session_cache.go"
	section14386Handler = "domains/streaming/handler.go"

	// ★★ §143.85 的设计里写错的开关名（★ kill-switch 语义 ⇒ ★ 默认「开」）
	section14386WrongSwitch = `settings.IsEnabled("session_cache_skip_auto_created")`
)

func repoRoot14386(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14386(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14386(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// code14386 返回一个文件的【可执行行】（★ 剥掉整行注释与行尾注释）。
//
// ★★★ 为什么要剥：★ 判据② 要断言「**不得**出现 kill-switch 写法」⇒ ⇒ 若不剥注释，
// ★ ★★ 只要有人在【注释】里引用那段历史写法就会假红 ⇒ ⇒ ⇒ 判据必须分面：
// ★ 【代码面】剥注释后查、★ 【登记面】用原文查。
func code14386(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14386(t, rel), "\n") {
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

// funcBody14386 取出某个函数的源码片段（★ 从函数声明到下一个顶层 `func ` 为止）。
func funcBody14386(t *testing.T, rel, sig string) string {
	t.Helper()
	lines := strings.Split(readRepoFile14386(t, rel), "\n")
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

// TestSection14386CarrierExistsAndShape（★ 判据①）
//
// ★★★★ 【载体存在且形态正确】：★ `auto_created.go` 必须提供
// ★ `WithAutoCreatedSession` / `AutoCreatedSessionID` / `ShouldSkipLegacyLoad`
// ★ + 一个【私有空结构体】ctx 键（★ 避免与其它包撞键）。
func TestSection14386CarrierExistsAndShape(t *testing.T) {
	code := code14386(t, section14386Carrier)
	for _, needle := range []string{
		"func WithAutoCreatedSession(ctx context.Context, sessionID string) context.Context",
		"func AutoCreatedSessionID(ctx context.Context) string",
		"func ShouldSkipLegacyLoad(ctx context.Context, gwSessionID string) bool",
		"type autoCreatedSessionKey struct{}",
	} {
		if !strings.Contains(code, needle) {
			t.Errorf("★ %s 里没有 %q —— "+
				"★ ★★ §143.85 设计的载体形态必须逐字对齐"+
				"★ ⇒ ⇒ ★★ 三个函数 + 私有 ctx 键是【零误伤护栏】的实现载体",
				section14386Carrier, needle)
		}
	}
	// ★★ 零误伤护栏：★ 必须同时比对 sessionID ⇒ ⇒ 不能只有 bool
	if !strings.Contains(code, "id == gwSessionID") {
		t.Errorf("★ %s 里没有 `id == gwSessionID` —— "+
			"★ ★★★★ 零误伤护栏：★ 恢复/重试路径的 gwSessionID 来自【请求头】"+
			"★ ⇒ ⇒ 它【可能】与本次会话不同 ⇒ ⇒ ★★ 只传 bool 会误伤别的会话",
			section14386Carrier)
	}
}

// TestSection14386SwitchIsOptInAndDefaultsOff（★ 判据②）
//
// ★★★★★ 【开关默认关】且 **不得**用 kill-switch 型的 `settings.IsEnabled`。
//
// ★ ★★★ 为什么这一条是本门最重要的一条：★ §143.85 的设计原本写的是
// `settings.IsEnabled("session_cache_skip_auto_created")` ⇒ ⇒ ★★ 实测该 API 是
// 【kill-switch】语义：★ 未注册的名字【fail-open ⇒ 默认「开」】⇒ ⇒ ★★ 用它实现
// 「默认关」【根本实现不了】⇒ ⇒ ★★ 一旦实现照抄设计 ⇒ ⇒ **默认就是「开」**
// ⇒ ⇒ ⇒ 部署瞬间就改变了线上行为 ⇒ ⇒ ⇒ **违背「尽量不破坏现有系统运行」**。
func TestSection14386SwitchIsOptInAndDefaultsOff(t *testing.T) {
	code := code14386(t, section14386Carrier)

	if !strings.Contains(code, `const EnvSkipAutoCreated = "ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED"`) {
		t.Errorf("★ %s 里没有 `const EnvSkipAutoCreated = \"ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED\"` —— "+
			"★ ★★ 开关必须是【独立的 opt-in env】（★ 默认关）⇒ ⇒ 部署后行为不变"+
			"★ ⇒ ⇒ ★★ 判据必须【逐字匹配常量名 + 常量值】★ ⇒ ⇒ 裸子串会被"+
			"★ ⇒ ⇒ 「改名成 …_V2」这种改坏逃过（★ 变异 M4 的实测）",
			section14386Carrier)
	}
	if strings.Contains(code, section14386WrongSwitch) {
		t.Errorf("★ %s 的【可执行代码】里出现了 %s —— "+
			"★ ★★★★★ settings.IsEnabled 是【kill-switch】语义：★ 未注册的名字"+
			"★ ⇒ ⇒ 【fail-open ⇒ 默认「开」】⇒ ⇒ ★★ 用它实现「默认关」【根本实现不了】"+
			"★ ⇒ ⇒ ⇒ ★★ §143.85 设计里的那一版是错的 ⇒ ⇒ 必须用【独立的 opt-in env】",
			section14386Carrier, section14386WrongSwitch)
	}
	// ★★ 默认关必须写在【代码】里（★ 而不是只写在注释里）。
	// ★ ★★ 注意：★ 本判据查的是【剥注释后的代码面】⇒ ⇒ 不能用「注释里写了默认关」当证据
	// ★ ⇒ ⇒ 只能断言【空 env 分支的下一条语句就是 return false】。
	idx := strings.Index(code, `if v == "" {`)
	if idx < 0 {
		t.Errorf("★ %s 的可执行代码里找不到 `if v == \"\" {` —— "+
			"★ ★★ 「空 env ⇒ 关」必须落在【代码】里（★ 注释不算）",
			section14386Carrier)
	} else {
		rest := code[idx+len(`if v == "" {`):]
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "return false") {
			t.Errorf("★ %s 里「空 env」分支的下一条语句不是 `return false`（★ 实际是 %q）—— "+
				"★ ★★ 「默认关」是本设计的安全底线（★ 部署后行为逐字一致）"+
				"★ ⇒ ⇒ 必须落在代码里",
				section14386Carrier, firstLine14386(rest))
		}
	}
}

// firstLine14386 取第一行（★ 报错信息用）。
func firstLine14386(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// TestSection14386GuardG1InsideLoadFromDB（★ 判据③）
//
// ★★★★ 【G-1 必须在 loadFromDB 内】：★ 它省掉的是【腿①②③ 整条读链】。
func TestSection14386GuardG1InsideLoadFromDB(t *testing.T) {
	body := funcBody14386(t, section14386Cache, "func (c *SessionCache) loadFromDB(")
	if !strings.Contains(body, "ShouldSkipLegacyLoad(ctx, gwSessionID)") {
		t.Errorf("★ `loadFromDB` 函数体里没有短路守卫 —— "+
			"★ ★★ G-1 覆盖 L-1（★ 省腿①②③ 整条读链）⇒ ⇒ 守卫必须在【这个函数】里"+
			"★ ⇒ ⇒ 只放在 `loadFromLegacyDB` 里 ⇒ ⇒ 只省 L3、★ 不省腿①② ⇒ ⇒ 收益打折（★ %s）",
			section14386Cache)
	}
}

// TestSection14386GuardG2InsideLoadFromLegacyDB（★ 判据④）
//
// ★★★★ 【G-2 必须在 loadFromLegacyDB 内】：★ L-2 直连它、绕过腿①② ⇒ ⇒ G-1 覆盖不到。
func TestSection14386GuardG2InsideLoadFromLegacyDB(t *testing.T) {
	body := funcBody14386(t, section14386Cache, "func (c *SessionCache) loadFromLegacyDB(")
	if !strings.Contains(body, "ShouldSkipLegacyLoad(ctx, gwSessionID)") {
		t.Errorf("★ `loadFromLegacyDB` 函数体里没有短路守卫 —— "+
			"★ ★★ L-2（★ 共享路径 legacy 校验分支）直连本函数、★ 绕过腿①②"+
			"★ ⇒ ⇒ ⇒ G-1 覆盖不到它 ⇒ ⇒ 必须在这里另设一个守卫（★ 当前是防御性的）（★ %s）",
			section14386Cache)
	}
}

// TestSection14386InjectionIsPairedWithRequestContext（★ 判据⑤）
//
// ★★★★★ 【注入点必须成对】：★ `WithAutoCreatedSession(ctx, ...)` 之后必须紧跟
// ★ `r = r.WithContext(ctx)` —— ★ 因为压缩器拿的是 **r.Context()**（★ 不是局部 ctx）。
//
// ★ ⇒ ⇒ ★★ 只改 ctx 不改 r ⇒ ⇒ 标志到不了压缩器 ⇒ ⇒ 整个优化【静默无效】。
func TestSection14386InjectionIsPairedWithRequestContext(t *testing.T) {
	raw := readRepoFile14386(t, section14386Handler)
	lines := strings.Split(raw, "\n")
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, "compression.WithAutoCreatedSession(") {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("★ %s 里没有 `compression.WithAutoCreatedSession(` —— "+
			"★ ★★ 注入点是【标志下传】的唯一入口 ⇒ ⇒ 缺了它 ⇒ ⇒ 压缩层永远拿不到标志",
			section14386Handler)
	}
	// ★★ 必须逐字匹配【传的是 sessionID】：★ 只查「有注入」会漏掉「传了空串」
	// ★ ★★ ⇒ ⇒ 那样【语义上短路永不发生】⇒ ⇒ 而形态看着完全正常（★ 变异 M3 的实测）。
	if !strings.Contains(lines[idx], "compression.WithAutoCreatedSession(ctx, sessionID)") {
		t.Errorf("★ 注入点没有逐字传 `sessionID`（★ 实际是 %q）—— "+
			"★ ★★★★ 传空串 ⇒ ⇒ ctx 里没有会话 ID ⇒ ⇒ ★★ 短路【永不发生】"+
			"★ ⇒ ⇒ ★★ 而形态看着完全正常 ⇒ ⇒ 必须逐字钉住",
			strings.TrimSpace(lines[idx]))
	}
	window := strings.Join(lines[idx:min(idx+4, len(lines))], "\n")
	if !strings.Contains(window, "r = r.WithContext(ctx)") {
		t.Errorf("★ 注入点之后 4 行内没有 `r = r.WithContext(ctx)` —— "+
			"★ ★★★★ 压缩器拿的是 **r.Context()**（★ handler.go 内的 Prepare 调用）"+
			"★ ⇒ ⇒ ★★ 只改 ctx 不改 r ⇒ ⇒ 标志到不了压缩器 ⇒ ⇒ 整个优化【静默无效】"+
			"★ ⇒ ⇒ ★★ 这一条是本轮最容易写漏的一行（★ %s）",
			section14386Handler)
	}
}

// TestSection14386SkipLogsExist（★ 判据⑥）
//
// ★★★ 【跳过日志存在】：★ 属主的验收判据要靠它与服务侧/库侧对拍
// ★ （★ 「库侧 Δcalls 下降」无法区分「短路生效」与「流量变了」）。
func TestSection14386SkipLogsExist(t *testing.T) {
	code := code14386(t, section14386Cache)
	for _, needle := range []string{
		"session_cache: skip db load, auto-created session",
		"session_cache: skip legacy load, auto-created session",
	} {
		if !strings.Contains(code, needle) {
			t.Errorf("★ %s 里没有跳过日志 %q —— "+
				"★ ★★ 属主要验收「短路是否真的生效」⇒ ⇒ 需要服务侧可 grep 的计数日志"+
				"★ ⇒ ⇒ ★★ 只有库侧 Δcalls 是不够的（★ 分不清「短路生效」与「流量变了」）",
				section14386Cache, needle)
		}
	}
}

// TestSection14386UnitTestsExist（★ 判据⑦）
//
// ★★★ 【同包单元测试存在】：★ 行为断言（★ 默认关、★ sessionID 不等不短路）
// ★ 必须在 compression 包内 ⇒ ⇒ 因为 cmd/gateway 那个包【看不见】别包的内部变量。
func TestSection14386UnitTestsExist(t *testing.T) {
	code := code14386(t, section14386Unit)
	for _, needle := range []string{
		"TestSkipAutoCreatedDefaultIsOff",
		"TestShouldSkipLegacyLoadRequiresEqualSessionID",
		"TestShouldSkipLegacyLoadRequiresSwitchOn",
	} {
		if !strings.Contains(code, "func "+needle+"(") {
			t.Errorf("★ %s 里没有测试 %s —— "+
				"★ ★★ 【行为正确性】的断言地点是同包单元测试（★ cmd/gateway 看不见内部变量）"+
				"★ ⇒ ⇒ ★★ 「默认关」与「sessionID 不等不短路」这两条必须有测试钉住",
				section14386Unit, needle)
		}
	}
}

// TestSection14386HandoffSurfacesExist（★ 判据⑧）
func TestSection14386HandoffSurfacesExist(t *testing.T) {
	runbook := readRepoFile14386(t, section14386Runbook)
	i := strings.Index(runbook, "### §143.86")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.86 段 —— ★ 本轮的落地必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED", // ★ 开关名
		"G-1",                                    // ★ 守卫 1
		"G-2",                                    // ★ 守卫 2
		"零行为变更",                                  // ★ 安全底线声明
		"回滚",                                     // ★ 回滚方式
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.86 段里没有 %q —— "+
				"★ ★★ 本轮的开关名、★ 两个守卫、★ 安全底线与★ 回滚方式必须可复核"+
				"★ ⇒ ⇒ ★★ 属主要靠它判断「能不能开这个开关」",
				needle)
		}
	}
	if !strings.Contains(readRepoFile14386(t, section14386Lessons), "## §277") {
		t.Error("★ LESSONS.md 里没有 §277 —— " +
			"★ ⇒ ⇒ 「kill-switch 型 API 不能当 opt-in 用」这条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14386(t, section14386Agents), "R129") {
		t.Error("★ AGENTS.md 里没有 R129 —— " +
			"★ ⇒ ⇒ 本轮踩到的「设计轮必须实测所依赖 API 的实际语义」必须落在交接段")
	}
}
