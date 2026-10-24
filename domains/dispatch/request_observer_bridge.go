package dispatch

// request_observer_bridge.go — **每次上游尝试** ↔ 请求级观察者的接线。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P1-1）要求把统一观察者「**挂到 v1 真实路径**（不是 v2）」，且
// magpie 的 `usage.Observer` 的 `Watch` 收的是 **`Attempt` / `End`** 事件。
//
// ★ 为什么挂在 **observation emitter** 上（`Pipeline.Submit` 里那一个闭包）：
//   ① 它是**尝试级**观察的**唯一出口** —— 尝试开始/首字节/成功/失败都从这里走；
//   ② `Observation` 已经带齐了 Watch 需要的一切
//      （ProviderID / CredentialID / Model / Attempt / Outcome / ErrorKind /
//      HTTPStatus）⇒ **不需要**新增判定，也不需要碰热路径上的转发逻辑；
//   ③ 发布是**非阻塞**的 ⇒ 观察面慢或挂都**不得**拖慢转发。
//
// # 引用
//
//	internal/requestobserver —— 契约与队列语义（丢弃计数、字段级 allow-list）
//	domains/streaming/request_observer_bridge.go —— 请求级 Begin/End 接线

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/internal/requestobserver"
)

// magpieP1PublishAttempt 把**尝试终态**的 observation 翻译成 Watch 事件。
//
// ★ 只在「尝试成功 / 尝试失败」时发布（magpie 的 `Watch` 收 Attempt/End）。
//
//	其它 observation（入队、选凭据、首字节、重试…）不进观察面 ——
//	否则观察面会变成另一份 dispatch 事件流（那是 dispatch 自己的职责）。
func magpieP1PublishAttempt(ctx context.Context, observation Observation) {
	switch observation.Type {
	case ObservationAttemptSucceeded, ObservationAttemptFailed:
	default:
		return
	}
	hub := requestobserver.Default()
	if !hub.Enabled() {
		return
	}
	attempt := 0
	if observation.Attempt != nil {
		attempt = observation.Attempt.AttemptNo
	}
	hub.Watch(ctx, requestobserver.AttemptInfo{
		RequestID:     observation.RequestID,
		Attempt:       attempt,
		ProviderID:    int(observation.ProviderID),
		CredentialID:  int(observation.CredentialID),
		OutboundModel: observation.Model,
		Success:       observation.Type == ObservationAttemptSucceeded,
		StatusCode:    observation.HTTPStatus,
		ErrorKind:     observation.ErrorKind,
		At:            observation.OccurredAt,
	})
}
