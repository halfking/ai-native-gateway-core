package compression

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ★ 本文件是**校验器单测之外的第二道腿**：上游 `02845e210` 补的是
// `ValidateSessionID` 的**单点**用例（7 条）；★ 本文件钉的是**压缩链整路径**
// （Prepare → CommitFinal → 缓存真有条目），★ 也就是「守卫放行了」之后
// 「压缩真的跑起来了」这后半程。
//
// ★ 修复前的形态（读数来自 211 生产）：
//   - Prepare 命中 `ValidateSessionID` 的子串闸 ⇒ 走 `fallbackResult`
//     ⇒ 会话不进缓存；
//   - CommitFinal 同样被拦 ⇒ 报 `commit final session cache failed`；
//   - ⇒ 该命名空间下 **7124 轮会话的 compression_reason 全空**（压缩命中 0），
//     而其它命名空间（131283 轮）压缩正常。
//
// ★ 修复后的契约（本文件钉住的三条）：
//   1. 211 生产的两种真实形态都**进得去**压缩路径：
//      `gw_session_<32 hex>` 与 `gw_session_<uuid-with-dashes>`；
//   2. CommitFinal **不**报错，且**缓存里真查得到条目**；
//   3. 真占位符仍然走降级路径（守卫没被削弱）。
//
// ★ 为什么 uuid 形态必须单列一条：★ 若把豁免实现成「结尾必须是连续 hex」
// （形态闸），★ `session_<uuid-with-dashes>` 会被**误拒** ⇒ **修好 A 的同时打死 B**
// ⇒ 211 实测 47 个 distinct 会话里有 **2 个 uuid 形态** ⇒ 这条用例是那道防线的锚。

// TestGwSessionNamespaceReachesCompressionPath 端到端证明：211 生产的
// `gw_session_<32 hex>` 会话**不再**被静默降级成「无会话模式」。
func TestGwSessionNamespaceReachesCompressionPath(t *testing.T) {
	assertReachesCompressionPath(t, "gw_session_5a3706a04683385ba1927ff588a9f6f5")
}

// TestGwSessionUUIDFormReachesCompressionPath 端到端证明：**非 hex 尾**的
// 生产形态（uuid-with-dashes）同样进得去 —— 豁免按「前缀惯例 + 有熵的尾」裁决，
// 而不是按「必须是连续 hex」裁决。
func TestGwSessionUUIDFormReachesCompressionPath(t *testing.T) {
	assertReachesCompressionPath(t, "gw_session_c7ebd2f5-c44f-42dd-ba2c-6e0157aee3db")
}

// TestWeakPlaceholderSessionStillDegrades 反向自证：真占位符 id 仍然
// 被判死并静默降级（本次修复没有削弱守卫）。
func TestWeakPlaceholderSessionStillDegrades(t *testing.T) {
	ctx := context.Background()
	cache := NewSessionCache(nil, nil)
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})

	const placeholderID = "test-session-123"

	body := buildLongChatBody(t, 40)
	result := sc.Prepare(ctx, body, "tenant-r1029", placeholderID, "openai", 0, false)
	if result == nil {
		t.Fatal("Prepare returned nil result")
	}

	// 修复前该形态降级为 sessionless ⇒ CommitFinal 直接报
	// 「commit final session cache failed: session_id contains forbidden keyword」。
	// 修复后仍必须报错（守卫没被削弱）。
	if err := sc.CommitFinal(ctx, "tenant-r1029", placeholderID, body, result); err == nil {
		t.Fatal("CommitFinal succeeded for a weak placeholder session id ⇒ the guard was weakened")
	}
}

// assertReachesCompressionPath 断言一个 gw_session_* 会话**真的进了压缩链**：
// Prepare 不降级 + CommitFinal 不报错 + **缓存里查得到条目**。
//
// ★ 判据用「缓存里能不能查到该会话」——比读内部字段更贴近真实后果
// （用户可见的「压缩命中」读数就是从缓存侧产生的）。
func assertReachesCompressionPath(t *testing.T, gwSessionID string) {
	t.Helper()
	ctx := context.Background()
	cache := NewSessionCache(nil, nil)
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})

	// 一个足够长的多轮历史（真实上游形态：多轮 user/assistant 交替）。
	body := buildLongChatBody(t, 40)

	result := sc.Prepare(ctx, body, "tenant-r1029", gwSessionID, "openai", 0, false)
	if result == nil {
		t.Fatal("Prepare returned nil result")
	}

	if err := sc.CommitFinal(ctx, "tenant-r1029", gwSessionID, body, result); err != nil {
		t.Fatalf("CommitFinal failed for a production gw_session_* id: %v", err)
	}

	state, cachedBody, err := cache.GetOrLoad(ctx, "tenant-r1029", gwSessionID)
	if err != nil {
		t.Fatalf("GetOrLoad failed: %v", err)
	}
	if state == nil || len(cachedBody) == 0 {
		t.Fatalf("session cache has no entry ⇒ the gw_session_* namespace was still degraded to sessionless mode (id=%s)", gwSessionID)
	}
}

// buildLongChatBody 构造一段多轮对话的 OpenAI chat 请求体。
func buildLongChatBody(t *testing.T, turns int) []byte {
	t.Helper()
	messages := make([]map[string]any, 0, turns*2)
	for i := 0; i < turns; i++ {
		messages = append(messages,
			map[string]any{"role": "user", "content": strings.Repeat("u", 64)},
			map[string]any{"role": "assistant", "content": strings.Repeat("a", 64)},
		)
	}
	raw, err := json.Marshal(map[string]any{
		"model":    "minimax-m3",
		"messages": messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
