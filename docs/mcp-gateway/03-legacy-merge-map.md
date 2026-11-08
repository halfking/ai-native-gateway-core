# 合并映射：既有 tools/mcp-gateway → llm-gateway-go 内置 MCP 网关

> 日期：2026-11-09
> 既有实现：`tools/mcp-gateway`（Node.js，独立仓，部署于 184 k3s :8088）
> 新实现：`mcp/`（Go 包）+ `cmd/mcp-gateway`（独立进程）+ `cmd/gateway/mcp_gateway.go`（随主网关挂载）

本表逐条列出既有仓**已完成的功能**及其落点，确保「不遗漏已经完成的功能」。
「状态」列口径：**等价** = 语义与行为一致；**增强** = 保留原语义并扩展；**替代** = 用新机制
覆盖原语义（说明差异）。

## 1. 数据面能力

| # | 既有功能 | 既有位置 | 新落点 | 状态 | 差异说明 |
|---|---|---|---|---|---|
| 1 | 多上游路径分流（`/acc/` `/memora/` `/trendradar/` `/crm/` `/brandmind/`） | `conf.d/default.conf` + Nginx | `mcp.Adapter`（具名适配器 + 工具命名空间） | 替代 | 路径前缀 → 具名注册项；工具名 `<adapter>__<tool>` 消歧；不再依赖 Nginx 前缀约定 |
| 2 | 智能路由层入口 :8088（health / metrics / routes） | `src/gateway.js` | `mcp/http.go` + `mcp/control.go`（`/health` `/healthz` `/mcp/control/routes` `/metrics`） | 等价 | 端口不再独立，随主网关或独立进程 |
| 3 | Capability 路由（`?capability=` → `X-MCP-Capability` / `X-MCP-Capability-Tools`） | `src/gateway.js` `CAPABILITY_TOOLS` | `mcp/capability.go`（映射表）+ `mcp/proxy.go`（同头注入） | 等价 | 头名与语义逐字一致 |
| 4 | 能力目录（64 条 STATIC_TOOLS：分层 L0–L6、组、cost、p99、限流、关键词） | `src/capability-router.js` | `mcp/capability_catalog.json`（**脚本从 Node 源原样导出**）+ `mcp/capability.go` | 等价 | 数据零改写；仅打分增加 CJK bigram 切分（召回增强） |
| 5 | 工具执行策略表（read/write/llm/unknown：缓存 TTL、限流、超时、重试） | `TOOL_POLICIES` | `mcp/capability.go` `ToolPolicy` + `mcp/server.go` `rateLimitFor`/`retriesFor` | 等价 | 限流从固定窗口改为令牌桶（允许突发、长期速率严格） |
| 6 | 工具检索推荐（`recommendTools` + 打分 + 分层统计） | `src/capability-router.js` | `mcp/capability.go` + `/mcp/control/recommend` `/mcp/control/capabilities` | 增强 | 增加 HTTP 入口；CJK bigram 提升中文召回 |
| 7 | API key 限流 60/min + 429 + `Retry-After` | `src/gateway.js` `rateLimits` | `mcp/ratelimit.go`（令牌桶）+ `mcp/server.go` | 增强 | 分 key×类别（read/write/llm）三档 + 适配器全局档 |
| 8 | 上游健康检查 + `/health` 展示上游状态 | `src/gateway.js` `checkUpstreamHealth` | `mcp/circuit.go`（熔断器）+ `/mcp/control/health` | 增强 | 由「健康布尔」升级为「熔断三态」（closed/open/half-open） |
| 9 | Prometheus 指标（`mcp_gateway_requests_total` 等 6 个） | `src/gateway.js` `metrics` | `mcp/metrics.go` | 等价 | 指标名逐字保留；另加 8 个治理类指标 |
| 10 | 工具代理分档限流 + 缓存 TTL + 超时 + 重试 | `src/tool-proxy.js` | `mcp/proxy.go`（超时/重试/SSE 解析）+ `mcp/server.go`（分档限流） | 等价 | 工具结果缓存未做（登记为后续增强，见 §5） |
| 11 | 上游 SSE 响应解析 | `src/tool-proxy.js` | `mcp/proxy.go` `extractSSEData` | 等价 | — |
| 12 | 请求体上限（`MCP_ALLOW_BODY_LIMIT`） | `src/gateway.js` | `mcp/server.go` `MaxBodyBytes`（默认 1MB） | 等价 | — |

## 2. 凭证与执法能力

| # | 既有功能 | 既有位置 | 新落点 | 状态 | 差异说明 |
|---|---|---|---|---|---|
| 13 | 「key × 工具」允许表执法（不在允许集 → 403，JSON-RPC `-32003`） | `src/tool-allow.js` | `mcp/policy.go` `keyAllowsTool` + `mcp/server.go` | 等价 | 错误码 `-32003` 保持不变；deny-wins；空允许表＝不收口 |
| 14 | 允许表执法审计（环形缓冲 + `X-Gateway-Admin` 门） | `/admin/tool-allow-audit` | `/mcp/control/audit`（统一审计面）+ `tool_allow_checks_total` 指标 | 增强 | 审计并入统一可查询/可导出体系 |
| 15 | 一 key 多后端单边禁用（`bindings[backend]=false` → 403；inactive → 401） | `src/bindings.js` | `mcp/policy.go` `bindingEnabled` + `mcp/server.go` | 等价 | 同语义：未登记后端＝放行；禁用＝403 |
| 16 | 绑定执法审计（环形缓冲 + 管理端点） | `/admin/bindings-audit` | `/mcp/control/audit` + `bindings_checks_total` | 增强 | 同上 |
| 17 | 远端账本不可达 ⇒ fail-open（放行 + 审计） | `src/bindings.js` / `src/tool-allow.js` | `mcp/ledger.go` `RemoteLedger` | 等价 | fail-open 口径逐字保留（含「key 不在账本 ⇒ 放行」） |
| 18 | 凭证引导 apply（免认证 + 幂等 + IP/设备双维限流 + 策略自动批准） | `src/keys.js` | `mcp/bootstrap.go` `Apply` | 等价 | 限流维度、幂等 TTL（15min）、先限流后幂等的顺序全部保留；默认**关闭**，需显式开启 |
| 19 | 凭证引导 status（无明文、无 token） | `src/keys.js` | `mcp/bootstrap.go` `Status` | 等价 | — |
| 20 | 凭证引导 claim（一次性领取明文 + bindings，不缓存不重放） | `src/keys.js` | `mcp/bootstrap.go` `Claim` | 等价 | 领取后立即清空明文；二次领取返回 `already_claimed` |
| 21 | 免认证面读取限流（status/claim 每 IP 每分钟） | `src/keys.js` | `mcp/bootstrap.go` `readIP` | 等价 | — |

## 3. 运维与可观测

| # | 既有功能 | 既有位置 | 新落点 | 状态 | 差异说明 |
|---|---|---|---|---|---|
| 22 | 路由清单 `/gateway/routes`（需 Authorization） | `src/gateway.js` | `/mcp/control/routes`（需管理令牌） | 等价 | 输出适配器 + 工具 + 元数据（可见性/敏感度/分组） |
| 23 | 结构化日志（audit 事件逐条 JSON） | `console.log(JSON.stringify(...))` | `mcp/audit.go`（环形缓冲 + 可选 JSONL 落盘） | 增强 | 落盘后可长期留存；导出 API 供 SIEM |
| 24 | WebSocket relay（实时推送 `/ws/`） | `ws-relay/index.js` | — | **未移植** | 见 §5：属实时推送通道，与「网关治理」正交，本轮不做 |
| 25 | Nginx 侧限流（`limit_req`） | `conf.d/default.conf` | 网关内令牌桶 | 替代 | 治理收敛到网关单点，避免两处口径漂移 |

## 4. 新增能力（既有仓没有）

| # | 能力 | 落点 | 对标 |
|---|---|---|---|
| 26 | 数据面/控制面分离 + 逐请求无状态路由 | `mcp/server.go`、`mcp/proxy.go` | microsoft/mcp-gateway |
| 27 | 工具命名空间化（`<adapter>__<tool>`） | `mcp.NormalizeToolName` | MCPJungle |
| 28 | 工具分组视图（`?group=`） | `mcp/server.go` | MCPJungle tool-groups |
| 29 | 两层工具授权（可见性 private/team/public + scope 动作位） | `mcp/policy.go` | IBM ContextForge |
| 30 | 敏感度分级（read/write/high）+ 分级审批凭据 | `mcp/types.go`、`mcp/server.go` `checkApproval` | PRD FR-A/FR-S |
| 31 | 参数白名单 + JSON Schema 校验 | `mcp/policy.go` `validateParams` | ContextForge schema_guard |
| 32 | 出参脱敏（凭据 + PII） | `mcp/redact.go`（复用仓内 `domains/secretmask`） | lasso-security/mcp-gateway |
| 33 | 审批工作流（创建/裁决/过期） | `/mcp/control/approvals*` | PRD FR-A |
| 34 | 凭证签发/轮换/吊销（明文一次性、摘要存储） | `mcp/auth.go` | PRD FR-07 |
| 35 | 智能体与技能注册（模型+允许工具 / manifest+绑定工具） | `mcp/types.go`、`/mcp/control/agents|skills` | PRD FR-01 |
| 36 | RFC 9728 受保护资源元数据 | `mcp/http.go` | IBM ContextForge / 规范族 |
| 37 | 运营看板（自包含 HTML，无前端构建链） | `mcp/dashboard.go` | PRD FR-11 |
| 38 | 审计导出（NDJSON，SIEM 友好） | `/mcp/control/audit/export` | PRD FR-11 |
| 39 | 熔断器（按适配器三态） | `mcp/circuit.go` | ContextForge circuit_breaker |
| 40 | 独立进程形态（边缘部署） | `cmd/mcp-gateway` | — |

## 5. 明确未移植（登记为后续增强，不隐瞒）

| 项 | 原因 | 后续轮次建议 |
|---|---|---|
| WebSocket relay（`ws-relay/`） | 实时推送通道，与「工具面治理」正交；主网关已有自己的实时通道 | 需要「网关状态实时推送」时单独立项 |
| 工具结果缓存（TOOL_POLICIES.cacheTTL） | 缓存一致性需要按工具逐个定义失效语义；先保证正确性 | 下一轮按「只读工具 + 短 TTL」试点 |
| Nginx `limit_req` 侧限流 | 治理收敛到网关单点，避免双口径 | 已由网关令牌桶覆盖 |
| 上游 stdio 传输 | 本轮只接 Streamable HTTP 形态上游 | 需要本地进程型 MCP 时接入 |
| 出向 OAuth 2.1 代理（3LO/OBO/RFC 8693） | 需要密钥库与 IdP 侧配合 | 与企业 IdP 接入轮次一起做 |
| CEL 策略引擎 | 先用「可见性 + 允许表 + 审批」三层覆盖常见场景 | 需要条件化授权时引入 |
