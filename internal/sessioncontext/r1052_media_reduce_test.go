package sessioncontext

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

// r1052（2026-11-13，211 fable-5-2 入口 422 根修）钉测。
//
// 事故形状（归档件 254340c9…，1,365,000B）：ZCode 会话历史里带截图
// （content 数组里的 image_url 块，>64KB 的巨型消息内含 base64 媒体）。
// LargeIngress 对 >1MiB 的请求调 Reduce 收敛到 ContextBytes；三处媒体
// fail-closed（fileMetadata 媒体块/消息级媒体字段、≤64KB 消息的
// UpperBound Uncountable、objectExcerpt 超大非字符串字段）任一命中 ⇒
// 整个 Reduce 失败 ⇒ 422「cannot produce a valid bounded context; split
// the protected input」⇒ 带截图的会话每笔请求必败（客户端无法「split」
// 任何东西——媒体在历史里）。修复＝三处全部改为打桩/跳过/降级继续：
// 媒体计量归 contextbudget 的 dispatch 面，缩减层只需要 role+工具链。
func TestR1052ReduceSurvivesMediaHistory(t *testing.T) {
	const oneMiB = 1 << 20
	// >1MiB body: media message in OLD history + big text padding + tail user.
	big := strings.Repeat("conversation filler line for the megabyte padding test. ", 180)
	msgs := []map[string]any{
		{"role": "system", "content": "You are ZCode."},
		// 首个 human user 是纯文本（受保护）；媒体消息在历史中部（非保护，
		// 与归档件同形——媒体在 idx 49，首个 user 在更早）。
		{"role": "user", "content": "please investigate the deployment records"},
		{"role": "assistant", "content": "on it"},
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "Tool result media from Read:"},
			map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "data:image/png;base64," + strings.Repeat("A", 4000),
			}},
		}},
		{"role": "assistant", "content": "got it"},
	}
	for i := 0; i < 160; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": big})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "ack " + jsonItoa(i)})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "请继续"})
	body, _ := json.Marshal(map[string]any{"model": "m", "max_tokens": 64, "messages": msgs})
	if len(body) <= oneMiB {
		t.Fatalf("fixture premise broken: body %d bytes must exceed 1MiB", len(body))
	}

	store, ref := captureForReduce(t, body)
	out, report, err := Reduce(context.Background(), store, ref, oneMiB)
	if err != nil {
		t.Fatalf("Reduce must survive media history (r1052): %v", err)
	}
	if !report.Reduced || len(out) > oneMiB {
		t.Fatalf("reduce report=%+v outBytes=%d", report, len(out))
	}
	if !bytes.Contains(out, []byte("请继续")) {
		t.Fatal("latest user turn lost from reduced context")
	}
	if bytes.Contains(out, []byte("AAAA")) {
		t.Fatal("base64 media payload leaked into the reduced context")
	}
	if !bytes.Contains(out, []byte("media omitted")) {
		t.Fatal("media dropped without the explicit omission marker (silent removal)")
	}
}

// 巨型（>64KB）消息内嵌媒体块——事故件的精确形态：fileMetadata 的媒体
// 拒绝路径。修复后打桩继续，缩减成功。
func TestR1052ReduceSurvivesGiantMediaMessage(t *testing.T) {
	const oneMiB = 1 << 20
	giantMedia := []any{
		map[string]any{"type": "text", "text": "Tool result media from Read:"},
		map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:image/png;base64," + strings.Repeat("B", 340*1024),
		}},
	}
	pad := strings.Repeat("history padding line for volume. ", 23000)
	msgs := []any{
		map[string]any{"role": "system", "content": "You are ZCode."},
		map[string]any{"role": "user", "content": "please investigate the deployment records"},
		map[string]any{"role": "assistant", "content": "on it"},
		map[string]any{"role": "user", "content": giantMedia},
		map[string]any{"role": "assistant", "content": "got it"},
		map[string]any{"role": "user", "content": pad},
		map[string]any{"role": "user", "content": "请继续"},
	}
	body, _ := json.Marshal(map[string]any{"model": "m", "max_tokens": 64, "messages": msgs})
	if len(body) <= oneMiB {
		t.Fatalf("fixture premise broken: body %d bytes must exceed 1MiB", len(body))
	}
	store, ref := captureForReduce(t, body)
	out, _, err := Reduce(context.Background(), store, ref, oneMiB)
	if err != nil {
		t.Fatalf("Reduce must survive a giant media message (r1052): %v", err)
	}
	if len(out) > oneMiB || bytes.Contains(out, []byte("BBBB")) {
		t.Fatalf("out=%dB mediaLeaked=%t", len(out), bytes.Contains(out, []byte("BBBB")))
	}
	if !bytes.Contains(out, []byte("media omitted")) {
		t.Fatal("giant media message dropped without the explicit omission marker")
	}
}

// 回归钉：非媒体的「受保护区装不下」仍必须失败——r1052 只放宽媒体面，
// 预算等式本身不动。
func TestR1052ProtectedOverflowStillFails(t *testing.T) {
	const maxBytes = 64 << 10
	msgs := []any{
		map[string]any{"role": "system", "content": strings.Repeat("s", maxBytes)},
		map[string]any{"role": "user", "content": strings.Repeat("u", 4096)},
	}
	body, _ := json.Marshal(map[string]any{"messages": msgs})
	store, ref := captureForReduce(t, body)
	if _, _, err := Reduce(context.Background(), store, ref, maxBytes); err == nil {
		t.Fatal("protected-region overflow must still fail")
	}
}

func captureForReduce(t *testing.T, body []byte) (*largebody.Store, largebody.Ref) {
	t.Helper()
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "gw_staging_r1052_test"}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return store, ref
}

func jsonItoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
