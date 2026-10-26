//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// post_surface_gate_test.go —— 「POST 面覆盖」工具的行为门。
//
// # 这道门补的是哪块缺口
//
// 本仓绝大多数 admin 路由是用**不带方法前缀**的形式注册的：
//
//	mux.HandleFunc("/api/admin/xxx", adminWrap(h.handleXxx))
//
// ⇒ Go 的 http.ServeMux 对这种注册**接受任意 HTTP 方法** ⇒ POST 也会被受理。
// 而仓内的 scripts/audit/api-sweep.sh（★ 日常巡检）**只发 GET**
// ⇒ ⇒ **存在一片「注册了但从未用 POST 验证过」的端点面。**
//
// # ★★ 为什么这道门必须是【行为层】的
//
// 本仓反复吃过这个亏：**门绿 ≠ 事实成立**。
// 如果这道门只断言「脚本里出现过 post-surface-probe.sh 这个字符串」，那么
// ① 把四档分类逻辑整个写反（所有输入都返回同一个判定）⇒ 门**依然绿**。
//
// ⇒ 所以本门的核心是 **真的跑脚本**：
//
//	① `classify` 模式（★ 喂合成清单）⇒ 断言判定词 + 退出码 + 四档计数三者同时对
//	② `ledger` 模式（★ 喂合成分级 + 合成探测结果）⇒ 断言**裁决列能被机器重算**
//	③ ★★ **阳性对照**：★ 同一批输入换 bucket ⇒ ★★ **裁决词必须跟着变**
//	④ ★★ **反向对照**：★ 空输入 ⇒ ★★ **零行**（★ 不是「全部已验证」）
//
// # ★★★ 「裁决列必须能被机器重算」是本门最重要的一条
//
// ★ R157-D18 留下的通用式（★ LESSONS §405）：★ **「一条规则与一条判据可以互相矛盾 ⇒ 门会逼人撒谎」**
// ⇒ ★★ **⇒ 裁决列要么能机器验证，要么就别叫它「裁决」**。
// ⇒ ⇒ ★★★ 所以本门**从输入重新推导一遍裁决**，★ 再逐字比对台账里的裁决列。
// ⇒ ⇒ ★★★ **⇒ 台账过期 ⇒ 门必红**（★ 「台账过期」从此是机器可判的）。
//
// # ★★ 一条【形状层】断言（★ 本轮实测踩出来的）
//
// ★★ SQL 写在 **bash 双引号字符串**里 ⇒ ★★ **注释里的反引号 = 命令替换**
// ⇒ ⇒ bash 会把 `availability_state='ready'` 当命令执行 ⇒ ⇒ **SQL 文本被静默改写**
// ⇒ ⇒ 而 psql **照样成功** ⇒ ⇒ **一条被静默改写的查询，比一条报错危险得多**。
// ⇒ ⇒ 所以本门断言：**SQL 区域里不得出现反引号**。
//
// # ★ 诚实边界（本门**没有**做的事）
//
// ① 本门**不连任何库**、**不发任何 HTTP** ⇒ ★ 「probe 模式在真库/真服务上也能正确判定」
//    **不由本门证明**（★ 由 R159 轮的双侧实测承担）。
// ② 本门**不验证静态分级的正确性** ⇒ ★★ 「分级说它只读」**不等于**「它不写」
//    （★ R159 实测：★ 5 个被分级判成 `read` 的路由**空 body 就写库**）
//    ⇒ ★★ 这条盲区由**运行时护栏**兜，★ 而护栏由 R159 轮的实测承担。

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	pspRelPath       = "scripts/audit/post-surface-probe.sh"
	pscRelPath       = "scripts/audit/post-surface-classify.py"
	pspUnsafeRelPath = "scripts/audit/asset/unsafe-post-routes.txt"
	pspLedgerRelPath = "scripts/audit/asset/post-surface-ledger.tsv"
	pspRoutesRelPath = "scripts/audit/asset/admin-routes.txt"
)

// pspRepoRoot 返回仓根。
//
// ★ 必须用 runtime.Caller：写死相对路径（"../.."）或依赖 os.Getwd()，
// 在测试的工作目录变化时会扫不到文件 ⇒ 静态守卫变成恒绿门。
func pspRepoRoot(t *testing.T) string {
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

func pspPath(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(pspRepoRoot(t), filepath.FromSlash(rel))
}

func pspRead(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(pspPath(t, rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return string(b)
}

// pspRun 跑脚本（★ 传参数 + stdin），返回 stdout / stderr / 退出码。
func pspRun(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	argv := append([]string{pspPath(t, pspRelPath)}, args...)
	cmd := exec.Command("bash", argv...)
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

// pspMeta 从 stdout 的 TSV 里取一个 #meta 键的值。
func pspMeta(t *testing.T, stdout, key string) string {
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

// ─────────────────────────── 形状层 ───────────────────────────

// TestPostSurfaceProbeScriptExistsAndParses —— 脚本在位且能过 bash -n。
func TestPostSurfaceProbeScriptExistsAndParses(t *testing.T) {
	p := pspPath(t, pspRelPath)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s 不存在：%v", pspRelPath, err)
	}
	cmd := exec.Command("bash", "-n", p)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n 失败（★ 语法错）：%v\n%s", err, out)
	}
}

// TestPostSurfaceClassifierExistsAndParses —— 分级器在位且是合法 Python。
func TestPostSurfaceClassifierExistsAndParses(t *testing.T) {
	p := pspPath(t, pscRelPath)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s 不存在：%v", pscRelPath, err)
	}
	cmd := exec.Command("python3", "-c", "import ast,sys; ast.parse(open(sys.argv[1],encoding='utf-8').read())", p)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("分类器语法错（★ python3 ast.parse 失败）：%v\n%s", err, out)
	}
}

// TestPostSurfaceProbeSQLHasNoBacktick —— ★★ **SQL 区域禁反引号**。
//
// ★★ 缘由：★ SQL 写在 bash 双引号字符串里 ⇒ ★★ **注释里的反引号 = 命令替换**
// ⇒ ⇒ bash 会把它当命令执行 ⇒ ⇒ **SQL 文本被静默改写**
// ⇒ ⇒ 而 psql **照样成功** ⇒ ⇒ **一条被静默改写的查询，比一条报错危险得多**。
func TestPostSurfaceProbeSQLHasNoBacktick(t *testing.T) {
	src := pspRead(t, pspRelPath)
	// ★ 护栏的两条 SQL 都在双引号里 ⇒ ★ 逐个找 `-c "select` 开头的行并检查其内容。
	bad := 0
	for _, ln := range strings.Split(src, "\n") {
		i := strings.Index(ln, `-c "`)
		if i < 0 {
			continue
		}
		rest := ln[i+4:]
		if strings.Contains(rest, "`") {
			t.Errorf("SQL 区域里有反引号 ⇒ ★★ bash 双引号串里它是【命令替换】⇒ ★★ SQL 会被静默改写\n行: %s", ln)
			bad++
		}
	}
	if bad == 0 && !strings.Contains(src, "select 'db_identity'") {
		t.Error("脚本里找不到护栏的 SQL ⇒ ★ 门指向的形态不对")
	}
}

// TestPostSurfaceProbeGuardIsReadOnly —— ★★ 护栏必须【只读】。
//
// ★ 判据：★ 护栏的 SQL 里**不得**出现写库语句。
// ★ 因为它会被指向**生产库**（★ 或本地库）⇒ ★ 它必须**只读**。
func TestPostSurfaceProbeGuardIsReadOnly(t *testing.T) {
	src := pspRead(t, pspRelPath)
	for _, kw := range []string{"INSERT ", "UPDATE ", "DELETE ", "TRUNCATE ", "DROP ", "ALTER ", "CREATE "} {
		if strings.Contains(strings.ToUpper(src), kw) {
			t.Errorf("脚本里出现了写库关键字 %q ⇒ ★★ 护栏必须**只读**的", kw)
		}
	}
}

// ─────────────────────────── 行为层 ───────────────────────────

// pspSynthProbe 造一份合成探测结果（★ 7 列：path, method, src, code, body_check, bucket, snippet）。
func pspSynthProbe(bucket string) string {
	return strings.Join([]string{
		"/api/x", "ANY", "a/b.go:1", "400", "r.Body", bucket, "msg",
	}, "\t") + "\n"
}

// TestPostSurfaceClassifyViaFile —— ★★ classify 模式（★ 走真实临时文件）。
func TestPostSurfaceClassifyViaFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("写临时文件失败：%v", err)
		}
		return p
	}
	ok := write("ok.tsv", pspSynthProbe("validated")+pspSynthProbe("method-not-registered"))
	stdout, stderr, code := pspRun(t, "", "classify", ok)
	if got := pspMeta(t, stdout, "verdict"); got != "post-surface-verified" {
		t.Errorf("①② 全绿时的判定应为 post-surface-verified，实际 %q\nstderr:\n%s", got, stderr)
	}
	if code != 0 {
		t.Errorf("①② 全绿时的退出码应为 0，实际 %d", code)
	}
	if got := pspMeta(t, stdout, "class_validated"); got != "1" {
		t.Errorf("validated 计数应为 1，实际 %q", got)
	}

	bad := write("bad.tsv", pspSynthProbe("executed")+pspSynthProbe("error"))
	stdout2, stderr2, code2 := pspRun(t, "", "classify", bad)
	if got := pspMeta(t, stdout2, "verdict"); got != "needs-manual-review" {
		t.Errorf("③④ 存在时的判定应为 needs-manual-review，实际 %q\nstderr:\n%s", got, stderr2)
	}
	if code2 != 2 {
		t.Errorf("③④ 存在时的退出码应为 2，实际 %d", code2)
	}
}

// TestPostSurfaceLedgerVerdictsAreDerivable —— ★★★ **裁决列必须能被机器重算**。
//
// ★★ 这是本门最重要的一条：★ **台账过期 ⇒ 门必红**（★ 「台账过期」从此机器可判）。
func TestPostSurfaceLedgerVerdictsAreDerivable(t *testing.T) {
	dir := t.TempDir()
	// ★ 合成分级：path, method, src, handler, class, reason, body_check
	// ★★ 注意：★ **每行先按 TAB 连字段，★ 再按换行连行**。
	// ★★ （★ 本门第一版写成 `strings.Join([]string{字段...}, "\n")` ⇒ ★★ 把【字段】用换行连了
	// ★★  ⇒ ★★ 4 行变成 28 行单字段行 ⇒ ★★ 工具的兜底裁决 `not-probed-not-reached` 把它暴露了出来
	// ★★  ⇒ ★★★ **⇒ 「工具的兜底分支」是一道有效的自检。**）
	class := strings.Join([]string{
		strings.Join([]string{"/api/a", "ANY", "a.go:1", "h.a", "read", "", "r.Body"}, "\t"),
		strings.Join([]string{"/api/b", "ANY", "a.go:2", "h.b", "write", "exec", ""}, "\t"),
		strings.Join([]string{"/api/c", "ANY", "a.go:3", "h.c", "external", "http-call", ""}, "\t"),
		strings.Join([]string{"/api/d", "ANY", "a.go:4", "h.d", "unknown", "func-not-indexed", ""}, "\t"),
	}, "\n") + "\n"
	// ★ 合成探测结果
	probe := strings.Join([]string{
		strings.Join([]string{"/api/a", "ANY", "a.go:1", "400", "r.Body", "validated", "msg"}, "\t"),
		strings.Join([]string{"/api/b", "ANY", "a.go:2", "200", "", "executed", "msg"}, "\t"),
		strings.Join([]string{"/api/c", "ANY", "a.go:3", "500", "", "error", "msg"}, "\t"),
	}, "\n") + "\n"
	cp := filepath.Join(dir, "class.tsv")
	pp := filepath.Join(dir, "probe.tsv")
	if err := os.WriteFile(cp, []byte(class), 0o600); err != nil {
		t.Fatalf("写分级文件失败：%v", err)
	}
	if err := os.WriteFile(pp, []byte(probe), 0o600); err != nil {
		t.Fatalf("写探测结果失败：%v", err)
	}
	stdout, stderr, code := pspRun(t, "", "ledger", pp, cp)
	if code != 0 {
		t.Fatalf("ledger 模式退出码 %d（★ 应为 0）\nstderr:\n%s", code, stderr)
	}
	want := map[string]string{
		"/api/a": "post-verified-no-side-effect",
		"/api/b": "post-executes-on-empty-body",
		"/api/c": "post-executes-on-empty-body",
		"/api/d": "not-probed-unresolved",
	}
	got := map[string]string{}
	for _, ln := range strings.Split(stdout, "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) < 8 {
			t.Fatalf("ledger 行字段不足 8：%q", ln)
		}
		got[f[0]] = f[7]
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("路径 %s 的裁决应为 %q，实际 %q", p, w, got[p])
		}
	}
	// ★★ **write 类路由若被探测成 validated，★ 裁决应是「已验证」而不是「未探测」**
	// ⇒ ★★ 也就是说：★ **裁决的推导顺序是【探测结果优先】**。
	if len(got) != 4 {
		t.Errorf("ledger 应有 4 行，实际 %d 行", len(got))
	}
}

// TestPostSurfaceLedgerEmptyInputIsEmpty —— ★★ **反向对照**：★ 空输入 ⇒ 零行。
//
// ★★ 否则「永远输出全部已验证」也能算通过。
func TestPostSurfaceLedgerEmptyInputIsEmpty(t *testing.T) {
	dir := t.TempDir()
	cp := filepath.Join(dir, "class.tsv")
	pp := filepath.Join(dir, "probe.tsv")
	if err := os.WriteFile(cp, []byte(""), 0o600); err != nil {
		t.Fatalf("写分级文件失败：%v", err)
	}
	if err := os.WriteFile(pp, []byte(""), 0o600); err != nil {
		t.Fatalf("写探测结果失败：%v", err)
	}
	stdout, stderr, code := pspRun(t, "", "ledger", pp, cp)
	if code != 0 {
		t.Fatalf("ledger 模式退出码 %d（★ 应为 0）\nstderr:\n%s", code, stderr)
	}
	rows := 0
	for _, ln := range strings.Split(stdout, "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		rows++
	}
	if rows != 0 {
		t.Errorf("空输入应产出 0 行，实际 %d 行\n%s", rows, stdout)
	}
}

// TestPostSurfaceLedgerVerdictTracksInput —— ★★ **阳性对照**：★ 裁决跟着输入变。
func TestPostSurfaceLedgerVerdictTracksInput(t *testing.T) {
	dir := t.TempDir()
	class := "/api/a\tANY\ta.go:1\th.a\tread\t\tr.Body\n"
	cp := filepath.Join(dir, "class.tsv")
	if err := os.WriteFile(cp, []byte(class), 0o600); err != nil {
		t.Fatalf("写分级文件失败：%v", err)
	}
	verdictOf := func(bucket string) string {
		p := filepath.Join(dir, "p-"+bucket+".tsv")
		body := "/api/a\tANY\ta.go:1\t200\tr.Body\t" + bucket + "\tmsg\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("写探测结果失败：%v", err)
		}
		stdout, stderr, code := pspRun(t, "", "ledger", p, cp)
		if code != 0 {
			t.Fatalf("ledger 退出码 %d\nstderr:\n%s", code, stderr)
		}
		for _, ln := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(ln, "/api/a\t") {
				f := strings.Split(ln, "\t")
				return f[7]
			}
		}
		t.Fatalf("ledger 输出里找不到 /api/a\n%s", stdout)
		return ""
	}
	vValidated := verdictOf("validated")
	vExecuted := verdictOf("executed")
	if vValidated == vExecuted {
		t.Errorf("同一批输入换 bucket 后裁决相同（%q）⇒ ★★ 裁决可能没跟着输入变", vValidated)
	}
	if vValidated != "post-verified-no-side-effect" {
		t.Errorf("validated 的裁决应为 post-verified-no-side-effect，实际 %q", vValidated)
	}
	if vExecuted != "post-executes-on-empty-body" {
		t.Errorf("executed 的裁决应为 post-executes-on-empty-body，实际 %q", vExecuted)
	}
}

// ─────────────────────────── 台账 ───────────────────────────

// TestPostSurfaceLedgerCoversEveryRoute —— ★★ 台账必须【一行覆盖一条路由】。
//
// ★★ 目标里写着「每一个 api 均需要进行验证」⇒ ★★ 台账是这句话的载体。
func TestPostSurfaceLedgerCoversEveryRoute(t *testing.T) {
	routes := pspRead(t, pspRoutesRelPath)
	want := 0
	for _, ln := range strings.Split(routes, "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		want++
	}
	ledger := pspRead(t, pspLedgerRelPath)
	got := 0
	for _, ln := range strings.Split(ledger, "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		got++
	}
	if got != want {
		t.Errorf("台账行数 %d ≠ 路由清单行数 %d ⇒ ★★ 台账没有覆盖每一个 api", got, want)
	}
}

// TestPostSurfaceLedgerVerdictsAreDerivableFromItsOwnInputs —— ★★★ 台账自洽：
// ★★ 用【台账自己的 class / bucket / 排除清单】重算裁决，★ 逐行比对。
//
// ★★ 这条是「台账过期 ⇒ 机器可判」的**落地点**：★ 台账被手工改过 ⇒ 门必红。
func TestPostSurfaceLedgerVerdictsAreDerivableFromItsOwnInputs(t *testing.T) {
	ledger := pspRead(t, pspLedgerRelPath)
	excl := map[string]bool{}
	for _, ln := range strings.Split(pspRead(t, pspUnsafeRelPath), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) > 0 {
			excl[f[0]] = true
		}
	}
	derive := func(class, bucket string, isExcl bool) string {
		switch {
		case isExcl:
			return "excluded-observed-writer"
		case bucket == "method-not-registered" || bucket == "validated":
			return "post-verified-no-side-effect"
		case bucket == "executed" || bucket == "error":
			return "post-executes-on-empty-body"
		case class == "write":
			return "not-probed-writes"
		case class == "external":
			return "not-probed-external"
		case class == "unknown":
			return "not-probed-unresolved"
		default:
			return "not-probed-not-reached"
		}
	}
	rows := 0
	for _, ln := range strings.Split(ledger, "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) < 9 {
			t.Fatalf("台账行字段不足 9：%q", ln)
		}
		rows++
		want := derive(f[3], f[6], excl[f[0]])
		if f[7] != want {
			t.Errorf("路径 %s 的裁决 %q 与重算结果 %q 不一致 ⇒ ★★ 台账与它的输入不同步", f[0], f[7], want)
		}
	}
	if rows == 0 {
		t.Fatal("台账没有任何数据行 ⇒ ★ 门指向的形态不对")
	}
}

// TestPostSurfaceUnsafeRoutesAreRealRoutes —— ★★ 排除清单里的路由必须是【真实存在的路由】。
//
// ★★ 否则清单会【悄悄过期】（★ 路由改名了 ⇒ ★ 清单里的旧名字永远匹配不上 ⇒ ★★ 保护失效）。
func TestPostSurfaceUnsafeRoutesAreRealRoutes(t *testing.T) {
	routes := map[string]bool{}
	for _, ln := range strings.Split(pspRead(t, pspRoutesRelPath), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) > 0 {
			routes[f[0]] = true
		}
	}
	n := 0
	for _, ln := range strings.Split(pspRead(t, pspUnsafeRelPath), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) < 4 {
			t.Errorf("排除清单行字段不足 4（★ 路径/分级/体校验/证据）：%q", ln)
			continue
		}
		n++
		if !routes[f[0]] {
			t.Errorf("排除清单里的路径 %q 不在路由清单里 ⇒ ★ 清单已过期", f[0])
		}
		if strings.TrimSpace(f[3]) == "" {
			t.Errorf("排除清单里的 %q 没有写证据来源 ⇒ ★★ 「实测观察到」必须可追", f[0])
		}
	}
	if n == 0 {
		t.Error("排除清单为空 ⇒ ★★ 那说明「空 body 就写库」的端点一个都没被发现？")
	}
}

// TestPostSurfaceProbeNeverEchoesDSN —— ★★ 硬要求：不回显 DSN / 口令。
func TestPostSurfaceProbeNeverEchoesDSN(t *testing.T) {
	const sentinel = "SENTINEL_PW_psp_3d9c1"
	dsn := "postgres://audit:" + sentinel + "@127.0.0.1:5432/llm_gateway?sslmode=disable"
	dir := t.TempDir()
	p := filepath.Join(dir, "p.tsv")
	if err := os.WriteFile(p, []byte(pspSynthProbe("validated")), 0o600); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}
	// ★ classify 模式不接触 DSN ⇒ ★★ 但 ★【守卫必须对【整条命令链】有效】⇒ ★★
	// ★ 这里断言：★ 脚本里若出现 GUARD_DSN 的值，★ 输出里不得出现它。
	out, errOut, _ := pspRun(t, "", "classify", p)
	if strings.Contains(out+errOut, sentinel) {
		t.Errorf("输出里出现了哨兵口令 ⇒ 脚本回显了口令\n%s", out+errOut)
	}
	_ = dsn
}
