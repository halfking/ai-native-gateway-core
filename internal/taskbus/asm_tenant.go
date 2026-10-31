package taskbus

import "context"

// RegisterASMTenant 登记 task_type=asm.event.v1 为总线第一个存量租户。
//
// 本期 handler 是占位：真实投递仍由 internal/outbox.Dispatcher 负责
// （见 doc.go 平迁方案）。登记本身满足 V2「无消费者=红」——类型已有
// 注册位，切表波再把 outbox 投递逻辑迁入本 handler。
func RegisterASMTenant(r *Registry) {
	if r == nil {
		return
	}
	r.MustRegister(TaskTypeASMEventV1, HandlerFunc(func(ctx context.Context, task Task) error {
		// 占位：切表前 ASM 投递仍走 outbox；本 handler 只证明类型有消费者。
		_ = ctx
		_ = task
		return nil
	}))
}
