// Package dispatch implements the multi-tier request-queue scheduling pipeline
// (model queue → credential queue → forwarder) with per-credential concurrency
// modes (concurrency / rpm / tpm / disabled) and decoupled executors.
//
// See docs/会话优化v2/57-多层队列调度架构设计方案.md for the full design.
//
// This package MUST NOT import domains/streaming/executors (cycle); it obtains
// routing/forwarding/model-resolution capabilities via callbacks (RouteFunc,
// ForwardFunc, ModelResolveFunc) supplied by the executor wiring.
package dispatch

import (
	"os"
	"sync/atomic"
)

// AUDIT_24H B2b (2026-08-17): the dispatch_v2.enabled kill-switch was retired.
// The multi-tier pipeline is the only execute path — the legacy synchronous
// candidate loop in executor.Execute was deleted, so a gate that could "fall
// back" no longer had anything to fall back to. The dispatch_v2.enabled spec,
// boot sync, admin hot-reload branch and transition handlers went with it.
// Snapshot views keep a literal `Enabled: true` for admin UI compatibility.

// ModelChangeGateKey controls pre-first-byte model failover.
//
// 2026-11-16（auto 模式可用性轮）：默认由 false 改为 true。
//
// 改默认值的理由（211 实测取证，docs/audit/2026-11-16-*）：auto 请求
// （model="auto"）此前**没有任何模型级回退**——同一模型的所有凭据耗尽后
// 直接把错误抛给客户端，而 30 天内 87 笔 auto 请求里 13 笔失败、
// fallback_used 全部为 false；其中 7 笔集中在同一条坏凭据上，会话表现为
// 「原来的模型不可用」。auto 模式要「真正可用」，模型级回退必须是默认行为
// 而不是可选项。
//
// 作用域**只在 auto 请求**：本闸门的每一处消费点都额外要求
// `DispatchAllowModelChange`/`qr.AllowModelChange`，而那两个字段的唯一
// 来源是 `logCtx.IsAutoRequest`（domains/streaming/handler.go:4915、
// responses.go:1184 的 requestDispatchOptions、executor_dispatch.go:462）。
// 因此非 auto 请求的字节级行为不受影响——它们在闸门打开时同样拿不到
// 换模许可。
//
// 运维仍可随时通过平台设置热加载关闭本闸门（admin settings PUT）。
const ModelChangeGateKey = "dispatch_v2.allow_model_change"

var (
	// modelChangeEnabled is the hot-path atomic cache of the allow_model_change
	// gate. Read on the dispatch failover path so requests never touch the DB.
	modelChangeEnabled atomic.Bool
)

func init() {
	// 默认开启（见 ModelChangeGateKey 的注释）：auto 请求在同模型凭据耗尽时
	// 应当换一个模型继续，而不是把错误抛给客户端。
	modelChangeEnabled.Store(true)
}

// IsModelChangeEnabled reports whether dispatch may ask autoroute for another
// model after all credentials under the current model are exhausted.
func IsModelChangeEnabled() bool {
	return modelChangeEnabled.Load() || os.Getenv("AUTO_ROUTE_FALLBACK_ENABLED") == "true"
}

// SetModelChangeEnabled updates the hot-reloadable platform setting. The
// legacy AUTO_ROUTE_FALLBACK_ENABLED=true override remains force-on.
func SetModelChangeEnabled(v bool) {
	modelChangeEnabled.Store(v)
}
