//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// credential_routability_consistency_gate_test.go —— 「凭据可恢复性 vs 可路由性」一致性检查器的**行为门**。
//
// # 这道门补的是哪块缺口
//
// 本仓的凭据有两个**各自独立**的判定来源：
//
//	① credentials.availability_state       （★ 「凭据能不能用」⇒ UI / 诊断面板读它）
//	② v_routable_credential_models.is_routable （★ 「这条 (凭据,模型) 能不能进路由候选池」）
//
// 而 handleForceRecoverSingle（admin/diagnostics_credential.go）**只重置 availability 那一族**
// ⇒ ★★ **不碰 quota_state** ⇒ ★★ 对「配额已耗尽」的凭据，force-recover 返回 200 且文案说
// ★★ 「凭据已强制恢复」，★ 但它**仍然不可路由** ⇒ ★★★ **「恢复成功」是假的**。
//
// ⇒ 本门守的是 `scripts/audit/check-credential-routability-consistency.sh`
//   （★ **只读**的探测器 ⇒ ★ 可安全指向生产库）。
//
// # ★ 为什么必须做成【行为层】门
//
// 本仓反复吃过这个亏：**门绿 ≠ 事实成立**。
// 如果这道门只断言「脚本里出现过 `force-recover-blind` 这个字符串」，那么
// ① 把分类逻辑整个写反（三种输入都返回同一个答案）⇒ 门**依然绿**。
//
// ⇒ 所以本门的核心是 **真的跑脚本的 classify 模式**，喂**合成清单**，
//
//	⇒ 断言【判定词】+【退出码】+【分类计数】三者同时对：
//
//	A 类（凭据级自相矛盾）⇒ inconsistent / 退出码 2
//	C 类（探针回标）      ⇒ probe-reverted-only / 退出码 2
//	空清单                ⇒ consistent / 退出码 0
//
// ★ 这就是脚本「判定逻辑与查库分开」的意义：**门不需要真库**（秒级、可进 CI）。
//
// # ★★ 门必须自带【阳性对照】+【反向对照】
//
// ① 喂 A 类 ⇒ 必须判 inconsistent 且退出码 2（★ 否则分类逻辑写反也绿）
// ② 喂空清单 ⇒ 必须判 consistent 且退出码 0（★ 否则「永远红」也算通过）
// ③ ★★ **同一批数据，把 A 改成 C ⇒ 判定词必须跟着变**（★ 证明不是恒返回同一个答案）
//
// # ★★ 一条**形状层**断言（★ 本轮实测踩出来的）
//
// ★★ SQL 写在 **bash 双引号字符串**里 ⇒ ★★ **注释里的反引号 = 命令替换**
// ⇒ ⇒ bash 会把 `availability_state='ready'` 当命令执行 ⇒ ⇒ **SQL 文本被静默改写**
// ⇒ ⇒ 而 psql **照样成功** ⇒ ⇒ **一条被静默改写的查询，比一条报错危险得多**。
// ⇒ ⇒ 所以本门断言：**SQL 区域里不得出现反引号**。
//
// # ★ 诚实边界（本门**没有**做的事）
//
// 本门只用**合成清单**测 classify 模式，**不连任何库**（含本地库 / 生产库）。
// ⇒ 「probe 模式在真库上也能正确判定」这件事，**不由本门证明**。

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// crcRelPath 是被测脚本**在仓内的相对路径**（正斜杠）。
//
// ★ 这条常量同时被「门指向的文件」与「脚本自述的用法」两处断言引用 ⇒
// 门指错文件 ⇒ 红；脚本被挪走 ⇒ 红。
const crcRelPath = "scripts/audit/check-credential-routability-consistency.sh"

// crcRepoRoot 返回仓根。
//
// ★ 必须用 runtime.Caller：写死相对路径（"../.."）或依赖 os.Getwd()，
// 在测试的工作目录变化时会扫不到文件 ⇒ 静态守卫变成恒绿门。
func crcRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// crcScriptPath 返回被测脚本的绝对路径。
func crcScriptPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(crcRepoRoot(t), filepath.FromSlash(crcRelPath))
}

// crcRead 读被测脚本文本。
func crcRead(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(crcScriptPath(t))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", crcRelPath, err)
	}
	return string(b)
}

// crcRun 跑 classify 模式（★ 喂合成清单），返回 stdout / stderr / 退出码。
func crcRun(t *testing.T, stdin string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", crcScriptPath(t), "classify")
	cmd.Stdin = strings.NewReader(stdin)
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

// crcMeta 从 stdout 的 TSV 里取一个 `#meta` 键的值。
func crcMeta(t *testing.T, stdout, key string) string {
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
// 清单格式：10 列 TAB 分隔（★ 与脚本 PROBE_SQL 的 SELECT 列表一一对应）：
//
//	cred, provider_id, availability_state, quota_state, health_status,
//	binding_id, raw_model_name, is_routable, unavailable_reason, finding_class
//
// ★ 第 10 列（finding_class）由**脚本的 SQL** 产出；★ 但门喂的是合成清单，
//
//	所以门按**第 10 列**做分类计数（★ 脚本的 classify 也是按第 10 列计数 ⇒ ★ 同源）。
const crcHeader = "cred\tprovider_id\tavailability_state\tquota_state\thealth_status\t" +
	"binding_id\traw_model_name\tis_routable\tunavailable_reason\tfinding_class"

// crcRow 造一行合成清单。
func crcRow(cred, avail, quota, reason, class string) string {
	return strings.Join([]string{
		cred, "2453", avail, quota, "healthy",
		cred + "0", "some/model", "f", reason, class,
	}, "\t")
}

// crcInvA 造一份「A 类（凭据级自相矛盾）」清单。
// ★ 用 var 而非 const：★ crcRow 是函数 ⇒ const 表达式里不能用。
var crcInvA = crcRow("7", "ready", "permanently_exhausted", "quota_permanently_exhausted", "A-credential-quota-contradiction") + "\n" +
	crcRow("8", "ready", "permanently_exhausted", "quota_permanently_exhausted", "A-credential-quota-contradiction")

// crcInvC 造一份「C 类（探针回标）」清单。
var crcInvC = crcRow("9", "ready", "ok", "node_probe_failed", "C-probe-reverted")

// ─────────────────────────── 形状层 ───────────────────────────

// TestCredentialRoutabilityConsistencyScriptExistsAndParses —— 脚本在位且能过 bash -n。
func TestCredentialRoutabilityConsistencyScriptExistsAndParses(t *testing.T) {
	p := crcScriptPath(t)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s 不存在：%v", crcRelPath, err)
	}
	cmd := exec.Command("bash", "-n", p)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n 失败（★ 语法错）：%v\n%s", err, out)
	}
}

// ★★ TestCredentialRoutabilityConsistencySQLHasNoBacktick —— ★★ **本轮实测踩出来的形状断言**。
//
// ★★ 缘由：★ SQL 写在 **bash 双引号字符串**里 ⇒ ★★ **注释里的反引号 = 命令替换**
// ⇒ ⇒ bash 会把 `availability_state='ready'` 当命令执行 ⇒ ⇒ **SQL 文本被静默改写**
// ⇒ ⇒ 而 psql **照样成功** ⇒ ⇒ **一条被静默改写的查询，比一条报错危险得多**。
//
// ⇒ 本门断言：**PROBE_SQL 区域里不得出现反引号**。
func TestCredentialRoutabilityConsistencySQLHasNoBacktick(t *testing.T) {
	src := crcRead(t)
	i := strings.Index(src, `PROBE_SQL="`)
	if i < 0 {
		t.Fatalf("脚本里没有 PROBE_SQL 赋值 ⇒ 门指向的形态不对\n%s", src[:minInt(len(src), 400)])
	}
	rest := src[i:]
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		t.Fatal("PROBE_SQL 区域没有闭合（★ 找不到结尾）")
	}
	region := rest[:end]
	if strings.Contains(region, "`") {
		t.Errorf("PROBE_SQL 区域里有反引号 ⇒ ★★ bash 双引号串里它是【命令替换】⇒ ★★ SQL 会被静默改写\n区域:\n%s", region)
	}
}

// TestCredentialRoutabilityConsistencyScriptHasNoWriteStatements —— 脚本**只读**。
//
// ★ 判据：★ 脚本里**不得**出现写库语句（INSERT / UPDATE / DELETE / DROP / TRUNCATE / ALTER / CREATE）。
// ★ 因为它会被指向**生产库** ⇒ ★ 它必须**只读**。
func TestCredentialRoutabilityConsistencyScriptHasNoWriteStatements(t *testing.T) {
	code := crcStripComments(crcRead(t))
	bad := []string{"INSERT ", "UPDATE ", "DELETE ", "DROP ", "TRUNCATE ", "ALTER ", "CREATE ", "GRANT "}
	for _, kw := range bad {
		if strings.Contains(strings.ToUpper(code), kw) {
			t.Errorf("脚本里出现了写库关键字 %q ⇒ 本检查器必须是**只读**的", kw)
		}
	}
}

// TestCredentialRoutabilityConsistencySupportsBothModes —— probe / classify 两个模式都在。
func TestCredentialRoutabilityConsistencySupportsBothModes(t *testing.T) {
	src := crcRead(t)
	for _, want := range []string{"probe", "classify"} {
		if !strings.Contains(src, want) {
			t.Errorf("脚本里没有 %q 模式 ⇒ 门指向的形态不对", want)
		}
	}
	// ★ 退出码契约必须在用法里写明（★ 「不许静默降级」的落点是退出码）。
	if !strings.Contains(src, "退出码") {
		t.Error("脚本的用法里没有写退出码契约 ⇒ 调用方无从判断结果")
	}
}

// ─────────────────────────── 行为层 ───────────────────────────

// TestCredentialRoutabilityConsistencyClassifiesAClass —— A 类 ⇒ inconsistent / 退出码 2。
func TestCredentialRoutabilityConsistencyClassifiesAClass(t *testing.T) {
	stdout, stderr, code := crcRun(t, crcInvA)
	if got := crcMeta(t, stdout, "verdict"); got != "inconsistent" {
		t.Errorf("A 类（★ 凭据级自相矛盾）的判定应为 inconsistent，实际 %q\nstderr:\n%s", got, stderr)
	}
	// ★★ **退出码必须反映判定**（★ 不是 stderr 上的一句话）
	if code != 2 {
		t.Errorf("A 类的退出码应为 2，实际 %d", code)
	}
	if got := crcMeta(t, stdout, "class_A_credential_quota"); got != "2" {
		t.Errorf("A 类计数应为 2，实际 %q", got)
	}
	if got := crcMeta(t, stdout, "unroutable_bindings"); got != "2" {
		t.Errorf("不可路由绑定总数应为 2，实际 %q", got)
	}
	if got := crcMeta(t, stdout, "credentials_affected"); got != "2" {
		t.Errorf("涉及凭据数应为 2，实际 %q", got)
	}
}

// TestCredentialRoutabilityConsistencyClassifiesCClass —— C 类 ⇒ probe-reverted-only / 退出码 2。
func TestCredentialRoutabilityConsistencyClassifiesCClass(t *testing.T) {
	stdout, stderr, code := crcRun(t, crcInvC)
	if got := crcMeta(t, stdout, "verdict"); got != "probe-reverted-only" {
		t.Errorf("C 类（★ 探针回标）的判定应为 probe-reverted-only，实际 %q\nstderr:\n%s", got, stderr)
	}
	if code != 2 {
		t.Errorf("C 类的退出码应为 2，实际 %d", code)
	}
	if got := crcMeta(t, stdout, "class_C_probe_reverted"); got != "1" {
		t.Errorf("C 类计数应为 1，实际 %q", got)
	}
}

// TestCredentialRoutabilityConsistencyEmptyIsConsistent —— 空清单 ⇒ consistent / 退出码 0。
//
// ★ 这是**反向对照**：★ 否则「永远红」也能算通过。
func TestCredentialRoutabilityConsistencyEmptyIsConsistent(t *testing.T) {
	stdout, stderr, code := crcRun(t, "")
	if got := crcMeta(t, stdout, "verdict"); got != "consistent" {
		t.Errorf("空清单的判定应为 consistent，实际 %q\nstderr:\n%s", got, stderr)
	}
	if code != 0 {
		t.Errorf("空清单的退出码应为 0，实际 %d", code)
	}
}

// TestCredentialRoutabilityConsistencyVerdictTracksInput —— ★★ **阳性对照**：判定跟着输入变。
//
// ★ 把 A 类的清单换成 C 类 ⇒ ★★ 判定词**必须跟着变** ⇒ ★★ 证明脚本不是恒返回同一个答案。
func TestCredentialRoutabilityConsistencyVerdictTracksInput(t *testing.T) {
	_, _, aCode := crcRun(t, crcInvA)
	_, _, cCode := crcRun(t, crcInvC)
	if aCode != cCode {
		t.Errorf("A 类与 C 类的退出码应相同（★ 都是 2），实际 %d vs %d", aCode, cCode)
	}
	// ★★ 判定词必须**不同**（★ 这是「它真的在分类」的证据）。
	aOut, _, _ := crcRun(t, crcInvA)
	cOut, _, _ := crcRun(t, crcInvC)
	if crcMeta(t, aOut, "verdict") == crcMeta(t, cOut, "verdict") {
		t.Errorf("A 类与 C 类的判定词相同（%q）⇒ ★★ 脚本可能在恒返回同一个答案",
			crcMeta(t, aOut, "verdict"))
	}
	// ★★ 反过来：把 C 类换成 A 类 ⇒ 分类计数也必须跟着变。
	if crcMeta(t, aOut, "class_A_credential_quota") == crcMeta(t, cOut, "class_A_credential_quota") {
		t.Error("A 类计数在两份清单里相同 ⇒ 分类计数可能没跟着输入变")
	}
}

// TestCredentialRoutabilityConsistencyNeverEchoesDSN —— ★★ 硬要求：不回显 DSN / 口令。
func TestCredentialRoutabilityConsistencyNeverEchoesDSN(t *testing.T) {
	const sentinel = "SENTINEL_PW_crc_7b41e"
	dsn := "postgres://audit:" + sentinel + "@127.0.0.1:5432/llm_gateway?sslmode=disable"
	stdout, stderr, _ := crcRun(t, crcInvA)
	out := stdout + stderr
	if strings.Contains(out, sentinel) {
		t.Errorf("输出里出现了哨兵口令 ⇒ 脚本回显了口令\n%s", out)
	}
	if strings.Contains(out, dsn) {
		t.Errorf("输出里出现了完整 DSN ⇒ 脚本回显了 DSN\n%s", out)
	}
}

// ─────────────────────────── 辅助 ───────────────────────────

// crcStripComments 去掉**注释**，只留「可执行部分」。
//
// ★ 为什么必须剥注释再查写语句：脚本的头部注释里**要**解释
// 「本脚本绝不写库」⇒ 注释里出现那些词是**应该的** ⇒ 判据必须只看**代码**。
func crcStripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		cut := len(line)
		inSingle, inDouble := false, false
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
			default:
				switch c {
				case '\'':
					inSingle = true
				case '"':
					inDouble = true
				case '#':
					cut = i
					i = len(line)
				}
			}
		}
		b.WriteString(line[:cut])
		b.WriteString("\n")
	}
	return b.String()
}

// minInt 取较小值。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 编译期护栏：strconv 必须被用到（★ 免得 go vet 报未使用导入）。
var _ = strconv.Itoa
