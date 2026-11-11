package streaming

import (
	"encoding/json"
	"testing"

	tooldialect "github.com/kaixuan/llm-gateway-go/internal/tooldialect"
)

// sentinelDialect 是装配分叉守卫的哨兵方言：StartArguments 返回可辨识标记。
type sentinelDialect struct{ marker string }

func (s sentinelDialect) Name() string { return "sentinel/" + s.marker }
func (s sentinelDialect) StartArguments(raw []byte) string {
	return s.marker
}
func (s sentinelDialect) MergeContinuation(start, buffered string) (string, bool) {
	return start + buffered, json.Valid([]byte(start + buffered))
}

// TestBridgeToolArgsDialectFollowsRegistry 钉住 2026-11-11（48h 审计遗留）
// 收口的装配分叉：桥此前持有 init 时钉死的实例，与 internal/ir 的
// ForProtocol 活查表分叉 ⇒ 后续 Register 会静默分裂两侧语义（ir 用新方言、
// 桥用旧实例）。门：① 桥与注册表返回**同一实例**；② 行为级——注册哨兵
// 方言后，桥立即看见哨兵（证明「Register 即全链生效」）；③ 测试退出时
// 恢复生产方言（不能让哨兵泄漏给同包其它测试）。
func TestBridgeToolArgsDialectFollowsRegistry(t *testing.T) {
	restore := tooldialect.ForProtocol(tooldialect.ProtocolAnthropicMessages)
	defer func() {
		tooldialect.Register(tooldialect.ProtocolAnthropicMessages, restore)
	}()

	// ① 同一实例（恒等）。
	if bridgeToolArgsDialect() != tooldialect.ForProtocol(tooldialect.ProtocolAnthropicMessages) {
		t.Fatal("bridge dialect and registry lookup must be the same instance")
	}

	// ② 行为级：注册哨兵 ⇒ 桥立即看见。
	const marker = "SENTINEL-BRIDGE-SEES-ME"
	tooldialect.Register(tooldialect.ProtocolAnthropicMessages, sentinelDialect{marker: marker})
	if got := bridgeToolArgsDialect().StartArguments([]byte(`{}`)); got != marker {
		t.Fatalf("bridge did not follow registry re-registration: got %q", got)
	}
	// ③ 内联恢复（defer 只兜 t.Fatal 的失败路径）；恢复后桥不得再返回哨兵。
	tooldialect.Register(tooldialect.ProtocolAnthropicMessages, restore)
	if got := bridgeToolArgsDialect().StartArguments([]byte(`{}`)); got == marker {
		t.Fatal("sentinel leaked past restore")
	}
}

// TestBridgeDialectIsValidatorInjected 反向守卫：生产装配必须把 builtin
// 种子升级为注入校验器的方言（不能退回直通种子）。
func TestBridgeDialectIsValidatorInjected(t *testing.T) {
	d := bridgeToolArgsDialect()
	if d.Name() == "passthrough/tool-args" {
		t.Fatal("production wiring regressed to passthrough seed")
	}
}
