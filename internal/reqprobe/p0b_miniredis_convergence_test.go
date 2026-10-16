package reqprobe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestP0BMiniredisCrossInstanceLearnStrip：raw-logs 形拒绝体 → 实例 A 学习 →
// 同 miniredis 上的实例 B 首发即剥字段。同层驱动 Coordinator.RecordTerminal
// + LearnedParams + StripParamsForWire（生产出站 choke 共用）。
func TestP0BMiniredisCrossInstanceLearnStrip(t *testing.T) {
	storeA, mr := newTestRedisStore(t)
	rdbB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdbB.Close() })
	storeB := NewRedisStore(rdbB, "llmgw:reqanom:test")

	coordA := NewCoordinator(storeA)
	defer coordA.Close()
	// vapeur 真实拒绝形态：经 Diagnose 生产入口提取字段（非手填 Diagnosis）。
	rawBody := []byte(`{"error":{"message":"Unknown parameter: 'service_tier'","type":"invalid_request_error","param":"service_tier"}}`)
	in := Input{HTTPStatus: 400, ErrorBody: rawBody, Protocol: "openai-responses"}
	diag, ok := Diagnose(in)
	if !ok || diag.Param != "service_tier" {
		t.Fatalf("Diagnose = %+v ok=%v, want service_tier (前提在场)", diag, ok)
	}
	coordA.RecordTerminal(in, diag, TerminalMeta{ProviderCode: "vapeur", OutboundModel: "grok-4.6", ProviderID: 1}, false)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recs, _, err := storeA.List(context.Background(), Filter{ProviderCode: "vapeur", Model: "grok-4.6"})
		if err == nil && len(recs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 实例 B：新 Coordinator + 同 Redis —— 必须看见已学字段。
	coordB := NewCoordinator(storeB)
	defer coordB.Close()
	var learned []string
	found := false
	for time.Now().Before(deadline.Add(2 * time.Second)) {
		coordB.InvalidateLearned()
		learned = coordB.LearnedParams(context.Background(), LearnedKey{ProviderID: 1, OutboundModel: "grok-4.6", ActualWire: "openai-responses"})
		for _, p := range learned {
			if p == "service_tier" {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("instance B LearnedParams = %v, want service_tier (cross-instance)", learned)
	}
	body := []byte(`{"model":"grok-4.6","messages":[],"service_tier":"default","stream_options":{"include_usage":true}}`)
	out, stripped := StripParamsForWire(body, "service_tier", "openai-responses")
	if len(stripped) != 1 || stripped[0] != "service_tier" {
		t.Fatalf("strip after learn = %v, want [service_tier]", stripped)
	}
	if strings.Contains(string(out), "service_tier") {
		t.Fatalf("body still has service_tier after strip: %s", out)
	}
	// 前提在场：chat 线 stream_options 骨架仍保护（负向钉子）。
	_, so := StripParamsForWire(body, "stream_options", "openai-chat")
	if len(so) != 0 {
		t.Fatalf("chat wire must keep stream_options, stripped=%v", so)
	}
}
