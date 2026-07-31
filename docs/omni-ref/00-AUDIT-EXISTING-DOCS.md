# OmniRoute 集成方案审计：参考源码、重建草案与 Go 真实代码

> **目标项目**: llm-gateway-go（`github.com/kaixuan/llm-gateway-go`, Go 1.25）
> **源参考**: OmniRoute 参考 checkout（package metadata `3.8.49`；已核对 snapshot `c8f1d62de5d223aa209c7b0f7e09bb35382a4d2e`）
> **Go 审计基线**: `052dbd02034d1e04a92c4e216b87e5b66e12dcee`
> **审计状态**: `RECONSTRUCTED-DRAFT`，不是原始 `omniroute-ref` 正文的恢复
> **证据标签**: `SOURCE-VERIFIED` / `AUDIT-INFERRED` / `NEW-DESIGN` / `MISSING-EVIDENCE`

---

## 1. 审计结论

当前 Go checkout 中原始 `docs/omniroute-ref` 方案正文无法从可用 refs、reflog 或 dangling objects 恢复。现有 `docs/omniroute-ref` 文件是本轮依据参考源码与 Go HEAD 重建的实施草案；它们不能被引用为历史设计的逐字内容。

Go 侧的 Candidate、Router、Executor、Compressor、metatools 和 `registry.ToolRegistry` 路径已经通过源码核对。跨项目的策略数量、MCP registry 数量和 stageTrace 结论只在有参考 checkout 路径时成立，不能反推 Go 已实现这些能力。

### 已确认的关键修正

| # | 主题 | 事实与影响 |
|---|---|---|
| 1 | provider catalog | Go 没有对应 OmniRoute provider registry 常量；provider 完全由 `providers` 数据和 `provider.Client` 驱动。旧的“290 条”总数没有可靠证据。翻译应是 catalog 导出 + 幂等 SQL seed。 |
| 2 | compression entry points | 当前 Go 有 `transformation.CompressMessagesIfNeeded`、`transformation.CompressAnthropicMessagesIfNeeded` 和 `compression.Compressor.Compress`/`CompressAfter4xx`；没有 `Compressor.Apply`。统一 Pipeline/Apply 是未来设计。 |
| 3 | MCP policy | `ToolRegistry.IsAllowed` 定义在 `registry/tool_registry.go`，admin 只是调用方。MCP server 尚未接线。 |
| 4 | routing strategies | OmniRoute 的参考源码当前为 19 个公开策略加内部 `quota-share`；这不是 Go 已有的策略清单。OmniRoute 的 5% exploration/rotator 也不是 Thompson Sampling。 |
| 5 | MCP cardinality | `MCP_TOOLS` registry 当前可核对为 42（34+6+1+1），但 server 会 union 其他工具集合；42 不能写成 server 的总可见工具数，scope 也必须动态取 metadata。 |
| 6 | stage trace | OmniRoute 的路径是 `open-sse/handlers/chatCore/stageTrace.ts`，由 `OMNIROUTE_TRACE=true` 或 `DEBUG=true` 控制；Go 没有同名实现。 |

---

## 2. 验证方法清单

每条事实都应记录来源 checkout、文件路径和验证命令。Go 路径可在当前工作树复跑；参考源码结论只对上方 snapshot 负责。原始设计正文缺失时，文档不得把推断写成已实现事实。

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
rg -n "type Candidate struct" provider
rg -n "func \(r \*Router\) PlanCandidatesWithContext" domains/streaming/executors/router.go
rg -n "func \(c \*Compressor\) (Compress|CompressAfter4xx)" domains/hooks/compression/compressor.go
rg -n "ToolRegistry|func \(tr \*ToolRegistry\) IsAllowed" registry admin
rg -n "mcp|a2a|fusion" --glob '*.go' .
```

---

## 3. Go 当前状态摘要

- `SOURCE-VERIFIED`: `provider.Candidate` 已有价格、缓存、上下文、延迟、成功率、配额和计费字段。
- `SOURCE-VERIFIED`: router 已有 tier、billing round、sticky、P2C、round-robin、pressure-aware、protocol affinity 和 URSM v2 集成。
- `SOURCE-VERIFIED`: Bandit scorer 代码存在，但当前 main 装配仍是 WIP/注释状态，不能写成默认启用。
- `SOURCE-VERIFIED`: compression 包的生产入口是 `Compress` 和 `CompressAfter4xx`；已有 context-window trim 与 recovery/memory 路径各自有 owner。
- `SOURCE-VERIFIED`: Go 当前没有 MCP、A2A 或 Fusion package/server。

详细字段、协议字面量、迁移基线和 `registry.ToolRegistry` 的签名仍保留在本文件后半部分；它们是 Go 源码核对结果，不是原始 OmniRoute 方案内容。

---
grep -n "type Candidate struct" provider/client.go          # → provider/client.go:88

# 路由主函数 + 评分
grep -n "func (r \*Router) PlanCandidatesWithContext" domains/streaming/executors/router.go
grep -n "ConcurrencyWeight\|LatencyWeight\|QualityWeight\|IdentityWeight" domains/streaming/executors/router_scoring.go

# 协议字面量（决定 executor 分支）
grep -n 'openai-completions\|anthropic-messages' domains/streaming/executors/executor_chat.go

# 压缩装配点
grep -n "compression.NewCompressor\|routingExec.Compressor" cmd/gateway/main.go

# 是否已有 mcp/a2a/fusion/provider-catalog
ls -d mcp a2a fusion provider/catalog provider/auth 2>/dev/null   # 全部不存在 → 待新建
```

---

## 3. 逐特性核对结果

下表为每个特性方案 → 真实代码的一致性核对。✅ = 一致并已验证；⚠️ = 措辞需修正；🆕 = 确认不存在、需新建（与方案一致）。

### P1 提供商扩展（phase1/01-provider-expansion.md）

| 文档断言 | 验证结果 | 真实位置 |
|---|---|---|
| `provider.Candidate` 在 `provider/client.go:88` | ✅ | `provider/client.go:88`，含全部 39 字段（见 §5） |
| Candidate 已有价格/缓存/上下文/计费字段 | ✅ | `PriceInPer1M *float64`、`SupportsPromptCache bool`、`ContextWindow *int`、`BillingMode string` |
| `Candidate.CalcCost` 已实现 | ✅ | `provider/client.go:154` |
| Router 按 `ProviderID/CredentialID/RawModel/Protocol` 关联状态 | ✅ | `router.go` 全程使用这些字段做去重/过滤 |
| executor 按 `cand.Protocol` 区分 Anthropic/OpenAI | ✅ | `executor_chat.go:375` `if cand.Protocol == "anthropic-messages"` |
| 启动装配在 `cmd/gateway/main.go`，不硬编码业务逻辑 | ✅ | `main.go:757` `providerClient := provider.NewClient()` |
| **暗示存在 `providerRegistry.ts` 对应的 Go 常量** | ⚠️ | **不存在 Go provider registry**。provider 完全 DB 驱动，`providers` 表是单一事实源（`sql/objects/tables/providers.sql`）。→ 见修正 #1 |
| 协议枚举 `openai-completions` / `anthropic-messages` | ✅ | 运行时字面量完全一致（见 §6） |
| `provider.Candidate` 有 `CatalogCode` 字段 | ✅ | `CatalogCode string`（`json:"catalog_code"`）— 天然支持 catalog 模型 |

### R1 高级路由策略（phase1/02-advanced-routing.md）

| 文档断言 | 验证结果 | 真实位置 |
|---|---|---|
| `PlanCandidatesWithContext` 在 `router.go` ~93 行 | ✅ | `router.go:93`，签名与文档完全一致（8 参数） |
| `LoadScoreWeights` 默认 0.4/0.1/0.3/0.2 | ✅ | `router_scoring.go:24-28` `DefaultLoadScoreWeights()` |
| `calculateLoadScore` 统一为"越低越好"惩罚分 | ✅ | `router_scoring.go:43` |
| `p2cOrder` 抽两项比 `loadScore` | ✅ | `router.go:615`（注意：在 router.go 而非 router_scoring.go） |
| `tierOrder = [1,2,3,9]` | ✅ | `router.go:23` |
| `splitByBillingRound` 先偏好套餐后按量 | ✅ | `router.go:450`，用 `provider.IsPreferredPlanBilling`（`provider/billing.go:18`） |
| Candidate 已有 `P50LatencyMs/P95LatencyMs/RecentSuccessRate/RecentSamples` | ✅ | 全部存在于 `provider/client.go:100-151` |
| `admin/auto_route.go` 管理 routing defaults/overrides | ✅ | `admin/auto_route.go:39` `AutoRouteHandlers`；defaults 在 `auto_route_defaults.go`（CRUD `task_default_routing` 表） |
| **Bandit 当前可用** | ⚠️ | Bandit scorer 在 `router.go` 支持但 **`main.go:832-836` 已注释掉**，当前未启用。策略测试需注意 Bandit 分支默认不激活 |
| 已有 headroom 计算 | ✅ | `router_scoring.go:50` `LLM_GATEWAY_ROUTING_W_HEADROOM`（默认 0.05） |

### C1 Lite 压缩（phase1/03-lite-compression.md）

| 文档断言 | 验证结果 | 真实位置 |
|---|---|---|
| `cmd/gateway/main.go` ~1208 注入 `compression.NewCompressor()` | ✅ | `main.go:1213` `routingExec.Compressor = compression.NewCompressor()` |
| `Executor` 有 `Compressor *compression.Compressor` 字段 | ✅ | `executor.go:641` |
| executor 有 `prepareRequestBody` / `finalizeOpenAIUpstreamBody` | ✅ | `executor_chat.go:1494` / `executor_chat.go:1253` |
| Anthropic 路径已有 `transformation.CompressMessagesIfNeeded` | ⚠️ | **两处同名，分属不同包**（见修正 #2）：(a) `domains/transformation/ctx_compress.go:98` 的客户端侧 context-window trim；(b) `compression` 包内的 `CompressMessagesIfNeededBody`（`compressor.go:401`）。方案接入前须精确到包 |
| `ExecParams`/`ExecuteResult` 已记录 compression 字段 | ✅ | `executor.go:1377` `ExecuteResult.CompressionReason/Strategy/Meta` |
| `domains/hooks/compression` 已含 compaction | ✅ | `compaction.go`、`rebuilder_openai.go`、`rebuilder_anthropic.go`、`recovery_coordinator.go` 等 |
| 无 `lite.go` | 🆕 | 确认不存在，与方案"新建"一致 |

### C2/C3/C4 深度压缩（phase2/04-deep-compression.md）

| 文档断言 | 验证结果 | 真实位置 |
|---|---|---|
| `Executor` 持有 `RecoveryCoord *compression.RecoveryCoordinator` | ✅ | `executor.go:781`；`main.go:1585` 装配 |
| `Executor` 持有 `Memora memory.Reader` / `MemoraSink memory.Writer` | ✅ | `executor.go:661/666`；`main.go:1230-1237` 条件装配 |
| compression 包有 `session_cache.go`、`session_compressor.go` | ✅ | 已存在（L1+L2+L3 cache），`main.go:1553-1560` 装配 |
| 多层（mechanical→memora→LLM 摘要）orchestration | ⚠️（澄清） | 这套分层**未被收敛进 compressor 包**，而是分散在 `compressor.go`（机械 trim）+ `executors/context_summarize.go`（memora/LLM 摘要）+ `compaction.go`。新建 Stage/Pipeline 时须明确与这三处的去重关系 |
| 无 `rtk.go`/`caveman.go`/`stage.go` | 🆕 | 确认不存在，与方案一致 |

### M1 MCP Server（phase2/05-mcp-server.md）

| 文档断言 | 验证结果 | 真实位置 |
|---|---|---|
| `admin/metatools_api.go` 提供 3 个 endpoint | ✅ | `/api/meta-tools/{categories,load,definitions}`（`metatools_api.go:21/38/63`） |
| `metatools.Handler` 有 `ListCategories`/`LoadTools`/`MetaToolDefinitions` | ✅ | `metatools/handler.go:52/85/129` |
| 迁移 `021_tool_registry_and_metatools.sql` 定义 tool_categories/tool_registry | ✅ | `021` 定义两张表 + 7 类别 + 3 示例工具 |
| `domains/hooks/tools` 有 `ToolInterceptionHook`/`ToolCall`/`ToolResult`/`Interceptor` | ✅ | `domains/hooks/tools/{types,hook}.go`；hook 实现 `pipeline.Hook`，`Priority()=100` |
| **`ToolRegistry.IsAllowed` 在 admin 包** | ⚠️ | 实际在 **`registry` 包**（`registry/tool_registry.go:334`，签名 `(tenantID, toolID string) (bool, string)`）；`admin/tool_policy_api.go` 只是调用方。→ 见修正 #3 |
| 无 `mcp` 包 / 无 JSON-RPC 处理 | 🆕 | 确认 main 源码树零命中，与方案一致 |

### A1 A2A Protocol（phase3/06-a2a-protocol.md）

| 文档断言 | 验证结果 | 真实位置 |
|---|---|---|
| 当前仓库无现成 A2A endpoint | ✅ | grep `message/send`/`jsonrpc` 在 main 源码树零命中 |
| 参考 OmniRoute 的 `src/app/a2a/route.ts` / `src/lib/a2a/taskManager.ts` | ✅ | 参考文件均存在（见融合指南） |
| 应新增 `a2a/{protocol,task,skills,http}` 包，不让 executor 依赖 a2a | ✅ | 与代码现状一致（a2a 不存在，可独立新增） |

### R3 Fusion 路由（phase3/07-fusion-routing.md）

| 文档断言 | 验证结果 | 真实位置 |
|---|---|---|
| Router 返回排序后 `[]provider.Candidate`，Executor 按序 retry/failover | ✅ | `router.go` + `executor.go` 行为一致 |
| 应抽取 `SingleCandidateExecutor` 窄接口，不复制 HTTP/协议转换 | ✅ | 当前 Executor 单候选逻辑内聚，可抽取 |
| 无 `fusion/` 包 | 🆕 | 确认不存在，与方案一致 |

---

## 4. 修正详情

### 修正 #1：provider 不是"移植常量"，而是"生成 seed 数据"

**问题**：旧草案的 §3.1 / §4 Step 1 措辞让人以为存在一个类似 OmniRoute provider constants 的 Go 常量文件，还把没有可靠来源的 provider 总数写成了 290。

**真实情况**：llm-gateway-go **没有** Go 层 provider 常量。Provider 是**完全 DB 驱动**的：
- `providers` 表（`sql/objects/tables/providers.sql`）是唯一事实源，字段含 `code`、`catalog_code`、`protocol`、`base_url`、`category`、`kind`、`enabled` 等。
- 运行时 `provider.NewClient()`（`main.go:757`）+ `SetDB(...)`（`main.go:762`）从 DB 解析 Candidate。
- 唯一的"常量"是 `catalog/display.go` 的厂商显示名映射（如 `"openai-gpt": "OpenAI"`），与协议/路由无关。

**修正后的翻译方式**（详见融合指南 `01-TS-TO-GO-FUSION-GUIDE.md` §3）：
1. 从 OmniRoute 的 TypeScript provider 定义**导出中间 JSON**（catalog_code + protocol + base_url + models）。
2. 用新建的 `provider/catalog` 包把 JSON **幂等生成 SQL seed**（`INSERT ... ON CONFLICT`）。
3. 不要在 Go 里建大段常量；让 DB 做 catalog 的单一事实源。
4. `domains/provider/types.go` 的 `Protocol` 枚举（`openai`/`anthropic`/`azure`/`custom`）是**领域层**语义，与 Candidate 的运行时 `Protocol` 字符串（`openai-completions` 等）不同，不要混用。

### 修正 #2：`CompressMessagesIfNeeded` 同名两物，接入须精确到包

**问题**：phase1/03 与 phase2/04 多次提到 `CompressMessagesIfNeeded`，未区分包。

**真实情况**：存在两个不同包的同名/近名函数：
- `transformation.CompressMessagesIfNeeded`（`domains/transformation/ctx_compress.go:98`）—— **客户端侧** context-window 机械裁剪，在 `prepareRequestBody`（`executor_chat.go:1528`）中按 `cand.ContextWindow` 调用。
- `compression.CompressMessagesIfNeededBody`（`domains/hooks/compression/compressor.go:401`）—— compression 包内的 body 级 trim。
- 此外 `Compressor.Compress(...)`（`compressor.go:269`）和 `Compressor.CompressAfter4xx(...)`（`:353`）是当前 v7 入口；不存在 `Compressor.Apply`。

**修正后接入原则**：Lite/RTK/Caveman 必须插在**协议转换完成后、上游发送前**，且与上述两个已有 trim 做 stage 去重。当前实现应调用 `Compress`/`CompressAfter4xx`；统一 `Apply`/Pipeline 只是 `NEW-DESIGN`，不要在 `executor_chat.go` 里再加第三处 body 改写。

### 修正 #3：`ToolRegistry.IsAllowed` 在 `registry` 包

**问题**：phase2/05 §8 把 `ToolRegistry.IsAllowed(tenantID, toolName)` 归到 admin 包。

**真实情况**：
- 定义在 **`registry/tool_registry.go:334`**，签名 `func (tr *ToolRegistry) IsAllowed(tenantID, toolID string) (bool, string)`（返回 `(allowed, reason)`，无 error）。
- `registry.ToolRegistry`（`registry/tool_registry.go:22`）有 60s 后台刷新缓存（`Reload` at `:181`）。
- `admin/tool_policy_api.go` 的 `PolicyAPI`（`:16`）只是 HTTP 调用方，内部委托 `ToolRegistry`。

**修正后实现**：MCP Server 的 `PolicyChecker` 应直接依赖 `registry.ToolRegistry`（不是 admin 包）：

```go
import "github.com/kaixuan/llm-gateway-go/registry"

type mcpPolicyChecker struct{ reg *registry.ToolRegistry }

func (c *mcpPolicyChecker) IsAllowed(ctx context.Context, tenantID, toolName string) (bool, string, error) {
    allowed, reason := c.reg.IsAllowed(tenantID, toolName) // 注意：无 error
    return allowed, reason, nil
}
```

---

## 5. `provider.Candidate` 全字段清单（已验证，截至 2026-07-29）

来源 `provider/client.go:88-152`。新 provider/routing/compression 特性应优先复用这些字段，而非新增列：

```
CredentialID, ProviderID, BaseURL, Protocol, CatalogCode, Tier, Weight,
RawModel, OfferRawModel, StandardizedName,
SuccessRate, P95LatencyMs, P50LatencyMs,
ConcurrencyLimit *int, FpSlotLimit *int, RPMLimit *int,
BalanceUSD *float64, CircuitState, AvailabilityState, QuotaState, LifecycleStatus,
Routable bool, BlockReason *string,
PriceInPer1M *float64, PriceOutPer1M *float64,
CacheReadPricePer1M *float64, CacheWritePricePer1M *float64,
SupportsPromptCache bool, CacheMode string,
ManualPriority, ActiveSessions, ConsecutiveFailures, CompositeScore,
Currency, BillingMode, ContextWindow *int, APIKey(json:"-"),
QualityFixMode, RecentSuccessRate *float64, RecentSamples int
```

> 关键：价格、缓存、上下文窗口、延迟、成功率、余量、熔断——R1/C1 所需信号**全部已存在**。`*float64`/`*int` 表示"未知"用 nil（不要把 nil 当 0）。

---

## 6. 协议字面量核对（影响 executor 分支）

executor 用**运行时字符串字面量**（非枚举常量）区分协议，这些字面量已验证与方案完全一致：

| 字面量 | 用途 | 示例调用点 |
|---|---|---|
| `openai-completions` | OpenAI-shaped 上游 | `executor_chat.go:209,497,632`（诊断默认值） |
| `anthropic-messages` | Anthropic Messages 上游 | `executor_chat.go:375,753,1132,1528` |
| `openai-responses` | Responses API 客户端协议 | `executor_chat.go:873-874` |

> 注意：`domains/provider/types.go` 另有 `ProtocolOpenAI="openai"` 等枚举，属于**领域层**定义，与上述运行时字面量不同体系。catalog 导入时协议值必须用运行时字面量（`openai-completions`/`anthropic-messages`），否则 executor 分支失配。

---

## 7. 当前路由策略盘点（核对 R1 "新增 4 种"）

已存在（`router.go`）：

1. **P2C**（power-of-two-choices）— `router.go:615`，tier 内默认
2. **Bandit scorer** — 代码支持但 `main.go:832-836` **当前注释禁用**；不要据此写成默认启用的 Thompson Sampling 路由
3. **Tier 分桶** — `tierOrder {1,2,3,9}`，`planByTier` `router.go:461`
4. **Billing round** — `splitByBillingRound` `router.go:450`
5. **Sticky**（L1/L2/L3）— `prioritizeSticky` + `sticky.go`
6. **Round-robin** — `rotateCandidates` `router.go:517`
7. **Pressure-aware** — `PressureAwareEnabled` + `applyPressurePenalty` `router.go:183`
8. **Protocol affinity** — `applyProtocolAffinity`
9. **URSM v2** — canary/authoritative 重排

R1 新增 `cost-optimized` / `cache-optimized` / `context-aware` / `headroom` 应作为 `orderBucket` 内的可插拔 `Strategy`，**不替换**上述语义。headroom 策略可直接复用 `router_scoring.go:50` 的 `LLM_GATEWAY_ROUTING_W_HEADROOM`。

---

## 8. 迁移编号基线

- 当前最大迁移号：**462**（`462_model_integrity_events.sql`）
- 下一可用迁移号：**463**
- 方案中所有 `<next>_xxx.sql` 占位符应从 **463** 起编，并先执行列/索引存在性检查避免与已发布迁移冲突。
- catalog/tool/mcp/a2a 各自的迁移应分开编号（如 463 catalog、464 mcp sessions、465 a2a tasks），便于独立回滚。

---

## 9. 与融合指南的关系

本审计**只做事实核对与修正**，不改架构方向。翻译方法论、参考代码清单、融合技能、分步落地步骤见：

- **`01-TS-TO-GO-FUSION-GUIDE.md`** —— TypeScript→Go 翻译方法论 + 每特性的可参考代码清单 + 融合技能
- `docs/omniroute-ref/{phase1,phase2,phase3}/*.md` —— 本轮带证据等级的重建草案，不是恢复的历史原文

**推荐使用顺序**：先读本审计 → 再读融合指南 §1 通用方法论 → 按 phase1→2→3 顺序，每读一份方案时对照本审计 §3 的对应行。
