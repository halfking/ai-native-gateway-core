//go:build !integration

package main

// §143.84 的门：把「L3 的调用方穷举」与「本轮抓到的两处量具缺陷」钉成代码面 + 量具面 + 文档面三组判据。
//
// ★★★★★★ 为什么这一轮要有这道门：
//
//	★ §143.83 把「库侧某条 SQL 的 calls」当成【场景计数】用（★ 落空次数），
//	★ ⇒ ⇒ 并明确留下诚实边界②：「库侧 L3 是否有别的调用方」【未穷举】。
//	★ ⇒ ⇒ 本轮把这件事【穷举 + 闭合】，★ 并顺手抓到【两处量具缺陷】：
//
//	  ① ★★★★ 【量具缺陷·恒假阴性】§143.81 复算脚本的 Q6/Q7 两条【当前形态自动判定】
//	     的 needle 写成了 'AND (sb.outbound_body ...'（★ ★★ 【少一个左括号】）
//	     ⇒ ⇒ 库内真实文本是 'AND ((sb.outbound_body ...'
//	     ⇒ ⇒ ★★ 旧 needle 【恒不匹配】⇒ ⇒ Q7 恒输出 "CURRENT SHAPE: NOT FOUND"
//	     ⇒ ⇒ ★★★ 这条判据【从未真正工作过】⇒ ⇒ 必须钉住 needle 的【真实形态】
//	  ② ★★★★ 【量具缺陷·窗口过短】复算脚本 Q1 块内窗口写死 60 s
//	     ⇒ ⇒ 对【突发型】腿不足以判生死
//	     ⇒ ⇒ ★★ 实测（252 共享库、同一时刻）：★ 60 s 读数 L3_gamma Δ=0（假死）
//	     ⇒ ⇒ ★★ 49 min 窗口 L3_gamma Δ=293（活的）⇒ ⇒ 必须钉住窗口告警
//
// ★★ 这道门要守的：
//
//	① ★★★★ 【代码面】L3 的 SQL 文本（`ff.outbound_body`）在非测试 .go 里【只出现在 1 个文件】
//	   ⇒ ⇒ ⇒ 【库侧 L3 calls 不含第二个文本生产者的贡献】⇒ ⇒ 关闭 §143.83 诚实边界②
//	② ★★★ 【代码面】`LastOutboundForSession` 的非测试实现里【PG 实现恰好 1 个】
//	   ⇒ ⇒ ⇒ InMemory / mock 实现【不产生 PG SQL】⇒ ⇒ 不污染库侧读数
//	③ ★★★ 【代码面】PG 后端的注入点【恰好 1 处】（`cmd/gateway/main.go`）
//	   ⇒ ⇒ ⇒ admin / bg / bench / forensics 【都没有注入】⇒ ⇒ 不构成 L3 调用方
//	④ ★★★ 【代码面】`cmd/compression-bench` 的 `Cache` 必须是 nil、★ `cmd/sessionforensics`
//	   的 `NewReplayer()` 必须无参 ⇒ ⇒ 两个 CLI 工具【不产生 L3】
//	⑤ ★★★★ 【代码面】`GetOrLoad(` 的非测试调用点必须【≥3 个文件】且必须包含
//	   `recovery_coordinator.go` ⇒ ⇒ ⇒ 钉住 §143.83 漏掉的【第三个触发入口】
//	⑥ ★★★★ 【量具面】复算脚本的 needle 必须与【库内主 WHERE 的真实形态】逐字对齐
//	   （★ 剥注释后查 ⇒ ★ 单括号形态出现次数必须为 0）
//	⑦ ★★★ 【量具面】复算脚本必须带【窗口告警】（★ ≥10 分钟 / 60 s 参考）
//	⑧ ★★★ 【文档面】§143.84 段必须登记【分解数字】与【两处量具缺陷】
//	⑨ ★★ LESSONS §275 与 AGENTS R127 必须存在

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14384Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14384Lessons = "LESSONS.md"
	section14384Agents  = "AGENTS.md"

	section14384LegValueScript = "scripts/audit/verify_l3_leg_value.sql"
	section14384WiringFile     = "cmd/gateway/main_v3_wiring.go"
	section14384GatewayMain    = "cmd/gateway/main.go"
	section14384BenchMain      = "cmd/compression-bench/main.go"
	section14384ForensicsMain  = "cmd/sessionforensics/main.go"
	section14384RecoveryCoord  = "domains/hooks/compression/recovery_coordinator.go"

	// ★★ L3 SQL 文本的独有指纹（★ 全仓只在 lastOutboundSessionSQL 里出现）
	section14384TextFingerprint = "ff.outbound_body"

	// ★★ Q6/Q7 的 needle 真实形态（★ ★★ 双左括号：主 WHERE 是 `AND ((sb.… OR (ff.…))`）
	section14384NeedleGood = "AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <>%"
	// ★★ 旧的坏 needle（★ 单左括号）⇒ ⇒ 可执行形态里必须【出现 0 次】
	section14384NeedleBad = "AND (sb.outbound_body IS NOT NULL AND sb.outbound_body <>%"
)

func repoRoot14384(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14384(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14384(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// scanGoFiles14384 扫全仓非测试 .go 文件，返回每个文件的【可执行行】列表。
//
// ★★★ 为什么必须【剥注释】：
//
//	★ 本轮的两处量具缺陷里，有一处正是「判据在注释里也有这串文本」
//	★ ⇒ ⇒ 判据必须【分面】：★ 代码面用剥注释后的文本、★ 登记面用原文。
//	★ ⇒ ⇒ 否则「needle 出现在注释里」会被算成「needle 生效」。
func scanGoFiles14384(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot14384(t)
	out := map[string][]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		var lines []string
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") {
				continue
			}
			// ★ 去掉行尾注释（★ 只处理整行注释已排除后的行内 `//`）
			if idx := strings.Index(line, "//"); idx >= 0 {
				trimmed = strings.TrimSpace(line[:idx])
			}
			if trimmed == "" {
				continue
			}
			lines = append(lines, trimmed)
		}
		out[rel] = lines
		return nil
	})
	if err != nil {
		t.Fatalf("遍历失败：%v", err)
	}
	return out
}

// TestSection14384L3SQLTextHasSingleProducer（★ 判据①）
//
// ★★★★ 【代码面】L3 的 SQL 文本指纹 `ff.outbound_body` 在非测试 .go 里
//
//	★ 必须【只出现在 cmd/gateway/main_v3_wiring.go 一个文件】
//	★ ⇒ ⇒ ⇒ 【库侧 L3 calls 只可能来自这一段 SQL】⇒ ⇒ ⇒ 不含 admin / bg / bench 的贡献
//
// ★★★ 为什么这条是本轮的核心结论（★ 关闭 §143.83 诚实边界②）：
//
//	★ §143.83 说「`LastOutboundForSession` 是 `pgBackendAdapter` 的方法 ⇒ admin / bg / bench 都可能算进去」
//	★ ⇒ ⇒ 本轮【实测】：★ 仓内 `ff.outbound_body` 只出现一处（★ 那一段 SQL）
//	★ ⇒ ⇒ ★★ 任何别的组件要贡献 L3 calls，★ 它必须执行【逐字相同】的 SQL
//	★ ⇒ ⇒ ★★ 而逐字相同的 SQL 文本在仓内【不存在第二处】⇒ ⇒ ⇒ 结论成立。
func TestSection14384L3SQLTextHasSingleProducer(t *testing.T) {
	files := scanGoFiles14384(t)
	hits := map[string]int{}
	for rel, lines := range files {
		for _, line := range lines {
			if strings.Contains(line, section14384TextFingerprint) {
				hits[rel]++
			}
		}
	}
	if len(hits) != 1 {
		t.Errorf("★ L3 SQL 文本指纹 %q 在非测试 .go 里出现在 %d 个文件（%v）—— "+
			"★ ★★ 本轮实测：★ 只有 %s 一个（★ 那才是 lastOutboundSessionSQL）"+
			"★ ⇒ ⇒ ★★ 「库侧 L3 calls 不含 admin / bg / bench 贡献」这条结论【就不成立】"+
			"★ ⇒ ⇒ ★★ ⇒ §143.83 的诚实边界②【不能关闭】⇒ ⇒ 必须重做调用方穷举",
			section14384TextFingerprint, len(hits), hits, section14384WiringFile)
		return
	}
	if hits[section14384WiringFile] == 0 {
		t.Errorf("★ %s 里没有 %q —— ★ L3 的 SQL 定义搬家了 ⇒ ⇒ 门要更新",
			section14384WiringFile, section14384TextFingerprint)
	}
}

// TestSection14384LastOutboundHasSinglePGImplementation（★ 判据②）
//
// ★★★ 【代码面】`LastOutboundForSession` 的非测试实现里，★ PG 实现必须【恰好 1 个】。
//
//	★ InMemory（`InMemoryL3DB`）与 mock（`MockSessionCacheDB`）实现【不发任何 PG SQL】
//	★ ⇒ ⇒ ⇒ 它们【不污染库侧读数】⇒ ⇒ 「库侧 L3 calls 只来自网关那一个适配器」成立。
func TestSection14384LastOutboundHasSinglePGImplementation(t *testing.T) {
	files := scanGoFiles14384(t)
	type impl struct {
		rel  string
		recv string
	}
	var impls []impl
	for rel, lines := range files {
		for _, line := range lines {
			if !strings.HasPrefix(line, "func ") || !strings.Contains(line, "LastOutboundForSession(") {
				continue
			}
			recv := ""
			if i := strings.Index(line, ")"); i >= 0 {
				recv = line[:i]
			}
			impls = append(impls, impl{rel: rel, recv: recv})
		}
	}
	if len(impls) == 0 {
		t.Fatal("★ 找不到 `LastOutboundForSession` 的非测试实现 —— ★ 门要更新")
	}
	pgImpls := 0
	for _, im := range impls {
		// ★ PG 实现 = 接收者是 `*pgBackendAdapter`（★ 仓内唯一持有 *db.DB 的那个）
		if strings.Contains(im.recv, "pgBackendAdapter") {
			pgImpls++
		}
	}
	if pgImpls != 1 {
		t.Errorf("★ `LastOutboundForSession` 的 PG 实现有 %d 个（全部实现：%+v）—— "+
			"★ ★★ 本轮实测：★ 只有 `*pgBackendAdapter` 一个是 PG 实现"+
			"★ ⇒ ⇒ ★★ 若多出第二个 PG 实现 ⇒ ⇒ 它【也会往同一个 queryid 里计 calls】"+
			"★ ⇒ ⇒ ★★ ⇒ 「库侧 L3 calls 的来源」必须重新穷举",
			pgImpls, impls)
	}
}

// TestSection14384PGBackendInjectedExactlyOnce（★ 判据③ + ④）
//
// ★★★ 【代码面】PG 后端的注入点必须【恰好 1 处】（★ `cmd/gateway/main.go`）。
//
//	★ ★★ 这是「admin / bg / bench / forensics 【都没有注入 L3 后端】」的机器判据。
//	★ ⇒ ⇒ 若新增一处注入 ⇒ ⇒ 库侧 L3 calls 的来源就变了 ⇒ ⇒ §143.84 的结论要重做。
func TestSection14384PGBackendInjectedExactlyOnce(t *testing.T) {
	files := scanGoFiles14384(t)
	injected := map[string]int{}
	for rel, lines := range files {
		for _, line := range lines {
			// ★★★★★ 2026-10-23（§143.84-B）★ 这里只排除【定义行】，★ 【不排除】其它 func 行。
			//
			// ★ ★★★★ 原写法是 `!strings.HasPrefix(line, "func ")` ⇒ ⇒ 它把
			// ★ 【写在单行 func 体里的调用点】也一起排除了 ⇒ ⇒ ★★ 这是一个【盲区】：
			// ★ ⇒ ⇒ 变异 M8（★ 新增 `func init() { _ = dbBackendFromPool(x) }`）
			// ★ ⇒ ⇒ ★★★ 旧写法【完全看不见】⇒ ⇒ ★★ 门【没牙】★ ⇒ ⇒ 本轮已实测复现。
			// ★ ⇒ ⇒ ★★ 现在只排【定义行】（★ `func dbBackendFromPool(`，
			// ★ ⇒ ⇒ ★★ 它在 `cmd/gateway/main_v3_wiring.go:90`）。
			if strings.HasPrefix(line, "func dbBackendFromPool(") {
				continue
			}
			if strings.Contains(line, "dbBackendFromPool(") {
				injected[rel]++
			}
		}
	}
	if len(injected) != 1 || injected[section14384GatewayMain] == 0 {
		t.Errorf("★ PG 后端 `dbBackendFromPool` 的注入点有 %d 处（%v）—— "+
			"★ ★★ 本轮实测：★ 只有 %s 一处"+
			"★ ⇒ ⇒ ★★ 若多了 ⇒ ⇒ 那个组件也会执行 L3 的 SQL ⇒ ⇒ 库侧 calls 的来源变了",
			len(injected), injected, section14384GatewayMain)
	}

	// ★★★★ 【扫描器自证有牙】（★ 本轮变异台 M8 抓出来的盲区 ⇒ ⇒ 必须钉住）
	// ★ ⇒ ⇒ 判据自己必须能看见【藏在单行 func 体里的调用点】
	// ★ ⇒ ⇒ 否则任何「把注入点写进一行 func」都能逃过门。
	for _, probe := range []string{
		"func init() { _ = dbBackendFromPool(x) }",
		"_ = dbBackendFromPool(x)",
		"func dbBackendFromPool(x int) any { return nil }", // ★ 定义行 ⇒ ★ 必须【看不见】
	} {
		seen := 0
		if !strings.HasPrefix(probe, "func dbBackendFromPool(") &&
			strings.Contains(probe, "dbBackendFromPool(") {
			seen++
		}
		want := 1
		if strings.HasPrefix(probe, "func dbBackendFromPool(") {
			want = 0
		}
		if seen != want {
			t.Errorf("★ 判据自身的可见性自检失败（probe=%q 命中 %d 次，期望 %d）—— "+
				"★ ⇒ ⇒ 判据有盲区 ⇒ ⇒ 「把注入点藏进一行 func」能逃过门"+
				"★ ⇒ ⇒ ★★ 这正是本轮变异 M8 抓出来的那类洞",
				probe, seen, want)
		}
	}
}

// TestSection14384CLIToolsDoNotProduceL3（★ 判据④）
//
// ★★★ 【代码面】两个 CLI 工具必须【不产生 L3】：
//
//	★ `cmd/compression-bench`：`Cache: nil` ⇒ ⇒ 压缩器没有缓存层 ⇒ ⇒ 不会走 L3
//	★ `cmd/sessionforensics`：`NewReplayer()` 无参 ⇒ ⇒ l3 为 nil ⇒ ⇒ 不会走 L3
//
// ★★ 任一条被改 ⇒ ⇒ 该工具就会往同一个 queryid 里计 calls ⇒ ⇒ 库侧读数被污染。
func TestSection14384CLIToolsDoNotProduceL3(t *testing.T) {
	bench := readRepoFile14384(t, section14384BenchMain)
	benchOK := false
	for _, line := range strings.Split(bench, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Cache:") && strings.Contains(trimmed, "nil") {
			benchOK = true
			break
		}
	}
	if !benchOK {
		t.Errorf("★ %s 里没有 `Cache: nil` —— ★ compression-bench 一旦注入缓存层，"+
			"★ ⇒ ⇒ 它执行 L3 的 SQL ⇒ ⇒ 【同一个 queryid】的 calls 会被它污染"+
			"★ ⇒ ⇒ ★★ 「库侧 L3 calls = 网关冷启动次数」这条等式【就断了】",
			section14384BenchMain)
	}

	forensics := readRepoFile14384(t, section14384ForensicsMain)
	if !strings.Contains(forensics, "NewReplayer()") {
		t.Errorf("★ %s 里没有无参的 `NewReplayer()` —— ★ sessionforensics 一旦注入 PG 的 L3 后端，"+
			"★ ⇒ ⇒ 它的调用会计进同一个 queryid ⇒ ⇒ 库侧读数被污染",
			section14384ForensicsMain)
	}
}

// TestSection14384GetOrLoadTriggerEntriesAreNotMissed（★ 判据⑤）
//
// ★★★★ 【代码面】`GetOrLoad(` 的非测试调用点必须【≥3 个文件】，★ 且必须包含
//
//	★ `domains/hooks/compression/recovery_coordinator.go`
//
// ★★★ 为什么这条要有门（★ 本轮的真实教训）：
//
//	★ §143.83 说「L3 有【两个调用点】」⇒ ⇒ 那说的是【代码位置】（★ `loadFromLegacyDB` 的两处）
//	★ ⇒ ⇒ ★★ 但【触发入口】有【三个】：★ 压缩器 v1 回落 + ★ 恢复协调器（重试时）
//	★ ⇒ ⇒ ★★ ⇒ 恢复协调器这条【不在 §143.83 的枚举里】⇒ ⇒ ⇒ 门要钉住它。
func TestSection14384GetOrLoadTriggerEntriesAreNotMissed(t *testing.T) {
	files := scanGoFiles14384(t)
	hits := map[string]int{}
	for rel, lines := range files {
		for _, line := range lines {
			if strings.Contains(line, ".GetOrLoad(") {
				hits[rel]++
			}
		}
	}
	if len(hits) < 3 {
		t.Errorf("★ `GetOrLoad(` 的非测试调用点只落在 %d 个文件（%v）—— "+
			"★ ★★ 本轮实测：★ ≥3 个文件（★ 压缩器 / 恢复协调器 / 取证回放）"+
			"★ ⇒ ⇒ ★★ 「触发入口」不止一个 ⇒ ⇒ 只算主路径 ⇒ ⇒ 收益会按比例高估",
			len(hits), hits)
	}
	if hits[section14384RecoveryCoord] == 0 {
		t.Errorf("★ %s 里没有 `GetOrLoad(` 调用点 —— "+
			"★ ★★ §143.83 的「两个调用点」枚举【漏了恢复协调器这条触发入口】"+
			"★ ⇒ ⇒ ★★ ⇒ 「触发入口只有一条」的结论是错的 ⇒ ⇒ 必须重新穷举",
			section14384RecoveryCoord)
	}
}

// stripSQLComments14384 剥掉 SQL 的 `--` 行注释。
//
// ★★★ 为什么必须剥（★ 本轮的核心教训之一）：
//
//	★ 修 needle 时★ 我在注释里【引用了旧 needle 的原文】⇒ ⇒ 若判据不剥注释
//	★ ⇒ ⇒ 「坏 needle 出现在注释里」会被算成「坏 needle 还在生效」⇒ ⇒ 假红。
//	★ ⇒ ⇒ ⇒ 判据必须【分面】：★ 代码面（剥注释）查 needle、★ 登记面（原文）查修订记录。
func stripSQLComments14384(raw string) string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestSection14384LegValueNeedleMatchesRealShape（★ 判据⑥）
//
// ★★★★ 【量具面】§143.81 的复算脚本里，★ 当前形态自动判定的 needle
//
//	★ 必须与【库内主 WHERE 的真实形态】逐字对齐：★ `AND ((sb.outbound_body ...`
//	★ ⇒ ⇒ ★★ 旧的单括号形态在【可执行行】里必须【出现 0 次】
//
// ★★★★ 为什么这是本轮最要紧的量具修复：
//
//	★ ★★ 旧 needle 少了【一个左括号】⇒ ⇒ 恒不匹配 ⇒ ⇒ Q6/Q7 恒输出
//	★ ★★ "CURRENT SHAPE: NOT FOUND" ⇒ ⇒ ★★ 那条「自动判定当前形态」的判据
//	★ ★★ 【从未真正工作过】⇒ ⇒ ★★ 而它【看起来是绿的】（脚本跑得通、输出有格式）
//	★ ★★ ⇒ ⇒ ★★★ 这就是「门绿 ≠ 门有牙」在【脚本判据】上的同族形态。
func TestSection14384LegValueNeedleMatchesRealShape(t *testing.T) {
	raw := readRepoFile14384(t, section14384LegValueScript)
	code := stripSQLComments14384(raw)

	good := strings.Count(code, section14384NeedleGood)
	if good != 2 {
		t.Errorf("★ 复算脚本里【正确 needle】的可执行出现次数是 %d（期望 2：★ Q6 与 Q7 各一处）—— "+
			"★ ★★ 本轮实测：★ 库内真实形态是 `AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <> $n::jsonb)`"+
			"★ ⇒ ⇒ ★★ needle 必须逐字对齐（★ 双左括号）⇒ ⇒ 否则恒 NOT FOUND",
			good)
	}
	if bad := strings.Count(code, section14384NeedleBad); bad != 0 {
		t.Errorf("★ 复算脚本里【旧 needle】（★ 单左括号）的可执行出现次数是 %d（期望 0）—— "+
			"★ ★★ 旧 needle 少了【一个左括号】⇒ ⇒ 恒不匹配 ⇒ ⇒ Q6/Q7 恒 NOT FOUND"+
			"★ ⇒ ⇒ ★★ ⇒ 「当前形态自动判定」这条判据【从未真正工作过】",
			bad)
	}
}

// TestSection14384LegValueScriptWarnsAboutShortWindow（★ 判据⑦）
//
// ★★★ 【量具面】复算脚本必须带【窗口告警】：
//
//	★ ★★ Q1 块内窗口写死 60 s ⇒ ⇒ 对【突发型】腿不足以判生死
//	★ ⇒ ⇒ ★★ 实测：★ 60 s ⇒ L3_gamma Δ=0（假死）；★ 49 min ⇒ Δ=293（活的）
//	★ ⇒ ⇒ ⇒ 脚本必须显式写明「判生死用 ≥10 分钟窗口」。
func TestSection14384LegValueScriptWarnsAboutShortWindow(t *testing.T) {
	raw := readRepoFile14384(t, section14384LegValueScript)
	for _, needle := range []string{"≥10 分钟", "60 s", "diff_l3_snapshots.py"} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 复算脚本里没有 %q —— "+
				"★ ★★ Q1 的 60 s 内采样对【突发型】腿【不足以判生死】"+
				"★ ⇒ ⇒ ★★ 实测：★ 60 s 读数 L3_gamma Δ=0（假死）、49 min Δ=293（活的）"+
				"★ ⇒ ⇒ ★★ ⇒ 判生死必须用长窗口 ⇒ ⇒ 脚本必须显式告警",
				needle)
		}
	}
}

// TestSection14384RunbookRegistersDecompositionAndToolDefects（★ 判据⑧）
//
// ★★★ 【文档面】§143.84 段必须登记：
//
//	★ ① 【调用结构分解】的关键数字（★ 293 三相等、★ 26.2 分钟/天、★ 61% 穿透率）
//	★ ② 【两处量具缺陷】（★ Q7 needle 少一个左括号、★ 60 s 窗口不足）
//	★ ③ 【触发入口 3 个】（★ 压缩器 / 恢复协调器 / 取证回放）
func TestSection14384RunbookRegistersDecompositionAndToolDefects(t *testing.T) {
	runbook := readRepoFile14384(t, section14384Runbook)
	i := strings.Index(runbook, "### §143.84")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.84 段 —— ★ 本轮的调用方穷举与分解必须落在仓内")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"293",   // ★ 腿② == 腿③ == L3 的三相等
		"26.2",  // ★ L3 现时分钟/天（49 min 窗口）
		"61%",   // ★ v2 落空穿透到 DB 的比例
		"触发入口",  // ★ 三个触发入口
		"量具",    // ★ 两处量具缺陷
		"49 分钟", // ★ 本轮的窗口长度
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.84 段里没有 %q —— "+
				"★ ★★ 本轮的分解数字与两处量具缺陷必须可复核"+
				"★ ⇒ ⇒ ★★ 尤其【分解的三个数字】⇒ ⇒ 属主的收益重估要靠它们",
				needle)
		}
	}
}

// TestSection14384HandoffSurfacesExist（★ 判据⑨）
func TestSection14384HandoffSurfacesExist(t *testing.T) {
	lessons := readRepoFile14384(t, section14384Lessons)
	if !strings.Contains(lessons, "## §275") {
		t.Error("★ LESSONS.md 里没有 §275 —— " +
			"★ ⇒ ⇒ 「唯一性判据要用【文本指纹】而不是【方法名】」与「窗口太短会给出假死」" +
			"★ ⇒ ⇒ 这两条通用式必须落进去")
	}
	agents := readRepoFile14384(t, section14384Agents)
	if !strings.Contains(agents, "R127") {
		t.Error("★ AGENTS.md 里没有 R127 —— " +
			"★ ⇒ ⇒ 本轮的调用方穷举结论与两处量具修复必须落在交接段（★ 否则下一轮会重走一遍）")
	}
}
