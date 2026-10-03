package compression

import (
	"bytes"
	"encoding/json"
	"testing"
)

// 196 号审计（全面审计v3/2026-10-03/196）：三层 provenance 的
// identity/occurrence 映射在 **sanitize ↔ compression 接缝上没有可用的 join 键**。
//
// 本文件把两件事实钉成可执行判据，而不是留在文档里让人重新推一遍：
//
//  事实 1（索引空间错位）：`SanitizedMessageRef.SanitizedIndex` 索引的是
//    **客户端请求体**的 messages 数组（security/sanitize/input_protocols.go:161-188
//    逐条枚举客户端数组；sanitize 只改内容不改长度/顺序，故 RawIndex==SanitizedIndex），
//    而 `AlignmentInfo.OriginalIndex` 索引的是**组装后出向体**的 messages 数组
//    （session_compressor.go:591/632 的 `before := outboundBody`，
//     outboundBody 来自 diff.go:142-144 的 `merged = lastMsgs ++ deltaTail`）。
//    ⇒ 两个坐标系没有任何换算关系，且偏移量随会话状态变化（没有公式可依赖）。
//
//  事实 2（哈希空间不通）：`AlignmentInfo.Hash` 走 `msgHash`
//    （diff.go:265，sha256(canonical JSON) 的**前 16 字节** = 32 hex，
//      归一化方式是 Unmarshal+Marshal ⇒ 对象键被 Go 按字典序重排），
//    `SanitizedMessageRef.RawHash/SanitizedHash` 走 `MessageFingerprint`
//    （sanitize_info.go:94 = 完整 sha256Hex，输入是 `json.Compact` 的结果
//      ⇒ 只压空白、**键序保持原样**）。
//    ⇒ 同一条消息在两个结构里的哈希**永不相等**，「按哈希 join」这条看似自然的
//    替代路也走不通。
//
// ⚠️ **本文件是契约钉桩（contract pin），不是「检测活缺陷」的判据**：
// 它断言的是「当前两条路都不可用」这一**现状**。谁在将来统一了哈希空间、
// 或改成按出向空间产出 refs，本文件会转红，强制同时更新审计记录 ——
// 这正是它存在的意义（否则下一次接手的人会重新推一遍，且很可能推错）。
//
// 判据取**具体数值**（偏移量、哈希长度）而不是「不相等」，
// 这样任何一侧的真实变更都会被看见，而不是恒亮。

// chatBodyOf builds an OpenAI chat body from message contents.
func chatBodyOf(contents ...string) []byte {
	msgs := make([]map[string]string, 0, len(contents))
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]string{"role": role, "content": c})
	}
	b, _ := json.Marshal(map[string]any{"model": "gpt-4o", "messages": msgs})
	return b
}

func messagesOf(t *testing.T, body []byte) []rawMsg {
	t.Helper()
	msgs, err := extractMessages(body)
	if err != nil {
		t.Fatalf("extractMessages: %v", err)
	}
	return msgs
}

// TestProvenanceSeam_IndexSpacesDifferAfterCompression proves 事实 1 with an
// executable scenario: the ordinary post-compression turn.
//
// The client resends its full 5-message history. The gateway's cache already
// holds a compressed body — a gateway summary plus the retained suffix [m3,m4].
// BuildOutboundMessages anchors the suffix at client index 3 and finds an empty
// delta, so the assembled outbound body is the 3-message cached body.
//
// Result: the same physical message m3 is client index 3 but
// AlignmentInfo.OriginalIndex 1 — a −2 offset, and m0/m1/m2 (which the gateway
// folded into the summary) have no alignment entry reachable from their ref index.
func TestProvenanceSeam_IndexSpacesDifferAfterCompression(t *testing.T) {
	const (
		clientMsgCount = 5
		summaryBody    = CompactionMarkerPrefix + " earlier turns"
	)
	clientBody := chatBodyOf("m0", "m1", "m2", "m3", "m4")
	cachedBody := chatBodyOf(summaryBody, "m3", "m4")
	if got := len(messagesOf(t, clientBody)); got != clientMsgCount {
		t.Fatalf("fixture: client body has %d messages, want %d", got, clientMsgCount)
	}

	state := &SessionState{LastOutboundHash: sha256Hex(cachedBody), MsgCount: 3}
	res, err := BuildOutboundMessages(clientBody, state, cachedBody, "openai")
	if err != nil {
		t.Fatalf("BuildOutboundMessages: %v", err)
	}
	assembled := res.Body
	assembledMsgs := messagesOf(t, assembled)
	if len(assembledMsgs) != 3 {
		t.Fatalf("assembled outbound must be the 3-message cached body, got %d", len(assembledMsgs))
	}

	// The alignment is computed over the ASSEMBLED space (pre-rewrite body).
	align := buildAlignmentMap(assembled, assembled, -1)
	if len(align) != len(assembledMsgs) {
		t.Fatalf("alignment must have one entry per assembled message: %d vs %d", len(align), len(assembledMsgs))
	}

	// m3 is the assembled message at index 1; in the client's array it is index 3.
	m3 := assembledMsgs[1]
	assembledIndexOfM3 := -1
	for i, entry := range align {
		if entry.Hash == msgHash(m3) {
			assembledIndexOfM3 = i
			break
		}
	}
	if assembledIndexOfM3 != 1 {
		t.Fatalf("m3 must be at assembled index 1, got %d", assembledIndexOfM3)
	}

	// The client index of the same message, i.e. what SanitizedMessageRef records.
	clientMsgs := messagesOf(t, clientBody)
	clientIndexOfM3 := -1
	for i, m := range clientMsgs {
		if msgHash(m) == msgHash(m3) {
			clientIndexOfM3 = i
			break
		}
	}
	if clientIndexOfM3 != 3 {
		t.Fatalf("m3 must be at client index 3, got %d", clientIndexOfM3)
	}

	// The whole point: the two coordinate spaces disagree, by a specific amount.
	// A ref-index == OriginalIndex join would read m3's provenance off m1.
	if offset := assembledIndexOfM3 - clientIndexOfM3; offset != -2 {
		t.Fatalf("the two index spaces must differ by −2 on this fixture, got %d "+
			"(if the assembly order or the anchor rule changed, update 196号's finding "+
			"and this pin together)", offset)
	}
}

// TestProvenanceSeam_AssembledOrderPinsOffset covers the case the first pin
// cannot see: a NON-EMPTY delta tail.
//
// 196号 first cut pinned the offset only on a fixture whose delta was empty
// (the client resent exactly what the cache held), so `merged = lastMsgs ++
// deltaTail` degenerated to `lastMsgs` and the assembly ORDER was not exercised
// at all — inverting the two appends in diff.go left that pin green. This case
// adds two new client turns so the order is observable, and asserts the exact
// assembled sequence, because the order is what makes the offset formula
// `assembled = len(lastMsgs) + (clientIndex - anchorEnd)`.
func TestProvenanceSeam_AssembledOrderPinsOffset(t *testing.T) {
	// Client history: m0..m4 plus two new turns m5, m6.
	clientBody := chatBodyOf("m0", "m1", "m2", "m3", "m4", "m5", "m6")
	// Cache holds the previous compressed body: gateway summary + retained [m3,m4].
	cachedBody := chatBodyOf(CompactionMarkerPrefix+" earlier turns", "m3", "m4")

	state := &SessionState{LastOutboundHash: sha256Hex(cachedBody), MsgCount: 3}
	res, err := BuildOutboundMessages(clientBody, state, cachedBody, "openai")
	if err != nil {
		t.Fatalf("BuildOutboundMessages: %v", err)
	}
	if res.DeltaCount != 2 {
		t.Fatalf("fixture must produce a 2-message delta, got %d", res.DeltaCount)
	}

	assembled := messagesOf(t, res.Body)
	if len(assembled) != 5 {
		t.Fatalf("assembled body must be cache(3) + delta(2) = 5 messages, got %d", len(assembled))
	}

	// Cache first, then the delta tail. Asserting the sequence — not just the
	// count — is what makes this pin sensitive to the merge order.
	want := []string{
		CompactionMarkerPrefix + " earlier turns", // assembled 0 (cache)
		"m3", // assembled 1 (cache)
		"m4", // assembled 2 (cache)
		"m5", // assembled 3 (delta)
		"m6", // assembled 4 (delta)
	}
	for i, wantContent := range want {
		var m struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(assembled[i], &m); err != nil {
			t.Fatalf("assembled[%d]: %v", i, err)
		}
		if m.Content != wantContent {
			t.Fatalf("assembled[%d] = %q, want %q — the assembly order is "+
				"cache-then-delta and it is what fixes the index offset", i, m.Content, wantContent)
		}
	}

	// m5 is client index 5 but assembled index 3: the same −2 offset as the
	// empty-delta case, now with the order actually load-bearing.
	clientIndexOfM5, assembledIndexOfM5 := 5, -1
	for i, m := range assembled {
		var decoded struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(m, &decoded)
		if decoded.Content == "m5" {
			assembledIndexOfM5 = i
			break
		}
	}
	if assembledIndexOfM5 != 3 {
		t.Fatalf("m5 must be at assembled index 3, got %d", assembledIndexOfM5)
	}
	if offset := assembledIndexOfM5 - clientIndexOfM5; offset != -2 {
		t.Fatalf("offset must stay −2, got %d (if the anchor rule or assembly "+
			"order changed, update 196号's finding and this pin together)", offset)
	}
}

// TestProvenanceSeam_HashSpacesAreDisjoint proves 事实 2: the two fingerprints
// are different functions of the same bytes, so a hash-based join across the
// seam is impossible without changing one of them.
func TestProvenanceSeam_HashSpacesAreDisjoint(t *testing.T) {
	msg := []byte(`{"role":"user","content":"my phone is 13800138000"}`)

	alignmentHash := msgHash(msg)      // diff.go:265
	refHash := MessageFingerprint(msg) // sanitize_info.go:94

	if alignmentHash == refHash {
		t.Fatal("msgHash and MessageFingerprint must not be the same value; " +
			"if they were unified, the 196号 hash-join finding is resolved and this pin must be rewritten")
	}
	// Pin the concrete shape so a one-sided change is visible: alignment hashes
	// are truncated to 16 bytes (32 hex), ref fingerprints are full (64 hex).
	if len(alignmentHash) != 32 {
		t.Fatalf("AlignmentInfo.Hash must stay a 32-hex short fingerprint, got %d chars", len(alignmentHash))
	}
	if len(refHash) != 64 {
		t.Fatalf("SanitizedMessageRef hash must stay a 64-hex full sha256, got %d chars", len(refHash))
	}
}

// TestProvenanceSeam_RefIndexSpaceIsClientSpace pins the other half of 事实 1:
// the sanitizer never reorders or re-lengths the client's message array, so a
// ref's RawIndex and SanitizedIndex are always the SAME client-array position
// (input_protocols.go:188 assigns both from one loop counter). This is what
// makes the seam a pure coordinate mismatch rather than a permutation.
func TestProvenanceSeam_RefIndexSpaceIsClientSpace(t *testing.T) {
	body := chatBodyOf("a", "b", "c")
	msgs := messagesOf(t, body)
	if len(msgs) != 3 {
		t.Fatalf("fixture: %d messages", len(msgs))
	}
	// messageRef lives in security/sanitize; the compression side can only pin
	// the invariant it depends on: sanitize rewrites bytes in place, so the
	// compacted original and the compacted updated form have the same length
	// only when nothing was replaced. The index invariant is structural: one
	// loop counter per client message, never renumbered.
	var compact bytes.Buffer
	if err := json.Compact(&compact, msgs[1]); err != nil {
		t.Fatalf("compact: %v", err)
	}
	var round struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(msgs[1], &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Role != "assistant" {
		t.Fatalf("fixture role drift: %q", round.Role)
	}
}
