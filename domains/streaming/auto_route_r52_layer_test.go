package streaming

// auto_route_r52_layer_test.go — R52（2026-10-01）role 兜底层归属字段的
// wire 契约门。
//
// 为什么要单独一道门：字段加在 autoroute.Decision 上**不等于**运维能看到。
// 运维读的是 X-Gw-Auto-Decision 头 / request_logs.auto_decision，二者都由
// autoRouteDecision 序列化。少一次映射，字段就只活在内存里——而内存里的
// 字段在生产上等于不存在。这道门钉住"Decision → wire → JSON"整条链。
import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

func TestDecisionToWire_RoleFallbackLayerRoundTrip(t *testing.T) {
	// 兜底层命中：必须透出。
	w := decisionToWire(&autoroute.Decision{
		ChosenModel:       "claude-opus-5",
		TaskType:          autoroute.TaskChat,
		SessionRole:       "worker",
		TaskKind:          "search",
		RoleFallbackLayer: "mainstream",
	})
	if w.RoleFallbackLayer != "mainstream" {
		t.Fatalf("wire.RoleFallbackLayer = %q, want mainstream", w.RoleFallbackLayer)
	}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"role_fallback_layer":"mainstream"`) {
		t.Fatalf("序列化后缺 role_fallback_layer: %s", raw)
	}

	// flag-off / 未命中：键必须整个消失（omitempty），保证既有请求的
	// auto_decision 字节级不变。
	w2 := decisionToWire(&autoroute.Decision{ChosenModel: "minimax-m3", TaskType: autoroute.TaskChat})
	raw2, err := json.Marshal(w2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw2), "role_fallback_layer") {
		t.Fatalf("未命中时不应出现该键（会改变既有 auto_decision 字节流）: %s", raw2)
	}
}
