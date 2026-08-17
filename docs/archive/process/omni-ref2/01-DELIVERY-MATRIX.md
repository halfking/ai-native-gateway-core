# OmniRoute v2 交付矩阵：llm-gateway-go

这份矩阵把 `README.md` 的路线压缩成可执行的交付单。状态只允许使用：`planned`、`blocked`、`shadow`、`enabled`、`done`。没有证据的能力不得标为 `done`。

## 1. 工作包矩阵

| ID | 工作包 | 目标位置 | 参考来源 | 当前状态 | 前置 | 验收 |
|---|---|---|---|---|---|---|
| GW-00 | canonical metadata | `domains/events` 或既有 audit/event owner | Gateway 现有审计/telemetry | done | 确认事件 owner | 字段白名单、低基数 metrics、无 secret |
| GW-01 | provider catalog export | `provider/catalog` | `src/shared/constants/providers/*` | done | catalog JSON schema | 幂等 seed、重复键失败、protocol contract |
| GW-02 | auth resolver | `provider/auth` | `open-sse/utils/publicCreds.ts` | blocked | secret store 约定 | 无明文 secret、轮换/缺失可诊断 |
| GW-03 | strategy interface | `domains/streaming/executors` | `routingStrategies.ts` | done | GW-00 | 默认 P2C 不变，新策略仅 bucket 内排序 |
| GW-04 | cost/cache/context/headroom | router strategy package | OmniRoute scoring rules | done | GW-03 | unknown 值惩罚、稳定排序、shadow diff |
| GW-05 | Lite stage | `domains/hooks/compression` | `compression/lite.ts` | done | GW-00 | golden fixtures、fail-open、retry 去重 |
| GW-06 | RTK stage | `domains/hooks/compression` | `compression/engines/rtk/*` | planned | GW-05 | schema validation、性能预算、保护块不变 |
| GW-07 | Caveman stage | `domains/hooks/compression` | `caveman.ts`, `cavemanRules.ts` | planned | GW-05 | RE2 兼容、规则可审计、无正文日志 |
| GW-08 | Stacked selector | compression pipeline owner | `strategySelector.ts` | blocked | GW-05..07 + compaction owner | 一个 request 一个 canonical decision |
| GW-09 | MCP JSON-RPC | `mcp` | `mcp-server/server.ts` | blocked | auth/scope/audit contract | protocol/race/security tests，默认关闭 |
| GW-10 | MCP tool registry adapter | `mcp` + `registry` | `schemas/tools.ts`, `scopeEnforcement.ts` | blocked | GW-09 | dynamic union/dedup，ToolRegistry 直接授权 |
| GW-11 | A2A task store | `a2a/task` | `taskManager.ts` | blocked | auth + migration review | PG lock、状态迁移、TTL、幂等 |
| GW-12 | A2A HTTP/SSE | `a2a/http` | `route.ts`, `streaming.ts` | blocked | GW-11 | JSON-RPC/SSE contract、取消/超时 |
| GW-13 | SingleCandidateExecutor | `fusion` 或 executor 内窄接口 | Go 新设计 | blocked | executor 行为 fixture | 现有 retry/telemetry 不回归 |
| GW-14 | Fusion staged mode | `fusion` | Go 新设计 | blocked | GW-13 + budget | staged stream、cancel、race、成本门禁 |
| GW-15 | ASM event producer | Gateway Outbox/EventBus owner | ASM `omni-ref2/02` | blocked | GW-00 + 跨仓 schema | 同事务 outbox、签名投递、重放幂等 |

## 2. 依赖图

```text
GW-00 ──┬── GW-01 ── GW-02
        ├── GW-03 ── GW-04
        ├── GW-05 ── GW-06 ──┐
        │                     ├── GW-08
        └── GW-15             └── GW-07

GW-09 ── GW-10
GW-11 ── GW-12
GW-13 ── GW-14
```

GW-15 不是压缩、MCP 或 A2A 的子任务，但它是 ASM 实时投影的跨仓门禁，必须在 canonical metadata 稳定后实施。

## 3. 每个工作包的完成定义

### Catalog

- [ ] 中间 JSON schema 已评审并包含来源版本。
- [ ] seed generator 使用 `INSERT ... ON CONFLICT`，可重复执行。
- [ ] provider/model/pricing/context 的未知值语义已测试。
- [ ] auth 字段只引用 secret name 或 resolver key。

### Routing

- [ ] 过滤与排序顺序有单元测试锁定。
- [ ] 默认 P2C、URSM、tier、billing、sticky 行为 golden diff 无变化。
- [ ] 新策略 shadow 输出包含 strategy/version/score reason。
- [ ] metrics 只使用低基数标签。

### Compression

- [ ] protected ranges 在每个 stage 前后完整。
- [ ] body transform 出错时返回原 body并记录可脱敏 reason。
- [ ] retry 使用 request-scope event，不重复执行。
- [ ] compression savings 的计算定义与 ASM 事件字段一致。

### MCP/A2A/Fusion

- [ ] tenant/auth/rate limit/timeout/cancel 有负向测试。
- [ ] 所有新表使用 tenant RLS 和独立 migration。
- [ ] 协议错误不回显 payload、token 或内部路径。
- [ ] 默认关闭，发布后先 shadow/canary，再扩大范围。

### Event producer

- [ ] canonical fact 在业务事务内写入 durable outbox。
- [ ] producer 按 ASM 的 body-bound HMAC 签名。
- [ ] 失败重试不会改变 `event_id`，成功后可重放且不重复投影。
- [ ] `request.completed.v1` 只发送 metadata/body_refs，不发送正文。

## 4. 变更审查清单

审查 PR 时逐项回答：

1. 新代码是否复用了现有 Candidate、Router、Executor、ToolRegistry 和 auth，而不是建立平行实现？
2. 新 DB 表是否声明 tenant key、RLS policy、索引和 rollback？
3. 新日志/metrics 是否可能包含 prompt、response、API key、credential ID 或高基数 request ID？
4. 失败路径是否 fail-open/fail-closed 与数据面风险相匹配？
5. 是否有 fixture 或 contract test 证明 TS 规则翻译没有改变保护块、协议字段和错误语义？
6. feature flag 关闭时，现有线上行为是否完全不变？
7. 是否更新了 ASM 需要消费的 event schema、版本和 producer 测试？

## 5. 推荐落地顺序

```text
GW-00 → GW-01 → GW-03 → GW-05 → GW-15
                  ↓        ↓
                GW-04   GW-06/07 → GW-08

随后按独立 release：GW-09/10、GW-11/12、GW-13/14
```

MCP、A2A、Fusion 没有相互依赖，可以分开评审和发布；但三者都必须先完成认证、租户、可观测性和取消语义，不能因为协议 endpoint 已能返回 200 就视为交付。
