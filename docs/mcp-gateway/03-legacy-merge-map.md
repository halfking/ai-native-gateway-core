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
| 3 | Capability 路由（`?capability=` → `X-MCP-Capability` / `X-MCP-Capability-Tools`） | `src/gateway.js` `CAPABILITY_TOOLS` | `mcp/capability.go`（映射表 + 保序清单）+ `mcp/proxy.go`（同头注入） | 等价 | 头名与语义逐字一致；`X-MCP-Capability-Tools` 为 **JSON 数组字符串**；请求级 `?capability=` **覆盖**适配器默认；非法值 ⇒ 400 + -32602（详见 §6.3/§6.4） |
| 3b | 按能力域过滤工具清单（`getCapabilityFromToolsList`） | `src/gateway.js:142`（★**只被导出、请求路径并未调用**） | `mcp/capability.go` `FilterToolsByCapability` | 等价 | 提供同能力 API；**请求路径保持与既有一致**（既有 `?capability=` 不据此过滤 tools/list 响应）⇒ 不把「既有导出」误写成「既有已生效」 |
| 4 | 能力目录（64 条 STATIC_TOOLS：分层 L0–L6、组、cost、p99、限流、关键词） | `src/capability-router.js` | `mcp/capability_catalog.json`（**脚本从 Node 源原样导出**）+ `mcp/capability.go` | 等价 | 数据零改写；仅打分增加 CJK bigram 切分（召回增强） |
| 5 | 工具执行策略表（read/write/llm/unknown：缓存 TTL、限流、超时、重试） | `TOOL_POLICIES` | `mcp/capability.go` `ToolPolicy` + `mcp/server.go` `rateLimitFor`/`retriesFor` | 等价 | 限流从固定窗口改为令牌桶（允许突发、长期速率严格） |
| 6 | 工具检索推荐（`recommendTools` + 打分 + 分层统计） | `src/capability-router.js` | `mcp/capability.go` + `/mcp/control/recommend` `/mcp/control/capabilities` | 增强 | 增加 HTTP 入口；CJK bigram 提升中文召回 |
| 7 | API key 限流 60/min + 429 + `Retry-After` | `src/gateway.js` `rateLimits` | `mcp/ratelimit.go`（令牌桶）+ `mcp/server.go` | 增强 | 分 key×类别（read/write/llm）三档 + 适配器全局档；429 的**三件套**（`Retry-After` 头 / `-32002` / `data.retry_after_ms`）同源给出（详见 §6.2） |
| 8 | 上游健康检查 + `/health` 展示上游状态 | `src/gateway.js` `checkUpstreamHealth` | `mcp/circuit.go`（熔断器）+ `/mcp/control/health` | 增强 | 由「健康布尔」升级为「熔断三态」（closed/open/half-open） |
| 9 | Prometheus 指标（`mcp_gateway_requests_total` 等 6 个） | `src/gateway.js` `metrics` | `mcp/metrics.go` | 等价 | 既有 `/metrics` 实际导出 **7** 个族（`requests_total` `rate_limited_total` `upstream_errors_total` `health_check_failures_total` `uptime_seconds` `upstream_healthy` `requests_by_capability`）⇒ 本实现**同名逐字保留**；另加 8 个治理类指标 + `requests_by_method`。★ 既有 `requests_by_path` 是**维护但未导出**的死计数器（既有 `/metrics` 里没有它）⇒ 本实现不移植该死计数，改导出更有区分度的 `requests_by_method` |
| 10 | 工具代理分档限流 + 缓存 TTL + 超时 + 重试 | `src/tool-proxy.js` | `mcp/proxy.go`（超时/重试/SSE 解析）+ `mcp/server.go`（分档限流） | 等价 | 工具结果缓存未做（登记为后续增强，见 §5） |
| 11 | 上游 SSE 响应解析 | `src/tool-proxy.js` | `mcp/proxy.go` `extractSSEData` | 等价 | — |
| 12 | 请求体上限（`MCP_ALLOW_BODY_LIMIT`） | `src/gateway.js` | `mcp/server.go` `MaxBodyBytes`（默认 1MB） | 等价 | — |

## 2. 凭证与执法能力

| # | 既有功能 | 既有位置 | 新落点 | 状态 | 差异说明 |
|---|---|---|---|---|---|
| 13 | 「key × 工具」允许表执法（不在允许集 → 403，JSON-RPC `-32003`） | `src/tool-allow.js` | `mcp/policy.go` `keyAllowsTool` + `mcp/server.go` | 等价 | 错误码 `-32003` 保持不变；deny-wins；空允许表＝不收口 |
| 14 | 允许表执法审计（环形缓冲 + `X-Gateway-Admin` 门） | `/admin/tool-allow-audit` | `/mcp/control/audit`（统一审计面）+ `tool_allow_checks_total` 指标 | 增强 | 审计并入统一可查询/可导出体系 |
| 15 | 一 key 多后端单边禁用（`bindings[backend]=false` → 403；inactive → 401） | `src/bindings.js` | `mcp/policy.go` `bindingEnabled` + `mcp/server.go` | 等价 | 同语义：未登记后端＝放行；禁用＝403。**另补齐**：`allowed_adapters` 白名单此前只在 tools/list 生效，直呼工具名可绕过 ⇒ 现已在调用路径上执行（治理链第 6 闸，见 `01-design.md`） |
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

## 6. 协议契约逐字对齐（错误码 / 头格式）

既有实现的客户端、监控与告警规则都按**具体错误码**和**具体响应头**判分支，
所以「语义一致」不等于「契约一致」。本节逐条登记本实现采用的字面值及其来源。

### 6.1 JSON-RPC 错误码

| 场景 | HTTP | JSON-RPC code | 附加 data | 既有实现出处 |
|---|---|---|---|---|
| 工具不在「key × 工具」允许集 | 403 | **-32003** | — | `src/tool-allow.js:23`、`src/tool-proxy.js:219` |
| 后端绑定被单边禁用（`bindings[backend]=false`） | 403 | **-32003** | — | `src/bindings.js` |
| 触发限流 | 429 | **-32002** | `retry_after_ms`（毫秒） | `src/gateway.js:495-528`、`src/tool-proxy.js` |
| 非法 `?capability=` | 400 | **-32602** | — | `src/gateway.js:519-527` |
| 上游不可用 / 代理失败 | 502 | **-32000** | — | `src/gateway.js:228/547/559`、`src/tool-proxy.js:299/402` |
| 工具不存在或不可见 | 404 | **-32001**（★新增） | — | 既有实现无此分支（找不到工具由上游报错） |
| 敏感工具缺审批凭据 | 403 | **-32004**（★新增） | — | 既有实现无审批层 |
| 熔断器打开 | 503 | **-32006**（★新增） | — | 既有实现无熔断 |
| 上游超时 | 502 | **-32007**（★新增） | — | 既有实现未区分超时 |

★ 新增码只占用既有实现**未使用**的取值；已占用的 -32000 / -32002 / -32003
**逐字沿用**（见 `mcp/errors.go` 的逐条注释）。

### 6.2 429 的完整契约

既有实现的 429 是「三件套同源」，缺任何一件都会被既有客户端判成异常响应：

```
HTTP/1.1 429 Too Many Requests
Retry-After: 1                       ← 秒，向上取整（ceil(ms/1000)），最小 1
{"jsonrpc":"2.0","error":{
   "code":-32002,
   "message":"Rate limit exceeded",
   "data":{"retry_after_ms":1234}}}  ← 毫秒，与 Retry-After 同源
```

本实现在 `mcp/server.go`（`errorHeaders` / `retryAfterSeconds` / `retryAfterMillis`）
里由**同一个等待时长**同时推导头与 data，保证两者永不失配。

★ `data` 还带 **`limit`**（适用的每分钟限额）—— 与既有 `tool-proxy.js:245` 的
`data: { retry_after_ms, limit }` 一致（既有 `gateway.js:502` 只带
`retry_after_ms`）⇒ 本实现取**两者的并集**，既有客户端按任一来源读都拿得到。

★ **message 的既有差异（已核对，非本实现引入）**：既有两条路径的文案本来就
不一样 —— `gateway.js` 是 `Rate limit exceeded`；`tool-proxy.js` 是
`Rate limit exceeded for tool ${tool.name} (${cls})`。⇒ **message 不是跨路径
稳定契约**，稳定契约是**错误码 -32002 + `Retry-After` 头 + `data.retry_after_ms`**
三者。本实现统一取 `gateway.js` 的那句，工具名与类别放在 `data`（`limit`）
与审计事件里，避免把「文案」当成可依赖的判据。

### 6.3 非法 `?capability=` 的文案

```
HTTP/1.1 400 Bad Request
{"jsonrpc":"2.0","error":{"code":-32602,
 "message":"Unknown capability: bogus. Valid: code, memory, agent, wiki, apikey, federation, reasoning"}}
```

★ 合法能力域清单的**顺序**与既有实现一致（= 既有 `CAPABILITY_TOOLS` 的键插入
顺序）。Go 的 map 迭代顺序随机 ⇒ 本实现在 `mcp/capability.go` 里用
`capabilityOrder` 显式保序，避免同一错误的文案每次都变。

### 6.4 能力路由头

| 头 | 值 | 说明 |
|---|---|---|
| `X-MCP-Capability` | 能力域名 | **请求级 `?capability=` 覆盖适配器默认**（既有实现是把头写在入站请求上再透传，等价于请求级优先） |
| `X-MCP-Capability-Tools` | **JSON 数组字符串**，如 `["code_generate","code_review",…]` | 既有实现是 `JSON.stringify([...])` ⇒ 上游按 `JSON.parse` 解析 ⇒ 逗号分隔会让上游解析失败 |

★ 头名大小写由 Go 规范化为 `X-Mcp-Capability` / `X-Mcp-Capability-Tools`；
HTTP 头名本身大小写不敏感（RFC 9110 §5.1）⇒ 语义与既有实现一致。

★ `mcp_gateway_requests_by_capability` 只对**通过合法性校验**的能力域计数
（既有实现也是先判合法再计数）。

### 6.5 capability 面的完整边界（避免「导出 ≠ 生效」的误判）

既有 `src/gateway.js` 的 capability 相关面共有**三处**，逐条对齐：

| 既有位置 | 既有是否在请求路径生效 | 本实现 |
|---|---|---|
| `gateway.js:510-528`（`?capability=` 校验 / 计数 / 注入请求头） | **生效** | 逐字一致（§6.3/§6.4） |
| `gateway.js:142` `getCapabilityFromToolsList`（按能力域过滤工具清单） | ★**只被导出、请求路径并未调用** | 提供同能力 API `FilterToolsByCapability`；**请求路径不据此过滤**（保持逐字一致） |
| `capability-router.js` 的 64 条 `STATIC_TOOLS` + 打分 + 分层统计 | 生效 | `capability_catalog.json`（脚本从 Node 源原样导出）+ `mcp/capability.go` |

★ 为什么要专门登记这一行：把「既有导出了但没接上」的能力写成「既有已生效」，
会让后来者以为本网关少了一块功能；反过来把它当没这回事，又会漏移植。⇒
**能力已移植（API 在），但生效范围与既有一致（请求路径不过滤）。**

## 7. 既有「执法在路径」两道门的登记

既有仓有**两道**门各管一件事，**互不替代**：

| 门 | 既有位置 | 回答的问题 | 本轮处理 |
|---|---|---|---|
| **拓扑门** | `scripts/audit-enforcement-in-path.js` | 「带工具级执法的服务，在**生产 MCP 流量的路径**上吗？」（读 nginx upstream 的主/备次序） | **前提已变 ⇒ 本仓不适用**（登记，不移植） |
| **运行时门** | `scripts/probe-tool-allow-enforcement.js` | 「对这条路径真打一次不该被允许的调用，它**真的被拒**了吗？」 | **已移植** ⇒ `tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh`，并作为 **S20** 纳入三层 harness |

### 7.0 资产清单（逐个登记，含台账、基线与测试）

★ 这一节回答「它们是不是**网关功能**、要不要跟着搬过来」。判定标准只有一条：
  **该资产回答的问题，在本实现里是否还存在**。

| # | 资产 | 既有位置 | 它回答的问题 | 本仓是否还存在这个问题 | 处置 |
|---|---|---|---|---|---|
| 1 | 拓扑门脚本 | `scripts/audit-enforcement-in-path.js` | 「谁在前面」（nginx upstream 主/备次序） | **不存在**（执法收敛到网关单点） | **登记不移植**（§7.1） |
| 2 | 拓扑门测试 | `test/audit-enforcement-in-path.test.js` | 同上（含台账漂移/未验证判定） | 同上 | **登记不移植** |
| 3 | 运行时探针脚本 | `scripts/probe-tool-allow-enforcement.js` | 「有没有真的拒」（三档结论 + allow 正控） | **存在**（这是网关**功能**的自证） | **已移植** ⇒ `tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh`（S20） |
| 4 | 运行时探针测试 | `test/probe-tool-allow-enforcement.test.js` | 探针自身的三档判别力 | 同上 | **以等价方式承接**：不移植 Node 测试文件，而是**分别实跑三档**（0/1/2）验证判别力（§7.2） |
| 5 | 事实台账 | `docs/enforcement-path-ledger.md` | 「我们以为在收口的那道门，真的拦得住生产流量吗？」（事实登记，每次拓扑变更都要改） | **前提已变**（台账记的是「执法点在 nginx 里是 backup ⇒ 生产事实上没有工具级强制力」这一已知暴露） | **登记不移植**：该暴露描述的是**旧拓扑**下的事实；本实现里执法必然在路径上 ⇒ 台账记的暴露不再成立 |
| 6 | 台账基线 | `enforcement-path.baseline.json` | 「已知暴露」的可进 CI 基线（`expect: standby_only`） | 同上（基线值指向旧主备拓扑） | **登记不移植**：连同其 `gates`（拓扑门/运行时门）映射一并登记在此，避免台账失联 |
| 7 | 网关契约自测 | `test/gateway.test.js` | 429 的 `-32002`、能力头 `x-mcp-capability-tools`、非法能力域的 `Unknown capability`（3 处契约断言） | 存在 | **由 Go 侧承接** ⇒ `mcp/capability_routing_test.go` 逐条对应（§7.3） |
| 8 | 执法契约自测 | `test/tool-allow.test.js` | 工具拒绝码 `-32003`（4 处断言，含批量语义） | 存在 | **由 Go 侧承接** ⇒ `mcp/capability_routing_test.go`（§7.3） |

★ 结论：8 项资产中 **2 项移植**（运行时探针脚本 + 其判别力验证）、**2 项由 Go 侧
契约测试承接**（既有 Node 契约自测）、**4 项登记不移植**（拓扑门脚本/测试 + 台账 +
基线 —— 它们是**针对旧 nginx 主备拓扑的生产诊断资产**，不是网关功能面，且其
前提在本实现里已消失）。

### 7.1 拓扑门 / 台账 / 基线为什么在本仓不适用

这三项成立的前提是「执法点（mcp-gateway）与业务上游是 nginx 的主/备两个
upstream，主健康时执法那台不接流量」。本实现的拓扑前提已经变了：

- 工具级执法收敛到网关**单点**（数据面 8 闸的第 5 闸「key × 工具 允许表」），
  不再存在「执法点 vs 业务上游」的主备之争；
- 上游适配器由控制面注册（`/mcp/control/adapters`），网关**必然**在调用路径上；
- 台账/基线记录的那条「已知暴露」（`expect: standby_only` ⇒ 生产事实上没有
  工具级强制力）在本实现里**不再成立** —— 执法点就是网关本身。

⇒ 「谁在前面」这个问题不再成立，拓扑门失去判别力；台账与基线记的是**旧拓扑
下的事实**，把它们搬过来反而会让后来者误判当前状态 ⇒ **一律登记不移植**，
但在此逐条写明前提为何消失，避免后来者以为漏掉了。

### 7.2 运行时门：移植后的口径

移植到 `tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh`，保持
**三档结论**与既有实现逐字一致：

| 观察 | 结论 | 退出码 |
|---|---|---|
| 不在允许集的工具 ⇒ **403**，且允许集内的工具 ⇒ **200** | 执法在生效 | **0** |
| 不在允许集的工具被放行 | 执法没生效 | **1** |
| 其余（连不上 / 没给 key / **两个工具都被拒**） | **未验证**（「查不了」≠「查过了没问题」） | **2** |

★ 保留「allow 工具这一档」的原因：若两个工具**都**被拒，说明的是「这把 key
全被禁」或「这条路径不认这把 key」，**不是**执法在生效 ⇒ 只看 deny 会把
两种情况混成一种。

★ 既有 `test/probe-tool-allow-enforcement.test.js` 断言的是「探针本身会不会
误判」。本轮**不以移植测试文件的方式承接**，而是**分别实跑三档**（deny 拒 +
allow 通 ⇒ 0；deny 放行 ⇒ 1；两侧皆拒 ⇒ 2），确认判别力未因移植而失真 ——
读数见 `05-verification-report.md` §5.1。

★ 与既有 Node 探针的**唯一差异**：新增 `--allow-args` / `--deny-args`
（默认 `{}`）。原因是本实现会按上游工具的 `inputSchema` 校验参数，而既有
网关不做 Schema 校验 ⇒ 「允许集内的工具」必须给合法入参，否则它会被参数
校验拒掉（403），把「执法在生效」误判成「两边都被拒」。

### 7.3 既有 Node 契约自测 → 本轮 Go 侧承接对照

| 既有断言（位置） | 本轮对应（位置） |
|---|---|
| 限流错误码 `-32002`（`test/gateway.test.js:180`） | `TestRateLimitCarriesRetryAfter`（`mcp/capability_routing_test.go`） |
| 能力头含 `code_generate`（`test/gateway.test.js:195`） | `TestRequestCapabilityOverridesAdapter`（逐项比对 JSON 数组内容） |
| 非法能力域文案含 `Unknown capability`（`test/gateway.test.js:204`） | `TestUnknownCapabilityRejected`（逐字比对 `Unknown capability: X. Valid: ...`） |
| 工具拒绝码 `-32003`（`test/tool-allow.test.js:321/336/347/348`） | `TestToolNotAllowedCodeMatchesLegacy` |
