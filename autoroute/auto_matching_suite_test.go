package autoroute

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// auto_matching_suite_test.go — auto 匹配提示词套件的离线回归(启发式分类层)。
//
// 套件文件 testdata/auto_matching_suite.jsonl 由 2026-09-14 匹配能力复审建立
// (docs/audit/2026-09-14-auto-matching-prompt-e2e-audit.md),覆盖 11 个任务类型
// 的业务化提示词 + 边界陷阱。本测试把每条用例转成 ClassificationSignals 直接
// 喂 HeuristicClassifier(静态默认关键词,与空 tuning_params 的网关行为一致),
// 是套件在无网络环境下的可回归形态;cmd/autoroute-e2e-audit 用同一文件打真实网关。
//
// known_failure=true 的用例记录的是"期望行为"而非当前行为,单独计数:
//   - 失败 = 已登记的待修缺口(见审计报告);
//   - 通过 = 缺口已被修复,此时应把标志位翻回 false。
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
}

const suitePaddingParagraph = "This section of the transcript discusses the quarterly platform review, including reliability metrics, capacity planning notes, incident timelines, and follow-up actions agreed by the team. "

// estimateSuiteTokens mirrors domains/streaming estimateTokens (ascii/4 + cjk*2),
// replicated here because importing that package from autoroute would cycle.
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

func loadAutoMatchingSuite(t *testing.T) []suiteCase {
	t.Helper()
	f, err := os.Open("testdata/auto_matching_suite.jsonl")
	if err != nil {
		t.Fatalf("open suite: %v", err)
	}
	defer f.Close()
	var cases []suiteCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c suiteCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("suite line %d: %v", lineNo, err)
		}
		if c.Name == "" || c.ExpectedTask == "" || c.Prompt == "" {
			t.Fatalf("suite line %d: missing name/expected_task/prompt", lineNo)
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan suite: %v", err)
	}
	if len(cases) < 50 {
		t.Fatalf("suite unexpectedly small: %d cases", len(cases))
	}
	return cases
}

func TestAutoMatchingSuiteHeuristic(t *testing.T) {
	cases := loadAutoMatchingSuite(t)
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	ctx := context.Background()

	pass, fail, knownFailPass, knownFail := 0, 0, 0, 0
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			text := tc.System + "\n" + tc.Prompt
			est := estimateSuiteTokens(text)
			if tc.PadToTokens > 0 {
				for est < tc.PadToTokens {
					text += suitePaddingParagraph
					est = estimateSuiteTokens(text)
				}
			}
			sigs := ClassificationSignals{
				LastUserPrompt:  tc.Prompt,
				SystemPrompt:    tc.System,
				ClientType:      tc.ClientType,
				ToolCount:       tc.Tools,
				HasToolResults:  tc.ToolResults,
				HasImages:       tc.Image,
				HasCodeBlock:    strings.Contains(text, "```"),
				EstimatedTokens: est,
				MessageCount:    2,
			}
			got, err := c.Classify(ctx, sigs)
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got.Primary == TaskType(tc.ExpectedTask) {
				t.Logf("PASS %s (conf=%.2f, reason=%s)", tc.Name, got.Confidence, got.Reason)
				return
			}
			if tc.KnownFailure {
				t.Logf("KNOWN-FAIL %s: got=%s want=%s (reason=%s) — %s",
					tc.Name, got.Primary, tc.ExpectedTask, got.Reason, tc.ExpectNote)
				return
			}
			t.Errorf("got=%s (conf=%.2f, reason=%s) want=%s",
				got.Primary, got.Confidence, got.Reason, tc.ExpectedTask)
		})
	}
	// 汇总(子测试里不方便跨用例计数,单独重跑一遍轻量统计)。
	for _, tc := range cases {
		text := tc.System + "\n" + tc.Prompt
		est := estimateSuiteTokens(text)
		if tc.PadToTokens > 0 {
			for est < tc.PadToTokens {
				text += suitePaddingParagraph
				est = estimateSuiteTokens(text)
			}
		}
		got, err := c.Classify(ctx, ClassificationSignals{
			LastUserPrompt: tc.Prompt, SystemPrompt: tc.System, ClientType: tc.ClientType,
			ToolCount: tc.Tools, HasToolResults: tc.ToolResults, HasImages: tc.Image,
			HasCodeBlock: strings.Contains(text, "```"), EstimatedTokens: est, MessageCount: 2,
		})
		if err != nil {
			continue
		}
		ok := got.Primary == TaskType(tc.ExpectedTask)
		switch {
		case ok && tc.KnownFailure:
			knownFailPass++
		case ok:
			pass++
		case tc.KnownFailure:
			knownFail++
		default:
			fail++
		}
	}
	t.Logf("suite summary: pass=%d fail=%d known_failure=%d known_failure_xpass=%d (total=%d)",
		pass, fail, knownFail, knownFailPass, len(cases))
	if fail > 0 {
		t.Errorf("%d non-known cases failed — 分类回归,见上方子测试明细", fail)
	}
}
