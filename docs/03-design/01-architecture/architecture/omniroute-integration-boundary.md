# OmniRoute Integration Boundary — 集成边界

> **事实快照：** 2026-08-21  
> **公开参考：** [Rawbeew/omniroute release/v3.8.49](https://github.com/Rawbeew/omniroute/tree/release/v3.8.49)。  
> **证据规则：** 上游 README 的 provider/tool/节省率数字是项目声明；本项目不将其当作独立验收事实。仓库内 `docs/omniroute-ref/` 是参考路线图，明确声明目标数字不代表当前代码已经实现。

## 1. 总原则

```text
Gateway owns provider call, routing, streaming, tenant, limiter, cost and audit.
ASM consumes Gateway facts and owns projection, analysis, approval and governance UI.
Maintain owns artifact, license, activation, installation and upgrade control plane.
```

不要将 OmniRoute 的 Node/SQLite/JS VM/provider executor 机制直接翻译到 Go。只吸收经过边界评审的协议、评分、压缩、任务状态和验证语义。

## 2. 能力矩阵

| 能力 | Gateway 当前 | 决策 | 目标 owner |
|---|---|---|---|
| Provider catalog | typed catalog、校验、seed、discovery 已有；290+ 数字未证实 | `ADOPT` | Gateway |
| cost/cache/context/headroom | scorer 已有，主要 shadow-only，request-aware 输入仍不完整 | `ADOPT` | Gateway |
| Lite/Caveman/RTK/Stacked | context trim/compaction 基础已有；完整 OmniRoute stage 未形成生产闭环 | `ADOPT`，分阶段 | Gateway |
| MCP registry/policy/audit | ToolRegistry、meta-tools、tenant policy 已有 | `ADOPT` | Gateway |
| MCP JSON-RPC/transports | server/dispatcher/stdio/SSE/HTTP 未完成 | `ADOPT`，独立实现 | Gateway |
| A2A task semantics | ASM 有窄 task state mapping；Gateway wire protocol 未完成 | `ADOPT` / `CONSUME` | Gateway transport，ASM projection |
| Fusion | 无完整 coordinator/panel/judge | `ADOPT`，最后实施 | Gateway |
| routing/compression/cost metadata | Gateway 可产生 canonical facts | `CONSUME` | ASM |
| provider/credential/router/compression engine | Gateway 领域所有权 | `REJECT` in ASM | Gateway |
| MCP execution/A2A streaming/Fusion in ASM | 会形成第二数据面 | `REJECT` | Gateway |
| plugin distribution/marketplace | Maintain 有 catalog/ticket/install 方向 | `CONSUME`/`REJECT` | Maintain |

## 3. Provider catalog

建议流程：

```text
OmniRoute/reference export JSON
  -> catalog schema lint
  -> protocol/auth/url/model manifest validation
  -> idempotent seed/upsert
  -> disabled until discovery/contract test succeeds
  -> candidate mapping through existing provider.Candidate
```

约束：

- 不在 Go 中复制大量 TS 常量；数据库 catalog 是运行时事实源。
- 未验证 provider 默认不可路由。
- secret、API key、完整 header 不进入 catalog、日志或 telemetry。
- provider contract test 必须覆盖 models、chat、stream、error、429、auth 和 endpoint-specific parameters。

## 4. Active routing strategy

保留线上 URSM、health、tier、billing round、sticky、P2C/Bandit 过滤顺序。OmniRoute 风格策略只对安全候选排序：

- cost：未知成本为惩罚，不允许 unknown 变成免费；
- cache：使用 request cache policy、prompt cache capability 和实际 telemetry；
- context：按输入 token、output budget、safety margin 做硬过滤；
- headroom：同时定义 concurrency、FP slot、queue pressure 的含义。

上线顺序：shadow → tenant opt-in → small traffic → observation → wider rollout。策略不得增加额外 LLM 请求或绕过 limiter。

## 5. Tiered compression

建议在协议转换完成、上游发送前建立 stage contract：

```text
original request
  -> Lite / mechanical normalization
  -> Caveman rules (if enabled)
  -> RTK tool-result filtering (explicit, structured tools only)
  -> optional Stacked orchestration
  -> final outbound body
```

每个 stage 必须有：

- request-scoped once guard；
- input/output hash；
- token estimate and savings；
- reason/code/version；
- max size/time budget；
- fail-open rollback；
- no mutation of unknown protocol fields。

默认不对 streaming response 做不可逆压缩；RTK 只处理明确的 tool-result，不删除 tool-call pairing。

## 6. MCP

现有 ToolRegistry/meta-tools 可作为 MCP backend，但 `/api/meta-tools/load` 不是 MCP server。

最小实施顺序：

1. stdio read-only server：`initialize`、`tools/list`；stdout 只输出 JSON-RPC，不输出日志。
2. `tools/call`：绑定 authenticated principal、tenant、scope、policy、timeout、panic isolation、audit。
3. HTTP 单请求 transport。
4. SSE/streamable HTTP，并补 session/nonce/replay/connection limits。

任何 disabled 或 tenant-denied tool 不得 list/call；工具结果必须经过敏感数据与大小限制。

## 7. A2A

Gateway 负责 JSON-RPC/HTTP/SSE adapter、Agent Card、skill invocation 和 executor binding；ASM 只接收任务状态与低敏 metadata。

先交付：

- 同步 `message/send`；
- persisted task lifecycle；
- tenant/RLS、cancel 幂等、TTL；
- read-only skills。

再交付 `message/stream` 和 SSE。断开连接默认不取消 task，取消必须显式且通过条件状态转换。

## 8. Fusion

Fusion 不是普通 retry/failover。必须独立实现：

- candidate panel；
- staged/parallel/judge coordinator；
- per-source limiter reservation；
- request budget and cancellation；
- source-level usage/cost/audit；
- winner/evaluator decision；
- replayable result。

默认只允许显式 opt-in、非流式。先 staged，再小 panel parallel，judge 最后实施。

## 9. ASM / Maintain 边界

ASM：

- `CONSUME` routing decision、provider/model、compression stage/savings/reason、quality/cost/task metadata；
- 不保存完整 prompt/response；
- 不实现 provider、router、compression engine、MCP execution、A2A streaming 或 Fusion。

Maintain：

- `OWNER` artifact、license、activation、ticket、install、upgrade；
- 可消费低敏 instance/fault/telemetry；
- 不实现 Gateway provider executor 或 Agent transport。

## 10. 验收约束

- Provider 未验证默认 disabled；catalog 导入幂等。
- Routing 不绕过 tenant/URSM/health/tier/limiter；决策可解释。
- Compression 语义等价、stage 可回退、未知字段不丢失。
- MCP auth/policy/schema/audit 顺序固定，disabled tool 不可 list/call。
- A2A 状态机合法、cancel 幂等、task TTL 和 tenant scope 可验证。
- Fusion 每个 source 可对账、可取消、可限额、可回放。
- 290 provider、104 tools、78–95% savings 只能标为上游声明或项目验收目标。
