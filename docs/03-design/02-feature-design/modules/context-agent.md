# 上下文智能体（Context Agent）

网关侧的记忆对接层：把「客户端请求 → 识别项目/任务/会话 → 提交 LLM → 识别工具调用 → 从网关加载记忆 → 从 Memora 加载分层信息 → 提交 LLM → 返回无记忆加载任务的最终答案」这条闭环做进网关，并把**接收与处理过的内容原样记录下来**（可逐字节还原客户端请求与 LLM 返回），中间由网关做上下文识别、记忆分层加载与动态压缩。

## 关联能力

- 会话缓存 / 会话分析：提供逐步摘要与正文（`session_request_summaries` / `session_bodies_unified`），是 L0 索引层与 L1 明细层的数据源。
- Memora：跨会话长期记忆（L2 语义层），见 [memora.md](./memora.md)。
- MCP 服务网关：把记忆能力以**本地工具**的形式暴露给 MCP 客户端，见 [mcp-gateway 合并映射](../../../../docs/mcp-gateway/03-legacy-merge-map.md)。
- 上下文压缩：现有三档兜底（机械裁剪 → Memora L1 → LLM 摘要）是本模块的**现状对照面**，不是它的替代品。

## 为什么需要它

现状有三条互相独立的缺口（本轮取证结论）：

| 缺口 | 证据 | 后果 |
|---|---|---|
| **网关没有记忆能力面** | 全仓 grep `memora`/`memory` 在 `mcp/` 里零命中；`mcp/server.go` 的 `tools/call` 唯一出口强制要求 Adapter | 客户端只能把记忆做成上游工具，网关自己「按步取回」的能力既列不出也调不了 |
| **逐步摘要只写不读** | `session_request_summaries` 由 `domains/analysis/request_summary.go` 写入，全仓唯一读方是运营全景页 `admin/session_panorama_handler.go:440` | 数据早就在库里，但**没人把它端给模型** |
| **「步」在仓内不存在** | 现有粒度是「轮」（`session_turns.turn_no`）；`domains/hooks/compression/task_analyzer.go:65-78` 的边界识别触发词**全是英文字面量** | 「每一步的目标与结果」在中文会话里没有落地载体（实测 0/120） |

⇒ 本模块把这三件事接起来：**层**（L0/L1/L2）、**步**（轮为单位的可寻址单元）、**工具**（网关托管的记忆工具）。

## 架构

### 分层模型

| 层 | 内容 | 数据源 | 寻址方式 | 体积量级 |
|---|---|---|---|---|
| **L0 索引** | 每步一行「目标 → 结果」 | `session_request_summaries`（网关 PG） | 步号 | ≈45 token/步 |
| **L1 明细** | 某一步的完整正文（工具输出、报错、文件内容） | `session_bodies_unified`（网关 PG） | 步号（精确） | ≈2.4K token/步（随工具输出大小变） |
| **L2 长期** | 跨会话事实、偏好、踩过的坑 | Memora | 语义（top-k） | 8 条/次 |
| **KB 知识库** | 项目级知识 | **仓内无知识库存储** | —— | 仅留契约与预算位 |

★ **Memora 只能做 L2**：它的检索请求体是 `{user_id, query, top_k}`（`domains/memory/client/client.go:324-326`），**没有 id/step 寻址**，所以「加载第 N 步」必须走网关 PG。这是 L0/L1 用 PG、L2 用 Memora 的唯一理由。

★ **「步」= 网关原生粒度「轮」**：网关的原生粒度是「轮」（一次 HTTP 请求 = `session_turns.turn_no`），不是「步」。本模块把「轮」作为可寻址的步号，`X-Gw-Step-Index` / `X-Gw-Step-Id` 作为未来客户端契约预留。

### 装配形状

出站体的装配顺序与**生产既有 rebuilder 同序**（`domains/hooks/compression/rebuilder_openai.go:141-146`）：

```
[system] [L0 索引] [首条 user] [尾部 N 条原始消息] [按需加载的步骤明细]
```

⇒ 从「一段扁平摘要」升级成「一行一步的索引」**不需要新装配器**，只改喂进去的内容。装配按**字节**做（`assemble.go` 的 `spliceMessages`），保留 `model` / `stream` 等原始字段字节。

### 记忆工具

网关托管两个工具（`domains/contextagent/tool.go`）：

| 工具 | 层 | 参数 | 说明 |
|---|---|---|---|
| `load_step_detail` | L1 | `step_index`（必填） | 按步号精确取回明细 |
| `search_memory` | L2 | `query`（必填）、`top_k` | 跨会话语义召回 |

设计约束：

- **不转发上游、不需要客户端注册、不需要 MCP 适配器**。
- **非记忆工具一律不识别、原样透传**（`DetectMemoryToolCalls` 只认这两个名字）。
- **失败不抛错**：工具失败也要给模型一句可读说明，让它换个方式继续，而不是让整条请求挂掉。
- **单次结果有字节上限**（默认 32KB，UTF-8 边界裁剪），防止一次加载炸掉上下文。

### 流程（九步）

`domains/contextagent/loop.go` 的 `Agent.Process`：

1. 识别身份（项目 / 任务 / 会话 / 租户）
2. 规划预算（`MakePlan`：预算 = 窗口×60% 封顶 200K − 已占用；分配序 Tail→Index→StepDetail→LongTerm）
3. 加载各层（L0 / L1 / L2）
4. 装配出站体
5. 注入记忆工具声明（幂等；客户端没带 `tools` 键时插入）
6. 提交上游
7. 识别记忆工具调用
8. 执行工具 → 追加结果 → **重新提交**（同候选，不换模型）
9. 记录（可还原）

★ **回环必须同步**：客户端要拿到「无记忆加载任务的最终答案」，所以本模块不复用仓内既有的异步 `injectFollowUpRequest`（`domains/streaming/response_interceptor_helpers.go:105-228`，只发不回收）。同步回灌用 `httptest.NewRecorder()` 式的回灌（同文件 `defaultDispatchFollowUp` 已示范过这条路的失败模式是**已知**的）。

★ **回灌必须带调用方自己的凭据**：否则回灌请求以网关内部身份过鉴权与计费，归属错乱（凭据归属 / 计费 / 审计全部挂错主体）。`RequestSubmitterDeriver` 就是为这条纪律加的：回灌实例按请求派生（带调用方 Authorization + 身份头）。

### 身份与命名空间

| 维度 | 来源 | 说明 |
|---|---|---|
| 租户 | 鉴权层已解析的结论 | 不自己重解析，避免两套口径漂移 |
| 会话 | `internal/sessionid.Resolve`（body 优先、再 header） | 仓内会话 id 的唯一真源 |
| 项目 | `X-Gw-Project-Id`，缺省回落 `app:<application_code>` | 端到端已接线，但实测几乎无客户端发送 ⇒ **回落路径才是常态路径** |
| 任务 | `X-Gw-Task-Id`（自由文本关联串） | 仓内无任务登记表 |

记忆命名空间 = `t:<tenant>:k:<key>:<task>`，与 `domains/memory.UserID` **同构** ⇒ L0/L1（PG）与 L2（Memora）落在同一命名空间下，否则跨层检索会串租户/串任务。

## 可还原记录

需求是「网关接收与处理的内容要能记录，能原样还原客户端请求与 LLM 返回」。现状三条路都不通：

| 现有资产 | 为什么不能用 |
|---|---|
| `domains/requestjourney/contract.go:371-373` | 契约**明文禁止** body/header（且 `DisallowUnknownFields`） |
| `internal/logging/raw_data_logger.go` | 唯一字节级存储，但**只写、轮转、无读面**；且 `client_request` 方向记的是**改写后**的体（`handler.go:2274-2281` ← `handler.go:4588 upstreamBody`），客户端**原始**字节只活到 `handler.go:2764 logCtx.Body` |
| `domains/streaming/diagnostic_context.go:82` | 流式逐帧字节级已有，但无「执行工具 → 追加结果 → 重投」的循环骨架 |

⇒ 本模块自带记录器：`Record` 存三份原始字节（客户端请求 / 第一次上游响应 / 最终响应）+ 逐轮中间态 + 各层来源；`FileRecorder` 按 `request_id` 一文件一 JSON，**base64 无损**、原子写（tmp + rename）、`safeFileName` 防路径穿越。`GET /record/{request_id}` 取回。

## 端点与接入

### HTTP 门面（`/api/system/context-agent`）

| 端点 | 作用 |
|---|---|
| `POST /resolve` | 识别 + 计划 + 分层加载 + 装配。`submit=false` ⇒ 返回装配后的出站体（客户端驱动）；`submit=true` ⇒ 网关继续提交并跑完记忆工具回环，返回最终答案（网关驱动） |
| `POST /tool` | 执行一次记忆工具调用（MCP 本地工具也走这条） |
| `GET /record/{request_id}` | 取回可还原记录 |

鉴权：管理令牌（`middleware.NewAdminTokenMiddleware`）。它是内网能力面，不是公开端点。

### MCP 本地工具

`mcp/local_tools.go` 补的是 `mcp/` 的能力缺口：`tools/call` 此前只有上游一个出口（`mcp/server.go:581-601`）且强制要求 Adapter（`resolveTool` 无适配器即 404），`tools/list` 也只镜像上游 ⇒ **网关自有的能力无法被列出、更无法被调用**。

本地工具**同样走授权链**（scope → 可见性 → key×tool 允许表（deny-wins）→ 敏感度审批 → 参数白名单/JSON Schema），只跳过「适配器允许表 / 绑定 / 限流 / 熔断」这四道**与上游有关**的闸。判定口径**复用**既有的 `toolVisible` / `keyAllowsTool` / `checkApproval` / `validateParams`，不另立一套。

零注册 ⇒ 行为与引入该能力前完全一致。

## 启用

```bash
# 总开关（默认关）
LLM_GATEWAY_CONTEXT_AGENT_ENABLED=1

# 可还原记录落盘目录（不配 ⇒ 不记录）
LLM_GATEWAY_CONTEXT_AGENT_RECORD_DIR=/var/lib/llm-gateway/context-agent

# 回灌端点（不配 ⇒ 由 LLM_GATEWAY_LISTEN 推导）
LLM_GATEWAY_CONTEXT_AGENT_LOOPBACK_URL=http://127.0.0.1:8781/v1/chat/completions
```

依赖是**软依赖**：没有 PG 就少 L0/L1 层，没有 Memora 就少 L2 层，缺层不阻断请求（记忆是增强，不是前置条件）。

## 实测读数

合成会话 120 步 / 285 轮 / 570 条消息，每条工具输出 6,000B。
证据：`docs/audit/evidence/2026-11-10-context-layering/bench-120steps-200k-session-32k-local.json`；
工具：`cmd/context-layering-bench`（守护测试 11 条断言）。

| 表示 | 会话窗口 200K | 本地识别/记忆处理窗口 32K |
|---|---|---|
| 完整历史 293,510 token | ❌ 超 93,510 | ❌ 超 260,742 |
| 现状①机械裁剪 | ✅ | ✅ |
| 现状②LLM 摘要 44,055 token | ✅ 装得下（占 22%） | ❌ **超 11,287** |
| **分层基座 8,861 token** | ✅（占 4.4%） | ✅（占 27%） |
| 分层 + 按需一步 10,651 | ✅ | ✅ |

⇒ **本地处理窗口内，分层基座之上还能再按需加载 9 步明细。**

> ★ **窗口口径订正（2026-11-10 晚间）**：可行性分析初版只按 32,768 一个窗口读数，因而写下「现状 LLM 摘要仍然超窗 1.34×」。**该结论在生产会话窗口（≈200K）下不成立** —— 摘要装得下 200K，装不下的是完整历史。真正成立且更锋利的约束是：**32K 是网关自己做「步边界识别 / 记忆处理」的本地模型的窗口**，而一段扁平摘要（44,055）**连本地模型自己都读不完** ⇒ 它无法在摘要上做步识别与记忆抽取。分层基座 8,861 装得下。
> ⇒ 方案的价值从「省会话窗口」搬到了「**让本地处理链真的能跑**」——后者才是「网关内建一个上下文识别 + 记忆分层加载的智能体」这条需求的前置条件。
> 口径已由 bench 的两条断言钉死（`TestSessionWindowAcceptsLLMSummaryButNotFullHistory` / `TestLocalProcessingWindowRejectsLLMSummary`），防止再被混用。

## 当前边界（诚实登记）

| 项 | 状态 |
|---|---|
| **中文步边界识别** | **零**。`domains/hooks/compression/task_analyzer.go` 的触发词全是英文字面量 ⇒ 中文会话分不出「步」。这是**前提级缺口**，本模块的 L0 索引在中文会话里拿不到可靠的步划分 |
| **L0 索引关键标识符保留率 62.5%** | 按 `ruleExtract` 契约（请求 200 / 回复 300 字符）生成时，约三分之一步骤的关键标识符会丢（它只出现在工具输出里）⇒ 索引不足以支撑「据此判断要不要加载这一步的细节」 |
| **L1 明细的存储形态是二次方的** | 生产上 `request_delta` 常常是**客户端完整历史**而不是本轮新增（`domains/session/v2/session_writer_v2.go` 的 FIXME-14372/14373，实测 45–146× 放大）⇒ 本模块对同一轮按 role+content 指纹**去重**缓解，并把去重前后的字节数记进 `LayerContent.Sources`；真正的修法是改 `extractRequestDelta` 的差集基准（属主决策⑭/⑮，未拍板） |
| **知识库层（KB）** | 只留契约与预算位，**未实现取数** —— 仓内没有知识库存储（`knowledge_bases` 是外仓 schema 残留） |
| **记录未 PG 化** | 现为文件型（可读回、可还原）；PG 表 + 迁移 + manifest + dbinit + RLS 属下一阶段（是替换而非重写） |
| **未内联到 chat 热路径** | 本轮以 HTTP 端点 + MCP 本地工具交付完整流程，**零热路径改动**。内联缝合点已定位（`handler.go:2764` 原体仅存处 / `4277→4588` 改写点序 / `6129-6320` 响应拦截链），属下一阶段 |
| **Memora 生产默认关闭** | 且样例里的 `MEMORA_PROJECT_ID` 与客户端读的 `LLM_GATEWAY_MEMORA_PROJECT_ID` 对不上 ⇒ 缺 `X-Project-ID` 时 Memora fail-closed 400 |

## 分阶段落地

| 阶段 | 内容 | 出口判据 |
|---|---|---|
| **P0（已完成）** | 分层包 + MCP 本地工具 + HTTP 门面 + 可还原记录 + 双窗口实测 | `go test ./domains/contextagent/ ./mcp/ ./cmd/gateway/` 全绿；`make guards-fast` rc=0 |
| **P1** | **中文步边界识别**（本地化触发词 + 结构化信号） | 中文会话边界覆盖率 > 0（当前 0/120） |
| **P2** | L0 索引升级：强制保留关键标识符（目标/结果各自带结构化 key 段） | 标识符保留率 > 90%（当前 62.5%） |
| **P3** | 记录 PG 化（表 + 迁移 + manifest + dbinit + RLS） | `GET /record/{id}` 换成 PG 读面，文件型退化为兜底 |
| **P4** | KB 层取数（等仓内有知识库存储后） | L0 之上多一层项目知识 |
| **P5** | 内联 chat 热路径（缝合点已定位） | 客户端零改动即获得分层上下文；带 `NeverWorse` 守门 |
| **P6** | 修 `request_delta` 的差集基准（等属主拍板⑭/⑮） | L1 明细不再二次方 |

★ P1 与 P2 是**前提**：它们不做，L0 索引在中文语境里就是空的或不可寻址的。
