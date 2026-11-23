package sessioncontext_test

// 分段抽取式摘要 + 分层合并穿过 Reduce 的集成测试（2026-11-23 第三步）。
//
// 钉住的契约：
//  1. 摘要覆盖整段历史的首/中/尾（best-16 摘录只能看到开头）；
//  2. 摘要正文不超过预算；
//  3. 结果确定，且与并行度无关；
//  4. 下一轮只算新的段（段缓存命中）；
//  5. 没通过历史守卫的消息，一个字都不进摘要；
//  6. >64KB 的消息只留省略标记，不被读进摘要；
//  7. 未启用时输出与此前逐字节相同；没有可摘的文本时不出摘要段。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
)

type digestM = map[string]any

// digestSampleConversation 造一份「每条消息内容都不同」的合成会话：像一场 agent
// 工作过程（人类提需求 → 助手调工具 → 工具输出 → 助手给结论），每条都带独一无二
// 的事实（文件名、数字、决定），用来检验抽取质量与首/中/尾覆盖。
func digestSampleConversation(turns int, secretEvery int) []byte {
	msgs := make([]any, 0, turns*3+2)
	msgs = append(msgs, digestM{"role": "user", "content": "Please fix the billing service; the deploy pipeline is red since release 2026.11.1."})
	for t := 0; t < turns; t++ {
		where := []string{"billing", "invoice", "ledger", "audit", "pricing", "gateway", "worker", "scheduler"}[t%8]
		msgs = append(msgs, digestM{"role": "assistant", "content": []any{
			digestM{"type": "text", "text": fmt.Sprintf("Step %d: I will inspect the %s module and compare it against the staging configuration.", t, where)},
			digestM{"type": "tool_use", "id": fmt.Sprintf("toolu_%d_a", t), "name": "Read", "input": digestM{"file_path": fmt.Sprintf("internal/%s/handler.go", where)}},
		}})
		var out strings.Builder
		for l := 0; l < 30; l++ {
			fmt.Fprintf(&out, "line %02d: package %s; port=%d retries=%d region=cn-east-%d\n", l, where, 6400+t*7+l, l%5, t%3)
		}
		msgs = append(msgs, digestM{"role": "user", "content": []any{
			digestM{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%d_a", t), "content": out.String()},
		}})
		var conclusion string
		switch t % 4 {
		case 0:
			conclusion = fmt.Sprintf("Finding %d: the timeout in %s was caused by a stale DNS entry for host svc-%d.internal; I will bump the retry budget to %d.", t, where, t, 3+t)
		case 1:
			conclusion = fmt.Sprintf("Decision %d: we keep the %s migration reversible by adding column legacy_flag_%d with a default.", t, where, t)
		case 2:
			conclusion = fmt.Sprintf("Note %d: test file %s_test.go covers the regression; coverage rose to %d%%.", t, where, 70+t%20)
		default:
			conclusion = fmt.Sprintf("Risk %d: rolling back %s would drop %d rows in table t_%d; take a snapshot first.", t, where, 1000*t, t%9)
		}
		if secretEvery > 0 && t%secretEvery == 0 {
			conclusion += fmt.Sprintf(" SECRET-%d must never reach the model.", t)
		}
		msgs = append(msgs, digestM{"role": "assistant", "content": []any{digestM{"type": "text", "text": conclusion}}})
		if t%5 == 2 {
			msgs = append(msgs, digestM{"role": "user", "content": fmt.Sprintf("Follow-up from me: please also check the %s dashboard and report the error rate for day %d.", where, t)})
		}
	}
	msgs = append(msgs, digestM{"role": "user", "content": "Now summarize what you changed and what is still risky."})
	raw, err := json.Marshal(map[string]any{"model": "m", "max_tokens": 10, "messages": msgs})
	if err != nil {
		panic(err)
	}
	return raw
}

func digestReduce(t *testing.T, ctx context.Context, raw []byte, maxBytes int) ([]byte, sessioncontext.Report) {
	t.Helper()
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, rep, err := sessioncontext.Reduce(ctx, store, ref, maxBytes)
	if err != nil {
		t.Fatalf("Reduce: %v", err)
	}
	return body, rep
}

// digestCtx 给一份全新的段缓存（测试之间互不串）。
func digestCtx(cfg sessioncontext.DigestConfig, workers int) context.Context {
	ctx := sessioncontext.WithDigestCache(context.Background(), sessioncontext.NewDigestCache(256))
	if workers > 1 {
		ctx = sessioncontext.WithReduceWorkers(ctx, workers)
	}
	if cfg.Enabled {
		ctx = sessioncontext.WithDigest(ctx, cfg)
	}
	return ctx
}

// summaryParts 取出摘要消息里的（摘要正文, 摘录段）。
func summaryParts(t *testing.T, body []byte) (digest, excerpts string) {
	t.Helper()
	var root struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	for _, m := range root.Messages {
		if !strings.HasPrefix(m.Content, "[Earlier history reduced;") {
			continue
		}
		content := m.Content
		i := strings.Index(content, "[Conversation digest: ")
		if i < 0 {
			return "", content
		}
		rest := content[i:]
		_, rest, _ = strings.Cut(rest, "\n")
		if j := strings.Index(rest, "[Excerpts of earlier history"); j >= 0 {
			return strings.TrimSuffix(rest[:j], "\n"), rest[j:]
		}
		return strings.TrimSuffix(rest, "\n"), ""
	}
	return "", ""
}

var digestTurn = regexp.MustCompile(`(?:Finding|Decision|Note|Risk|Step) (\d+):`)

func digestTurns(text string) (min, max, distinct int) {
	seen := map[int]bool{}
	min, max = 1<<30, -1
	for _, m := range digestTurn.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		seen[n] = true
		if n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	if len(seen) == 0 {
		return -1, -1, 0
	}
	return min, max, len(seen)
}

func TestReduceDigestCoversWholeHistoryWhereExcerptsOnlySeeTheStart(t *testing.T) {
	const turns, maxBytes = 120, 64 << 10
	raw := digestSampleConversation(turns, 0)

	off, _ := digestReduce(t, digestCtx(sessioncontext.DigestConfig{}, 1), raw, maxBytes)
	digestOff, excerptsOff := summaryParts(t, off)
	if digestOff != "" {
		t.Fatal("未启用时不该有摘要段")
	}
	// 既有行为（best-16）：前 16 条非空消息先占位 ⇒ 只看得到会话开头。
	if _, max, distinct := digestTurns(excerptsOff); max >= 15 || distinct > 12 {
		t.Fatalf("前提变了：best-16 摘录应当只覆盖开头，实际覆盖到第 %d 轮（%d 个不同轮次）", max, distinct)
	}

	on, rep := digestReduce(t, digestCtx(sessioncontext.DigestConfig{Enabled: true}, 1), raw, maxBytes)
	digest, _ := summaryParts(t, on)
	if digest == "" {
		t.Fatal("启用后应有摘要段")
	}
	min, max, distinct := digestTurns(digest)
	if min > 10 || max < turns-10 {
		t.Fatalf("摘要必须覆盖首/中/尾：实际覆盖第 %d ~ %d 轮（共 %d 轮）", min, max, turns)
	}
	if distinct < turns/2 {
		t.Fatalf("摘要只覆盖了 %d 个不同轮次（共 %d 轮）——覆盖太稀", distinct, turns)
	}
	if rep.DigestSegments == 0 || rep.DigestCacheHits != 0 || rep.DigestBytes != len(digest) {
		t.Fatalf("report 里的摘要计数不对: %+v", rep)
	}
	// 行首标签：人类消息与工具结果要分得开。
	if !strings.Contains(digest, "\nuser: ") || !strings.Contains(digest, "\nassistant: ") || !strings.Contains(digest, "\ntool_result: ") {
		t.Fatalf("摘要里应有 user / assistant / tool_result 三种标签:\n%s", digest)
	}
	// 受保护的消息原样保留在归约结果里，不再重复进摘要（开头的任务陈述、末尾的当前请求）。
	if strings.Contains(digest, "Please fix the billing service") || strings.Contains(digest, "Now summarize what you changed") {
		t.Fatalf("受保护的消息不该再进摘要:\n%s", digest)
	}
	if strings.Contains(digest, "user: line ") {
		t.Fatal("工具输出不该被标成 user（Anthropic 形态下它挂在 user 角色上）")
	}
}

// TestReduceDigestKeepsEveryHumanMessageAcrossSegments —— 跨段去重不能把用户第二次提的要求当成第一次的重复。
//
// 实测过的回归：去冗余阈值 0.8 时，「请检查 ledger 面板，第 42 天」与第 0 段的「……第 2 天」只差一个数字（重合度 0.83），
// 在树形合并里被当成重复丢掉——于是用户消息只留在了前几段。模板相同、只差编号的行是不同的事实。
func TestReduceDigestKeepsEveryHumanMessageAcrossSegments(t *testing.T) {
	const turns = 120
	raw := digestSampleConversation(turns, 0)
	want := 0
	for t := 0; t < turns; t++ {
		if t%5 == 2 {
			want++
		}
	}
	// 预算宽松：每一条人类消息都应在。
	body, rep := digestReduce(t, digestCtx(sessioncontext.DigestConfig{Enabled: true, BudgetBytes: 64 << 10}, 8), raw, 64<<10)
	digest, _ := summaryParts(t, body)
	if got := strings.Count(digest, "\nuser: Follow-up from me:"); got != want {
		t.Fatalf("预算宽松时人类消息应全部保留: %d / %d", got, want)
	}
	// 默认预算（16KB）：人类消息权重最高，至少留下八成。
	body, _ = digestReduce(t, digestCtx(sessioncontext.DigestConfig{Enabled: true}, 8), raw, 64<<10)
	digest, _ = summaryParts(t, body)
	if got := strings.Count(digest, "\nuser: Follow-up from me:"); got < want*8/10 {
		t.Fatalf("默认预算下人类消息只留下 %d / %d 条", got, want)
	}
	if rep.DigestSegments < 4 {
		t.Fatalf("前提：这个样本应切出至少 4 段，实际 %d", rep.DigestSegments)
	}
}

func TestReduceDigestRespectsBudget(t *testing.T) {
	raw := digestSampleConversation(120, 0)
	for _, budget := range []int{1024, 2048, 8192} {
		cfg := sessioncontext.DigestConfig{Enabled: true, BudgetBytes: budget}
		body, rep := digestReduce(t, digestCtx(cfg, 8), raw, 64<<10)
		digest, _ := summaryParts(t, body)
		if digest == "" {
			t.Fatalf("budget=%d: 没有摘要", budget)
		}
		if len(digest) > budget {
			t.Fatalf("budget=%d: 摘要 %d 字节，超预算", budget, len(digest))
		}
		if rep.DigestBytes != len(digest) {
			t.Fatalf("budget=%d: report.DigestBytes=%d ≠ %d", budget, rep.DigestBytes, len(digest))
		}
	}
}

func TestReduceDigestIsDeterministicAndIndependentOfParallelism(t *testing.T) {
	raw := digestSampleConversation(90, 0)
	cfg := sessioncontext.DigestConfig{Enabled: true, BudgetBytes: 6144}
	first, repFirst := digestReduce(t, digestCtx(cfg, 1), raw, 64<<10)
	for _, workers := range []int{1, 1, 2, 8, 64} {
		again, rep := digestReduce(t, digestCtx(cfg, workers), raw, 64<<10)
		if !bytes.Equal(again, first) {
			t.Fatalf("workers=%d: 输出与串行不一致", workers)
		}
		if rep != repFirst {
			t.Fatalf("workers=%d: report 不一致: %+v vs %+v", workers, rep, repFirst)
		}
	}
}

func TestReduceDigestCacheHitsOnNextTurn(t *testing.T) {
	body1 := digestSampleConversation(60, 0)
	// 段切小一点，这场会话才有足够多的段。
	cfg := sessioncontext.DigestConfig{Enabled: true, SegmentBytes: 8 << 10}
	ctx := digestCtx(cfg, 8)
	_, rep1 := digestReduce(t, ctx, body1, 64<<10)
	if rep1.DigestSegments < 8 || rep1.DigestCacheHits != 0 {
		t.Fatalf("首轮应全部未命中: %+v", rep1)
	}

	// 下一轮：末尾追加两条消息。新的 user 消息成为受保护的尾部，而它前面那条助手消息
	// （带一个独一无二的事实）变成了历史 ⇒ 必须进摘要。最后一段因此改变 ⇒ 它必须重算，
	// 其余段全部命中。
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body1, &root); err != nil {
		t.Fatal(err)
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(root["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs,
		json.RawMessage(`{"role":"user","content":"Noted. One more thing: the secret rotate cron runs at 03:17 on the audit worker."}`),
		json.RawMessage(`{"role":"user","content":"Please go ahead."}`),
	)
	root["messages"], _ = json.Marshal(msgs)
	body2, _ := json.Marshal(root)

	body, rep2 := digestReduce(t, ctx, body2, 64<<10)
	digest2, _ := summaryParts(t, body)
	if !strings.Contains(digest2, "rotate cron runs at 03:17") {
		t.Fatalf("新变成历史的消息必须进摘要:\n%s", digest2)
	}
	if rep2.DigestCacheHits >= rep2.DigestSegments {
		t.Fatalf("新变成历史的消息改变了最后一段，至少应有一段未命中: segments=%d hits=%d", rep2.DigestSegments, rep2.DigestCacheHits)
	}
	// 同一请求再发一次 ⇒ 全部命中，摘要逐字相同。
	again, rep3 := digestReduce(t, ctx, body2, 64<<10)
	digest3, _ := summaryParts(t, again)
	if rep3.DigestCacheHits != rep3.DigestSegments || digest3 != digest2 {
		t.Fatalf("同一请求重放应全部命中且摘要不变: segments=%d hits=%d", rep3.DigestSegments, rep3.DigestCacheHits)
	}
}

func secretGuard(ctx context.Context, open func() io.Reader) (bool, error) {
	data, err := io.ReadAll(open())
	if err != nil {
		return false, err
	}
	return !bytes.Contains(data, []byte("SECRET")), nil
}

func TestReduceDigestNeverQuotesUnsafeHistory(t *testing.T) {
	const turns = 80
	raw := digestSampleConversation(turns, 7)
	unsafe := 0
	for t := 0; t < turns; t++ {
		if t%7 == 0 {
			unsafe++
		}
	}
	base := sessioncontext.WithHistoryGuard(digestCtx(sessioncontext.DigestConfig{Enabled: true}, 8), secretGuard)
	body, rep := digestReduce(t, base, raw, 64<<10)
	if rep.SensitiveHistoryOmitted != unsafe {
		t.Fatalf("被判不安全的历史消息数不对: %d, want %d", rep.SensitiveHistoryOmitted, unsafe)
	}
	if bytes.Contains(body, []byte("SECRET")) {
		t.Fatal("没通过守卫的消息一个字都不能进归约结果（含摘要）")
	}
	digest, _ := summaryParts(t, body)
	if digest == "" {
		t.Fatal("其余消息仍应生成摘要")
	}
	if strings.Contains(digest, "Finding 0:") {
		t.Fatalf("含 SECRET 的第 0 轮结论不该进摘要:\n%s", digest)
	}
	if min, _, distinct := digestTurns(digest); distinct < 10 || min == 0 {
		t.Fatalf("干净的历史应照常生成有覆盖的摘要: 覆盖 %d 个不同轮次，最小轮次 %d", distinct, min)
	}
}

func TestReduceDigestMarksBigMessagesInsteadOfReadingThem(t *testing.T) {
	msgs := make([]any, 0, 40)
	msgs = append(msgs, digestM{"role": "user", "content": "Start by reading the giant export file and then continue."})
	for i := 0; i < 12; i++ {
		msgs = append(msgs, digestM{"role": "assistant", "content": fmt.Sprintf("assistant note number %d about the export pipeline stage %d.", i, i)})
	}
	bigText := strings.Repeat("giant export row with plenty of repeated words ", 3000) // ≈ 150KB
	big, _ := json.Marshal(digestM{"role": "assistant", "content": bigText})
	msgs = append(msgs, digestM{"role": "assistant", "content": bigText})
	for i := 0; i < 12; i++ {
		msgs = append(msgs, digestM{"role": "assistant", "content": fmt.Sprintf("closing remark %d: the export finished with status code %d.", i, 200+i)})
	}
	msgs = append(msgs, digestM{"role": "user", "content": "What happened with the export?"})
	raw, _ := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	if len(big) <= 64<<10 {
		t.Fatalf("测试前提：大消息应超过 64KB，实际 %d", len(big))
	}
	body, _ := digestReduce(t, digestCtx(sessioncontext.DigestConfig{Enabled: true}, 4), raw, 32<<10)
	digest, _ := summaryParts(t, body)
	want := fmt.Sprintf("[message of %d bytes omitted from digest]", len(big))
	if !strings.Contains(digest, want) {
		t.Fatalf("大消息应留一行省略标记 %q:\n%s", want, digest)
	}
	if strings.Contains(digest, "giant export row") {
		t.Fatal("大消息的内容不该被读进摘要")
	}
	if !strings.Contains(digest, "closing remark 11") || !strings.Contains(digest, "assistant note number 0") {
		t.Fatalf("大消息前后的普通消息应照常进摘要:\n%s", digest)
	}
}

func TestReduceDigestOffIsByteIdenticalToLegacy(t *testing.T) {
	raw := digestSampleConversation(70, 0)
	legacy, repLegacy := digestReduce(t, context.Background(), raw, 64<<10)
	for _, cfg := range []sessioncontext.DigestConfig{{}, {Enabled: false, BudgetBytes: 4096}} {
		got, rep := digestReduce(t, digestCtx(cfg, 8), raw, 64<<10)
		if !bytes.Equal(got, legacy) {
			t.Fatalf("cfg=%+v: 未启用时输出必须与此前逐字节相同", cfg)
		}
		if rep != repLegacy {
			t.Fatalf("cfg=%+v: report 不一致", cfg)
		}
	}
}

func TestReduceDigestHandlesTextlessHistory(t *testing.T) {
	msgs := make([]any, 0, 40)
	msgs = append(msgs, digestM{"role": "user", "content": "Please review the attached data."})
	for i := 0; i < 30; i++ {
		msgs = append(msgs, digestM{"role": "assistant", "content": fmt.Sprintf("%s\n%d\n%s", strings.Repeat("#", 300), 1000+i, strings.Repeat("{}", 200))})
	}
	msgs = append(msgs, digestM{"role": "user", "content": "What do you think?"})
	raw, _ := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	body, rep := digestReduce(t, digestCtx(sessioncontext.DigestConfig{Enabled: true}, 4), raw, 8<<10)
	if bytes.Contains(body, []byte("[Conversation digest:")) {
		t.Fatalf("没有可摘的文本时不应出现摘要段:\n%s", body)
	}
	if rep.DigestSegments == 0 {
		t.Fatal("段还是切出来了（只是没有可摘的行）")
	}
	// 摘录段仍按既有行为工作；归约结果仍是合法 JSON。
	if !json.Valid(body) {
		t.Fatal("归约结果必须是合法 JSON")
	}
}
