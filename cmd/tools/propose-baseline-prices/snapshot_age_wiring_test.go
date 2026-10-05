package main

// 端到端判据：**门必须真的被调用**。
//
// ★ 这条存在的唯一理由是变异 F4。F1–F3 证明 `judgeSnapshotAge` 的判定逻辑
//   有牙，但把 `main` 里的**调用点**摘掉（`if *maxSnapshotAge > 0` → `if false`）
//   时，57 条判据**全绿** —— 因为前面那几条都直接调函数，从不经过接线。
//
//   ⇒ 「函数有牙」与「门在跑」是两件事。这里跑**真正的二进制**，
//   用一个现场搭的 raw/ 目录，一份陈旧快照 + 一份新鲜快照，
//   断言陈旧那份的价**没有**出现在提案里，而新鲜那份在。
import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// proposalEntry 是提案里 ready_to_review 的最小形状 —— 只取本判据要断言的字段。
type proposalEntry struct {
	Model string   `json:"model"`
	Input *float64 `json:"input_per_1m"`
}

// writeFixtureSnapshot 写一份带指定 Published Time 的快照，价格是唯一的那个数。
func writeFixtureSnapshot(t *testing.T, dir, name, published, price string) {
	t.Helper()
	body := "Title: fixture\nURL Source: https://api.example.invalid/pricing\n"
	if published != "" {
		body += "Published Time: " + published + "\n"
	}
	body += "\nMarkdown Content:\n\n" +
		"| Model | Input | Output |\n| --- | --- | --- |\n" +
		"| fixture-model | " + price + " / MTok | " + price + " / MTok |\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestTheFreshnessGateIsActuallyWiredIntoTheRun(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this builds and runs the tool")
	}
	// 陈旧：内容是 84 天前的版本，而你声称今天抓的。
	stale := "Tue, 02 Jun 2026 05:31:59 GMT"
	// 新鲜：内容 2 天前的版本。
	fresh := "Thu, 03 Oct 2026 05:31:59 GMT"
	// 无日期：应当**放行但记录**（不能与「新鲜」混为一谈）。
	noDate := ""

	// ★ 文件名必须是 vendorPage 里登记过的真名，否则整页会以
	//   「not an originating vendor in the map」被跳过 —— 那样这条判据就会
	//   因为「什么都没通过」而全绿，是最典型的假绿。
	realDir := t.TempDir()
	writeFixtureSnapshot(t, realDir, "deepseek.md", stale, "$1.11") // 陈旧
	writeFixtureSnapshot(t, realDir, "openai.md", fresh, "$3.33")   // 新鲜
	writeFixtureSnapshot(t, realDir, "xai.md", noDate, "$4.44")     // 无日期

	out := filepath.Join(t.TempDir(), "proposal.json")
	cmd := exec.Command("go", "run", ".",
		"-raw", realDir,
		"-out", out,
		"-fetched-at", "2026-10-05T00:00:00Z",
		"-max-snapshot-age-days", "30",
	)
	// 让 go run 在测试自己的模块里跑，且不继承 GOFLAGS 之类的东西。
	cmd.Dir = "."
	var stderr strings.Builder
	cmd.Stderr = &stderr
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

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read proposal: %v\nstderr:\n%s", err, stderr.String())
	}
	var p struct {
		ReadyToReview     []proposalEntry `json:"ready_to_review"`
		SnapshotFreshness []struct {
			File    string `json:"file"`
			Verdict string `json:"verdict"`
		} `json:"snapshot_freshness"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("parse proposal: %v", err)
	}

	// 断言一：陈旧快照的价 $1.11 **没有**进提案。门没被调用时它会进，
	//         而这里正是 F4 摘掉调用点后要变红的地方。
	for _, c := range p.ReadyToReview {
		if c.Input != nil && *c.Input == 1.11 {
			t.Errorf("a price from an 84-day-old snapshot reached ready_to_review — the "+
				"freshness gate is not being applied on the run path (proposal had %d entry/entries)",
				len(p.ReadyToReview))
		}
	}
	// 断言二：新鲜快照的价 $3.33 **在**提案里 —— 否则这条判据会因为「什么都没
	//         通过」而全绿（恒真）。
	if !hasPrice(p.ReadyToReview, 3.33) {
		t.Errorf("the fresh snapshot's price is missing too, so this test cannot tell a working "+
			"gate from a broken one that drops everything (proposal had %d entry/entries)",
			len(p.ReadyToReview))
	}
	// 断言三：结论必须**写进提案本身**，而不只是 stderr。提案会被拷进工单与
	//         聊天，只写日志的证据到那时就丢了。
	verdicts := map[string]string{}
	for _, s := range p.SnapshotFreshness {
		verdicts[s.File] = s.Verdict
	}
	if verdicts["deepseek.md"] != "stale" {
		t.Errorf("snapshot_freshness must record deepseek.md as stale, got %q (whole map: %v)",
			verdicts["deepseek.md"], verdicts)
	}
	if verdicts["xai.md"] != "no_published_time" {
		t.Errorf("snapshot_freshness must record xai.md as no_published_time, got %q",
			verdicts["xai.md"])
	}
}

func hasPrice(entries []proposalEntry, want float64) bool {
	for _, e := range entries {
		if e.Input != nil && *e.Input == want {
			return true
		}
	}
	return false
}
