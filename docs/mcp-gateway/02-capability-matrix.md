# MCP 网关能力矩阵（开源对标 + 实施状态）

> 日期：2026-11-09
> 对标对象与来源见 §1；「本仓状态」口径：**已实现**（本仓有代码且有测试证据）/ **部分**
> （有实现但语义有边界）/ **未做**（明确登记为后续增强）。

## 1. 开源对标（事实与来源）

| 项目 | 仓库 / 文档 | 许可 | 语言 | 定位 |
|---|---|---|---|---|
| Microsoft MCP Gateway | https://github.com/microsoft/mcp-gateway | MIT | C# | K8s 上的 MCP 反向代理 + 管理面；**无状态**按请求路由；adapters/tools CRUD；Agents & Sessions（预览） |
| IBM ContextForge | https://github.com/IBM/mcp-context-forge | Apache-2.0 | Python | 注册中心 + 联邦代理 + 41 个治理插件；协议覆盖最广；RFC 9728 / 8707 / 7591 / 8414 规范族 |
| Docker MCP Gateway | https://github.com/docker/mcp-gateway | MIT | Go | 容器级信任边界 + MCP Catalog/Toolkit |
| MCPJungle | https://github.com/mcpjungle/MCPJungle | MPL-2.0 | Go | 轻量自托管网关；工具规范化命名 `<server>__<tool>`；独立 tool-group 端点 |
| agentgateway | https://github.com/agentgateway/agentgateway | Apache-2.0 | Rust | MCP + A2A + LLM 统一代理；CEL 策略引擎 |
| mcp-gateway-registry | https://github.com/agentic-community/mcp-gateway-registry | Apache-2.0 | Python | nginx 数据面 + FastAPI 控制面；per-user 3LO/OBO 出向鉴权；RFC 8693 |
| lasso mcp-gateway | https://github.com/lasso-security/mcp-gateway | MIT | Python | 插件式护栏：PII/令牌掩码、prompt injection |
| Kong AI MCP Proxy | https://developer.konghq.com/plugins/ai-mcp-proxy/ | 商业（AI Gateway Enterprise 专有插件） | Lua | 把 MCP 接入既有 API 网关插件生态（4 种模式） |
| Azure API Management MCP | https://learn.microsoft.com/azure/api-management/mcp-server-overview | 商业 | — | REST-as-MCP + 外部 MCP server 代理治理 |

> 商业边界说明：Kong 的 `ai-mcp-proxy` 插件与 Azure APIM 的 MCP 能力**不在开源仓库中**
> （Kong OSS 的 `kong/plugins` 只有 `ai-proxy`/`ai-prompt-*` 等 6 个 AI 插件），
> 因此其特性不计入「可自建复现」的基线。

## 2. 基线能力清单（可直接当验收项）

### 2.1 协议与路由

- [x] Streamable HTTP 入口（JSON-RPC 2.0 over HTTP）— `POST /mcp`
- [x] 上游 Streamable HTTP 接入（含 SSE 响应解析）— `mcp/proxy.go`
- [ ] 上游 stdio / SSE 传输归一化 — **未做**（登记）
- [ ] REST/gRPC 虚拟化为工具 — **未做**（marketing 侧已有 http-adapter 骨架）
- [x] 多上游聚合 + 工具命名空间化（`<adapter>__<tool>`）
- [x] 按分组暴露工具子集（`?group=`）
- [x] 无状态路由（协议元数据随请求携带，不依赖 session 亲和）
- [x] 无状态为默认、有状态显式开启 — 本仓只提供无状态（有意取舍，避免多副本粘性问题）

### 2.2 认证与授权

- [x] 入站 API key（`Authorization: Bearer` / `X-Api-Key`）
- [x] 入站 scope 动作位（`mcp.read` / `mcp.execute` / `mcp.admin`）
- [x] RFC 9728 受保护资源元数据（`/.well-known/oauth-protected-resource`）
- [x] 两层工具授权：可见性（private/team/public）+ 动作位（read/execute）
- [x] 「key × 工具」允许表（deny-wins，空表＝不收口）
- [x] 一 key 多后端单边禁用（bindings）
- [x] 外部账本 fail-open（远端不可达 ⇒ 放行 + 审计）
- [x] 凭证签发 / 轮换 / 吊销（明文一次性、摘要存储、吊销即时生效）
- [x] 零凭据引导面（apply 幂等 + IP/设备双维限流 + 自动批准 / 人工批准 / 一次性 claim）
- [ ] RFC 8707 `resource` 参数 + 入站 audience 校验 — **未做**（需要 IdP 侧配合）
- [ ] RFC 7591 动态客户端注册 / RFC 8414 AS 元数据发现 — **未做**
- [ ] 出向 OAuth 2.1（3LO/OBO）、RFC 8693 令牌交换 — **未做**
- [ ] CEL 策略引擎 — **未做**（以三层策略替代）
- [x] 企业 IdP 接入路径：外部账本（ACC 兼容）+ Bearer 凭证由 IdP 侧签发后写入注册面

### 2.3 治理

- [x] 参数白名单
- [x] JSON Schema 入参校验（复用仓内 `santhosh-tekuri/jsonschema/v6`）
- [x] 工具级速率限制（按 key×类别 + 按工具覆盖 + 适配器全局）
- [x] 并发上限（适配器 `MaxConcurrent` 字段已建模，**限流执行未做** — 见下）
- [x] 超时（适配器级 + 工具级覆盖）
- [x] 重试（只读类 1 次，写类不重试）
- [x] 熔断（closed/open/half-open，按适配器）
- [x] 敏感度分级（read/write/high）与分级审批凭据
- [x] 注册期治理（工具元数据可登记：可见性、敏感度、分组、参数白名单）
- [ ] 插件式治理中间件（可插拔 hook 链）— **部分**：治理链按闸位切分，但未提供第三方插件接口

### 2.4 数据安全

- [x] 出参凭据掩码（复用 `domains/secretmask`）
- [x] 出参 PII 掩码（邮箱/手机/身份证/银行卡，可关闭）
- [x] 审计可归因（租户/组织/用户/凭证/智能体/工具/适配器/请求 ID/耗时/结果）
- [x] 审计导出（NDJSON，SIEM 友好）+ 环形缓冲 + 可选 JSONL 落盘
- [ ] Prompt injection 检测 — **未做**（仓内 `domains/promptinjection` 依赖 DB 规则，未接入）

### 2.5 可观测性

- [x] Prometheus（沿用既有 `mcp_gateway_*` 指标名 + 8 个治理指标）
- [x] 结构化审计日志
- [x] 独立健康检查（`/health` 含上游适配器与熔断状态）
- [x] 运营看板（`/mcp/admin`，自包含 HTML）
- [ ] OpenTelemetry trace — **未做**（仓内已有 OTel 依赖，接入留待后续轮次）

### 2.6 多租户与生命周期

- [x] 多租户隔离（租户维资源归属 + 共享资源回退）
- [x] 跨租户同名工具不泄露（`GetToolAnyTenant` 判定）
- [x] 凭证吊销即时生效；离职回收的落点（吊销 API + 外部账本接管）
- [ ] SCIM/HR 事件自动回收 — **未做**（需 IdP 侧）
- [ ] Session 管理（无状态默认）— 本仓只提供无状态

## 3. 已知边界（诚实登记）

| 边界 | 影响 | 处置 |
|---|---|---|
| 注册面默认在内存（未配 `MCP_GATEWAY_STORE_FILE` 时） | 进程重启后凭证与注册项丢失 | 生产必须配置文件路径或接外部账本（`MCP_GATEWAY_LEDGER_URL`） |
| 适配器并发上限字段已建模但未执行 | 高并发下上游可能被压 | 下一轮接入（与令牌桶同层） |
| 工具结果缓存未实现 | 读类工具重复调用会打到上游 | 已登记（见 03-legacy-merge-map §5） |
| 审批凭据通过请求头传递 | 需要客户端配合改造 | 与审批队列（RedClaw/ACC）集成后由 SDK 注入 |
| 出参脱敏是模式匹配 | 非规范形态的凭据可能漏网 | 与仓内 `secretmask` 同级风险，已在代码注释中声明 |

## 4. 验证矩阵

| 层 | 内容 | 入口 |
|---|---|---|
| L1 | Go 单测/集成测（治理链、存储、策略、账本、控制面、看板） | `go test ./mcp/...` |
| L2 | 真实进程 + 真实 HTTP 的 API 场景（S01–S22） | `bash tests/mcp-gateway-maestro/run.sh` |
| L3 | 浏览器逐页断言（运营看板） | `maestro/flows/10-mcp-gateway.yaml` |
