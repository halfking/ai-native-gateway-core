// asm_event_handler.go — task_type=asm.event.v1 的真实消费 handler
// （会话治理方案 2026-11 P1-1 第一步）。
//
// 定位：把 internal/outbox 的投递执行体（HTTPDeliverer：HMAC 签名 +
// batch POST /internal/v1/events + 退避语义）作为本 handler 的执行核，
// 对齐 doc.go 平迁四步的第 1 步（双写期）——asm.event.v1 从此有真实
// 消费能力，切表波接上双写桥即可承接生产流量。
//
// 依赖方向（过渡性）：taskbus → outbox（泛化层引用被泛化者，outbox
// 不反向依赖 taskbus，无环）。outbox 退役（第 4 步）时此依赖随之消除。
//
// payload 契约：task.Payload = outbox.EventEnvelope 的 JSON 序列化
//（json tag 齐全，自包含 event_id/event_type/payload 等；双写桥从
// outbox_events 行列重建 envelope 后序列化，或直接序列化 builder 产物）。
// 消费端幂等：ASM ingress 按 event_id 去重（既有契约），handler 侧
// 无需额外去重——与 outbox Dispatcher 的单事件投递语义一致。
package taskbus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
)

// ASMDeliverer 是 ASM 事件投递的窄接口。生产实现是
// *outbox.HTTPDeliverer（Deliver(ctx, env) error）；测试注入 fake。
// 保持接口而非具体类型，让 taskbus 不绑定 HTTP 细节。
type ASMDeliverer interface {
	Deliver(ctx context.Context, env outbox.EventEnvelope) error
}

// NewASMEventBridgeHandler 返回真实消费 handler：解 payload → 投递。
//
// 错误语义：Deliver 的错误原样返回——taskbus Dispatcher 的
// attempts/maxAttempts/backoff/DLQ 策略接管重试（与 outbox Dispatcher
// 的行内重试不同层，双写期两边各自的 at-least-once 语义互不干扰，
// ASM 侧按 event_id 幂等收敛）。unmarshal 失败视为毒任务：返回包裹
// 错误（重试耗尽后自然 DLQ，与 outbox 对 poison-pill 直落 DLQ 的处置
// 等价——不额外短路，保持 Dispatcher 策略单一）。
func NewASMEventBridgeHandler(deliverer ASMDeliverer) Handler {
	return HandlerFunc(func(ctx context.Context, task Task) error {
		if deliverer == nil {
			return fmt.Errorf("taskbus asm.event.v1: nil deliverer (bridge not wired)")
		}
		var env outbox.EventEnvelope
		if err := json.Unmarshal(task.Payload, &env); err != nil {
			return fmt.Errorf("taskbus asm.event.v1: decode envelope (task %d, key %q): %w",
				task.ID, task.IdempotencyKey, err)
		}
		if env.EventID == "" || env.EventType == "" {
			return fmt.Errorf("taskbus asm.event.v1: envelope missing event_id/event_type (task %d)", task.ID)
		}
		return deliverer.Deliver(ctx, env)
	})
}
