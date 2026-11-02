package taskbus

import (
	"context"
)

// RegisterASMTenant 登记 task_type=asm.event.v1 为总线第一个存量租户。
//
// deliverer 非 nil ⇒ 登记真实消费 handler（NewASMEventBridgeHandler，
// P1-1 第一步：投递执行体复用 outbox.HTTPDeliverer，见 asm_event_handler.go）。
// deliverer == nil ⇒ 保留占位——真实投递仍由 internal/outbox.Dispatcher
// 负责（见 doc.go 平迁方案）；demo bus / 无 ASM 配置的形态用这个。
//
// 登记本身满足 V2「无消费者=红」——类型始终有注册位；双写桥（平迁第
// 1-2 步）接上后由本 handler 承接生产流量。
func RegisterASMTenant(r *Registry, deliverer ASMDeliverer) {
	if r == nil {
		return
	}
	if deliverer == nil {
		r.MustRegister(TaskTypeASMEventV1, HandlerFunc(func(ctx context.Context, task Task) error {
			// 占位：切表前 ASM 投递仍走 outbox；本 handler 只证明类型有消费者。
			_ = ctx
			_ = task
			return nil
		}))
		return
	}
	r.MustRegister(TaskTypeASMEventV1, NewASMEventBridgeHandler(deliverer))
}

// RegisterASMTenantPlaceholder 是 RegisterASMTenant(r, nil) 的显式别名，
// 供只想占位、不想传 nil 的调用方表达意图。
func RegisterASMTenantPlaceholder(r *Registry) {
	RegisterASMTenant(r, nil)
}
