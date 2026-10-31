// Package taskbus 是请求后处理的通用任务总线内核。
//
// # 定位
//
// 把 internal/outbox（Gateway→ASM 专用）的轮询 / 退避 / DLQ / 幂等语义
// **泛化**为按 TaskType 路由的总线。投递语义统一为 at-least-once +
// idempotency_key 去重（消费端 Handler 必须幂等）。
//
// # 与 outbox_events 的关系（本期不强制切表）
//
// ASM 事件登记为 task_type = "asm.event.v1"（见 asm_tenant.go）。
// 存量表平迁方案（注释即方案，本波不执行切表）：
//
//  1. 双写期：业务路径继续写 outbox_events；新租户写 task_events。
//  2. 桥接期：outbox Dispatcher 旁挂「把 pending 行投影为 asm.event.v1
//     任务」的适配器，或反过来由 taskbus 消费 outbox_events 视图。
//  3. 切读：新流量只进 task_events；outbox Dispatcher 只消化存量。
//  4. 停写 + 归档 outbox_events。
//
// 目标表形态（参考，非本波强制 migration）：
//
//	task_events(
//	  id, task_type, payload, payload_version, idempotency_key UNIQUE,
//	  status /*pending|failed|dlq|sent*/, attempts, next_retry_at,
//	  last_error, created_at, updated_at
//	)
//
// # 本波交付
//
// Store（Memory）+ Registry + Dispatcher + 每 TaskType 指标 +
// asm.event.v1 租户登记 + cache.save.v1 示范消费链。
package taskbus
