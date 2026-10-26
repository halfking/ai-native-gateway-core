package ursm_test

// R95 轮 · 段 F 的门：**「两个独立口径 + 不一致即拒绝出读数」判据模板的行为契约**。
//
// 被测对象：scripts/audit/two-caliber-crosscheck.py
//
// 为什么需要它（§19.20.54 ⑧ 1 的待办）：
//   「双口径对拍」此前只活在某一个工具的写法里 ⇒ ⇒ 下一���量工具要「也这么做」时
//   只能重写一遍 ⇒ ⇒ 而重写出来的版本很可能【只打警告不拒绝】⇒ ⇒ 于是「分母被换掉」
//   这个根因（§19.20.54 ⑥ 的 12 条假变化）就会在下一个工具里原样复发。
//   ⇒ ⇒⇒ ⇒ 本门把这件事变成**仓内可复用的模板 + 一道有牙的门**。
//
// 这道门验证九件事：
//   1. 两口径一致 ⇒ 退出码 0（调用方可以给结论）
//   2. 键集不一致（eq）⇒ 退出码 2（★ 拒绝出读数）
//   3. 同一键上两侧读数不同 ⇒ 退出码 2
//   4. 一侧口径是空集 ⇒ 退出码 3（分母为 0 ⇒ 拒绝出结论，不是「不一致」）
//   5. 同一份口径里键重复 ⇒ 退出码 3
//   6. ★ 未声明 --independent ⇒ 退出码 3（★ 两侧若同源，「一致」是恒真的）
//   7. relation=superset / subset 的语义各自正确
//   8. ★ 恒等式自检：|A| = |A∩B| + |只A|（分母没被换掉的机器可验形式）
//   9. ★★ **有牙自检**：★ 断言必须锚在【两个实现真正给出不同读数的量】上
//      —— 即【差异明细里的具体键名】⇒ ⇒ 若有人把「不一致」判成「一致」，
//      这两个字段会变 ⇒ ⇒ 门会红 ⇒ ⇒ 而不能只断言「退出码非 0」这种在变异前后不变的量。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const tccChecker = "../audit/two-caliber-crosscheck.py"

// tccReport 是检查器 --json 输出的结构。
type tccReport struct {
	Consistent bool   `json:"consistent"`
	Relation   string `json:"relation"`
	Counts     struct {
		A             int `json:"a"`
		B             int `json:"b"`
		Inter         int `json:"inter"`
		OnlyA         int `json:"only_a"`
		OnlyB         int `json:"only_b"`
		ValueMismatch int `json:"value_mismatch"`
	} `json:"counts"`
	OnlyA         []string `json:"only_a"`
	OnlyB         []string `json:"only_b"`
	ValueMismatch []struct {
		Key string `json:"key"`
		A   string `json:"a"`
		B   string `json:"b"`
	} `json:"value_mismatch"`
	Identities []string `json:"identities"`
}

// tccRun 造两份口径文件并跑对拍器。
func tccRun(t *testing.T, a, b string, extraArgs ...string) (int, string, tccReport) {
	t.Helper()
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.tsv")
	bPath := filepath.Join(dir, "b.tsv")
	if err := os.WriteFile(aPath, []byte(a), 0o600); err != nil {
		t.Fatalf("写口径 A 失败: %v", err)
	}
	if err := os.WriteFile(bPath, []byte(b), 0o600); err != nil {
		t.Fatalf("写口径 B 失败: %v", err)
	}
	args := []string{tccChecker, "--a", aPath, "--b", bPath, "--independent", "--json"}
	args = append(args, extraArgs...)
	cmd := exec.Command("python3", args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("跑对拍器失败: %v\n%s", err, out)
	}
	var rep tccReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("解析 --json 输出失败: %v\n%s", err, out)
	}
	return code, string(out), rep
}

func TestTwoCaliberCrosscheckConsistentIsExitZero(t *testing.T) {
	a := "# 口径 A（观测口径）\n./admin\tTestA\tPASS\n./admin\tTestB\tPASS\n./db\tTestC\tPASS\n"
	b := "# 口径 B（期望口径）\n./admin\tTestA\tPASS\n./admin\tTestB\tPASS\n./db\tTestC\tPASS\n"
	code, out, rep := tccRun(t, a, b, "--key-cols", "0,1", "--value-col", "2")
	if code != 0 {
		t.Fatalf("一致时退出码应为 0，实际 %d\n%s", code, out)
	}
	if !rep.Consistent {
		t.Errorf("一致时 consistent 应为 true: %+v", rep)
	}
}

func TestTwoCaliberCrosscheckKeySetMismatchRefuses(t *testing.T) {
	// ★★ 有牙自检：★ 断言锚在【差异明细里的具体键名】上 ⇒ 变异「把不一致判成一致」会红
	a := "./admin\tTestA\tPASS\n./admin\tTestB\tPASS\n"
	b := "./admin\tTestA\tPASS\n./admin\tTestC\tPASS\n"
	code, out, rep := tccRun(t, a, b, "--key-cols", "0,1", "--value-col", "2")
	if code != 2 {
		t.Fatalf("★ 键集不一致时退出码应为 2（拒绝出读数），实际 %d\n%s", code, out)
	}
	if rep.Consistent {
		t.Errorf("★ 键集不一致时 consistent 应为 false: %+v", rep)
	}
	// ★ 差异明细必须点名「谁多谁少」⇒ ⇒ 判据不能只吐一个布尔
	if len(rep.OnlyA) != 1 || rep.OnlyA[0] != "./admin|TestB" {
		t.Errorf("★ only_a 应恰好是 [\"%.28s\"]，实际 %v", "./admin|TestB", rep.OnlyA)
	}
	if len(rep.OnlyB) != 1 || rep.OnlyB[0] != "./admin|TestC" {
		t.Errorf("★ only_b 应恰好是 [\"%.28s\"]，实际 %v", "./admin|TestC", rep.OnlyB)
	}
	if rep.Counts.Inter != 1 || rep.Counts.OnlyA != 1 || rep.Counts.OnlyB != 1 {
		t.Errorf("★ 计数口径不对: %+v", rep.Counts)
	}
}

func TestTwoCaliberCrosscheckValueMismatchRefuses(t *testing.T) {
	// 键集合相同、但同一对象上的读数不同 ⇒ 同样是「不一致」
	// ★ 键是复合的（包 + 函数名）⇒ 用 --key-cols 0,1
	a := "./admin\tTestA\tPASS\n"
	b := "./admin\tTestA\tFAIL\n"
	code, out, rep := tccRun(t, a, b, "--key-cols", "0,1", "--value-col", "2")
	if code != 2 {
		t.Fatalf("★ 值不一致时退出码应为 2，实际 %d\n%s", code, out)
	}
	if len(rep.ValueMismatch) != 1 {
		t.Fatalf("★ 值不一致应被点名 1 条，实际 %v", rep.ValueMismatch)
	}
	if rep.ValueMismatch[0].A != "PASS" || rep.ValueMismatch[0].B != "FAIL" {
		t.Errorf("★ 值不一致明细应带两侧读数: %+v", rep.ValueMismatch[0])
	}
}

func TestTwoCaliberCrosscheckEmptySideIsInvalid(t *testing.T) {
	// ★★ 一侧是空集 ⇒ 分母为 0 ⇒ 这是「没法比」（退出码 3），**不是**「不一致」（2）
	a := "./admin\tTestA\n"
	b := "# 只有注释，没有数据行\n"
	code, out, _ := tccRun(t, a, b)
	if code != 3 {
		t.Fatalf("★ 空口径应退出码 3（分母为 0），实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "空集") && !strings.Contains(out, "分母") {
		t.Errorf("★ 报错应点明「空集 / 分母为 0」，实际输出：%s", out)
	}
}

func TestTwoCaliberCrosscheckDuplicateKeyIsInvalid(t *testing.T) {
	a := "./admin\tTestA\n./admin\tTestA\n"
	b := "./admin\tTestA\n"
	code, out, _ := tccRun(t, a, b)
	if code != 3 {
		t.Fatalf("★ 键重复应退出码 3（分母不可信），实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "重复") {
		t.Errorf("★ 报错应点明「键重复」，实际输出：%s", out)
	}
}

func TestTwoCaliberCrosscheckRequiresIndependentDeclaration(t *testing.T) {
	// ★★ 未声明 --independent ⇒ 拒绝执行（退出码 3）
	//   理由：两侧若同源，「一致」是恒真的 ⇒ ⇒ 那道「对拍」没有判别力。
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.tsv")
	bPath := filepath.Join(dir, "b.tsv")
	if err := os.WriteFile(aPath, []byte("./admin\tTestA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("./admin\tTestA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("python3", tccChecker, "--a", aPath, "--b", bPath, "--json").CombinedOutput()
	if err == nil {
		t.Fatalf("★ 未声明 --independent 时应失败，实际成功：%s", out)
	}
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("★ 未声明 --independent 的退出码应为 3，实际 err=%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "independent") {
		t.Errorf("★ 报错应点明 --independent，实际输出：%s", out)
	}
}

func TestTwoCaliberCrosscheckRelations(t *testing.T) {
	a := "./a\t1\n./b\t2\n"
	b := "./a\t1\n"
	// superset：A ⊇ B ⇒ 只有 A 多出键时仍算一致
	code, out, rep := tccRun(t, a, b, "--relation", "superset")
	if code != 0 {
		t.Fatalf("★ relation=superset 且 A⊇B 时应退出 0，实际 %d\n%s", code, out)
	}
	if !rep.Consistent {
		t.Errorf("★ relation=superset 时 consistent 应为 true: %+v", rep)
	}
	// subset：A ⊆ B ⇒ A 多出键时应拒绝
	code, out, _ = tccRun(t, a, b, "--relation", "subset")
	if code != 2 {
		t.Fatalf("★ relation=subset 且 A⊄B 时应退出 2，实际 %d\n%s", code, out)
	}
}

func TestTwoCaliberCrosscheckIdentityHolds(t *testing.T) {
	// ★★ 恒等式：|A| = |A∩B| + |只A| ⇒ 分母没被换掉的机器可验形式
	// ★★ 夹具必须让 |只A| ≠ |只B| ⇒ 否则「把 |只A| 写成 |只B|」这类变异在夹具上是
	//   【空操作】⇒ ⇒ 门抓不到 ⇒ ⇒ ⇒ **变异必须能被门抓到**（否则它不是有效变异）。
	a := "./a\t1\n./b\t2\n./c\t3\n" // 3 个
	b := "./a\t1\n./z\t9\n"         // 2 个 ⇒ 交集 1、只A 2、只B 1
	_, out, rep := tccRun(t, a, b)
	if rep.Counts.A != 3 || rep.Counts.B != 2 || rep.Counts.Inter != 1 ||
		rep.Counts.OnlyA != 2 || rep.Counts.OnlyB != 1 {
		t.Fatalf("★ 计数不对: %+v\n%s", rep.Counts, out)
	}
	// 恒等式必须被打印出来（读者能自己验）
	if len(rep.Identities) != 2 {
		t.Fatalf("★ 应打印两条恒等式，实际 %d 条: %v", len(rep.Identities), rep.Identities)
	}
	// ★★ 门必须验【算术正确性】⇒ ★ 而不只是「串里有 ✓」
	//   ⇒ ⇒ 变异「把 |只A| 写成 |只B|」会被这条抓到（★ 靠 |只A| ≠ |只B| 的夹具）
	identRe := regexp.MustCompile(`\|([AB])\|=(\d+) = \|.∩.\|=(\d+) \+ \|只([AB])\|=(\d+)`)
	seen := map[string]bool{}
	for _, id := range rep.Identities {
		m := identRe.FindStringSubmatch(id)
		if m == nil {
			t.Fatalf("★ 恒等式格式不可解析: %q", id)
		}
		side, tot, inter, onlySide, only := m[1], m[2], m[3], m[4], m[5]
		totN, interN, onlyN := atoi(t, tot), atoi(t, inter), atoi(t, only)
		if totN != interN+onlyN {
			t.Errorf("★ 恒等式算术不成立: %q ⇒ %d != %d + %d", id, totN, interN, onlyN)
		}
		// ★ 恒等式里的「只X」必须真的是 X 侧的只数 ⇒ ★ 抓「张冠李戴」型变异
		wantOnly := rep.Counts.OnlyA
		if side == "B" {
			wantOnly = rep.Counts.OnlyB
		}
		if onlySide != side {
			t.Errorf("★ 恒等式 %q 的「只%s」与本侧不符（应只统计 %s 侧）", id, onlySide, side)
		}
		if onlyN != wantOnly {
			t.Errorf("★ 恒等式 %q 的「只%s」= %d，与实际计数 %d 不符", id, side, onlyN, wantOnly)
		}
		seen[side] = true
	}
	if len(seen) != 2 {
		t.Errorf("★ 应有 A/B 两条恒等式，实际 %v", seen)
	}
}

// atoi 是个只用于测试夹具的极简整数解析。
func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("★ 不是数字: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func TestTwoCaliberCrosscheckScopeFormatMatchesMatrix(t *testing.T) {
	// ★★ 形态对齐：★ 矩阵脚本产出的 scope.tsv 形态是 `pkg<TAB>^(TestA|TestB)$`
	//   ⇒ 本模板必须能直接吃这种「一格多名字」的清单（--value-col 指向正则列）。
	//   ⇒ 这是「观测覆盖自检能接入矩阵」的前置条件。
	a := "./admin\t^(TestA|TestB)$\n./db\t^(TestC)$\n"
	b := "./admin\t^(TestA|TestB)$\n./db\t^(TestC)$\n"
	code, out, rep := tccRun(t, a, b, "--value-col", "1")
	if code != 0 {
		t.Fatalf("★ scope 形态应能直接对拍，实际 %d\n%s", code, out)
	}
	if rep.Counts.A != 2 || rep.Counts.B != 2 {
		t.Errorf("★ scope 形态下计数不对: %+v", rep.Counts)
	}
	// ★ 形态真的不同 ⇒ 必须被认出来（不许把两个不同包当成同一个）
	a2 := "./admin\t^(TestA)$\n./db\t^(TestC)$\n"
	code, out, _ = tccRun(t, a2, b, "--value-col", "1")
	if code != 2 {
		t.Fatalf("★ 形态不同的 scope 应退出 2，实际 %d\n%s", code, out)
	}
}

// ── 接入层：「观测覆盖自检」接入矩阵读数的薄封装 ──────────────────────────────

const mvcWrapper = "../audit/matrix-verdict-crosscheck.sh"

// mvcFixture 造一份「矩阵输出目录」+ 一份「矩阵 stdout」。
//
// ★ 目录里要有 scope.tsv（形态 = `pkg<TAB>^(TestA|TestB)$`）与各档的日志；
// ★ 日志名 = `<档>__<包名把 / 换成 _>.log`；verdict 行 = `--- PASS: TestX`（零缩进）。
func mvcFixture(t *testing.T, tiers map[string]bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	scope := "./pkg\t^(TestA|TestB)$\n"
	if err := os.WriteFile(filepath.Join(dir, "scope.tsv"), []byte(scope), 0o600); err != nil {
		t.Fatal(err)
	}
	for tier, present := range tiers {
		if !present {
			continue
		}
		body := "--- PASS: TestA (0.01s)\n--- PASS: TestB (0.01s)\nPASS\n"
		if err := os.WriteFile(filepath.Join(dir, tier+"__pkg.log"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 矩阵 stdout：读数段（A1/B1/B2 各一行，PASS SKIP FAIL 合计）
	rows := ""
	for _, tier := range []string{"A1", "B1", "B2"} {
		total := 0
		if tiers[tier] {
			total = 2
		}
		rows += fmt.Sprintf("%s         2         0         0         %d\n", tier, total)
	}
	logText := "── 3. 读数 ──\n档       PASS   SKIP   FAIL     合计\n" + rows
	logPath := filepath.Join(dir, "matrix.log")
	if err := os.WriteFile(logPath, []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, logPath
}

func mvcRun(t *testing.T, outDir, logPath string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", mvcWrapper, "--matrix-log", logPath, "--out", outDir)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("跑封装失败: %v\n%s", err, out)
	}
	return code, string(out)
}

func TestMatrixVerdictCrosscheckRefusesWhenTierMissing(t *testing.T) {
	// ★★ 真实负样本的形态：★ A1 跑全了（合计 == 期望），B1/B2 根本没跑（合计 0）
	//   ⇒ ⇒ 矩阵自己的汇总会把 B1/B2 印成 0 并据此报出「A1→B1 判定变化 92 条」
	//   ⇒ ⇒ ★ 本封装必须【拒绝出读数】。
	outDir, logPath := mvcFixture(t, map[string]bool{"A1": true})
	code, out := mvcRun(t, outDir, logPath)
	if code != 2 {
		t.Fatalf("★ 有档未跑全时应退出 2（拒绝出读数），实际 %d\n%s", code, out)
	}
	// ★ 必须点名是哪一档不一致 ⇒ 判据不能只吐一个退出码
	if !strings.Contains(out, "B1") || !strings.Contains(out, "B2") {
		t.Errorf("★ 输出必须点名不一致的档（B1/B2），实际：%s", out)
	}
	if !strings.Contains(out, "【最终裁决·不一致】") {
		t.Errorf("★ 输出必须打上【最终裁决·不一致】标记（★ 不能只靠「拒绝出读数」四个字 —— 对拍器自己也会打 ⇒ 断言锚不住），实际：%s", out)
	}
}

func TestMatrixVerdictCrosscheckPassesWhenAllTiersComplete(t *testing.T) {
	outDir, logPath := mvcFixture(t, map[string]bool{"A1": true, "B1": true, "B2": true})
	code, out := mvcRun(t, outDir, logPath)
	if code != 0 {
		t.Fatalf("★ 三档都跑全时应退出 0（可以给结论），实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "【最终裁决·一致】") {
		t.Errorf("★ 输出必须打上【最终裁决·一致】标记，实际：%s", out)
	}
}

func TestMatrixVerdictCrosscheckMissingInputIsInvalid(t *testing.T) {
	outDir, _ := mvcFixture(t, map[string]bool{"A1": true})
	code, out := mvcRun(t, outDir, filepath.Join(outDir, "nope.log"))
	if code != 3 {
		t.Fatalf("★ 输入缺失应退出 3，实际 %d\n%s", code, out)
	}
	// ★ 矩阵 stdout 存在但读数段解析不出来 ⇒ 也是「没法比」⇒ 3（不是 2）
	badLog := filepath.Join(outDir, "bad.log")
	if err := os.WriteFile(badLog, []byte("no read-number section here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = mvcRun(t, outDir, badLog)
	if code != 3 {
		t.Fatalf("★ 读数段解析不出应退出 3，实际 %d\n%s", code, out)
	}
}

// ── v2（R97 轮）：表头锚定解析 + 「口径 D 零漂移」对拍 ─────────────────────────
//
// ★★ v1 被上游 R96 轮**实测打断**：上游给读数表加了一列（「未观测」）⇒
//   v1 的 `^([A-Z][0-9]+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)$` 直接失配。
//   ⇒ v1 的失效模式是 fail-closed（exit 3）★ 方向对，但「上游加一列就红」是脆弱点。
//   ⇒ v2 改成**表头锚定**：找表头里叫「合计」的那一列 ⇒ 加列 / 调列序都不再打断它。

// mvcMatrixLog 造一份矩阵 stdout：header 是表头列名序列（第一个是档名列），
// values 是每档的各列数值（顺序与 header[1:] 对齐）。
func mvcMatrixLog(t *testing.T, dir, name string, header []string, values map[string][]int) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("── 3. 读数 ──\n")
	sb.WriteString(strings.Join(header, " ") + "\n")
	for _, tier := range []string{"A1", "B1", "B2"} {
		cells := []string{tier}
		for _, n := range values[tier] {
			cells = append(cells, strconv.Itoa(n))
		}
		sb.WriteString(strings.Join(cells, " ") + "\n")
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// mvcScopeDir 造一个「矩阵输出目录」：scope.tsv + 每档日志（+ 可选探针日志）。
func mvcScopeDir(t *testing.T, tiers map[string]bool, probeLog string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scope.tsv"),
		[]byte("./pkg\t^(TestA|TestB)$\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for tier, present := range tiers {
		if !present {
			continue
		}
		body := "--- PASS: TestA (0.01s)\n--- PASS: TestB (0.01s)\nPASS\n"
		if err := os.WriteFile(filepath.Join(dir, tier+"__pkg.log"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if probeLog != "" {
		if err := os.WriteFile(filepath.Join(dir, "scope_probe.log"), []byte(probeLog), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMatrixVerdictCrosscheckParsesByHeaderAnchor(t *testing.T) {
	// ★★ 表头锚定必须对**三种表形态**给同一结论：
	//   ① 旧格式（4 列：PASS/SKIP/FAIL/合计）—— v1 时代的形态
	//   ② 新格式（5 列：上游 R96 加了「未观测」）—— ★ v1 在这里直接失配
	//   ③ 列序打乱（合计不在第 4 位）—— ★ 证明锚的是「名字」不是「位置」
	//   ⇒ 三者都必须给出**同一个退出码** ⇒ ⇒ 解析与列数、列序无关。
	cases := []struct {
		name   string
		header []string
		values map[string][]int
	}{
		{"旧格式4列", []string{"档", "PASS", "SKIP", "FAIL", "合计"},
			map[string][]int{"A1": {2, 0, 0, 2}, "B1": {0, 0, 0, 0}, "B2": {0, 0, 0, 0}}},
		{"新格式5列", []string{"档", "PASS", "SKIP", "FAIL", "合计", "未观测"},
			map[string][]int{"A1": {2, 0, 0, 2, 0}, "B1": {0, 0, 0, 0, 2}, "B2": {0, 0, 0, 0, 2}}},
		{"列序打乱", []string{"档", "合计", "PASS", "SKIP", "FAIL", "未观测"},
			map[string][]int{"A1": {2, 2, 0, 0, 0}, "B1": {0, 0, 0, 0, 2}, "B2": {0, 0, 0, 0, 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := mvcScopeDir(t, map[string]bool{"A1": true}, "")
			logPath := mvcMatrixLog(t, dir, "matrix.log", tc.header, tc.values)
			code, out := mvcRun(t, dir, logPath)
			// ★ 统一期望：B1/B2 的合计 = 0 ≠ 期望 2 ⇒ 拒绝出读数 ⇒ exit 2
			if code != 2 {
				t.Fatalf("★ %s：B1/B2 未跑全时应退出 2（拒绝出读数），实际 %d\n%s", tc.name, code, out)
			}
			if !strings.Contains(out, "B1") || !strings.Contains(out, "B2") {
				t.Errorf("★ %s：输出必须点名不一致的档（B1/B2），实际：%s", tc.name, out)
			}
			// ★★ 锚定必须落在「合计」那一列上 ⇒ 输出里的口径 A 必须是**合计**，
			//    而不是恰好同值的别的列 ⇒ ⇒ 用一个「合计 ≠ PASS」的夹具把它钉住。
			//    （本夹具里 A1 的 PASS=2、合计=2 同值 ⇒ 下面另有一条专门不等值的用例。）
		})
	}
	// ★★ 专门一条：PASS 与合计**不同值** ⇒ 口径 A 必须取合计
	dir := mvcScopeDir(t, map[string]bool{"A1": true, "B1": true, "B2": true}, "")
	logPath := mvcMatrixLog(t, dir, "m2.log",
		[]string{"档", "PASS", "SKIP", "FAIL", "合计", "未观测"},
		map[string][]int{"A1": {1, 1, 0, 2, 0}, "B1": {1, 1, 0, 2, 0}, "B2": {1, 1, 0, 2, 0}})
	code, out := mvcRun(t, dir, logPath)
	if code != 0 {
		t.Fatalf("★ 三档齐全且合计==期望时应退出 0，实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "A1=2") {
		t.Errorf("★ 口径 A 必须取「合计」列（=2）而不是 PASS 列（=1），实际输出：%s", out)
	}
}

func TestMatrixVerdictCrosscheckHeaderMissingIsInvalid(t *testing.T) {
	// ★ 表头里没有「合计」⇒ 读数段形态已变 ⇒ **没法比** ⇒ exit 3（不是 2）
	dir := mvcScopeDir(t, map[string]bool{"A1": true}, "")
	logPath := filepath.Join(dir, "noheader.log")
	body := "── 3. 读数 ──\n档 PASS SKIP FAIL 计数\nA1 2 0 0 2\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := mvcRun(t, dir, logPath)
	if code != 3 {
		t.Fatalf("★ 表头缺「合计」应退出 3，实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "合计") {
		t.Errorf("★ 报错应点明缺的是「合计」列，实际：%s", out)
	}
	if !strings.Contains(out, "【最终裁决·输入无效】") {
		t.Errorf("★ 必须打上【最终裁决·输入无效】标记（★ 断言要锚在封装自己的裁决上），实际：%s", out)
	}
}

// ── 「口径 D 零漂移」对拍 ───────────────────────────────────────────────────
//
// ★ 背景：上游矩阵脚本的口径 D（结论口径）由一个**运行时生成的探针**算出，
//   探针**直接调用官方门的未导出函数** ⇒ **判据原语**层零漂移（已核实成立）；
//   但探针**自带一份「文件选取」副本** ⇒ ⇒ **选取**层并不零漂移
//   ⇒ ⇒ 官方门一改而探针没改 ⇒ 口径 D 静默漂移 ⇒ 矩阵读数照常发布。
//   ⇒ ⇒ 这条「零漂移」承诺今天恰好成立，但**没有任何东西守着它**。
//   ⇒ ⇒ 本门把它变成两个独立口径的对拍：
//       口径 A′ = 探针 E 桶的 PG 文件数（探针日志）
//       口径 B′ = 官方门本体数到的 PG 文件数（官方门日志）

const mvcProbeLogE = "PROBE-CAL E files=293 functions=889  # 官方门自己的分母\n"
const mvcGateLog293 = "    datasource_env_test.go:431: 扫到 293 个打开 PG 的测试文件、12 个 DSN 类变量名；事实源导出 12 个、豁免 0 个、未覆盖 0 个\n"

func mvcRunWithGate(t *testing.T, outDir, logPath, gateLog string) (int, string) {
	t.Helper()
	args := []string{mvcWrapper, "--matrix-log", logPath, "--out", outDir}
	if gateLog != "" {
		args = append(args, "--gate-log", gateLog)
	}
	cmd := exec.Command("bash", args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("跑封装失败: %v\n%s", err, out)
	}
	return code, string(out)
}

func TestMatrixVerdictCrosscheckZeroDriftDetectsDivergence(t *testing.T) {
	// ★★ 漂移可被检出：探针说 293、官方门本体说 294 ⇒ 必须拒绝出读数（exit 2）
	//   ⇒ 且**必须点名两个数** ⇒ ⇒ 判据不能只吐一个退出码。
	dir := mvcScopeDir(t, map[string]bool{"A1": true, "B1": true, "B2": true}, mvcProbeLogE)
	logPath := mvcMatrixLog(t, dir, "m.log",
		[]string{"档", "PASS", "SKIP", "FAIL", "合计", "未观测"},
		map[string][]int{"A1": {2, 0, 0, 2, 0}, "B1": {2, 0, 0, 2, 0}, "B2": {2, 0, 0, 2, 0}})
	gatePath := filepath.Join(dir, "gate.log")
	if err := os.WriteFile(gatePath,
		[]byte(strings.Replace(mvcGateLog293, "扫到 293", "扫到 294", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := mvcRunWithGate(t, dir, logPath, gatePath)
	if code != 2 {
		t.Fatalf("★ 零漂移对拍不一致时应退出 2（拒绝出读数），实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "293") || !strings.Contains(out, "294") {
		t.Errorf("★ 输出必须点名两侧的数（293 / 294），实际：%s", out)
	}
	if !strings.Contains(out, "漂移") {
		t.Errorf("★ 输出必须点明「漂移」，实际：%s", out)
	}
	if !strings.Contains(out, "【判据失败·不一致】") {
		t.Errorf("★ 必须打上【判据失败·不一致】标记，实际：%s", out)
	}
}

func TestMatrixVerdictCrosscheckZeroDriftEqualIsExitZero(t *testing.T) {
	// ★ 两侧相等 ⇒ 判据 3 通过 ⇒ 整体 exit 0
	dir := mvcScopeDir(t, map[string]bool{"A1": true, "B1": true, "B2": true}, mvcProbeLogE)
	logPath := mvcMatrixLog(t, dir, "m.log",
		[]string{"档", "PASS", "SKIP", "FAIL", "合计", "未观测"},
		map[string][]int{"A1": {2, 0, 0, 2, 0}, "B1": {2, 0, 0, 2, 0}, "B2": {2, 0, 0, 2, 0}})
	gatePath := filepath.Join(dir, "gate.log")
	if err := os.WriteFile(gatePath, []byte(mvcGateLog293), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := mvcRunWithGate(t, dir, logPath, gatePath)
	if code != 0 {
		t.Fatalf("★ 零漂移对拍一致时应退出 0，实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "零漂移") {
		t.Errorf("★ 输出应点明判据 3 是零漂移对拍，实际：%s", out)
	}
	if !strings.Contains(out, "【最终裁决·一致】") {
		t.Errorf("★ 必须打上【最终裁决·一致】标记，实际：%s", out)
	}
}

func TestMatrixVerdictCrosscheckZeroDriftUnreadableIsInvalid(t *testing.T) {
	// ★ 被要求做零漂移对拍、但**拿不到可比读数** ⇒ 不是「一致」也不是「不一致」
	//   ⇒ 是「没法比」⇒ exit 3 ⇒ ⇒ ★ 不许静默 SKIP（否则这道判据会形同虚设）。
	dir := mvcScopeDir(t, map[string]bool{"A1": true, "B1": true, "B2": true}, mvcProbeLogE)
	logPath := mvcMatrixLog(t, dir, "m.log",
		[]string{"档", "PASS", "SKIP", "FAIL", "合计", "未观测"},
		map[string][]int{"A1": {2, 0, 0, 2, 0}, "B1": {2, 0, 0, 2, 0}, "B2": {2, 0, 0, 2, 0}})
	// 官方门日志存在，但**没有那条计数行**（官方门自己先失败了）
	gatePath := filepath.Join(dir, "gate.log")
	if err := os.WriteFile(gatePath, []byte("FAIL: 官方门自己先失败了\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := mvcRunWithGate(t, dir, logPath, gatePath)
	if code != 3 {
		t.Fatalf("★ 拿不到可比读数应退出 3，实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "【判据失败·输入无效】") {
		t.Errorf("★ 必须打上【判据失败·输入无效】标记（★ 不能锚在「可比读数」四个字上 —— python 的 stderr 里也有它 ⇒ 断言会被顶替 ⇒ 门就没牙了），实际：%s", out)
	}
	// ★★ 再锚一层：**封装自己的消息全文**（python 的 stderr 说的是「没给出可比读数」，
	//   封装说的是「拿不到可比读数」⇒ 两者字面不同 ⇒ ⇒ 只有走对了分支才会出现）。
	//   ⇒ ★ 标记是粗粒度（多种失败共用），**消息全文**才是细粒度 ⇒ ⇒ 两者都要断言。
	if !strings.Contains(out, "零漂移对拍拿不到可比读数") {
		t.Errorf("★ 必须出现封装自己的消息「零漂移对拍拿不到可比读数」（★ 与 python 的「没给出可比读数」字面不同 ⇒ 可区分），实际：%s", out)
	}
}

func TestMatrixVerdictCrosscheckSkipsDriftWhenNotRequested(t *testing.T) {
	// ★ 没给 --gate-log / --wt ⇒ 零漂移对拍**显式 SKIP 且说清理由**
	//   ⇒ ★ 必需要件必须条件化；但 SKIP 不能是「悄悄跳过」。
	dir := mvcScopeDir(t, map[string]bool{"A1": true, "B1": true, "B2": true}, "")
	logPath := mvcMatrixLog(t, dir, "m.log",
		[]string{"档", "PASS", "SKIP", "FAIL", "合计", "未观测"},
		map[string][]int{"A1": {2, 0, 0, 2, 0}, "B1": {2, 0, 0, 2, 0}, "B2": {2, 0, 0, 2, 0}})
	code, out := mvcRun(t, dir, logPath)
	if code != 0 {
		t.Fatalf("★ 未要求零漂移对拍时应仍能出结论（exit 0），实际 %d\n%s", code, out)
	}
	if !strings.Contains(out, "【判据3·SKIP】") {
		t.Errorf("★ 未做的判据必须显式打【判据3·SKIP】标记，实际：%s", out)
	}
	if !strings.Contains(out, "零漂移") {
		t.Errorf("★ SKIP 也要点名是哪一段判据，实际：%s", out)
	}
}
