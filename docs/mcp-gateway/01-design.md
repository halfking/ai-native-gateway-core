# MCP 网关（llm-gateway-go 内置）— 设计说明

> 日期：2026-11-09
> 代码位置：`mcp/`（包）、`cmd/mcp-gateway/`（独立进程）、`cmd/gateway/mcp_gateway.go`（主网关接线）
> 关联：[03-legacy-merge-map.md](./03-legacy-merge-map.md)（与既有 tools/mcp-gateway 的合并映射）· [02-capability-matrix.md](./02-capability-matrix.md) · [04-operations.md](./04-operations.md)

## 1. 为什么做进 llm-gateway-go

既有格局是三块拼起来的：

| 面 | 现状 | 问题 |
|---|---|---|
| 工具面 | `tools/mcp-gateway`（Node.js，独立仓，:8088） | 独立进程、独立技术栈、独立告警；治理链只有 key×tool 与 backend binding 两道 |
| 模型面 | `llm-gateway-go`（本仓） | 强在路由/计量/审计，弱在工具面 |
| 资产与审批 | `marketing`（应用壳） | 目录/申请/绑定齐了，但**调用链的强制点在另一个仓** |

企业场景的真实诉求是「一个入口管住模型与工具两件事」。因此本轮把 MCP 网关
作为 llm-gateway-go 的一等模块落地：**同一个进程、同一条审计链、同一套指标与
限流口径**，同时保留独立进程形态（边缘部署 / 与模型面解耦）。

## 2. 目标架构

```mermaid
flowchart TB
  subgraph clients [AgentAccess]
    A1[Codex / VS Code / opencode / WorkBuddy]
  end

  subgraph gw [llm-gateway-go]
    subgraph data [MCP 数据面]
      DP[POST /mcp · JSON-RPC over HTTP]
      ROUTE[无状态路由：协议元数据随请求]
      POL[治理链 8 闸]
    end
    subgraph ctrl [MCP 控制面]
      AD[适配器 CRUD]
      TL[工具 CRUD]
      AG[智能体 / 技能]
      KY[凭证签发·轮换·吊销]
      AP[审批]
      AU[审计查询 / JSONL 导出]
    end
    subgraph ops [运维面]
      DASH[/mcp/admin 运营看板]
      METRICS[/metrics · Prometheus]
      PRM[RFC 9728 受保护资源元数据]
    end
  end

  subgraph up [上游 MCP 服务]
    MEM[Memora]
    ACC[ACC]
    CRM[CRM / ERP / PLM]
    OA[OpenAPI→MCP 适配器]
  end

  A1 --> DP
  DP --> ROUTE --> POL
  POL --> UP
  UP[上游 MCP 服务]
  ctrl -.注册.-> UP
  DASH -.读.-> ctrl
```

### 2.1 数据面（无状态）

- 入口：`POST /mcp`，JSON-RPC 2.0 over HTTP（Streamable HTTP 形态）。
- **无状态路由**（对齐 microsoft/mcp-gateway）：协议元数据随请求携带
  （`MCP-Protocol-Version` / `Mcp-Method` / `Mcp-Name` 头，或 `params._meta`），
  网关逐请求解析路由目标，**不依赖 transport session 亲和**。
- 工具命名空间化：聚合多个上游后工具名为 `<adapter>__<tool>`（对齐 MCPJungle 的
  规范化命名）；上游已自带命名空间时保持原样。
- 分组视图：`?group=<name>` 只暴露该组工具（对齐 MCPJungle tool-groups 的
  「抑制上下文膨胀」诉求）。

### 2.2 控制面（注册与治理）

`/mcp/control/*`，管理令牌保护（`X-Admin-Token` 或 `Authorization: Bearer`）。
未配置管理令牌时整面返回 503（不暴露任何管理入口）。

适配器 / 工具 / 智能体 / 技能 / 凭证 / 审批 / 审计 七类资源的 CRUD，语义见
`mcp/control.go`。

### 2.3 治理链（数据面 8 道闸，顺序固定）

| # | 闸 | 失败语义 | 依据 |
|---|---|---|---|
| 1 | 身份（凭证校验） | 401 | 网关注发或外部账本签发的 MCP 凭证 |
| 2 | 工具存在性 | 404 / -32001 | 注册面 |
| 3 | scope（mcp.execute） | 403 / -32003 | 角色位 |
| 4 | 可见性（private/team/public） | 403 / -32003 | ContextForge 两层授权的 L1 |
| 5 | key×tool 允许表 | 403 / -32003 | 移植既有 tool-allow（deny-wins） |
| 6 | backend binding（一 key 多后端单边禁用） | 403 / -32003 | 移植既有 bindings 执法 |
| 7 | 敏感度审批（write/high） | 403 / -32004 | 分级审批（`X-MCP-Approval`） |
| 8 | 参数白名单 + JSON Schema | 403 | 参数校验与注入面收口 |

其后是**数据面配额与稳定性**：令牌桶限流（按 key×类别 / 适配器全局）→ 429；
熔断器（按适配器）→ 503；上游超时/错误 → 502；出参脱敏 → 返回前掩码凭据与 PII。

> 取舍说明：**限流排在授权之后**。被策略拒绝的调用不消耗配额，避免「越权探测」
> 反而把合法业务挤下线；同时授权判定是纯内存/单查，成本远低于上游调用。

### 2.4 敏感度与审批

| 敏感度 | 开通 | 调用 | 限流档 |
|---|---|---|---|
| read | 自助 | 直接 | read 档（默认 120/min） |
| write | 人工审批 | 直接 | write 档（默认 30/min） |
| high | 人工审批 | **需携带已批准审批凭据** | write 档 + 出参脱敏 |

审批凭据经 `X-MCP-Approval: <approvalID>` 传递，须已批准、未过期、主体匹配、
覆盖该工具（支持通配）。

### 2.5 凭证与账本

- `Ledger` 抽象出两种账本：
  - `LocalLedger`：网关自身即权威账本（独立部署）。
  - `RemoteLedger`：对接外部账本（如 ACC），**保持既有 fail-open 语义**（远端不可达
    ⇒ 放行 + 审计，不因账本故障打断数据面）。
- 签发/轮换：明文只在签发与轮换那一次返回，账本只存 SHA-256 摘要。
- 吊销：立即生效（吊销后下一次调用 401），同时清空该 key 的限流桶。

### 2.6 可观测性

- Prometheus：沿用既有 `mcp_gateway_*` 指标名（requests/rate_limited/upstream_errors/
  health_check_failures/uptime/upstream_healthy），另加 `tool_calls_total`、
  `tool_call_latency_seconds`、`authz_denied_total`、`tool_allow_checks_total`、
  `bindings_checks_total`、`approvals_required_total`、`redactions_total`。
- 审计：内存环形缓冲（默认 200 条）+ 可选 JSONL 落盘 + `/mcp/control/audit/export`
  （NDJSON，SIEM 友好）。
- 看板：`/mcp/admin` 自包含 HTML（无前端构建链，边缘环境「打开就能看」）。

## 3. 与开源实现的取舍

调研对象与结论见 `02-capability-matrix.md` 的对标表。**本轮采纳的差异化机制**：

| 采纳项 | 来源 | 落地位置 |
|---|---|---|
| 数据面/控制面分离 + 无状态逐请求路由 | microsoft/mcp-gateway | `mcp/proxy.go`、`mcp/server.go` |
| 工具命名空间化 + 分组端点 | MCPJungle | `mcp.NormalizeToolName`、`?group=` |
| 两层工具授权（可见性 + RBAC 动作位） | IBM ContextForge | `mcp/policy.go` |
| RFC 9728 受保护资源元数据 | IBM ContextForge / 规范族 | `mcp/http.go` |
| 治理插件式中间件链的可插拔点 | IBM ContextForge（41 插件） | 治理链按闸位切分，新增闸位不改主干 |
| 出参 PII/令牌掩码 | lasso-security/mcp-gateway | `mcp/redact.go`（复用仓内 `domains/secretmask`） |
| 注册即治理（工具元数据可登记） | microsoft/mcp-gateway（tools CRUD） | `mcp/control.go` |
| fail-open 的外部账本 | 既有 tools/mcp-gateway | `mcp/ledger.go` |

**明确未采纳**（V1 不做，登记为后续增强）：

- 人工审批流的原生实现（ContextForge 亦未见成熟实现）——本仓用「审批凭据 + 工具
  可见性」组合实现等价能力，审批队列本体仍归 RedClaw/ACC。
- CEL 策略引擎（agentgateway）——先用「可见性 + 允许表 + 审批」三层覆盖常见场景。
- OAuth 2.1 出向代理（3LO/OBO/RFC 8693）——需要密钥库与 IdP 侧配合，属后续轮次。
- 上游 stdio / SSE 传输归一化——本轮只接 Streamable HTTP 形态的上游。

## 4. 权威边界（不可破坏）

| 域 | 权威写方 | 本模块角色 |
|---|---|---|
| 模型调用与计量 | llm-gateway-go relay/dispatch | 不碰 |
| 工具定义目录（tool_registry） | `registry` 包 | 只引用，不复制定义权威 |
| 工具调用的强制入口 | **本模块**（数据面） | 全部 MCP 工具调用必经 |
| 资产广场与申请 | marketing（组合部署） | 通过 MCP 侧审批凭据衔接 |
| 组织身份与审批队列 | Casdoor / RedClaw / ACC | 通过 JWT 或外部账本接入 |

## 5. 部署形态

| 形态 | 组成 | 适用 |
|---|---|---|
| 随主体部署 | llm-gateway-go + `/mcp` 挂载 | 已有模型网关的企业（推荐） |
| 独立部署 | `cmd/mcp-gateway` 单进程 | 边缘/工具面专用、与模型面解耦 |

环境变量与运维口径见 [04-operations.md](./04-operations.md)。
