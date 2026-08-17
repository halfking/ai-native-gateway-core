# Live stream model dimension: prefer canonical (standard) name over vendor raw name

## Symptom

首页实时请求流首页按"模型"分维时，前端泳道的单条 tile 显示的 model 名称以及
泳道 key（极端情形下）仍带着供应商原始模型名（vendor raw model name），例如：

- `azure-gpt-4o-mini`、`gpt-4o-2024-08-06`、`claude-sonnet-4-5-20251001`
- 不同凭证托管同一标准模型 `minimax-m3` 时，前端泳道里出现多个变体名称，
  看起来像"模型数翻倍"。

## Root cause

`liveStreamDimensionKey` 的 `model` 分支已经做了两件事：

1. 优先用 `CanonicalName`（标准模型名）做分组 key；
2. 用 `normalizeModelKey` 做 case-insensitive 折叠，避免大小写分裂。

但**单条 tile** 仍然把供应商原始模型名当成 `LiveRequest.Model` 显示：

- `LiveRequestFromTelemetry`：`Model` 字段被设为
  `outboundModel → clientModel → canonicalName`，
  outbound 优先级最高，所以即便 canonical 解析成功、tile 也还是供应商原始名。
- `adminLiveRequestFromEntry` 的 hub-为-nil 兜底分支同理（用 `displayModel`，
  本质是 outbound 优先）。
- DB replay SQL（`admin/live_stream_sse.go` 的 `replay` 函数）：
  `model` 列为 `COALESCE(NULLIF(rl.outbound_model, ''), rl.client_model, '')`，
  同样 outbound 优先。

## Fix

把模型名回填顺序**统一倒过来**，让标准名永远优先：

### `admin/live_stream_sse.go`

- `LiveRequestFromTelemetry`：`Model` 选择顺序改为
  `canonical_name → clientModel → outboundModel`；`CanonicalName` 字段直接
  复用同一份已解析结果，不再调两次 `CanonicalNameFor`。
- `replay` SQL 的 `model` 列改为
  `COALESCE(NULLIF(mc.canonical_name, ''), NULLIF(rl.client_model, ''), rl.outbound_model, '')`，
  与 telemetry 路径保持一致。
- `CanonicalNameFor` 把 cache `sync.Map` 的检查提前到 `h.db == nil` 守卫之前，
  让单元测试可以用 `hub.canonicalCache.Store(...)` 预填并驱动 fallback 链路
  验证，而不必立一个真实的 PG。
- 更新 `LiveRequest` 结构体字段注释和 `LiveRequestFromTelemetry` 的 fallback
  chain 文档字符串，明确指出"outbound 仅作为最后兜底"。

### `cmd/gateway/main.go`

- `adminLiveRequestFromEntry` 的 hub-为-nil 兜底改为
  `clientModel → outboundModel`，同时去掉无用的 `displayModel` 中间变量。

## Verification

- 新增 `admin/live_stream_drift_test.go::TestLiveRequestFromTelemetry_ModelPrefersCanonicalName`，
  覆盖 4 个场景：
  1. canonical 解析成功 → 用 canonical
  2. canonical 缺失但 client 有 → 用 client（不再用 outbound）
  3. canonical 和 client 都为空 → 用 outbound（向后兼容）
  4. canonical 与 client/outbound 都冲突 → 必须用 canonical
- 每个 case 还断言 `liveStreamDimensionKey("model", got)` 等于
  `normalizeModelKey(wantModel)`，确保维度 key 与 tile 显示名一致。
- `go test ./admin/...` 全部通过；`go vet ./admin/... ./cmd/gateway/...` 无告警；
  `go build ./...` 成功。

## 影响范围

仅 `admin/` 实时请求流和 `cmd/gateway/adminLiveRequestFromEntry` 的 fallback
路径，不影响请求路由、计费、写库（`request_logs` schema 不变）。
