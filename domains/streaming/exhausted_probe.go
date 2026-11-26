package streaming

// exhausted_probe.go —— 「额度耗尽节点」的**需求驱动复探**钩子。
//
// # 背景（2026-11-26 · 211 充值事故）
//
// 凭据被判「永久配额耗尽」后，候选 SQL（provider/client.go:1292 等五处）
// 会把它**整体排除** ⇒ 该模型零候选 ⇒ 没有业务流量 ⇒ 也就没有新的健康证据
// ⇒ 只能等余额探针的**时间轮询**（退避上限 1 小时）⇒ 「充值了但网关最长
// 1 小时不知道」。
//
// # 本钩子做什么
//
// 新请求打到一个模型上时，若该模型挂着**额度已耗尽**的节点 ⇒ 立刻通知上游
// 为这些节点发起一次探活（不等 1 小时退避）。探到已恢复 ⇒ 凭据翻绿 ⇒ 候选
// 恢复 ⇒ 模型立刻可用。
//
// 探测间隔由 bg.ExhaustedNodeDemandProbe 自动回退（30s→1m→2m→4m→5m 封顶），
// 所以高频请求不会把上游打爆。
//
// # 纪律
//
//   - **可选钩子**：nil ⇒ 完全惰性（行为与本文件上线前逐字节相同）；
//   - **不阻塞响应**：通知走独立 goroutine，客户端延迟不受影响；
//   - **脱离请求 ctx 的取消**：客户端断开不该取消后台探测（探测有自己的
//     2s 查询预算）。

import "context"

// exhaustedNodeProbeTrigger 是钩子的函数契约（装配方注入）。
//
// model/tenantID 是「本次请求问的那个模型」与租户；实现方据此找出该模型
// 下额度耗尽的凭据并发起探活。
type exhaustedNodeProbeTrigger func(ctx context.Context, model, tenantID string)

// SetExhaustedNodeProbeTrigger 装配方注入需求驱动复探钩子（cmd/gateway/main.go）。
func (h *ChatHandler) SetExhaustedNodeProbeTrigger(fn func(ctx context.Context, model, tenantID string)) {
	if h == nil {
		return
	}
	h.exhaustedNodeProbeTrigger = fn
}

// notifyExhaustedNodes 在**候选解析之后**调用：告诉上游「有人正在等这个模型」。
//
// ★ 为什么放在解析之后而不是之前：解析失败（DB 抖动）时没有「模型被耗尽」
//
//	这件事可言；而解析成功（含零候选）才是事实。
//
// ★ 为什么放在「零候选出口」之外：模型**部分**可用时（还有别的候选在跑），
//
//	那些耗尽的节点同样值得探活 —— 它们恢复后能立刻补位。
func (h *ChatHandler) notifyExhaustedNodes(ctx context.Context, model, tenantID string) {
	if h == nil || h.exhaustedNodeProbeTrigger == nil {
		return
	}
	if model == "" {
		return
	}
	fn := h.exhaustedNodeProbeTrigger
	// ★ 脱离请求 ctx 的取消（客户端断开/超时不该取消后台探测），但保留 ctx
	//   里的值（租户等）；探测自身另有 2s 查询预算。
	detached := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				// 钩子是旁路：panic 绝不能带走请求 goroutine。
				_ = rec
			}
		}()
		fn(detached, model, tenantID)
	}()
}
