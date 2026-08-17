# OmniRoute v2 融合基线：llm-gateway-go

> 目标：把 OmniRoute v3.8.49 中值得复用的声明式规则、协议契约和观测语义融合到现有 Go 网关，同时保持现有路由、凭据健康、租户隔离和流式执行边界。
>
> 参考 checkout：`/Users/xutaohuang/workspace/ai/OmniRoute`
>
> 适用范围：`llm-gateway-go` 数据面。控制面插件 `ai-session-manager` 的适配见其 `docs/omni-ref2/`。

## 1. 先读这份文档

本目录不是 OmniRoute 历史设计正文的恢复，也不是“把 TypeScript 目录翻译成 Go 目录”的任务清单。它是当前 checkout 的融合基线：

1. 先确认现有 Go 事实和不可破坏的不变量。
2. 再按 P1/R1/C1 → C2-C4/MCP → A2A/Fusion 的顺序交付。
3. 每个阶段都有独立的 fixture、feature flag、指标和回滚方式。
4. 参考源码只提供行为和数据来源；HTTP、数据库、认证、并发和生命周期必须按 Go 现有架构重写。

### 证据标签

| 标签 | 含义 |
|---|---|
| `SOURCE-VERIFIED` | 已从当前 Go 或 OmniRoute snapshot 的具体源码核对 |
| `TARGET-BOUNDARY` | 由现有架构决定的责任边界，不是 OmniRoute 功能承诺 |
| `NEW-DESIGN` | 目标项目当前不存在，需要评审后新增 |
| `BLOCKED` | 依赖跨仓契约、数据源或安全审查，未满足前不得上线 |

## 2. 当前 Go 基线

以下是融合必须保留的事实：

- `cmd/gateway/main.go` 是主要装配点；新能力通过构造注入和 feature flag 接入。
- `provider.Candidate` 已携带价格、缓存、上下文窗口、延迟、成功率、配额、熔断和计费字段；provider catalog 不应复制成大段 Go 常量。
- Router 已有 tier、billing round、sticky、P2C、round-robin、pressure-aware、protocol affinity 和 URSM v2。新策略只扩展 bucket 内排序，不替换健康过滤和状态后端。
- Executor 已区分 `openai-completions`、`anthropic-messages`、`openai-responses`，并有 `Compress`/`CompressAfter4xx`、context-window trim、RecoveryCoordinator 和 Memora 路径。
- `registry.ToolRegistry.IsAllowed(tenantID, toolID)` 是工具授权的现有入口；`admin` 只是 HTTP 调用方。
- 当前没有可上线的 MCP server、A2A endpoint 或 Fusion package。
- 当前最大数据库迁移号以 checkout 实际文件为准；新增迁移必须先重新检查，不能直接沿用旧文档中的占位号。

## 3. 责任边界

| 能力 | llm-gateway-go | ai-session-manager | 备注 |
|---|---|---|---|
| provider/auth/credential 调用 | 负责 | 不实现 | 数据面唯一 owner |
| 路由选择、重试、熔断 | 负责 | 不实现 | ASM 只读消费决策 metadata |
| prompt/context 压缩 | 负责 | 不实现 | ASM 只读消费 savings/stage |
| MCP transport/tool execution | 负责 | 不实现 | ASM 不能成为第二个工具运行时 |
| A2A JSON-RPC/streaming | 负责 | 不实现 | ASM 可提供控制面 capability 发现 |
| session/task 投影 | 发布事实 | 只读投影/操作 | 通过 versioned event contract |
| cost/health/routing explanation | 产生 canonical metadata | 聚合与展示 | 不在 ASM 重算数据面结论 |

硬约束：新增包不得让 `executor` 依赖 `mcp`、`a2a` 或 `fusion`；可由这些上层能力依赖已有 executor/router 接口。所有跨租户读写使用现有认证上下文和 RLS 约定，禁止从请求 arguments 覆盖 tenant。

## 4. 融合顺序

```text
HTTP adapter
  -> tenant/profile/auth
  -> existing availability + URSM/health/fp-slot/limiter filters
  -> billing round + tier bucket
  -> existing P2C / new strategy score
  -> sticky / protocol affinity / canary
  -> protocol conversion
  -> existing context-window trim
  -> compression pipeline stages (feature-flagged)
  -> single-candidate executor + retry/failover
  -> audit / telemetry / canonical events
```

新逻辑不得在可用性过滤前把 unavailable candidate 排到前面；压缩不得在 executor 中再增加第三处 body 改写；重试不得重复执行有副作用的压缩 stage。

## 5. 阶段计划与门禁

### Phase 0：契约与观测基线（先做）

**交付**：

- 统一 `RoutingDecision`、`CompressionEvent`、`ProviderRef` 的内部/事件字段白名单。
- 为路由和压缩建立低基数 metrics；request ID、prompt、token、credential secret 不得成为 label 或日志内容。
- 把 OmniRoute 的纯函数 fixture 导入 Go 测试，记录输入、期望输出和来源 commit。
- 明确 event producer → Outbox → ASM ingress 的跨仓契约；ASM 侧 contract 见 `../../../ai-session-manager/docs/omni-ref2/02-EVENT-INGRESS-CONTRACT.md`。

**门禁**：`go test ./...`、`go vet ./...`、race test 覆盖新增并发组件；没有 fixture 和回滚开关的特性不得进入数据面。

### Phase 1：Provider catalog、路由策略、Lite compression

#### P1 Provider catalog

来源：`src/shared/constants/providers/*`、`open-sse/config/providerRegistry.ts`、`src/shared/validation/providerSchema.ts`。

做法：

1. 从参考源码导出中间 JSON，字段至少包括 `catalog_code`、display name、protocol、base URL、auth kind、model aliases 和能力 flags。
2. 在 `provider/catalog` 建校验器和幂等 SQL generator；DB 的 `providers`/model offer 数据是运行时事实源。
3. 认证 secret 只从环境或 secret store 解析，禁止把 OAuth secret、API key 或参考仓库中的本地凭据写入 seed。
4. 对每个 protocol 建 provider/model contract test，确保 runtime literal 与 executor 分支一致。

**门禁**：重复 catalog key、非法 HTTPS URL、未知 protocol、缺失 pricing/context metadata 都必须在导入前失败；迁移可独立回滚。

#### R1 Routing strategies

只翻译评分函数，不复制 OmniRoute 的路由编排。候选顺序保持：去重 → state/health/fp-slot/limiter → billing round → tier → strategy → sticky/protocol affinity → canary。

建议新增窄接口：

```go
type Strategy interface {
    Name() string
    Score(ctx context.Context, candidate provider.Candidate, in StrategyInput) (float64, error)
}
```

`cost-optimized`、`cache-optimized`、`context-aware`、`headroom` 的 score 必须规定 unknown 值语义：未知价格不是免费，未知 context 不是无限，未知 headroom 不能绕过健康过滤。默认模式保持现有 P2C。

#### C1 Lite compression

来源：`open-sse/services/compression/lite.ts`、`types.ts`、`preservation.ts`。

翻译为 `domains/hooks/compression` 内的纯函数 stage：空白/换行归一化、尾随空白清理、保护块抽取/还原以及 system prompt 保留策略。Go `regexp` 使用 RE2，参考代码中的 lookbehind 必须改写并补 golden test。

接入现有 `Compressor.Compress`/`CompressAfter4xx`；统一 `Pipeline.Apply` 可以作为后续 API，但在此之前不要改造 `executor_chat.go` 增加第三处 body transform。

**门禁**：fail-open 返回原 body；同一 request scope 内 retry 不重复压缩；code block、URL、路径、结构化 content block 的 before/after fixture 不变；compression event 能解释 stage、原因和节省量。

### Phase 2：RTK、Caveman、Stacked 与 MCP

#### C2/C3/C4 compression

- RTK：翻译 command detector、deduplicator、line filter、smart truncate 和 JSON rule schema。
- Caveman：翻译 protected-block 流程和规则字面量；所有规则预编译，RE2 不支持的语法要拆写。
- Stacked：翻译 `strategySelector` 的优先级链：assigned combo → override → auto trigger → default → off。
- 与现有 context trim、RecoveryCoordinator、Memora compaction 去重；同一请求只能有一个 canonical compression decision。

#### M1 MCP server

这是 `NEW-DESIGN`，不能把 `@modelcontextprotocol/sdk` 当 Go 依赖。Go 侧需要独立 JSON-RPC dispatcher、HTTP/SSE transport、tool definition registry、scope evaluator 和 audit writer：

- 授权直接依赖 `registry.ToolRegistry.IsAllowed`，返回 reason 并记录 audit。
- 工具定义可参考 `MCP_TOOLS`，但工具总数必须由 runtime union/dedup 后计算，不能把 registry cardinality 当 server 可见总数。
- tenant 来自认证 context；arguments 中的 `tenant_id` 一律忽略或拒绝。
- audit 只记录低敏 metadata，不保存 prompt、response、credential 或完整工具参数。

**门禁**：JSON-RPC contract tests、invalid method/params/tenant/scope 测试、replay/nonce 测试、`go test -race ./mcp/...`，默认 flag 关闭。

### Phase 3：A2A 与 Fusion

#### A1 A2A

参考 `src/lib/a2a/taskManager.ts`、`taskExecution.ts`、`streaming.ts`、`skills/*` 和 route handler。Go 侧拆成 `a2a/task`、`a2a/skills`、`a2a/http`，持久化用 Store 接口和 PostgreSQL 实现；内存实现只用于测试。

状态迁移必须在事务中 `SELECT ... FOR UPDATE` 校验旧状态，SSE 事件由 Go 原生实现，认证复用 gateway middleware。技能执行调用现有 Router/Executor，不复制一套 provider 选择器。

#### R3 Fusion

OmniRoute 没有完整 Fusion 实现，Fusion 是 Go 侧新设计。先抽取 `SingleCandidateExecutor` 窄接口，再实现 staged panel/parallel/judge：

- 第一版 stream 强制 staged，避免多路并发改变响应语义。
- 每个 candidate 独立 limiter reservation；context cancel 必须释放未完成 goroutine。
- judge 只消费结构化结果和 cost envelope，不接收 secret 或完整 prompt 日志。
- 输出必须带可审计的 routing explanation，但不泄露内部 credential ID。

**门禁**：超时、取消、部分失败、重复结果、预算耗尽和 race test；没有明确 cost/latency budget 的 Fusion 模式保持关闭。

## 6. 测试与发布规则

每项能力至少有四层测试：

1. 纯函数 table-driven test，输入来自 OmniRoute fixture。
2. `httptest.Server` 上游协议/重试集成测试。
3. 跨仓 contract test，验证 canonical event 和签名 envelope。
4. 生产前 shadow/flag 测试，比较新旧决策但不改变流量。

常用命令：

```bash
go test ./...
go vet ./...
go test -race ./domains/hooks/compression/...
go test -race ./mcp/... ./a2a/... ./fusion/...
```

发布要求：feature flag 默认关闭；每阶段单独 migration、dashboard、告警和 rollback；任何涉及 provider/auth/tool 的变化必须经过 secret scan 和租户隔离负向测试。

## 7. 明确不做

- 不恢复不可核实的 OmniRoute 历史文档，不把参考源码的数量写成当前 Go 已实现数量。
- 不把 provider catalog 写成 Go 常量，也不把 credential secret 放入 catalog。
- 不在 ASM 复制路由、压缩、MCP、A2A 或 streaming 数据面。
- 不在 executor/router 中引入 Node/SQLite/IPC 运行时结构。
- 不把 unknown price/context/headroom 当作零成本、无限能力或健康候选。

## 8. 参考导航

- 事实核对：`docs/omni-ref/00-AUDIT-EXISTING-DOCS.md`
- TS → Go 方法论：`docs/omni-ref/01-TS-TO-GO-FUSION-GUIDE.md`
- 重建阶段草案：`docs/omniroute-ref/00-IMPLEMENTATION-ROADMAP.md` 与 `phase1/phase2/phase3/`
- 当前系统架构：`docs/architecture/ARCHITECTURE.md`
- ASM 事件契约：`../../../ai-session-manager/docs/omni-ref2/02-EVENT-INGRESS-CONTRACT.md`
