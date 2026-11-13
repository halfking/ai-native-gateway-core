package sessioncontext

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestR54OverThousandContentBlocksFollowsRegisteredContract — B-P2-1 归因订正
// （2026-11-14 复审）：B 组把「1025 个小文本块消息 ⇒ Reduce ErrProtectedContext」
// 归因到 fileMetadataBounded 的 content/parts 元素数检查（:110-112，
// r1053 的孪生硬失败）。主代理钉测当场订正：元素数帽**实际不可达**——
// 256KB/消息元数据预算（file.go:13 `remaining := 256<<10`）先拦：每块递归
// charge 256B 结构费 + 块载荷 read，1024×(256+~85) ≈ 354KB > 256KB ⇒
// metadata.go:59 `*remaining < 256 ⇒ ErrProtectedContext` 在第 ~757 块即触发。
// ⇒ 真实可达的失败源＝256KB 预算面 = r1053 主档 §五已登记的「复合形状维持
// fail-closed」设计决策（内存守卫）。
//
// 本测试钉住的是**登记契约**：该形状维持 fail-closed（预算面守卫有牙），
// 且最新 user 消息路径不被殃及（错误在此形状下是设计行为，不是回归）。
// P2-1 的元素数帽截断修复（>1024 截断省略）保留为防御纵深：预算常量若
// 未来上调，元素数帽成为第一道闸时不得再是硬失败。
func TestR54OverThousandContentBlocksFollowsRegisteredContract(t *testing.T) {
	const oneMiB = 1 << 20

	blocks := make([]any, 0, 1025)
	for i := 0; i < 1025; i++ {
		blocks = append(blocks, map[string]any{"type": "text", "text": strings.Repeat("b", 60)})
	}
	bigMsg := map[string]any{"role": "user", "content": blocks}

	msgs := []map[string]any{
		{"role": "system", "content": "You are ZCode."},
		{"role": "user", "content": "task start"},
		bigMsg,
	}
	pad := strings.Repeat("history filler line for the r54 blocks test padding. ", 190)
	for i := 0; i < 155; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": pad})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "ack " + jsonItoa(i)})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "请继续收尾"})
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= oneMiB {
		t.Fatalf("fixture premise broken: body %d bytes must exceed 1MiB", len(body))
	}

	store, ref := captureForReduce(t, body)
	_, _, err = Reduce(context.Background(), store, ref, oneMiB)
	// 登记契约：1025 块 × 结构费超 256KB/消息预算 ⇒ fail-closed 维持
	// （r1053 主档 §五复合形状观察项；恢复此形状放行需要预算面动态截断
	// 设计，另案）。
	if err == nil {
		t.Fatalf("1025-block shape must stay fail-closed per the r1053 registered compound-shape contract (256KB/message metadata budget); if this turned green the budget contract changed — re-register")
	}
	if !strings.Contains(err.Error(), "protected task, instructions and active tool chain") {
		t.Fatalf("expected the registered ErrProtectedContext contract, got: %v", err)
	}

	// 对照面：小形状（预算内）正常收敛，证明 fail-closed 只咬复合形状。
	small := []map[string]any{
		{"role": "system", "content": "You are ZCode."},
		{"role": "user", "content": "task start"},
	}
	smallBody, _ := json.Marshal(map[string]any{"model": "gpt-6-astra", "messages": small})
	store2, ref2 := captureForReduce(t, smallBody)
	out2, _, err2 := Reduce(context.Background(), store2, ref2, oneMiB)
	if err2 != nil || !bytes.Contains(out2, []byte("task start")) {
		t.Fatalf("small shape must reduce cleanly: err=%v", err2)
	}
}
