package sessioncontext_test

// 归约器的「重复解压」回归钉测（2026-11-22 211 生产事故根修）。
//
// ## 事故形态
//
// ZCode 等 agent 客户端的历史是**成千上万条小消息**（211 实测：3953 条、
// 中位数 1KB、总体 7.7MB）。归约器（Reduce）对每条历史消息都要过历史守卫，
// 守卫对同一条消息开 3 个以上独立 reader；归约器全程共用一个归档文件游标，
// 每次 reopen 都是「往回读」。旧实现把它做成「回卷到文件开头重放到目标」
// ⇒ 解压量 ∝ 消息数 × 平均偏移（平方级）⇒ 7.7MB 归约 128~141 秒，
// 撞破 LargeIngress 的 120 秒预算 ⇒ 503 request_processing_timeout。
//
// ## 契约（用「解码帧数」做量具——确定性，不靠计时）
//
//  1. 带守卫的归约，解码帧数与**归档帧数**同阶（每个顺序遍历轮次把归档
//     顺序解压一遍），而不是与「消息数 × 帧数」同阶；
//  2. 守卫语义不变：被守卫判不安全的消息不进摘要，且计入 SensitiveHistoryOmitted；
//  3. 受保护内容（system / 首个任务 / 尾部）与摘录照常保留。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
)

const archiveFrame = 64 << 10 // largebody 的帧大小（64KiB 解压后一帧）

// manySmallMessagesBody 生成 agent 会话形态的请求体：system + 首个任务 +
// 大量 assistant(tool_use) / user(tool_result) 小消息交替 + 尾部任务。
// 含 marker 的历史消息用于验证守卫省略语义。
func manySmallMessagesBody(want int) (body []byte, marked int) {
	rng := rand.New(rand.NewSource(11))
	var b bytes.Buffer
	b.WriteString(`{"model":"m","max_tokens":100,"messages":[{"role":"system","content":"Protect the billing configuration."},{"role":"user","content":"Maintain the billing service and keep notes."}`)
	total := b.Len()
	marked = 0
	for i := 0; total < want; i++ {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("sed -n '1,80p' src/mod%03d/handler.go", i)})
		a := fmt.Sprintf(`,{"role":"assistant","content":[{"type":"tool_use","id":"toolu_%05d","name":"Bash","input":%s}]}`, i, args)
		n := 600 + rng.Intn(900)
		var sb strings.Builder
		for sb.Len() < n {
			sb.WriteString(fmt.Sprintf("func (h *Handler) handle%03d() { /* request_id=%d status=200 */ }\n", i%89, i))
		}
		if i%7 == 3 { // 每 7 条有 1 条带敏感标记
			sb.WriteString("\ncredential SECRET-DO-NOT-EXCERPT-")
			sb.WriteString(fmt.Sprint(i))
			sb.WriteString("\n")
			marked++
		}
		res, _ := json.Marshal(sb.String())
		u := fmt.Sprintf(`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%05d","content":%s}]}`, i, res)
		b.WriteString(a)
		b.WriteString(u)
		total += len(a) + len(u)
	}
	b.WriteString(`,{"role":"user","content":"Find the billing database port and config path."}]}`)
	return b.Bytes(), marked
}

// countingGuard 是历史守卫的测试替身：与真实守卫同形——对同一段内容开
// 3 个独立 reader 并各自读完（预筛 + 两个文本视图）。内容含 SECRET 标记即不安全。
func countingGuard(opens *int) sessioncontext.HistoryGuard {
	return func(ctx context.Context, open func() io.Reader) (bool, error) {
		var last []byte
		for k := 0; k < 3; k++ {
			data, err := io.ReadAll(open())
			if err != nil {
				return false, err
			}
			*opens++
			last = data
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return !bytes.Contains(last, []byte("SECRET-DO-NOT-EXCERPT")), nil
	}
}

func TestReduceManySmallMessagesGuardedIsFrameLocal(t *testing.T) {
	const want = 2 << 20 // ≈ 32 帧、约 2000 条小消息
	body, marked := manySmallMessagesBody(want)
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	frames := (ref.Size + archiveFrame - 1) / archiveFrame
	if frames < 24 || marked < 100 {
		t.Fatalf("test premise: want a multi-frame body with many marked messages, got frames=%d marked=%d", frames, marked)
	}

	opens := 0
	ctx := sessioncontext.WithHistoryGuard(context.Background(), countingGuard(&opens))
	before := largebody.FramesDecoded()
	out, rep, err := sessioncontext.Reduce(ctx, store, ref, 1<<20)
	decoded := largebody.FramesDecoded() - before
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if !json.Valid(out) || len(out) > 1<<20 || !rep.Reduced {
		t.Fatalf("bad reduce result: valid=%v len=%d report=%+v", json.Valid(out), len(out), rep)
	}
	// 守卫确实逐条跑过（每个非保护消息 3 次 open）。
	if opens < 3*marked {
		t.Fatalf("guard opens=%d, want >= 3×marked(%d) — guard must screen every historical message", opens, marked)
	}
	// ★ 核心契约：解码帧数与归档帧数同阶。旧实现下每次 reopen 从头重放 ⇒
	// 约 3 × 2000 次 reopen × 平均 16 帧 ≈ 10 万帧。
	if limit := int64(12 * frames); decoded > limit {
		t.Fatalf("reduce decoded %d frames for a %d-frame archive (limit %d): repeated decompression is back — ReadAt rewind or guard no longer served from memory", decoded, frames, limit)
	}
	t.Logf("archive=%d frames(%d bytes) marked=%d guard_opens=%d decoded_frames=%d", frames, ref.Size, marked, opens, decoded)
	// 守卫省略语义：带标记的消息数全部计入，且摘要里不出现任何标记。
	if rep.SensitiveHistoryOmitted != marked {
		t.Fatalf("SensitiveHistoryOmitted=%d, want %d", rep.SensitiveHistoryOmitted, marked)
	}
	if bytes.Contains(out, []byte("SECRET-DO-NOT-EXCERPT")) {
		t.Fatal("guarded (sensitive) history leaked into the bounded context")
	}
	// 受保护内容与摘录照常保留。
	for _, want := range []string{"Protect the billing configuration.", "Maintain the billing service and keep notes.", "Find the billing database port and config path.", "Earlier history reduced"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("lost %q", want)
		}
	}
}
