package main

// 端到端判据：**撞名不变量必须真的在跑的那条路上**。
//
// # 这条存在的唯一理由是变异 F4（快照新鲜度门）
//
// 那个变异把 `main` 里的调用点摘掉（`if *maxSnapshotAge > 0` → `if false`），
// 57 条判据**全绿** —— 因为前面每一条都直接调 `judgeSnapshotAge`，从不经过接线。
// 于是「函数有牙」被当成了「门在跑」。
//
// ⇒ 这里跑**真正的二进制**，用现场搭的 raw/ 目录摆出那个真实形状
// （anthropic 页：同一个展示名在两张表里各有一行，$5/$25 与 $10/$50），
// 断言这个价**没有**出现在 ready_to_review，而同页的对照模型**在**。
//
// # 为什么要有对照模型
//
// 没有对照的话，「全部被拒」与「门在跑」在这条判据上表现完全一样 ——
// 恒真的判据比没有判据更坏。所以 claude-sonnet-4-5 必须在 ready_to_review 里，
// 它的价格是这条判据的量具自证。
import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// collisionProposal 是这条判据要读的那几栏。
type collisionProposal struct {
	Counts         map[string]int  `json:"counts_by_confidence"`
	ReadyToReview  []proposalEntry `json:"ready_to_review"`
	AmbiguousNames []struct {
		Canonical string `json:"canonical"`
		Rows      int    `json:"rows"`
		Distinct  int    `json:"distinct_prices"`
		Prices    []struct {
			Input  *float64 `json:"input_per_1m"`
			Output *float64 `json:"output_per_1m"`
			Row    string   `json:"row"`
			LineNo int      `json:"line_no"`
		} `json:"prices"`
	} `json:"ambiguous_canonical_prices"`
}

// runProposalToolForTest 跑**真正的二进制**并回读提案。
//
// 为什么用 `go run .` 而不是预先 `go build` 出一个二进制：后者会在仓库根留下
// 一个同名可执行文件（2026-10-06 事故：`git add -A` 把它提交进 9d0401a1e）。
func runProposalToolForTest(t *testing.T, args ...string) ([]byte, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: this builds and runs the tool")
	}
	out := filepath.Join(t.TempDir(), "proposal.json")
	cmd := exec.Command("go", "run", ".")
	cmd.Args = append(cmd.Args, append(args, "-out", out)...)
	cmd.Dir = "."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = os.Stdout
	cmd.Env = append(os.Environ(), "GOFLAGS=")

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("go run failed: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatalf("go run timed out; stderr so far:\n%s", stderr.String())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read proposal: %v\nstderr:\n%s", err, stderr.String())
	}
	return body, stderr.String()
}

// writeTwoTableSameModelSnapshot 摆出真实形状：同一页两张表，同一个展示名两行、价不同，
// 外加一个只出现一次的对照模型。
func writeTwoTableSameModelSnapshot(t *testing.T, dir string) {
	t.Helper()
	body := "Title: fixture pricing\nURL Source: https://docs.anthropic.invalid/en/docs/pricing\n" +
		"\nMarkdown Content:\n\n" +
		"| Model | Input | Output |\n| --- | --- | --- |\n" +
		"| Claude Opus 4.8 | $5 / MTok | $25 / MTok |\n\n" +
		"| Model | Input | Output |\n| --- | --- | --- |\n" +
		"| Claude Opus 4.8 | $10 / MTok | $50 / MTok |\n" +
		"| Claude Sonnet 4.5 | $3 / MTok | $15 / MTok |\n"
	if err := os.WriteFile(filepath.Join(dir, "anthropic.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write anthropic.md: %v", err)
	}
}

func writeTwoNameCanonicalList(t *testing.T, path string) {
	t.Helper()
	body := "# source: models_canonical @ 127.0.0.1:5432 (SELECT canonical_name FROM models_canonical)\n" +
		"# exported_at: 2026-10-05T06:01:34Z\n" +
		"# count: 2\n" +
		"claude-opus-4-8\n" +
		"claude-sonnet-4-5\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write canonical list: %v", err)
	}
}

func TestThePriceConflictInvariantIsActuallyWiredIntoTheRun(t *testing.T) {
	// ★ 文件名必须是 vendorPage 里登记过的真名，否则整页会被
	//   「not an originating vendor in the map」跳过 —— 那样下面所有断言
	//   都变成「什么都没通过」，是恒绿。
	rawDir := t.TempDir()
	writeTwoTableSameModelSnapshot(t, rawDir)
	canon := filepath.Join(t.TempDir(), "canonical.txt")
	writeTwoNameCanonicalList(t, canon)

	body, stderr := runProposalToolForTest(t,
		"-raw", rawDir,
		"-canonical", canon,
		"-fetched-at", "2026-10-06T00:00:00Z",
	)
	var p collisionProposal
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("parse proposal: %v", err)
	}

	// 量具自证（承重）：对照模型必须在 ready_to_review 里，而且价对。
	// 没有这一条，下面所有「没进去」的断言都能被「全被拒」满足。
	if !hasPrice(p.ReadyToReview, 3) {
		t.Fatalf("control model missing from ready_to_review (%d entry/entries) — this test "+
			"cannot tell the invariant from a tool that rejects everything:\n%s",
			len(p.ReadyToReview), stderr)
	}

	// 断言一：撞名的那个价一个都不许留在 ready_to_review。
	// 变异 C1（把调用点摘掉）时 $5 或 $10 就在这里出现。
	for _, c := range p.ReadyToReview {
		if c.Input != nil && (*c.Input == 5 || *c.Input == 10) {
			t.Errorf("a price for a canonical with two conflicting prices (%v/%v) reached "+
				"ready_to_review — the conflict invariant is not on the run path; whichever "+
				"survived becomes the baseline price by accident of table order",
				strPtr(c.Input), strPtr(c.Input))
		}
	}
	for _, c := range p.ReadyToReview {
		if c.Model == "Claude Opus 4.8" {
			t.Errorf("ready_to_review still carries a row for the conflicting model %q (%v) — "+
				"BOTH rows must be withheld; a human picks, not the document order",
				c.Model, strPtr(c.Input))
		}
	}

	// 断言二：两行必须**都**出现在结构化字段里 —— 扣下不等于丢钱。
	if len(p.AmbiguousNames) != 1 {
		t.Fatalf("want exactly 1 ambiguous_canonical_prices entry, got %d (stderr:\n%s)",
			len(p.AmbiguousNames), stderr)
	}
	a := p.AmbiguousNames[0]
	if a.Canonical != "claude-opus-4-8" {
		t.Errorf("ambiguous entry names %q, want claude-opus-4-8", a.Canonical)
	}
	if a.Rows != 2 || a.Distinct != 2 {
		t.Errorf("ambiguous entry must account for 2 rows / 2 distinct prices, got rows=%d distinct=%d",
			a.Rows, a.Distinct)
	}
	seen := map[float64]bool{}
	for _, pr := range a.Prices {
		if pr.Input != nil {
			seen[*pr.Input] = true
		}
		if pr.LineNo == 0 {
			t.Errorf("a withheld price lost its line number — the reader cannot go back to the page")
		}
		if pr.Row == "" {
			t.Errorf("a withheld price lost its source row text")
		}
	}
	if !seen[5] || !seen[10] {
		t.Errorf("both conflicting prices ($5 and $10) must be listed in the ambiguous entry, got %v",
			seen)
	}

	// 断言三：提取计数**不因这条不变量而改**。这正是 Notice 里那句话的意思，
	// 也是「人拿 counts_by_confidence 当 ready 条数就会错」的根据：
	// 3 条 table_row，1 条 ready，2 条在 ambiguous 里。
	if p.Counts["table_row"] != 3 {
		t.Errorf("counts_by_confidence must still report every extracted row (3 table_row), got %v — "+
			"if this changed, the Notice's warning is wrong", p.Counts)
	}

	// 断言四：结论必须在提案**文件**里（不是只 stderr），并且 stderr 上有人话。
	if !strings.Contains(stderr, "price-conflict") {
		t.Errorf("the run must announce the conflict on stderr so an operator sees it without "+
			"opening the JSON:\n%s", stderr)
	}
}

func strPtr(f *float64) string {
	if f == nil {
		return "nil"
	}
	return fmt.Sprintf("%g", *f)
}
