# 04 · 网上热门思路与发散型建议

> **目的**：把 2024-2026 年行业内"AI 网关 / Agent 编排 / LLM 可观测"的热门思路映射到我们当前的能力缺口，并给出 8 项发散型实验建议。
> **约束**：不引入新协议独占执行器（v6 维持 Gateway 是 provider executor 唯一 owner）；不动 v4 已冻结契约；PII / RLS / 安全优先于新能力。
> **结构**：§1 行业基线（10 项）→ §2 落地映射（每个基线对应到 v6 哪个波次 / 哪个差距）→ §3 发散型建议（8 项，含风险与证据要求）。

---

## 1. 行业基线（10 项）

> 每条均提供"我们应当学到什么 + 落到 v6 哪里"。引用源以官方文档 / RFC / 学术论文为主；未实证的不当作事实承诺。

### 1.1 MCP（Model Context Protocol）治理与安全基线

- **共识**：MCP（Anthropic 2024-11 提出，2025 进入 Linux Foundation Agentic AI Foundation）已成为 Agent↔Tool 通信事实标准；规范定义 JSON-RPC 2.0 transport、tool 描述、`tools/list` 与 `tools/call` 语义。Server 端应只输出 JSON-RPC 日志，不污染 stdout；必须支持 OAuth / scope / policy；disabled tool 不可 list/call。
- **学到什么**：我们 `plugin-runtime/` 已具备 manifest / sandbox / health_loop / binding 骨架；`metadata/tool_registry` 已存在；缺的只是 JSON-RPC transport 与 auth/policy/audit 串联。
- **落到 v6**：V6-W4-W1 ~ W4（详见 [`03-roadmap-v6-waves.md` §5.2](03-roadmap-v6-waves.md)）。先 stdio read-only、再 `tools/call`、再 HTTP、再 SSE / streamable HTTP。每一步独立 staging 验证。
- **来源**：modelcontextprotocol.io 规范、Linux Foundation Agentic AI Foundation 公告、MCP Inspector 参考实现。

### 1.2 A2A（Agent-to-Agent）协议与 task state machine

- **共识**：A2A（Google 主导，2025-04 开放）核心是 AgentCard 自描述 + `message/send` / `message/stream` + Task 状态机；适合长时、跨 Agent 协作。一个 Task 可跨多轮 Message；产出是 Artifact。断开连接默认不取消 task；取消必须显式。
- **学到什么**：我们的 v6 不做 A2A 完整生产路径，只做"同步 message/send + persisted task 试点"。`GoalRun` CAS + lease 已是 task 状态机的雏形；A2A 可消费 ASM 投影层。
- **落到 v6**：V6-W4-W5（试点）。同步 path + persisted task + tenant/RLS + cancel 幂等 + TTL + read-only skills。SSE 与 streaming 留给后续版本。
- **来源**：a2a.proto v1.0（a2aproject/A2A @ 2026-06-24）、A2A 协议规范 §Task state machine。

### 1.3 AI Gateway 模式：Envoy AI Gateway / LiteLLM / Portkey / Kong / Cloudflare / Solo.io

- **共识**：
  - Envoy AI Gateway（2024-Q4 GA）：把 LLM 路由抽象为 `llm` route；ext_proc / ext_authz 做请求级插件；内置 token rate limiting 与 cost attribution。
  - LiteLLM（最广泛使用）：OpenAI 兼容接口 + 一行替换；与 LiteLLM Proxy 类似，多 provider failover；缺 enterprise 多租户。
  - Portkey：可观测 + cache + fallback；偏 SaaS。
  - Kong AI Gateway / Cloudflare AI Gateway：边缘 / L7 网关上的 LLM 治理；多与 WAF、rate limit 集成。
  - Solo.io / Bifrost：gateway-first，envoy-based；与 k8s 集成强。
- **学到什么**：这些产品验证了 3 件事 ——
  1. **Provider failover 必须有 backoff + circuit breaker**，否则会放大故障（我们已有 [`node-probe-mechanism.md`](../../03-design/01-architecture/architecture/node-probe-mechanism.md)）；
  2. **Token-based rate limiting 比 RPM 更合理**，因为不同模型 token 量差异巨大；TPM limiter 已存在但 admission 完整接线需 W1-W8 收口；
  3. **Cost attribution 必须基于"每次请求的 token × 模型价目表"**，而不是聚合日志；我们已有 MaaS + `model_rates` + `usage`，但与 cascade router / semantic cache 联动需 V6-W5。
- **落到 v6**：W1-W5 retry budget、W1-W8 联合 lease、W5-W1 cascade、W5-W2 semantic cache。
- **来源**：Envoy AI Gateway 官方 docs、LiteLLM Proxy README、Portkey 文档、Kong AI Gateway 文档、Cloudflare AI Gateway 文档、Solo.io 博客。

### 1.4 eBPF L7 观测 / OpenTelemetry eBPF profiler

- **共识**：eBPF 已在生产环境用于零侵入 L7 观测（Cilium Tetragon、Pixie、Beyla）。对 LLM 网关，可以零侵入捕获 SSE chunk latency、queue wait、Go goroutine 阻塞；与 OTel trace 串联。
- **学到什么**：我们已经有 OTel + Prometheus，但 SSE chunk 级 latency 与 Go goroutine 调度阻塞仅靠应用埋点不够；eBPF sidecar（Pixie / Beyla）能补"客户端→网关→上游"全路径 latency。
- **落到 v6**：暂不引入内核依赖（会增加部署复杂度）；V6-W5 评估 Pixie / Beyla 作为可选 observability sidecar；先在 staging 跑 1 周成本评估。
- **来源**：CNCF Tetragon 项目、Pixie docs、Beyla docs、OpenTelemetry eBPF profiler SIG。

### 1.5 OpenTelemetry GenAI Semantic Conventions

- **共识**：OTel GenAI SIG（2024-2025 推进）已发布稳定 + 实验性语义约定：
  - `gen_ai.system`（openai / anthropic / vertex_ai 等）
  - `gen_ai.request.model`、`gen_ai.response.model`
  - `gen_ai.usage.input_tokens`、`gen_ai.usage.output_tokens`
  - `gen_ai.response.finish_reasons`
  - `gen_ai.request.choice`（tool / message）
- **学到什么**：当前 OTel 属性为内部命名（`autoroute.classify.duration_ms`、`streaming.ttfb_ms` 等），未对齐 `gen_ai.*`。这导致跨厂商 LLM 客户端（APM / 后端成本分析）无法直接消费我们的 trace。
- **落到 v6**：V6-W3-W12 在 `internal/observability/semconv.go` 引入对齐；保留内部命名属性做兼容；统一 emit 两份。
- **来源**：opentelemetry.io docs（semantic conventions → gen_ai）、OTel GenAI SIG 仓库。

### 1.6 语义缓存（Vector / Hybrid Prompt Cache）

- **共识**：pgvector / Qdrant / RedisVL 可做 prompt 语义缓存（embedding similarity > threshold 视为命中）。生产案例 hit rate 15-35%；严格语义要求"重放必须可证明等价"，因此常与"response snapshot + token hash"组合。
- **学到什么**：我们已有 cache 子目录（`cache/`）；但语义缓存需与 token 估算、cost attribution、policy（per-tenant opt-in）协同。OTel GenAI semconv 是必要前置。
- **落到 v6**：V6-W5-W2（依赖 W3-W12 OTel semconv + W5-W1 cascade）。先 per-tenant Opt-in + 仅对非敏感内容启用；首月目标 hit rate ≥ 15%。
- **来源**：RedisVL docs、pgvector docs、Qdrant docs、LangChain Cache 集成文档、Anthropic prompt caching 文档（不同语义层级）。

### 1.7 Wasm 插件 / OPA Rego / Envoy ext_proc

- **共识**：Envoy 的 Wasm 插件 + ext_proc / ext_authz 提供 L7 sidecar-less 扩展；OPA Rego 用于 prompt 安全 / 速率 / 配额策略；Wasm 用 OCI 镜像分发。
- **学到什么**：我们的 plugin-runtime 进程内 sandbox 已就绪；Wasm / OPA 暂不引入（部署复杂度高）。但 Wasm 的"无 sidecar、可热更新"理念可借鉴：plugin 升级走 `installer/` + GPG 签名 + rollout gate（已部分实现）。
- **落到 v6**：不引入 Wasm / OPA 运行时；仅在 V6-W4-W2（plugin 升级）借鉴 OCI 分发模式。
- **来源**：Envoy Wasm 文档、OPA docs、Proxy-Wasm ABI。

### 1.8 SSE 取消 / Backpressure 行业基线

- **共识**：2024-2026 边缘运行时（Cloudflare Workers、Vercel Edge、Deno Deploy）默认支持 fetch abort；SSE 取消依赖客户端 `EventSource.close()` 或 `AbortController`。服务端正确做法：
  - 检测客户端断连（`http.CloseNotifier` → `ctx.Done()`）；
  - 主动取消上游（`req.Context().Done()` 透传）；
  - 资源按 lease token 释放（不能依赖 defer，因为 defer 在 socket close 后未必触发）。
- **学到什么**：我们的 stream retry + survival coordinator 已经做"客户端取消 → 释放资源"；但 lease token 释放路径在 W1-W8 才统一。`deploy/zero-downtime-design.md` 涉及部分，但未覆盖 SSE backpressure 详情。
- **落到 v6**：V6-W1-W5 统一 RetryBudget + V6-W1-W8 联合 lease；不引入新依赖。
- **来源**：WHATWG fetch spec、Cloudflare Workers docs、Vercel Edge docs、MDN EventSource / AbortController。

### 1.9 OpenAI 兼容扩展（行业事实）

- **共识**：截至 2025-08，OpenAI 兼容事实上包括：
  - `/v1/chat/completions`（含 `stream`、`tools`、`response_format`、`logprobs`、`stream_options.include_usage`）
  - `/v1/responses`（OpenAI 2025 新协议）
  - `/v1/embeddings`、`/v1/models`、`/v1/files`、`/v1/batches`
  - structured outputs（`json_schema` / `strict: true`）
- **学到什么**：我们的 OpenAI handler 已覆盖 Chat + Embeddings + Models；`stream_options.include_usage` 在 token 计费上有用；structured outputs 与 logprobs 我们未实现（可作 backlog）。
- **落到 v6**：V6-W4 在 MCP/A2A/Fusion 落地后评估 structured outputs 是否进 v6.5；不引入新协议独占执行器。
- **来源**：OpenAI API reference（2025-08）、LiteLLM OpenAI 兼容矩阵。

### 1.10 Cascade Router / 模型级联路由

- **共识**：学术 RouteLLM / FrugalGPT / NotDiamond 证明：把"明显简单的请求"路由到便宜 / 本地小模型，可显著降本且 quality 退化 < 5%。生产上常与 cached classifier + per-task threshold 联合。
- **学到什么**：我们 `autoroute/` 已有 classifier（pattern / LLM / embedding / complexity scorer）；缺的是"按 cost-aware + quality-aware"的双层决策。我们已经规划 cost / cache / context / headroom 评分器但仅 shadow 模式。
- **落到 v6**：V6-W5-W1。先 shadow 对比 + tenant Opt-in + 14 天 quality 不退化观察；首月目标 cost ↓ ≥ 10%。
- **来源**：RouteLLM（arxiv 2403.02321）、FrugalGPT（arxiv 2305.05176）、NotDiamond 案例研究。

---

## 2. 落地映射表

| 行业基线 | v6 波次 | 主要差距（来自 [`02`](02-code-vs-design-deltas.md)） |
|---|---|---|
| MCP 治理与安全 | W4-W1 ~ W4 | D17 |
| A2A message/send + persisted task | W4-W5 | D17 |
| AI Gateway failover + token RL + cost attribution | W1-W5 / W1-W8 / W5-W1 | D8 / D19 / D20 |
| eBPF L7 | W5（评估） | D25 |
| OTel GenAI semconv | W3-W12 | D16 |
| 语义缓存 | W5-W2 | 新增 |
| Wasm / OPA | 借鉴（不引入） | 不立项 |
| SSE 取消 / backpressure | W1-W5 / W1-W8 | 隐式 |
| OpenAI 兼容扩展 | W4 评估（structured outputs） | 新增（backlog） |
| Cascade router | W5-W1 | 新增 |

---

## 3. 发散型建议（8 项）

> 每项标注"投入 / 风险 / 证据要求"。v6 内不全部立项；评估通过后进入 backlog。

### 3.1 X1 · Prompt-Level 成本归因面板（按 model × task × tenant）

- **想法**：把"每次请求"绑定到 `(model_used, task_type, tenant_id)` 三元组，输出"成本 TOP-N 模型 × 任务 × 租户"看板；与 [`stats-reconciliation.md`](../../stats-reconciliation.md) 对账。
- **投入**：小（2 周）；仅前端 + SQL 视图。
- **风险**：低。复用 cost reconciliation worker 数据。
- **证据**：P50/P95 cost 报表上线；周环比 baseline；与 MaaS 订单对账 ≤ 0.5%。
- **落地**：V6-W5 并行项。

### 3.2 X2 · Token-Level Hot Path Trace

- **想法**：在每次请求的 OTel trace 上加入"每个 token 的来源（input cached / input new / output）"，方便模型成本归因到具体 token。
- **投入**：中（3-4 周）；需要 provider 返回 `cached_tokens` / `prompt_tokens_details` 等字段对齐（OpenAI / Anthropic / Gemini 各异）。
- **风险**：中。各 provider 字段命名差异大；需做 adapter。
- **证据**：3 个 provider 同时跑通；cached token 占比可观测；与 cost 对账一致。
- **落地**：V6-W5 评估。

### 3.3 X3 · LLM 路由的"模型卡"概念（Model Card Registry）

- **想法**：为每个模型定义一份"卡片"（输入/输出价格、context window、tool support、stream support、cached price、latency p95、quality p50、已知问题），路由决策可引用卡片字段；不在 hot path 实时拉取，启动 / 周期加载。
- **投入**：中（3 周）；`modelcatalog/` + `autoroute/card.go`。
- **风险**：中。卡片字段定义需评审；模型更新频繁需 hot reload。
- **证据**：3 个 provider × 10 个模型卡完整；路由决策可解释包含卡片字段。
- **落地**：V6-W5-W1 前置。

### 3.4 X4 · 端到端"真实重放"工具（基于生产 request_id）

- **想法**：输入生产 `request_id`，在隔离 canary 用同一 tenant / 同一模型 / 同一凭据重放；输出"本次响应是否与生产一致"（接受 SSE 等价、TTFB ±20%、token 量 ±10%）。
- **投入**：中（4 周）；依赖 session V2 canonical owner 已切换。
- **风险**：高。生产 prompt 可能含敏感数据；必须 per-tenant Opt-in + redaction gate；非生产环境不能直接落 prompt。
- **证据**：10 个真实 request_id 重放，5 个等价；附 PII 拦截证据。
- **落地**：v6 之后（V7 候选）。

### 3.5 X5 · Provider 合同测试矩阵

- **想法**：按 OmniRoute §3 提到的 contract test 思路，建立"每个 provider 的合同测试矩阵"——models / chat / stream / 429 / auth / endpoint-specific parameters；CI 自动跑；provider 升级触发告警。
- **投入**：中（4 周）；`provider/contract/` 子包。
- **风险**：中。Provider 行为不一致；需约定 baseline。
- **证据**：6 个 provider 合同测试全绿；CI 阻断 provider 升级失败。
- **落地**：V6-W4-W8 并行。

### 3.6 X6 · 多协议等价性自动化测试平台

- **想法**：用同一入参（OpenAI Chat / Anthropic Messages / Responses / Gemini native）发请求，断言响应结构 + token 量 + latency 在阈值内等价；用于客户端 SDK 升级 / provider 升级验证。
- **投入**：中（4 周）；`tests/integration/protocol_equivalence/`。
- **风险**：低（仅测试）。但要小心：协议不等价是预期行为，断言需 careful。
- **证据**：每协议 ≥ 20 用例；CI 报告等价率。
- **落地**：V6-W1 并行。

### 3.7 X7 · 前端 Vue3 组件级 Storybook

- **想法**：把 `web/src/components/` 中关键组件（Dashboard / QueuePerspectivePanel / NodeDetailDrawer / RequestLogDrawer / LiveRequestStreamV2）按 Storybook 6 整理；含 visual regression + a11y 测试。
- **投入**：小（2 周）；前端纯改造。
- **风险**：低。前端单测 + vitest 已就绪。
- **证据**：50+ stories；视觉回归 0 误报。
- **落地**：V6-W2 并行（与 D7 Dashboard 优化同时）。

### 3.8 X8 · Gateway "自描述" OpenAPI 文档站点

- **想法**：把 `cmd/gateway` 注册的所有路由（`/v1/*`、`/v2/*`、`/api/admin/*`、`/maintain-api/*`）自动生成 OpenAPI 3.1；按 OpenAI 兼容 + Anthropic 兼容 + Admin + Maintain 分组；含租户 scope / RLS 标注。
- **投入**：中（3 周）；`internal/openapi/` + 文档站点（`web-docs/`）。
- **风险**：中。大量端点写得不规范；需先做"端点契约审计"。
- **证据**：100% 端点覆盖；OpenAPI lint 0 错；文档站点可部署。
- **落地**：V6-W2 / V6-W5 之间。

---

## 4. 风险与不做

- ❌ **不引入 Wasm / OPA 运行时**（部署复杂度高，借鉴 OCI 分发理念即可）。
- ❌ **不引入 eBPF 内核依赖**（除非能证明零侵入 + 显著价值）。
- ❌ **不做 prompt-level 模型微调 / self-hosted LoRA**（不在网关能力范围）。
- ❌ **不做模型输出内容安全审核**（归属上游 provider / 客户端；仅做 PII 脱敏）。
- ❌ **不引入新协议独占执行器**（v6 维持 Gateway 是 provider executor 唯一 owner）。

---

## 5. 后续动作

- V6-W5 立项时挑 ≥ 3 项发散型实验（如 X1 / X5 / X6 / X7）。
- 其余项进 `docs/07-reporting/lessons-learned/v6-backlog.md`（v6 之后评估）。
- 任何实验必须满足"5 完成标准"（见 [`README.md` §5](../架构优化v6/README.md)），否则不算成功。
