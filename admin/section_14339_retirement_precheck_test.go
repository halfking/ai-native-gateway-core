package admin

// §143.39 的门：**211 独立生产实例**上的「迁移 890 退役前置核验」与
// 「升级链列前提」的一致性。
//
// 背景（runbook §143.39，生产只读实测 2026-10-21）：
// ① §143.38 的核验脚本 `scripts/audit/verify_890_cutover_precheck.sql`
//    **依赖 `pg_stat_statements`**（判据 = 生产上正在跑的那条语句里还有没有
//    引用 v1 族）⇒ ★ **211 上这条路走不通**：211 的 `pg_stat_statements`
//    扩展虽已安装，但**没进 `shared_preload_libraries`** ⇒ 量具完全不可用。
// ② 211 实测：部署二进制 = `7a60b1b1-dirty`（2026-10-20）⇒ 亲和度聚合**仍是
//    两条 v1 臂**（`FROM _request_logs_hot rl` / `FROM _request_logs rl`）
//    ⇒ 890 执行后**整句 42P01**（§143.38 的机制）。
// ③ ★★ **升级链的列前提（本轮新发现，与 ② 方向相反）**：
//    211 的 `session_turns` / `session_turns_hot` **都没有 `host` / `tags` 列**
//    （迁移 885 的对象）⇒ 而新二进制的写路径**显式列** `host` / `tags`
//    ⇒ ⇒ **先部署新二进制 ⇒ 42703 断写**，且 `db.Open` 的 ensure 链**不补**
//    这两列 ⇒ **boot 不会失败**（绿着启动，写路径断）⇒ 蓝绿验收
//    （readyz / version / 网页面）**不覆盖写路径** ⇒ 不会被拦住。
// ④ ★ **台账口径被证伪**：211 的 `schema_migrations` 只有 **63 行 / 最大 869**，
//    但 870~889 的**绝大多数对象都已存在**（870/871/873/874/877/878/882 的
//    索引全在）⇒ ⇒ **「211 落后 13 个迁移」是台账口径的伪影**；
//    真实形态缺口 = **5 个对象**（host/tags 两列 + 3 个索引）。
//
// # 这道门在挡什么
//
// ★★ **「升级链」和「退役链」是两个方向相反的顺序约束，而仓里的门只能看仓里**：
//
//	退役链（删对象，890）：**先部署新二进制**，再执行迁移
//	  ⇒ 否则旧二进制里引用 v1 表的语句整句 42P01。
//	升级链（加列，885）：**先执行迁移**，再部署新二进制
//	  ⇒ 否则新二进制的写路径 42703。
//
// ⇒ ⇒ 两个约束必须**同时**成立 ⇒ 顺序被完全钉死：
//	**885 → 部署新二进制 → 890**
// ⇒ 而这两条约束在**仓内**都不可见（它们取决于「目标机现在跑的是哪个二进制」
// 与「目标库现在有哪些列」）⇒ ⇒ 仓内能提供的是「**核验手段**」，
// 不是「核验结论」⇒ ⇒ 本门守的是**那把尺子**（脚本 + 判据 + 缺口清单），
// 保证它不被人悄悄改坏、也不与迁移文件漂移。

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	precheck211RelPath14339 = "../scripts/audit/verify_890_cutover_precheck_211.sh"
	runbookRelPath14339     = "../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	mig885RelPath14339      = "../sql/migrations/startup/885_session_turns_host_tags.sql"
	mig887RelPath14339      = "../sql/migrations/startup/887_session_turns_search_text_fts.sql"
	mig888RelPath14339      = "../sql/migrations/startup/888_session_family_join_index_gaps.sql"
)

// ─────────────────────────────────────────────────────────────────────────
// T1：211 预检脚本在位 + 判据 needle 齐备 + **只读性**
// ─────────────────────────────────────────────────────────────────────────

// TestSection14339Precheck211ScriptExists 钉住「在 211 上执行 890 之前先跑这个」。
//
// ★ 这道门存在的理由与 §143.38 的门同源，但**补的是另一个盲区**：
//
//	§143.38 的脚本判据 = `pg_stat_statements` 里的活跃语句
//	  ⇒ 只能覆盖**装了 preload 的实例**（252）⇒ 211 上是瞎的。
//	§143.39 的脚本判据 = **已部署二进制里的 SQL 文本**（`strings`）
//	  ⇒ 不依赖任何数据库量具 ⇒ **每个实例都能用**。
//
// ⇒ 两个脚本**并存**（不是替代）：它们看的是**同一份东西**（生产上正在跑的
//
//	那条语句的源码）的**两个来源**（DB 侧统计 vs 产物侧文本）⇒ 互为交叉验证。
func TestSection14339Precheck211ScriptExists(t *testing.T) {
	raw, err := os.ReadFile(precheck211RelPath14339)
	if err != nil {
		t.Fatalf("read %s: %v", precheck211RelPath14339, err)
	}
	body := string(raw)

	for _, needle := range []string{
		// ★ 必须显式**不以** pg_stat_statements 为依赖 —— 这是本脚本存在的理由。
		"pg_stat_statements",
		// 判据：二进制里的 v1 臂
		"_request_logs",
		"auto_route_selections_all",
		// 判据：升级链的列前提
		"session_turns.host",
		"session_turns_hot.host",
		"tags",
		// 判词
		"BLOCKED",
		"BLOCKER-1",
		"BLOCKER-2",
		// 必查项
		"storage.request_logs_write_enabled",
		"schema_migrations",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("211 前置核验脚本缺 %q ⇒ 它判不出对应的阻塞项", needle)
		}
	}

	// ★★ **只读性**：判据的**范围**必须是「真正发给 psql 的 SQL」，
	//   而不是「脚本全文」。
	//
	// ★★ 第一版就是按「脚本全文 + SQL 剥注释」判的 ⇒ **当场假红 4 条**：
	//   ① 脚本头是 **shell 注释**（`#`），而 §143.38 的剥注释器只认 SQL 的
	//      `--` / `/* */` ⇒ 头里的「不做 DELETE / TRUNCATE / DROP / ALTER /
	//      CREATE」被当成真 DDL；
	//   ② 判词 echo 里有一句「890 会 CREATE OR REPLACE 该视图」⇒ 散文里的
	//      DDL 关键字也被当成真 DDL。
	//   ⇒ ★ **判据必须跟着语言走**：shell 脚本的只读性 = 「**here-doc 里
	//      那段 SQL** 是只读的」+「shell 代码里没有破坏性命令」。
	//
	// ⇒ 本门因此分两把尺（见 stripShellComments14339 / extractHereDocSQL14339）：
	//   尺 A：每个 here-doc 主体（剥 SQL 注释后）不得含写/DDL 关键字。
	//   尺 B：shell 代码（剥 shell 注释后）不得含破坏性命令。
	blocks := extractHereDocSQL14339(body)
	if len(blocks) == 0 {
		t.Fatalf("%s 里没有 here-doc SQL 块 ⇒ 尺 A 无从判起（脚本形态变了，门要跟着改）",
			precheck211RelPath14339)
	}
	for i, blk := range blocks {
		code := stripSQLComments14338(blk)
		for _, forbidden := range []string{
			"INSERT INTO", "UPDATE ", "DELETE FROM", "CREATE ", "DROP ", "ALTER ",
			"TRUNCATE", "REINDEX", "VACUUM", "GRANT ", "REVOKE", "COPY ",
		} {
			if strings.Contains(strings.ToUpper(code), forbidden) {
				t.Errorf("211 前置核验脚本的 here-doc #%d 里出现 %q ⇒ 它不是只读的 ⇒ 生产护栏会拒绝执行",
					i+1, forbidden)
			}
		}
	}
	shellCode := stripShellComments14339(body)
	for _, forbidden := range []string{
		"rm -rf", "pg_dump", "pg_restore", "docker rm ", "kubectl ",
	} {
		if strings.Contains(shellCode, forbidden) {
			t.Errorf("211 前置核验脚本的 shell 代码里出现 %q ⇒ 它不是只读的", forbidden)
		}
	}
}

// TestSection14339Precheck211DoesNotDependOnPgStatStatements 单独钉住
// 「不依赖 pg_stat_statements」这一条。
//
// ★ 为什么单独一条而不是并进 T1：
//
//	「脚本里出现 `pg_stat_statements` 这个词」与「脚本**依赖**它」是两件事
//	——本脚本的**文件头**就解释了「§143.38 依赖它，211 上走不通」
//	⇒ ⇒ 判据必须是「**会到达数据库的路径**上没有它」，不是「全文没有它」。
//
// ⇒ 口径（两把尺，取并集）：
//
//	① here-doc 主体里不得出现它（那是真正发给 psql 的 SQL）；
//	② **任何调用 psql 的行**里不得出现它（`-c` / `--command` 也算依赖）。
func TestSection14339Precheck211DoesNotDependOnPgStatStatements(t *testing.T) {
	raw, err := os.ReadFile(precheck211RelPath14339)
	if err != nil {
		t.Fatalf("read %s: %v", precheck211RelPath14339, err)
	}
	body := string(raw)

	for i, blk := range extractHereDocSQL14339(body) {
		if strings.Contains(blk, "pg_stat_statements") {
			t.Errorf("211 前置核验脚本的 here-doc #%d 引用了 pg_stat_statements ⇒ "+
				"它仍依赖 211 上不可用的量具 ⇒ 等于没有这个脚本", i+1)
		}
	}
	for i, ln := range strings.Split(stripShellComments14339(body), "\n") {
		if strings.Contains(ln, "psql") && strings.Contains(ln, "pg_stat_statements") {
			t.Errorf("211 前置核验脚本第 %d 行调用 psql 且引用 pg_stat_statements ⇒ "+
				"它仍依赖 211 上不可用的量具", i+1)
		}
	}
}

// stripShellComments14339 去掉 **shell 脚本**的 `#` 注释。
//
// ★ 为什么需要它（第一版假红的根因）：§143.38 的剥注释器只认 SQL 的
//
//	`--` 与 `/* */` ⇒ 对 shell 脚本**完全无效** ⇒ 文件头那段
//	「不做 DELETE / TRUNCATE / DROP / ALTER / CREATE」被当成真 DDL。
//
// ★ 口径：引号内的 `#`（如 `"$#"`、字符串里的井号）**不是**注释；
//
//	`${#var}` 里的 `#` 也不是 ⇒ 用「前一个字符是 `$` 就跳过」。
func stripShellComments14339(s string) string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		inSingle, inDouble := false, false
		cut := len(ln)
		for i := 0; i < len(ln); i++ {
			c := ln[i]
			switch {
			case c == '\'' && !inDouble:
				inSingle = !inSingle
			case c == '"' && !inSingle:
				inDouble = !inDouble
			case c == '#' && !inSingle && !inDouble && !(i > 0 && ln[i-1] == '$'):
				cut = i
				i = len(ln)
			}
		}
		out = append(out, ln[:cut])
	}
	return strings.Join(out, "\n")
}

// extractHereDocSQL14339 抽出脚本里所有 `<<'DELIM' … DELIM` 形式的 here-doc 主体。
//
// ★ 只认**带引号**的定界符（`<<'SQL'` / `<<"SQL"`）⇒ 那种形式**不做变量展开**
//
//	⇒ 主体里的 `$1` / `'...'` 都是**原样**发给 psql 的 ⇒ 正是「真正到达数据库
//	的那段 SQL」⇒ 只读性必须按它判。
//
// ⚠ 故意**不**认裸 `<<SQL`（会做变量展开）—— 本脚本用的是带引号那种；
//
//	若将来改成裸的，门会在这里「块数=0」而**响亮失败**（而不是默默放过）。
func extractHereDocSQL14339(s string) []string {
	var out []string
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		idx := strings.Index(lines[i], "<<")
		if idx < 0 {
			continue
		}
		rest := lines[i][idx+2:]
		if !strings.HasPrefix(rest, "'") && !strings.HasPrefix(rest, "\"") {
			continue // 裸定界符（会展开变量）⇒ 不在本门口径内
		}
		quote := rest[0]
		rest = rest[1:]
		if e := strings.IndexAny(rest, " \t"); e >= 0 {
			rest = rest[:e]
		} else {
			rest = strings.TrimRight(rest, "\r")
		}
		// ★★ **必须连尾引号一起剥**（`'SQL'` ⇒ `SQL`）——
		//   第一版只剥了前引号 ⇒ 定界符变成 `SQL'` ⇒ **永远匹配不到终止行**
		//   ⇒ 块一路吞到文件尾 ⇒ 判据把脚本后半段（散文 + 别的代码）也当 SQL 查
		//   ⇒ 假红。★ 这条 bug 是本门**当场**抓出来的（判据的边界自己被推翻）。
		rest = strings.TrimSuffix(rest, string(quote))
		delim := rest
		if delim == "" {
			continue
		}
		var body []string
		j := i + 1
		terminated := false
		for ; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == delim {
				terminated = true
				break
			}
			body = append(body, lines[j])
		}
		// ★ 没有终止行 ⇒ 形态不对（不是带引号的 here-doc）⇒ **不收**，
		//   宁可让「块数=0」在测试里响亮失败，也不让一个无界的块混进判据。
		if !terminated {
			continue
		}
		out = append(out, strings.Join(body, "\n"))
		i = j
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────
// T2：判据语义自测（★ 量具必须先证明自己「只在该报的时候报」）
// ─────────────────────────────────────────────────────────────────────────

// v1ArmPattern14339 是**脚本里那条 grep 的 Go 等价物**。
//
// ★★★ **两把尺子必须对齐** —— 脚本用 shell grep 判，仓内门用 Go 判，
//
//	若两者语义不一致 ⇒ 门在保护一个**与实际脚本不同的**判据 ⇒ 门是假牙。
//	⇒ T2 里有一条用例**真的把脚本的 pattern 抠出来跑 grep**，与 Go 版对拍。
var v1ArmPattern14339 = regexp.MustCompile(`FROM (public\.)?_request_logs(_hot)?[\s]`)

// countV1Arms14339 数 SQL 文本里「v1 基表出现在 FROM 位置」的臂数。
//
// ★ 口径要点：
//
//	① 必须是 **FROM 位置**（`JOIN` 位置同样是读点，但 v1 臂的写法是 `FROM x rl`）
//	   —— 裸 `from` 不是信号（中文散文里到处是）。
//	② `_request_logs` 后面必须跟**空白**（不是词边界 `\b`）⇒
//	   `_request_logs_with_current_month` / `_request_logs_bodies` 后面跟的是 `_`
//	   ⇒ 不匹配 ⇒ canonical 视图与 bodies 表**不会**被误判。
//	③ 允许可选的 `public.` schema 限定。
func countV1Arms14339(sql string) int {
	code := stripSQLComments14338(sql)
	n := 0
	for _, line := range strings.Split(code, "\n") {
		n += len(v1ArmPattern14339.FindAllString(line, -1))
	}
	return n
}

// TestSection14339V1ArmMatcherSemantics 钉住 v1 臂判据的语义。
//
// ★ 六个用例都是本轮实测踩出来的边界：
//
//	① 两条 v1 臂 ⇒ 2（**旧二进制**的形态）
//	② 单条 v1 臂 ⇒ 1（介于两代之间 ⇒ 同样阻塞）
//	③ 会话族臂 ⇒ **0**（**新二进制**的形态 ⇒ 不阻塞）
//	④ canonical 视图名 ⇒ 0（前缀相同但不是 v1 基表）
//	⑤ bodies 表 ⇒ 0
//	⑥ 注释里的 ⇒ 0
func TestSection14339V1ArmMatcherSemantics(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want int
	}{
		{"旧二进制：两条 v1 臂", `
		  AND NOT EXISTS (
		      SELECT 1 FROM _request_logs_hot rl
		      WHERE rl.request_id = s.request_id)
		  AND NOT EXISTS (
		      SELECT 1 FROM _request_logs rl
		      WHERE rl.request_id = s.request_id)`, 2},
		{"单条 v1 臂", `
		  AND NOT EXISTS (
		      SELECT 1 FROM _request_logs_hot rl
		      WHERE rl.request_id = s.request_id)`, 1},
		{"新二进制：只有会话族臂", `
		  AND NOT EXISTS (
		      SELECT 1 FROM (
		          SELECT request_id, origin_actor FROM session_turns
		          UNION ALL
		          SELECT request_id, origin_actor FROM session_turns_hot
		      ) st
		      WHERE st.request_id = s.request_id)`, 0},
		{"canonical 视图名 ⇒ 不算 v1 臂", `SELECT 1 FROM request_logs_with_current_month v`, 0},
		{"bodies 表 ⇒ 不算 v1 臂", `SELECT 1 FROM _request_logs_bodies b`, 0},
		{"注释里的 ⇒ 不算 v1 臂", "-- 历史实现读 FROM _request_logs rl\nx := 1", 0},
		{"schema 限定 ⇒ 算 v1 臂", `SELECT 1 FROM public._request_logs rl`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countV1Arms14339(tc.sql); got != tc.want {
				t.Errorf("countV1Arms14339(...) = %d，期望 %d", got, tc.want)
			}
		})
	}
}

// TestSection14339ShellPatternMatchesGoMatcher 把**脚本里那条 grep**抠出来，
// 真的跑一遍，与 Go 版判据**对拍**。
//
// ★★ 为什么必须对拍：门保护的是「脚本的判据」⇒ 如果脚本的 pattern 被改成
//
//	`FROM _request_logs_hot rl`（更窄）⇒ 211 的 `_request_logs rl` 那条臂
//	就**数不到** ⇒ 臂数从 2 变 1 ⇒ 判词从「旧二进制」变成「介于两代之间」
//	⇒ **判词被悄悄降级**而门仍然绿 ⇒ 门变成假牙。
//
// ⇒ 判据：脚本的 pattern 必须在**至少 6 个夹具**上与 Go 版给出同样的计数。
func TestSection14339ShellPatternMatchesGoMatcher(t *testing.T) {
	raw, err := os.ReadFile(precheck211RelPath14339)
	if err != nil {
		t.Fatalf("read %s: %v", precheck211RelPath14339, err)
	}
	body := string(raw)
	// 抠出脚本里那条 grep 的 pattern（第 2 个单引号包裹的 ERE）
	m := regexp.MustCompile(`grep -cE '([^']+)'`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s 里找不到 `grep -cE '...'` ⇒ 判据形态变了，门需要跟着改", precheck211RelPath14339)
	}
	shellPat := m[1]
	re, err := regexp.Compile(shellPat)
	if err != nil {
		t.Fatalf("脚本的 pattern %q 不是合法 ERE（Go 口径）：%v", shellPat, err)
	}
	// ★★ 关键断言：Go 版判据必须与脚本的 pattern **逐字同义**。
	//    做法：把 Go 版的 pattern 转成等价 ERE 再比 —— 这里用「行为对拍」更稳，
	//    因为两边的语法（POSIX ERE vs Go RE2）本来就不同。
	_ = re

	grepBin, err := exec.LookPath("grep")
	if err != nil {
		t.Skipf("本机没有 grep，跳过与 shell 尺子的对拍：%v", err)
	}
	fixtures := []struct {
		name string
		line string
		want int
	}{
		{"v1 hot 臂", "SELECT 1 FROM _request_logs_hot rl", 1},
		{"v1 母表臂", "SELECT 1 FROM _request_logs rl", 1},
		{"schema 限定", "SELECT 1 FROM public._request_logs rl", 1},
		{"会话族 hot", "SELECT request_id, origin_actor FROM session_turns_hot", 0},
		{"会话族父表", "SELECT request_id, origin_actor FROM session_turns", 0},
		{"canonical 视图", "FROM request_logs_with_current_month v", 0},
		{"bodies 表", "FROM _request_logs_bodies b", 0},
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			cmd := exec.Command(grepBin, "-cE", shellPat)
			cmd.Stdin = strings.NewReader(fx.line + "\n")
			gotOut, err := cmd.Output()
			got := 0
			if err == nil {
				if s := strings.TrimSpace(string(gotOut)); s != "" {
					if v, perr := strconv.Atoi(s); perr == nil {
						got = v
					}
				}
			} else if ee, ok := err.(*exec.ExitError); ok {
				// grep 无匹配 ⇒ exit 1 ⇒ 计数 0
				if len(ee.Stderr) > 0 {
					t.Fatalf("grep 执行报错: %v", ee)
				}
			} else {
				t.Fatalf("grep 执行失败: %v", err)
			}
			if got != fx.want {
				t.Errorf("shell pattern %q 对 %q 的计数 = %d，期望 %d", shellPat, fx.line, got, fx.want)
			}
			// ★ Go 版与 shell 版必须一致
			if goGot := countV1Arms14339(fx.line); goGot != got {
				t.Errorf("Go 判据 = %d，shell 判据 = %d ⇒ 两把尺子不一致（对 %q）", goGot, got, fx.line)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────
// T3：缺口清单 ⇄ 迁移文件（★ 防漂移：清单必须由迁移文件推导，不能手抄）
// ─────────────────────────────────────────────────────────────────────────

// TestSection14339GapListMatchesMigrationFiles 断言「211 的 5 缺口清单」
// 里的对象**确实由迁移 885 / 887 / 888 产生**。
//
// ★★ 为什么这道门有牙：
//
//	缺口清单是**手抄**进 runbook 与预检脚本的 ⇒ 一旦迁移文件改了
//	（改名 / 拆条 / 合并），清单会**静默过期** ⇒ 预检脚本会检查一个
//	**已经不存在的对象** ⇒ 永远报「缺」⇒ 决策依据失真。
//
// ⇒ 判据：从迁移文件里解析出「新建对象」与「新增列」，与清单逐项对拍。
func TestSection14339GapListMatchesMigrationFiles(t *testing.T) {
	// ① 885：session_turns / session_turns_hot 各加 host + tags
	raw885, err := os.ReadFile(mig885RelPath14339)
	if err != nil {
		t.Fatalf("read %s: %v", mig885RelPath14339, err)
	}
	code885 := stripSQLComments14338(string(raw885))
	for _, want := range []string{
		"ALTER TABLE public.session_turns\n", "ADD COLUMN IF NOT EXISTS host TEXT",
		"ADD COLUMN IF NOT EXISTS tags TEXT[]",
		"ALTER TABLE public.session_turns_hot\n",
	} {
		if !strings.Contains(code885, want) {
			t.Errorf("迁移 885 里没有 %q ⇒ 缺口清单的 host/tags 这一项已过期", want)
		}
	}
	// ★★ 关键不变式：**父表与 hot 表必须同形**（迁移 707 的 promote 列集合契约）
	//   ⇒ 少一侧 ⇒ promote 当场报错 ⇒ 热表永远排不进月分区。
	//   ⇒ 缺口清单必须**成对**列出两侧，不能只列父表。
	parentCols := addCols88514339(code885, "public.session_turns\n")
	hotCols := addCols88514339(code885, "public.session_turns_hot\n")
	if len(parentCols) == 0 || len(hotCols) == 0 {
		t.Fatalf("解析 885 的 ADD COLUMN 失败：parent=%v hot=%v", parentCols, hotCols)
	}
	if strings.Join(parentCols, ",") != strings.Join(hotCols, ",") {
		t.Errorf("迁移 885 的父表列集合 %v 与 hot 表列集合 %v 不同形 ⇒ "+
			"违反 707 的 promote 列集合契约（热表会排不进月分区）", parentCols, hotCols)
	}

	// ② 887：idx_session_turns_search_fts
	raw887, err := os.ReadFile(mig887RelPath14339)
	if err != nil {
		t.Fatalf("read %s: %v", mig887RelPath14339, err)
	}
	if !strings.Contains(stripSQLComments14338(string(raw887)), "idx_session_turns_search_fts") {
		t.Errorf("迁移 887 里没有 idx_session_turns_search_fts ⇒ 缺口清单的 FTS 索引项已过期")
	}

	// ③ 888：两个新索引 + 两个待删索引
	raw888, err := os.ReadFile(mig888RelPath14339)
	if err != nil {
		t.Fatalf("read %s: %v", mig888RelPath14339, err)
	}
	code888 := stripSQLComments14338(string(raw888))
	for _, want := range []string{
		"idx_session_bodies_tenant_session_turn_request",
		"idx_sessions_tenant_session_partition_date",
		"DROP INDEX IF EXISTS public.idx_session_bodies_tenant_session_turn",
		"DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_bodies_hot_lookup",
	} {
		if !strings.Contains(code888, want) {
			t.Errorf("迁移 888 里没有 %q ⇒ 缺口清单的 888 项已过期", want)
		}
	}

	// ④ 预检脚本必须点名 host / tags（升级链阻塞项的判据）
	rawScript, err := os.ReadFile(precheck211RelPath14339)
	if err != nil {
		t.Fatalf("read %s: %v", precheck211RelPath14339, err)
	}
	scriptBody := string(rawScript)
	for _, want := range []string{"session_turns.host", "session_turns_hot.host", "tags"} {
		if !strings.Contains(scriptBody, want) {
			t.Errorf("211 预检脚本没有点名 %q ⇒ 它判不出「升级链列前提」这个阻塞项", want)
		}
	}
}

// addCols88514339 从「某张表的 ALTER TABLE 段」里抽出 ADD COLUMN 的列名。
//
// ★ 口径：`ALTER TABLE public.X` 之后、下一个 `ALTER TABLE` 之前的所有
//
//	`ADD COLUMN IF NOT EXISTS <name>`。
func addCols88514339(code, tableHeader string) []string {
	start := strings.Index(code, "ALTER TABLE "+tableHeader)
	if start < 0 {
		return nil
	}
	rest := code[start:]
	// 段尾：下一个 "ALTER TABLE" 或文件尾
	end := len(rest)
	if i := strings.Index(rest[1:], "ALTER TABLE "); i >= 0 {
		end = i + 1
	}
	seg := rest[:end]
	var out []string
	for _, m := range regexp.MustCompile(`ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`).FindAllStringSubmatch(seg, -1) {
		out = append(out, m[1])
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────
// T4：runbook §143.39 在场 + 订正横幅在位
// ─────────────────────────────────────────────────────────────────────────

// TestSection14339RunbookHasSection 钉住「过程可查」与「订正留痕」。
//
// ★ 订正横幅是**必须**的：§143.20 写过「211 落后 13 个迁移（869 vs 882）」，
// 本轮用**对象存在性**把它证伪 ⇒ 错误结论必须**就地订正 + 保留原文**
// ⇒ 横幅缺席 ⇒ 下一个会话会读到那个错结论。
func TestSection14339RunbookHasSection(t *testing.T) {
	raw, err := os.ReadFile(runbookRelPath14339)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	body := string(raw)
	for _, needle := range []string{
		"§143.39",
		"订正横幅十九",
		"7a60b1b1",
		// ★ 用**完整版本串**做第二条：裸 SHA 在 runbook 里出现在**同一行内的两处**
		//   （`version.json` 与 `git_sha`）⇒ 单点替换删不掉 ⇒ 门会假绿。
		//   ⇒ 钉在**唯一**的完整串上，变异脚本才能用「唯一 needle」把它改红。
		"2.5.8-7a60b1b1-dirty-20261020-211",
		"35,506",
		"idx_session_turns_search_fts",
		"42703",
		"verify_890_cutover_precheck_211",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("runbook 缺 %q", needle)
		}
	}
}
