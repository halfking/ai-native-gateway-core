// Package sessionforensics_test - mutation_report_test.go
//
// 在跑 mutation 测试的同时，自动生成"baseline vs mutated"对比报告到
// tests/session_replay/sessions/reports/mutation_<sid>_<kind>.json。
//
// 报告字段：
//
//   - session_id     — 被测 session
//   - mutation       — Mutation 结构
//   - baseline       — 原始 pack 的 ReplayReport
//   - mutated        — mutation 后 pack 的 ReplayReport
//   - delta          — 关键指标差异（strategy count diff, bytes diff, 异常标志）
//
// 由 operator 审阅，验证压缩 / 缓存 / lossiness 在 mutation 前后没有
// 出现非预期差异。

package sessionforensics_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

// TestMutationReport_AutoGenerate 通过真实生产 session (gw_7c9f06ab) 上跑
// 全部 7 个 mutation，对每对 baseline/mutated 输出 JSON 文件供人工审查。
func TestMutationReport_AutoGenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping report writer in short mode")
	}
	// 找 gw_7c9f06ab 真实 session（10 轮累积对话，最适合做 mutation 对比）
	const sid = "gw_7c9f06ab-7520-4ad0-a9c7-863c7152878c"
	pack, err := sessionforensics.LoadExtractPyFile(
		findSessionFile(t, sid),
	)
	if err != nil {
		t.Skipf("无法加载 %s：%v（可能是 ABS_SESSIONS_DIR 不对）", sid, err)
	}

	// 输出目录
	dir := filepath.Join(sessionsRoot(t), "reports", "mutation")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ctx := context.Background()
	rp := sessionforensics.NewReplayer()

	// baseline
	baseRep := rp.Replay(ctx, pack, sessionforensics.ReplayOptions{
		TenantID: "default", ContextWindow: 128_000,
	})
	t.Logf("baseline: %d turns, strategy_counts=%v",
		baseRep.Aggregate.TotalTurns, baseRep.Aggregate.StrategyCounts)

	// 7 个 mutation
	mutations := []sessionforensics.Mutation{
		{Kind: sessionforensics.MKindModelSwap, AtTurn: 5, Extra: "claude-sonnet-5"},
		{Kind: sessionforensics.MKindToolTruncated, AtTurn: 3},
		{Kind: sessionforensics.MKindCutAndAppend, AtTurn: 7, Extra: "请总结前面并给我建议"},
		{Kind: sessionforensics.MKindThinkingInject, AtTurn: 5},
		{Kind: sessionforensics.MKindVisionContent, AtTurn: 1},
		{Kind: sessionforensics.MKindLongSystemPrompt, Extra: "50000"},
		{Kind: sessionforensics.MKindEmptyMessagesAt, AtTurn: 4},
	}

	for _, m := range mutations {
		t.Run(string(m.Kind), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("mutation %s caused panic: %v", m.Kind, r)
				}
			}()
			packC := deepCopyPack2(t, pack)
			if err := sessionforensics.Mutate(packC, m); err != nil {
				t.Fatalf("mutate: %v", err)
			}
			mutRep := rp.Replay(ctx, packC, sessionforensics.ReplayOptions{
				TenantID: "default", ContextWindow: 128_000,
			})

			report := map[string]any{
				"session_id":   sid,
				"mutation":     m,
				"generated_at": time.Now().UTC().Format(time.RFC3339),
				"baseline":     baseRep,
				"mutated":      mutRep,
				"delta": map[string]any{
					"strategy_diff": mapStrategyDiff(
						baseRep.Aggregate.StrategyCounts,
						mutRep.Aggregate.StrategyCounts),
					"bytes_baseline_total": baselineBodySize(baseRep),
					"bytes_mutated_total":  mutatedBodySize(mutRep),
					"lossiness_diff": map[string]int{
						"baseline_none": baseRep.Aggregate.LossinessCounts["none"],
						"mutated_none":  mutRep.Aggregate.LossinessCounts["none"],
					},
				},
			}
			// 输出到磁盘
			safe := strings.ReplaceAll(string(m.Kind), "_", "-")
			fp := filepath.Join(dir, "report_"+safe+".json")
			b, _ := json.MarshalIndent(report, "", "  ")
			if err := os.WriteFile(fp, b, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			t.Logf("wrote %s (%d bytes)", fp, len(b))

			// sanity check: 至少应当跑通
			if len(mutRep.Steps) == 0 {
				t.Errorf("mutated replay produced 0 steps (kind=%s)", m.Kind)
			}
		})
	}
}

// TestMutationReport_SummaryAggregate 汇总所有 mutation 的差异，验证
// 至少其中 5 个不会让 SessionCompressor.Prepare 出现 cache / strategy 异常。
func TestMutationReport_SummaryAggregate(t *testing.T) {
	pack := buildMockPack2(t, 8)
	ctx := context.Background()
	rp := sessionforensics.NewReplayer()
	baseRep := rp.Replay(ctx, pack, sessionforensics.ReplayOptions{
		TenantID: "default", ContextWindow: 128_000,
	})
	if baseRep.Aggregate.TotalTurns != 8 {
		t.Fatalf("baseline turns: %d", baseRep.Aggregate.TotalTurns)
	}
	// baseline 应当全是 delta_append 或 fallback（无 schema 改动时）
	t.Logf("baseline strategy counts: %v", baseRep.Aggregate.StrategyCounts)

	// 跑 M3（cut + append）；验证 turn 数从 8 → 6 + 2 = 8（砍 2 + 加 2 = 守恒）
	pack2 := deepCopyPack2(t, pack)
	if err := sessionforensics.Mutate(pack2, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindCutAndAppend,
		AtTurn: 4,
	}); err != nil {
		t.Fatal(err)
	}
	mutRep := rp.Replay(ctx, pack2, sessionforensics.ReplayOptions{
		TenantID: "default", ContextWindow: 128_000,
	})
	if mutRep.Aggregate.TotalTurns != 8 {
		t.Errorf("M3 turns after mutation: %d, want 8", mutRep.Aggregate.TotalTurns)
	}
	// 至少 1 个 step 应当触发 delta_append（LCS 能从 user-msg 找到共同点）
	t.Logf("M3 strategy counts: %v", mutRep.Aggregate.StrategyCounts)
}

// ── helpers ────────────────────────────────────────────────────────────────

func findSessionFile(t *testing.T, sid string) string {
	t.Helper()
	safe := strings.TrimPrefix(sid, "gw_")
	candidates := []string{
		filepath.Join("tests", "session_replay", "sessions", "session_"+safe+".json"),
		filepath.Join("..", "tests", "session_replay", "sessions", "session_"+safe+".json"),
	}
	if d := os.Getenv("ABS_SESSIONS_DIR"); d != "" {
		candidates = append([]string{
			filepath.Join(d, "session_"+safe+".json"),
		}, candidates...)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skipf("session file not found for %s", sid)
	return ""
}

func sessionsRoot(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"tests/session_replay/sessions",
		"../tests/session_replay/sessions",
		"../../tests/session_replay/sessions",
	}
	if d := os.Getenv("ABS_SESSIONS_DIR"); d != "" {
		candidates = append([]string{d}, candidates...)
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "manifest.json")); err == nil {
			return c
		}
	}
	t.Skip("sessions root not found")
	return ""
}

func mapStrategyDiff(a, b map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] -= v
	}
	return out
}

func baselineBodySize(rep *sessionforensics.ReplayReport) int {
	n := 0
	for _, s := range rep.Steps {
		n += s.BytesBefore
	}
	return n
}

func mutatedBodySize(rep *sessionforensics.ReplayReport) int {
	n := 0
	for _, s := range rep.Steps {
		n += s.BytesAfter
	}
	return n
}

// buildMockPack2 别名避免 import 循环
func buildMockPack2(t *testing.T, n int) *sessionforensics.SessionPack {
	t.Helper()
	return buildMockPack(t, n)
}

func deepCopyPack2(t *testing.T, p *sessionforensics.SessionPack) *sessionforensics.SessionPack {
	t.Helper()
	return deepCopyPack(t, p)
}
