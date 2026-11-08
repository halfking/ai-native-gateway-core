# MCP 网关验证报告（Maestro 方式）

> 日期：2026-11-09（首轮）＋ 2026-11-09 契约对齐轮（本文档已含两轮读数）
> 执行入口：`bash tests/mcp-gateway-maestro/run.sh`
> 报告产物：`tests/mcp-gateway-maestro/reports/<时间戳>/summary.md`（不入库，`.gitignore` 已登记）
> ★ 产物自带证据：`summary.md` 除计数外还写入**逐条用例读数**（每条 ok/bad 的实际
>   观测值）⇒ 复核者只读该文件即可取到证据，不必重跑或翻控制台。

## 1. 结论

**通过 30 / 失败 0**（L1 Go 测试 1 例 + L2 API 场景 28 例 + L3 Maestro 浏览器 flow 1 例）。

> 首轮读数：24/24 全绿（当时 L2 为 S01–S19）。
> 契约对齐轮：新增 S20（工具级强制力运行时探针）后为 **25/25 全绿**。
> 契约运行时可观测轮：新增 S10b / S16b / S21 / S22 / S22b（把被点名的
> **错误码、429 三件套、请求级能力路由与能力头格式**做成**真实进程里的可观测场景**）
> 后为 **30/30 全绿**。

## 2. 三层验证口径

| 层 | 内容 | 手段 | 读数 |
|---|---|---|---|
| L1 | 治理链单测/集成测 | `go test ./mcp/...` | 全绿 |
| L2 | 真实进程 + 真实 HTTP | 脚本内 S01–S22 | 28/28 通过 |
| L3 | 浏览器逐页断言 | `maestro/flows/10-mcp-gateway.yaml` | rc=0，FAILED=0 |

另：`go build ./...` rc=0；`go vet ./mcp/... ./cmd/mcp-gateway/... ./middleware/...` rc=0；
`gofmt -l mcp/ middleware/auth_mw.go cmd/mcp-gateway/` **零输出**；
`go test ./mcp/...` 与 `go test ./middleware/...` 全绿；
`go test ./cmd/gateway/ -run TestRegisterMCPGateway` 全绿（主网关接线，3 个用例）。

## 3. L2 用例明细（S01–S22）

| # | 场景 | 期望 | 实测 |
|---|---|---|---|
| S01 | 无凭证访问数据面 | 401 | 401 ✅ |
| S02 | initialize | 200 + 协议版本 | 200，`2026-07-28` ✅ |
| S03 | tools/list 聚合上游工具 | 命名空间化 `demo__*` | `demo__add_memory demo__search_memory` ✅ |
| S04 | tools/call 转发上游 | 200 | 200 ✅ |
| S05 | 控制面无管理令牌 | 401 | 401 ✅ |
| S06 | 注册适配器 | 201 | 201 ✅ |
| S07 | 注册高敏工具（需审批） | 201 | 201 ✅ |
| S08 | 签发受限凭证 | 201 + 一次性明文 | 201 ✅ |
| S09 | 受限凭证调用允许的工具 | 200 | 200 ✅ |
| S10 | 越权工具（**适配器白名单外**） | 403 | 403 ✅（白名单闸；此前是「审批闸」提前挡下，见 §6-④） |
| S10b | 工具拒绝的 JSON-RPC 码 | **-32003** | -32003 ✅（与既有 `tool-allow` 逐字一致） |
| S11 | 高敏工具缺审批凭据 | 403 | 403 ✅（改用可访问 crm 适配器的凭证 ⇒ 测的确实是审批闸） |
| S12 | 带审批凭据 | 放行 | 放行（上游未就绪 ⇒ 502，治理链已通过） ✅ |
| S13 | 吊销凭证后调用 | 401 | 401 ✅ |
| S14 | 审计查询 | 非空 | 15 条 ✅ |
| S14b | 审计导出 NDJSON | 逐行可解析 | 15 行 ✅ |
| S15 | RFC 9728 受保护资源元数据 | 含 `resource` | 含 ✅ |
| S16 | 读类限流 1/min | 第 1 次 200、第 2 次 429 | 200 / 429 ✅ |
| S16b | **429 三件套同源** | `Retry-After` 头 + 码 -32002 + `data.retry_after_ms` + `data.limit` | `Retry-After=60s`、码 -32002、`retry_after_ms=59988`、`limit=1` ✅ |
| S17 | 引导面 apply（自动批准） | approved | approved ✅ |
| S17b | 幂等重放 | 同一 requestId | 一致 ✅ |
| S18 | 引导面 status | 无明文 | 无明文 ✅ |
| S19 | 引导面 claim | 一次性领取明文 | 领取成功 ✅ |
| S19b | 二次 claim | 不可重放 | `already_claimed` ✅ |
| S20 | 工具级强制力运行时探针 | 三档判别 | **执法在生效（exit 0）** ✅ |
| S21 | 非法请求级能力域 | 400 + 码 -32602 + 文案含合法清单 | 400 / -32602 / `Unknown capability: bogus. Valid: code, memory, agent, wiki, apikey, federation, reasoning` ✅ |
| S22 | 请求级能力域**覆盖**适配器默认 | 上游收到的 `X-MCP-Capability` = 请求级值 | 上游回显 `code`（适配器默认是 `memory`）✅ |
| S22b | 能力工具头格式 | **JSON 数组字符串**（上游可 `JSON.parse`） | 可 `JSON.parse`，首项 `code_generate` ✅ |

## 4. L3 Maestro flow（运营看板）

断言点：看板标题 → 六个统计卡 → 上游适配器表格（含 `--demo` 预置的 demo/crm 两个
适配器）→ 滚动到「Top 工具（按调用量）」→ 滚动到「最近审计事件」→ 无令牌打开时
必须出现可见错误文案（不能静默空白）。

## 5. 契约对齐轮：5 处证据缺口与补证

宿主验收反馈指认 5 处「既有实现契约有没有被逐字继承」的证据缺口。本轮逐条补齐，
**每条都有可复跑的读数**：

| # | 缺口 | 既有实现的字面契约 | 本轮处置 | 证据 |
|---|---|---|---|---|
| 1 | 错误码 | -32002 限流、-32000 上游错误、-32003 tool-allow/bindings | 限流码 -32008→**-32002**、上游错误码 -32005→**-32000**；新增码只占未使用取值 | `mcp/errors.go` 逐条注释；`mcp/capability_routing_test.go` 断言 -32000 / -32003 |
| 2 | 能力头格式 | `x-mcp-capability-tools = JSON.stringify([...])` | 由逗号分隔改为 **JSON 数组字符串** | `TestRequestCapabilityOverridesAdapter` 断言该头可被 `json.Unmarshal` 成数组且内容逐项相等 |
| 3 | 请求级能力路由 | `?capability=X`：先判合法 → 计数 → 注入头并**覆盖**适配器默认；非法 ⇒ 400 + -32602 + `Unknown capability: X. Valid: ...` | 三件事全部补齐；合法清单用 `capabilityOrder` **显式保序** | `TestUnknownCapabilityRejected`（400/-32602/文案逐字）、`TestValidCapabilitiesOrderIsStable`、`TestRequestCapabilityOverridesAdapter`、`TestAdapterCapabilityUsedWithoutOverride`、`TestInvalidCapabilityNotCounted`、`TestValidCapabilityCounted` |
| 4 | 429 契约 | `Retry-After`(秒,ceil) + -32002 + `data.retry_after_ms`(毫秒) **三者同源**；`tool-proxy.js:245` 的 `data` 还带 `limit` | 由**同一个等待时长**同时推导头与 data；`data` 取既有两处来源的**并集**（`retry_after_ms` + `limit`） | `TestRateLimitCarriesRetryAfter` 断言三者同时存在、秒数与毫秒同源（ceil）、且 `data.limit` 存在 |
| 5 | 既有「执法在路径」资产未登记 | 共 8 项：拓扑门脚本 + 其测试、运行时探针脚本 + 其测试、事实台账、台账基线，以及既有契约自测 `test/gateway.test.js`（429 -32002 / 能力头 / Unknown capability）与 `test/tool-allow.test.js`（-32003） | **逐个登记**（§7.0）：判定标准＝「该资产回答的问题在本实现里是否还存在」。运行时探针**移植**为 `tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh` ⇒ 纳入 S20；既有契约自测**由 Go 侧逐条承接**（§7.3 对照表）；拓扑门脚本/测试 + 台账 + 基线＝**旧 nginx 主备拓扑的生产诊断资产**，前提已消失 ⇒ **登记不移植**并写明理由 | `03-legacy-merge-map.md` §7.0–§7.3；S20 读数 exit 0；探针三档自检 0/1/2 |

### 5.1 探针的三档判别力（自检，不是只跑通一档）

★ 被点名的 4 项契约（错误码 / 429 三件套 / 请求级能力路由 / 能力头格式）原先
  只有 **Go 单测**与**文档行号**两级证据。本轮把它们做成 **L2 真实进程场景**，
  证据直接出现在 harness 输出里（任何复核者重跑即可复现）：

| 契约 | 观察方式 | 读数 |
|---|---|---|
| 工具拒绝码 -32003 | 真实进程调用越权工具后读 JSON-RPC 错误码（S10b） | `-32003` |
| 429 三件套同源 | 真实进程触发限流后读**响应头**与**错误体**（S16b） | `Retry-After=60s` + `-32002` + `retry_after_ms=59988` + `limit=1` |
| 非法能力域拒绝 | 真实进程带 `?capability=bogus` 调用（S21） | `400` + `-32602` + 含合法清单的逐字文案 |
| 请求级能力域覆盖 | **上游回显它实际收到的头**（S22） | 上游收到 `X-MCP-Capability=code`（适配器默认是 `memory`） |
| 能力头格式 | 上游回显头原串后做 `JSON.parse`（S22b） | 可解析成数组，首项 `code_generate` ⇒ 与既有 `JSON.stringify([...])` 逐字一致 |

★ 观测点为什么选「上游回显」：头是网关**写给上游**的，网关自说自话证明不了
  格式对不对；让上游把**实际收到的原串**回吐出来，才证明「上游按 `JSON.parse`
  解析不会失败」这一条真的成立（这正是被指认的那条风险的判据）。

| 场景 | 期望 | 实测 |
|---|---|---|
| deny 工具 403 且 allow 工具 200 | 执法在生效 ⇒ exit **0** | exit 0 ✅ |
| deny 工具被放行 | 执法没生效 ⇒ exit **1** | exit 1 ✅ |
| 两个工具都被拒 | **未验证**（「查不了」≠「查过了没问题」）⇒ exit **2** | exit 2 ✅ |

### 5.2 证据的「可观测级别」：从读代码升级为运行时观测

★ 被点名的 4 项契约（错误码 / 429 三件套 / 请求级能力路由 / 能力头格式）原先
  只有 **Go 单测**与**文档行号**两级证据。本轮把它们做成 **L2 真实进程场景**，
  证据直接出现在 harness 输出里（任何复核者重跑即可复现）：

| 契约 | 观察方式 | 读数 |
|---|---|---|
| 工具拒绝码 -32003 | 真实进程调用越权工具后读 JSON-RPC 错误码（S10b） | `-32003` |
| 429 三件套同源 | 真实进程触发限流后读**响应头**与**错误体**（S16b） | `Retry-After=60s` + `-32002` + `retry_after_ms=59988` + `limit=1` |
| 非法能力域拒绝 | 真实进程带 `?capability=bogus` 调用（S21） | `400` + `-32602` + 含合法清单的逐字文案 |
| 请求级能力域覆盖 | **上游回显它实际收到的头**（S22） | 上游收到 `X-MCP-Capability=code`（适配器默认是 `memory`） |
| 能力头格式 | 上游回显头原串后做 `JSON.parse`（S22b） | 可解析成数组，首项 `code_generate` ⇒ 与既有 `JSON.stringify([...])` 逐字一致 |

★ 观测点为什么选「上游回显」：头是网关**写给上游**的，网关自说自话证明不了
  格式对不对；让上游把**实际收到的原串**回吐出来，才证明「上游按 `JSON.parse`
  解析不会失败」这一条真的成立（这正是被指认的那条风险的判据）。

## 6. 实跑发现并修复的问题（按发现顺序）

| # | 现象 | 根因 | 处置 |
|---|---|---|---|
| 1 | L2 全部 404 | 固定端口 :8799 被本机常驻的 `DeployConfigConsole` 占用，请求静默串台 | 改为**随机空闲端口**；启动后校验演示凭证是否取到，取不到即判失败（不再静默串台） |
| 2 | L1 `exec format error` | 容器内交叉编译（GOOS=darwin）的测试二进制无法在 linux 容器执行 | 构建带 `GOOS/GOARCH`，测试**不带**；两者拆成两套命令 |
| 3 | 「最近审计事件」判红 | Maestro web flow 的 visible 判定受**视口**约束，折叠线以下内容需先滚动 | 改用 `scrollUntilVisible` 再断言 |
| 4 | 「健康信息加载失败」判红 | 看板把错误文案与 HTTP 详情拼成整串，文本匹配失败 | 拆成稳定文案 + 详情两个元素 |
| 5 | 二次 claim 报 `not_approved` | 领取后记录状态置 claimed，按状态判会误报 | 先判「已领取」再判「未批准」；同时区分 pending（未批准）与 claimed（已领取） |
| 6 | 主网关接线测试期望 401 实得 503 | 控制面在**未配置**管理令牌时返回 503（不可用），测试口径写错 | 测试改双口径：未配置 ⇒ 503、令牌错误 ⇒ 401、令牌正确 ⇒ 200 |
| 7 | **S20 探针首跑 exit 2（未验证：两边都被拒）** | 探针脚本里 `${2:-{}}`：Bash 的参数展开在**第一个** `}` 处结束 ⇒ 默认值变成 `{}` 后又多出一个字面 `}` ⇒ 实际发出的 `arguments` 是 `{"q":"probe"}}` ⇒ JSON 解析失败 ⇒ 参数校验把**允许集内**的工具也拒了 | 改为 `[ -n "${2:-}" ] || X='{}'` 的显式写法；并**单独复测三档**（0/1/2）确认判别力未因修复而失真 |
| 8 | `gofmt -l` 报 `middleware/auth_mw.go` | 首轮插入 `/mcp` 放行项时把 `PathPrefixes` 整块缩进弄乱了 ⇒ **首轮报告里「gofmt 零输出」的结论不成立** | `gofmt -w` 修复（纯缩进，语义不变）；本轮 `gofmt -l` 复检零输出 |
| 9 | 控制面签发凭证时 `allowed_tools` / `scopes` / `allowed_adapters` **不生效** | `KeySpec` 没有 JSON 标签 ⇒ `encoding/json` 只能按 Go 字段名做大小写不敏感匹配 ⇒ `"allowed_tools"` 这种 snake_case 被**静默丢弃**（`scopes` 为空还落回默认档，看起来「能用」其实没生效） | 给 `KeySpec` 补齐与 `Key` 出参一致的 snake_case 标签；S20 探针的「允许集」由此才真正生效 |
| 10 | `allowed_adapters` 白名单可被绕过 | 白名单此前只在 `tools/list` 生效 ⇒ 直呼工具名即可调用别的适配器的工具 | 补到**调用路径**（治理链第 6 闸改为「backend 可达性」＝适配器白名单 + 单边禁用绑定）；S10/S11/S12 改为用**能访问 crm 适配器**的凭证，确保各测各的闸 |
| 11 | 429 的 `data` 少了 `limit` | 逐字比对既有 `tool-proxy.js:245` 时发现：其 `data` 为 `{ retry_after_ms, limit }`（`gateway.js:502` 只有 `retry_after_ms`）⇒ 本实现原先只给 `retry_after_ms` | `data` 取两处来源的**并集**（`retry_after_ms` + `limit`，适配器档另带 `adapter`）；测试断言 `data.limit` 存在 |
| 12 | 既有按能力域过滤工具清单的能力未登记 | 既有 `gateway.js:142` 的 `getCapabilityFromToolsList` **只被导出、请求路径并未调用** | 提供同能力 API `FilterToolsByCapability`（并**保持请求路径与既有一致**：不据此过滤 tools/list）；§6.5 专门登记「导出 ≠ 生效」的边界 |

## 7. 本轮同时收口的安全项

- `/mcp/control/health` 含上游端点与熔断态 ⇒ 改需管理令牌；探针改用公开的
  `/mcp/control/healthz`（只回 `ok`，不泄露拓扑）。
- 控制面未配置管理令牌时整体 503（不是「无鉴权开放」）。
- 免认证引导面默认关闭（`MCP_GATEWAY_BOOTSTRAP_ENABLED=false`）。
- 探针凭据只从环境读（`MCP_API_KEY`）⇒ 不进 argv、不进进程列表与 shell 历史。

## 8. 未覆盖与既存失败（诚实登记）

- `go test ./cmd/gateway/` **全包**仍为红：失败项全部是 `storage_mode_init_test.go`
  的 **lite 存储模式需要 CGO**（本机以 `CGO_ENABLED=0` 构建 SQLite 驱动不可用）
  ⇒ 与 MCP 网关**无关**的**既存**环境性失败；本轮相关用例用定向
  `go test ./cmd/gateway/ -run TestRegisterMCPGateway` 验证通过。
- 上游 stdio / SSE 传输归一化、REST→MCP 虚拟化（未实现）。
- 出向 OAuth 2.1（3LO/OBO）、RFC 8707/7591/8414（未实现）。
- OTel trace、prompt-injection 检测、适配器并发上限执行、工具结果缓存（未实现）。
- 真实企业 IdP / RedClaw 持票联调（未做；需外部系统配合）。
- 多副本部署下的注册面一致性（当前注册面为单进程内存/文件存储，外部账本模式可覆盖）。
- 既有拓扑门 `audit-enforcement-in-path.js` 未移植（前提已变，理由见
  `03-legacy-merge-map.md` §7.1）。
