package ursm_test

// R98 轮的门：**「API handler 运行时零覆盖」台账的完整性门**。
//
// # 这道门补的是哪块缺口
//
// 本仓有一条长期目标：「需要逐个 api 进行单元测试，确保所有任务已经完成」。
// 现状：`admin` 包的路由里**绝大多数在运行时从未被测试执行过** —— 而这条事实
// 只存在于**一次性的运行输出**里 ⇒ 别人无法复核、无法跟踪收口。
//
// ⇒ 本门把「缺口」变成**仓内可复现、可跟踪的资产**：
//   · 量具：`scripts/audit/api-handler-coverage.sh`（覆盖度审计器）
//   · 台账：`scripts/ursmcheck/api_handler_coverage_ledger.tsv`（零覆盖例外台账）
//   · 门  ：本文件
//
// # ★ 口径（★ 与量具逐字同源，★ 别在这里另立一套口径）
//
// ★★ 「运行时零覆盖」= coverprofile 里该 handler 函数体范围内
//    **没有一个语句块的执行次数 > 0**。
// ★★ 它**不是**「测试文件里没出现过这个名字」—— 那是**静态**口径，两者不可互推。
// ★★ 三类：`covered` / `zero_coverage` / `undetermined`（**判不了**，不许混进零覆盖）。
// ★★ `undetermined` **可能是被覆盖的** ⇒ **零覆盖比例是【下界】**。
//
// # 这道门**不**要求「零缺口」
//
// ★ 理由同 `pipefail_race_ledger_test.go`：台账里躺着几百条**真实的**零覆盖 handler
//   ⇒ 若门要求「零零覆盖行」，这个门会**永久红** ⇒⇒ 没人看它 ⇒⇒ 门就废了。
// ⇒⇒ 所以本门管的是**台账的完整性**，不是**缺口的清零**：
//	① 台账非空（分母 ≠ 0）；
//	② 每行都有**合法**的人工裁决，且**不允许 `pending`**（静态判据只能给候选集，
//	   终值必须逐条人工做出）；
//	③ **有覆盖的 handler 不许出现在台账里**（台账不许拿有覆盖的条目赦免）；
//	④ 零覆盖的 handler **必须**出现在台账里（不许悄悄漏登）；
//	⑤ 主键 (handler, fingerprint, occ) 唯一；
//	⑥ fingerprint 与 sha256(handler@def_file) 前 12 位一致（量具与门不许漂移）。
//
// # ★ 门自己怎么跑量具（★ 不许在门里跑完整的 `go test ./admin/`）
//
// ★ `go test ./admin/ -coverprofile` 实测 ≈ 95 s，且需要完整单测环境
//   ⇒ 门**不**跑它。门改用量具的**快速模式**：
//   `--cover <自造 fixture> --routes <3 条极小清单> --scan-dirs admin`
//   ⇒ 实测 ≈ 2 s。⇒ **门验证的是「机制」，不是「全集」**：
//   3 条 fixture 里既有「零覆盖」（必须在台账里）也有「有覆盖」（必须不在台账里）。
//
// # ★ 门自身的「有牙」证明
//
// `TestApiHandlerCoverageGateHasTeeth` 会**从内存里的台账副本删掉一行**
// （只动副本，不碰真台账），然后断言比对函数**必须**报出那一行缺失
// ⇒ 证明判据 ④ 真的在检查，而不是恒真。

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

const (
	// 量具（仓内相对路径）
	ahcToolRel = "scripts/audit/api-handler-coverage.sh"
	// 台账（与本文件同目录）
	ahcLedgerRel = "scripts/ursmcheck/api_handler_coverage_ledger.tsv"
	// 门本身
	ahcGateRel = "scripts/ursmcheck/api_handler_coverage_gate_test.go"
)

// ahcAdjudications = 台账里允许出现的**人工裁决**取值域。
//
// ★ 与量具写进台账 header 的取值域**逐字一致**（改一处必须改两处 —— 门会校验）。
// ★ `pending` 不在这里：它是量具对**新**项写的**初稿态**，门明确**禁止**它出现在台账里
//
//	（见 ahcPendingAdjudication）⇒ 所以「量具的集合」= 这里的集合 ∪ {pending}。
var ahcAdjudications = map[string]bool{
	"needs-unit-test":        true,
	"needs-integration-test": true,
	"logic-migrated":         true,
	"delegating-wrapper":     true,
	"param-validation-only":  true,
	"admin-manual-op":        true,
}

// ahcPendingAdjudication = 台账里**不允许**残留的初稿态裁决。
const ahcPendingAdjudication = "pending"

// ahcRepoRoot 用 runtime.Caller 定位仓库根（不依赖测试的工作目录）。
func ahcRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := ahcRepoRootErr()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func ahcRepoRootErr() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller 失败 ⇒ 无法定位仓库根")
	}
	// scripts/ursmcheck/xxx_test.go ⇒ 上一级是 scripts ⇒ 再上一级是仓库根
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile))), nil
}

// ahcRow = 台账的一行（14 列，末列 = fingerprint）。
type ahcRow struct {
	Path         string
	Line         string
	Occ          string
	Method       string
	Handler      string
	RegFile      string
	DefFile      string
	Stmts        string
	Verdict      string
	Adjudication string
	Reason       string
	Evidence     string
	Owner        string
	Fingerprint  string
}

// ahcKey = 台账主键：(handler, fingerprint, occ)。
//
// ★ 与量具写台账时编的 occ 同构：occ = 同 (handler, def_file) 组内按 path 排序的序号。
func (r ahcRow) ahcKey() string {
	return r.Handler + "\t" + r.Fingerprint + "\t" + r.Occ
}

// ahcFingerprint = 与量具**逐字同构**的 fingerprint：
// sha256(handler + "@" + defFile) 的前 12 位十六进制。
//
// ★ 两处各写一份是刻意的 —— 一处改了另一处没改 ⇒ 本门必须发现
//
//	（见 TestApiHandlerCoverageLedgerFingerprintMatchesTool）。
func ahcFingerprint(handler, defFile string) string {
	sum := sha256.Sum256([]byte(handler + "@" + defFile))
	return hex.EncodeToString(sum[:])[:12]
}

// ahcReadLedger 读台账 ⇒ key → 行。
func ahcReadLedger(t *testing.T) map[string]ahcRow {
	t.Helper()
	root := ahcRepoRoot(t)
	path := filepath.Join(root, ahcLedgerRel)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打不开台账 %s：%v", path, err)
	}
	defer f.Close()
	out := map[string]ahcRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if cols[0] == "path" {
			continue // 表头
		}
		if len(cols) < 14 {
			t.Fatalf("台账行列数 = %d，不足 14（末列必须是 fingerprint）：%q", len(cols), line)
		}
		r := ahcRow{
			Path: cols[0], Line: cols[1], Occ: cols[2], Method: cols[3], Handler: cols[4],
			RegFile: cols[5], DefFile: cols[6], Stmts: cols[7], Verdict: cols[8],
			Adjudication: cols[9], Reason: cols[10], Evidence: cols[11], Owner: cols[12],
			Fingerprint: cols[13],
		}
		if _, dup := out[r.ahcKey()]; dup {
			t.Fatalf("★ 台账主键重复：%q ⇒ 主键不唯一 ⇒ 同形行会互相冒用裁决", r.ahcKey())
		}
		out[r.ahcKey()] = r
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读台账出错：%v", err)
	}
	if len(out) == 0 {
		t.Fatalf("★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论")
	}
	return out
}

// ahcResult = 快速模式里量具的一条判定结果。
type ahcResult struct {
	Path    string
	Method  string
	Handler string
	Verdict string
	Pct     string
	DefFile string
}

// ahcFixture = 快速模式的一条 fixture 路由。
//
// ★ 三条里**故意**混了「零覆盖」与「有覆盖」⇒ 门才能同时验：
//
//	① 零覆盖的**必须**在台账里；② 有覆盖的**必须不在**台账里。
type ahcFixture struct {
	Path       string
	Method     string
	Handler    string
	RegFile    string
	DefFile    string
	ExpectZero bool
}

// ahcFixtures = 门用的极小路由清单（**不是**全集 —— 门只验证机制）。
//
// ★ handleListAuditLogs 在真实读数里就是零覆盖（实测 0 命中语句块）
//
//	⇒ 门断的「它必须在台账里」是真断言，不是空话。
//
// ★ handleCapabilities / handleChangePassword 在真实读数里是 covered
//
//	⇒ 门断的「它们必须不在台账里」也是真断言。
var ahcFixtures = []ahcFixture{
	{Path: "/api/admin/audit-logs", Method: "ANY", Handler: "handleListAuditLogs",
		RegFile: "admin/handler.go", DefFile: "admin/audit_log.go", ExpectZero: true},
	{Path: "/api/admin/capabilities", Method: "GET", Handler: "handleCapabilities",
		RegFile: "admin/capabilities.go", DefFile: "admin/capabilities.go", ExpectZero: false},
	{Path: "/api/auth/change-password", Method: "ANY", Handler: "handleChangePassword",
		RegFile: "admin/handler.go", DefFile: "admin/users.go", ExpectZero: false},
}

// ahcFuncStartLine 用 go/ast 定位 handler 的**函数体首行**（供 fixture 造 coverprofile）。
//
// ★ 用 go/ast 而不是「下一个顶格 }」：ast 给的是**精确**的函数体位置，
//
//	与量具的启发式（顶格 }）在正常 Go 代码上一致 ⇒ fixture 的语句块落在两者内部。
func ahcFuncStartLine(t *testing.T, root, relFile, handler string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, relFile), nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", relFile, err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil {
			continue
		}
		if fd.Name.Name == handler {
			return fset.Position(fd.Pos()).Line
		}
	}
	t.Fatalf("★ 在 %s 里找不到方法 %s ⇒ fixture 失效", relFile, handler)
	return 0
}

var ahcFastOnce sync.Once
var ahcFastRes []ahcResult
var ahcFastLedger map[string]ahcRow // 量具 --ledger 重生成后的台账（副本）
var ahcFastErr error

// ahcFastMode 跑一次量具的**快速模式**（自造 coverprofile + 3 条路由清单）。
func ahcFastMode(t *testing.T) []ahcResult {
	t.Helper()
	ahcFastOnce.Do(func() { ahcFastRes, ahcFastLedger, ahcFastErr = ahcFastRun(t) })
	if ahcFastErr != nil {
		t.Fatalf("★ 量具快速模式不可用 ⇒ 机制判据跑不了 ⇒ 拒绝出结论：\n%v", ahcFastErr)
	}
	return ahcFastRes
}

func ahcFastRun(t *testing.T) ([]ahcResult, map[string]ahcRow, error) {
	t.Helper()
	root, err := ahcRepoRootErr()
	if err != nil {
		return nil, nil, err
	}
	tool := filepath.Join(root, ahcToolRel)
	if _, err := os.Stat(tool); err != nil {
		return nil, nil, fmt.Errorf("量具不存在：%s ⇒ %v", tool, err)
	}
	dir, err := os.MkdirTemp("", "ahcfast")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)

	// ---- 自造 coverprofile：零覆盖的 handler 放 0 命中的块，有覆盖的放 >0 的块 ----
	var cover strings.Builder
	cover.WriteString("mode: set\n")
	var routes strings.Builder
	routes.WriteString("# path\tmethod\thandler\treg_file\n")
	for _, fx := range ahcFixtures {
		start := ahcFuncStartLine(t, root, fx.DefFile, fx.Handler)
		cnt := 1
		if fx.ExpectZero {
			cnt = 0
		}
		cover.WriteString(fmt.Sprintf("%s:%d.1,%d.1 1 %d\n", fx.DefFile, start, start, cnt))
		routes.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n", fx.Path, fx.Method, fx.Handler, fx.RegFile))
	}
	coverPath := filepath.Join(dir, "cover.out")
	if err := os.WriteFile(coverPath, []byte(cover.String()), 0o644); err != nil {
		return nil, nil, fmt.Errorf("写 coverprofile fixture 失败：%v", err)
	}
	routesPath := filepath.Join(dir, "routes.tsv")
	if err := os.WriteFile(routesPath, []byte(routes.String()), 0o644); err != nil {
		return nil, nil, fmt.Errorf("写路由清单 fixture 失败：%v", err)
	}
	outPath := filepath.Join(dir, "out.tsv")

	// ★ 只扫 admin/ ⇒ 快（实测 ≈ 2 s）；★ 不跑 go test（★ 门里绝不跑 ./admin/ 全量单测）
	cmd := exec.Command("bash", tool,
		"--root", root,
		"--cover", coverPath,
		"--routes", routesPath,
		"--scan-dirs", "admin",
		"--out", outPath,
	)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("★ 量具快速模式执行失败 ⇒ 拒绝出读数：\n%s", string(combined))
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, nil, fmt.Errorf("读量具输出失败：%v", err)
	}
	var res []ahcResult
	lines := strings.Split(string(raw), "\n")
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		cols := strings.Split(ln, "\t")
		if cols[0] == "path" {
			continue
		}
		if len(cols) < 6 {
			return nil, nil, fmt.Errorf("量具输出行列数 = %d，不足 6：%q", len(cols), ln)
		}
		res = append(res, ahcResult{
			Path: cols[0], Method: cols[1], Handler: cols[2],
			Verdict: cols[3], Pct: cols[4], DefFile: cols[5],
		})
	}
	if len(res) == 0 {
		return nil, nil, fmt.Errorf("★ 量具快速模式没有产出任何判定 ⇒ 分母为 0 ⇒ 拒绝出结论")
	}

	// ---- 第二遍：让量具走 --ledger 的 carry-over 路径 ----
	//
	// ★ 为什么必须跑这一遍（★ 门自己有牙的关键）：
	//   断言「**有覆盖的 handler 必须不在台账里**」只有在「台账是**量具自己重生成**的」
	//   前提下才有牙 —— 否则量具把「有覆盖」误判成「零覆盖」时，台账是旧的，
	//   旧台账里本来就没有它 ⇒ 断言恒真 ⇒ 门瞎。
	//   ⇒ 这里让量具拿**真台账的副本**重生成一次，再看它会不会把
	//     「有覆盖」的 fixture 写进台账（★ 写进去了 ⇒ 门必须红）。
	// ★ 真台账**只读**（量具只写副本）⇒ 对仓零写入。
	ledgerSrc := filepath.Join(root, ahcLedgerRel)
	rawLedger, err := os.ReadFile(ledgerSrc)
	if err != nil {
		return nil, nil, fmt.Errorf("读真台账失败（%s）：%v", ledgerSrc, err)
	}
	ledgerCopy := filepath.Join(dir, "ledger.tsv")
	if err := os.WriteFile(ledgerCopy, rawLedger, 0o644); err != nil {
		return nil, nil, fmt.Errorf("写台账副本失败：%v", err)
	}
	cmd2 := exec.Command("bash", tool,
		"--root", root,
		"--cover", coverPath,
		"--routes", routesPath,
		"--scan-dirs", "admin",
		"--ledger", ledgerCopy,
	)
	if combined2, err := cmd2.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("★ 量具 --ledger 路径执行失败 ⇒ 拒绝出读数：\n%s", string(combined2))
	}
	regen := map[string]ahcRow{}
	lf, err := os.Open(ledgerCopy)
	if err != nil {
		return nil, nil, fmt.Errorf("打开重生成台账失败：%v", err)
	}
	defer lf.Close()
	lsc := bufio.NewScanner(lf)
	lsc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for lsc.Scan() {
		line := lsc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if cols[0] == "path" {
			continue
		}
		if len(cols) < 14 {
			return nil, nil, fmt.Errorf("重生成台账行列数 = %d，不足 14：%q", len(cols), line)
		}
		r := ahcRow{
			Path: cols[0], Line: cols[1], Occ: cols[2], Method: cols[3], Handler: cols[4],
			RegFile: cols[5], DefFile: cols[6], Stmts: cols[7], Verdict: cols[8],
			Adjudication: cols[9], Reason: cols[10], Evidence: cols[11], Owner: cols[12],
			Fingerprint: cols[13],
		}
		if _, dup := regen[r.ahcKey()]; dup {
			return nil, nil, fmt.Errorf("★ 重生成台账主键重复：%q", r.ahcKey())
		}
		regen[r.ahcKey()] = r
	}
	if err := lsc.Err(); err != nil {
		return nil, nil, fmt.Errorf("读重生成台账出错：%v", err)
	}
	return res, regen, nil
}

// ahcFastRegen 取量具 --ledger 重生成后的台账（副本）。
func ahcFastRegen(t *testing.T) map[string]ahcRow {
	t.Helper()
	ahcFastMode(t) // 触发 once
	if ahcFastErr != nil {
		t.Fatalf("★ 量具快速模式不可用：\n%v", ahcFastErr)
	}
	if len(ahcFastLedger) == 0 {
		t.Fatalf("★ 量具重生成的台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论")
	}
	return ahcFastLedger
}

// ahcFindResult 按 (path, method) 找快速模式的结果。
func ahcFindResult(res []ahcResult, path, method string) (ahcResult, bool) {
	for _, r := range res {
		if r.Path == path && r.Method == method {
			return r, true
		}
	}
	return ahcResult{}, false
}

// ahcLedgerHasFingerprint 台账里有没有这个 fingerprint 的行。
func ahcLedgerHasFingerprint(ledger map[string]ahcRow, fp string) bool {
	for _, r := range ledger {
		if r.Fingerprint == fp {
			return true
		}
	}
	return false
}

// TestApiHandlerCoverageFilesExist：三个文件都在，量具语法检查通过。
func TestApiHandlerCoverageFilesExist(t *testing.T) {
	root := ahcRepoRoot(t)
	for _, rel := range []string{ahcToolRel, ahcLedgerRel, ahcGateRel} {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("★ 缺少文件 %s：%v", rel, err)
		}
	}
	// ★ 量具的 shell 语法检查
	cmd := exec.Command("bash", "-n", filepath.Join(root, ahcToolRel))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("★ 量具 bash -n 没过 ⇒ 量具本身语法有问题：\n%s", string(out))
	}
}

// TestApiHandlerCoverageLedgerIsNotEmpty：台账非空（分母 ≠ 0）。
//
// ★ 与 pipefail_race_ledger_test.go 同一条纪律：台账为空 ⇒ 「零覆盖为零」是恒等式，
//
//	不是发现 ⇒ 拒绝出结论。
func TestApiHandlerCoverageLedgerIsNotEmpty(t *testing.T) {
	ledger := ahcReadLedger(t)
	if len(ledger) == 0 {
		t.Fatal("★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论")
	}
	// ★ 台账里必须**全是**零覆盖（机器判定列恒为 zero_coverage）
	for k, r := range ledger {
		if r.Verdict != "zero_coverage" {
			t.Errorf("★ 台账 %s 的机器判定列 = %q（台账只该收 zero_coverage）", k, r.Verdict)
		}
	}
	t.Logf("台账行 = %d（全部为 zero_coverage）✔", len(ledger))
}

// TestApiHandlerCoverageLedgerAdjudicationIsComplete：每行都有**合法**的人工裁决，
// 且**不允许 pending**（静态判据只能给候选集，终值必须逐条人工做出）。
func TestApiHandlerCoverageLedgerAdjudicationIsComplete(t *testing.T) {
	ledger := ahcReadLedger(t)
	var pend []string
	for k, r := range ledger {
		if strings.TrimSpace(r.Adjudication) == "" {
			t.Errorf("★ 台账 %s 的人工裁决列是空的 ⇒ 不许空口断言", k)
			continue
		}
		if r.Adjudication == ahcPendingAdjudication {
			pend = append(pend, k)
		}
		if !ahcAdjudications[r.Adjudication] {
			t.Errorf("台账 %s 的裁决值 %q 不在允许集合 %v", k, r.Adjudication, ahcAdjudications)
		}
		if strings.TrimSpace(r.Reason) == "" {
			t.Errorf("★ 台账 %s 的理由列为空 ⇒ 「为何零覆盖」必须逐条写出来", k)
		}
	}
	sort.Strings(pend)
	if len(pend) > 0 {
		t.Errorf("★ 台账仍有 %d 行 %s（量具只能给候选集，终值必须逐条人工裁决）：\n%s",
			len(pend), ahcPendingAdjudication, strings.Join(pend, "\n"))
	}
}

// TestApiHandlerCoverageLedgerPrimaryKeyIsUnique：主键 (handler, fingerprint, occ) 唯一。
//
// ★ read 函数本身就会因重复而 Fatal；这里再显式断一次，
//
//	免得「以后有人改了 read 函数」就把这条纪律弄丢了。
func TestApiHandlerCoverageLedgerPrimaryKeyIsUnique(t *testing.T) {
	ledger := ahcReadLedger(t)
	seen := map[string]int{}
	for k := range ledger {
		seen[k]++
	}
	var dup []string
	for k, n := range seen {
		if n > 1 {
			dup = append(dup, k)
		}
	}
	sort.Strings(dup)
	if len(dup) > 0 {
		t.Errorf("★ 台账主键重复 %d 组（(handler, fingerprint, occ) 必须唯一）：\n%s",
			len(dup), strings.Join(dup, "\n"))
	}
	// ★ occ 必须从 1 起、且同 (handler, fingerprint) 组内连续
	groups := map[string][]int{}
	for _, r := range ledger {
		var occ int
		if _, err := fmt.Sscanf(r.Occ, "%d", &occ); err != nil {
			t.Errorf("★ 台账 %s 的 occ 列 %q 不是整数", r.ahcKey(), r.Occ)
			continue
		}
		if occ < 1 {
			t.Errorf("★ 台账 %s 的 occ = %d（应从 1 起）", r.ahcKey(), occ)
			continue
		}
		gk := r.Handler + "\t" + r.Fingerprint
		groups[gk] = append(groups[gk], occ)
	}
	for gk, occs := range groups {
		sort.Ints(occs)
		for i, o := range occs {
			if o != i+1 {
				t.Errorf("★ 组 %s 的 occ 序列 = %v（应为 1..%d 连续）", gk, occs, len(occs))
				break
			}
		}
	}
	t.Logf("同形组 %d 个，主键唯一且 occ 连续 ✔", len(groups))
}

// TestApiHandlerCoverageLedgerFingerprintMatchesTool：台账的 fingerprint 必须等于
// sha256(handler@def_file) 前 12 位 —— 量具与门不许各写一套。
func TestApiHandlerCoverageLedgerFingerprintMatchesTool(t *testing.T) {
	ledger := ahcReadLedger(t)
	bad := 0
	for k, r := range ledger {
		want := ahcFingerprint(r.Handler, r.DefFile)
		if want != r.Fingerprint {
			bad++
			if bad <= 10 {
				t.Errorf("★ 台账 %s 的 fingerprint = %q，期望 %q（sha256(%q@%q) 前 12 位）",
					k, r.Fingerprint, want, r.Handler, r.DefFile)
			}
		}
	}
	if bad > 10 {
		t.Errorf("★ 共 %d 处 fingerprint 与公式不符", bad)
	}
	t.Logf("fingerprint 与公式一致 ✔（%d 行）", len(ledger))
}

// TestApiHandlerCoverageFastModeMatchesLedger：★ 核心断言 ——
//
//	① 快速模式判成 zero_coverage 的 fixture **必须**在台账里（不许漏登）；
//	② 快速模式判成 covered 的 fixture **必须不在**台账里（台账不许赦免有覆盖的条目）；
//	③ 快速模式的判定必须与 fixture 的意图一致（量具没把 covered 判成零覆盖）。
func TestApiHandlerCoverageFastModeMatchesLedger(t *testing.T) {
	res := ahcFastMode(t)
	ledger := ahcReadLedger(t)
	regen := ahcFastRegen(t)

	for _, fx := range ahcFixtures {
		got, ok := ahcFindResult(res, fx.Path, fx.Method)
		if !ok {
			t.Errorf("★ 快速模式没有产出 %s %s 的判定 ⇒ 机制没跑起来", fx.Method, fx.Path)
			continue
		}
		want := "covered"
		if fx.ExpectZero {
			want = "zero_coverage"
		}
		if got.Verdict != want {
			t.Errorf("★ 快速模式把 %s 判成 %q（fixture 意图 = %q）⇒ 量具的判定逻辑有问题",
				fx.Handler, got.Verdict, want)
		}
		fp := ahcFingerprint(fx.Handler, fx.DefFile)
		inLedger := ahcLedgerHasFingerprint(ledger, fp)
		switch {
		case fx.ExpectZero && !inLedger:
			t.Errorf("★ %s 被量具判成零覆盖，却**不在**台账里 ⇒ 台账漏登了真实缺口", fx.Handler)
		case !fx.ExpectZero && inLedger:
			t.Errorf("★ %s 在真实读数里是 covered，却出现在台账里 ⇒ 台账拿有覆盖的条目赦免了",
				fx.Handler)
		}

		// ★★★ **有覆盖的必须不在台账里**这半边的「牙」：
		//   让量具拿真台账副本**重生成**一次（--ledger carry-over 路径），
		//   再看重生成结果 —— 若量具把「有覆盖」误判成零覆盖，
		//   它就会把这条写进台账 ⇒ 门在这里必须红。
		inRegen := ahcLedgerHasFingerprint(regen, fp)
		switch {
		case fx.ExpectZero && !inRegen:
			t.Errorf("★ %s 被量具判成零覆盖，却**不在**量具重生成的台账里 ⇒ carry-over 路径有问题",
				fx.Handler)
		case !fx.ExpectZero && inRegen:
			t.Errorf("★ %s 明明是 covered，却被量具写进了台账 ⇒ 台账被「有覆盖」的条目赦免了",
				fx.Handler)
		}
	}
	t.Logf("快速模式 %d 条 fixture：零覆盖的在台账里、有覆盖的不在台账里 ✔", len(ahcFixtures))
}

// TestApiHandlerCoverageGateHasTeeth：删掉台账一行 ⇒ 比对**必须**报出缺行。
//
// ★ 这是「门绿 ≠ 门有牙」的自检：证明判据 ①/④ 真的在检查，而不是恒真。
// ★ 只动内存副本（真台账零写入）。
func TestApiHandlerCoverageGateHasTeeth(t *testing.T) {
	res := ahcFastMode(t)
	ledger := ahcReadLedger(t)

	// 挑一条「本该在台账里」的零覆盖 fixture 当受害者
	var victimFP string
	for _, fx := range ahcFixtures {
		if !fx.ExpectZero {
			continue
		}
		fp := ahcFingerprint(fx.Handler, fx.DefFile)
		if ahcLedgerHasFingerprint(ledger, fp) {
			victimFP = fp
			break
		}
	}
	if victimFP == "" {
		t.Fatal("★ 找不到一条既在台账里、又被量具判成零覆盖的 fixture ⇒ 无法验证门有牙")
	}

	cut := map[string]ahcRow{}
	for k, r := range ledger {
		if r.Fingerprint == victimFP {
			continue
		}
		cut[k] = r
	}
	if len(cut) != len(ledger)-1 {
		t.Fatalf("★ 删受害者时出了意外：台账 %d 行 ⇒ 副本 %d 行", len(ledger), len(cut))
	}

	// 用同一个判据重新比：零覆盖的 fixture 必须仍在台账里
	missing := ""
	for _, fx := range ahcFixtures {
		if !fx.ExpectZero {
			continue
		}
		if _, ok := ahcFindResult(res, fx.Path, fx.Method); !ok {
			continue
		}
		fp := ahcFingerprint(fx.Handler, fx.DefFile)
		if !ahcLedgerHasFingerprint(cut, fp) {
			missing = fp
			break
		}
	}
	if missing == "" {
		t.Fatalf("★ 门没有牙：删掉台账行 %q 之后，比对**没有**报出该行缺失 ⇒ 判据恒真", victimFP)
	}
	t.Logf("门有牙 ✔ 删掉 %q 后报出缺行", victimFP)
}
