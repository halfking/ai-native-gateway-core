# 审计补报：rtk-borrowing-optimization 代码正确性与接线完整性审计

> 日期：2026-07-06
> 审计基准：main（已合并 feat/rtk-borrowing-optimization）
> 触发：handoff 文档要求「对任务完成情况进行审计，修正发现的问题」
> 状态：审计完成，2 个问题已修复

## 审计范围

针对 feat/rtk-borrowing-optimization 合并到 main 后的 5 个改动进行全面代码审计：

| # | 模块 | 审计重点 | 覆盖 |
|---|---|---|---|
| 1 | `compression/guard.go` | NeverWorse 守卫 | 完整性、接线、文档一致性 |
| 2 | `session_cache.go` | L1 真 LRU | 数据结构、并发安全、边界条件 |
| 3 | `session_compressor.go` | Lossiness 分类 | classifyLossiness 穷举、RecordLossiness 触发 |
| 4 | `handler.go` | Stabilize/Inject 热路径 | fail-open、守卫、数据流顺序 |
| 5 | `main.go` | CacheInjector 装配 | env 读取、错误路径 |

## 审计方法

| 审计项 | 工具/方法 | 范围 |
|---|---|---|
| 文件完整性 | 手动验证 | 12 个文件与 handoff 文档 1:1 核对 |
| 代码质量 | golangci-lint (50+ linters) | compression/ + streaming/ + cmd/ |
| 静态分析 | go vet | compression/ + streaming/ |
| 竞态检测 | go test -race | compression/ (25 个 rtk 特定测试 + 全包) |
| 覆盖率 | go test -cover | compression/ (62.8%) |
| 构建完整性 | go build ./... | 全项目 |
| 数据流正确性 | 手动代码审查 | handler.go 三阶段接线顺序 |
| 守卫完整性 | grep 搜索 | NeverWorse 在生产代码中的调用位置 |
| 指标注册 | 源码审查 | guardRegressed + lossinessCounter init 路径 |

## 审计发现

### 发现 1（高）：`GuardStageCompress` 守卫在生产代码中未接线

**位置**：`domains/streaming/handler.go:1497-1498`（改前代码）

**问题**：session compressor 的输出直接赋值给 `bodyBytes`，未经 `NeverWorse` 守卫检查：

```go
// 改前代码：
if scResult != nil && len(scResult.OutboundBody) > 0 {
    bodyBytes = scResult.OutboundBody  // ← 无 NeverWorse 守卫
}
```

**影响**：虽然 compressor 内部有内联长度检查（`len(summarised) < len(outboundBody)`、`len(trimmed) < len(outboundBody)`），但 handoff 文档宣称「never_worse 守卫自动应用于 compress/inject」。缺少外层守卫意味着：
- 如果未来 compressor 内部检查出现逻辑错误，没有兜底安全网
- 指标 `compression_regressed_total{stage="compress"}` 永远不会自增，使 operators 对该指标无从判断
- 生产代码与文档声明不一致

**修复**：在 compressor 输出后插入 NeverWorse 守卫：

```go
if guarded, regressed := compression.NeverWorse(bodyBytes, scResult.OutboundBody, compression.GuardStageCompress); !regressed {
    bodyBytes = guarded
}
```

### 发现 2（低）：guard.go 文档字符串与实现不一致

**位置**：`domains/hooks/compression/guard.go:42`

**问题**：Prometheus 指标 Help 字符串枚举 `(compress|stabilize|inject)`，但：
- `compress` — 此前未接线，修复后已接入（见发现 1）
- `stabilize` — 有意识的不接线（重排操作，不是压缩）
- `inject` — 已接线

**影响**：运维人员查看 Help 文本时会认为三者都有效，但实际上 `stabilize` 永远不会产生指标，造成困惑。

**修复**：更新 Help 文本说明各阶段的接线状态，明确 stabilize 是有意不守卫。

## 已实施的修复

| # | 文件 | 改动 | 影响 |
|---|---|---|---|
| 1 | `domains/streaming/handler.go` | compressor 输出后插入 `NeverWorse(bodyBytes, scResult.OutboundBody, GuardStageCompress)` | `+5 行` |
| 2 | `domains/hooks/compression/guard.go` | 更新 metric Help 文本，说明 stabilize 为何不守卫 | `+1 行` |

## 修复验证

| 检查项 | 结果 |
|---|---|
| `go build ./...` | ✅ 通过 |
| `gofmt` | ✅ clean |
| `go vet ./domains/hooks/compression/... ./domains/streaming/...` | ✅ clean |
| `golangci-lint run ./domains/hooks/compression/...` | ✅ 0 issues |
| `go test ./domains/hooks/compression/... -race` | ✅ 3.024s PASS |
| `go test -cover ./domains/hooks/compression/...` | ✅ 62.8% |

## 未发现的问题

以下审计项均 ✅ 通过，未发现问题：

### 1. LRU 实现（`session_cache.go`）

- `container/list` 双向链表 + `map[string]*l1Entry`，O(1) promote/evict ✅
- 更新已存在的 key 不产生重复节点（`setL1` 中 `existing, ok` 分支）✅
- `Invalidate` 从 list 和 map 中同步删除 ✅
- `sync.Mutex` 保护所有 L1 操作，-race 测试通过 ✅
- `l1MaxSessions = 1024` 有界，churn 测试确认不会无限增长 ✅

### 2. Lossiness 分类（`session_compressor.go:338-358`）

- `""` → `LossinessNone` ✅
- `"delta_append"` → `LossinessNone` ✅
- `"mechanical_trim"` → `LossinessTail` ✅
- `"sliding_window_*"` with summary marker → `LossinessWhole` ✅
- `"sliding_window_*"` without summary marker → `LossinessTail` ✅
- 未识别的 strategy（保守）：有 summary marker → `LossinessWhole`，无 → `LossinessTail` ✅
- 只在 `CompressionStrategy != ""` 时记录指标（避免空值膨胀）✅

### 3. Stabilize/Inject 接线（`handler.go`）

- **执行顺序**：session compression → stabilize → WAL → inject → forward（正确：先压缩/重排，再注入缓存标记）✅
- **Stabilize 无守卫**：注释明确说明是重排不是压缩，有意识不守卫 ✅
- **Inject 有守卫**：在 `NeverWorse(byte, GuardStageInject)` ✅
- **fail-open**：Stabilize 错误时返回原 bytes ✅；Inject 错误时返回 `nil` 跳过 ✅
- **env 控制**：`LLM_GATEWAY_PROMPT_CACHE_STABILIZE`（默认开）、`LLM_GATEWAY_PROMPT_CACHE_INJECT`（默认关）✅
- **响应头**：Stabilize 触发时设 `X-Gw-Prefix-Stabilized` ✅

### 4. 指标注册（`metrics.go` + `guard.go`）

- `lossinessCounter` 使用 `prometheus.Register` 配合 `AlreadyRegisteredError` 容忍 ✅
- `guardRegressed` 同样使用 `prometheus.Register` 容忍 ✅
- 两者都有 `Reset*` 和 `Count*` 测试 helper ✅

### 5. 并发安全

- `ChatHandler` 的 `promptCacheStabilize` / `promptCacheInject` / `cacheInjector` 字段在启动时赋值，热路径只读 ✅
- `guardRegressed.WithLabelValues` Prometheus 自带并发安全 ✅
- `lossinessCounter.WithLabelValues` Prometheus 自带并发安全 ✅

## 结论

**任务完成质量：良好。** 上一次合并后存在 1 个安全网缺口（compress 守卫未接线）和 1 个文档字符串不一致，均已修复。其他 9 个审计维度全部通过。

- ✅ 文件完整性：12/12
- ✅ 代码质量：0 lint issues
- ✅ 测试：25+ rtk 测试全通过，62.8% 覆盖率
- ✅ 并发安全：-race 全通过
- ✅ 架构合规：全部加法，无架构改动
- ～ 2 个问题发现并修复

**推荐**：已修复内容在 main 分支可直接交付。
