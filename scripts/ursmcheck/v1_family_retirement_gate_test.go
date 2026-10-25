//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// v1_family_retirement_gate_test.go —— v1「request_logs」族退役状态检查器的**行为门**。
//
// # 这道门补的是哪块缺口
//
// 本仓的长期目标之一是「把 request_logs 及其相关表退役、全部切到 session_turns 会话族」。
// 仓内已有一道门 cmd/tools/sql_source_indirection_audit/manifest_test.go，
// 但它**只扫仓内 SQL 文本** ⇒ ★★ 它查不到「生产/开发库里的 v1 表被复活」。
// ⇒ 本门守的是 `scripts/audit/check-v1-family-retirement.sh`
//   （对任意 PG DSN 跑一次 ⇒ 报告「v1 族退役状态」的**只读**检查器）。
//
// # ★ 为什么必须做成【行为层】门，而不是「关键字在不在」的形状门
//
// 本仓反复吃过这个亏：**门绿 ≠ 事实成立**。
// 如果这道门只断言「脚本里出现过 `v1-revived` 这个字符串」，那么
// ① 把判定逻辑整个写反（三种输入都返回同一个答案）⇒ 门**依然绿**。
//
// ⇒ 所以本门的核心是 **真的跑脚本的 classify 模式**，喂三份**合成清单**，
//   然后断言 **判定词与退出码**分别是：
//
//	全退役（v1 表 0 个 + 接管视图读会话族） ⇒ retired   / 退出码 0
//	v1 表复活                             ⇒ v1-revived / 退出码 3
//	半退役（v1 表没了但接管视图不读会话族）   ⇒ degraded  / 退出码 2
//
// ★ 这就是脚本「判定逻辑与查库分开」的意义：**门不需要真库**（秒级、可进 CI）。
//
// # ★ 合成清单必须能抓住「同族变体」
//
// 仓内已登记的教训（§9.226.3）：**「精确相等 + 前缀兜底」两个真相源并存**时，
// 任何删掉其中一个的变异都测不出来（行为被两个真相源同时决定）。
// ⇒ 本门刻意有一份**只含变体名**（`request_logs_v2_archive`，不在任何枚举名单里）
//   的清单，并断言它仍被判成 v1-revived ⇒ 钉住「判定按**族形态**而不是按枚举名单」。
//
// # ★ 门必须自带阳性对照
//
// TestV1FamilyRetirementVerdictTracksInput：把合成清单里的 v1 表名改掉
// ⇒ 判定必须**跟着变** ⇒ 证明脚本不是恒返回同一个答案（否则上面三条都是恒绿门）。
//
// # ★ 诚实边界（本门**没有**做的事）
//
// 本门只用**合成清单**测 classify 模式，**不连任何库**（含本地库）。
// ⇒ 「probe 模式在真库上也能正确判定」这件事，**不由本门证明**
//   （它是**脚本作者**用一次性本地库验的，不在本门范围内）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// v1RetireRelPath 是被测脚本**在仓内的相对路径**（正斜杠）。
//
// ★ 这条常量同时被「门指向的文件」与「脚本自述的用法」两处断言引用 ⇒
//
//	门指错文件 ⇒ 红；脚本被挪走 ⇒ 红。
const v1RetireRelPath = "scripts/audit/check-v1-family-retirement.sh"

// v1RetireRepoRoot 返回仓根。
//
// ★ 必须用 runtime.Caller：写死相对路径（"../.."）或依赖 os.Getwd()，
//
//	在测试的工作目录变化时会扫不到文件 ⇒ 静态守卫变成恒绿门。
func v1RetireRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	// file = <repo>/scripts/ursmcheck/<this>.go
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// v1RetireScriptPath 返回被测脚本的绝对路径。
func v1RetireScriptPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(v1RetireRepoRoot(t), filepath.FromSlash(v1RetireRelPath))
}

// v1RetireRead 读被测脚本文本。
func v1RetireRead(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(v1RetireScriptPath(t))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", v1RetireRelPath, err)
	}
	return string(b)
}

// v1RetireStripComments 去掉**注释**，只留「可执行部分」。
//
// ★ 为什么必须剥注释再查写语句：脚本的头部注释里**要**解释
//
//	「本脚本绝不写库」⇒ 注释里出现那些词是**应该的**。
//	⇒ 判据必须只看**代码**。
//
// ★ 剥法：逐行扫，`#` 只有在「行首」或「前面是空白」时才是注释起点
//
//	（bash 的 `#` 只有在词首才是注释；awk 的 `#` 任意位置都是注释 —— 两者取并集，
//	对本门来说够用：宁可多剥一点，也不要把注释里的词当成代码）。
//	★ 引号内的 `#` 不算注释（脚本里有 `'#meta\t…'` 这种字面量）。
func v1RetireStripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		var inSingle, inDouble bool
		cut := len(line)
		for i := 0; i < len(line); i++ {
			c := line[i]
			switch {
			case inSingle:
				if c == '\'' {
					inSingle = false
				}
			case inDouble:
				if c == '"' {
					inDouble = false
				}
			case c == '\'':
				inSingle = true
			case c == '"':
				inDouble = true
			case c == '#':
				if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
					cut = i
				}
				i = len(line)
			}
		}
		b.WriteString(line[:cut])
		b.WriteString("\n")
	}
	return b.String()
}

// v1RetireRun 跑一次脚本的 classify 模式，返回 stdout / stderr / 退出码。
//
// ★ 走 stdin（清单）⇒ 与脚本的「classify 从 stdin 读清单」契约一致。
func v1RetireRun(t *testing.T, stdin string, extraEnv ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", v1RetireScriptPath(t), "classify")
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), extraEnv...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("跑脚本失败（不是退出码问题）：%v\nstderr:\n%s", err, errBuf.String())
		}
		code = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// v1RetireMeta 从 stdout 的 TSV 里取一个 `#meta` 键的值。
func v1RetireMeta(t *testing.T, stdout, key string) string {
	t.Helper()
	for _, ln := range strings.Split(stdout, "\n") {
		f := strings.Split(ln, "\t")
		if len(f) >= 3 && f[0] == "#meta" && f[1] == key {
			return f[2]
		}
	}
	t.Fatalf("stdout 的 TSV 里没有 #meta\\t%s\\t… 这一行：\n%s", key, stdout)
	return ""
}

// ─────────────────────────── 合成清单（★ 门不连任何库） ───────────────────────────
//
// 清单格式见脚本头部：三列 `kind <TAB> name <TAB> body`。
// ★ body 是视图定义原文（表/其它对象留空）。

// v1RetireInvRetired：全退役形态 —— 接管视图读会话族两臂，无任何 v1 对象。
const v1RetireInvRetired = "view\trequest_logs_with_current_month\t" +
	" SELECT id, request_id FROM public.session_turns_hot t " +
	"LEFT JOIN public.session_turn_details_hot d ON d.request_id = t.request_id " +
	"UNION ALL SELECT id, request_id FROM public.session_turns t\n" +
	"table\tsession_turns\t\n" +
	"table\tsession_turns_hot\t\n"

// v1RetireInvV1Revived：v1 基表被复活（`_request_logs` 是 860 之后那一代物理名）。
const v1RetireInvV1Revived = v1RetireInvRetired + "table\t_request_logs\t\n"

// v1RetireInvDegraded：半退役 —— v1 表没了，但接管视图不读会话族。
const v1RetireInvDegraded = "view\trequest_logs_with_current_month\t" +
	" SELECT id FROM public.some_unrelated_table\n"

// v1RetireInvVariantOnly：**只含一个族变体名**（不在任何枚举名单里）。
//
// ★ 这份清单是「判定按族形态而不是按枚举名单」的阳性对照：
//
//	若判定退化成「认精确表名列表」⇒ 这份清单会被判成 degraded ⇒ 门红。
const v1RetireInvVariantOnly = "table\trequest_logs_v2_archive\t\n"

// v1RetireInvTakeoverMissing：接管视图缺失（口径上 ⇒ degraded）。
const v1RetireInvTakeoverMissing = "table\tsession_turns\t\n"

// v1RetireInvTakeoverStillV1：接管视图的体里还引用 v1 关系（⇒ v1-revived，不是 degraded）。
const v1RetireInvTakeoverStillV1 = "view\trequest_logs_with_current_month\t" +
	" SELECT id FROM public.request_logs_hot r UNION ALL SELECT id FROM public.request_logs r\n"

// ─────────────────────────── 形状层 ───────────────────────────

// TestV1FamilyRetirementScriptExistsAndParses —— 文件在、有可执行位、bash 能解析。
//
// ★ `bash -n` 是最便宜的检查：能抓住绝大多数自伤（引号不闭合、heredoc 不完整）。
//
// ★★ 本仓实测踩过的坑就在这一层：脚本里的 awk 程序曾被塞进
//
//	`AWK_PROG="$(cat <<'EOF' … EOF)"` ⇒ ★ bash 在 `"$( … )"` 里会**数 heredoc body 里的双引号**
//	⇒ body 里只要有奇数个双引号（例如 `gsub(/[",]/, "", s)`）整个脚本就解析失败。
//
// ⇒ 这条断言就是为了把那种自伤挡住。
func TestV1FamilyRetirementScriptExistsAndParses(t *testing.T) {
	path := v1RetireScriptPath(t)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s 不存在：%v", v1RetireRelPath, err)
	}
	if st.IsDir() {
		t.Fatalf("%s 是目录，不是脚本", v1RetireRelPath)
	}
	if st.Mode().Perm()&0111 == 0 {
		t.Errorf("%s 没有可执行位（%v）⇒ 用 `bash %s` 能跑，但直接执行会失败",
			v1RetireRelPath, st.Mode().Perm(), v1RetireRelPath)
	}
	out, err := exec.Command("bash", "-n", path).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n 失败（%s）:\n%s", v1RetireRelPath, out)
	}
}

// TestV1FamilyRetirementGateTargetsRepoScript —— ★ 门**没有指错文件**。
func TestV1FamilyRetirementGateTargetsRepoScript(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位门自身")
	}
	self, err := os.ReadFile(thisFile)
	if err != nil {
		t.Fatalf("读门自身失败：%v", err)
	}
	if !strings.Contains(string(self), v1RetireRelPath) {
		t.Errorf("门自身源码里没有出现 %q ⇒ 这道门可能已经指错文件了", v1RetireRelPath)
	}
	src := v1RetireRead(t)
	if !strings.Contains(src, v1RetireRelPath) {
		t.Errorf("脚本 %s 里没有出现自己的仓内路径 %q ⇒ 用法注释与实际位置对不上",
			v1RetireRelPath, v1RetireRelPath)
	}
	rel, err := filepath.Rel(v1RetireRepoRoot(t), v1RetireScriptPath(t))
	if err != nil {
		t.Fatalf("算相对路径失败：%v", err)
	}
	if got := filepath.ToSlash(rel); got != v1RetireRelPath {
		t.Errorf("门解析到的脚本是 %q，与期望的 %q 不一致 ⇒ 门指错文件了", got, v1RetireRelPath)
	}
}

// TestV1FamilyRetirementSupportsBothModes —— 脚本必须同时提供 probe 与 classify。
//
// ★ classify 是**门能用的那一半**（不需要真库）；probe 是**运维用的那一半**（查库）。
// ⇒ 少任何一个 ⇒ 这道门/这个工具的契约就破了。
func TestV1FamilyRetirementSupportsBothModes(t *testing.T) {
	src := v1RetireRead(t)
	for _, needle := range []string{"probe", "classify", "LLM_GATEWAY_DATABASE_URL"} {
		if !strings.Contains(src, needle) {
			t.Errorf("脚本里没有 %q ⇒ 契约缺了一半（探库 / 判定的双模式）", needle)
		}
	}
	// ★ 未知模式必须响亮失败（退出码 64），不能默默当 classify 跑。
	cmd := exec.Command("bash", v1RetireScriptPath(t), "no-such-mode")
	cmd.Stdin = strings.NewReader("")
	out, _ := cmd.CombinedOutput()
	if cmd.ProcessState == nil {
		t.Fatal("脚本没跑起来")
	}
	if code := cmd.ProcessState.ExitCode(); code != 64 {
		t.Errorf("未知模式的退出码应为 64（用法错误），实际 %d；输出：\n%s", code, out)
	}
}

// TestV1FamilyRetirementScriptHasNoWriteStatements —— ★ 脚本**不含写语句**。
//
// ★ 这是「只读」这条硬约束的门：检查器会被指向生产库，
//
//	一旦它带写语句 ⇒ 「跑一次只读检查」这件事就不再成立。
//
// ★ 判据：先剥注释（★ 注释里**允许**出现这些词：脚本要解释「本脚本绝不写库」），
//
//	再在**剩下的代码**里查写语句关键字。
func TestV1FamilyRetirementScriptHasNoWriteStatements(t *testing.T) {
	code := v1RetireStripComments(v1RetireRead(t))
	for _, kw := range []string{
		"CREATE", "DROP", "ALTER", "TRUNCATE", "DELETE", "INSERT", "UPDATE", "GRANT", "REVOKE",
	} {
		re := regexp.MustCompile(`(?i)\b` + kw + `\b`)
		for i, ln := range strings.Split(code, "\n") {
			if re.MatchString(ln) {
				t.Errorf("第 %d 行（剥注释后）出现写语句关键字 %q ⇒ "+
					"检查器必须是只读的：\n  %s", i+1, kw, strings.TrimSpace(ln))
			}
		}
	}
}

// ─────────────────────────── 行为层：★ 真跑 classify ───────────────────────────

// v1RetireCase 是一份「合成清单 ⇒ 期望判定/期望退出码」的用例。
type v1RetireCase struct {
	name  string
	inv   string
	verdt string
	code  int
}

// TestV1FamilyRetirementClassifiesSyntheticInventories —— ★★ 本门最硬的一条。
//
// ★ 它**真的跑脚本**（不是 grep 源码），喂合成清单，断言判定与退出码。
//
// ★ 三档判定的语义（与脚本头部注释必须一致）：
//
//	retired    v1 基表 0 个 + v1 臂视图 0 个 + 接管视图确实读会话族  ⇒ 0
//	degraded   v1 表已退役，但接管视图没读会话族（或缺失）          ⇒ 2
//	v1-revived 有 v1 基表或 v1 臂视图存在                            ⇒ 3
func TestV1FamilyRetirementClassifiesSyntheticInventories(t *testing.T) {
	cases := []v1RetireCase{
		{"全退役", v1RetireInvRetired, "retired", 0},
		{"v1 表复活", v1RetireInvV1Revived, "v1-revived", 3},
		{"半退役（接管视图不读会话族）", v1RetireInvDegraded, "degraded", 2},
		{"接管视图缺失", v1RetireInvTakeoverMissing, "degraded", 2},
		{"接管视图体里还有 v1 臂", v1RetireInvTakeoverStillV1, "v1-revived", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := v1RetireRun(t, c.inv)
			got := v1RetireMeta(t, stdout, "verdict")
			if got != c.verdt {
				t.Errorf("判定应为 %q，实际 %q（退出码 %d）\nstdout:\n%s\nstderr:\n%s",
					c.verdt, got, code, stdout, stderr)
			}
			if code != c.code {
				t.Errorf("退出码应为 %d，实际 %d\nstdout:\n%s", c.code, code, stdout)
			}
			// ★ TSV 里的 exit_code 必须与进程退出码一致（机器读的那份不能撒谎）。
			if gotCode := v1RetireMeta(t, stdout, "exit_code"); gotCode != strconv.Itoa(code) {
				t.Errorf("TSV 里的 exit_code=%q 与进程退出码 %d 不一致", gotCode, code)
			}
		})
	}
}

// TestV1FamilyRetirementDetectsFamilyVariants —— ★ 同族变体也必须被判成 v1-revived。
//
// ★ 这条钉住的是「判定按**族形态**，不按枚举名单」：
//
//	`request_logs_v2_archive` 不在仓内任何 v1 表名清单里
//	（迁移 890 的非空闸名单 / cmd/tools/sql_source_indirection_audit 的 v1Tables 都没有它），
//	但它**属于 v1 族** ⇒ 若判定退化成精确匹配 ⇒ 这份清单会被判成 degraded ⇒ 门红。
//
// ★ 这也是「把 v1 表名集合删掉一项 ⇒ 门必须红」那组变异的着力点。
func TestV1FamilyRetirementDetectsFamilyVariants(t *testing.T) {
	for _, variant := range []string{
		"request_logs_v2_archive",  // 不在任何枚举名单里的族变体
		"_request_logs_bodies_hot", // 带下划线那一代的 bodies_hot
		"request_logs_hot",         // 旧物理名 + hot
		"_request_logs_archive",    // 带下划线那一代的 archive
	} {
		t.Run(variant, func(t *testing.T) {
			inv := "table\t" + variant + "\n"
			stdout, stderr, code := v1RetireRun(t, inv)
			got := v1RetireMeta(t, stdout, "verdict")
			if got != "v1-revived" {
				t.Errorf("v1 族变体 %s 应被判成 v1-revived，实际 %q（退出码 %d）\nstdout:\n%s\nstderr:\n%s",
					variant, got, code, stdout, stderr)
			}
			if code != 3 {
				t.Errorf("v1 族变体 %s 的退出码应为 3，实际 %d", variant, code)
			}
			if n := v1RetireMeta(t, stdout, "v1_base_tables"); n != "1" {
				t.Errorf("v1 族变体 %s 应被计成 1 张 v1 基表，实际 %q", variant, n)
			}
		})
	}
}

// TestV1FamilyRetirementVerdictTracksInput —— ★★ 阳性对照：判定必须**跟着输入变**。
//
// ★ 若脚本对任何清单都返回同一个答案，上面那些断言就全是恒绿门。
// ⇒ 这里把清单里的 v1 表名改成**非族名**（`sessions_audit_log`），
//
//	判定必须从 v1-revived 变回 retired（接管视图仍读会话族）。
func TestV1FamilyRetirementVerdictTracksInput(t *testing.T) {
	before, _, beforeCode := v1RetireRun(t, v1RetireInvV1Revived)
	after, _, afterCode := v1RetireRun(t,
		strings.Replace(v1RetireInvV1Revived, "_request_logs", "sessions_audit_log", 1))
	if v1RetireMeta(t, before, "verdict") != "v1-revived" || beforeCode != 3 {
		t.Fatalf("改名前的基线不对（应为 v1-revived / 3）：%s", before)
	}
	if got := v1RetireMeta(t, after, "verdict"); got != "retired" {
		t.Errorf("把 v1 表名改成非族名后，判定应变成 retired，实际 %q\nstdout:\n%s",
			got, after)
	}
	if afterCode != 0 {
		t.Errorf("把 v1 表名改成非族名后，退出码应变成 0，实际 %d", afterCode)
	}
}

// ─────────────────────────── ★★ 不回显 DSN / 口令 ───────────────────────────

// TestV1FamilyRetirementNeverEchoesDSN —— ★★ 硬要求：脚本**不得**打印 DSN 或口令。
//
// 判据（两条，都做）：
//
//	① **行为层**（更稳）：跑 classify 时给它一个带**哨兵口令**的环境变量，
//	   断言 stdout + stderr 里**没有**那个哨兵串。
//	② **形状层**：任何**打印语句**（echo/printf）都不得**展开** DSN 变量。
//	   ⇒ 只写变量名（如「请设 LLM_GATEWAY_DATABASE_URL」）是允许的；
//	   写 `${LLM_GATEWAY_DATABASE_URL}` 才是回显 ⇒ 判据认的是**展开形态**。
//
// ★ 为什么这条是硬要求：检查器会被指向生产库，
//
//	而生产 DSN 里带着口令 ⇒ 一旦回显，口令就进了 CI 日志 / 终端历史。
func TestV1FamilyRetirementNeverEchoesDSN(t *testing.T) {
	const sentinel = "SENTINEL_PW_9f3a2b"
	dsn := "postgres://audit:" + sentinel + "@127.0.0.1:5432/llm_gateway?sslmode=disable"

	stdout, stderr, _ := v1RetireRun(t, v1RetireInvRetired,
		"LLM_GATEWAY_DATABASE_URL="+dsn)
	if strings.Contains(stdout, sentinel) {
		t.Errorf("stdout 里出现了哨兵口令 ⇒ 脚本回显了口令\nstdout:\n%s", stdout)
	}
	if strings.Contains(stderr, sentinel) {
		t.Errorf("stderr 里出现了哨兵口令 ⇒ 脚本回显了口令\nstderr:\n%s", stderr)
	}
	if strings.Contains(stdout+stderr, dsn) {
		t.Errorf("输出里出现了完整 DSN ⇒ 脚本回显了 DSN\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	// ★ 形状层：打印语句不得展开 DSN 变量。
	code := v1RetireStripComments(v1RetireRead(t))
	dyn := regexp.MustCompile(`\$\{?LLM_GATEWAY_DATABASE_URL`)
	for i, ln := range strings.Split(code, "\n") {
		if !dyn.MatchString(ln) {
			continue
		}
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "echo ") || strings.HasPrefix(trimmed, "printf ") {
			t.Errorf("第 %d 行在打印语句里展开了 DSN 变量 ⇒ 口令会进日志：%s", i+1, trimmed)
		}
	}
}

// TestV1FamilyRetirementProbeDoesNotEchoPassword —— ★ probe 模式（探库那条腿）的口令不回显。
//
// ★ 用一个**不可达**的 DSN 制造连接失败（秒级、无外部副作用），
//
//	断言报错里没有哨兵口令 ⇒ 证明「scrub 抹口令」这条约束在探库路径上真的生效。
//
// ★ 没有 psql 的机器上跳过（★ 这是环境依赖，不是产品缺陷）。
func TestV1FamilyRetirementProbeDoesNotEchoPassword(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 下跳过（★ 脚本是 bash）")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("PATH 里没有 psql ⇒ 跳过（★ 探库路径无法在本机验证）")
	}
	const sentinel = "SENTINEL_PW_probe_41c7"
	dsn := "postgres://audit:" + sentinel + "@127.0.0.1:1/llm_gateway?sslmode=disable&connect_timeout=2"

	cmd := exec.Command("bash", v1RetireScriptPath(t), "probe", dsn)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if err == nil {
		// ★ 不可达端口几乎不可能连上；连上了也不算失败，但下面照样查哨兵。
		t.Logf("probe 竟然成功了（输出）：\n%s", out)
	}
	if strings.Contains(string(out), sentinel) {
		t.Errorf("probe 的输出里出现了哨兵口令 ⇒ 探库路径回显了口令：\n%s", out)
	}
	if strings.Contains(string(out), dsn) {
		t.Errorf("probe 的输出里出现了完整 DSN ⇒ 探库路径回显了 DSN：\n%s", out)
	}
}

// ─────────────────────── ★★★ 库身份自报（R157 轮新增） ───────────────────────
//
// # 这道门补的是哪块缺口
//
// R157 轮**本地实测**踩到的一件事：本机 PATH 里的 `psql` 是**另一个项目的转发脚本**
// （硬编码转发到 `acc-pg17`）。⇒ 检查器的 probe 模式报出来的是
//
//	「探库失败（psql 退出码 1）」+ 一个**别的容器**的名字
//
// ⇒ ★★ **报错只说「psql 失败」，不说「你连的不是你以为的那个库」** ⇒ ★★ 人只能去猜。
//
// # ⇒ 修法（已落地）
//
// ① **身份行**：探库时额外查一次「库自己怎么说」（current_database / current_user /
//
//	inet_server_addr:port / pg_postmaster_start_time / pg_is_in_recovery），
//	以 `#identity` 行**前置**进清单 ⇒ classify 把它印成 `#meta db_identity`。
//
// ② **失败也要自报目标**：probe 失败时把「我要连的 host:port/db」写进 FATAL 行。
//
// ★★ **两侧对照**才是关键：★ `requested=`（我以为连的是谁）vs `db=/server=/postmaster_start=`
// （库说它是谁）⇒ ★★ 一旦 PATH 里的 psql 指向别的库，**两侧立刻对不上**。
//
// # ★ 门必须自带阳性对照
//
// ① 给了 `#identity` 行 ⇒ `#meta db_identity` 必须**逐字**含身份字段；
// ② **没给** ⇒ 必须是 `(not provided)`（★ 不能静默变成空串 ⇒ 空串读起来像「没有身份」）。
// ③ probe 失败 ⇒ stderr 必须**说出目标 host:port/db**。
func TestV1FamilyRetirementReportsDatabaseIdentity(t *testing.T) {
	// ① 给了身份行 ⇒ 逐字保留。
	const ident = "#identity\trequested=127.0.0.1:54318/llm_gateway|db=llm_gateway|user=llm_gateway|server=(local):(unix)|postmaster_start=2026-10-25 18:08:45+08|in_recovery=false"
	stdout, stderr, code := v1RetireRun(t, ident+"\n"+v1RetireInvRetired)
	if code != 0 {
		t.Fatalf("带身份行的清单应判 retired（退出码 0），实际 %d\nstderr:\n%s", code, stderr)
	}
	got := v1RetireMeta(t, stdout, "db_identity")
	for _, want := range []string{
		"requested=127.0.0.1:54318/llm_gateway",
		"db=llm_gateway",
		"user=llm_gateway",
		"server=(local):(unix)",
		"postmaster_start=2026-10-25 18:08:45+08",
		"in_recovery=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("#meta db_identity 里缺 %q\n实际：%s", want, got)
		}
	}

	// ② 没给身份行 ⇒ 必须是 (not provided)（★ 不能是空串）。
	stdout2, stderr2, code2 := v1RetireRun(t, v1RetireInvRetired)
	if code2 != 0 {
		t.Fatalf("基线应判 retired（退出码 0），实际 %d\nstderr:\n%s", code2, stderr2)
	}
	got2 := v1RetireMeta(t, stdout2, "db_identity")
	if got2 != "(not provided)" {
		t.Errorf("没有身份行时 #meta db_identity 应为 \"(not provided)\"，实际 %q", got2)
	}
}

// TestV1FamilyRetirementProbeFailureNamesTarget —— probe 失败时**必须说出目标**。
//
// ★ 为什么这条是硬要求：★ 「psql 退出码 1」这句话**不携带任何定位信息**。
//
//	★ 本轮实测：一个指向别的容器的 psql shim ⇒ 报错与「目标库」毫无关系。
//	⇒ 读者必须能只凭这一行就判断「我是不是连错库了」。
//
// ★ 没有 psql 的机器上跳过（★ 环境依赖，不是产品缺陷）。
func TestV1FamilyRetirementProbeFailureNamesTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上跳过（★ 本门依赖 bash + psql）")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("PATH 里没有 psql ⇒ 跳过（★ 探库路径无法在本机验证）")
	}
	// ★ 端口 1 ⇒ 几乎必然连不上 ⇒ 秒级失败。
	dsn := "postgres://audit:sentinel_probe_target@127.0.0.1:1/llm_gateway?sslmode=disable&connect_timeout=2"
	cmd := exec.Command("bash", v1RetireScriptPath(t), "probe", dsn)
	cmd.Stdin = strings.NewReader("")
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr == nil {
		t.Skipf("probe 竟然成功了（★ 端口 1 连上了？）⇒ 本门不适用\n%s", outBuf.String())
	}
	combined := outBuf.String() + errBuf.String()

	// ★★★★ **必须分开 stdout 与 stderr**（★ 本轮变异验证实测出来的坑）。
	//
	// ★ 背景：★ 失败时 **stdout 也有身份行**（★ 含 `requested=<host:port/db>`）
	//   ⇒ ★★ 若断言写在**合并输出**上 ⇒ ★★ **删掉 FATAL 消息里的目标也照样过**
	//   ⇒ ★★ 实测 M2 变异：★ 门**依然绿** ⇒ ★★ **断言被我自己新加的来源削弱了**。
	// ⇒ ★★★ **同一个事实有多个来源时，★ 断言必须点名来源** ⇒ ★★ 否则「加一个来源」会静默削弱旧断言。
	//
	// ★ 所以拆成两条：★ ① **stderr**（FATAL 消息）必须说出目标；
	//   ② **stdout**（身份行）必须形状正确。
	stderr := errBuf.String()
	if !strings.Contains(stderr, "127.0.0.1:1/llm_gateway") {
		t.Errorf("probe 失败的 FATAL 消息（stderr）里没有说出目标 host:port/db ⇒ 读者无法判断是不是连错库了\nstderr:\n%s", stderr)
	}

	// ★ 口令仍然不能回显（★ 与既有门一致，★ 两路都查）。
	if strings.Contains(combined, "sentinel_probe_target") {
		t.Errorf("probe 的输出里出现了哨兵口令 ⇒ 回显了口令\nstdout:\n%s\nstderr:\n%s", outBuf.String(), stderr)
	}
	if strings.Contains(combined, dsn) {
		t.Errorf("probe 的输出里出现了完整 DSN ⇒ 回显了 DSN\nstdout:\n%s\nstderr:\n%s", outBuf.String(), stderr)
	}

	// ★★★★ **生产者的格式**也必须被钉住（★ 本轮变异验证实测出来的缺口）。
	//
	// ★ 背景：★ 门喂的是**手写**身份行 ⇒ ★⇒ **从不验证 probe 模式那个 printf 的格式**
	//   ⇒ ★★ 把 probe 的分隔符从 `|` 改回 TAB ⇒ ★★ **门依然绿**（★ 实测 M3 变异：门仍绿）。
	// ⇒ ★★ 修法两条：★ ① probe 的成功/失败**共用一个 printf**（★ 格式单一真值源）
	//   ★ ② 门断言**失败路径 stdout 里那行身份行**的形状（★ 不需要真库 ⇒ 不引入环境依赖）。
	//
	// ★ 判据：★ `#identity` 行必须**恰好一个 TAB**（★ 行首分隔符）
	//   ⇒ ★★ **其余分隔符必须是 `|`**（★ 若用 TAB ⇒ `#meta` 行会多字段 ⇒ 读数被静默截断）。
	var idLine string
	for _, ln := range strings.Split(outBuf.String(), "\n") {
		if strings.HasPrefix(ln, "#identity\t") {
			idLine = ln
			break
		}
	}
	if idLine == "" {
		t.Errorf("probe 失败时 stdout 里没有 #identity 行 ⇒ 读者看不到「我要连的是谁」\nstdout:\n%s", outBuf.String())
		return
	}
	if n := strings.Count(idLine, "\t"); n != 1 {
		t.Errorf("#identity 行里有 %d 个 TAB ⇒ 必须恰好 1 个（★ 其余字段用 `|`，★ 否则 #meta 会多字段而静默截断）\n该行：%s", n, idLine)
	}
	if !strings.Contains(idLine, "|") {
		t.Errorf("#identity 行的字段之间没用 `|` 分隔 ⇒ 会被 TSV 解析截断\n该行：%s", idLine)
	}
}
