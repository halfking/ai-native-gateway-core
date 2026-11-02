package ursm_test

// pipefail_race_ledger_shape_test.go —— 台账的【形状】自检（2026-11-03 第三十轮）
//
// # 这道门要钉住的事
//
// ★ 台账是 TSV（13 列），**每一列都可能含有双引号、括号、星号等标点**
// （★ 实测：仓内 389 行里 **331 行合法地含双引号** ⇒ ⇒ ★ **TSV 不能用 csv 模块读**）
// ⇒ ⇒ ★ 台账的解析只能靠 `split("\t")` ⇒ ⇒ ★★ **而 `split("\t")` 对「列数不对」的行毫无保护**：
//
//	① 列数 **多于** 表头 ⇒ `zip(LEDGER_COLS, cols)` 会**静默丢掉多出来的列**
//	   ⇒ ⇒ ★★ 该行的 verdict / reason / evidence **整体错位**
//	   ⇒ ⇒ ★★ 而且 `cols[12]` 不再是 fingerprint ⇒ ★★ **主键也错位**
//	   ⇒ ⇒ ★★★ 结果：台账被**静默写坏**，而现有门只报「台账有 N 行不在候选集里」
//	     （★ 报的是**症状**，不是**病因** ⇒ ⇒ 下一个来的人会往错的方向查）
//	② 列数 **少于** 表头 ⇒ 会被 `continue` **静默丢弃**
//	   ⇒ ⇒ ★★ 那一行的**人工裁决凭空消失**
//
// ★★★ 实测来源（★ 第三十轮的真实事故）：
//   我自己的订正脚本用 `csv.DictReader` 读 TSV ⇒ ★ csv 有引号规则 ⇒ ★ 331 行含双引号
//   ⇒ ★ 读回来就错位 ⇒ ★ 一行被拆成两行 ⇒ ★ 台账里出现 verdict="verdict" 的畸形行
//   ⇒ ★ **写成 390 行**（正确是 389 行）⇒ ★ 而且**全程没有任何报错**
//
// ★★★ 这道门的作用：★ 把「形状坏了」从**症状**升级成**病因**，并且**指名行号**。
// ★★ 与既有门的分工：
//   · `TestPipefailRaceLedgerCoversCandidateSet` 管**键集**（台账行 vs 候选集）
//   · 本门管**形状**（每行的列数 vs 表头的列数）
//   ⇒ ⇒ ★★ 两道门都过 ⇒ 台账才既**完整**又**可被正确解析**。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// r30LedgerShapeError 描述台账的**一处**形状异常。
type r30LedgerShapeError struct {
	Line   int    // ★ 物理行号（1 起）
	Got    int    // ★ 实际列数
	Want   int    // ★ 表头列数
	Reason string // ★ 后果说明
}

func (e r30LedgerShapeError) Error() string {
	return "第 " + strconv.Itoa(e.Line) + " 行列数 = " + strconv.Itoa(e.Got) +
		"（表头 " + strconv.Itoa(e.Want) + " 列）⇒ " + e.Reason
}

// r30CheckLedgerShape 读台账并逐行核对**列数**。
//
// ★★ 为什么按**表头**列数而不是硬编码 13：
//
//	旧台账是 12 列（无 occ 列）⇒ ⇒ 量具支持零改动迁移
//	⇒ ⇒ 硬编码 13 会把「合法的旧台账」判成坏的 ⇒ ⇒ **门槛必须跟着表头走**。
func r30CheckLedgerShape(path string) []r30LedgerShapeError {
	raw, err := os.ReadFile(path)
	if err != nil {
		return []r30LedgerShapeError{{Line: 0, Got: -1, Want: -1,
			Reason: "打不开台账 " + path + "：" + err.Error()}}
	}
	var errs []r30LedgerShapeError
	want := 0
	for i, line := range strings.Split(string(raw), "\n") {
		lineno := i + 1
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if cols[0] == "file" {
			want = len(cols)
			continue
		}
		if want == 0 {
			errs = append(errs, r30LedgerShapeError{Line: lineno, Got: len(cols), Want: 0,
				Reason: "★ 台账**没有表头行** ⇒ 无法确定应有列数 ⇒ 台账不可信"})
			continue
		}
		if len(cols) == want {
			continue
		}
		reason := "多出来的 tab 会让字段**静默错位** ⇒ 该行裁决被改坏、且主键不再是 fingerprint"
		if len(cols) < want {
			reason = "列数不足 ⇒ 该行会被**静默丢弃** ⇒ 那一行的人工裁决凭空消失"
		}
		errs = append(errs, r30LedgerShapeError{Line: lineno, Got: len(cols), Want: want, Reason: reason})
	}
	return errs
}

// TestPipefailRaceLedgerShapeIsWellFormed：★ 台账每一数据行的列数必须 == 表头列数。
func TestPipefailRaceLedgerShapeIsWellFormed(t *testing.T) {
	root := r97RepoRoot(t)
	path := filepath.Join(root, "scripts", "ursmcheck", r97LedgerFile)
	errs := r30CheckLedgerShape(path)
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("★ 台账形状坏了：%s", e.Error())
		}
		t.Fatalf("★ 台账 %s 有 %d 处列数与表头不符 ⇒ ★ 台账可被**静默写坏** ⇒ 拒绝接受", path, len(errs))
	}
	t.Logf("✔ 台账 %s 的每一数据行列数都与表头一致（表头 %d 列）",
		filepath.Base(path), r30LedgerHeaderCols(t, path))
}

// r30LedgerHeaderCols 返回台账表头的列数（★ 供上面那条门打印）。
func r30LedgerHeaderCols(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("打不开台账 %s：%v", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "file\t") {
			return len(strings.Split(strings.TrimRight(line, "\r"), "\t"))
		}
	}
	t.Fatalf("台账 %s 没有表头行", path)
	return 0
}

// TestPipefailRaceLedgerShapeGateHasTeeth：★ 证明上面那道门**有牙**。
//
// ★ 变异：造一份**临时副本**，注入一行「reason 里含一个 tab」的行（★ 列数多出 2）
// ⇒ ⇒ 检查函数必须**报出来** ⇒ ⇒ 门红 ⇒ ⇒ 还原（★ 用文件副本，不碰真台账）。
func TestPipefailRaceLedgerShapeGateHasTeeth(t *testing.T) {
	root := r97RepoRoot(t)
	path := filepath.Join(root, "scripts", "ursmcheck", r97LedgerFile)

	// 基线：真台账必须是干净的（★ 否则下面的「变异被抓到」就没有说服力）
	if errs := r30CheckLedgerShape(path); len(errs) > 0 {
		t.Fatalf("★ 基线台账就已经有 %d 处形状异常 ⇒ 下面的变异臂没有说服力", len(errs))
	}

	// 造副本（★ 临时目录，不碰真台账）
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读台账失败：%v", err)
	}
	// ★ 注入一行：13 列 + 2 个多余的 tab（★ 模拟「reason 里手写了一个 tab」）
	bad := strings.Join([]string{
		"scripts/ursmcheck/__shape_mutant__.sh", "1", "1", "<top>",
		"head", "judgment", "unknown", "X", "benign-bare",
		"reason 里", "有", "两个", "多余的 tab", "deploy/", "deadbeefdeadbeef",
	}, "\t")
	mutated := string(raw) + bad + "\n"

	tmp, err := os.CreateTemp(t.TempDir(), "ledger-shape-*.tsv")
	if err != nil {
		t.Fatalf("建临时文件失败：%v", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(mutated); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("关临时文件失败：%v", err)
	}

	errs := r30CheckLedgerShape(tmpName)
	if len(errs) == 0 {
		t.Fatalf("★ 变异臂**没被抓到** ⇒ ⇒ 本门**没有牙** ⇒ 门是摆设")
	}
	found := false
	for _, e := range errs {
		if e.Got > e.Want {
			found = true
			t.Logf("✔ 变异被抓到：%s", e.Error())
		}
	}
	if !found {
		t.Fatalf("★ 变异臂报出的 %d 处异常里没有「列数多于表头」那类 ⇒ 门的判据写错了", len(errs))
	}
}
