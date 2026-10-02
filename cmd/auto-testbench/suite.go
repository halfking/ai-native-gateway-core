package main

// suite.go — auto 匹配套件加载与信号构造（auto-testbench 离线回归层）。
//
// 套件格式与 autoroute/auto_matching_suite_test.go 的 suiteCase 同构。
// 该结构体已是仓内第三份拷贝（包内 _test / cmd/autoroute-e2e-audit /
// 本文件）——它是套件 JSONL 的属主 schema，按既有惯例随消费者复制，
// 改字段时三处必须同步（套件文件本身归 autoroute/testdata 所有，见
// docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md §五.3）。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// suiteCase mirrors autoroute/auto_matching_suite_test.go (JSONL schema).
type suiteCase struct {
	Name         string `json:"name"`
	Bucket       string `json:"bucket"`
	ExpectedTask string `json:"expected_task"`
	KnownFailure bool   `json:"known_failure"`
	ExpectNote   string `json:"expect_note"`
	System       string `json:"system"`
	ClientType   string `json:"client_type"`
	Prompt       string `json:"prompt"`
	Tools        int    `json:"tools"`
	ToolResults  bool   `json:"tool_results"`
	Image        bool   `json:"image"`
	PadToTokens  int    `json:"pad_to_tokens"`

	// generated 标记：generate 模式产出的候选样本行。候选样本的 prompt
	// 是占位符（隐私口径：绝不回填原文），不参与分类评测，仅在报告的
	// inventory 中计数，等待人工脱敏复核后转正。
	Generated   bool   `json:"generated,omitempty"`
	LabelSource string `json:"label_source,omitempty"`
	SignalsNote string `json:"signals_note,omitempty"`
}

// suitePaddingParagraph mirrors autoroute/auto_matching_suite_test.go —
// 填充段落必须逐字一致，保证 pad_to_tokens 用例在包内回归与 testbench
// 离线评测中产生相同的 EstimatedTokens。
const suitePaddingParagraph = "This section of the transcript discusses the quarterly platform review, including reliability metrics, capacity planning notes, incident timelines, and follow-up actions agreed by the team. "

// estimateSuiteTokens mirrors the package test's ascii/4 + cjk*2 estimator.
func estimateSuiteTokens(s string) int {
	ascii, cjk := 0, 0
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		} else {
			ascii++
		}
	}
	return ascii/4 + cjk*2
}

// resolveRepoPath resolves a suite path relative to the repo root, walking up
// from the working directory so the tool works both from the repo root (go
// run ./cmd/auto-testbench) and from inside the package dir (go test).
func resolveRepoPath(p string) string {
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	dir, err := os.Getwd()
	if err != nil {
		return p
	}
	for i := 0; i < 6; i++ {
		cand := filepath.Join(dir, p)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		dir = filepath.Dir(dir)
	}
	return p
}

// loadSuiteFiles loads one or more suite JSONL files, enforcing the same
// invariants as the package-level regression loader (unique names, mandatory
// name/expected_task/prompt) so a malformed suite fails loudly here too.
func loadSuiteFiles(paths []string) ([]suiteCase, error) {
	var cases []suiteCase
	seen := map[string]string{}
	for _, path := range paths {
		path = resolveRepoPath(path)
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open suite %s: %w", path, err)
		}
		fileCases := 0
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
		for lineNo := 1; sc.Scan(); lineNo++ {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			var c suiteCase
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				f.Close()
				return nil, fmt.Errorf("suite %s line %d: %w", path, lineNo, err)
			}
			if c.Name == "" || c.ExpectedTask == "" || c.Prompt == "" {
				f.Close()
				return nil, fmt.Errorf("suite %s line %d: missing name/expected_task/prompt", path, lineNo)
			}
			if prev := seen[c.Name]; prev != "" {
				f.Close()
				return nil, fmt.Errorf("duplicate case name %q in %s and %s", c.Name, prev, path)
			}
			seen[c.Name] = path
			cases = append(cases, c)
			fileCases++
		}
		if err := sc.Err(); err != nil {
			f.Close()
			return nil, fmt.Errorf("scan suite %s: %w", path, err)
		}
		f.Close()
		if fileCases == 0 {
			return nil, fmt.Errorf("suite %s contains no cases", path)
		}
	}
	return cases, nil
}

// caseSignals builds the ClassificationSignals for one suite case, using the
// same construction as the package regression test (text = system + prompt,
// padding loop against PadToTokens, code-fence detection on the padded text).
func caseSignals(c suiteCase) autoroute.ClassificationSignals {
	text := c.System + "\n" + c.Prompt
	est := estimateSuiteTokens(text)
	if c.PadToTokens > 0 {
		for est < c.PadToTokens {
			text += suitePaddingParagraph
			est = estimateSuiteTokens(text)
		}
	}
	return autoroute.ClassificationSignals{
		LastUserPrompt:  c.Prompt,
		SystemPrompt:    c.System,
		ClientType:      c.ClientType,
		ToolCount:       c.Tools,
		HasToolResults:  c.ToolResults,
		HasImages:       c.Image,
		HasCodeBlock:    strings.Contains(text, "```"),
		EstimatedTokens: est,
		MessageCount:    2,
	}
}
