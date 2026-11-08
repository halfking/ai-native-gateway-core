# llm-gateway-go — AGENTS.md（维护者交接）

> 适用：未来任何 cold-start 切到本仓库的助手会话（不区分会话身份）。
> 写入时机：2026-10-09 首次生成；**2026-10-11 由 root 会话
> `mvs_d10f8ded3e7c4d73b8221fe670ddd9dc` 重写「工具链」与「已落地结论」两节**，
> 原文件关于「go/brew/docker全部缺失」的判断已被实测推翻，见下。
> **2026-11-09 MCP 服务网关内置轮（goal 会话）**：
> ★ 用户目标＝「学习微软及其它公司开源的 MCP 服务网关特性，结合附件方案（JFMCP PDF ＋
>   marketing 侧 `docs/jfmcp-gateway/` 方案族），把智能体/技能/MCP 的网关功能做成可用的
>   完整功能；MCP 网关可以放到 llm-gateway-go 中一并提供；**合并前先检查 ~/workspace 下的
>   既有 mcp 网关项目，不遗漏已完成的功能**；完成后用 Maestro 方式全面测试验证 → 修正 →
>   提交推送」。
> ★ 取证与口径变更：附件方案在 `ai-platform/marketing/docs/jfmcp-gateway/`（PDF 为 10 页
>   扫描件、无文本层，已由 00 号文档逐页还原）。该方案 §4 明确写「不在 llm-gateway-go 内
>   实现通用 MCP 工具网关」——**本轮按用户指令改口径**：工具面强制入口做进 llm-gateway-go，
>   marketing 侧保留广场/申请/审批外壳。既有实现＝`ai-native-tools/tools/mcp-gateway`
>   （Node.js，独立进程 :8088，nginx 路径分流 + 能力路由 + key 限流 + bindings/tool-allow
>   执法 + keys 引导面 + Prometheus）。
> ★ 交付（新包 `mcp/`，根包，与 `registry`/`apihub` 同级）：数据面**无状态** JSON-RPC over
>   HTTP（`POST /mcp`，协议元数据随请求携带，不依赖 session 亲和，对齐 microsoft/mcp-gateway）；
>   控制面 `/mcp/control/*`（适配器/工具/智能体/技能/凭证/审批/审计）；治理链 8 闸（身份 →
>   scope → 可见性(private/team/public) → key×tool 允许表(deny-wins) → backend 绑定 →
>   敏感度审批凭据 → 参数白名单+JSON Schema → 令牌桶限流/熔断）；凭证签发·轮换·吊销（明文
>   只返回一次、只存 SHA-256）；零凭据引导面 apply/status/claim（移植既有 D2/D3 语义：幂等 +
>   IP/设备双维限流 + 自动/人工批准 + 一次性领取）；审计环形缓冲 + JSONL 落盘 + NDJSON 导出；
>   出参脱敏（复用仓内 `domains/secretmask` + PII 掩码）；运营看板 `/mcp/admin`（自包含 HTML，
>   无前端构建链）；RFC 9728 受保护资源元数据。指标沿用既有 `mcp_gateway_*` 指标名（告警规则
>   可平移），另加 8 个治理指标。独立进程形态 `cmd/mcp-gateway`（含 `--demo` 演示模式）。
> ★ 合并映射：既有仓 25 项已完成功能**逐条落位**（等价/增强/替代），未移植 6 项显式登记
>   （WS relay、工具结果缓存、nginx 侧限流、stdio 上游、出向 OAuth 2.1、CEL）——见
>   `docs/mcp-gateway/03-legacy-merge-map.md`（对应「不遗漏已完成功能」的硬要求）。
> ★ 验证（三层，脚本 `tests/mcp-gateway-maestro/run.sh`）：L1 `go test ./mcp/...` 全绿；
>   L2 真实进程 API 场景 S01–S19 全绿（401 身份 / 命名空间化聚合 / 转发 / 控制面鉴权 /
>   注册 / 签发 / allow-list 越权 403 / 高敏缺审批 403 / 带审批放行 / 吊销即 401 / 审计与
>   导出 / RFC 9728 / 限流 429 / 引导面幂等·安全状态·一次性领取）；L3 Maestro flow
>   `maestro/flows/10-mcp-gateway.yaml` 全绿（运营看板逐段断言 + 无令牌时报错可见）。
>   另：`go build ./...` rc=0、`go vet ./mcp/... ./cmd/mcp-gateway/... ./middleware/...` rc=0、
>   `gofmt -l` 零输出、`go test ./middleware/...` 全绿（鉴权放行表改动无回归）。
> ★ 实跑修掉的 5 个问题（都是「先跑再判」发现的，不是读代码想出来的）：①固定端口 :8799 被
>   本机常驻的 DeployConfigConsole 占用 ⇒ 请求静默串台、断言全红且与被测物无关 ⇒ 改随机空闲
>   端口；②容器交叉编译出的测试二进制 exec format error ⇒ 构建带 GOOS/GOARCH、测试不带；
>   ③Maestro web flow 的 visible 判定受**视口**约束 ⇒ 折叠线以下内容必须 scrollUntilVisible
>   再断言（"最近审计事件"明明在 DOM 里却判红）；④看板错误文案与 HTTP 详情拼成整串导致文本
>   匹配失败 ⇒ 拆成稳定文案 + 详情两段；⑤claim 语义：领取后二次领取应报 already_claimed，
>   原实现按状态判会误报 not_approved。另修一处安全收口：`/mcp/control/health` 含上游端点与
>   熔断态 ⇒ 改需管理令牌，探针改用公开的 `/mcp/control/healthz`。
> ★ 明确没做（登记在 `docs/mcp-gateway/02-capability-matrix.md` §3）：上游 stdio/SSE 归一化、
>   REST→MCP 虚拟化、RFC 8707/7591/8414 规范族、出向 3LO/OBO、OTel trace、prompt-injection
>   检测、适配器并发上限执行、工具结果缓存、OAuth 出向代理。
>
> **2026-11-09 MCP 网关「既有实现逐字对齐」修补轮（goal 会话）**：
> ★ 背景：上一轮交付后宿主下发的验收反馈指认 **5 处证据缺口**（不是新功能，是「既有实现的
>   契约有没有被逐字继承」没拿出证据）⇒ 本轮**只补这 5 处**，不扩功能面。
> ★ 缺口① 错误码：既有 tools/mcp-gateway 已占用 -32002（限流）、-32000（上游错误）、-32003
>   （tool-allow / bindings 拒绝）；本轮把限流码 -32008→**-32002**、上游错误码 -32005→
>   **-32000**，并在 `mcp/errors.go` 逐条注释出处；新增码（-32001 工具不存在、-32004 审批缺失、
>   -32006 熔断、-32007 超时）只占**既有实现未使用**的取值。
> ★ 缺口② 能力头格式：既有实现 `x-mcp-capability-tools = JSON.stringify([...])` ⇒ 本轮把
>   `X-MCP-Capability-Tools` 由逗号分隔改为 **JSON 数组字符串**（上游按 `JSON.parse` 解析，
>   逗号分隔会解析失败）；头名大小写由 Go 规范化（HTTP 头名大小写不敏感，RFC 9110 §5.1）。
> ★ 缺口③ 请求级 capability 路由：既有 `?capability=X` 会校验合法性（非法 ⇒ 400 + -32602 +
>   `Unknown capability: X. Valid: ...`）、**只对合法能力域计数**、把能力头注入上游并
>   **覆盖**适配器默认 ⇒ 本轮补齐这三件事；合法清单用 `capabilityOrder` 显式保序（Go map
>   迭代顺序随机，否则同一错误的文案每次都变 ⇒ 既有客户端无法做稳定断言）。
> ★ 缺口④ 429 契约：既有 429 是「`Retry-After` 头(秒, ceil) + -32002 + `data.retry_after_ms`
>   (毫秒)」**三件套同源** ⇒ 本轮由**同一个等待时长**同时推导头与 data（`errorHeaders` /
>   `retryAfterSeconds` / `retryAfterMillis`），保证两者永不失配。
> ★ 缺口⑤ 既有「执法在路径」资产的**逐个登记**（`03-legacy-merge-map.md` §7.0）：
>   既有仓共 8 项相关资产 —— 拓扑门脚本 `scripts/audit-enforcement-in-path.js`
>   ＋其测试 `test/audit-enforcement-in-path.test.js`、运行时探针脚本
>   `scripts/probe-tool-allow-enforcement.js` ＋其测试
>   `test/probe-tool-allow-enforcement.test.js`、事实台账
>   `docs/enforcement-path-ledger.md`、台账基线 `enforcement-path.baseline.json`，
>   以及承载契约断言的 `test/gateway.test.js`（429 -32002 / 能力头 /
>   Unknown capability 三处）与 `test/tool-allow.test.js`（-32003 四处）。
>   判定标准＝**该资产回答的问题在本实现里是否还存在**：
>   · 运行时探针（脚本 + 判别力验证）＝ 网关功能的自证 ⇒ **已移植**为
>     `tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh`，作为 **S20** 纳入
>     三层 harness；既有 Node 探针测试**不以移植测试文件的方式承接**，而是**分别实跑
>     三档**（0 执法生效 / 1 没生效 / 2 未验证）确认判别力未因移植而失真。
>   · 拓扑门脚本/测试 + 台账 + 基线 ＝ **针对旧 nginx 主备拓扑的生产诊断资产**，
>     不是网关功能面；且其前提已消失（执法收敛到网关单点 ⇒ 「谁在前面」不再成立，
>     台账记的 `standby_only` 已知暴露也不再成立）⇒ **登记不移植**，并写明前提。
>   · 既有契约自测（gateway/tool-allow 的 4 处断言）⇒ **由 Go 侧逐条承接**
>     （`mcp/capability_routing_test.go`，对照表见 §7.3）。
> ★ 顺带收口两个**实打实的缺陷**（跑出来的，不是读代码想出来的）：
>   ①`KeySpec` 没有 JSON 标签 ⇒ 控制面按仓内其它接口的惯例传 `allowed_tools` / `scopes` /
>     `allowed_adapters` 会被**静默丢弃**（`scopes` 为空还落回默认档，看起来「能用」其实
>     没生效）⇒ 补齐 snake_case 标签，与 `Key` 出参字段名一致。
>   ②`allowed_adapters` 适配器白名单此前只在 tools/list 生效，**直呼工具名可绕过** ⇒ 补到
>     调用路径上（治理链第 6 闸改为「backend 可达性」＝适配器白名单 + 单边禁用绑定）。
> ★ 验证：新增 `mcp/capability_routing_test.go`（非法能力域 400/-32602/文案保序、请求级覆盖
>   适配器默认、JSON 数组头、非法能力域不计数、429 三件套、上游错误 -32000、工具拒绝 -32003）；
>   `go test ./mcp/...` 与 `go test ./middleware/...` 全绿；`go build ./...` rc=0；
>   `go vet ./mcp/... ./cmd/mcp-gateway/... ./middleware/...` rc=0；`gofmt -l` 零输出；
>   三层 harness（L1 + L2 S01~S20 + L3 Maestro）读数见 `docs/mcp-gateway/05-verification-report.md`。
>   ★ 诚实登记：`go test ./cmd/gateway/` **全包**在本轮仍是红的，失败项全部是
>     `storage_mode_init_test.go` 的 **lite 存储模式需 CGO**（本机以 `CGO_ENABLED=0` 构建）
>     ⇒ 与本轮改动无关的**既存**环境性失败；本轮相关用例用定向
>     `go test ./cmd/gateway/ -run TestRegisterMCPGateway` 验证（主网关接线）通过。
>
> **2026-11-09 211 蓝绿部署轮（T-P1+W1 上线 + 专项验证，goal 会话，通用式见 `LESSONS.md` **§645**；原取 §644 与并行会话撞号，按后到者让号先例改 §645）**：
> ★ 用户目标＝「拉取合并 → 部署 211（蓝绿、健康才切流、前后端都正常）→ 专项测试验证」。
> ★ **部署闸门两缺陷修复（Windows 部署机）**：① 测试闸门卡 go1.27.1（本机 go 实体 1.27.1、
> 平时 auto 在线用缓存 1.27.2/1.27.3，闸门硬编码 GOTOOLCHAIN=local；Docker 兜底在 Windows
> 无 daemon）⇒ 不跳闸门，`DD_TEST_GO_BIN` 钉模块缓存工具链二进制（真 1.27.3，离线，闸门
> 零改动）；② `--force` 撞 `invalid lock target: 211`＝§623 修复从未进本机父仓副本 ⇒
> `ai-native-tools/deploy-lib/lock.sh` 白名单补 211（241 行 + 217 行命令名判据同步）。
> ★ **蓝绿切换全绿**：测试闸门 PASS（sanitizerules 2 红＝基线既有，门自动判「没有新红」）→
> 前端 web+web-mobile 双构建 → 同步 211 → 候选槽 a@8781 起 → **readyz OK(db+redis) 后才切**
> → upstream 8783→8781 → 公网 llm.itestu.cn 200；版本戳 **2968-6e62d2e0**；whoami 401×3
> 无 429；旧槽 b Exited(137) 保留可 docker start 回滚；本地 menu-config.json 构建副作用
> （exported_at）已还原。
> ★ **专项验证（本任务需求）全过**：① **T-P1 探测空转＝0**——scope-skip 队列任务从
> 106/min（前 30min 1040 条）→ 切流后前 2 分钟 154 条（积压排水，同窗真实探测 418 次）
> → **稳态 5min+ 归零**；~400 对冻结对醒来（cred 18 last_attempt 11-05→01:30，诚实
> http_410）；cred 57 的 403(INSUFFICIENT_BALANCE) 行由真实任务接管调度（02:59 重试爬梯，
> 不再是提交失败假死）。② **W1 生产行为实证**——切流后首笔有机 429 的 auto_rate_limit
> 绑定冷却＝**精确 60s**（窗口下限，非旧 3min）；admin settings API 暴露
> `routing.rate.window_refill_seconds`(默认 60) + `routing.rate.pace_violation_threshold`
> (默认 3)，均 HotReload（铸 super_admin JWT 走 /api/admin/settings，453 specs 在册）。
> **2026-11-08 211 探测空转根修 + token 桶 W1 节奏校准落地轮（goal 会话，通用式见 `LESSONS.md` **§643**）**：
> ★ 用户目标＝「继续下一步（W1 实施轮）＋同步检查 211 探测为何状态不对」。
> ★★★ **检查结论＝T-P1【P1】活缺陷**：F2 恢复复检豁免（periodic/credential_recovery 绕过
> 模型范围门）**只加在提交侧**，消费侧 ProbeService.Run 照旧拒收 ⇒ 泵入队成功 → Run 拒收
> settle success → 泵 45s holdoff ⇒ **每对每 45s 一条「success 但零探测」任务永动**——
> 211 实测：24h 159,313 条任务、150,298 条 success（94%）、~397 对 last_attempt 冻结 3 天+、
> reason_code=automatic_probe_model_out_of_scope 占 12,778/2h；probe-schedule-audit R1-R4=0
> 全盲（45s holdoff 让行永不过期 15min）。取证三方对拍（状态行 45s 被推×运行史零新行×
> 队列史每 30s 一条 success）＋蓝绿翻转后容器名失效坑（-a 已死、在役 -b）。
> ★ **修复**（`fix(bg/probe)` 8ad9bd8d865d）：Run 的模型范围检查套用同一豁免（恢复复检
> Catch-22 在最后一跳打通，periodic 行真实探测结果接管调度、空转收敛）＋范围拒收路径补
> deferScopeSkippedRow 15min（两端闸再漂移退化为 pacing 而非永动）＋ modelScopeFn seam；
> 门 B1-B4 ＋ 变异臂 M3/M4 全检出。
> ★ **W1 落地**（`feat(credential)` 0b4ce4715576）：`ObserveWithPace` 把 A2 无条件累计升级
> 改为节奏校准（间隔 ≥ 窗口重灌估计＝节奏正常 ⇒ 归零按窗口冷却；快速连击 ≥ threshold 才进
> 3min<<n 梯）；settings 两键 `routing.rate.window_refill_seconds`（默认 60，**0=逐字节回退
> A2**）+ `routing.rate.pace_violation_threshold`（默认 3）热加载；**既有 A2 测试族按 refill=0
> 注入一行断言不改全绿**（逃生口＝遗留路径的门）；RA 优先/遗忘窗/绑定级作用域/429 不进断路器
> 四红线不动；变异臂 M1/M2 全检出。四臂还原全部 md5 逐字一致。
> ★ 验收：settings/credential 两包 ok（-skip TestRedisRPM）、bg 全包 ok 39.3s；
> `TestRedisRPM*`＝无本地 Redis 环境红（干净 worktree 基线实证非本轮引入）；
> 合并并行会话 5 提交零冲突（c289656d1bdd）。**明确没做**：未部署 211（修复已推送，
> 部署待指令——部署后预期队列 churn 归零、~400 对冻结行随真实探测爬梯自愈）。
> **2026-11-07 Token 桶双层路由优化方案轮（规划·零代码，goal 会话，`docs/audit/2026-11-07-token桶双层路由优化方案.md`；通用式见 `LESSONS.md` **§642**）**：
> ★ 用户目标＝「429/超限族不判死节点＋保在途＋隔时回量爬坡；结合 magpie 对比；模型+凭据=
> token 桶定型定参进路由模型（序列逐个尝试）；auto 两层路由双模块+高速缓存计数；会话桶历史
> 粘性/重试等待/失败换桶；客户端成功率与桶成功率双口径+质量向上 rollup。先方案＋自审计＋
> 模拟验证，再提交推送，先不改代码」。开工 ff-merge `df2288e0cb35`（197 提交零冲突，
> build/vet rc=0、bg+errorsx short 绿）。
> ★ 证据底座＝211 实测（24h 12609 请求：客户端成功率 76.4%；失败 91.4%＝2710 例
> `no_candidate "modality=text => 0 candidates"`——不是 429 漏到客户端，是**候选池被门空**；
> 饥饿实证 kimi-k3 15 绑定→0 可路由、opus-4-8 12→0、minimax-m3 6→0；门分布
> availability_unreachable 485/binding_unavailable 689/node_probe_failed 19…）＋ magpie 源码
> 对比（rest/Sink/lanes/affinity/replan 五面已同构或更细，无「抄机制」级缺口）。
> ★★ **仿真否决了直觉方案**：四策略对比（P0 现状/P1 A2+绝对 AIMD/P2 节奏校准+AIMD/
> P3 节奏校准+相对爬坡）——P1 比 P0 还差 7.3pp（限速门把恢复容量让给饱和桶）；
> **P2=P3 完全同数**（短冷却下窗口自重灌，爬坡形状不敏感）⇒ 保留 P0-A 相对份额爬坡、
> 否决绝对限速准入；真杠杆＝**冷却税则改「节奏校准」**（RA∨60s 优先、只在 429 间隔<窗口
> 且连击≥3 时升级）⇒ 亏空域 client_ok 62.8%→80.0%、免费桶 cool_time 196→125min，
> 代价＝上游 429 噪音 43→236 次/4h（显式标价，待裁决）。
> ★★ 诉求盘点：六件五件已有骨架（429 不进断路器/dispatch 三级梯/RetryScheduler 5s×2/
> sticky L1-L3/is_final_success/P0-A 爬坡）；真缺口＝①A2 梯对节奏正常桶过重 ②同桶三套门
> （cmb.available/credentialstate/node_probe_state）各自为政 ③桶无统一读模型与质量 rollup。
> 方案＝T1 节奏校准冷却（ratebackoff.go 参数级改造）＋T-Gate BucketRegistry 影子层
> （先合并 verdict 不合并存储）＋T4 会话桶历史环/C1 经济学转正＋T5 质量 rollup
> （复用 supplier_error_stats 骨架不建新表）；W1–W4 分轮各带验收门；待裁决 4 条
> （含「单次 429=3min」既有承诺变更、上游噪音可接受度）。**明确没做**：零代码零迁移零部署。
> **2026-11-08 报价系统 MVP 风暴档审计与终版实施轮（goal 会话，审计档
> `docs/audit/2026-11-08-报价系统MVP-风暴档审计与终版实施轮.md`；通用式见 `LESSONS.md` **§644**（合并时实测并行会话已用 §643，改号））**：
> ★ 用户目标＝「根据《2026-11-04-报价系统MVP-头脑风暴与运营思路》进行分析与审计，修正发现的问题，
> 完成最终版本，然后进行执行实施」。开工树 `bb38b3223` 干净；审计 = 2 并行只读代理（后端 90
> tool-uses / 前端 29）+ 211 活体（sales_* 8 表全 0 行＝「链路通了没人走」字面应验；视图 trusted
> 27 行未漂移；salesdecorate price_observation 9242 行最新 11-08）。
> ★★★★ **P1＝前端建单链必 23514**：payload 无 confidence ⇒ 版本置信空串违反 945 CHECK
> （真库测试全显式传 trusted ⇒ 全绿掩盖唯一真实用户路径）⇒ 根修＝**服务端派生**（deriveQuoteItems：
> 按 canonical 回查供给视图覆盖成本三轴+置信，客户端声明永不采信；P5 五价快照恒空/P6 C3 门可伪造
> 同源关掉）；本轮自己的 realdb 腿当场抓出 **margin_pct 22003 溢出**（收入趋零 ⇒ -721300% 击穿
> numeric(8,4)）⇒ marginPctOf 钳 ±9999.9999 + 收入≤0⇒NULL。
> ★ 4P2 收口＝公开面真限速（ipWindowLimiter per-IP 固定窗，`sales.public.rate_limit_per_min`=30，
> IP 走 ContextClientIP 可信解析）/owner 语义落实（salesOwnerScope + EnsureQuoteOwner 非本人 404 +
> ListQuotes SQL 收窄）/**RevokeShare 接线**（此前无任何 API 面＝「可撤销」只有服务半边）/
> 联系人最小化（revealContacts 仅 super）+ 8P3（RowsAffected 守卫/负数拒绝/quoted 联动激活/
> 过期探测不污染埋点/审计行实状态/死代码）。
> ★ 交付（执行实施）＝**W2-T2**：`GET /api/admin/sales/dashboard`（super，全现算零新表：漏斗含
> expired 派生态现算 + DealHub 突破率走 status_log 面 + jsonb 逐维聚合 + 毛利分桶；**占位键
> approval_rate_alert_pct 自此有消费方**）+ 成本漂移（attachCostDrift 与派生**同源选行**，提醒不自动撤）
> + 三档系数 settings 化（tier_coef_*=1.3/1.6/2.0 仅前端快填，服务端不校验——两把尺反模式不进服务端）
> + **客户版页面** `/public/quote/:token`（public 路由 + PublicQuoteView + 裸 fetch 绕 401 会话裁决 +
> 打印兜底 PDF + 水印 + 过期/撤销统一不可用态；响应体断言随 sale_total_usd/subtotal_usd 收紧——
> 合计是报价要素非敏感轴）+ 前端建单弹窗化/模型选择器/三档快填/prompt→dialog/分享撤销。
> ★ 验收＝go build/vet 全仓 rc=0；sales realdb **13 腿全绿**（+7 新）；admin 110s/settings 绿；
> vitest **296 文件/3027 passed**（+6 判据）；vue-tsc rc=0、vite build ✓；i18n 四门全绿（20+19 新键×8
> 语种，死键 4 删，es/fr 两处同值棘轮当场改译）；navCoverage 反向断言抓回我自己的白名单登记
> （工作台复制链接＝真实入口 ⇒ 撤登记）；admin-routes 619→620 + post-surface 补 1（class=read）。
> ★ 登记：940 同步器显式不落地（跨仓读 salesdecorate 需 owner 授权+跨库凭据两件前置＝新裁决）；
> 两维红线分层维持退化（「模型档」无数据源＝假维度）；服务端 PDF 不做（打印兜底已给）；
> err.Error() 直透与 944 status_log 注释矛盾登记遗留。**明确没做**：零迁移（本轮无新迁移）、
> 零部署（245/154/211 留给部署轮，公开面行为变化应带冒烟）。
> **2026-11-07 探测浪费与恢复及时性根修轮（goal 会话；方案档
> `docs/audit/2026-11-07-探测浪费与恢复及时性根修轮.md`；T2 留档
> `tests/48h-audit/reports/history/T2-2026-11-07-wasteround.md`）**：
> ★ 用户目标＝「正常节点模型桶被反复探测浪费资金＋异常桶恢复感知不及时 → 修正 → 脚本部署 211 →
> Maestro 全面测试验证 → 修正发现的问题 → 提交推送」。即小时审计轮登记的「245 R4 需开发轮根修」那一轮。
> ★ 取证（211 真库）：多个对 24h 被探 460-480 次、cf 累积 947-1026 仍恒 ~5min 一探；凭据 35 族
> ~70s 一探且 **node_probe_state 无行（cf=-1）**；2h 窗口 submit 18,667 次。
> ★ 根因四叠加＝①`probe.recovery_max_backoff_seconds`(300s) 把恢复族长尾削平（对持续失败无出口）；
> ②`expiredCmbRecoverySQL` 不看梯子（2026-07-24 有意移除）⇒ 绑定冷却一到每 30s tick 重开
> attempt=1 新任务从梯底重走；③配额窗口内**非配额形态**失败（503 形态）不触发 F4 钉；
> ④write-through 无新鲜度守卫＋成功 mirror 的 last_attempt_at=记账时刻 ⇒ R4 竞态复发。
> ★ 交付（W1–W9）＝**W1** 持续失败升级（attempt=cf+1 ≥ `probe.sustained_failure_attempt_threshold`
> 默认 21 ⇒ 复检升 `probe.sustained_failure_recheck_seconds` 默认 1h；阈值以下与 11-05 口径逐字一致；
> 两处 cap 位点统一走 `capRecoverBackoffForAttempt`）；**W2** 绑定冷却同档升级；
> **W3** expired 分支补梯子守卫（**有意反转 07-24**：搁浅两前提——梯子无界、无事件回拉——都已消除）；
> **W4** clamp 豁免 cf≥阈值行（`< $3`）；**W5** 配额钉第二臂（凭据行用尽态+未来 recover_at 即钉，
> 失败形态无关；typed-nil pool 显式拦）；**W6** 回拉分级（`rearmEligibleRecoveryKind`：health_status_reset/
> consecutive_failures_clear 家务类不 rearm）＋ rearm 用尽窗口守卫；**W7** healthyBindingSQL 新鲜度守卫
> （只恢复 `unavailable_at <= 证据时刻`，evidenceAt=直连轮完成，三路贯通）；**W8** 成功 mirror
> last_attempt_at=证据时刻（R4 审计量「拉黑 vs 证据」）；**W9**（线上验证暴露）失败镜像改 **upsert**
> ——队列模式无写点建 node_probe_state 行 ⇒ 无行业对 cf 永不累积、升级永不触发（gateway-side 保持不建行）。
> ★ 验证＝211 两笔部署（2965 槽 b→2966 槽 a @8781，readyz/公网/登录冒烟绿）；线上稳态＝高频对
> next_retry ~25-30min（featured 50%×1h）、绑定冷却 ~1h、**node_probe_runs 15min 窗 155→38**、
> 凭据 35 族开始建行爬梯、probe-schedule-audit **R1-R5=0 M0=0**；Maestro＝三门合成绿
> （race 全仓唯一红＝D17 既有缺口：sales 占位键未登记 → 本轮收口）、17 域 17/17、压测 18/19
> （唯一红＝已披露 s17）、s18 5500/5500、s19 12000/12000 p95 78ms、goroutine Δ+16<20。
> ★ 遗留登记：245/154 未部署本轮（用户只点名 211；R4 在 245 需下轮统一部署轮收敛）；
> 探测失败 vs 业务成功的优先级裁决未做；`recoveringSweeper`（model_probe_state 面）未动；
> 启动提示词 4.3 步（tests/stress 无 Go 测试文件）与现状不符（登记，未代改）。
> **2026-11-07 第 50 增量：48 小时修订审计轮（goal 会话，`docs/全面审计v3/2026-11-07/{01-审计方案,02-审计发现与缺陷台账,03-修复与验证记录}.md`；通用式见 `LESSONS.md` **§641**）**：
> ★ 用户目标＝「对 48 小时内的修正进行审计……制定详细执行方案后审计执行」。开工 ff-merge
> `22f391ca7`→`442af665c`（12 提交零冲突）；窗口代码 5 笔＝/live 全屏＋折叠（0bf442dd3/d415e6a3d）、
> 探测时间点规划 F1–F6＋迁移950（95fecb3d4）、E2BIG 根修（205b6dbd7）、小时探测审计（49ada52f2）；
> 方案文档三份通读（探测轮/全屏轮/D09 plan）。五组并行只读取证（A 探测恢复/B 审计脚本/C eval 深解码/
> D 前端/E 横切）。
> ★★ **成立面**：F1–F6 实现与方案档逐条一致（豁免闭集/钉点三态/rearm 守卫/选模链全复核成立）；
> D 组零阻塞（Fullscreen 生命周期三全、i18n 棘轮全过）；迁移 950 幂等与登记三面一致；版本五处一致。
> ★★★★ **本轮最重发现＝F-1【P2】**：迁移 950 的 **installer 五点同步全缺**（embeddata 副本＋StartupFiles
> ＋**embeddedSQLFiles map**＋installed_startup_migrations.tsv）⇒ guards-installer **4 红在 main 上挂着**
> （CI 不跑单测的盲区），且实测 **942–949 整批从未接进 map**（W1 轮就漏）⇒ 本轮成批接线收口；
> **F-2【P2】**＝db.go `ensureNodeProbeTriggerKindSchema` queue-source 臂仍 6 值（缺 credential_recovery）
> ⇒ init-local 链会把 950 静默撤回 ⇒ 6→7 值＋契约门补断言。**F-10/F-11【P2】**＝E2BIG 根修的同族残留
> 与注入面：`_dd_unq` 只接了 3 个身份键、同族 13 键仍「写 %q 读不解码」（首轮即坏值＋逐层累积）⇒
> 解码函数上移＋16 键全接；且 `_dd_unq` 的 eval 对「反斜杠＋未转义 $(/反引号」混合形态**会执行命令替换**
> （沙箱 touch 落地）⇒ eval 前危险形态守卫（剥 `\$` 对后剩未转义 `$(`/`${`/反引号/非 `$'` 的 `$` ⇒ 拒绝解码）
> ＋**常设门 19 判据**（变异臂＝抽守卫段 5 红全检出；守卫首版 `printf '%c' 96`→'9' 的反引号漏判被门当场抓回）。
> ★ **B 组**：FIX_R3/FIX_R5 与报告谓词漂移＋FIX_R4 半面写（缺 model_offers 镜像）＋FIX_R2 死谓词
> ⇒ 全部同源化＋双面写＋新增形状门（4 测试）；--fix 后复测计数＋快照原子写＋接线锚点落仓；
> 245 PGURL 暴露裁决＝与 .env 同信任域、头注登记不改造。**E 组**：admin-routes 快照漂移（窗口引入）
> 与 canonical 登记簿缺 probe_model_scope.go（既有红，§619 判据本轮收口）都从 SSOT 重生成/补登。
> ★ 验收读数：build/vet 双模块 rc=0；**guards-fast 全聚合 rc=0**（installer 4 红→0）；bg 探测族＋D09
> 四包＋db＋admin canonical＋ursmcheck 定向全绿；identity 门 20/0、asm 门 26/1（唯一红=既有
> slot_b_port，stash 基线对拍）；审计脚本本地真库实跑 findings=0。登记：A4a rearmFn 无锁写（既有族）/
> A4b SetOnQuotaRecovered 死接线/domain349 滞后/D4 量具脚手架重复/六份 source 闭集仍无互拍门。
> **明确没做**：未部署（本轮不触网关运行时行为；db.go ensure 臂下次重启一次性幂等收敛）。
> **2026-11-07 凭据探测时间点规划与恢复及时性轮（goal 会话，通用式见 `LESSONS.md` §640）**：
> ★ 用户目标＝「检查凭据节点可用性检查（对象＝24h 用量模型∪特色模型；出错/用尽凭据）→ 凭据已恢复但
> 状态不及时更正 → 规划探测时间点（节约并有效）→ 总结方案 → 模拟中断/恢复场景修正 → 本地部署 +
> docs/全面测试 Maestro 逐域验证 → 完善测试方案 → 提交推送 → 245/154 → 211」。
> ★ 交付（F1–F6）＝**F1** 恢复验证探针空模型静默丢弃根修（`Submit("")` 直接 return ⇒ 接线处
> `ResolveRecoveryProbeModel`（default_probe_model→绑定回退）+ `source="credential_recovery"`）；
> **F2** 模型范围门豁免恢复源 `{periodic, credential_recovery}`（冷模型失败对 Catch-22：失败→无流量→
> 出窗→不能探→永不恢复；泵行天然带错误证据谓词 ⇒ INV-1 不破坏）；**F3** scope skip 推进到期行
> 15min（45s 空转 ×20 治理）；**F4** 配额类失败（402/403/429+errorsx 配额族）且 quota_recover_at
> 已知未来 ⇒ 钉 recover_at+60s（用尽期 0 探测，~288 次/天/对 → 重置点 1 次；绕过 featured 加速）；
> **F5** quota 恢复事件（onQuotaRecovered 四处）追加 `RearmCredentialFailures`（re-arm 该凭据
> last_direct_ok≠TRUE 的未来排程行 ⇒ 泵 30s 接管）；**F6** 注释如实化（cheapest→字典序）。
> ★ **第 6 缺陷＝本轮自己引入**：`credential_recovery` 在 trigger_kind（425）合法但
> `credential_probe_queue_source_check`（489/538）没跟上 ⇒ 23514 + persistSubmitFailure 覆写
> （P-1 同族）⇒ **迁移 950**（幂等 DO 块 6→7 值，down fail-closed）。
> ★ 验证＝三门 rc=0（race 全量含）；bg 9 测试/21 判据 + 变异臂 3/3；D09 域由占位**填实**
> （business 4/data 2/stress 1/safety 3）；17 域 17/17 PASS；压测 18/19（唯一红＝已披露 s17），
> s18/s19 强制门双绿，goroutine Δ+8。**本地 2948 终验**：constraint 7 值、
> `{source=credential_recovery,outcome=success}=5`、23514 零新增。
> ★ 环境读数（登记）：`deploy-local.sh` **不跑 pending startup 迁移**（那是 seamless 引擎
> `_deploy_pending_startup_migrations` 的职责）⇒ 本地 950 手工应用+补两台账；deploy-local 从 redis
> 容器 env 原样继承 `LLM_GATEWAY_REDIS_ADDR=nbjl-redis:6379`（网内名+NOAUTH；发现层信端口不信鉴权）
> ⇒ 取道＝启动项目自带 `llm-gateway-redis`（compose，无密码）+ `LLM_GATEWAY_REDIS_CONTAINER` 钉容器名；
> 前端构建会把 `localeKeyParityBaseline.json` 掏空成 `{}`（`.hollowout-backup` 同族）⇒ **提交前必须
> `git checkout` 还原**（棘轮清零=把门弄瞎）。
> ★ **三台部署全过（95fecb3d4 推双仓后）**：本地=2948（端到端：约束 7 值、
> `{source=credential_recovery,outcome=success}=5`、23514 零新增）→ **245=2949**（263s/切换 22s，
> pending 迁移 950 自动应用，约束 7 值实证）→ **154=2950**（244s/切换 22s，公网 llm.kxpms.cn 同步）
> → **211=2963**（槽 b @8783 蓝绿；**首次部署被 E2BIG 拦下**＝`IDENTITY_ISSUER_ALLOWLIST` 单参
> 512KB 撞 MAX_ARG_STRLEN——**%q 读-写不对称逐层累积**（写侧 printf %q、读侧只剥引号不解码 ⇒
> 2ⁿ 增长，11-06 的 64KB 尚可通过）⇒ `_dd_unq` 深度解码（211 实测 512KB→**74 字节**）+
> GW_ENV 100KB 单参守卫（`205b6dbd7`）；**211 的 docker 引擎走 `apply-db-revision-sequence.sh`
> 显式清单通道**（与 systemd 的 pending 扫描两条通道）⇒ 950 补清单后复跑落库实证（7 值）；
> 登录冒烟 3×401 无 429；公网 llm.itestu.cn 2963 同步；版本戳 2963 收敛 `205b6dbd7`）。
> **2026-11-07 小时审计轮（同日追加）：探测记录小时审计 + 恢复回拉补全（通用式并入 `LESSONS.md` §640 族）**：
> ★ 用户目标＝「定制计划任务每 1 小时审计探测 log/记录，对比检查状态更新延迟/探测缺失，修正问题，
> 245 先试、部署 211」。★ 交付＝`scripts/audit/probe-schedule-audit.sh`（R1-R5+M0 六面；快照对比；
> `--fix` 有界修复＝与系统自身组件同谓词幂等）＋ ZCode 计划任务每小时 :07 跑 211+245。
> ★ **判据校准三轮（误报甄别＝审计脚本最有价值的部分）**：R3「67 天未探」全部是停用凭据
> （43 的 manual=t 第一遍看漏 ⇒ 逐谓词消元才定位）⇒ 判据必须带泵同款 eligibility；R4 两类误报
> （成功后业务重拉黑=正常抖动、auto_discovery_expired=discovery 域语义）⇒ 加时序对比与语义豁免；
> R5 增长形态＝http_404/410 合法 6h 长停放 ⇒ 排除永久族。★ 真发现＝① R4 真 write-through 缺口
> 1 例（已补写）；② **availability/stale-cleanup 恢复路径不触发事件回拉**（R5 基线停未来 5.3h）
> ⇒ `SetRearmFunc` 全恢复类触发 RearmCredentialFailures（49ada52f2）。★ 部署＝本地 2951 →
> 245=2952（**active-port 翻转至 8781 ⇒ 8782 无人监听是正常形状**，别当故障）→ 211=2964（槽 a；
> R1-R5 全 0；M0 57→13 排水）。★ ZCode 计划任务 automation-b615cd12（每小时 :07）。
> **2026-11-06 /live 路由判定折叠轮（goal 会话，通用式见 `LESSONS.md` §639）**：
> ★ 用户目标＝「/live 的『路由判定』部分要能折叠与展开 → 部署 245/154 测试 → 成功后部署 211
> （脚本＋蓝绿）→ 提交推送」。
> ★ 交付＝`RoutingDecisionTimeline.vue` 头部改 toggle 按钮（**StatsPanel 同族形态**：标题即按钮 +
> CaretRight 旋转 + `aria-expanded` 同步 + `data-testid=decisions-toggle`）；默认展开（判定时间线是
> 排查主视图）；折叠收掉列表与空态文案只留头部一行，canvas（flex:1）自动吃回空间；**折叠态不渲染
> 「无判定事件」空态**（那是无数据的形态，不是收起的形态）＋ 新键 `liveTopology.decision.collapse/expand`
> × 8 语种（ar-SA/de-DE/en-US/es-ES/fr-FR/ja-JP/zh-CN/zh-TW；fr 用 Déplier/Réduire 措辞族）＋
> **新测试 4 判据**（默认展开/折叠恢复往返/空数据×折叠正交/i18n 键真实存在——`aria-label` 直接断言
> 中文文案，缺键渲染字面键名当场红）。
> ★ 验收读数：vue-tsc rc=0；**vitest 295 文件/3021 passed（+4）**；vite build ✓（web+web-mobile）；
> i18n 四门全绿（parity/shared-dead/locale-value/主审计+CJK 棘轮 4483 持平）。
> ★ **三台部署全过（d415e6a3 推双仓后）**：245=2940（227s/切换 19s，2939→2940）→ 154=2941（276s/
> 切换 20s，公网 llm.kxpms.cn 同步）→ 211=2958（**bash5 + LLM_GATEWAY_ROOT 钉根**；槽 b @8783
> 蓝绿接管，旧槽 a 保留可 docker start 回滚；公网 llm.itestu.cn 同步；登录冒烟连登 6 次无 429）。
> ★ 三台 SPA bundle 同一产物 `index-CMn7QGm5.js` 均含 `decisions-toggle`（grep release 目录实证）。
> ★ 211 测试闸门：admin 包 `TestCanonicalReadPointRegistryCoversEveryMentionedFile` 红＝**基线既有**
> （闸门自动比对 origin/main 判「没有新红」；扫描器只扫 .go，本轮零 Go 改动——全屏轮同一条）。
> ★ 版本戳收敛 `c089cd5c6` 推双仓。
> **2026-11-06 build_seq 跨环境分歧排查＋245 补部署轮（goal 会话，通用式见 `LESSONS.md` §638）**：
> ★ 用户目标＝「使用脚本部署到 245 与 154，前端显示编译序号 2938、211 同期却 2958，检查原因并修正」。
> ⇒ ★★★★ **根因＝两台机器各一套机器级 floor 计数器、互不可见**：`bump-version.sh`（245/154 systemd
> 无感路径）的 floor 在**部署机** `$HOME/.llm-gateway-go/build_seq.floor`；211 的 docker 蓝绿引擎
> `deploy-seamless-docker.sh` **整个 seq 改写块随 `--via` 在目标机执行**，floor 在 **211 的
> `/root/.llm-gateway-go/`** ⇒ 两套计数器自 10-31 build_seq 解冻（原恒 2910）起各自独立递增，
> 211 部署频次更高 ⇒ 号领先（2957 vs 2938）＝**机器级计数器的固有形状，非丢号非落后**。
> ⇒ ★ 机器读数：本机 floor=2938；211 floor=2957、槽 a=2957（0bf442dd-**dirty**，AGENTS 记录
> 过的全屏轮部署）、槽 b=2956；154 已被 19:46 **无 AGENTS 记录的部署**收敛到 2938（60438c8e，
> healthz/管理页 200）⇒ 唯一真缺口＝245 停在 2936（全屏轮收口提交 `60438c8e1` 之前）。
> ⇒ ★ **修正＝245 补部署**：`deploy-245.sh` 全绿（185s/切换 15s，2936→**2939**，healthz/readyz/
> 凭据冒烟 17 creds failed=0/admin 密码同步 200/nginx 仲裁通过；无 pending 迁移）；
> 两脚本序号分配点补注释钉死「两套计数器、跨环境序号不可比」。★ 门：bump_version_collision 5/0、
> deploy_seamless_version_identity 9/0、deploy_sync_prune 24/0。★ 双仓已推 `e946c6a88`
> （origin 直推；github 本机出网断 ⇒ 经 115 SOCKS 隧道 `-D 19995` 推讫，§623 先例复用）。
> ★ 遗留登记：① 154 的 2938 部署者未记录（§618 族在途风险，本轮不代记）；② 211 在役戳含
> `-dirty`（构建时仓库树不洁，功能代码=0bf442dd，下轮 211 部署自然消解）。
> **2026-11-06 /live 三栏整体全屏轮（goal 会话，`docs/audit/2026-11-06-live三栏整体全屏轮.md`）**：
> ★ 用户目标＝「/live 加一个按钮：客户端响应＋中间实时流程图＋右侧供应商作为**一个整体**的全屏显示 →
> 测试验证 → 先部署 245/154 验证 → 再部署 211（蓝绿）→ 管理页能打开 → 提交推送」。
> ★ 交付＝顶栏过滤栏右端常驻「进入/退出全屏」按钮（原生 Fullscreen API 把 `.live-topology` 本体推成
> 全屏元素：三栏＋过滤栏＋统计整体占满视口；ESC/手势退出；`fullscreenchange` 同步文案与
> `is-fullscreen` 类；卸载摘监听＋退出）＋ 视图测试 5 判据 ＋ 8 语种 `fullscreen.enter/exit` 键
> （★ zh-TW 用「全螢幕」措辞——直抄 zh-CN 会撞 same-as-source 棘轮）＋ **新量具
> `scripts/audit/verify_live_fullscreen.mjs`**（CDP Chrome：FS1-6＋C0，受信点击/ESC/三栏≥200px/铺满≤2px）。
> ★ 选原生 API 而非 CSS 覆盖层的理由：element-plus popper/overlay 挂 body 下不进自制全屏层（z-index 竞赛不可赢）。
> ★ 验证：vue-tsc/vite build 绿；**vitest 全量 294 文件/3017 passed**；i18n 四门＋间距尺全绿；
> 本地 preview 量具 **7/7 PASS**（三栏各 671px、全屏层 1440×900）＋截图视觉复核。
> ⚠ 已知边界：IAB webview 不支持 Fullscreen API（Permissions check failed）⇒ 量具走 Chrome CDP（两步量具同族先例）；
> 211 基线两步量具 2 红（L1b/L3 probe 行窗口抖动）为**存量**、与本轮无关（本轮零接触面板/浮层逻辑）。
> ★ **三台部署全过（0bf442dd3 推双仓后）**：245=2936（215s/切换14s，healthz+readyz 200、管理页
> /maintain/home 200、全屏 7/7＋两步 ALL PASS）→ 154=2937（171s/切换15s，公网 llm.kxpms.cn 同步，
> 管理页 200、全屏 7/7）→ 211=2957（bash5 + LLM_GATEWAY_ROOT 钉根；槽 b→a 蓝绿；healthz/管理页 200、
> 全屏 7/7＋两步 ALL PASS 优于基线）。★ 部署门唯一红 TestCanonicalReadPointRegistryCoversEveryMentionedFile
> （admin）＝基线既有：扫描器只扫 .go（go/ast），本轮零 Go 改动 ⇒不可能本轮引入（登记待收口）。
> ★ 量具沉淀：全屏量具补 mint/token 鉴权通道（245/154 管理台需登录）＋ SPA 挂载轮询（固定 sleep 会把
> 慢载误判成缺按钮）。
> **2026-11-06 第 49 增量：48 小时修订审计轮 —— 五组并行只读取证 ＋ 16 项修复 ＋ P1 自维持风暴止血 ＋ 真库腿 6/6**
> （goal 会话，`docs/全面审计v3/2026-11-06/{01-审计方案,02-审计发现与缺陷台账,03-修复与验证记录}.md`；通用式见 `LESSONS.md` **§637**）：
> ★ 用户目标＝「对 48 小时内的修正进行审计……制定详细执行方案后审计执行」。树对齐 main==origin/main
> `b8777d9f1`；窗口 11-05 06:30 → 11-06 00:20，代码类 9 笔；审计对象＝A 报价 W1 前端（`e0ed0b6b1`，
> 仅自验）/ B 第 48 增量 14 项修复复核（`78f07316d`）/ C probe 收窄深审（`97c9c1b21`，Cursor 协作、
> **无审计档**）/ D 部署断链轮 / E 接缝文档遗留。
> ★★ **本轮最重发现＝P-1【P1】**：probe 收窄的新哨兵 `ErrProbeModelOutOfAutomaticScope` 不在**两侧**
> 确定性门白名单（`node_probe.go:1167`＋`probe_queue_worker.go:281`）⇒ scope 外存量行每 45s tick
> 烧 3 重试＋persistSubmitFailure 覆写 last_err_code＋泵不推进 next_retry_at＝**glm-5.3 洪水同族自维持
> 循环** ⇒ 修双侧白名单＋第三哨兵测试臂（skip 后泵推进 holdoff、指标 skipped_out_of_scope 可见）。
> ★ **B 组修复的修复**：A4 scratch 守卫排在 15 张 DROP TABLE **之后**（P-2）⇒ 守卫前置＋实测非
> scratch 库名立即 Fatalf 零 DDL；其余 11 项全部独立成立。★ **C 组**：P-3「近 24h 真实用量」实为
> 热腿 8h（口径如实降级＋spec 对齐）；P-7 clamp 漏 dns_error/request_error；P-8 sweeper 6× 补记。
> ★ **A 组前端七项**：P-4 审批明细接 /approvals（trigger_reasons 此前取回即弃）/ P-5 super-only 按钮
> isSuperAdmin 镜像 / P-6 token 判据 mock-on-mock → 组件交互驱动（el-input stub 补 v-model 契约）/
> P-9 withheld 明细表 / P-10 可空类型 / P-11 reload 序号守卫＋find 断言消除 / P-16 页签真驱动。
> ★ **E/D 组**：慢SQL提案 35 列含 **7 幽灵列**（在 request_logs/session_bodies）⇒ 提案副本订正 28 列
> ＋提示回编排台核实真实数据源；遗留 10 项维持登记（C4 由 P-14 顺带收口：_shared-lib 候选链补新布局）；
> 跨仓 P-15＝父仓 deploy-prereqs 死默认改候选链＋本仓 envs 门断言 ⑪（§636 通用式第二次实证）；
> D 组确认既有红 deploy_sync_prune 已转绿 24/0、父仓半迁移态已收敛；远端分支
> docs/bank-security-profile-2026-11-06 在途不并入。
> ★ 验收读数：build/vet rc=0；bg/settings/sales 全绿；**sales realdb 6/6**（含 A6 上界回归钉）；
> web vitest **293 文件/3012 passed**；vue-tsc rc=0；vite build ✓；i18n 三门 26/26（+7 键 × 8 语种）；
> guards-gate-scripts rc=0（envs 26/26、migration 8/8、shared_lib 25/25、登记册 166/0）。
> ★ 遗留登记：P-17 指标维度 / P-18 sent 未触发修订保锁（裁决）/ P-19 /public/quote 路由（W2）/
> P-20 CI 盲区 / P-21 历史噪音；第 48 增量遗留 A6b/A8/A9/A10后半/B2/C5/D1/D2/D4/E3 维持。
> ★ **三台部署收口（同日部署轮）**：245=2934-e9c9e78c（402s）→ 154=2935-e9c9e78c（186s，公网
> llm.kxpms.cn 同步）→ 211=2956-e9c9e78c（槽 b @8783，llm.itestu.cn 200）；**零 pending 迁移**；
> ★ P-1 在役行为证据（211 真库）：441 行 queue_submit_failed 历史戳记**零新增**＋ 5 分钟 162 次
> deterministic gate skip（outcome=skipped_out_of_scope）＋ 到期行 470→452（holdoff 推进）⇒
> glm-5.3 同族自维持风暴止血生效；211 部署需 bash5（deploy-211.sh 要求 bash>=4，脚本自带引导）。
> **2026-11-05 报价系统 MVP W1 前端轮：/operations/sales 工作台 ＋ 8 语种 i18n（第 48 增量遗留项交付）**
> （goal 会话，`docs/audit/2026-11-05-报价系统MVP-W1前端轮.md`；通用式见 `LESSONS.md` **§635**）：
> ★ 用户目标＝「拉最新合并 → 提交推送 → 按实施规划继续剩余任务」。开工 ff-merge
> `1d7d8d46d`（8 提交零冲突，3 条新部署门脚本 24/9/21 全绿）；规划盘点＝W1 后端已被
> 第 48 增量调和收口（`3c9954acb`，本会话早前后端 WIP 按先例并入）⇒ 本轮＝其 §6 W1-T2
> 遗留前端。★ 交付＝`web/src/api/sales.ts` ＋ `SalesWorkbenchView.vue`（供给/需求/报价
> 三页签；withheld 组单独呈现；C3 置信档逐行展示不越权阻断）＋ `/operations/sales` 路由
> ＋ operations 菜单项（不设 super 门：写权限应用层闸）＋ 8 语种 sales.ts/nav 键/index
> 合并 ＋ menu-config.json 重导出 ＋ 视图测试 3 判据。
> ★★ **行为决议**：分享 token 一次性呈现（`ShareLink.token` 类型层 optional ＋ 测试断言
> 清单取不到）／红线判定无独立端点（submit 内服务端裁决，前端只呈状态与 trigger_reasons）／
> 前端不做 C3 提交拦截（两把尺反模式）。
> ★★ **踩过的门（全转绿）**：element-plus 无 auto-import（显式引入 ⇒ 测试 mock 必须
> importOriginal 部分透传）· UI 契约 B 四弹层补 max-height 滚动 · 间距尺 6px 离阶收
> `--kx-space-2`（改值不调基线）· i18n 值质量三棘轮逐键改译（de/fr +4、zh-TW +14 ⇒ 0 新增）
> · en-US nav 漏键被 parity 硬闸当场抓（8 语种缺一即红＝门的牙）· DefaultRow 显式断言 ×8。
> ★ 验收读数：vue-tsc rc=0；**vitest 全量 293 文件 / 3011 passed**；vite build ✓（UI 契约
> A/B=0）；i18n parity/shared-dead/间距尺全绿；config 门 60/60；Go 侧零改动（build rc=0、
> sales 6 真库腿 ok 1.132s 复核零扰动）。★ 遗留不变：W0 真单（人工）、W2 同步器/看板/PDF、
> 公开面客户版页面。
> **2026-11-05 第 48 增量：48 小时修订审计轮（五组并行只读取证 ＋ 报价系统 MVP 调和落地 ＋ 14 项修复 ＋ 真库腿首次跑绿）**
> （goal 会话，`docs/全面审计v3/2026-11-05/{01-审计方案,02-审计发现与缺陷台账,03-修复与验证记录}.md`；通用式见 `LESSONS.md` **§634**）：
> ★ 用户目标＝「对 48 小时内的修正进行审计……制定详细执行方案后审计执行」。
> ⇒ ★ 开工动作：ff-merge `f9db58cd6`（零冲突）＋ **调和落地并行会话在途 WIP**（报价系统迁移 942-949 + sales 包，
> 中断于 11-05 02:36 无审计档 ⇒ 按先例收口：编译/vet/派生台账门全绿＋补 db-changelog 第 7 面＋gofmt ⇒ `3c9954acb` 推双仓）。
> ⇒ ★★★ **本轮最重发现＝A1【P1】**：报价系统 `cents_per_credit` 被当**元**用（A-14 裁决钉死是**分**）⇒ 收入虚大 100×
> （order_amount 维恒触发＝"只审例外"失效、毛利下限永不触发、945/946 快照金额全错）——**「WIP 能编译＋登记面全绿」拦不住
> 纯算术缺陷，唯一闸＝纯单元测试**（本轮补 `TestPrecomputeLinesA14Unit`）。真库腿（sales realdb **首次跑绿 10/10**）另钉出
> **A-11：ListSellable 扫 941 可空列非指针目标 ⇒ 市场表为空（生产 211 现状）时工作台必 500**（已修）。
> ⇒ ★ 14 项修复全部落地并验证（A2 分享钉版 / A3 sent 修订强制重审（redLineVerdict 单一判定核）/ A4 夹具 scratch 守卫 /
> A5 GET 死分支 / A6 匿名可达行级写入有界 / A7 946 补 FK（prepared 改字节，936 先例）/ A10 日志 / B1 基线副本 /
> **C1+C2 秘密扫描器 BLOCK 11→0（占位符约定改写 10 行 docs，不动 baseline）/ C3 F-22 同族 / D3 / E1 台账指针**）；
> 验证：build/vet rc=0、admin 90s/settings/sql.schema/sales 全绿、ursmcheck 关键门全绿（admin-routes 快照从 SSOT 重生成 1 行）、
> 扫描器（钩子同参）rc=0。★ 遗留登记：A6b 限流中间件、A8 毛利分层、A9 version_no 重试、B2/C4/C5/D1（口令历史残留，建议
> owner 轮换）/D2/D4/E3；**报价前端（/operations/sales + 8 语种）与 W0 真单＝下轮实施**。
> **2026-11-05 第 48 增量：245/154 部署断链修复轮（goal 会话）** —— ★ 用户目标＝「检查并修正部署到 245 与 154 的脚本，完成后重新对两强主机进行部署」，报错＝`✗ envs SSOT loader not found: /Users/xutaohuang/workspace/ai-native-workspace/envs/loader.sh`。
> ⇒ ★★★★ **断链点①（用户报的那条）＝ envs SSOT 路径漂移**：envs 仓（halfking/envs）随工作区 2026-11-04 重组（根仓 `85faac2a`：deploy-lib/ → deploy/deploy-lib/）落到 **`~/workspace/ai-native-tools/deploy/envs`**，而 `deploy-seamless.sh` / `clear-upgrade-banner.sh` 内联的**两候选链**（`ai-native-workspace/envs` → `ai-native-tools/envs`）**两个都落空** ⇒ 报的是一条本机**从来不存在**的死路径（看起来像「envs 丢了」，实际是「默认值没跟着迁移改」）。⇒ 修法＝新增 **`scripts/envs-resolve.sh`**（候选链**单一真相** + 显式 `ENVS_ROOT` **必校验**（无效 ⇒ rc=64 + 可执行提示）+ 失败文案与候选链同源）+ **在 source 共享部署库之前**解析并 export（⇒ 共享库 `deploy-prereqs.sh` 的 `${ENVS_ROOT:-<默认死路径>}` 跟着走）+ `lib/pg-instance-env.sh` 探测扩成「新布局优先」。
> ⇒ ★★★★ **断链点②（部署推进到迁移那步才暴露）＝ 共享部署库的「迁移退役判据」把本仓迁移**静默丢弃**：判据原写法是「**前 15 行出现 superseded/deprecated 三个词就跳过**」⇒ 表头提到这些**业务词汇**的 **944_sales_quotes.sql**（状态图例含 superseded）与 **945_sales_quote_versions.sql**（含 superseded_by）被跳过 ⇒ 两张表**从未建出来** ⇒ 946 外键失败 `relation "public.sales_quote_versions" does not exist` ⇒ 部署在「切换前 pending 迁移」中止（**未切符号链接 ⇒ 线上零影响**，但 942/943 已落库）。⇒ 修法在**共享 SSOT 另一仓** `ai-native-tools:deploy/deploy-lib/db-changelog.sh`：退役 = **显式标记行**（标记词必须是注释行第一个词 + 词边界 ⇒ `superseded_by` 不算）+ **跳过打日志** + **单点真源 helper**（两处调用点同走）⇒ 误伤 4 → **0**，不改变任何现存迁移的处置。
> ⇒ ★★ **门（两道，均已接线 Makefile + .workflow/main-verify.yml）**：`tests/envs_resolution_contract_test.sh`（25 断言；夹具由**实现自己的候选链**推导 ⇒ 布局改名不假红；变异臂：旧实现 14 红 / 显式值不校验 7 红 / 文案漂移 1 红）＋ `tests/migration_retired_marker_contract_test.sh`（8 断言；**判据取共享库的真实现** ⇒ 变异臂把 helper 还原成旧子串匹配 ⇒ 同一道门 **5 红并点名 944/945**）。
> ⇒ ★★★ **部署读数（从**干净 worktree** 出发 ⇒ 不带上并行会话的 36 项未提交 WIP）**：**245=2932-b17edaf9**（208s/切换 27s）· **154=2933-b17edaf9**（213s/切换 20s）⇒ 主机内 `/api/system/version` ＝ seq 2932/2933 + sha b17edaf9、healthz/readyz 200、154 公网 `https://llm.kxpms.cn/api/system/version` ＝ 2933 ⇒ 迁移 **942–949 落库 6/6**、凭据解密冒烟 failed=0、admin 密码同步 200 ⇒ 版本戳收敛入库（`92446d2f3`）。
> ⇒ ★ **门禁读数（口径必须写清谁的红）**：主树 `make guards-gate-scripts` 停在 `tests/deploy_sync_prune_contract_test.sh`（23/1，缺 `deploy/phase0/optimization.env`）⇒ 归因＝**并行会话未提交的** `scripts/deploy-seamless-docker.sh` + 该门自身在途改动（这两文件在我提交区间**未被我改过**；同一提交在干净 worktree **24/0**）⇒ **不改闸门、不动他人 WIP**；另两条 `deploy_sops_test.sh` / `scan_secrets_enumeration_contract_test.sh` 本机 **rc=64「需要 bash>=4」= 显式 skipped**；登记册门 **PASS=166 FAIL=0**。
> ⇒ ★ **遗留（登记）**：① 主树 local `main`（`f67120923`）与 `origin/main` **分叉**（本地另有并行会话 WIP ⇒ `git merge origin/main` 被 git **中止** ⇒ 未强行处理，交由其自行合并）；② `ai-native-tools` 工作区 checkout 同样分叉（`bc460fed` vs origin `f0def7d6`）；③ `scripts/deploy-lib.legacy/db-changelog.sh`（**休眠**的历史副本）仍是旧子串判据 —— 未动（部署不加载它）。
> ⇒ ★ **通用式见 `LESSONS.md` §636**（路径类默认值是「迁移债」/ 判据要区分「标记」与「提及」/ 跨仓修复要配跨仓的门 / 中止在切符号链接前的部署＝线上零影响但 DB 可能已部分迁移）。
> **2026-11-05 第 47 增量：glm-5.3 请求洪水溯源与 ASM 分析回路熔断轮（跨仓：溯源在网关库 / 修复在 ai-session-manager）**
> （goal 会话）：★ 用户目标＝「查实 211 上大量 glm-5.3 请求（如 `045b5104e328694a78f173dc151fe0f8`）是做什么的、来源于哪里、
> 有无必要、能否换 glm-5.3-flash」＋「修正发现的问题，提交推送」。
> ⇒ ★★★★ **溯源结论（acc-pg17 真库 + 容器 env 逐字）**：这些请求＝**ASM 会话分析 agent**（`session-analyzer` worker → Pi
> runner，`gw_asm_agent_<sha256>` 每物理尝试一个会话），runner 经公网入口用 **`deploy-smoke-245-20260815-160010` 冒烟 key**
> （api_key 105，`rate_limit_rpm=0`，累计 459,529 次/$18,374.37）回调网关 ⇒ `AGENT_MODEL=glm-5.3` 钉死，而 glm-5.3 全库
> **仅 1/14 绑定可路由**（凭据 62 智谱 coding），该凭据上游 **429 code 1310「周/月配额耗尽，2026-11-07 01:00 重置」** ＋
> 凭据 29「1分钟最多30次**含失败次数**」⇒ 1/s（60/min）> 30/min ⇒ 自维持死循环：实测 **60 次/分钟 × 近 4 小时 = 12,668 次
> no_candidate（99.7%）、3,260 万 prompt token 记账零产出**；job 级退避（5min/1h）拦不住 1s claim ticker 拉下一个任务。
> ⇒ ★★★★ **「能否换 glm-5.3-flash」＝不能解本问题**：62 的 flash 腿**同账号同配额墙**（同文案同重置时刻）、99023（freellm-unified
> free）探针时好时坏 ⇒ 健康替代＝**minimax-m3**（11 月 2,444 次 99.2% ok、独立供应商、runner 注册首模型＋worker 代码默认）。
> ⇒ ★★ **修复（ASM `a9d14cd`→`56e4163`）**：分析回路加**全局熔断**（transport 错误/`upstream_unavailable` ⇒ 30s 起步翻倍
> 10min 封顶冷却，ErrCooldown 不 claim 不调模型，Validate 通过即清零，/status 暴露 `analysis_cooldown_until`）＋
> cooldown_test 三腿（纯单元＋一次性 PG 真库腿）＋ store_test 续跑腿白盒清熔断；**网关侧零改动**（洪水期间网关行为全部正确）。
> ⇒ ★★ **211 配置修复已执行（11-05 00:52–00:57，主机恢复后）**：状态重测 glm-5.3 **18/18 全不可路由**（62 变
> node_probe_failed、63 quota_permanently_exhausted）、minimax-m3 0/6 ⇒ 模型改 **`minimax-m3.1-flash`**（凭据 29 唯一可路由）
> ＋ `AGENT_DAILY_TOKENS=40000000`（分析器日耗实测 10-13M 的 ~3.2x；兼作洪水刹车）＋ runner 注册 flash ＋ 删 release compose
> 内联 `'0'`（仓内同步，ASM `f60a93d`）＋ 今日 119.2M phantom 预扣按账实归零。验证：/status=40M ✓、**glm-5.3 自 00:47:14 归零**、
> m3.1-flash 7/7 成功（~1.75 次/分 ≪ 凭据 29 的 30/min）。
> ⇒ ★★ **两项挂起全部收口（11-05 01:10–01:30，审计档 §3.6）**：① **熔断二进制上线**＝标准 release 流程
> `deploy.sh deploy --apply --release 20261105-f60a93d`（★ 并行 WIP 挡住主工作树 ⇒ 干净 detach worktree 作 `--source`；
> ★ activate() 从 `$base/config` 拷贝 ⇒ **先同步规范 config 否则下次 release 回滚修复**；免 --migrate＝schema 自 ac5421f8 零变更；
> 熔断二进制实存＝distroless 无 shell ⇒ docker create+cp 拷出 grep 特征串）；② **凭据分离**＝铸 600s admin JWT
> （HS256/`LLM_GATEWAY_SECRET_KEY`/user 41）走 `POST /api/keys` ⇒ **key 228 `sk-YEYaxeR****`（rpm=30）**换
> `RUNNER_LOCAL_API_KEY` ⇒ 验证新 key 8/8 成功＋**旧冒烟 key 的 asm 流量归零**（admin 密码失效不是死路：签名密钥在容器 env）。
> ⇒ ★ 遗留（登记）：freellmapi 自有流量仍在无限流冒烟 key 105 上、其 4 死模型 fallback 洪水仍在——属 freellmapi 仓＋105 加限流的
> owner 裁决。
> ⇒ ★ 通用式见 `LESSONS.md` **§631**（失败作用域决定退避载体 / 凭据用途字段不是约束 / 换模型先查配额池同族 / 0=关闭的阀门语义）。
> **2026-11-04 报价系统 MVP 下一步方案轮（规划 · 零代码）——两轮外部调研＋本地接地把路线图变成任务卡**
> （goal 会话）：★ 用户目标＝「结合头脑风暴文档 → 查询网络中高星项目与方案 → 结合本地代码 → 给出下一步方案及实施规划」。
> ⇒ ★ **产出＝`docs/audit/2026-11-04-报价系统MVP-下一步方案与实施规划.md`；通用式见 `LESSONS.md` §630**。
> ⇒ ★★★ **三个增量结论**：① 版本化/快照/审批的行业答案全部一手取证且与《风暴档》预判**同构**（ERPNext「Cancel and amend 禁止静默改已发出报价」＋ Invoice Ninja 七态/**Expired 派生态不落库**/line_items 内嵌 JSON 快照 ＋ Odoo locked 正交布尔/purchase_price 创建时定格 ＋ DealHub 三维阈值「政策内自动放行只审例外」/阈值突破率自校准）——方案从「我们要这么做」升级为「业界都这么做，按业界形态落」；② **LLM 计费生态全部收敛到「输入价+输出价+子资源扩展位」原子结构、基础价×折扣正交分解且折扣从不改基础价**（one-api 三倍率公式进消费日志可复算/LiteLLM 显式 0 才算 free＝NULL≠0 业界同构/OpenRouter max_price 硬闸与 sort=price 偏好分离）⇒ 正面支持「成交不改价」：零售价存基础价、折扣走 `discount_pct`（939 已同形）；③ 迁移号落定 **942–948**（＋审计日志并入头表或 949），实施分 **W1-T1/T2·W2-T1/T2·W3** 五卡、每卡带验收门，W0 第一张真单仍是上线门。
> ⇒ ★★ **三决议**（细化《风暴档》）：版本边界＝**sent 时刻**＋快照物化（毛利预计算落库）＋Expired 派生态；审批＝DealHub 三维（毛利下限/折扣深度/单笔金额）＋ `trigger_reasons jsonb` 可复算；零售价快照＝基础价×折扣正交＋毛利五因子与流水 `detail` 同构（W3 对账有逐列面）。待裁决新增 4 条（9–12：折扣阈值/触发率告警线/阶梯价不进 MVP/Expired 派生）。
> ⇒ ★ 本地接地（全部实测）：941 视图 `price_confidence` 四档 CASE **已落列**（工作台 C3 门＝`WHERE price_confidence='trusted'` 零新增代码）；`settings/cny_fx.go` FX 单源锚点；`maas/rates.go` 零售价兜底链（租户价→manual→全局）；`AuthContext.Role` 三角色＋super 显式越权先例（`maas_my_handlers.go:62`）；`registerMaasMyRoutes` 路由接线先例。
> **2026-11-04 报价系统 MVP 头脑风暴轮（规划 · 零代码）**
> （goal 会话）：★ 用户目标＝「继续，并用头脑风暴仔细思考运营思路：供给池/需求池/报价工作台/分享传播/历史记录/权限审计 ＋ 价格治理/毛利红线/内外隔离/版本化」。
> ⇒ ★ **产出＝`docs/audit/2026-11-04-报价系统MVP-头脑风暴与运营思路.md`；通用式见 `LESSONS.md` §629**。
> ⇒ ★★★ 三个判断：① 三根数据柱子真库活读数（2026-11-04）全空/极薄（P3=2339 绑定仅 **27 真价**、P2=940 三表 **0 行且无同步器**、P5=`model_credit_rates` **0** 行）⇒ MVP 形态＝**引导式工作台＋数据置信门**（C3 可信档是工作台准入），不是自动比价引擎；② **冷启动不需要覆盖率**——27 真价够第一张真报价单，上线验收＝一张真实询价走完全链（W0 前置半天），防 salesdecorate「链路通了没人走」（供给卡 7/报价单 1）重演；③ `credit_ledger_hot` 仍 0（default 租户**按设计**跳过计费）⇒ MVP 只报报价毛利＋漏斗，实收侧显式标「待租户计费激活」，**禁止为出数放开保护门**。
> ⇒ ★ 建议迁移号 942–948；RLS 决策预登记＝自营内部运营表**不挂** `tenant_isolation_*`（角色闸应用层，实施轮复核）；8 条待裁决在 §8（红线数值/成交落价通道/分享链接公开性/宿主仓等）。
> **2026-11-04 第 46 增量：48 小时修订审计轮——5 组并行只读取证（含 211 真库活体复核）＋ 12 项修复落地 ＋ 两道新机器门**
> （goal 会话，`docs/全面审计v3/2026-11-04/`）：★ 用户目标＝「对 48 小时内的修正进行审计……编写架构设计与审计检测文档保存到 docs/全面审计v3，制定详细执行方案后审计执行」。
> ⇒ ★ **开工对齐**：工作树 5 文件在途＝245 部署 build_seq 2929 部署侧效应 ⇒ 按先例（版本戳随轮收敛）先收敛入库清树（`3b2b4c996`）。
> ⇒ ★★★ **承重论断独立复核 14 项全部成立**（运营计费 934-941 六面/AF-1 FX 口径/AF-15 三代一把尺/W3 owner 收窄/memoraprewarm 有界 dedup/session.closed.v1 歧义拒绝/分类器锁步门/恢复投影排除集/无可用后端归责分写/W1 菜单/i18n——**A 组含 211 真库只读活体**：934-941 全 applied、936 自愈 PK 实存、视图 security_invoker=true、embeddata 逐字节）；
> ⚠ 实施档两处措辞被订正：AF-1「单测」实为纸面算术（maas 无落地单测）、`pg_policies` 「51+5」实为 **55=51+4**。
> ⇒ ★★★★ **本轮真缺陷头部三条**：① **F-1【Medium】hooks/compression 第二压缩链未过模态门**（`compaction.go:393`+`summary_client.go:87-93` 无过滤 `GetCandidates`，默认启用装配，`40bbe7a54` 只修了 executors 侧副本、登记 0 处提及 ⇒ **「同族第二处未登记」实证**；embedding 绑定同名 ⇒ chat 形状 400 ⇒ 压缩静默失败）；
> ② **F-5【Low-Medium】session_sanitize_matches 租户解析器与已修 outbox bug 同形**（撞名同族全仓残留 6 处，修有归责后果 2 处：sanitize 视图挂错租户＋output_compliance owner 子查询归属错租户；其余登记）；
> ③ **F-7【跨仓】SSO 票据无 jti/一次性＋token 进 URL**（TTL 5min/白名单/fail-closed 缓解在，验证端在 FreeLLMAPI ⇒ 网关单方面加无效 ⇒ 待裁决）。
> ⇒ ★★ **12 项修复**（6 修复代理两波、共享树零文件冲突）：压缩模态门（接口编译期锁＋解析失败不退回无过滤）· memoraprewarm dedup 键带租户＋panic 补记终态＋**漏斗求和闭合测试**（B 组指出的求和断言缺口）· 音频车道 owner 贯通（`keyMetaFromKeyInfo` 同源）· sanitize 两档口径（scoped 谓词/super DISTINCT 歧义拒绝）· SSO 签发审计（seam 先例照抄）· scoring 注释对齐 AF-15 · **继承 F-11** myOwnerScope 死 bool 删除＋auth 分支显式 · **继承 F-2 延伸** 活动对账 HTTP 面（`GET /api/admin/maas/campaign-reconciliation` super-only，balanced 判全量，admin-routes 610→611＋post-surface +1 两台账重生成）。
> ⇒ ★★ **两道新机器门（均带变异臂＋还原 md5 对拍）**：**menu-config 反向同步门**（`web/src/config/menuConfigSync.test.ts`：导出件≡appNav.ts 逐组逐项逐字段，忽略 exported_at，388fe0bd6 修漂移时欠的门）＋ **FK→PK/UNIQUE 静态门**（`scripts/ursmcheck/fk_target_key_gate_test.go`：词法净化器＋三语句形态解析，覆盖判据=列集精确相等（PG 真实语义），基线 403 表/498 键/59 FK ⇒ **3 条真缺口全为「运行期 $$ DO 块自愈」形态**（936/612/825）逐条豁免＋双向棘轮＋旗舰案例可见性断言；包已登记 Makefile GUARD_PACKAGES_SLOW 无需额外接线）。
> ⇒ ★ 验收读数：`go build`/`vet`（9 包）rc=0；memoraprewarm **-race** ok 2.859s；compression 族＋session_replay＋streaming/audio＋admin/maas/autoroute/gateway 定向＋ursmcheck（AdminRoutes/PostSurface/FkTarget）全绿；web vitest src/config **60/60**＋vue-tsc rc=0；gofmt 改动文件仅 2 处**既有漂移**（与 HEAD 的 gofmt diff 逐字节相同 ⇒ 零新增）。
> ⇒ ★ **共享树 6 代理协作实录（§618 族新例）**：逐文件划界零冲突；两次 vet 假红均系他人在途半成品（收敛后复测 rc=0）；**一次 `git checkout --` 误伤自愈**（变异臂还原时把自己未提交改动一并抹回 ⇒ 按上下文重做＋md5 对拍一致）⇒ **共享树上做变异臂禁止用 `git checkout`，恢复必须无条件执行＋md5 对拍**。
> ⇒ 审计档 `docs/全面审计v3/2026-11-04/{01-审计方案,02-审计发现与缺陷台账,03-修复与验证记录}.md`；README 流水已追加。**明确没做**：SSO jti（跨仓）、N2/N4/N5 展示面同族残留、N6 唯一键设计层（需迁移）、936 down 补 PK（字节冻结，下触顺带）、部署（指令止于推送）。
> **2026-11-04 第 45 增量：运营管理与价格计费体系 P0/P1 实施轮——迁移 934–941 ＋ 流水闭环 ＋ AF-1/AF-15 修复 ＋ operations 菜单 ＋ 211/245/154 部署**
> （goal 会话，承接第 44 增量设计档）：★ 用户目标＝「按设计档完成实施 → 脚本部署 211 测试验证 → 通过后部署 245 与 154」。
> ⇒ ★ **W2-T0 真库重测（211 `acc-pg17/llm_gateway`）推翻 AF-3 主假设**：`credit_ledger_hot` **也是 0 行**
> ⇒ 「量具假象」不成立；真因＝`chargeTokensWithMultiplier` 的 `tenantID=="default"` 跳过门
> ＋ 生产 24,114 条请求（24h）**全部 default 租户** ⇒ **计费从未发生**（AF-11/12 观测闭合）。
> 另实测：RLS 51 条、钱包 9 行（4 非零）、`model_credit_rates` 0、cents=0.01/CNY、`request_logs` 父表生产不存在（分区+视图布局）。
> ⇒ ★★ **迁移 934–941 全部落库（up+down 各 8 条，六面登记＋第 7 面 `docs/db-changelog.md`）**：
> 934 商业补列（plans+9/topup+4）· **935 流水三列两表同改 ＋ hot 补 RLS（实测 relrowsecurity=f）＋ promote 函数重铸 10→13 列
> （698 版硬编码列清单⇒不重铸=新列 promote 边界静默丢）＋ 两臂读视图扩列+security_invoker（实施时定夺：扩既有
> `credit_ledger_with_current_month`，Go 零消费方的存量视图，不建重复的第二视图）** · 936 套餐按模型额度 ·
> 937 活动族（redemptions RLS）· 938 租户预算 · 939 租户分层价 · 940 市场三表（AF-8 canonical_id 解析列）·
> 941 供应商视图末尾追加 7 列（C2 币种可比才算/C3 四档镜像健康门口径）。
> 派生台账三份重生成 **863→872**（selftest 50/0 先行）；admin-routes 604→610；post-surface +6。
> ⇒ ★★ **代码修复**：① **AF-15 零价分档**（v1 `scorePrice` + v2/simplified 共用函数 + is_free SQL/Go 双侧收窄
> ——round-1 五模式维持优待、round-2 未知价中性（P75 锚定 50/无池信号 80）；executors 不动（A-4 待影子））＋
> 四门测试（三代 v1≡v2 逐位一致 + 三方向变异臂）；② **AF-1 毛利口径**（`consumption_detail` 经
> `CNYToUSDFX`；A/B=7.2× 与设计档逐字吻合，现网流水 0 ⇒ 修复零扰动）；③ **流水扩参**（`writeLedgerWithBalanceDetail`
> 11 参收口 + 七写点逐个过 + 五因子 detail `{rate_in,rate_out,tokens,discount,rate_multiplier}` + ownerUser 贯通
> handler `keyOwner`；`ListLedger`/对账 C 侧改两臂视图）。
> ⇒ ★ **W1 菜单重组**：`operations` 组（一节两态）＋ tenant-portal 退役（5 项原 path 迁入）＋
> `/pricing`、`/model-pricing`、`/admin/reconciliation` 迁组（path 不变）＋ **board-operational 收编并移出
> KNOWN_ORPHANS**（AF-5；隔离遗留面以 super-only 兜底登记）＋ 结算双入口删除（页内已有双视角页签）＋
> i18n 8 语种 +2 键。**W3 后端**：`/api/maas/my/{usage,requests,bills/,budget}` ＋ `/api/maas/campaigns*`
> （owner 收窄 A-11：JWT 用户强制本人、super 可显式越权、legacy key 全租户；活动兑现 campaign_id 双写）。
> ⇒ ★ 验收读数：`go build`/`vet` rc=0；maas/autoroute/admin/streaming/sql/.../ursmcheck(关键门)/installer **全绿**；
> `vue-tsc` rc=0；`vitest` **291 文件/3005 passed**；`vite build` ✓；navCoverage 6/6；i18n 四门 PASS。
> **踩过的门（全转绿）**：db-changelog 近邻窗口门（第 7 面）· embeddata 逐字节门（改 canonical 后忘重拷 935）·
> ursmcheck 600s 假红（口径必须 1800s）· reachability 漏 `--columns` 制造假漂移（3 条存量行误翻 installer-bug，
> 工具参数静默降级是假漂移制造机）· **clobber 链第 935 面**（promote 函数第三定义方须进 intentional_function_chains）·
> **间接读点清单**（`maas/my_usage.go` 2 处 requestLogsSource 拼接须进 manifest_test 登记表＋快照 157→159）。
> ⇒ ★★ **三台部署全过（211/245/154）**：211=f2af856c/2946 蓝绿 a→b；245=2929-bac2be17（14 新应用含
> 934–941；HEAD 被并行会话推到 bac2be176，`is-ancestor` 实证我三提交在血统内）；154=2930-414a5010
> （与 245 共库 ⇒ 无 pending）。**211 六门全过**：promote 搬运不丢列实证（hot 0/分区 1/13 列齐）、
> campaign 双写对账 500=500、**AF-1 真库 A/B=精确 7.2×**、四新端点 200+owner 收窄生效、测试数据清理。
> **部署途中环境缺陷两处（已收口）**：① Redis 库号漂移（db15..db8 全是网关自占残留键 ⇒ allocate 的
> 自占识别只认 claim 库 ⇒ 假「全被占用」⇒ 清 9 死库，db6/7=asynq 他服保留）；② **936 FK 42870**
> —— 211 的 `subscription_plans` 是基线 dump 形态（code UNIQUE 有、**主键无**）⇒ 936 幂等 DO 块补 PK
> （prepared 未应用 ⇒ 改字节合法，`f2af856c1`）。★ **移动端构建既有红**（`requestDetail.test.ts` 的
> as-const includes(string) TS2345，合并带入 ⇒ web 不跑 `-b` 所以本地不显形 ⇒ 211 部署引擎拦下）⇒
> string[] 显式放宽收口（`644903c8b`）。实施档
> `docs/audit/2026-11-04-运营管理与价格计费体系-P0P1实施轮-迁移934-941与流水闭环与AF修复.md`；
> 通用式见 `LESSONS.md` **§627**。
> **2026-11-03 第 44 增量：两规划合并审计 +「运营管理/价格计费」总体设计与实施规划（规划轮 · 零业务逻辑改动）＋ AGENTS.md 第二批分档归档**
> （goal 会话）：★ 用户目标＝「根据《模型价格情报-供应商评分-路由参数与运营营销方案》与《运营管理子菜单与用户计费体系设计》
> 两档**做审计**，形成**完备设计方案**，再**规划实施**，落成文档」。
> ⇒ ★ 产出＝`docs/audit/2026-11-03-运营管理与价格计费体系-两规划合并审计与总体实施设计.md`：
> **46 项承重论断复核（40 成立 / 4 引据偏 / 2 需修订）＋ 审计发现 AF-1…AF-15 ＋ 统一总体设计
> （迁移号 934–941 逐条分配 ＋ RLS 矩阵 ＋ API 三级面 ＋ 前端 /operations/* 信息架构 ＋ A-1…A-16 合并裁决清单）
> ＋ 实施规划（W1–W6 工作流 × P0–P3 阶段，每张任务卡带验收门 ＋ 风险登记册 ＋ 明确不做）＋ 与两档原文的差异回写表**。
> ⇒ ★★ 要点①（AF-3）：**`credit_ledger`「0 行」疑似量具口径假象**——写入方 `writeLedgerWithBalance`
> （`maas/service.go:689`）**直写 `credit_ledger_hot`**（2026-07 生命周期设计，`bg/partition_manager.go:1806` 提升进分区），
> 计数打父表读不到未提升行；且 `ListLedger`/对账 C 侧只读父表 ⇒ 读侧必须 **hot ∪ 分区两臂并读** ⇒ P0 第一步＝先重测（W2-T0）。
> ⇒ ★★ 要点②（AF-1）：**`cents_per_credit` 单位语义两处相反**——`maas/bills.go` 当 CNY 分 vs
> `maas/consumption_detail.go:116` 当美分 /100 折 USD ⇒ **`GrossMarginUSD` 混币种**（偏大约 7.2 倍假毛利）；
> 两档各引其一（0.01 vs 0.1 分）而底层本就冲突（DDL 默认 0.1 / 种子行 0.01）⇒ 裁决 A-14。
> ⇒ ★★ 要点③（AF-2/4/15）：**`/ops/*` 前缀已被 opsplatform 组占用**（`/ops/vibecoding` ＋ 5 条重定向）⇒ 运营组
> 路径改 **`/operations/*`**；**CostWeight 默认实为 0.15 非 0**（`router_scoring.go:93`，注释过时）⇒ 零价缺陷在
> 执行层**是活的** ⇒ 修法按 **billing_mode 分档**（round-1 套餐 155 条维持优待、round-2 按量未知价才中性）——
> 《价格档》P0-4 的一刀切「blended<=0 → 中性」会误伤周期套餐。
> ⇒ ★ 本轮同时完成上轮交接阻塞项：**AGENTS.md 第二批分档归档**（第 1730–5014 行共 3285 行移入
> `AGENTS-archive-20261031.md`）＋ **R186/R188 索引补标 ⇒ `TestAgentsMdRMarkerIndexPointsAtArchive` 既有红已修**。
> 通用式见 `LESSONS.md` **§626**；★ 正文全在设计档，本条只留索引＋要点。
> **2026-11-03 第 43 增量：「运营管理」子菜单 + 用户计费体系设计（规划轮 · 零业务逻辑改动）**
> （会话 `mvs_90cedb30ecfc440ebd00587455defbf3`，root goal 会话）：★ 用户目标＝「把运营策略（含套餐
> 设定）、价格设定、市场价格采集、对比分析、活动/套餐/计费手段放进**新子菜单「运营管理」**；用户与
> 租户选套餐 → 充值计费 → 结算账单；每个用户可看请求历史/充值/费用/结算；**从用户角度**分析功能需要」。
> ⇒ ★★★ **本轮性质＝规划轮：零代码/零迁移/零页面**，产出＝设计档
> `docs/audit/2026-11-03-运营管理子菜单与用户计费体系设计.md`（41KB）；通用式见 `LESSONS.md` **§624**
> （★ 取号：实测最高 §623 ⇒ 连号 §624；★ 8 条通式）。★ **按上一轮约定，AGENTS.md 只留索引 ＋ 指针 ＋
> 要点，正文全在设计档。**
> ⇒ ★★★★ **要点①（第一判断，真库实测 `acc-pg17`/`llm_gateway`）：★ 商业骨架早已在仓里，但几乎是空的**
> —— `subscription_plans` 3·`topup_packages` 3·`tenant_credit_wallets` 9·`billing_orders` 3·
> `maas_monthly_bills` 3·`maas_credit_consumption_buckets` 266 ⇒★ 而 **`tenant_subscriptions` 0**、
> **`maas_coupons` 0**、**`maas_invoice_requests` 0**、**`maas_reconciliation_findings` 0**、
> ★★ **`model_credit_rates` 0**（⇒ 零售价不存在，全部按 `maas_settings` 全局默认
> `base_credits_per_1m=600`/`out=1500`/`global_discount=0.82`/`cents_per_credit=0.01` 计价）、
> ★★ **`credit_ledger` 0**（⇒ 用户侧「我花了多少」没有底账）⇒ 且 `maas_settings` 的
> `alipay_account`/`wechat_mch_id`/`stub_qr` **全空** ⇒ ★★ **支付通道一个都没配**
> ⇒ ⇒ ★★★ **判据：本节第一件事不是做页面，是先把「价 → 扣 → 记 → 账单 → 对账」跑通一次真实闭环**。
> ⇒ ★★★★ **要点②（用户侧最缺的那块不需要新数据模型）**：`api_key_owner_user` 已在 `_request_logs`
> （`sql/schema/01-schema.sql:3759`）＋ `maas/consumption_detail.go:63,91,95` 已投影/过滤/GROUP BY ＋
> `web/src/api/maas.ts:418` 的 `getAdminMaasConsumptionDetail(tenantCode,days,ownerUser)`
> ★★ **三层都齐，只缺「租户可见端点 ＋ 鉴权收窄」** ⇒ ⇒ 判据：**先查数据维度在不在，再决定建不建表**。
> ⇒ ★★★★ **要点③（★ 混淆陷阱 ＋ 已核实结论）**：`/admin/subscription-tiers`（「订阅层级」）
> **不是商业套餐** —— 实测它读 `GET /api/admin/tiers`，是 **nginx 反代的跨仓 `license-authority`
> 独立 service**（`SubscriptionTiersView.vue:1-13` 明写「不参与授权判断」）⇒ ★ 它恰好也叫「订阅」
> 「层级」且**也有 `price_cents`** ⇒ 最易被误当「套餐管理」而错误合并 ⇒ **必须显式分开**。
> ⇒ ★★ **已核实**：`credit_ledger` 现有 10 列（`entry_type`/`ref_type`/`ref_id`/`pool`/`balance_after`
> ＋ 6 分区 ＋ 已有 RLS）**够用、不重建表**，只**加三列** `owner_user`/`campaign_id`/`detail jsonb`。
> ⇒ ★★ **实施前必读的仓内硬约束（设计档 §9 逐条列 11 条）**：① **孤儿路由门**
> `web/src/config/navCoverage.test.ts:208-236` ⇒ **新增 router 路径必须能从菜单到达** ⇒ 新页面**一律**
> 挂进新组 `operations`；② **i18n 8 语种**新键全写 ＋ `i18n:key-parity:check` ＋ ★
> `i18n:shared-dead:check` 是**死键棘轮**；③ **间距尺棘轮**
> `web/scripts/design-spacing-scale-decisions.json`；④ ★★ **租户隔离的真正载体是 RLS**（库内 **51** 条
> `tenant_isolation_*`）⇒ **每张新租户可见的表必须带策略**；⑤ 新增表的迁移要**六面登记**。
> ⇒ ★★ **本轮明确没做**：零代码/零迁移/零页面；未改 `appNav.ts`（菜单重组在设计档 P0-5）。
> ⇒ ⚠ **★ 交接阻塞（下一轮必读）**：本轮落笔后 **AGENTS.md 约 523.4KB / 上限 524,288 字节 ⇒ 余量不足 1KB**
> ⇒ ★★ **下一轮必须先做分档归档**（把旧轮正文移进 `AGENTS-archive-*.md`、AGENTS.md 只留索引行，★ 先例
> `AGENTS-archive-20261031.md`；★ 棘轮**只降不升** ⇒ **不能调高上限续命**）⇒ ★ 且
> `TestAgentsMdRMarkerIndexPointsAtArchive` **当前是既有红**（R186/R188 不在归档文件里）⇒ 归档时一并修。
> **2026-11-03 第 42 增量：模型价格情报 ⇒ 供应商评分 ⇒ 路由参数 的一体化运营/营销方案（规划轮 · 零业务逻辑改动）**
> （会话 `mvs_90cedb30ecfc440ebd00587455defbf3`，root goal 会话）：★ 用户目标＝「拉最新 →
> 提交推送主分支 → 查模型管理与价格管理（含供应商价与内部核算价）→ 结合 `salesdecorate`
> 整合外部原厂价/MaaS 价/模型清单做成营销方案 → 头脑风暴：能否用外部 MaaS 定价对比我们的
> 供应商价、给出供应商模型评分并作为路由算法的一个参数 → 给完整方案」。
> ⇒ ★★★ **本轮性质＝规划轮：零业务逻辑改动**，产出是一份带**真库实测读数**的方案档
> （`docs/audit/2026-11-03-模型价格情报-供应商评分-路由参数与运营营销方案.md`）。
> ⇒ ★★ **拉取侧**：本轮开工时本地 `main` 落后 `origin/main` **52 提交**（`git fetch` 后实测）
> ⇒ **可快进、零冲突** ⇒ `git merge --ff-only origin/main` ⇒ 落到 `f4f86fcd5` ⇒ ★
> **工作树开工时是干净的** ⇒ ⇒ **「提交代码并合并到主分支中推送」＝把本轮方案档 ＋ 两处登记
> 提交并推 `origin/main`**（★ 本轮无业务代码可提交 ⇒ 交付物就是方案本身）。
> ⇒ ★★★★ **两侧现状（一律 2026-11-03 真库实测，`acc-pg17` 的 `llm_gateway` ＋ `wa-pg17` 的
> `salesdecorate`）**：
> ★ **网关侧三套价格体系、三个单位、互不相通** —— ① **供应商价**（`credential_model_bindings
> .unit_price_*_per_1m`，USD/1M）② **基准价/原厂标准价**（`models_canonical.baseline_*`，迁移 826，
> ★★ **只告警不参与金额计算**，唯一写者 `bg/pricing_baseline_sync.go:363`，SSOT
> `bg/data/model_baseline_prices.json` **仅 16 模型 / 仅 3 厂商**：anthropic 8·minimax 7·openai 1）
> ③ **客户零售价**（`model_credit_rates.credits_per_1m_*` ＋ `maas_settings.cents_per_credit`，
> **积分**计价）⇒ ★★ **毛利只有一处算**：`maas/consumption_detail.go:116-119`
> （`TenantRevenueUSD − UpstreamCostUSD`，**事后报表字段，从不进路由**）⇒ ★★ 全仓**无**「加价率/
> 目标毛利/相对基准折扣」的一等概念 ⇒ ⇒ **本轮第一判断：营销与成本决策之间缺的不是功能，是一根线**。
> ⇒ ★★★★ **「有价」这件事实测比想象中薄**：`credential_model_bindings` **2328** 条 ⇒ 有价仅
> **177**（7.6%）⇒ 其中 **141 条是 0/0 空价**（79.7%）＋ **9 条正价但输入=输出**（占位形状，
> ★★ **9 条全部仍 routable**）⇒ **真正像真价的只有 27 条** ⇒ 币种 USD 170·CNY 7；
> 计费模式 `token_plan` **155**·`per_token` 14·`token` 4·`code_plan` 3·`monthly` 1 ⇒ ★★
> **87.6% 的有价绑定是周期性套餐（边际成本≈0）**；`v_supplier_price_vs_baseline` 2283 行，
> `has_baseline` 162 行，**能算出倍率的只有 10 行**。
> ⇒ ★★★★ **`salesdecorate` 侧：行情层已厚、商业层几乎为空** —— `platform` 25（`is_vendor`
> **14** 原厂渠道）· `model` 1232 · `model_alias` 1731 · **`price_observation` 3744**（覆盖 1230
> 模型，`max(run_date)=2026-11-03`，每日累积历史不删）· `price_change` 41 · `source_snapshot`
> 203 · `crawl_run` 115 · `sales_fact` 503 · `market_fact` 35 · `parse_coverage` 49 · `alert` 42
> ⇒ ★★ 而 **`supply_card` 只有 7 张、`quote` 只有 1 张（`draft` 测试单）、`price_lead` 1 条**，
> 且 7 张卡的 `margin_pct_input/output` **全部恰好 20.0000** ⇒ ⇒ **加成率是全局默认值，不是按
> 模型/客户/渠道分层算出来的** ⇒ ⇒ ★★ **「链路通了但没人走」比「链路没通」更贵**。
> ⇒ ★★★★ **接合可行性实测（把两仓模型目录拉下来本地归一求交）**：全量交集 **361 = 网关 1217 的
> 29.7%**（★ 看着像做不了）⇒ ★★ **但「有价批次」交集 32/36 = 88.9%** ⇒ ⇒ ★★★ **可行性论证的
> 分母必须取「真正会走这条链路的那批」**，取全集会把「大部分模型没价」这个已知且无关的事实
> 误算成「接不上」⇒ ⇒ **第一批不需要统一两仓目录就能跑通**。
> ⇒ ★★★★ **已经在量上的四个模型（FX 7.2）**：**`claude-opus-4-8` 我方实付 $10/$25 vs 原厂
> $5/$25 ⇒ 输入 2.00×**、市场（deepinfra ¥33.38/¥166.92 ⇒ $4.64/$23.18）⇒ **2.16×**；
> **`claude-fable-5` $15/$50 vs 原厂 $10/$50 ⇒ 输入 1.50×**、市场 $9.27/$46.37 ⇒ **1.62×**
> ⇒ ⇒ ★★★ **我们在 Anthropic 系买贵了**（两条已超 `supplier_price_drift` 的 1.5× 阈值）；
> 反向侧 `glm-5.1` 我方 $0.1/$0.1 vs 市场（ppio ¥6/¥24）$0.83/$3.33 ⇒ 「便宜 8.3 倍」
> ⇒ ⇒ ★★ **那不是折扣，是我把价填错了**（占位形状）⇒ ⇒ **「先清数据、再做比价」不是洁癖，
> 是比价结论成立的前提**。
> ⇒ ★★★★ **核心设计（三层，本轮最值钱的部分）**：① **五价坐标系**（P1 原厂刊例价 / P2 市场
> 最低价 / P3 供应商实付价 / P4 网关侧基准价副本 / P5 客户零售价）⇒ ★★ **P1 与 P4 不是两个东西**
> （826 明写 `baseline_price_vendor` 是「基准价所引用的厂商，**不是实际被计费的供应商**」）；
> ② **供应商竞争力评分拆三个子分数、不合成一个** —— **C1 采购折扣率**（`1 − 供应商价/原厂刊例`，
> **NULL=无原厂价（未知）≠ 0=正好等于原厂价**）＋ **C2 市场位势**（`市场最低价/供应商价`，>1
> = 我们更便宜，**必须排除我方自己的平台**）＋ **C3 价格置信度**（★ **是门不是分**：可信 /
> 对称占位 / 空价 / 币种未知四档，★ 直接复用本仓既有健康门 `offer_price_looks_like_placeholder`
> 的口径与措辞，**不新造判据**）⇒ ⇒ ★★★ **拆分的真正理由不是「好维护」，是「符号」**：
> 市场比价「买得便宜」是**加分**、路由成本「花得多」是**减分** ⇒ 合成一个数**必然要手工翻
> 符号** ⇒ 翻错就是「买得便宜的供应商被降权」⇒ 拆开后**市场参考价是固定分母 ⇒ 符号天然正确**
> （★ 且 **C3 必须前置**，因为 C1/C2 的分子都是我方实付价）；③ **路由接入＝新增一个可选维度、
> 权重默认 0、无信号即中性** ⇒ 与既有 `CostWeight`/`IQWeight`/`OutlierWeight`/`CacheAffinityWeight`
> 同族（`router_scoring.go:187-195`）⇒ ★★ **不接线时行为逐字节不变**才敢合 ⇒ 接入点已精确定位
> （`autoroute/scoring.go:243-260` `scorePrice` 旁新增 `scoreMarketAdvantage` ＋ `CostContext`/`Candidate`
> 加字段 ＋ `refreshIndexSQL`/`bg/auto_index_refresher.go` 投影 ＋ v2 主链 `scoring_v2.go:52-68`
> 加一项 ＋ `router_scoring.go` 同族新增 `marketAdvantagePenalty`）⇒ ★★ **`p2cOrder` 的 10% 成本带
> 闸门不改**（它是「带」不是「点」）。
> ⇒ ★★★★ **★ 本轮挖出的两处真缺陷（待裁决，未改代码）**：
> ★ **① 本仓内部不自洽**：健康门 `offer_price_looks_like_placeholder` 的注释白纸黑字写「0/0
> **不是缺陷**——`CalcCost` 对 0/0 返回 nil，正确地让 `cost_usd` 留 NULL」
> （`bg/routing_health_checks.go:1030-1034`）⇒ 而 **`autoroute` 内部有**两份逐字相同**的零价分支
> —— `scoring.go:243-260` `scorePrice`（v1）**与** `scoring_simplified.go:259-277`
> `scorePriceByCostContext`（**v2 主链与 simplified 共用**）⇒ **两处** `blended <= 0` 都返回 100
> （注释写 `// free or unknown`）⇒ ★★★ **三代评分全部中招，只改第一份会漏掉主链** ⇒ ★★ **同一个
> 「没有价格」，`autoroute` 当它「最便宜」、`executors` 的 `calculateCostPenalty` 当它「最大惩罚」**
> ⇒ 且 `autoroute/index.go:578-582` 的索引 SQL 又把 0/0 显式标成 `is_free`（而 `IsFree` 只被当
> **标记**用、**不是排除条件**）⇒ **确实会进候选池** ⇒
> ⇒ ★★★ **这是本方案唯一建议立刻动的代码点**（P0-4），但**会触及既有测试期望值与注释** ⇒ 需裁决。
> ★ **② 纪律只覆盖了一半的写侧**：本仓 `cmd/tools/propose-baseline-prices/canonical_price_collision.go`
> 的「同名同 canonical 多价 ⇒ **整组 withhold**、钱永不消失」规矩**只管住了行情层** ⇒ 实测
> `salesdecorate` 的 `model` 表里有被合并的垃圾行 `claude-opus-5-/-claude-opus-4.8` ⇒ ★★ **它已经
> 爬进商业层**：供给卡 `SC-20261101-007` 的标题就是它，`raw_model_id` 写 `claude-opus-5`，成本
> ¥53.41/¥267.07（折 $7.42/$37.09）**恰好是 Fast mode 的价、不是标准价** ⇒ ⇒ **脏数据从行情层
> 漏到商业层＝直接变成钱**。
> ⇒ ★★★★ **方案档的完整结构**（53KB）：现状测绘（§2）→ 五价坐标系与三子分数算法（§4）→
> **治理红线表（§4.4，逐条继承本仓既有裁决：基准价不进金额计算 / NULL≠0 / 整组 withhold /
> 对账只记账不改价 / 币种未知拒绝不默认 USD / 条件价不压平）** → 数据模型 3 张新表 ＋ 单向同步
> 链路（三步法：只出提案 → 人审 → 既有导入通道，**禁止营销手工价回流改路由**）（§5）→ 运营节奏
> ＋ RACI（§6）→ **三条产品线**（A 面向客户：双口径价目表（积分＋每百万 token）＋ 市场参考价
> 并列 ＋ 预计月耗；B 面向销售：行情→供给卡一键化 ＋ **成本口径切到我方实付价** ＋ 分层加成策略
> ＋ 一键报价 3 档；C 面向平台：把「采购折扣率」变成可证明的对外价值）（§6.2）→ **另外 10 条用途**
> （采购谈判反向筹码 / 准入评估闸门 / 涨价提前量 / 模型清单决策 / 市场最优价保证 / 四方对账 /
> 成本置信度护栏 / 路由第五目标 / 报价单自动失效 / 降价传导 SLA）（§7）→ **四阶段路线图，每阶段
> 带验收门**（P0 数据地基 5 项 · P1 对账告警不碰路由 ＋ **「独立来源数 ≥3」硬指标** · P2 影子→
> 灰度→全量 ＋ 回滚开关 · P3 客户侧与营销）（§8）→ 风险与**明确不做的事**（§9）→ **7 条待
> 裁决**（§10：★ 最关键是 **10.1「基准价/市场参考价能不能进路由排序」**——既有 2026-10-07 裁决
> 只说「不参与金额计算」，★ **「排序算不算金额计算」从未裁决过** ⇒ 按本仓范式必须**显式补裁决**，
> 不能默默做）。
> ⇒ ★★ **本轮明确没做（别重复排查）**：① **零代码/零迁移/零同步器**（规划轮，§8 的四阶段是路线图
> 不是已完成项）⇒ **本轮不声称实现了任何功能**；② 未触碰 `bg/data/model_baseline_prices.json`
> （16 模型/3 厂商是**已知现状**，补齐路径＝同步器，但需先裁决 10.1）③ 未评估
> `collector/sd_collector/` 的解析覆盖率细节（只用了 `parse_coverage` 的表行数 49）④ 未采集
> 「我方在哪些平台有账号」的清单（10.5 待裁决）⑤ **未核实生产环境 `UseSimplifiedScoring` /
> `UseChannelQualityRouting` 两个 flag 的实际取值** ⇒ §4.3 的接入点给的是**三代并存**的形状，
> 具体落哪一代**须在 P2 前实测确认**。
> ⇒ 审计档 `docs/audit/2026-11-03-模型价格情报-供应商评分-路由参数与运营营销方案.md`；
>   通用式见 `LESSONS.md` **§623**（★ 取号：fetch 实测 origin/main == HEAD `f4f86fcd5`、
>   最高 §622 ⇒ 连号 §623；★ 10 条通式）。
> ⇒ ⚠ **★ 交接提醒（下一轮必读）**：**AGENTS.md 体积已逼近棘轮上限** —— 本轮落笔后
> **518,254 字节 / 上限 524,288 字节（`agentsMdSizeRatchet` 的 `512*1024`）⇒ 余量仅 6,034 字节**
> ⇒ ★★ **下一轮不要再往 AGENTS.md 里写长正文**（正文一律进 `docs/audit/` 方案档，AGENTS.md 只留
> 索引行 ＋ 指针 ＋ 3 条以内要点）⇒ ⇒ ★ **且该棘轮是「只降不升」的** ⇒ 余量用完时**不能**靠
> 调高上限续命 ⇒ 只能分档归档（`AGENTS-archive-20261031.md` 已是同款先例，★ 见 §619 族与
> `agents_md_size_ratchet_test.go` 的 R1–R4）。★ **本轮实测：`git status` 显示工作树在开工时
> 是干净的 ⇒ 与并行会话无在途条目 ⇒ 本轮提交不抢别人的产物（§618 族底线）。**
> **2026-11-03 同步轮（cron）：GitHub 出网全断的取道 + `error_type` 闭集第二次漂移（930）+ 211 `--force` 锁恢复未接线**
> （cron 会话 `mvs_c3e5ed67422c47dab8f0e0a601c37451`）：本轮任务＝拉最新 → 修问题 →
> 编译测试 → 提交并同步 codeup + github（两仓 main 的树一致）。
> ⇒ ★★★★ **P0（运维阻塞，本轮最大的成本）：`git fetch github` 失败**，
> `github/main` 落后本地 **3406** 提交、落后方向另有 **13504** 笔本地没有的历史
> ⇒ ⇒ **本机的出网链路全断**，逐层实测如下（★ 每一层都取机器读数，不靠猜）：
> ① **DNS**：`github.com` → `198.18.0.74`（fake-ip 段）⇒ 流量走 Clash Verge 的
>    TUN（`utun4`，gVisor 栈，`auto-route` 开）。
> ② **代理实例有三套**：Clash Verge 服务核心 `7890`（mihomo，root）、
>    network-switch `7894`、xray SOCKS `19989` ⇒ ⇒ **三套全部对 github 返回 `000`**。
> ③ **Clash Verge 的 anytls 节点全死**，mihomo 日志逐条点名：
>    `[TCP] dial 节点选择 (match Match/) … error: failed to create session:
>    dial tcp 64.178.123.33:5771: i/o timeout` ⇒ ★★ **两个不同的死法**
>    （**用隔离实例复跑才分得清**，在同一实例上它们都只表现为「超时」）：
>    - `64.178.123.33:5771`（现役配置）：**TCP 连上但 anytls 握手 EOF** ⇒ **password 过期**；
>    - `64.178.123.12:5771`（旧配置）：**TCP connect 超时** ⇒ **地址不可达**。
>    ⇒ ⇒ ★ **换节点不解决问题；缺的是「订阅刷新」**。
>    ⚠ ★★ **量具陷阱**：`nc -z` 在本机**不可信** —— TUN 的 gVisor 栈会**本地 accept**
>    ⇒ 任何 `nc -z` 都报 `OPEN`，**连死掉的节点也报 OPEN** ⇒ 判「端口通不通」
>    必须看 mihomo 自己的 dial 结果，不能看 `nc`。
> ④ **订阅端点**：`api.efanyunapi.com` / `0b96e976-….com` 两个都取不到（它们自己
>    也走 TUN ⇒ 撞同一堵墙）；只有 `115.29.212.252:8080/subscribe` 通（国内 IP ⇒
>    `GEOIP,CN` ⇒ DIRECT）⇒ ⇒ 该订阅里只有两个 vless-reality 节点（Exit-56/Exit-252）。
> ⑤ **252 的 vless 隧道「握手成功但不转发」**：xray 日志 `tunneling request to
>    tcp:github.com:443 via 115.29.212.252:443` 之后**没有任何回包**，连 `baidu`
>    也不通 ⇒ ⇒ **服务端出口本身坏了**，不是客户端参数问题（★ 其 uuid 其实是对的
>    —— xray 的键名是 `id` 不是 `uuid`，第一眼看 `uuid` 会误判成「没配 uuid」）。
> ⇒ ★★★ **★★ 本轮可用的取道（实测）**：用户自己的服务器里 **`115`
>   （101.37.209.115）能直连 GitHub** —— `ssh 115 'curl -sI -m 12 https://github.com'`
>   ⇒ `GH=200`（`252`/`211` 同样通）⇒ ⇒ **`ssh -N -D 19995 115` 建 SOCKS 隧道**，
>   再让 git 走它：
>   ```bash
>   ssh -N -D 19995 -o BatchMode=yes -o ExitOnForwardFailure=yes 115 &
>   export GIT_SSH_COMMAND="ssh -o ProxyCommand='nc -X 5 -x 127.0.0.1:19995 %h %p' -o BatchMode=yes"
>   git ls-remote github        # ⇒ 读得出 refs/heads/main
>   ```
>   ⇒ ★★ **判据：「本机出网断」时，别去修代理，先找一条还能用的机器出网**
>   （SSH 动态端口转发是标准手段）⇒ ★★ 与「阴性结论要当缺陷一样复核」同族：
>   ★ **「GitHub 不可达」这个否定性结论的第一版是错的** —— 真因＝**本地代理链全断**，
>   不是 GitHub 封了本机 ⇒ ⇒ ★★ **否定性结论要问「它的前提（出网链路）自己通吗」**。
> ⇒ ★★★★ **P1（本轮修，跨仓）：`tests/deploy_force_lock_recovery_test.sh` 是红**
>   ——**根因不是测试写错，是它自己的 commit 没改它要测的那处代码**：
>   `6f245a9f3`（2026-11-02 17:04，message 明写「**锁库加 211**」）新增了这条测试
>   + `deploy-211.sh`/`deploy-seamless-docker.sh` 的 `--force` 接线，
>   **但 `git show --stat 6f245a9f3` 的文件清单里没有 `scripts/deploy-lib/lock.sh`**
>   ⇒ ⇒ 引擎侧白名单仍是 `{154,245}` ⇒ `lock_recover_local 211 1` 直接
>   `rc=2 / ERROR: invalid lock target: 211` ⇒ ⇒ ★★ **运维的 `--force` 依然到不了
>   「恢复陈旧锁」这一步**（`deploy-211.sh` 把 `--force` 转交引擎后，引擎在白名单
>   这一关就拒掉）⇒ ⇒ 211 部署卡在 rc=75 时**仍只能手工删锁目录** ⇒ ★★ 这正是
>   那条测试要防的链 ⇒ 修法＝`lock.sh` 的 `lock_recover_local` 白名单加 `211`
>   （★ 放行 211 **不放宽别的边界**：target 仍是**路径推导**的 ⇒ 跨 target 仍然
>   结构上不可能误删）⇒ ★★ **该文件在父仓 `ai-native-tools`（`deploy-lib` 是软链
>   SSOT，不在本仓）** ⇒ 见下方「跨仓落点」。
> ⇒ ★★★★ **P2（本轮修）：`self_check_runs.error_type` 的 CHECK 闭集第二次漂移**
>   —— 新增 startup 迁移 **930**（`930_self_check_error_type_kind_closure_model_unavailable.sql`）：
>   `errorsx.KindModelUnavailable`（`model_unavailable`）是 `4fe043dc3`
>   （**2026-11-02 12:23:59**）加的，比 929 的落地（**07:47:48**）**晚 5 小时**
>   ⇒ ⇒ CHECK 闭集少 1 项 ⇒ 写侧 `bg/credential_selfcheck.go:844` 写的正是 kind 名
>   ⇒ `finalizeRun`（`:896`）的 UPDATE 被 DB 以 **23514** 拒绝 ⇒ **整条回滚**
>   ⇒ run 行停在 `status='running'` 的**半写态** ⇒ 审计与发布都不执行
>   ⇒ ★★ **这次自检在系统里根本没发生过，但调度器以为发生过**。
>   ⇒ ★★ 闭集 **37 → 38** 项（kind 32 → **33** + worker 专用 5）⇒ 修法＝**新增一条
>   迁移**（★ 929 的字节已冻结在台账里 ⇒ 改它会走 content replay 通道并连锁 break）
>   ⇒ ★★ **元教训比迁移本身更值钱**：929 的交叉门**抓到了**这次漂移（当场红）
>   ⇒ 但 ★★ **门只报警不施救** —— 门说「新增一条迁移」，**迁移要人来写**
>   ⇒ ⇒ ★★ **「加一个 kind」的系统成本 = 一次跨仓的人工动作**，而它**每次都会晚**
>   ⇒ ⇒ **每加一个 kind 都要预留一次「新增迁移」的跨仓动作**。
> ⇒ ★★ **本轮另外收口的六条（★ 全部是「上游已停手那一片」或「**本仓自己的上一轮**引入」的**>   ⇒ ⇒ ★★ **判据同 §619**：**「红要收口」不是按红的数量或新旧决定，是按「谁还在动那片」决定**
>   ⇒ ⇒ 本轮逐条量了每片目录自 `187ec7ead` 以来的改动量）：
>   ① **P1-1：`self_check_runs.error_type` 闭集** ⇒ 新迁移 930（见上）。
>   ② **P1-2：`lifecycle.session_turns_probe_hot_ttl_days` 是孤儿 settings 键**
>      （运行期在读、但从未 Register 成 spec ⇒ `settings.Global.Spec(key)` 返回 nil
>      ⇒ `GetPlatformInt` 直接回退 fallback ⇒ ★★ **往 `settings_kv` 里写这个键
>      完全无效**，静默无效不报错）⇒ 补 spec（`settings/spec_lifecycle.go`：
>      TypeInt / ScopePlatform / **Default 14 与读方 fallback 对齐** / Min·Max 1..90
>      **与代码 clamp 同档** ⇒ 两侧夹取口径一致）。
>   ③ **P1-3：`session_id` 直读棘轮**（`TestSessionIDRawHeaderRatchet`）——
>      上游 `187ec7ead` 给 `internal/userscriptcapture/handler.go` 加了
>      `X-Gw-Session-Id` 直读 ⇒ 门红 ⇒ ★★ **但不能改经 `internal/sessionid`**：
>      那层的 `SanitizeGwHeader` **要求 `gw_`/`gt_`/`gs_` 前缀**
>      ⇒ userscript 侧发来的**裸 uuid**会被判非法 ⇒ ★★ **改走 SSOT = 静默丢掉
>      这批会话的 `ack.CanonicalSession` 标注** ⇒ ⇒ 那不是重构，是**行为变更**
>      ⇒ ⇒ 按门自己给的第二条路：**登记豁免 + 写明理由**。
>   ④ **P1-4：派生台账漂移三件套**（**全部从 SSOT 重新生成，不手改产物**）：
>      - `scripts/audit/asset/admin-routes.txt`：`extract-admin-routes.sh --update-snapshot`
>        ⇒ **602 条**（生成器 602 vs 快照 601 ⇒ ★ **是上一轮自己的 `admin/` 改动带的**）；
>      - `scripts/audit/asset/db-object-expect.tsv`：`db-object-census.py expect`
>        ⇒ **863 行**（差的那 5 个索引 `idx_capture_batches_tenant` /
>        `idx_capture_messages_batch` / `idx_capture_messages_conversation` /
>        `idx_outbox_events_status_retry` / `idx_task_events_claim` ⇒ **正是迁移
>        931/932/933 带来的** ⇒ ★★ 与上一轮登记的读数**逐字一致** ⇒ 确认是既有漂移）；
>      - `db-object-gap-ledger.tsv`（`reconcile`）+ `installer-reachability.tsv`
>        （`reachability`）⇒ 同为 863 行 ⇒ ★ **重生成前先跑 `selftest` ＝
>        50 checks / 0 failures**（本轮已实证）⇒ ★★ 三份产物**各只多 5 行**，
>        **其余逐字未变** ⇒ 棘轮没被放松。
>        ⇒ ★★ **第四份派生台账（`post-surface-ledger.tsv`）是本轮**被自己的改动带出来的**：
>        admin-routes 从 601 重铸到 602 之后，POST 面台账的行数对不上了
>        （`TestPostSurfaceLedgerCoversEveryRoute`：台账 601 ≠ 路由清单 602）⇒
>        ⇒ ★★ **那 1 条缺的正是 `/v1/userscript/captures`**（迁移 931 的摄取端点，
>        上游 `187ec7ead` 加的）⇒ ⇒ ★★ **旧状态是「行数相等但内容没覆盖」**
>        （门只比行数 ⇒ 绿而内容缺）⇒ ⇒ ★ **重新生成把这条隐形缺口显性化了**
>        ⇒ ⇒ 修法 ＝ 跑仓内既有的补齐器 `scripts/audit/post-surface-ledger-refresh.py
>        --apply`（★ **只追加不重写既有行** ⇒ 既有 601 行逐字保留，含人工改判）
>        ⇒ 补进 1 条 + 1 行 `#gen_append` 标记 ⇒ 台账 602 行 ⇒ 门绿。
>        ⇒ ★★ 附带一条：**「比行数」的门在两边同时漂时会互相抵消** ⇒ ⇒ 改用
>        「从 SSOT 重生成」的口径 ⇒ 才能让「内容缺」显形。
>   ⑤ **P1-5：请求面文档三面同钉（第八次重钉）**——`cmd/gateway/main.go` 的
>      中间件链相对第七次再漂 **+73** ⇒ Recovery **8056** / Logging 注释
>      **8058-8065** / maintain 注释 **8047-8052** / Auth **8071** /
>      SecurityHeaders **8074**（链本体 **8047-8074**）⇒ ★ **三面同钉**
>      （本对拍表 · `docs/subsystems/request-surface.md` · magpie 审计档 §3.18.3
>      认证表 A1 行）⇒ ★★ 这已是**第八次重钉** ⇒ 「行号会漂」是**常态**。
>   ⑥ **P1-6：两条前端棘轮** —— ★★ **两条都是「修根因、不碰基线」**：
>      - `i18n-shared-dead-baseline.json`（共有死键数 0 → 1）：那 1 个键是
>        **`requests.length`**（8 语种共有、**全仓 0 引用**：无字面引用、无动态
>        拼接）⇒ ★ **删掉 8 个语种里的这个键**（不是调基线！★ 调基线 ＝ 把门弄瞎）
>        ⇒ 计数回到 0 ✓；
>      - `design-spacing-scale-decisions.json`（离阶间距 0 → 4）：那 4 处全是
>        ★★ **上一轮自己引入的 idle 指示条 CSS**（`.cp__idle-strip` /
>        `.pp__idle-strip` 的 `gap: 6px` 与 `padding: 3px 6px`）⇒ ★ 按尺自己的
>        修法「收到就近的间距阶值（平局取下界）」⇒ **6px → 4px** ⇒ 离阶回到 0 ✓
>        ⇒ ★★ **这正是「红要收口不是按新旧决定」的正面例子**：两条红的引入者
>        **都是本仓上一轮**，且都是**机械可判**的（一个删键、一个改两个数）。
> ⇒ ★★★ **既有红的分类（本轮逐条定性，别再重复排查）**：
>   | 套件 | rc | 定性 |
>   |---|---|---|
>   | `deploy_force_lock_recovery_test.sh` | 1 | **真缺陷** ⇒ 本轮已修（跨仓，见上） |
>   | `deploy_local_contract_test.sh` | 1 | **环境红**：本机 harness 把删除动作接管成 `mavis-trash` ⇒ `lock_release_build` 的递归删目录被拒 ⇒ 锁目录留在 ⇒ 断言红（★ 代码路径正确；复现见 `mavis-trash: failed to move … errno 2 (-2700)`） |
>   | `deploy_rollback_test.sh` | 64 | **环境红**（同族：`mavis-trash` 保护路径） |
>   | `deploy_ssh_retry_test.sh` | 1 | **环境红**（缺注入 SSH key ⇒ `SSH_RETRY_KEY_ARGS: 未绑定的变量`） |
>   | `session_storage_audit_gate.sh` | 1 | **环境红**（要 docker + `-tags=integration` 真库） |
>   | `sql/migrations/startup` 的 `931` DDL 预算 | 红 | **待实测裁决**：要按目标环境最大表**实测**再抬 `statement_timeout` ⇒ ★ 本轮**不拍数**（★ 拍 120s ⇒ 245 部署中止是仓内反例）⇒ 沿用上一轮登记 |
>   | `bg` / `cmd/gateway` / `storage` / `storage/factory` / `storage/sqlite` / `tests/integration` | 红 | **cgo=0 宿主必然红**（`verify.sh` 已登记名单；SQLite 驱动按设计 panic） |
> ⇒ ★★ **跨仓落点（本轮明确没做）**：930 的 SQL 改动全在本仓；但 **P1 的 `lock.sh`
>   在父仓 `ai-native-tools`**（`scripts/deploy-lib` 是软链 ⇒ 真身在
>   `ai-native-tools/deploy/deploy-lib/`）⇒ ⇒ ★★ **本轮只在父仓工作树里改了那一行**
>   （⇒ 本仓那条红已转绿），**没有在父仓提交** ⇒ ★★ 理由：父仓工作树当前处在
>   **「`deploy-lib/` → `deploy/deploy-lib/` 软链搬迁未提交」的半迁移态**
>   （`git status` 显示 35 个文件「已删除」，搬迁自 2026-11-01 00:39 起未落盘）
>   ⇒ ★★ 在这种状态下提交父仓 = **抢别的会话的产物**（§618 族的竞态纪律）
>   ⇒ ⇒ **待父仓那笔搬迁落地后，把这一行并进去**。
> **2026-11-03 12h 修订审计轮：origin/main 带红测（CI 载体一条单测都不跑）+ 摄取端点数据竞争 + session.closed.v1 跨租户污染 + 基线写回同族两洞**
> （goal 会话）：审计窗口 2026-11-02 19:05 → 2026-11-03 07:35，窗口内 15 提交。
> ⇒ ★★★★ **P0：`origin/main` 带着一条红测**，pristine worktree 复现
> （`domains/analysis/sessionpush` 的 `TestDeliverRecordsReconciliationCounters`：
> `success/provisional = 0, want 1`）。根因两段：① 合并机器人把**别的会话的在制品**
> 代为落盘（`c9859fc6c`），那条快照**自落地起就没绿过**——测试构造的 `Result` 没有
> 可沉淀内容，`BuildPayload` 的 `HasLandableContent()` 前置闸直接判 `ok=false` ⇒
> 推送根本不会发生 ⇒ 计数恒 0；② **仓内唯一 CI 载体 `.workflow/main-verify.yml`
> 只跑 `make guards-fast`（静态自证门），一条单元测试都不跑**，完整门禁
> `verify.sh`（含 `go test ./...`）是**本地手工**执行的 ⇒ ★★ 「代码进 main」与
> 「门跑过」之间**没有强制连接** ⇒ 判据：审计「门跑过吗」要问**谁在 push 路径上
> 自动跑了它**。
> ⇒ ★★★★ **P1-1：请求路径上的惰性初始化写回共享字段 = 数据竞争**
> （`internal/userscriptcapture/handler.go`：进程级单例 + 每请求一 goroutine ⇒
> `-race` 当场报 `WARNING: DATA RACE`，读 :116 / 写 :117）；同族附带
> `SessionExists` 在 `db` 为 nil 时**空接口调用 = nil 解引用 panic**（请求线程直接炸）。
> ⇒ ★★★★ **P1-2：`session.closed.v1` 的租户解析没有租户约束 ⇒ 跨租户污染**
> （`public.sessions` 按月分区、唯一键 `UNIQUE (session_id, partition_date)` ⇒
> **session_id 不是全局唯一** ⇒ `WHERE session_id=$1 LIMIT 1` 无 `ORDER BY` 返回
> **任意一行** ⇒ 事件挂到别的租户名下；仓内其余每一处都带 `tenant_id` ⇒ 孤例即指纹）。
> ⇒ ★★★★ **P1-3：sessionpush 对账口径**——注释承诺「禁用期直接可读」，实现却把
> 「禁用」与「空载荷」都记 `skipped`、`dispatched` 只在启用态计数 ⇒ 兑现不了；
> 且该门断言用**进程级全局计数器的绝对值** ⇒ 同包另一测试也调 `Deliver` ⇒ 不可复现。
> ⇒ ★★★ **P2：基线写回同族三洞**——① 只从本次量到的页构造新基线 ⇒ **没量到的页
> 被当成「已删除」** ⇒ 那几页的棘轮约束被悄悄摘掉；② 审计日期用 `toISOString()`
> （UTC）⇒ UTC+8 凌晨写成**前一天**；③ **写回口径此前没有任何门覆盖**、两把尺各写
> 一份 ⇒ 本轮抽 `web/scripts/lib/baseline-ratchet.mjs` + `web/src/baseline-ratchet.test.ts`。
> ⇒ ★★ **本轮改动**：`sessionpush.go`（口径分档）、`push_metric_test.go`（修绿+增量断言）、
> `internal/userscriptcapture/{handler,store}.go` + `handler_race_test.go`、
> `admin/session_closed_outbox.go` + `session_closed_outbox_test.go`、
> 两把基线尺 + 新共用纯函数 + 新 vitest 门。
> ⇒ ★★ **未修并已登记**：`pg_test_env_registry` / `pipefail_race_ledger` / 931 迁移
> DDL 预算三条既有红（需改已应用迁移或补原会话裁决）；双写桥 `OnEventSent` 同步在事务内
> 的耗时回压；cookie Domain 由请求 Host 推导且作用域放大到 `*.itestu.cn`；nonce 缓存
> 全扫 O(n)。审计档：`docs/12小时内修订审计-2026-11-03-0735.md`；通用式见 §621。
> **2026-11-03 /live 两步交互 idle 心跳行占位修正轮：左右面板死入口根修 + 量具 X1/X2 与不可判定分码**
> （goal 会话）：★ 承接 owner 指令「/live 左右两侧请求的点击逻辑应像旧版本：
> 点击弹摘要 → 再点『查看全景』才开新窗，检查修正后提交并推送」。
> ⇒ ★★★★ **结论：真实请求的两步交互在线上完好，被打破的是 idle 心跳行**——
> ★ 线上 211 = `v2.5.9 #2944`（前端树 `81360f8d9`，11-02 晚一次 **AGENTS 无记录的部署**，
> ★ `deploy-seq-src=git-sha` ＝ 没走 deploy 脚本通道、build_seq 未透传）；
> ★ 上游 `7c3632e7c`（11-02）把后端 `idle_marker`（「泳道安静」瓦片，每分钟注入、
> Redis 常驻）渲染成请求面板的**可点行** ⇒ 每个交互出口对它都是死路：点行弹灰态摘要、
> 「查看全景」被 `onOpenPage` 守卫吞掉、行尾 ↗ 被 `v-if` 拿掉 ⇒ 低流量时段实测 **75 行**
> 全是死入口 ⇒ owner 体感＝「点击逻辑坏了」（★ 此前 5 轮复核全绿是因为当时窗口里
> 只有真实请求行一种形态 ⇒ **复核结论的保质期由「数据形状集合」决定**，见 §620）。
> ⇒ ★★★★ **取证链**：① 免鉴权读线上 `deploy-seq` meta + bundle 标记
> （`liveTopology.zoom.openPage`、`block:"nearest"` 在役 ⇒ 代码在）；② 铸签 admin JWT
> → 量具真浏览器两轮：升级前 L1b 红且「zoomText **contains** mid id 却判红」
> ⇒ 唯一解释＝`midId` 空串 ⇒ 被点行无 ↗ ＝ idle 行；③ 截图实证（浮层请求 ID =
> `idle-global-vendor-…`、右栏整列「空闲」心跳）。
> ⇒ ★★★★ **修法（两面板镜像）**：`realRequests/realResponses` 过滤 `isIdleMarker`
> ⇒ 行列表/分页/表头计数/翻页回退/入场动画全只看真实请求；心跳收敛为**非交互灰态
> 指示条**（`client/provider-idle-strip`：复用既有键 `idleLane`/`idleLaneHint` ⇒
> **零新增语言包键**，不碰 8 语种棘轮；泳道名去重 ≤4 + `+N` + 最新时刻；虚线描边
> =「不是普通请求」家族语义）；行级 idle 分支保留退居防御。
> ⇒ ★★★★ **量具升级（`verify_live_two_step_interaction.mjs`）**：新增 **X1**
> （心跳行数必须为 0）+ **X2**（有心跳信号 ⇒ 指示条可见且无 button），先于真实行门跑；
> 新增 `waitRealRows`（以「↗ 存在」定义真实行，与产品判据同源）；窗口无真实请求行
> ⇒ **exit 3 不可判定**（不是红）⇒ **且用 throw 不用 `die()`**（`process.exit` 跳过
> 视口 finally 的 Chrome/profile 清理 ＝ 「门被中止时 finally 没跑」量具版）；
> L1b/R1b 中部行只在真实行里挑（旧版靶到心跳行 ⇒ L1b/L2/L3 三连假红）。
> ⇒ ★★★★ **验收读数**：`vue-tsc` rc=0；`vitest run` **290 文件 / 2994 passed**
> （+5 新判据）；`vite build` ✓；变异臂 **M1/M2/M3 全检出 + 还原 md5 逐字一致**；
> **量具对生产 2944：X1 红 ×2 视口（idleRows=75/72）+ X2 红 ＝ 天然变异臂**，
> 其余 **18/18 全绿**（真实行上两步流完好 ⇒ 诊断闭环）。
> ⇒ ★★ **本轮明确没做（别重复排查）**：① **未部署**（指令止于提交推送；211 仍 2944
> ⇒ 线上 X1/X2 仍红、心跳死入口仍在，**部署后终验＝量具双视口 ALL PASS**）；
> ② `onOpenPage` idle 守卫与浮层 `idleNote` 分支保留（后者随心跳行退场而不可达，
> 属后续清理面）；③ idle 条目对拓扑画布的影响未排查（`useLiveTopology` 无过滤、
> 截图画布正常，登记待查）；④ 2944 部署者未记录 ⇒ 与并行会话部署竞态风险在
> （§618 族）⇒ 本轮不代部署也避免与其在途验证相撞。
> ⇒ 审计档 `docs/audit/2026-11-03-live两步交互idle心跳行占位修正轮.md`；
>   通用式见 `LESSONS.md` **§620**（★ 取号：fetch 实测 origin/main == HEAD
>   `2c638eafe`、最高 §619 ⇒ 连号 §620）。
> **2026-11-02 第 41 增量：前端门清单首次整体重跑 + 上游遗留红收口（6 条红全收口 + 3 臂变异验证）**
> （会话 `mvs_49b32e49a4b94018a9c4998e83b66678`，承接第 36 增量补交轮 `8b1c769f0`）：★ 上一轮
> 在审计档 §7.4 ④ 里明写「**32 门清单未整体重跑 ⇒ 32 门全绿没测过**」⇒ ★★★ **本轮第一件事就是
> 补这笔账**，★ 而补账的方式不是「重跑交差」，★ 是**在重跑中把清单里已有的红逐条定性并收口**。
> ⇒ ★★★★ **验收读数（★ 树 = 将要提交的那棵树 `origin/main` `0683edec7` ＋ 本轮 9 文件改动）**：
> ★ **前端门清单 61 项全通过（30 门 + 31 自证），rc=0**（★ 9 项刻意排除照常列出，不静默）·
> ★ **`vitest run` rc=0（290 文件 / 2989 测试全过）** · ★ **`vue-tsc --noEmit` rc=0** ·
> ★ **`vite build` rc=0** · ★ **登记册普查门 PASS（1.086s）** ·
> ★ **6 份浏览器尺基线 md5 跑前跑后逐字一致**（★ 掏空门自愈）。
> ⇒ ★★★★ **★ 清单里的 3 道红 + vitest 的 3 道红 ＝ 6 条，根因归为 4 类，全部是「上游加了东西
> 但没配套齐」的同一个族 ⇒ 本轮 6 条全部收口**：★ ① **上游只补「三语」**（`81360f8d9` 加
> `nav.item.freellm` 只给 en-US/zh-CN/zh-TW）⇒ ★ 5 语种缺文案 ⇒ `i18n:key-parity:check` 硬闸红
> ⇒ 补 5 语种（★ **译文必须 ≠ en-US 的 `'FreeLLM'`**，★ 否则撞 `same-as-source` 另一道棘轮）·
> ★ ② **fr-FR 的 `liveTopology.idleNote: 'Note'` 与 en-US 逐字同形**（★ 上游 `7c3632e7c` 给 9 语种
> 加键）⇒ `untranslated fr-FR 1119 > 1118` 突破棘轮 ⇒ ★★ 改同义词 `'Remarque'`（★ untranslated
> 降回 1118 ＝ 恰好回到基线；★ **不调基线**，★ 调基线 ＝ 把门弄瞎）· ★ ③ **上游把两个菜单项改成
> 外链**（`path` 字段直接放 `https://freellm.itestu.cn`）⇒ `navCoverage` 死链红 ⇒ ★★ 判据缺
> 「绝对 URL」这一类 ⇒ ★★ 修法 ＝ 形态判据 `/^https?:\/\//i`（★ **不是具体值豁免**）⇒ ★★ **诚实边界：
> 外链的可达性不归本门管**（★ 单元测试做网络检查既慢又不稳）· ★ ④ **后端加 `SourceProbeLane` 而前端
> 联合没跟**（★ 后端 `7c3632e7c` 加、前端联合停在更早的 `a440cba9e`）⇒ `source-contract` 契约红 ⇒
> 前端联合补 `'probe_lane'`（★ 前端无 switch 穷尽分支 ⇒ 无编译影响）· ★ ⑤ **新测试与 4 个月前的
> 实现意图相反**（★ 上游新加的 `liveStreamIdle.test.ts` 断言 `upstream_timeout → failure_timeout`，
> ★ 而实现的负向预查**故意**把它排除在超时之外 ⇒ 落 `failure_5xx`）⇒ ★★★ **裁决依据不是新旧，
> 是「后端到底产出哪几种形态」**：★ 后端确有语义不同的两种 timeout（★ 裸 `"timeout"` ←
> `middleware/logging_mw.go:191` ＝ **网关侧** HTTP 408/504；★ `"upstream_timeout"` ←
> `internal/trace/builder.go:416` ＝ **上游调用**超时）⇒ ★★ 实现的分层是刻意的 ⇒ ★★ **改测试对齐
> 实现**，★ 并把两条后端出处写进注释 ⇒ ★★ **同族**：上游把两条路径登记进 `KNOWN_ORPHANS`，★ 而那个
> 白名单管的是**反方向**（router 有、菜单没覆盖）⇒ ★★ **「登记了」不等于「门认了」**。
> ⇒ ★★★★ **★★ 三臂变异验证（★ 每臂还原到 md5 逐字一致）**：★ **M1** 把外链 `path` 改成站内假路径
> ⇒ 红（`/definitely-not-a-real-route`，★ 证明新排除条件没把站内死链放过）· ★ **M2** 去掉排除条件
> ⇒ 回到红（★ 证明排除必要）· ★ **M3** 去掉实现的负向预查 ⇒ 新断言红（★ 证明它钉住了分层语义、有牙）。
> ⇒ ★★★★ **★★ 第 7 类红（本轮新发现，★ 属「门自己的工具」那一族）：★ 运行时尺的
> `--update-baseline` 写回命令，本身可能就是放松棘轮的那只手**：★ 补登 `/model-credibility` 前逐把查了
> 6 把运行时尺 ⇒ ★★ **6 把里 5 把守住了**（★ `icon-size` / `nested-card` / `hero-metric` 用
> `min(旧值, 实测值)`；★ `action-density` 用「**写回前拒绝**放松方向」；★ `spacing-scale` **压根没有**
> 该命令 —— 它是**观测工具**，★ 基线里明写「本工具是观测工具，不是硬闸」⇒ ★ 它读数升高**不是门红**）
> ⇒ ★★★ **只有 `metric-skeleton` 是整份覆盖**（`Object.fromEntries(report.pages.map(...))`）⇒ ★★★ 后果：
> **任何一次「补登一个新页面」的动作，都会把其它每一页的基线一并改成实测值** ⇒ ★ 若某页实测恰好升高 ⇒
> **棘轮被悄悄放松 ⇒ 门被弄瞎** ⇒ ⇒ ★★ 修法 ＝ **照抄 nested-card 的成熟形态**：写回值 ＝
> `min(旧值, 实测值)` ＋ `keptHigher` 留痕 ＋ **新页按实测值登记**（★ 新页必须被棘轮看见）＋
> **note 动态化**（★ 静态模板让「变更史」失去意义）＋ **日期用本地日期而非 `toISOString()`**
> （★ 后者是 UTC ⇒ ★ 跨日边界会把审计日期写错一天）⇒ ★ 自证 **43/43**（★ 改后复跑）·
> ⇒ ★★★★ **★★ 变异臂 M4（★ 证明修好后的写回守住棘轮）**：★ 人为把 `/admin/agents` 的旧基线
> **5 → 1** ⇒ 跑 `--update-baseline` ⇒ ★ 尺输出「**棘轮保护**：实测 **5** > 基线旧值 **1** ⇒
> **保持旧值 1**」⇒ ★★ 写回后该页仍是 **1** ⇒ ★★ **守住**（★ 若沿用原实现 ⇒ 会被实测值 5 覆盖 ⇒ 破口）·
> ⇒ ★★ **★★ 同族的第二处（★ 本轮**审出、但实测未触发**；★ 且我先把它写错过一次）**：★ 「**基线里有、
> 但本次没量到的页**」⇒ ★★ 我最初判「`pagesMatch` 硬检查会兜住」⇒ ★★ **读完整实现才发现它是错的**：
> ★ `pagesMatchBaseline` 只做**单向**包含检查（`measured.every((p) => known.has(p))`）⇒ ★★ 它兜住的是
> 「量到基线里**没有**的页」，★ **兜不住「基线里有、这次没量到的页」** ⇒ ⇒ ★★★★ **★ 但本轮必须记一条
> 自我更正（★ 它本身就是一条通式）**：★ 我最初**目测**基线页数「**99** 页」⇒ ★★ 推断「复跑 90 页 ⇒
> ★★ 差 9 页 ⇒ ★★ 那 9 页被静默删掉了」⇒ ★★ ★★ **而机器读数是：两份基线各 **89** 页** ⇒ ★★ **复跑
> 量到 90 页 ⇒ ★★ 差的那 1 页正是**新增页** ⇒ ★★★ **⇒ 本轮实测并**没有**出现「丢页」** ⇒ ⇒ ★★ **⇒
> 证据强度要如实降级**：「**审出、但本轮未触发**」（★ 而**不是**「实测撞上」）⇒ ⇒ ★★★★ **★ 而「目测页数」
> 本身就是一条独立教训**：★ **数字要机器读，不要目测**（★ 本轮我数路径得出 98/99，而 `len()` 给出 **89**
> ⇒ ★★ **目测多算了 10 个**）⇒ ★★ **⇒ 与「计数口径要跟数字一起发布」同族**。⇒ ⇒ ★★ 修法 ＝ **写回时
> 先保留旧基线全部条目**，★ 再让「量到的页」按 `min` 覆盖上去 ⇒ ★★ **「先保留旧的、再覆盖新的」这个
> 顺序不能省**（★ 省掉它 ⇒「没量到」被当成「已删除」⇒ ★★ **删除基线条目＝把那些页的约束悄悄摘掉**）。
> ⇒ ★★★ **★★ 判断纪律**：★ 本轮先看写回表达式就判 `action-density` 不守，★ **读完整区块才发现它用
> 「写回前拒绝放松方向」守住了** ⇒ ★★ **看半截就下结论会误改一把本来正确的门** ⇒ ★★ **错的量具比
> 没量具危险** ⇒ ★★ **判据本身要像它审的代码一样，被完整读过才配下判断**。
> ⇒ ★★★★ **★★ 一条本轮真实的操作事故（★ 教训比读数更值钱）**：★ A/B 脚本 v1 写了 `set -e` ⇒ ★★ 而
> `spacing-scale` 尺**返回 rc=1 是预期行为**（★ 它是观测工具，读数升高就非 0）⇒ ★★ ⇒ ★★ **尺一红 ⇒
> 脚本提前退出 ⇒ 「恢复改动」那步根本没跑** ⇒ ★★ **工作区被留在回退态**（★ 5 个 locale 文件的改动没了）
> ⇒ ★★ 靠外部备份才恢复回来（★ md5 逐条核对一致 ✓）⇒ ⇒ ★★★ **⇒ 变异态的「恢复」不能靠「顺序上排在
> 后面」，必须无条件执行**（★ v2 用 `trap … EXIT` 兜住）⇒ ★★ **同「门被中止时 `finally` 没跑」同族**
> ⇒ ★★ **且每次跑完必须 md5 对拍工作树**（★ 本轮就是靠这一步发现并确认恢复成功）。
> ⇒ ★★★★ **★★ `spacing-scale` 的归属（A/B 决定性）**：★ 回退「影响渲染的 6 个文件」⇒ 读数对照
> ⇒ ★★ **过挤 88 与横向溢出 10 在两态逐字相同** ⇒ ★★ **⇒ 那 +2/+4 与本轮改动零关系 ⇒ 归属上游**
> ⇒ ★★ （★ 附带：★ 本轮改动让**渲染元素 +16**，★ 但**不影响版面质量判据** ⇒ ★★ 「元素数变了」与
> 「版面变差了」是两件事）⇒ ★★ **基线建自 2026-10-27（4776 元素）⇒ 回退态实测 4960 ⇒ 上游加了 184 个
> 渲染元素（+3.9%）** ⇒ ★★ **诚实边界**：★ 本轮**没定位到具体是哪一处上游改动**造成那 2 个溢出点
> ⇒ ★★ **不猜**，★ 只登记「上游引入、★ 本轮零新增」。
> ⇒ ★★★★ **★ Go 侧仍有 5 条红在 `origin/main` 上（★ 全部上游引入 ⇒ 本轮零新增 ⇒ 只登记不代改）**：
> ★ ① **DB 对象普查四份派生台账漂移**（Go 侧 863 vs 台账 858 ⇒ 差 5 个索引
> `idx_capture_batches_tenant` / `idx_capture_messages_batch` / `idx_capture_messages_conversation` /
> `idx_outbox_events_status_retry` / `idx_task_events_claim`）⇒ 引入者 ＝ 上游 `187ec7ead`（加迁移
> 931/932/933）⇒ ★★ **同 §617 族**（生成侧动了、产物侧没重新生成）⇒ 修法 ＝ **从 SSOT 重新生成**
> （`db-object-census.py expect/reconcile/reachability`，★ **重生成前先跑 `selftest` ＝ 50 checks /
> 0 failures**，★ 本轮已实证）· ② **`pg_test_env_registry.tsv` 未登记 `LLM_GATEWAY_COOKIE_DOMAIN`**
> ⇒ 引入者 ＝ 上游 `81360f8d9` ⇒ 修法 ＝ 登记册补一行（★ class ∈ `dsn | dsn_exclusive | isolation |
> dangerous | component` ⇒ 该 env 是**应用配置而非「连哪个库」** ⇒ 归 `component`）。
> ⇒ ★★★ **★ 本轮不代改这两条的三个理由**：★ ① ★ **上游 `3df9ab91a` 的 commit message 明写
> 「补齐 931/932/933 的四个登记面」** ⇒ ★ 它正在做同一件事 ⇒ ★ 代改 ＝ 与它抢同一份产物 ＝ **竞态**
> （★ §618 记过真实竞态）· ② env 归类虽可判，★ 但那是上游那笔功能的语义 · ③ ★ 本轮改动**全在
> `web/`**（★ locale ＋ 一个类型 ＋ 两个测试文件 ＋ 两个尺脚本）⇒ ★ **零接触 Go 侧** ⇒ ★ 保持「零新增红」。
> ⇒ ★★★ **★★ 本轮最重要的方法论沉淀（★ LESSONS §619）**：★ **「上游引入的红」有保质期**——
> ★ **「不代改」的前置条件是「上游仍在推那片」** ⇒ 判「上游是否还在推」的最硬证据 ＝ **那片目录的
> diff 是否为空**（★ 本轮 `git diff --stat HEAD..origin/main -- web/` ＝ 空）⇒ ★★ 推论：**「红要收口」
> 不是按红的数量或新旧决定，是按「谁还在动那片」决定** ⇒ ★★ 同一轮里前端 6 条（上游已停手）收口、
> ★★ Go 侧 5 条（上游仍在推）只登记 ⇒ ★★ **两种待遇，必须各自写清理由**。
> ⇒ ★★★★ **★ 诚实边界（★ 别把读数当全绿引用）**：★ ① 「61 项全通过」的范围是**清单内**的
> 30 门 + 31 自证；★ 9 项刻意排除**不在这个数里**（★ 其中 6 道浏览器运行时尺本轮**补跑**了，
> ★ 但 `ui:sweep:selftest` 与 `typecheck` 本轮未跑）⇒ ★ **不许读成「前端所有门都绿了」** ·
> ★ ② **Go 侧仍有 5 条红在远端** ⇒ ★ **不许写「全仓全绿」** · ③ 本轮**只跑了 `scripts/ursmcheck` 包**
> ⇒ ★ 「其余包无红」**没测过** · ④ **本地后端仍是降级的**（★ `:8782` ＝ build_seq 2717 旧二进制，
> ★ `POST /api/auth/token` 返回 `database not configured`）⇒ ★ 依赖后端数据的读数与第 36 增量 §7.1
> 的分母**不可比** · ⑤ ★★ 本轮**跑了 6 道浏览器运行时尺**（★ 上一轮没跑）⇒ ★ 3 道通过、2 道**补登
> 基线后**转绿、1 道（`spacing-scale`）是**观测工具不是门** ⇒ ★★ **但本轮没跑这 6 道尺的「真仓变异臂」**
> ⇒ ★★ 「浏览器尺仍有牙」**靠「31 项自证全过 ＋ M4 棘轮写回变异臂」支撑** · ⑥ ★★ **hero-metric 的
> 指标格分母 116 与历史读数不可比**（★ §7.1 记 110、补交轮 31 ⇒ ★★ 三个分母互不可比 ⇒ ★ 分母必须带时刻与树）。
> ⇒ 审计档 `docs/audit/2026-11-02-第41增量-前端门清单首次整体重跑与上游遗留红收口.md`；
>   通用式见 `LESSONS.md` **§619**（★ 取号：fetch 实测 origin/main 最高 §618）。
> **2026-11-02 第 36 增量补交轮：hero-metric 门的验收收尾（三道红全部定性 + 基线补登 + census 口径订正）**
> （会话 `mvs_49b32e49a4b94018a9c4998e83b66678`，承接第 36 增量
> `43ea986a3` ＋ merge `5d10c6fc2`）：★ 那一笔的 commit message 明写「32 门清单与 Go 掏空门在
> 下一个提交回填」⇒ ★★★ **本笔就是那个回填提交**（★ 只补记，★ 不改判据立场）。
> ⇒ ★★★★ **验收读数（★ 树 = 补交时的 `origin/main` `7c3632e7c`）**：
> ★ **hero-metric 运行时尺：138 页 / 0 处墙 / 0 violations / 0 unresolved / 0 errors / rc=0**
> （★ origin 显式钉 `http://localhost:5202`）· ★ **`vue-tsc --noEmit` rc=0** ·
> ★ **`vitest run` rc=1 · 4 条红（★ 全部上游引入 ⇒ 本轮零新增）** · ★ **`vite build` rc=0** ·
> ★ **登记册普查门 PASS（0.04s）** · ★ **6 份浏览器尺基线 md5 跑前跑后逐字一致**（★ 掏空门自愈）。
> ⇒ ★★★★ **★★ 另有一组「上游引入」的 vitest 红（★ 4 条，★ 本轮零新增，★ A/B 决定性）**：
> ★ 在上游 `7c3632e7c` 的**父树** `7748d3bc8` 上复跑同一套 vitest ⇒ **✅ 全绿（289 文件 / 2986 测试）**
> ⇒ ★★ 那 4 条红**全部由上游 `7c3632e7c` 引入** ⇒ ★★ 逐条根因是同一个族
> （★ **上游自己新加的东西没配套齐**）：★ ① 后端加了 `SourceProbeLane = "probe_lane"`
> ★ 而**前端联合没加** ⇒ 契约门判红（★ 这道门的作用正在此处）· ★ ② 上游新加的
> `liveStreamIdle.test.ts` 断言 `upstream_timeout → 'failure_timeout'`，
> ★ 而 `statusCategory` 的正则**故意**把带 `upstream_` 前缀的 timeout 排除在超时之外
> （★ ⇒ 落到 `failure_5xx`）⇒ **测试与实现的意图相反** · ★ ③④ 上游给 **9 个语种**加了
> 文案 ⇒ 「值 = 英文原文」条数上升 ⇒ i18n 值质量棘轮被突破（★ 棘轮只许降 ⇒ 必然红）
> ⇒ ★★ **这 4 条属【上游那笔的收尾工作面】⇒ 本轮不代改**（★ 登记在此，★ 交上游裁决）。
> ⇒ ★★★★ **交接单点名的三道红，全部定性完毕（★ 其中两条的猜测被证伪）**：
> ★ ① **hero-metric —— ★ 是真红，但不是「dev server 挂掉的基础设施抖动」**（★ 交接单的猜测**不成立**）：
> ★ ★★ 真因＝**上游 `f7446c911` 给 `web/src/router.ts` 加了 `/model-credibility`**
> ★ ⇒ **门派生的页面清单 137 → 138 ⇒ 基线里没有这一页 ⇒ 门判红 `HERO-METRIC-BASELINE-MISSING`**
> ★ ⇒ ★★ **这正是那道门该有的行为**（★ 新页进清单、基线没跟上 ⇒ 不许读成「通过」）
> ★ ⇒ ★★ 修法＝**跑门自带的 `--update-baseline`（★ 从 SSOT 重新生成、不手改产物）**
> ★ ⇒ ★★ **diff = `+10 / −0` 纯追加**（★ 新增 1 个页条目 + 1 条 history；★ **已有 137 页的计数
> ★ 一个都没动** ⇒ 写回值 = `min(旧值, 实测值)` ⇒ **棘轮没被放松**）⇒ ★★ 复跑 **rc=0**。
> ★ ② **`design-spacing-scale:check` ⇒ 现绿**（★ 读数：离阶 0（0 个文件）· rem 写法 0）；
> ★ ③ **`design-motion:check` ⇒ 现绿**（★ 读数：动效声明 367 处 · 八项不合规全 0）
> ★ ⇒ ★★ **交接单「这两道红疑似合并带进来的远端改动」的猜测被证伪**：★ 它们既不是本轮引入、
> ★ 也不是合并带进来，★ 而是**上游自己在这两轮之间修绿的**。
> ⇒ ★★★★ **另两笔 Go 侧红，全部零新增红（★ A/B 取证）**：★ ① **canonical 读点 TSV 漂移**
> ★ （★ 上游往 `bg/integrity_fingerprint_drift.go` / `bg/stats_minute_rollup.go` **插行**
> ★ ⇒ 行号从 259/271 平移到 272/283 ⇒ ★ 「生成侧动了、产物侧没重新生成」⇒ ★ 与并行会话那轮
> ★ 「四类派生台账漂移」**同一形态** ⇒ ★★ 上游已用 `f74842452` 重落地收口 ⇒ ★ 补交轮实测
> ★ TSV = 272、JSON = 272、实际行 = 273 ⇒ **漂移已闭合**）；★ ② **copy-quality 棘轮突破**
> ★ （★ 上游 `a440cba9e` 给 `web/src/locales/{en-US,fr-FR}/requestDetail.ts:134` 加了带
> ★ **em-dash** 的文案 ⇒ ★★ **A/B：在 `6d535ad2d` 上复跑同门 ⇒ ✅ PASS** ⇒ 上游引入）；
> ★ ③ `post-surface-ledger.tsv`（★ 589 ≠ 591）⇒ ★★ **在第 36 增量原始树 `5d10c6fc2` 上同样红
> ★ ⇒ 既有红**。
> ⇒ ★★★★ **★ 诚实边界（★ 两条，别把读数当全绿引用）**：★ ① **补交轮的指标格分母是 31（8 页有格）**，
> ★ 而第 36 增量 §7.1 记的是 **110（26 页有格）** ⇒ ★★ 差异来自**本地后端降级**
> ★ （★ `:8782` 跑的是 build_seq 2717 / build_date 20261017 的**旧二进制**，★ 其
> ★ `POST /api/auth/token` 返回 `database not configured` ⇒ ★ 靠后端数据才渲染的那类格
> ★ 整批没渲染）⇒ ★★ **分母不可比 ⇒ 读数不可比** ⇒ ★★ **「分母 110」不能用补交轮的读数复核**；
> ★ ② **补交轮没有重跑整张 32 门清单** ⇒ ★★ **「32 门全绿」本轮没测过，不许这样写**。
> ⇒ ★★★ **登记册 `scripts/ursmcheck/structured-registry-census.json` 的条数口径**：
> ★ 第 36 增量代码那笔提交时是 **54 条** ⇒ `fa1a09558`（补记）**55 条** ⇒
> ★ ★★ **上游 `1f3fc5840` 再 +1（`scripts/guards/win-test-baseline.json` 补登记）⇒ 补交时实测
> ★ 56 条** ⇒ ★★ **本笔的注记按 56 条落**（★ ★ 交接单预测的「55 条」**已过期**，★ 差在
> ★ 上游那一笔）⇒ ★★ **AGENTS 侧与 commit message 都按实测的 56 条写**（★ 莫写成 54/55）。
> ★ 通用式见 `LESSONS.md` **§522**（★ 十七条通式）；★ 审计档
> ★ `docs/audit/2026-10-28-第36增量-hero-metric覆盖面轮.md` §1 / §7.3 / §7.4。
> **2026-11-02 模型列表口径线上验证轮：生产端到端七口径实证 + 全量套件 8 门红收口 + merge 吞掉轮次记录的发现**
> （会话 `mvs_3819385bca0f43d7bf0c059acb269bda`，承接 `96a781507`）：★ 用户指令
> 「结合当前树做测试验证 → 修正发现的问题 → 提交并推送」。⇒ ★★★ **验证结论：口径实现
> 正确，本轮零改业务逻辑**；改的全是**派生台账/快照/对拍表**的漂移（四类）。
> ⇒ ★★★★ **生产端到端实证（211，build `6d535ad2`＝当前 HEAD，`96a781507` 是其祖先 ⇒
>   线上确实含本功能）**：默认 `/v1/models` → **20**（特色）；`?scope=featured` → 20；
>   `?scope=all` → **89**（全量）；`?all=true` → 89；`?scope=bogus` → **400**
>   `invalid_scope`；`/v1beta/models`（Gemini 原生形状）→ 20，`?scope=all` → 89；
>   线上 `http_request` 日志逐条对上（响应体 1918B/8450B/133B/14630B）。
> ⇒ ★★★★ **生产真库对拍（`acc-pg17/llm_gateway`，SQL 从 Go 常量导出非手抄）**：
>   精选 **20 ⊆ 全量 89**（差集为空）；★ **「全量口径没被改窄」的证明方式**＝把同一份
>   SQL 剥掉 CTE 与谓词（= 改动前原查询）在**同库同刻**跑 ⇒ 89 vs 89 **逐条 diff 为空**。
>   生产 `routing_policy.featured_models`（tenant default）**31 项**，其中 **11 项当前
>   无可路由模型** ⇒ 31−11=20 与精选口径条数逐字吻合（谓词没漏配也没多出）；
>   生产 `settings_kv` **无 `models.*` 行** ⇒ 默认走代码内文档默认 `featured`（不是碰巧配对）。
> ⇒ ★★★ **「模型数」是每分钟都变的活事实**：验证期间全量口径 **88→89**
>   （`deepseek-v4-flash` 转为可路由），精选口径全程稳定 20 ⇒ 报告里的模型数**必须带时刻**。
> ⇒ ★★★★ **全量套件门红的归因与修法（★ 一律从 SSOT 重新生成，不手改产物）**：
>   ① **admin 路由快照**（生成器 601 vs 快照 598）：真实新增 3 条探针车道路由
>   （`admin/probe_dashboard.go:1918/1921/1924`）、消失为空 ⇒ `extract-admin-routes.sh
>   --update-snapshot`；② **DB 对象普查三份台账**（Go 侧反解 858 vs 工具侧 855，差迁移 928
>   的 3 个 `idx_session_turns_probe_hot_*`）⇒ `db-object-census.py expect/reconcile/
>   reachability`（重生成前先跑 `selftest`＝50 checks/0 failures；**三份各只多 3 行，
>   其余逐字未变**）；③ **探针内容预算例外台账**：`directProbeBody` 被改成薄封装
>   （只调 `directProbeBodyForModality`）⇒ 两个 `"max_tokens": 32` 位点**搬家** ⇒
>   「台账命中不到位点」红 + 「新位点裸奔」红**是同一个漂移的两个面** ⇒ 搬锚点（序号
>   #1/#2 不变，序号是「函数内」的）；④ **请求面文档三面行号**（探测车道那几轮往
>   `main.go` 插的行 ⇒ **已提交部分**链整体 **+74**：Recovery 7974 / Auth 7989 /
>   SecurityHeaders 7992 / Logging 注释 7976-7983 / maintain 注释 7965-7970）
>   ⇒ **本表 · 设计页 · 认证表 A1 必须同钉**（该门已记**七次**重钉史）；
>   ⑤ **canonical 读点三份产物**（`check-canonical-read-point-views.py`）：`e4faccbc5`
>   改了 `bg/stats_minute_rollup.go`/`admin/live_stream_sse.go`/
>   `bg/integrity_fingerprint_drift.go` ⇒ 读点行号平移 ⇒ **形态键/SQL 摘要/列全不变、
>   只有 file:line 变** ⇒ `extract-canonical-read-point-shapes.py` 重生成（差异恰好 24 行、
>   逐条只有行号）。
> ⇒ ★★★★ **归因方法（两段）**：pristine 基线 `git worktree add --detach /tmp/lg-base-96
>   96a781507^` 复跑同 8 门 ⇒ **全绿** ⇒ 本轮改动**零引入**（红来自并行会话后续提交
>   `12f34ff30`/`9773e9266`/`caa1c5f32`/`1ba00f0ac`/`e4faccbc5` 改 `main.go`/
>   `bg/node_probe.go`/`bg/stats_minute_rollup.go` 等）。
> ⇒ ★★ **两条流程教训（本轮代价真实）**：★ **(一) 派生产物必须按「提交树」生成，不是按
>   共享工作树** —— 工作树里有并行会话**未提交的 `main.go` 改动（+9 行）** ⇒ 按工作树
>   生成的行号类产物在**我的提交树**里对不上 ⇒ **提交后门必红**（且是在别人提交之后才
>   红 ⇒ 极难归因）⇒ 正确做法＝在 `git worktree add --detach <base> HEAD` 里生成 ⇒ 拷回
>   提交 ⇒ ★★ **在干净树里复跑门**才算数；★ 附带一次真实竞态：并行会话把本轮已重生成
>   的三份 DB 对象台账**重写回了旧版** ⇒ **共享工作树里的产物随时可能被另一会话覆盖
>   ⇒ 生成完要尽快提交**。★ **(二)「失败清单」的长度受输出截断影响** ⇒ 我据第一次
>   全量跑（输出裁到 40 行）写下「8 门红」，真实是**九类**（第五类的长 diff 恰好被裁掉）
>   ⇒ ★★ **断言「只有 N 个红」之前必须先证明读数没被截断**（被截断的读数不是阴性结论，
>   是「没看到」）。
> ⇒ ★★ **超时口径**：`go test ./...` 默认 10m，而仓内自记 `scripts/ursmcheck` 实测
>   **~840s** ⇒ 默认口径**必成假红** ⇒ 复跑慢门用 Makefile 口径 `-timeout=1800s`。
> ⇒ ★★★★ **新发现：merge 会静默吞掉手写轮次记录 ⇒「代码在」≠「记录在」**：本轮发现
>   **下面那条 2026-10-29 的条目在 `origin/main` 里零命中**（代码、审计档、LESSONS §557
>   都在）⇒ 丢失点是 **`1d1108a53 merge(R180)`**（合并时该文件取了另一侧版本 ⇒ 整块记录
>   被吞）⇒ ★ 判据：**共享仓 merge 后必须自查「我的轮次标题还在不在」**
>   （`git show origin/main:AGENTS.md | grep -c "<标题>"`，本轮读数 **0**）——它**不报错、
>   不影响构建与测试** ⇒ ⇒ 根治要**给记录文件本身加门**。
> ⇒ ★★ **本轮明确没做（别重复排查）**：① Gemini/OpenAI 的 **retrieve 端点**仍只有 list
>   （`handler_gemini.go` 只认 `:generateContent`/`:streamGenerateContent`/`:countTokens`）
>   ——两套形状一个数据源，**要么一起设计要么都不做**，等 owner 定夺；② ★ **`cmd/gateway-v2`
>   有第二套 `/v1/models`**（数据源是 `ProviderStore`、**无 scope 口径**）⇒ **验证本功能
>   不要打 `:8782`**（本地冒烟 `scripts/local-r112-smoke.sh` 打的正是它）——生产只跑
>   `cmd/gateway`（`Dockerfile:54`）；③ 「部分精选模型不可路由」的**缺失可见化**未做
>   （生产 31 配 11 缺，客户端无从得知缺哪 11 项；下轮做但**不得复制活度门**，建议走
>   `v_routable_credential_models` 视图）；④ `models.list_scope` 的**热加载生产验证**未做
>   （需写生产设置行）。
> ⇒ 审计档 `docs/audit/2026-11-02-模型列表口径线上验证与门红收口.md`；
> 通用式见 `LESSONS.md` **§617**。
> **2026-10-29 第 40 增量：模型列表口径改造（默认只列特色模型）+ Gemini 认证核验**（会话
> `mvs_3819385bca0f43d7bf0c059acb269bda`）：★ 承接 owner 指令两件事 —— ①「网关对外的模型
> 列表 API，默认只输出特色模型，全量需要加参数才输出」；②「网关对 gemini 的适配好像有些
> 问题，请检查原厂文档，检查认证相关的信息是否正确」。
> ⇒ ★★★ **开工前的重大更正（别重复排查）**：本轮开工时读到的 `AGENTS.md` 快照是
>   **2026-10-25 的树**；期间并行会话已把 Gemini 原生认证与原生模型列表落地
>   （`0eff161c6` / `659ad160b`，`internal/auth/clientkey.go` + `models.go:ServeGeminiModels`）
>   ⇒ **「Gemini 认证没做」是过期结论**。★ **开工前必须读当前树，不是读快照**。
> ⇒ ★ **Part 1（模型列表口径）**：新文件 `domains/streaming/models_scope.go`；`/v1/models`
>   与 `/v1beta/models` **默认只列特色模型**（`routing_policy.featured_models`，tenant
>   `default` —— 全仓既有的一等概念：autoroute 的 FeaturedBonus / bg/shared_pick.go 的
>   featured 优先 / alternativesSQL 的 tier-2 / admin 的精选过滤，四处同源）；全量需显式
>   **`?scope=all`**（或 `?all=true`）。★ 口径优先级 = **请求参数 > 运维默认**；
>   新增热加载设置 **`models.list_scope`**（`TypeEnum{featured,all}`，默认 `featured`）
>   = **逃生舱**（改它即可无发版回到全量；与 r1027 的 `models.list_only_routable` 同款形状
>   —— 公开契约的默认值变更必须可无发版回退）。
> ⇒ ★★★ **本轮最值钱的一处**：精选过滤做成**同一个查询里的布尔参数 `$1`**（不是第二份
>   SQL 常量 ⇒ 十几条活度门只存在一份）；★ 但**两处坑** —— (a) 匹配**三种名字形态**
>   （canonical / raw / standardized）：`bg/shared_pick.go` 解析 featured 时认的是供应商侧
>   名字 ⇒ 只认 canonical 会让「路由当特色、发现当不存在」的模型被藏起来；
>   (b) ★★ **PostgreSQL 的 set-returning function 在 target list 里先于 LIMIT 展开**
>   ⇒ `SELECT LOWER(unnest(featured_models)) ... LIMIT 1` 只返回**数组第一个元素** ⇒
>   精选过滤静默只认一个模型，而**纯文本判据钉不住**（两种写法都含 unnest 与 LIMIT）
>   ⇒ 改写成两个 CTE（policy 先钉行、featured 再展开数组）+ 真库判据直问「CTE 产出几个名字」=3。
> ⇒ ★ **两个发现查询都带同一份过滤**（scope 与活度源正交，翻活度开关不得顺带改变公布范围）。
> ⇒ ★ **安全降级**：精选口径筛不出模型（清单为空 / 精选模型当前全不可路由）⇒ **退回全量
>   + WARN**，不是交空清单（发现端点回空会打断所有读它的客户端）；**缓存按口径分槽**
>   （`ModelsHandler.stale` map）⇒ 口径进了查询，缓存只有一格会**静默少报模型**。
> ⇒ ★ **非法 scope 报 400**（不静默回落）：客户端要全量却拿到精选（或反之）会**静默**得出
>   错误结论。
> ⇒ ★ **Part 2（Gemini 认证核验）**：★ 厂方原文实读留证 —— Gemini API reference：「**All
>   requests to the Gemini API must include a `x-goog-api-key` header**」；OpenAI 兼容文档：
>   兼容端点鉴权是 `Authorization: Bearer`。⇒ **入向**（`internal/auth.ExtractClientKey`：
>   Bearer → `x-api-key` → `x-goog-api-key` → `?key=`，`?key=` 仅在 Gemini 原生路径生效）
>   **正确**；⇒ **出向**（走厂方 OpenAI 兼容端点 `.../v1beta/openai`，executor 统一
>   `Authorization: Bearer`）**正确**。★ **入向收三种、出向用 Bearer 不是不一致，是两个端点的
>   厂方契约本来就不同**。
> ⇒ ★★ **本轮实际修掉的一处**：`docs/vendor-formats/gemini.md` 的认证说明**漏了厂方强制的
>   `x-goog-api-key` 头形态**（只写了 query string / Bearer 两种；同仓
>   `docs/QUICK_REFERENCE.md` 的 Gemini 示例用的恰是这个头 ⇒ 仓内两份文档彼此不自洽）
>   ⇒ 已订正并补齐「模型列表」一节（原生形状 + scope 口径）。
> ⇒ ★ **顺带修掉的一处**：`154-production-release-checklist` 把「`/v1/models` 模型数与 245
>   同级（685）」当**部署放行标准** ⇒ 默认口径改 featured 后该读数必然变小 ⇒ 按原文判会
>   读成「掉模型」而误回滚 ⇒ 已改为**两口径分列**（默认 vs `?scope=all`）。
> ⇒ ★ **验证**：真库判据（Postgres 17 隔离容器；夹具走仓 SSOT `internal/schemaobj.Table`
>   不手抄 DDL；样本**每条只验一件事**：canonical 命中 / 供应商侧名命中 / 不精选 / 精选但
>   凭据已死）⇒ 默认 2 条、`?scope=all` 3 条、筛空退回 3 条、CTE 产出 3 个名字；
>   ★ **变异臂三臂全红**（M1 CTE 改一行 / M2 删供应商侧名匹配 / M3 删回退 ⇒ 三处都精确报红）
>   且还原后源文件 **md5 逐字一致**；`go build ./...` ✓；`gofmt`+`golangci-lint fmt`
>   本轮文件 **0 差异**、`golangci-lint run` **0 命中**；
>   `go test ./domains/streaming/ ./settings/ ./cmd/gateway/ ./internal/ingressguard/ ./maas/`
>   **全绿**；顺手清掉 `models.go` 的死函数 `writeDBFailure`（0 调用方，且调的是旧签名的
>   `lastGoodEntries()`）。
> ⇒ ★ **本轮明确没做**：web 前端（其 `/v1/models` 引用全是**上游供应商**目录发现，非网关
>   自己的发现端点）；`/api/maas/models`（租户价目目录，另一套东西）；`GET /v1/models/{model}`
>   与 `/v1beta/models/{model}` 的 retrieve（本轮未加，需两套形状一个数据源一起设计）；
>   **多租户精选清单**（`featured_models` 表结构按租户，但全仓一律读 `default`，本轮沿用）。
> ⇒ 审计档 `docs/audit/2026-10-29-模型列表口径与Gemini认证核验.md`；
>   通用式见 `LESSONS.md` **§557**（★ 取号记录：初取 §545 / AGENTS.md 初取「第 37 增量」
>   —— **推送前实测 `origin/main` 已被并行会话占用**（AGENTS「第 37 增量」= 拦截持久化与规则
>   管理轮；LESSONS §545 已存在）⇒ 按「**后到者取下一个空闲号**」让号 ⇒ **§556 / 第 39 增量**
>   （★ 实测当时上游最大节号 §555、最大增量号「第 38 增量」）—— ★★ **再次让号**：合入前上游
>   又推进（§556 = r1048 轮 / 第 39 增量 = Gemini `countTokens` 诚实实现轮）⇒ 再让 ⇒
>   **§557 / 第 40 增量**（★ 实测当时上游最大节号 §556、最大增量号「第 39 增量」）。

> **2026-11-02 双仓同步轮：探测车道启动检查的闸门缺口根修 + 双仓树移植同步**
> （cron 同步轮，09:07 起）：★ 上游拉取 `origin/main` `c692992e3`→`fa631a405`
> （+2 提交 `9aa973e56`/`fa631a405`）**可快进**（零冲突）；读数：`go build ./...`
> rc=0（仅 vendor `go-m1cpu` 的 cgo 折叠告警，与本仓无关）+ `go vet ./...`
> 根模块与 `installer/` 双 rc=0 + `make test-short` **353 包全绿 TS_RC=0 零 FAIL**
> + 审计守卫七包全绿 + `bash scripts/checks/guards-sync.sh` rc=0（8 条「不以 guard
> 结尾」是设计内告警）+ `scripts/scan-secrets.sh --mode=normal --tracked-only`
> **BLOCK=0**（4 条 WARN＝测试夹具占位串；脚本口径 `warn ⇒ exit 0`）+ `tests/*.sh`
> 全量 **47 套 43 PASS / 4 FAIL** ⇒ 4 红（`deploy_local_contract_test` /
> `deploy_rollback_test` / `deploy_ssh_retry_test` / `session_storage_audit_gate`）
> 经**同工作区 `git stash push -u` A/B 在 pristine 上游树 `fa631a405` 上复现同样 4 红**
> ⇒ **既有环境红，本轮零新增红**（★ 用 stash 不用 worktree：deploy 类套件在 worktree 里
> 因 `scripts/deploy-lib/*` 悬空符号链接产生**误导性失败**；★ pop 后四文件 md5 与
> stash 前逐字一致；**本机另有的两笔他人 stash 原样保留**）。
> ⇒ ★★★★ **本轮真缺陷（上游 `9aa973e56` 引入）**：探测车道**启动期表存在性检查**
> （`probelane.AssertWithQueryRow`）被写在 `if !bgDataPlaneOnly {` 闸门**内**，而它
> 守护的探测**写路径**（telemetry → `probelane.PersistTurn`）的可达性由
> `telemetryClient.SetDB(dbConn.Pool())` 决定、那处**只看 dbConn 不看
> bgDataPlaneOnly** ⇒ **data-plane-only 实例（245 用 `bgDataPlaneOnly=true`）写路径
> 照跑、检查一次不跑** ⇒ 「flag 开 + 迁移 928 未上」在 245 上**静默失败**
> （每条探测都失败、只留一条 error、下游 hooks 哑）＝ 正是这个检查要防的那个面。
> ⇒ ★ **修法**：检查**移出 `!bgDataPlaneOnly` 闸门**（仍在 `dbConn.Enabled()` 块内
> + `dbConn.Pool() != nil` 守卫）⇒ 修后 **AST 祖先链实测零 `bgDataPlaneOnly` 引用**
> （临时探针打印）。⇒ ★★ **门禁用 AST 判据而非文本拼写**（新增
> `cmd/gateway/probelane_boot_check_wiring_guard_test.go`）：**反向拼写
> `if bgDataPlaneOnly { } else { <检查> }` 时被搜的串根本不出现**（同 autoroute
> 停摆族）⇒ 改判**每层 `IfStmt.Cond`/`ForStmt.Cond`/`SwitchStmt.Tag` 是否引用该
> 标识符**，并同时钉住写路径的 `telemetryClient.SetDB`（两处必须同时与运行模式
> 无关，否则耦合悄悄腐化）；★ **变异臂 2/2 检出**（CONTROL 绿 / M1 挪回闸门内红 /
> M2 反向拼写红；★ **M1 第一版被生成器写坏成编译失败 ⇒ 编译失败 ≠ 判据红**，补
> 「变异体先 `go build` 通过」前置后重生成；还原 md5 逐字一致）。
> ⇒ ★ **gofmt 实测口径**：本仓 11279 个 .go 里 **540 个非 gofmt-clean** ⇒
> **gofmt 不是本仓的门** ⇒ 只修自己动过的 `cmd/gateway/main.go`（上游加
> `probelane` import 造成的 import 注释对齐漂移），**不做全仓改写**（避免与并行
> 会话产生无关 diff 面）。
> ⇒ ★ 双仓同步：codeup/github 两条历史**互相独立**（merge-base 停在 10-07；codeup
> 侧 3345 提交 github 侧没有、github 侧 13503 提交 codeup 侧没有）⇒ **`git merge`
> 合不动** ⇒ 走**树移植**（`commit-tree <origin/main^{tree}> -p <github/main>`，父提交
> 取镜像侧原 tip ⇒ 镜像原始历史一条不丢；`sync-to-github.sh` 的 filter-repo 与
> 「保留原始历史」**互斥**，仅作 `--dry-run` 诊断）⇒ 验收口径＝**两仓 main 的
> tree SHA 必须相同** + `git diff --stat origin/main github/main` 为空 +
> `git ls-remote` 对拍远端真值。
> ⇒ 审计档 `docs/audit/2026-11-02-双仓同步轮-探测启动检查闸门缺口.md`；
> 通用式见 `LESSONS.md` **§616**。
> **2026-11-01 24h 审计轮：prompt-injection 时间戳 scan 族根修 + 48h 遗留三项复核**
> （goal 会话，17:56 起）：★ 48h 轮遗留全绿——① 新槽 tick（11-02 14:38+08）
> **未到不写闭案**，中间读数干净：lt10min 4 对全部落在 14:38–14:48 =
> **部署交接尾巴**（旧槽 a 活到 14:38:35 的最后探针 × 新槽启动序列，14:48 后
> 零新增）；17 时 263 行全失败 = **每日 6h anomaly 全池扫荡的正常一拍**
> （05/11/17/23 时节律，昨日同拍 257 行，每目标仅 1 行非放大，404/410 存量
> 下线模型族）；② dropped_events 全 166 实例**零增长**；③ sanitize audit
> queue full WARN=**0** 零复发（grep "queue.*full" 的 12 条命中全是
> probe_necessity INFO——正则误匹配）。⇒ ★★★★ **本轮真缺陷（潜伏 4 个月）**：
> prompt-injection 管理面 5 读端点（policy/rules/engines/canary/vectors）把
> 11 个 timestamptz 列 scan 进 Go string——**pgx v5 prepared-statement 对
> timestamptz 回 binary 格式，*string 只放行 text/varchar ⇒ 有行即 500**；
> handler 起源 06-29 + 表行 06-30 起 + 页面 4 个月无人访问（三证考古）⇒
> 自上线即坏，今日 18:04 首次访问触发。⇒ ★★★★ **与并行会话实时撞车零通信
> 调和**：我方 18:0x 落 `::text` 投影修复 + 隔离 schema 真库腿 → 并行会话
> 18:19 落 `*time.Time` 方向测试三件套（AST 门明文禁 `时间戳列::text`）
> → 双方通过文件系统互相看见：**并行版 handler 修法胜出**（::text 产出
> PG 文本格式非 RFC3339 移动端解析不了 + 可空列 NULL::text 扫非指针 string
> 仍 500；*time.Time + RFC3339Nano + PUT 写载荷分离才是正解）、**我方
> 隔离 schema 真库腿骨架胜出**（并行版并入其前提门/update 体成 7 子测试）、
> 静态门独立成文件（字段指针/禁::text/RFC3339 JSON 三门，空库不假绿）⇒
> `9d3b190de`（+793/−41）调和提交已推送；变异臂 2/2（字段回退 string ⇒
> 编译红；投影回退 ::text ⇒ AST 门红）还原 md5 一致。⇒ ★ 部署：**211 也
> 已按用户明确要求重部署**（初稿决策是「211 本轮不重部署」，理由=保放大类
> 闭案 tick 锚点 11-02 14:38；**但用户目标原文要求「使用脚本部署到 211 上，
> 并进行测试验证」**，且该 500 是**线上当前活跃缺陷** ⇒ 不部署=缺陷继续在线
> ⇒ 执行重部署；★ **代价记实**：新槽 StartedAt 2026-11-01T10:42:11Z =
> 18:42:11+08 ⇒ tick 锚点顺延到 **11-02 18:42**（≈4h，**顺延非取消**）；
> ★ 读数：**211=2929-bb020177**（蓝绿 b@8783→**a@8781**，build_seq 2928→2929，
> 脚本自带验收全绿：readyz/SPA/网页面/公网版本身份 bb020177/公网入口/登录
> 冒烟连登 6 次无 429；迁移全 already applied；**双口径缺陷验证**：127.0.0.1:8781
> 与公网 https://llm.itestu.cn 两端点均 **200**（policy 881B、
> `created_at=2026-07-19T13:08:25.836952Z` RFC3339 ✓、`last_detection_at=null`
> 如实 ✓、rules count=23）；部署后 30min 日志 admin handler internal error=0 /
> "Failed to get policy"=0；**生产数据零改动**））；245/154 部署（★ 部署坑实录：
> 后台 shell 必须
> `export PATH=$HOME/sdk/go/bin` 否则 deploy-seamless `go: command not found`
> 回退 docker 跑法而三 registry 拉不到镜像即拒——**首跑失败形态**）：
> **245=2927-9d3b190d**（验收 admin 登录 200/token 289B/修复 5 端点全 200，
> policy 真行 created_at RFC3339Nano ✓）+ **154=2928-bb020177**（部署时刻
> 本地 HEAD 已含并行会话三个 docs 提交，`is-ancestor` 实证 9d3b190de 在
> 血统内；登录 200/端点 200）——服务端代码三台同源，211 与 245/154 的
> 差异仅文档提交。⇒ ★★ **新工具链坑（已根修 `b6837d64a`）**：
> `scripts/deploy-test-gate.sh` 声明 `# requires-bash: 4`（`declare -A`）⇒
> macOS bash 3.2 必红；**且「用 bash5 跑 deploy-211.sh」救不了**——脚本内部
> 三处硬写 `bash` ⇒ 解释器被降回系统 bash ⇒ **调用方的 bash 没被继承** ⇒
> 修法＝三处改 `"${BASH:-bash}"`；★ 自查教训：改解释器入口**必须实跑**
> （`bash -n` 抓不到「多一层 bash」那个 `cannot execute binary file`）。
> ⇒ ★ 例行审计：211 ERROR 411 全归因（探针 503 族 359 + executor 上游 44 +
> **admin 6=本轮缺陷** + provider 2）；245/154 零错误；**acc-pg17（网关库）
> 14 条错误仓内零引用 ⇒ 全部外部工具打错 DSN**（llm_usage_stats 族
> 17:09 波 11 条 + prompt_injection_attack_vectors/output_compliance_keywords
> 18 点 3 条——注意本仓查的是不带前缀的 injection_attack_vectors）；
> ssh "Connection closed" 坑：**245/154/252 别名全走 25022，直连 IP 默认
> 22 被立即关闭**。⇒ ★ make test-short 的 **admin 120s 结构性边缘**：三跑
> 同位置超时（120.1-120.3s）+ 单独三跑全绿（103-106s）= 边缘定性非回归
> （admin -short 基线 104s 对 timeout=120s 只剩 16s 余量，全量并行被抢
> CPU 即越线；勿当回归上报，勿擅自改口径）。⇒ 审计档
> `docs/audit/2026-11-01-24h审计轮-promptinjection时间戳scan族与遗留复核.md`；
> 通用式见 `LESSONS.md` **§611**。
> **2026-11-01 48h 审计轮：12h 修订 WIP 调和落地 + 三台部署**
> （goal 会话，14:14 起）：★ 承接 0310 会话未提交 WIP（其审计档
> `docs/12小时内修订审计-2026-11-01-0310.md` §6 自称已落地，但代码从未提交——
> 其间并行会话把**子集**落为 `38dbec6c9`）。⇒ ★★★★ **撞车调和逐文件 best-of-both**：
> backfill 查询语义取 origin 版（会话级窗口+两源并读——0310 版仍是 turn 行级窗口，
> 正是 0720 轮修掉的缺陷）、backfill 写入路径取 0310 版（分块事务 200/块+failed 计数
> +非零退出+独立预算+`--execute` 强制 `--from`，origin 版没修失败面）、
> `tool_json_invalid` 形态族（常量/形态表/`output_sensitive` Issues）与批写退路+计数
> 为 0310 独有净新增。⇒ ★★★★ **合并假绿实录**：`git merge origin/main` 对
> `internal/ir/extensions_restore.go` 自动合并成功但**语义错误**——本地
> `ccd3fb006`（树==e3babdddb）的 reasoning 循环内特例与 `7feb23b5b` 的「阶段 1
> 确定性快照」重构落在不同文本区 ⇒ 3-way 两份都保留 ⇒ 确定性回归会复活 ⇒ 判据：
> **origin 侧已重构过的文件，合并后对 origin 的 diff 必须为空**。⇒ ★ 分支治理复核
> 零未合并分支（origin/local/github 各仅 main；父仓全部分支 is-ancestor）；/tmp/lg-verify
> 遗留 worktree 已清。⇒ ★ 日志审计三台：211（07b521d8）458 ERROR/8h 全为上游可用性族
> （探针 503 stream_interrupted/429/all-0-candidates×2 fail-fast，无本仓缺陷）；PG 无本仓
> 可归因错误，外部噪声=kxuser 不存在角色探测 11/24h+外部工具手搓 SQL（`LIKE %llm%` 裸写）；
> 245/154（28e0076e）0 ERROR；慢 SQL 仍不可观测（log_min_duration_statement=-1，
> 48h 轮已登记待决）。⇒ ★★ **容器日志时区坑**：docker logs 时间戳是容器 TZ（UTC），
> 「日志停在 8h 前」先对时区再下结论；本仓日志级别是裸词 ` ERROR `/` WARN `（不是
> `level=error`），量具口径错 ⇒ 全零假读数。⇒ 读数：go build/vet ✅、四包+test-short
> 351 包全绿（TS_RC=0）、10 新回归用例 PASS、变异台 3/3（RuleID 置空/去 dropped.Add/
> 去 failed++），还原 md5 一致。⇒ 部署：211/245/154 三台（见审计档 §6）。
> ⇒ 审计档 `docs/audit/2026-11-01-48h审计轮-12h修订WIP调和落地与三台部署.md`；
> 通用式见 `LESSONS.md` **§610**。
> **2026-11-01 12 小时内修订审计轮：ursmcheck 整包红根修 + 工具/脚本三处「宣称能力与实现不符」**
> （goal 会话，审计窗口 10-31 19:10 → 11-01 07:20、34 提交）：★ 窗口内业务逻辑达标，但
> **P0：`c82fa475c` 新增的 modelcredibility 真库契约腿直接消费事实源统一下发的
> `TEST_DATABASE_URL` 而按库名做决定（`current_database()` `_test` 后缀守卫）⇒ 必然拒绝
> ⇒ `scripts/ursmcheck` 整包报红（基线绿 / HEAD 红），连带掏空门「不可判定」——
> `cf8bf8d1e` 提交信息自称「guards 全绿」，该读数在其写下时即不成立** ⇒ 修法照
> maas/bills 既有形态：腿改专用名 `TEST_MODELCREDIBILITY_ROLLUP_DATABASE_URL` +
> 双车道登记（`dsn_exclusive_registry.tsv` + `session_storage_gate_test.go` 的
> `destructiveGuardedDB`）+ `make test-pg-contracts` 补入口（★「写了腿」≠「门在那里」）。
> ⇒ ★★ **P1 三处工具缺陷**：① `backfill-session-opened` 只读 hot 表（8h 排水后历史
> 会话漏 10/13=77%，漏的恰是它要补发的 ⇒ 改两臂并读 hot ∪ parent，PG17 实跑 3→13
> 与全历史真值逐字相等）；② 同工具 `--from/--to` 窗口作用在 turn 行而非会话级
> （跨窗口会话 occurred_at 取错 ⇒ DISTINCT ON 取全局最早、窗口谓词移外层）；③
> `deploy-seamless-docker.sh` 的 build_seq floor **锁不保护读**（锁外读+锁内写不是
> 临界区，6 路并发全撞 101 ⇒ 读移进锁内 + `except OSError` 降级不崩，修后 101..106 全异）。
> ⇒ ★ **P2**：`stream_compliance.go` event 路径裸 error 换与 lane 路同款 `PolicyBlockError`
> （「先补形态表再改码」的前置条件在登记时就已满足——形态表按形态枚举，第二个调用点
> 天然落在表外）+ `verdict_order_test.go` 把 VerdictRank 与 SQL CASE 次序互相钉住
> （断言同序而非相等，两侧数字方向刻意相反）+ 掏空门条目表述收窄到本门实际覆盖侧；
> `security/sanitize/output_sensitive.go:209-210` **登记保留未修**（形态不同类，需补 Issue 另修）。
> ⇒ **menu-config.json 是构建产物不纳入本轮提交**。审计档
> `docs/12小时内修订审计-2026-11-01-0720.md`；通用式见 `LESSONS.md` **§609**（8 条）。
> **2026-11-01 参数兼容静态化轮：跨端点 reasoning 实证 + DialectNVIDIA 登记 + reqprobe 退避**
> （goal 会话）：★ 承接 nvidia 直通事故轮（learnedTTL 1h）与 r1021 教义。
> ⇒ ★★★★ **四腿实机矩阵（OpenRouter/zhipu/方舟/nvidia，凭据自 211 生产库
> 本地解密）**：(1) **「chat 线顶层 reasoning 对象普遍被拒」不成立——三行为并存**：
> nvidia NIM 硬 400「Unsupported parameter(s): \`reasoning\`」（直探被组织级 429 挡，
> 证据取 reqanom 台账四天连证）；**zhipu/方舟 200-但-静默忽略**（对象形态
> effort=minimal 与基线无异=被吞；意图无声丢失比 400 更隐蔽）；OpenRouter
> **原生生效**（free 腿基线思考打满 4096，两种拼写都压到 ~340=都生效）。
> (2) **「双表达只留下划线」成立**：四腿全部认 reasoning_effort，**没有只认
> 对象的供应商**；OpenRouter 双表达冲突值端点级 400（欠费 402 账户上仍 400=
> 校验先于计费），同值 200；zhipu/方舟冲突 200 且**下划线赢**（方舟判据是
> 确定性的：reasoning_effort=minimal 归零思考 0/0/0，冲突臂 0/0/0）。
> ⇒ ★★★★ **r1021 登记落地**：新 `DialectNVIDIA`（catalog "nvidia" 登记前恒
> Unknown fail-open）+ `reasoning` 行 RejectedBy[NVIDIA] + **thinking/
> enable_thinking 补 NVIDIA**（★ 方言登记后 dst 不再是 Unknown，RejectedByUnknown
> 对它失效——不补会把 unknown-relay 时代已裁剪的字段重新放进 nvidia 请求体，
> 这是加方言时的隐藏回归面）+ `ReasoningObjectChatDecision`（chat 线特例：
> 硬拒查 registry/双表达剥对象内 effort 留旋钮（exclude/max_tokens 仅
> OpenRouter 有意义）/无下划线时翻译方言集 {GLM,Ark,DeepSeek,Grok,OpenAIChat}
> 把对象提升成 reasoning_effort——zhipu/方舟否则静默吞意图；值域由 paramguard
> 出站钳制兜底）。**Kind 保持 IRHandled 不改 Translatable**：IRHandledFields
> 驱动 TransportIR 侧 isStandardField（211 实测 TRANSPORT_IR 未启用，仍按
> 零风险原则）。⇒ ★★ **TTL 复核：不做 (provider,model) 静态分级表**
> （LearnedKey 本就是该粒度；静态表需人工维护不自适应）⇒ **指数退避**：
> learnedWindow = base × 2^(RecoveredCount−1)，上限
> LLM_GATEWAY_REQPROBE_LEARNED_TTL_BACKOFF_MAX 步（默认 4=16×，0=关），
> memory/redis 两 cutoff 同换；被反复证实的拒绝探测频率指数下降
> （nvidia glm ≈5 次/3h → ≈5 次/天档），指纹含 Day 退避自然日重置。
> ⇒ ★ **变异臂 9/9 检出**（registry 三行/翻译集/特例整块/下划线探测/退避指数/
> memory+redis cutoff），还原 md5 逐字一致；三包 + paramguard + transformation +
> executors 全绿。⇒ ★ **实测坑**：volcano coding 端点 CodingPlan 订阅失效 +
> normal 端点 key 401（两腿凭据皆死，方舟腿用 cred 11 打 /api/v3 替代，处置属
> 运维）；OpenRouter free 档仅 agentic harness 可用（403 拦截）；zhipu
> reasoning_effort=minimal 枚举接受且生效（GLM「7 档」实证补笔）；
> prompt_cache_key 的 serialize 侧方言门控留卡（chat 入向进 IR 恒发射，
> RejectedBy 对主线不生效）。
> ⇒ 审计档 `docs/audit/2026-11-01-参数兼容静态化轮-跨端点reasoning实证.md`；
> 通用式见 `LESSONS.md` **§607**（§606 被并行会话在途占用，取号 provenance 在节首）。
> **2026-11-01 输出合规总开关启用轮：管理面 PUT 热加载 + 验收④漏网路径登记**
> （goal 会话）：★ 生产 `sanitize.output_check_enabled` 已由 false 经**管理面 PUT**
> 置 **true**（02:43:52；`PUT /api/admin/sanitize-rules/output-check`，super_admin）——
> 三面核验全过（读回 enabled=true source=db / settings_kv prev_value=false /
> 211-b 容器审计日志 `output_check_switch_changed`），零重启；
> **被动生效证明**：hook.go:68 在开关 false 时结构性不可能写 output/egress ⇒
> `sanitize_rule_events` 的 output/egress 两相 last_seen/count 开启后前进
> （A/B 60s：egress +6 / output +12，input 对照 +1659）。⇒ **语义面**：活了 =
> mandatory 支路（强制外传守卫 + 容量守卫，interceptor.go:106 只闸总开关）；
> **仍闸着** = 可选输出合规 + 写出前脱敏（二级键 `output_compliance.enabled=false`
> 10-17 既有态，属 owner 另行决断，本轮不动）。⇒ **验收④两个漏网路径已登记不修**
> （现行树行号：`stream_compliance.go:984/:987` 裸 error（验收④记的 :995 系行号漂移）
> + `output_sensitive.go:209-210` 工具 JSON 非法 Blocked 无 Issues ⇒ 空 RuleID；
> 运行树 5269313a2 是 HEAD 祖先且两文件 426393659 后零改动 ⇒ 行号对线上成立；
> 修法方向=归因轮同款 PolicyBlockError / 带 RuleID 的 Issue；**先补形态表再改代码**）。
> ⇒ 审计档 `docs/audit/2026-11-01-输出合规总开关启用轮-管理面PUT与漏网路径登记.md`；
> 通用式见 `LESSONS.md` **§605**。
> **2026-11-01 24h 审计轮：放大类闭案推进（tick 未到）+ dsn_exclusive 共享台账 + all-0-candidates 闭卡**
> （goal 会话，2026-10-31 23:22 起）：★ 承接 48h 轮全部遗留。⇒ ★★ **② 优先收口：闭案判据
> 物理 未到**——`automation-43481e79`（明晚 23:30）runCount=0 未执行；自己跑同款 lag 分桶
> SQL：新槽 `llm-gateway-211-a`（StartedAt=10-31 20:59:40+08）窗口内仅 **4 行启动探针
> lt10min=0/0** ✓，但**启动探针 ≠ 24h tick**（判据=11-01 20:59 未到），本轮**不写闭案**、自动化
> 原样保留（fire 时 tick 已过 2.5h）；近 24h 分布与 48h 档逐字一致零新增。⇒ ★★★★
> **③ dsn_exclusive 结构项落地（`e619cef44`）**：共享台账
> `scripts/audit/dsn_exclusive_registry.tsv`（9 条 name⇥reason），ursmcheck 字面量豁免缩成
> 5 条 + `loadDsnExclusiveRegistry` 装载牙齿 + 门 1 **死条目棘轮**（TSV 名字仓内零真库腿
> 引用 ⇒ 红）；sql/schema 旧单向正则重写为 `TestDestructiveDBsRegisteredInSharedDsnExclusiveTSV`
> **双向对账**：(a) destructiveGuardedDB ⊆ TSV；(b) **TSV ∩ 扫描器形状 ⊆ 两车道并集**
> （destructiveGuardedDB ∪ dedicatedSessionDB）——★ 第一版「TSV ⊆ lanes」全量断言被
> 5 条 `_DSN` 后缀名字打红：主门形状表（抽包级 `isDBCredentialEnvName` 共用）不含
> `_DSN` ⇒ 它们对主门不可见从未需要车道；形状表扩到 `_DSN` 时对账门强制补车道。
> **变异臂 4/4 检出**（删行两侧红/删文件两侧装载器红/摘注册表条目车道对账红/假行
> 死条目棘轮红），还原 md5 逐字一致。⇒ ★★ **元清点门反抓本轮**：guards 终态树首跑
> `TestTsvHollowoutInventoryCoversEveryTsv` 报新台账未登记 `tsv_hollowout_inventory.tsv`
> ⇒ 登记读方+掏空行为（`0d2b62454`，答案恰是变异臂 M2 实测）——新增数据文件 = 新增
> 登记册 ⇒ 元门必然点名（§595 同族）。⇒ ★★★★ **④ all-0-candidates 闭卡**：「~8 条
> available=t」是**目录属性不是路由判据**——`v_routable_credential_models.is_routable`
> 折叠 8 道闸（含 node_probe_state 冷却窗），15 绑定逐条归因后**能进 DB 候选层仅 3 条**
> （cred 3/49/50）；「4 候选」= 事故窗视图闸时变输出，**无候选上限（SQL 无 LIMIT）、
> 无 URSM 截断** ⇒ 整池真红 fail-fast 正确，原卡预评估的语义变更**前提不成立无需提案**；
> 观测面可选改进（属主裁决）：DB 层排除原因进日志。⇒ ★★★★ **① guards 抓到并修正一处
> 上游真红**：`TestAdminRoutesSnapshotMatchesGenerator`（快照 1519 vs 生成器 1524）——
> `426393659` 改 admin/handler.go 未重生成快照 ⇒ 该门只在 ursmcheck（test-short 排除的
> 慢包）⇒ **红潜伏了半天直到第一次跑 guards**；按生成器口径重生成（290 行 file:line
> 对齐，行数 598 不变）。test-short 351 包全绿（钉 73c8c8fd5 worktree；坑：worktree 里
> make 的 PATH 要显式 export `~/sdk/go/bin`，首跑 RC=127）；ursmcheck 全包 ~894.8s ⇒
> **自设 580s 超时把 hollowout 慢门掐成假红，超时口径必须用 Makefile 的 1800s**。
> ⇒ ★★ **⑤ 三台本轮不重部署（偏离固定流程，理由入档）**：`git diff 28e0076ef..HEAD`
> 实证**服务端代码零增量**（只新增独立工具/文档/deploy-seamless-docker.sh）；211 重部署
> 会把 tick 锚点推迟一天砸掉闭案（**部署决策的输入还有「有没有以进程生命期为锚的验收
> 在飞」**）；245/154 重部署为纯搅动；`merge-base --is-ancestor` 实证 926 已在运行树
> （旧「待部署」账已消解）。巡检：245/154 槽位 active（2922/2923-28e0076e）、无窗口分支、
> **154 acc-nginx 仍 Restarting 登记不擅动**。⇒ ★ 并行会话中途落盘（domains/streaming
> 3 文件 + menu-config.json，后自己提交为 9485f53bc/d12ebe07b/3c4f9d522 并推送，
> **LESSONS §601 被其占用 ⇒ 本轮取 §602**）；全量读数分两档：基线在钉住 worktree、
> 主 checkout 只跑定向门并标注。⇒ 用户侧动作两项不代做：245 alertmanager webhook 静默 +
> kb-importer 工具坏转义盲插。⇒ 审计档
> `docs/audit/2026-11-01-24h审计轮-放大类闭案推进与dsn_exclusive共享台账.md`；
> 通用式见 `LESSONS.md` **§603**（8 条）。
> **2026-10-31 48h 审计轮：集成门 maas DSN 注册修复 + 满窗复测 + 日志慢SQL审计**
> （goal 会话）：★ 承接第 12 轮 §5 遗留。⇒ ★★★★ **① 共享 checkout 上「全量测试绿」必须先
> 回答「测的是哪棵树」**：合并 5 提交后 make test-short 首跑 2 红（sql/schema 集成门真红 +
> cmd/gateway build failed），全量复跑**换了失败位置**（admin 120s 超时）⇒ 当场 git status
> 实证**并行会话 21 项 WIP**（其拦截归因轮，后已成 426393659）⇒ 两次全量跑测的都是别人
> 中间态 ⇒ **worktree 方法**（钉在已推送提交上：**test-short 351 包全绿 TS_RC=0**；
> ursmcheck 需先在 worktree 的 web/ 装依赖，错误信息自己会指路）——git status 干净是全量
> 读数有效性的前置判据（LESSONS §600 通用式一）。⇒ ★★★★ **② 真红修复 = dsn_exclusive
> 双台账第三次反向报红**：58dbfc3a5 新增 TEST_MAAS_BILLS_DATABASE_URL 只在 ursmcheck
> 豁免表登记，sql/schema 集成门不认识 ⇒ **反向要求 $GATE_URL 注入**（恰是豁免禁止的事；
> R86 REPORT_E2E 第二次、本次第三次）⇒ 修法两层：destructiveGuardedDB 升级「路径+各自
> 守卫牙齿」注册表（reportrollup 与 maas 各自断言不共用一把尺）+ 新增两台账机械对账门；
> **变异臂 2/2 检出**（M2 破坏 maas 守卫断言 / M3 摘 ursmcheck 条目，还原 md5 一致）；
> 反方向对账需共享数据文件 ⇒ 结构项留档（提交 498c46b9b）。⇒ ★★ **③ 满窗复测**（21:00
> 实测，窗口 20:58 满 24h）：**lt10min dup 99/33 与第 12 轮逐字相同、零新增**；07:07 后
> （含 16:12 tick 与 19 时 45 行 4h 重开轮）始终 0；313 行 / failed 254 全分类零空 error /
> 按轮全 <100 ⇒ 满窗判据达成；闭案最后一环=**重部署后新槽的 24h tick**（动态锚点自动化）。
> ⇒ ★★★★ **④ canceling statement ≠ 慢查询**：postgres 日志 12 次取消 ⇒ 假设「UNION ALL
> 大视图 + ts≠partition_date ⇒ 参数化计划裁剪失效」⇒ EXPLAIN 成本上成立（33.6k vs 8.3k）
> **ANALYZE 实测反转（28.5ms）** ⇒ 取消=客户端上下文/部署竞态 ⇒ 不带证据链就会做一次
> 无收益的视图 DDL；真慢风险（10 月分区 1.97GB、details 全扫线性增长）因 pg_stat_statements
> 未开启**不可观测**（维持待决）。⇒ ★ **⑤ 日志审计收获**：42P10（23 条）**全部 ≤ 部署前
> 3 分钟 = 闭案实证**；今日 42P18=他产品（f.pe/pb/roe 股票表）；relation request_logs 不存在
> =非本仓 PG 代码（仓内仅 sqlite store 用该名；共享实例日志无 %d/%u 前缀不可精确归属）；
> **kb-importer 外部工具**坏转义+盲插撞主键（用户侧动作）；211 **首现 all-0-candidates 34 条**
> （deepseek-v4.1-flash，08:37–11:18 探针 429/403 池可用性窗自愈；**调查卡：库内另有 ~8 条
> available=t 绑定为何 resolve 只出 4 候选**）；fp_slot 降级 22 条=按设计工作（8h 零复发）；
> **queue full=0**（攒批+4096+节流生效实证）；modelbinding WARN=3=minimax@cred21 真歧义
> 设计保留（3 请求=3 WARN，与第 12 轮同源探针一致）。⇒ ★ **⑥ 21:12 收口自动化已删除**
> （与本轮部署并发冲突）⇒ 本轮接管其全部工作 + 明日动态锚点改排；245 systemd 两单元已定性
> （collector-service=禁用单元 10-15 陈旧失败态、reset-failed 可清 / nim-case-cron=每日
> 04:30 对已 EOL 的 NIM 必红，处置属运维）。⇒ 审计档
> `docs/audit/2026-10-31-48h审计轮-合并修复与日志慢SQL审计.md`；通用式见 `LESSONS.md`
> **§600**（6 条）。★ **收口自动化补记（11-01 23:30 执行，§2.3.1）**：tick 未到（锚点已两度
> 顺延 ⇒ 11-02 18:42，自动化改排）；中间读数 ~40h 仅 **3 对 lt10min** = cred 42 MiniMax
> **success 基准双执行者一次性重叠**（00:13–00:18 自灭，非放大类），14:48 后恒 0；
> 与 24h 轮「4 对 14:38–14:48」判读口径分歧已如实并列（两类归因均非放大类，不影响判据）。
> **2026-10-31 拦截归因轮：守卫失败可归因 + 读时归因逐单窗口**（goal 会话，
> 承接同日「211 拦截审核合理性分析」轮）：★ **本轮先做批判式审计，订正上一轮三处判读**
> ⇒ ★①「审核面是空白清单」**半错**：审核单**表**空白（24/24 条 `rule_id/reason/证据` 全空），
>   但**读时归因机制已存在且已接线**（`admin/sanitize_rules_review_context.go`，按 `request_id`
>   联表 ⇒ 211 实测 24/24 命中上下文）；★②「10-28~29 波次全是容量越界」**过度归因**：
>   10-29 12:38 的部署已把容量越界改成 release ⇒ **12:38 之后**的空 rule 拦截**不可能**是它
>   ⇒ 波次成因有**截止点**；★③「唯一人工判例 rv-22」**是 921 终验的功能验证标注**
>   （文案自带「921 终验：」）⇒ 不是运营判读。
> ⇒ ★★★ **根因两层**：① 守卫侧 fail-closed 分支返回**裸 error**（检查器报错 / lane 不可安全
>   改写 / 脱敏结果非合法 JSON / 无请求态 / 帧无法重编码）⇒ 审核单空；② **读时归因被一个
>   全局 `LIMIT 50` 整体打败**（24 条待审单横跨 **40 小时**、窗内 **56 行** egress 事件 ⇒
>   预算被**最新**吃光 ⇒ 早期单**零候选** + 整批标记截断 ⇒ 归因一律 unknown ⇒ ★★ 读时归因
>   对真正需要它的历史空白单**等于没生效** ★★）。
> ⇒ ★★ **本轮修法**：① 三条 fail-closed 路径改 `PolicyBlockError`（自带规则名/原因/证据），
>   两条「误挂 capacity」路径各自归因（`no_request_state` / `frame_encode_failed`）；
>   ② 6 条 `builtin.guard.*` 逐条登记**内建展示说明**（★ 不登记 ⇒ 审核面显示「（托管规则…）」
>   ⇒ ★★ 指向错误事实源 ★★）；③ **flush 路径补证据传播**（★ lane 级拦截的错误自带 Evidence，
>   写者此前只取 rule/reason/field/digest ⇒ 证据被丢 ★）+ 非流式四个登记点补证据；
>   ④ 读时归因改**逐单窗口 + 逐单预算**（一次 `LATERAL + unnest` 往返；截断粒度降到单条）；
>   ⑤ `grantReviewRelease` 跳过 `builtin.guard.*`（不写死行）；⑥ `goal_control` 的
>   `output_compliance_enabled` 恒 false（`len(interceptors) > 3` 恒假 ⇒ 与同一份日志里的
>   「output compliance guard configured」**互相矛盾**）⇒ 按「装上即真」修。
> ⇒ ★★ **守卫档归因措辞订正**：守卫侧失败**不等于**误报（命中敏感内容却不能安全改写时，
>   阻断是**正确**行为）⇒ ★★ 不许说「可直接标误报」★★；处置方向是**容量/形态/上游可用性**，
>   不是规则表达式。
> ⇒ ★ **211 数据修正 1 条**（可逆、窄谓词）：`rv-22` 的 `label='false_positive'` 是终验产物
>   ⇒ 误报率分母（「有判断的记录数」）被污染 ⇒ `unattributed` 桶读成 **100% 误报**
>   ⇒ 会把运营引向「去放宽规则」（★ 正好伤到精准度 ★）⇒ 清 `label` 留注记
>   （桶读数 labeled 1→0 / fp 1→0）。
> ⇒ ★★ **本轮踩到的静默坑（新 LESSONS §599）**：新真库测试文件初名
>   `postgres_block_review_windows_test.go` ⇒ ★★ `_windows` 被 Go 当 **GOOS 约束** ⇒ ★★ 该文件
>   在 macOS/Linux 上**被静默排除**、`go test` 报「no tests to run」而不报错 ⇒ ★★ 一条真库
>   判据可以整轮**从未运行** ★★ ⇒ 改名 `..._perwindow_test.go`；查法 `go list -e -json |
>   .IgnoredGoFiles`。
> ⇒ ★ **读数**：受影响包测试全绿（outputcompliance 0.084s / sanitizerules 1.767s /
>   admin 95.735s / streaming 75.432s / cmd-gateway 19.324s）+ build + vet ✅；
>   ★ **真库腿**（一次性本地 PG17，simple + extended 两种协议各 PASS）+ 211 只读 SQL 复跑
>   同形状（窗0=0 条 / 窗1=3 条）；★ **变异臂 2/2 检出**（删证据传播 ⇒ 红；退回整批窗口 ⇒ 红；
>   还原后 md5 逐字一致）。
> ⇒ ★ **本轮未部署 211**（部署属下一轮，须钉 `LLM_GATEWAY_ROOT`）；★ **未代标历史空白单**
>   （无证据 ⇒ 不做未经证实的判读）；★ **未改生产规则快照**（`pii.address.egress` 结构上无法
>   硬阻断 ⇒ 无行为影响；`compliance.internal_ip` 是 log-only ⇒ 改了只削弱覆盖）。
> ⇒ 审计档 `docs/audit/2026-10-31-拦截归因轮-守卫失败可归因与逐单窗口.md`；
> 通用式见 `LESSONS.md` **§599**（7 条）。
> **2026-10-31 24h 审计第 12 轮：完整窗口复测与 4x 复发定性 + resolver 验证闭环 + sanitize 队列批写根修**
> （goal 会话）：★ 承接第 11 轮 §5 遗留前三项。⇒ ★★ **窗口校准：goal 写「新窗口已满 24h」，
> 实测 17.3h**（37004018 起于 10-30 20:58，读数时刻 14:14）⇒ **goal 的前提也要按时钟算**。
> ⇒ ★★★★ **①「根修后复发」：01:10 出现 4x 放大爆发又自灭**（dup<10min 99 行/33 对，
> 同对 2-5 秒串行重探，115 行/29 对 ≈ 4.0x）——20:58 部署的 37004018 **含 DISTINCT**（镜像 grep 实证）
> 且第一轮 dup=0 ⇒ 爆发轮另有其主；**其容器日志已随两次蓝绿替换湮灭**（07:07 与 14:29 b 槽两度重建）
> ⇒ 特定进程不可归因（如实记），类别归因=**部署后残留的旧代码进程**按自身 24h tick 打了一轮。
> ⇒ ★★★★ **放大类消除的判据是「类根因在两层消失」，不是「某一轮干净」**：routing_policy
> 927 单例约束后实测 **1 行**（数据层）+ 两处发现查询 DISTINCT（代码层，现行代码仅 3 处
> CROSS JOIN routing_policy：两处有 DISTINCT、shared_pick 有 LIMIT 1）+ 07:07 后三轮全净
> （29/38 行=对数）；复测锚点=当前槽位的下一 24h tick（明午 14:10/14:29 后）dup 必须为 0。
> ⇒ ①五项判据终判：failed<100 按轮全过（17/15/25/36）、error 163/163 全分类、
> success 18.7% 全窗（第一轮 44%，其后是**失败窗重开的慢性失败对**，构成性下降非回归）、
> 同伴对比 **1.36 家/5 家 ≥2**（上轮新窗口 1.00/0——改善但池结构未变）、node_iq_latest 51→59。
> ⇒ ★ **245/154 维持不开；「同伴对比 ≥2」按 goal 授权降级为管理面裁决项**
>（池结构属性非工程指标；`enable_per_node` settings 开关仍是唯一事实源，判据变参考读数）。
> ⇒ ★★ **② resolver 降噪已由并行会话落地**（e98737794，best_rank 分层消歧，已部署 211），
> 本轮补**验证闭环**：211 真数据 rank 实测（glm: flash rank=1 vs 非 flash rank=3 ⇒ 静默 ✓）、
> **exact 双候选全库=0**（minimax 数据线已被处置）、两新槽 modelbinding WARN=0；
> **补行为单测（log spy 4 分支）+ 两臂变异全检出**——此前门槛只有 SQL 形状契约与选择结果断言，
> **改坏时测试全绿**（「日志行为本身就是交付物」）；**minimax 判读订正**：本轮一度据
> 「同名绑定=0」误判「minimax 数据线已处置」，实际 minimax 走**归一化路径**且两候选同 rank ⇒
> 收口轮同源探针实测 **WARN=1（真歧义保留）**——量对不同的歧义类不能互相作证。
> ⇒ ★★★ **③ sanitize audit queue full**：触发面=**输出合规把规则命中面扩到每个响应**
>（sanitize_rule_events 的 output/egress 指纹首现 10-30 实证），生产 ~4-5 条/秒 > 单 worker
> 逐条事务（BEGIN+advisory lock+upsert+retention DELETE+COMMIT 五次往返/事件）⇒ 256 深队列持续满。
> ⇒ ★★ **攒批本体已由并行会话（第 11 轮遗留收口轮）落地并三台部署**（d953b55e3 → 7eddace4，
> `BatchEventWriter` 可选接口 + 128/批 + count 进 INSERT 列表 + dropped_events 上 status API；
> 与我方独立实现撞车 ⇒ **合并以其生产已验证的实现为基底，我方重复部分丢弃**）。
> ⇒ ★ **本轮正交增量**：**queue 256→4096**（上游 DB 抖动+命中风暴同发的突发余量）+
> **queue-full WARN 节流 30s/条并带累计 dropped_total**（攒批修「丢事件」，节流修「丢事件发生时
> 的告警风暴」——旧行为每丢一条报一条=44250 WARN/3h 自我刷屏；管理面出口=收口轮的
> Status().DroppedEvents）。
> ⇒ ★★★ **批 upsert 的正确性前提**：单条 `INSERT ... ON CONFLICT` 在同一语句对同一键命中两次
> 会报 "cannot affect row a second time" ⇒ 批内必须先按冲突键归并（count += n）——**是正确性语义
> 不是性能优化**；真库集成验证（本地一次性库 sanitize_batch_tmp，unnest 多行 upsert 语义）+ 凑批/节流
> 两臂变异全检出（还原 md5 一致）。
> ⇒ ★ **本地未提交修改的批判审计**：main.go goacos 停机事实订正——事实声明**全部核实**
>（StopAsync 零调用点 / DeregisterTimeout=3s / 四个 tracked 单元 TimeoutStopSec=25s），
> 唯一缺陷=「决策表 **B96**」全仓不存在（悬空引用）⇒ 改指向审计档遗留清单（§5.8），
> 停机行为不动（31s>25s 的并发注销/单元超时调整仍待决）。
> ⇒ ④常规：合并 origin/main 28 提交；154 的 **acc-nginx 崩溃循环**（nginx 1.31.5 pid 权限 EPERM，
> 60 次重启）——**无主机端口绑定的内部容器**，主机 nginx 正常、服务面不受影响 ⇒ 登记不擅动
>（并行部署会话的活动面）；154 all-0-candidates 当前部署 51min 零复发（历史日志已随容器替换轮转，
> 分桶不可做——复发的第一动作是抓当场日志）；慢 SQL 连续第五轮不可查（preload 仍只有 citus）。
> ⇒ **三台全量部署 298a5cd7**：211 槽 b/8783（16:12 起，冒烟 OK）/ 245-2918 / 154-2919；
> **部署闸门实战拦下一次假红**（我方凑批测试为已让位的 linger 设计而写，在收口轮无 linger
> drainBatch 下时序敏感 docker 49/50 ⇒ 比对基线判新红拒上线 ⇒ 撤测试放行——
> ★ **为被让位设计写的测试必须随实现一起撤**）。154 的 acc-nginx 崩溃循环登记不擅动
>（内部容器无端口绑定，服务面不受影响）。
> ⇒ 审计档 `docs/audit/2026-10-31-24h审计第12轮-完整窗口复测与审计队列批写.md`；
> 通用式见 `LESSONS.md` **§598**（5 条）。
> **2026-10-31 第 11 轮遗留收口轮：sanitize 审计攒批 + resolver best_rank 部署判据**
> （211 线上 = `2.5.8-d953b55e-20261031-211`，槽 b/8783）：★ **sanitize audit queue full
> 已收口**——根因=排水侧单条写路径（每条事件付 事务+跨实例 advisory 锁+O(水位) 保留期 DELETE）
> ⇒ 修法=攒批（`BatchEventWriter` 可选接口，128 条/批、每批一事务一锁一清理；
> ★ **聚合列必须进 INSERT 列表**：`count DEFAULT 1` 会吃掉批内聚合值 ⇒ 新 fingerprint 的
> k−1 次命中无声丢失，变异臂 M6 抓到）；`dropped_events` 已上 status API
> （`7eb19712d` 去 omitempty，**211 线上暂为缺席=0 形态，下次部署显式 0 上屏**）。
> ⇒ ★ **resolver best_rank 分层已在 211 线上并按部署判据验收**：同源探针
> `ResolveRawBinding` 对真库四臂全 PASS——glm-5.3-flash@cred19 selected=`z-ai/glm-5.3-flash`
> 且 WARN=0（特异性 Debug 留痕）；**minimax-m2.7@cred21 WARN=1（真歧义保留，不预设 WARN=0）**；
> minimax-m2.7-highspeed 随分层静默。★ **AGENTS 归档拆分（dfcb1bc36）字节级三重核对无丢失**
> （移出段==归档正文 12758 行 / 保留段零改动 / 拆分后归档未被触碰）。
> ⇒ 审计档 `docs/audit/2026-10-31-第11轮遗留收口轮-sanitize审计攒批与resolver部署判据.md`；
> 通用式见 `LESSONS.md` **§596**（六条：排水侧单条写路径 / 聚合列必须进 INSERT /
> 终态计数分不清聚合与累加 / 可选接口的桩不能两头实现 / 观测面显式零 / 管道吞 rc）。
> **2026-10-31 维护轮：双仓同步 + 927 台账漏登修复**（会话
> `mvs_0a20a52276a24605b7ea29823d70169b`）：★ 承接 12h 审计第 1 轮（`fd3d243d5`）
> 新增的迁移 **927**（routing_policy 单例化）⇒ ★ **它从未进 `docs/db-changelog.md`**
> ⇒ ⇒ ⇒ `internal/sqlguard` 的迁移连续性门（`TestMigrationRegistryStaysContinuous`）
> **判红**（★ 窗口锚点取**磁盘侧最大编号** ⇒927 恰在近邻窗口 [908, 927] 内 ⇒ ⇒ **真红**，
> ★ 且 **pristine HEAD 上同样红** ⇒ **不是回归**）⇒ ⇒ ★ 机器核验：927 的**五点同步全在**
> （embeddata 副本逐字节一致 / main.go 的 go:embed + 映射 / runner.go StartupFiles /
> 通道腿 files=() / TSV 第 327 位）⇒ ⇒ ★★ **唯独台账这一面漏了** ⇒ ⇒ ⇒ ★★ 而
> LESSONS §290 那份「六个点位」清单**本身也不含台账** ⇒ ⇒ ⇒ 清单漏掉的面没人会提醒你。
> ⇒ ★ **修法**：台账补一行（sha = **文件实测值**）＋ 新增
> `sql/migrations/startup/migration_927_routing_policy_singleton_unique_test.go`
> （★ 七处登记点**逐点断言且每点恰好出现一次** ＋ 台账 sha 实测 ＋ 语义面）⇒ ⇒ ★
> 四臂变异台全检出（★ 含「守卫原样保留、只挪到 BEGIN 之前 ⇒ 门报**落在事务块外**」）。
> ⇒ ★★ **★ 本轮测得一条 handover 级事实（★ 原文未记 ⇒ 别再重复排查）**：
> ⇒ ★★ **`make test`（全量口径）当前【结构性地不可能全绿】** —— ★ `scripts/ursmcheck`
> 属 `GUARD_PACKAGES_SLOW`，★ 本轮实测 `make guards-ursmcheck` 需 **407.8s**
> （★ Makefile 另记有机器实测1122.8s）而 `make test` 给 **300s** ⇒ ⇒ ★★ **它必然超时
> panic** ⇒ ⇒ ⇒ ★★ **CI 从不跑 `make test`**（★跑的是 `make test-short` 本轮 **rc=0 /
> 2m55s** ＋ `make guards`）⇒ ⇒ ⇒ ★★ ⇒ **别把这条超时当回归上报，也别擅自改
> `make test` 的口径**（★ 审计轮已把「CI 有无否决权」登记为**属主待决**项 P3-4）。
> ⇒ ★ **另一条既有红**：`make guards-deploy` 与 `tests/*.sh` 的 **7 套**
> （★ deploy_lock / deploy_switch_lock_contract / deploy_via_identity_gates_iso /
> deploy_local_contract / deploy_rollback / deploy_ssh_retry / session_storage_audit_gate）
> ⇒ ⇒ ★★ 根因 = **工具沙箱对「脚本内嵌套的 mavis-trash 调用」拒绝删除**
> （★ 同一调用在工具顶层直调**成功**）⇒ ⇒ ★★ **同工作区 A/B（不含本轮改动）逐条同样红**
> ⇒ ⇒ ★★ **既有环境红、非回归** ⇒ ★ 前 4 套本仓 AGENTS.md 早已登记。
> ⇒ ★ **双仓同步**：codeup `855c1ffe4`（★ 本地 HEAD 一致）+ github 树移植 `e6621be21d`
> （★ tree `e7f1f48efe6b` 两仓相同、★ `diff --stat` 为空、★ 镜像原始历史一条不丢）。
> ⇒ 通用式见 `LESSONS.md` **§595**（3 条）。
> **2026-10-30 第 15 增量：耗时治理 + count_tokens 限流收口轮**（会话
> `mvs_bebea3efc1bd4fee824da9780c6ddce2`）：★ 承接 `docs/audit/2026-10-29-Gemini-countTokens诚实实现轮.md`
> §9 登记的三项（转发上游那一支 / Anthropic count_tokens 无 RPM 缺口 / streaming 耗时逼近余量）。
> ⇒ ★★★ **第 3 项的登记描述与事实不符**（本轮最值钱的一处发现）：登记写的是
> 「约 150s，**逼近** `-timeout=120s` 的余量」⇒ ★★★ **实测是已经越界**：
> 全包 **163.561s**、`-short` 口径 **120.249s 直接超时 panic** ⇒ ★★★
> **`make test-short` 当前就是红的**（它是 `.github/workflows/sessionforensics-ci.yml:42`
> 的调用方）⇒ ★★★ **⇒ 通式：「余量还剩多少」是【会漂的读数】不是事实**
> ⇒ ★★★ `ingressguard` 文件头那句「余量只剩 13.5 秒」**写下时就已经过期了**
> ⇒ ★★★ **注释里的读数不会自己失效** ⇒ 它会变成一句**指向相反结论的可信假话**。
> ⇒ ★★★★ **根因一：测试构造了一个【生产不存在的客户端】** —— **32 处**测试用
> `upstream.New()`（`maxRetries=2` + 5s/10s 退避 ⇒ 15s），而**生产用
> `upstream.NewWithRetries(0)`**（`cmd/gateway/main.go:828`）⇒ ★★★ **这不只是慢，
> 是【口径漂移】：测试测的失败语义与生产不同** ⇒ ★★★ **改动性质是「让测试测回生产」**。
> ⇒ ★★★ **根因二：「超时参数」≠「超时后返回」** —— `sse.LineReader.ReadLineWithContext`
> 超时后走「`closer.Close()` → 再 `<-results` 等读协程退出」⇒ ★★★ 若 `Close()`
> 不解除 `Read` 的阻塞，**超时门设成 1s 也要等满 sleep** ⇒ ★★★
> 修法用 channel（★ 仓内 `internal/sse/line_reader_test.go` 已有对照实现）。
> ⇒ ★★★ **改共享测试设施的外溢成本**（本轮自愈）：给 `hangingBody` 加字段后
> ★ **另一个文件复用它却不初始化该字段 ⇒ `close(nil channel)` panic** ⇒ ★★★
> ★ **改共享测试设施前先数清复用面**（★ 本轮 3 个构造点）+ 字段做 nil-safe。
> ⇒ ★★★ **第 2 项（Anthropic count_tokens 补 RPM 闸）已落地**：闸位在
> **鉴权之后、读 body 之前** ⇒ ★★★ **闸位就是它的全部意义**（否则被拒的请求
> **已经付掉了 128MB 的读取成本**）⇒ ★★★ **⇒ 必须有一条【位置断言】**
> （★ 只断言状态码的门挡不住「闸被挪位」）⇒ 登记册 `gateNone` → `gateOwn`。
> ⇒ ★★★ **两个方向的变异臂都验证过**：闸挪到读 body 之后 ⇒ 位置断言 rc=1；
> ★ 删掉整道闸 ⇒ 登记册门 rc=1（报「登记为 own，但锚点出现 0 次」）⇒ ★★★
> ★ **⇒ 登记表与代码必须【双向咬合】**（只做「代码改了就更新表」是单向的）。
> ⇒ ★★ **一张测试表要容纳语义不同的成员时【显式区分】优于【放宽】**
> （★ count_tokens 不是推理面 ⇒ 不写请求日志 ⇒ 给表加一列显式声明，
> ★ **而不是把「恰好 1 条」放宽** ⇒ 放宽会架空既有三面的断言）。
> ⇒ ★★ **测试判据坑**：`checkGatewayRateLimit` **只在 ctx 带 deadline 时**才走
> `AdmitRPMWithBudget` ⇒ 用只实现了该方法的桩去测**不带 deadline 会假绿**。
> ⇒ ★★ **第 1 项（转发上游那一支）未做**：★ **其口径前提被代码推翻了** ——
> ★ 网关出站体就是 `ir.SerializeGemini(IR)`（`executor_gemini.go:1454`）
> ⇒ ★★ **转发归一后的体**（而非客户端原文）⇒ ★★ **「两个口径」的前提被消解**
> ⇒ ★★ 但「以哪个为准」仍是产品裁决 ⇒ **已向 Owner 发问，裁决前不实现**。
> ⇒ ★★ **读数**：全包 **163.6s → 59.2s**；`-short` **120.249s(panic) → 55.8s**
> （★ **从红变绿**）；`-race` **260.4s → 123.7s**；`ingressguard` 绿；
> `go build ./...` ✅；`go vet` ✅；两向变异臂 rc=1 且还原后 **md5 逐字一致**。
> ⇒ 审计档 `docs/audit/2026-10-30-耗时治理与count_tokens限流收口轮.md`（8 节）；
> 通用式见 `LESSONS.md` **§576**（6 条）。
> **2026-10-27 第 9 增量：属性承载键 / 运行时拼键 两类引用集盲区修复轮**（会话
> `mvs_dc5e181989fc4539805f343f39be2b5d`）：★ 承接第 8 增量（§414）——**新门
> `i18n-cjk-native-simplified-guard.mjs` 的「用户可见」口径只认字面量调用**
> ⇒ **运行时拼键**（`t(\`prefix.${x}\`)`）与**属性承载键**（`labelKey: 'nav.item.xxx'`）
> 这两类**用户可见**的键**从未进过引用集** ⇒ 既没进过工单、也没被门看过。
> ⇒ ★ **扫描器扩展**（`web/src/i18n/scanner.ts` 加 `KEY_PROP_RE` / `PROP_PREFIX_RE`，
> `scanFile` 内先还原运行时前缀再入集）⇒ 引用集 **5252 → 5371**；
> ★ **前缀解析缺失会造 29 处假引用**（`promptInjectionFieldBasic` 的真键是
> `sessions.config.promptInjectionFieldBasic`）⇒ 加前缀解析后归零。
> ⇒ ★ **9 处残留假引用治的是根因不是症状**：`utils/journeyStageMap.ts` 的 `labelKey`
> 存的是 **journey stage 名**（真键 `requestDetail.flow.diagram.stages.<stage>`）⇒
> **改名 `stageKey` + 注释写明「它不是 i18n 键」** ⇒ parity 门 hard gate 29 → 9 → **0**。
> ⇒ ★★ **门口径修正（本轮最值钱的一处）**：CJK 门的 `refKeys` 只含字面量 ⇒
> **运行时拼键下的键全落进「未引用」桶，而它们是用户可见的** ⇒ 「用户可见残留 = 0」是**假绿**；
> ⇒ 把 `callSiteParams.ts` 的 template 静态前缀（实测 34 个）接进 referenced 判定 ⇒
> **同一门立刻报出 zh-TW 27 / ja-JP 39 条整条中文原文**（`providerDetail.errorDetail.*`
> 整块 25 条、`providerDetailPage.quality.*` 12 条、`dataLifecycle.hotTableNames.*` 4 条）
> ⇒ **补译 197 个值后归零**（zh-TW 49 / ja-JP 63 / de 20 / es 20 / fr 20 / ar 25）⇒
> 未引用残留同步降 ja-JP 337→294、zh-TW 320→288 ⇒ 门自证 20/20 → **22/22**（新增
> S15b「动态前缀下的键算 referenced」/ S15c「前缀不匹配的仍算 unreferenced，不许把潜伏存量洗白」）。
> ⇒ ★ **字表判据不完备**：「Big5 转不出来 = 简体专用字」——**Big5 自己收录了简体字形**
> （`iconv` 对「异」返回 `c9dd`，该码位存的是繁体「異」）⇒ 异/据/与/余/卖永远进不了
> iconv 推导的 notBig5 ⇒ 门抓不到 ⇒ 生成器新增**人工审定的正名单** `BIG5_TRAPS`
> （★ 与 §414「豁免表要人工负名单」互为镜像）；「与」「余」因出现在 ja-JP 已本地化值里
> 而**自动**进 `japaneseOk` ⇒ ja-JP 不误报、zh-TW 照抓。★ 已知**未穷尽**（185 候选待人工审定）。
> ⇒ ★ **两个基线都动了**：`localeValueBaseline.json` 的 `untranslated` **人工显式**重铸
> （+1 ~ +5，理由写在同文件 `baselineRaiseReason`：引用集扩大 ⇒ 82 个真未翻译值**已补译**，
> 剩余增量是**合法英文同形**）⇒ ★★ **调高基线的理由必须是「检测器变准」而不是「新增了未翻译内容」**；
> `sameAsSource` 同步下调（ja-JP 1345→607、zh-TW 2133→1474）。
> ⇒ ★ **独立复核**：8 语种各 **9410 键零增删 + 占位符零失配 + 8 语种键集合一致**（用
> `git archive HEAD web/src/locales` 导出 HEAD 侧、**同一个 loader** 加载两边逐键比对）；
> 7 个 i18n 门全 PASS；`vue-tsc` 0 错；`vitest` 2815 通过 / 2 跳过；`build` ✓ 11.64s。
> ⇒ ★★ **本轮明确没做（留给下一轮，别重复排查）**：**应用外壳 i18n 化**
> （`brand.ts` 的 `SITE_TITLE`/`SITE_TITLE_LINE_TWO`、`LoginModal.vue` 的 8 条硬编码、
> `edition.ts` 的 `LOCAL_OPS_MENU` 7 条接 `labelKey`、`appNav.ts` 的 `label` 兜底收口）
> ⇒ 涉 80 个新语言包值 + 4 处组件/配置改动 + 一轮独立 A/B ⇒ **整体留给第 10 增量的首个子项**；
> **字表完整正名单**（185 候选 ⇒ 暴露「繁体化了一半」的半成品族）；**语言包死键**
> （ja-JP 294 / zh-TW 288 条 ⇒ 实测**源码 0 引用** ⇒ 是**嵌套命名空间重构后遗留的历史键**，
> 用户看不到 ⇒ 独立的删除型工程）；**后端下发的中文**；**剩余 4758 段硬编码中文**；
> **扫描器剥注释**（幽灵引用 11 处 / 8 文件）；**移动端 8 语言**。
> ⇒ 审计档 `docs/audit/2026-10-27-第9增量-属性承载键与运行时拼键引用集盲区.md`；
> 通用式见 `LESSONS.md` **§415**（通用式一~七）。
> **2026-10-27 第 10 增量：应用外壳 i18n 化轮**（会话
> `mvs_dc5e181989fc4539805f343f39be2b5d`）：★ 承接第 9 增量遗留的「应用外壳 i18n 化」子项
> —— ★ **外壳此前有一整套「绕过 i18n 体系的第二份真值」**：
> `config/brand.ts` 的品牌标题是**硬编码中文常量**（`SITE_TITLE = 'AI Native 组织核心网关'`），
> 出现在 **5 个外壳组件**（App / AppTopbar / LoginModal / LandingView / UpdateActivateView）
> ⇒ 换语种时这 5 处仍是中文，而**语言包里没有任何一份可以改的文案**；
> 同时 `app.ts` 里还有一个 **0 引用的死键** `brand: 'AI-Native 组织网关'`（两处品牌字符串**拼写都不一样**）。
> ⇒ ★★ **`appNav.ts` 每个菜单项同时带 `label`（硬编码中文）与 `labelKey`** ⇒ 两份真值
> ⇒ 而 `label` 是**死副本**（渲染走 `navLabel(labelKey, fallback)`）**却仍在漂移**：
> 实测 `appNav.ts` 的 `'License管理'`（无空格）与语言包的 `'License 管理'`（有空格）**当场不一致**，
> 另有 2 处 `label` 的值是**英文**（`'Sensitive information rules'` / `'Agent Registry'`）⇒ 中文界面里也上屏。
> ⇒ ★ **`edition.ts` 的 `LOCAL_OPS_MENU` 7 条**与 **`LoginModal.vue` 8 条**同样硬编码
> ⇒ **`/examples#auto-config`** 是唯一没有 `labelKey` 的菜单项。
> ⇒ ★★ **本轮最值钱的一处自伤自愈**：本轮一度写 `export const SITE_TITLE_KEY = 'app.brand.siteTitle'`
> + 模板里 `t(SITE_TITLE_KEY)` ⇒ **引用集扫描器只认字面量** ⇒ 这两个键**根本没进引用集**
> （增量：常量写法 **+11** vs 字面量写法 **+13**）⇒ 它们**逃出了「用户可见简体残留 = 0」那道硬闸的视野**
> ⇒ 改回字面量，并**另建一道门** `scripts/i18n-hidden-key-guard.mjs` 把这条路堵死
> （判据：裸标识符 + 可解析成字符串常量 + 值形如点分键 ⇒ 存储键/枚举值不命中；
> **自证 8/8**（含「注释里的 `t(CONST)` 判绿」「模板串 `${}` 里的调用判红」）；
> **真仓变异臂**实测：把常量写法放回真实代码 ⇒ `rc=1` 报出确切行号 ⇒ 还原 ⇒ `rc=0`）。
> ⇒ ★ **15 处 `t(key, '中文兜底')` 全是死代码**：全仓真值 58 处逐一核对 8 个语言包 ⇒ **键全部存在**
> ⇒ 危害不在多写字，而在**它把「缺键」伪装成「已翻译」** ⇒ 本轮删掉外壳这 15 处 ⇒ **缺键从此显形为键路径**。
> ⇒ ★ **改动面**：语言包 8 语种 × 4 文件、**净 +7 键/语种**（9410 → 9417；`app.brand` 字符串→对象
> `{siteTitle, siteTitleLineTwo, alt}` ⇒ 顺带删掉 1 个死键）；**删掉 64 段硬编码中文 `label`**
> （appNav 62 + 插件组 1 + `NavItem.label`/`NavGroup.label` 改可选 + `OpsMenuItem.label`/`OpsMenuGroup.label` 改可选
> + `OpsMenuGroup` 加 `labelKey` + `mergeRemoteOps` 转发分组 `labelKey`）；
> `LoginModal.vue` 8 条、`UpdateActivateView.vue` 标题行（h1 **复用** `nav.item.updateActivate` ⇒ 同一句话不许两份）。
> ⇒ ★ **读数**：引用集 **5371 → 5384**（+13，移除 0）；8 语种复核**新增 64 / 删除 8 / 值变化 0 / 占位符失配 0**；
> 硬编码中文 **4758 → 4651（−107）**；9 个 i18n 门全 PASS；`vue-tsc` 0 错；
> `vitest` 2815 通过 / 2 跳过（两处既有测试随改动更新：`appNav.test.ts` 断言从 `label` 改 `labelKey`；
> `LoginModal.test.ts` 的 i18n 从**桩**改为**挂真实 zh-CN 语言包** ⇒ 缺键从此会让测试失败）；
> `build` ✓；浏览器 A/B 四台 vite（访客对 + 登录对）：**预期外差异 0 / 键路径上屏 0 / 读数缺失 0**。
> ⇒ ★★ **读数修正（不是本轮降的）**：`localeValueBaseline.json` 的 `untranslated.ar-SA` 由 980 → **979** ——
> 在 HEAD `6f7b1d11b` 上**实测**就是 979 ⇒ **上一轮人工重铸的基线比实测高 1** ⇒ **人工重铸 ≠ 实测，基线也会漂**；
> 其余 6 语种实测值与基线逐项相等；本轮新增 9 键对 `untranslated` 净贡献 = 0。
> ⇒ ★ **本轮明确没做（留给下一轮，别重复排查）**：**远端运维菜单的中文**（maintain 后端下发 `label`
> 覆盖本地兜底 ⇒ 运维中心在非中文语种仍是中文 ⇒ 属「后端下发的中文」工作线）；
> **`t(k)` 形态的键仍是引用集盲区**（全仓 1549 处非字面量 `t(...)` ⇒ 这些键不受「用户可见」硬闸保护）；
> **页面级硬编码中文**（计数 4651 段 / 181 文件，本轮只清了外壳面）；
> **`UpdateActivateView.vue` 其余部分**（105 段中文里只清了标题行）；**语言包死键**（ja-JP 294 / zh-TW 288 条）；
> **字表完整正名单**（185 候选）；**移动端 8 语言**。
> ⇒ 审计档 `docs/audit/2026-10-27-第10增量-应用外壳i18n化.md`；
> 通用式见 `LESSONS.md` **§417**（通用式一~十一；★ 节号因并行会话占用 §416 而在合并前改号）。
> **2026-10-27 第 11 增量：清晰文案门轮**（会话 `mvs_dc5e181989fc4539805f343f39be2b5d`）：
> ★ 承接用户的总目标（「学习 impeccable 的技能与规范，优化整个网关的 UI —— 减少 AI 化、
> 增加人味与艺术化、把复杂表达做得更人性化」）⇒ 前两轮清的是 **i18n 覆盖**，
> 本轮转向 **文案质量本身**：把 impeccable `skill/reference/clarify.md` 的三条判据
> （**错误三问** / **别用手势** / **空状态要能区分**）落成**可执行的门** + 8 语种同步改写。
> ⇒ ★ **新门 `web/src/i18n/copyClarity.ts` + `web/scripts/copy-clarity-guard.mjs`**
> （放 `src/i18n/` 是因为要 Vue 感知的 `maskSource`，且可被 vitest 覆盖）
> ⇒ **判据是位置驱动不是内容驱动**（「文案里有『失败』两个字」≠「这是错误文案」）：
> 错误位置 = ① 兜底形状 ② 错误槽位（赋值目标**逐段**含 err/error/msg/message/fail
> 且整串不含 filter/label/type/status/code）③ 报错调用 ④ throw/new Error；
> 排除日志语境与标签语境。**作用域** = `App.vue` + `views/` + `components/` + `composables/` + `hooks/`
> （排除 `api/` `types/` `utils/` `locales/` `i18n/` ⇒ 接口层中文仍是人工清单，门看不见）。
> ⇒ ★★ **本轮最值钱的一处发现（门自己的盲区）**：门在接线完成后归零 ⇒ 按纪律做
> **真仓变异臂** ⇒ **三臂全绿** ⇒ 真因不在判据而在**区间配对**：
> `templateRanges` / `scriptRanges` 用 **`indexOf('</template>')`** 找结束
> ⇒ 遇 Vue 嵌套 `<template v-if>` 就提前截断 ⇒ 实测 `RoutingDashboardView.vue`
> 顶层 `<template>` 在 974 行、第一个嵌套在 991 行 ⇒ **区间 994 行就结束**
> ⇒ **995~1683 行整段模板从未被扫过** ⇒ ★ 改用**深度计数**（开 +1 / 闭 −1，归零才是结束）后
> 门从 **0 命中变成 7 命中** ⇒ **那 7 处此前一直被判绿**。
> 同族修法：`scriptRanges` 同样错，而 `.vue` 允许 **`<script setup>` + `<script>` 两个块**
> （`views/admin/UsageTrendExplorer.vue` 就是）⇒ 两个块被合并成一段 ⇒ 语句上下文判错。
> ⇒ ★ **7 处新暴露的真违规**：R2 手势 ×5（「点击 ↻ 刷新」「点击详情查看」「点击上方变体按钮」
> 「点击行跳转」「点击下方按钮弹出」）+ R3 通用空状态 ×2（客户成本表 / 模型成本表的「暂无」）
> ⇒ **改法**：手势换「动词+宾语」（按刷新键 / 打开详情 / 选择上方的变体 / 选择任意一行 /
> 在下方确认协议后）；空状态**点名缺的是什么 + 时间范围**（「这段时间没有客户成本记录」）
> ⇒ ★ 客户成本 / 模型成本是**两张表 ⇒ 两个键**，不共用一个「暂无」（同一句话不许两份）。
> ⇒ ★ 第 7 处（`bootstrap/BootstrapWizardView.vue`）按**仓内明文纪律**处理
> （`views/SelfCheckPanel.vue:185` 记载「本文件全文件不用 i18n」）⇒ **就地改写中文、不引入键**。
> ⇒ ★★ **顺带发现「源码抄语言包」族的实例**：`routing.dashboard.overview.emptyHint` 与
> `routing.dashboard.policy.empty` **在语言包里已存在但全仓 0 引用**（死键），
> 而源码**硬编码了同样的话** ⇒ 本轮把源码接回语言包 ⇒ 该族消掉 1 个实例。
> ⇒ ★ **语言包**：zh-CN 真值 99 键（16 改值 + 83 新增）+ 第 2 轮 6 键 ⇒
> **键数 9396 → 9476（+80；19 个键 HEAD 已存在故 +80 而非 +83）**；
> 8 语种 key 集合完全一致 / 占位符 0 失配（逐语种独立复核，不用工具自己的成功日志当证据）；
> 另建 `web/scripts/locale-key-writer.mjs`（**AST 定位 + 保格式替换 + 缺段自动建**，
> 命名空间 = 文件名）——它自己踩过两个**静默的错误落地**坑（路径没剥命名空间段 ⇒ 值被追加到根上；
> 插入点落在尾逗号之前 ⇒ 写出 `highlightOff: '…',,`）⇒ **批量落地后必须独立复核键集合**。
> ⇒ ★ **真仓变异臂（第 2 版·最终）**：M1 R1（脚本字面量）/ M2 R2（模板静态文本）/
> M3 R3（独占文本节点）三臂**全检出**（注入 rc=1 + 命中 1 行 ⇒ 还原 rc=0 ⇒ md5 逐字一致）；
> ★★ **第 1 版三臂全绿的教训已写进 `LESSONS.md` §419**：靶点不能选注释行 / 已被 `$t()` 取代的位置 /
> 与相邻段落连成一段的模板文本 ⇒ ★★ **「0 命中」有两种读法**（扫到了没发现 vs 根本没扫到那一段）
> ⇒ ★★ **「门归零」之后要立刻做变异臂，而不是直接写「全仓通过」**。
> ⇒ ★ 自证 16 → **19**（新增 S17 嵌套 template 之后 / S18 之前 / S19 双 script 块
> —— ★ S19 一开始写成 `expect: {}` **形式通过零牙齿** ⇒ 改成断言命中 2 处；
> **断言恒真比恒假更隐蔽**）。
> ⇒ ★ **本轮明确没做（留给下一轮，别重复排查）**：**语言包侧同族**（bareError 340 / gesture 68 /
> emptyGen 152 共 560 处 / 8 语种 ⇒ 门只扫源码侧）；**「源码抄语言包」族**（632 处 / 107 文件，
> 本轮消掉 1 个实例，族本身未治）；**`t(k)` 形态的键仍是引用集盲区**（1549 处非字面量 `t(...)`）；
> **页面级硬编码中文**（第 10 增量后 4651 段 / 509 文件）；**语言包死键**（ja-JP 294 / zh-TW 288；
> 全语种共有但 0 引用的叶子键 3608 个 ⇒ ★ 重要次序：**先清死键再修字表**）；
> **字表完整正名单**（185 候选，`BIG5_TRAPS` 现有 5 字）；**移动端 8 语言**；**后端下发的中文**。
> ⇒ 审计档 `docs/audit/2026-10-27-第11增量-清晰文案门.md`；
> 通用式见 `LESSONS.md` **§418**（第 1 轮 9 条：判据区分内容与位置 / 提取器对「代码里长得像文案的东西」失明 /
> 恢复词表吞掉失败主体 / 赋值目标逐段判定且不锚定行首 / 键是唯一事实源 / 中文段数是代理指标 /
> 服从仓内明文决定 / 改文案触发「同一句话不许两份」 / 门自己也要迭代）
> 与 **§419**（第 2 轮 5 条：★ 用第一个闭合标签配对会把文件后半段变成盲区 /
> ★ 「0 命中」有两种读法 / ★ 变异臂三臂全绿时先问靶点再问门 /
> ★ 提取器的「区间」是一等公民 / ★ 「门归零」后立刻做变异臂）。
> **2026-10-27 第 12 增量：语言包侧清晰文案门轮**（会话 `mvs_dc5e181989fc4539805f343f39be2b5d`）：
> ★ 承接第 11 增量（**源码侧**文案质量门）⇒ 本轮把 impeccable `clarify.md` 的两条判据
> （**别用手势** / **空状态要能区分**）扩展到**语言包侧**（8 语种）并同步改写文案。
> ⇒ ★ **新门 `web/src/i18n/copyClarityLocale.ts` + `web/scripts/copy-clarity-locale-guard.mjs`**
> （已注册 `copy-clarity-locale:check` / `:selftest` / `:list`）。
> ⇒ ★ **语言包侧的位置信号 = 键路径**（不是内容）：空状态位 = 末段以
> `empty|noData|noRecord|noResult|noItems|nothing` **开头**；术语位 `common.terms.*` **不是**空状态位
> ⇒ **零豁免**（位置规则，不是白名单）。
> ⇒ ★★ **「点名具体目标」只认引号内容 / 模板插值 / 箭头** ⇒ **裸拉丁词不算**
> ⇒ 否则拉丁语种天然不判红（实测 en-US 仅 3 处且 2 处假阳性、fr-FR **0 处**）⇒ **规则在语种间不一致 ⇒ 门就不是门**。
> ⇒ ★ **L1 错误三问在语言包侧不设硬闸**（键路径不区分「错误陈述」与「计数摘要」）⇒ **列人工清单**。
> ⇒ ★★ **本轮最值钱的一处：两次差点删掉用户看得见的键，两次都是「量具指错对象」造成的**：
> ★★ **事故一**：分类脚本用**字面量 grep** 判「0 引用」⇒ 源码里根本没有
> `providers.credential.empty` 这个完整字符串（它长成 `const pm = (k) => t(\`providers.${k}\`)`
> + `pm('credential.empty')` ⇒ **grep 必然 0 命中** ⇒ **活键被判死**；
> ★★ 是 **`i18n-audit` 事后报出「2 missing i18n key(s)」** 才暴露的 ⇒ **那道门救了这轮**；
> ★★★ 而**仓内的两道门本来就是对的**（`src/i18n/scanner.ts` 的 `scanAll()` 与
> `scripts/i18n-dead-key-guard.mjs` 的 `collectStaticRefKeys()` **都能还原 `pm()` 形态**）
> ⇒ ★★ **错的不是仓内的门，是临时写的分类脚本** ⇒ ⇒ **判死必须用与事实源同源的量具，
> 临时 grep 只能找线索、不能下判决**；★ **删活键不可逆 ⇒ 判决前必须三口径对拍**
> （scanner / 仓内死键门 / 全仓字面量）⇒ 本轮 33 个死键三口径一致判死。
> ★★ **事故二**：第一版删除脚本的校验基准取自**门报告** ⇒ ★★★ **某个键在某个语种本来就没违规
> ⇒ 门对它没有读数 ⇒ 期望值 `undefined` ⇒ 校验分支整个空转**（`expectVal !== undefined` 守卫）
> ⇒ 再叠加「**中间段正则先命中更早的同名块**」⇒ **误删 7 个活键**
> （`providers.list.empty` / `sessions.list.emptyHint` × 4 语种）。
> ⇒ ⇒ **通用式**：★ 校验基准必须来自**被改对象自身**（loader 实测值），不是另一份报告；
> ★ **定位必须做结构化校验**（本轮正解：**brace 深度扫描**算每行完整键路径 ⇒ 「路径逐段等于目标键」
> 且「值在文件里唯一」才允许删 ⇒ 删除时**正确跳过了 3 处同名不同路径的误命中**）；
> ★★★ **发现已落盘的损害 ⇒ 第一动作是「回滚 + 确定性重放」，不是手工插回**
> （`git checkout HEAD -- web/src/locales/` ⇒ 重放改写 ⇒ 用带路径校验的脚本重删）。
> ⇒ ★★ **变异臂发现的第二个盲区**：第 3 臂靶点 `sessions.list.emptyHint` **没被检出**
> ⇒ 真因是**空状态位的「末段带后缀」族**（`emptyHint`/`emptyTitle`/`noResults`/`noDataText`
> **同样是空状态位却因带后缀整族落在门外**，实测 **47 个键**）⇒ 末段判据由「整段相等」改为
> 「**以空状态词开头**」⇒ 报出 **23 处新违规（全部合法、零假阳性）** ⇒ 2 个活键 8 语种改写 +
> 3 个死键 8 语种删除 ⇒ ★ **判据扩面后必须立刻加牙**（自证 26 → **29**）。
> ⇒ ★ **语言包**：改写 **278** 个值（zh-CN 34 / zh-TW 29 / ja-JP 39 / en-US 37 / de-DE 37 /
> es-ES 28 / fr-FR 41 / ar-SA 33）＋ 删除 **33** 个 0 引用死键 × 8 语种 = **264 行**
> ⇒ **键数 9480 → 9447（8 语种完全一致、added = 0）**；占位符逐键复核 **75576 对失配 0**。
> ⇒ ★ **读数**：**15 个门全 PASS**；自证 **29/29**（语言包侧）+ 19/19（源码侧）+ 30/30（copy-quality）；
> `vue-tsc` **0 错**；`vitest` **2815 通过 / 2 跳过**；`build` ✓ 19.32s；
> **真仓变异臂 3 臂全检出**（rc=1）＋ 还原 rc=0 ＋ **md5 逐字一致**。
> ⇒ ★ **棘轮**：`localeValueBaseline.json` **只降不升**（untranslated 8 语种降 3~4；
> sameAsSource ja-JP 606→604 / zh-TW 1480→1478）⇒ ★ 本轮**未调高任何指标**；
> ★ `baselineRaiseReason` 追加本轮理由（★ 并记录了一次「临时判据误判活键」的自愈）；
> `i18n-cjk-count` 4510 未变（源侧未动）；`i18n-key-parity-count` ★ **中途一度 FAIL 2 项，
> 正是那 2 个被误删的活键造成的** ⇒ **该门同样救了这一轮**。
> ⇒ ★ **本轮明确没做（留给下一轮，别重复排查）**：**L1 错误三问（语言包侧）**（381 候选已定性大半，
> 列人工清单，下一轮设硬闸）；**通用槽拆键**（`common.feedback.noData` 4 处 /
> `dashboard.board.empty` 11 处 / `hyper.list.empty` 12 处 = 27 个调用点）；
> **「源码抄语言包」族**（632 处 / 107 文件，族未治）；**`t(k)` 形态的键仍是引用集盲区**
> （1549 处非字面量 `t(...)`；★ 本轮实测**命名空间助手形态仓内扫描器能还原**，临时 grep 不能）；
> **页面级硬编码中文**（4510 段）；**语言包死键清理**（ja-JP 294 / zh-TW 288；
> 全语种共有但 0 引用的叶子键 **3585** 个 ⇒ ★ 重要次序：**先清死键再修字表**）；
> **字表完整正名单**（185 候选，`BIG5_TRAPS` 现有 5 字）；**移动端 8 语言**；**后端下发的中文**。
> ⇒ 审计档 `docs/audit/2026-10-27-第12增量-语言包侧清晰文案门.md`（10 节）；
> 通用式见 `LESSONS.md` **§421**（9 条：★ 判死键的量具必须与事实源同源 /
> ★ 「拿不到期望值」会让安全阀整个空转 / ★ 定位必须做结构化校验 /
> ★ 发现已落盘的损害 ⇒ 回滚 + 确定性重放 / ★ 量具自己也要被量 /
> ★ 语言包侧的位置信号 = 键路径 / ★ 「点名具体目标」必须跨语种统一 /
> ★ 语种词表必须带词边界 / ★ 判据扩面后必须立刻做变异臂）。
> **2026-10-27 第 13 增量：语言包死键清理轮**（会话 `mvs_dc5e181989fc4539805f343f39be2b5d`）：
> ★ 承接第 12 增量留下的次序「**先清死键再修字表**」⇒ 本轮把 8 个语言包里
> 「**全仓 0 引用**」的键清干净（8 语种各 9468 → **6153**，共删 **3315 键 × 8 语种 = 26520 行**），
> 并把这一族从「只报数」升级为**只降不升的受门管棘轮指标**。
> ⇒ ★ **新门 `web/scripts/i18n-shared-dead-count.mjs` + `web/scripts/i18n-shared-dead-baseline.json`**
> （已注册 `i18n:shared-dead:check` / `:update-baseline` / `:selftest`；自证 **12/12**）
> ⇒ ★ **口径复用**：死键口径从门内联提取为**导出函数** `computeDeadKeyFacts(loadLocale)`
> （`web/scripts/i18n-dead-key-guard.mjs`）⇒ **死键门与棘轮门同一份实现、读数天然可对拍**。
> ⇒ ★ **新工具 `web/scripts/i18n-dead-key-prune.mjs`**（安全批量删除器，内置**三道闸**）。
> ⇒ ★★★ **本轮最值钱的发现：「0 引用」这个前提在本仓是**假的** ——
> ★★★ **仓内实际有 6 种取键形态，而死键守门只认 1 种** ⇒ 首轮实测**误删 63 个活键**
> ★★★ （是 `vue-tsc` 报 7 个错抓到的，**不是任何一道 i18n 门**）：
> ★★★ ① `t('a.b')`（唯一被认的）；② `te/tm` 与**大写别名 `TE('a.b')`**；
> ★★★ ③ **`localeArray<string[]>('a.b')`（带泛型参数）**；④ **`t(c ? 'a.b' : 'c.d')`（三元）**；
> ★★★ ⑤ **`t(变量)`（运行时字符串，如 `errorText = (e) => e.startsWith('clientConfigDialog.error.') ? t(e) : e`）**；
> ★★★ ⑥ **`err.startsWith('a.b.')`（错误前缀判定）**。
> ★★★ ⇒ 修法**只往保守方向**：静态引用集纳入**所有「点分键路径形态」的字符串字面量**、
> ★★★ 动态前缀纳入**以 `.` 结尾的点分字面量**、引用扫描**纳入测试文件**
> ★★★ ⇒ 守门读数 3582 → **3315** ⇒ **267 个活键被保护**。
> ⇒ ★★ **删除器（`i18n-dead-key-prune.mjs`）踩过 14 个缺陷**，每一个都写在文件头注释里
> ⇒ ★★★ 其中三个是**可复用的类**：
> ★★★ ① **「一行多键」⇒ 行级删除会连带删掉同行的兄弟键** ⇒ 删除必须落在**字符区间**上；
> ★★★ ② **解析器不认的键 ⇒ 父容器被判「真空」⇒ 剪枝把仍含活内容的整块删掉**
> ★★★ （引号键 `'24h':`、数字开头键 `400:`、数字值 `amount: 2900` 三种形态）;
> ★★★ ③ **吃行尾换行的条件错了 ⇒ 下一行被粘上来** ⇒ 必须与「整行是否变空」联动；
> ★★★ ④ **行尾注释会变成孤儿** ⇒ 注释应随它描述的键一起走。
> ⇒ ★★★ **三道闸（把「不可逆删除」的判据变成可证伪的等式）**：
> ★★★ ① **解析器 ↔ 运行时真值对拍**（`--check-parser` + 逐文件前置闸）⇒ **656 文件 0 不一致**；
> ★★★ ② **硬不变量**：存活叶子集 == 原叶子集 − 申报删除集 ⇒ **拦下 96 次「多删未申报的键」**；
> ★★★ ③ **落盘后语法自检** ⇒ 坏文件绝不留在工作区。
> ⇒ ★★ **批次纪律**：每批独立验证；**无进展即停**（本轮曾因判据写反而**空转 16 轮**，
> ★★ 日志却一路显示「验证: ✔ 全绿」）；**棘轮门不进「每批」门列表**（删除进行中计数必然高于最终基线）。
> ⇒ ★★★ **回滚 + 确定性重放，本轮用了三次**（8 语种分叉 / 63 个活键被误删 /
> ★★★ 一次性正则 `',\s{2,}'` 把 `\s` 的换行也吞了 ⇒ 583 个文件排版被压平）。
> ⇒ ★ **读数**：**12 道 i18n 门全 PASS**；自证 死键门 **19/19**（新增 B1~B7 覆盖 6 种取键形态
> ⇒ ★ 含一条「六种形态下的键一律不判死」的**反向自证**）/ 棘轮门 **12/12** / 语言包侧 **29/29** /
> ★ 源码侧 **19/19** / hidden-key **8/8**；`vue-tsc` **0 错**；`vitest` **2815 通过 / 2 跳过**；
> ★ `build` ✓ 10.56s；**8 语种键集合完全一致 = 6153**；**占位符 6153 对零失配**；
> ★ **数组型键 5 个、长度跨语种零变化**；★ **值级 A/B：8 语种 49224 个存活键值对、值变化 0**；
> ★ **HEAD 对拍：被删的 26520 个键**全部落在守门死键集内 ⇒ **违规 0、新增 0**。
> ⇒ ★ **真仓变异臂 2 臂全过**：A（注入一个「共有 + 0 引用 + 无前缀保护」的键 ⇒ **rc=1**）
> ⇒ B（注入一个**落在真实代码 `startsWith('clientConfigDialog.error.')` 保护前缀下**的键 ⇒ **rc=0**）
> ⇒ ★★ **B 臂比 A 臂更重要**：它证明门**分得清**哪些键该红、哪些不该红；两臂还原后 **md5 逐字一致**。
> ⇒ ★ **浏览器 A/B**（vite 5199 + Electron）：zh-CN / en / ja / ar 四语种 × 访客面三页 ⇒
> ★ **0 键路径上屏、0 控制台错误**；★★ **本仓 vue-i18n 设了 `missingWarn: false`**
> ⇒ ★★ **「控制台没报缺键」是空的判据** ⇒ ★★ **正确判据是「渲染文本里有没有键路径」**；
> ★ 登录后页面未人工浏览（无凭据）⇒ 由 **2815 个通过的测试**兜底（其中 5 个定向文件
> ★ **就是在断言渲染文本等于语言包的值** ⇒ 缺键必红）。
> ⇒ ★ **棘轮**：`i18n-shared-dead-baseline.json` count **3582 → 0**（**只降不升**），
> ★ 且 baseline 文件**保留 `history` 变更史**（每一条都写明「为什么降」）；
> ★ ★ 本轮**未调高任何指标**；`localeValueBaseline.json` 未动。
> ⇒ ★ **两处仓内自带的「防空过假绿」下限按新事实下调**（并写明实测值与理由）：
> ★ `localeKeyParity.test.ts` `unionSize > 9000` ⇒ **`> 5000`**（9468 → 6153）；
> ★ `localePlaceholderParity.test.ts` `keysWithPlaceholder ≥ 500` ⇒ **`≥ 400`**（703 → 467）。
> ⇒ ★ **本轮明确没做（留给下一轮，别重复排查）**：**把取键形态采集接进 `src/i18n/scanner.ts`**
> ⇒ ★★ （⇒ `i18n-audit` 也能查形态 ②~⑥ 的缺键；★ 但要先量清「点分字面量」的误报面）；
> ★ **L1 错误三问（语言包侧）**（381 候选已定性大半，列人工清单，下一轮设硬闸）；
> ★ **通用槽拆键**（27 个调用点）；**「源码抄语言包」族**（632 处 / 107 文件）；
> ★ **页面级硬编码中文**（4510 段）；**字表完整正名单**（185 候选，`BIG5_TRAPS` 现有 5 字）；
> ★ **移动端 8 语言**；**后端下发的中文**。
> ⇒ 审计档 `docs/audit/2026-10-27-第13增量-语言包死键清理轮.md`（10 节）；
> 通用式见 `LESSONS.md` **§423**（10 条：★★ 「0 引用」是采集规则的输出而非事实 /
> ★★ 不可逆删除的判据必须能被反向自证 / ★★ 一行多键 ⇒ 删除落在字符区间 /
> ★★ 守门报绿前先问「守门跑了吗」/ ★★ 测试文件也是引用源 /
> ★★ 量具口径比事实源窄 ⇒ 伪绿 / ★★ bash 会把 `$VAR` 后的多字节吞进变量名 /
> ★★ 回滚 + 确定性重放 / ★★ 格式化类正则的 `\s` 会跨行）。
> **2026-10-27 第 14 增量：空状态 5 分轮**（会话 `mvs_dc5e181989fc4539805f343f39be2b5d`）：
> ★ 承接用户的总目标（「优化整个网关的 UI，减少 AI 化、增加人味、把复杂表达做得更人性化」）
> ⇒ 前三轮清的是 **i18n 覆盖**，本轮回到 **文案语义本身**：
> ★★ impeccable `clarify.md` 的硬要求是「**An empty state distinguishes first use,
> no results, filters, permissions, and failure**」⇒ **「区分」是硬要求**。
> ⇒ ★★ **本仓把 4 个通用空态键跨语义共用在 24 处调用点上**（`common.feedback.noData` /
> `dashboard.board.empty` / `hyper.list.empty` / `dashboard.noData`）
> ⇒ 一句「暂时没有内容可显示」覆盖 6 种语义 ⇒ 用户看到「没有数据」时无法判断是
> 「真没有」「被筛掉了」还是「算错了」。
> ⇒ ★ **本轮最值钱的一处发现**：**已有的语言包侧清晰文案门（L3）测的根本不是这件事** ——
> 它判「值整句等于通用空状态短语」，而「暂时没有内容可显示」**不是**通用短语
> ⇒ **L3 判绿** ⇒ ★★ **门是绿的，界面在混同状态** ⇒ ★ **「门绿」只说明「门测的那件事成立」**。
> ⇒ ★ **设计：状态种类进【结构】不进【措辞】** —— 23 个新键的**末段带 kind 标记**
> （`emptyNoResult*` / `emptyFirstUse*` / `emptyFiltered*` / `emptyFailed*`）
> ⇒ 复核时先看结构、再看措辞；否则差异会被下一次改写抹平。
> ⇒ ★★ **kind 只能由「页面已经掌握的事实」决定**：VibeCodingView 的会话列表谓词是
> **`filteredSessions`** ⇒ 空 = 筛选后为空（不是「没有会话」）；
> `SessionStatsSignals` 的 `errors === null` ⇒ 服务端**没返回**错误汇总 = **未知**
> （原文案说成「该时段暂无数据」⇒ **把「不知道」讲成「知道」**）；
> BlobManager 页没有时间筛选器 ⇒ 空态**不写时间范围**（不编造页面不知道的事实）。
> ⇒ ★ **新门 `web/scripts/i18n-empty-state-guard.mjs`**（+ `i18n-empty-state-registry.json` 登记簿）
> —— 七条判据：退役键源码 0 引用 / 退役键 8 语种都不存在 / 登记键 8 语种齐备 /
> **键名带 kind 标记** / 值不是通用空态短语 / **同语种内值互不相同** / 登记键引用数 ≥ 1
> ⇒ ★★ **引用集口径自己定义**（`collectCallSiteRefKeys()`，只认真取键调用）：
> **死键门那个保守引用集会把「注释里提到」与「测试里断言」都算成引用**
> （实测误报 3 处）⇒ 这是**把判据对准被问的那件事**，不是放宽判据。
> ⇒ ★ **本轮明确没做**：**`scanner.ts` 的 6 形态采集**（开工前实测**真缺键 = 0**，
> 而「所有点分字面量」规则在缺键方向产生 **2027 条假阳性** ⇒ **同一条规则在两个方向上
> 的安全方向相反**）；**扫描器剥注释**（幽灵引用 11 处 / 8 文件）；
> **L1 错误三问（语言包侧）**；**通用槽拆键的其余 92 个既有空态位键**（登记簿只管本轮新增）；
> **「源码抄语言包」族**；**页面级硬编码中文**（本轮只清了 1 段空态提示）；
> **字表完整正名单**；**移动端 8 语言**；**后端下发的中文**。
> ⇒ 审计档 `docs/audit/2026-10-27-第14增量-空状态5分轮.md`；
> 通用式见 `LESSONS.md` **§424**（10 条形态：★★★ 门测的东西必须逐字对照要的东西 /
> ★★★ 空状态的差异必须进结构 / ★★ kind 只能由页面掌握的事实决定 /
> ★★ 通用键退役必须带棘轮 / ★★ 登记簿不许变成死键的新家 /
> ★★★ 量具升级前先量收益 + 同一条规则在两个方向上安全方向相反 /
> ★★ 注释里的键名是幽灵引用 / ★★ 局部同名函数会伪装成取键调用 /
> ★★ 自证夹具带缺口 ⇒ 断言变成恒非空 /
> ★★★ 变异臂的「红」也有两种读法：被变异抓到了 / 本来就红）。
> **2026-10-27 第 15 增量：L1 错误三问轮（跨 `t()` 边界）**（会话 `mvs_dc5e181989fc4539805f343f39be2b5d`）：
> ★ 承接第 12 增量留下的「L1 错误三问（语言包侧）列人工清单」⇒ 本轮把它变成**一道门**。
> ⇒ ★★ impeccable `clarify.md` 的硬要求是
> 「**An actionable error answers: 1. what failed; 2. why, when known and useful;
> 3. how to recover or what alternative remains.**」
> ⇒ ★★ **本轮最值钱的一处发现**：**本仓早就有一道源码侧的门（`copyClarity.ts` 的 R1
> 「错误位置必须给出恢复动作」），但它只判【字面量】** ⇒
> `error.value = t('keys.loadFailed')` 这种**值来自语言包**的写法**它一个字都看不到**
> ⇒ 实测语言包侧 **151 条**错误文案是「加载失败」「保存失败」这种**裸失败短语**
> ⇒ ★★ **R1 门全绿，而界面在混同** ⇒ ★ 「门测的东西（字面量）≠ 要的东西（用户看到的文案）」。
> ⇒ ★★★ **位置信号从【键路径】搬到【源码上下文】**：
> 第 12 增量试过用键路径当位置信号 ⇒「键路径分不出『错误陈述』与『计数摘要』」（死路）；
> 而**源码侧的赋值目标 / 报错调用分得出** ⇒ 本轮把 R1 的四类位置识别器抽成
> **`web/src/i18n/errorPositions.ts`（R1 门与 L1 门共用同一份实现）**。
> ⇒ ★★ **改写 143 个错误陈述位 × 8 语种**（「加载失败」→「<主体>没能加载出来，请重试」等）
> ⇒ **以仓内既有的合规文案为标尺**（`common.feedback.loadFailed` 等 77 条），不自己发明语气
> ⇒ ★ **「主体」从键的命名空间推导且必须点名** ⇒ 直接治了「24 个页面共用一句『加载失败』」。
> ⇒ ★ **新门 `web/scripts/i18n-error-clarity-guard.mjs` + 登记簿
> `web/scripts/i18n-error-clarity-registry.json`**（143 槽位 + 9 豁免，6 条判据），
> 已注册 `i18n:error-clarity:check` / `:selftest` / `:list`。
> ⇒ ★ **读数**：L1 门 PASS（8 语种 × 143 槽位）/ 自证 **20/20** / 变异臂 **5/5** /
> R1 门重构后仍 PASS（自证 19/19）/ 12 道 i18n 门全 PASS /
> `vue-tsc` 0 错 / `vitest` **2825 通过 / 2 跳过** / `build` ✓ 10.39s /
> 8 语种键集合完全一致 = **6172 键** / 占位符 **3269 对零失配** /
> **浏览器实测 8 语种 × 143 键 = 1144 次渲染，键路径上屏 0**。
> ⇒ ★★ **本轮明确没做（留给下一轮，别重复排查）**：
> **★ `e.message` 主路径**（172 处 `error.value = e.message` + 134 处兜底链 ⇒ 用户 95% 看到的是
> 后端原文 / 英文 `Internal Server Error` ⇒ **impeccable 明令「Do not expose internal codes
> as the primary message」** ⇒ 正确修法是**收口成一条错误出口**，172 处改造，独立工程）；
> **`errors.ts` + `useApiError.ts` 死链**（96 键，`resolveApiError` **0 调用** ⇒
> 且**死键门不判它死**（动态前缀保护）⇒「死代码 → 动态前缀 → 保活死键」是门看不见的链）；
> **未登记槽位的跨语种一致性**（L1a 只用 zh-CN 触发）；
> **字表完整正名单**（185 候选）；**页面级硬编码中文**（4509 段）；
> **移动端 8 语言**；**后端下发的中文**。
> ⇒ 审计档 `docs/audit/2026-10-27-第15增量-L1错误三问轮.md`；
> 通用式见 `LESSONS.md` **§427**（10 条形态：★★★ 位置信号要选在分得出那两类东西的地方 /
> ★★★ 「门只判字面量」⇒ 跨调用边界的值完全没被看过 / ★★★ 词表判据在不是该类事物
> 的值上照样命中 / ★★★「主体里的动词」不是「恢复动作」/ ★★★ 落笔前先量「我以为的
> 主战场是不是主战场」/ ★★ 以仓内既有的合规文案为标尺 / ★★ 门与门共用口径 /
> ★★ 自证夹具必须逐语种 / ★★ 自证证判据对不对、变异臂证门有没有牙）。
> **2026-10-27 第 16 增量：错误出口轮（单一出口 `surfaceError`）**（会话 `mvs_dc5e181989fc4539805f343f39be2b5d`）：
> ★ 承接第 15 增量留下的独立工程「**`e.message` 主路径**」⇒ 本轮把「用户可见错误文案」
> 收成**唯一出口**。
> ⇒ ★★★ **本轮最值钱的一处发现**：**三元分支的顺序决定「谁上屏」** ——
> 全仓的病根是 `error.value = e instanceof Error ? e.message : t('…')`，
> **`.message` 在第一分支** ⇒ 兜底只在「抛出的值**不是** `Error` 实例」时才触发
> ⇒ ★★ **第 15 增量改写的 143 个语言包键，在这条主路径上从来没上过屏**。
> ⇒ ★★ **后端实测（admin 侧）**：`writeError(...)` 硬编码错误文案 **1852 处**，
> 去重 **631 条**，其中**仅 33 条中文**（98% 英文）⇒ 用户看到的是英文技术串。
> ⇒ ★★ **新出口 `web/src/i18n/errorSurface.ts` 的 `surfaceError(err, { key, withReason })`**：
> **主文案 = 主体专属键**（点名主体 + 恢复动作）｜**原始 detail 只作次要原因**
> （`主文案（原因：xxx）`）｜无主体键时落**状态层** `errors.byStatus.*` / `errors.network` /
> `errors.unknown`（这 12 个键原是**死键**：`useApiError.ts` 的 `resolveApiError` 生产代码
> **0 调用** ⇒ 本轮把它们**复活**成兜底层；`useApiError.ts` 本轮**保持死链不动**）。
> ⇒ ★★ **迁移**：全量三元 **316 处 / 112 文件**（T1 `t()` 键 141 / T2 字面量 24 / T4 其它 151）
> ⇒ 全部改走出口；T2 的 24 处字面量转成 **13 个新键**（`common.feedback.*`）。
> ⇒ ★★ **新门 `web/scripts/i18n-error-outlet-guard.mjs` + 登记簿
> `web/scripts/i18n-error-outlet-registry.json`**（4 条判据：O1 裸用原始 message /
> O2 出口键 8 语种齐备 / O3 状态层含恢复动作且非裸失败短语 / O4 原因后缀保留 `{reason}`），
> 已注册 `i18n:error-outlet:check` / `:selftest` / `:list`。
> ⇒ ★★ **`errorPositions.ts` 增认 `surfaceError(..., { key })`** ⇒ L1 门候选 **381 → 410**
> （**扩大**不是缩小）⇒ 否则错误位上不再有 `t()` ⇒ **L1 门会变绿而什么都不看**。
> ⇒ ★ **两处工具修法**：`i18n-error-clarity-guard.mjs` 加 **main-guard**（可被 import ⇒
> O3 复用 L1 门的**同一把尺子**，同一口径只一份实现）；
> `locale-key-writer.mjs` 的 `findProp` 增认 **NumericLiteral** ⇒ `errors.byStatus.400`
> 这类**数字键**被**替换**而不是**追加重复键**。
> ⇒ ★★★ **本轮第二值钱的一处**：**迁移器白名单漏了 11 处命名空间助手**
> ⇒ 被**静默降级**成无键的 `surfaceError(e)` ⇒ 主体键丢失 ⇒ 变成**共有死键**
> ⇒ **被死键棘轮抓住**（0 → 4）⇒ 回填后归零 ⇒ ★ **「批量改造里『没匹配上』必须响亮地失败」**。
> ⇒ ★ **语言包**：8 语种 × **58 值**（32 改写 + 13 新键 + `common.feedback.reasonSuffix`
> + `errors.byStatus.*`/network/unknown 12）+ 追加 10 键 × 8 语种；
> **撞值 4 条**按 L1e「不许两份」修掉（主体点得更具体 ⇒ 重新翻译 8 语种）。
> ⇒ ★ **登记簿**：L1 槽位 **143 → 198**；出口门豁免 5 条 + 文件级豁免 4 条
> （`SelfCheckPanel.vue` 服从仓内明文纪律「本文件全文件不用 i18n」）。
> ⇒ ★ **读数**：**12 道 i18n 门全 PASS** / 出口门自证 **13/13** /
> ★ **真仓变异臂 5/5**（4 条判据各 1 臂 + **1 条负控**「无关改动 ⇒ 仍绿」）/
> ★ **新增契约测试 `web/src/i18n/errorSurface.test.ts` 11/11**（8 语种逐个断言）+
> ★ **其变异臂 3/3** / `vue-tsc` **0 错** / `vitest` **2836 通过 / 2 跳过**（第 15 增量为 2825；本轮**新增契约测试 11 例**）
> （与第 15 增量**逐字相同** ⇒ 用例数零变化）/ `build` ✓ 14.90s /
> 8 语种键集合完全一致 = **6186 键** / 占位符 **6186 对零失配** /
> 硬编码中文 **4510 → 4484** / 共有死键 **0** /
> **浏览器访客面**：落地页 / 登录弹窗 / `/bootstrap` ⇒ **0 键路径上屏、0 控制台错误**。
> ⇒ ★★ **本轮明确没做（留给下一轮，别重复排查）**：
> ★★ **后端下发文案本地化 / code 化**（admin 1852 处英文硬编码 ⇒ 前端只能把原始 detail
> 当**次要原因**；正解是后端给**机器可读 code**）；
> ★★ **`t(k)` 形态的键仍是引用集盲区**（1549 处非字面量 `t(...)`）；
> ★★ **未登记槽位的跨语种一致性**（L1a 只用 zh-CN 触发）；
> **通用槽拆键的其余既有空态位键**；**「源码抄语言包」族**（632 处 / 107 文件）；
> **页面级硬编码中文**（4484 段，含访客页 en 语种里混进的「完整协议 ↗」）；
> **语言包死键**（ja-JP 294 / zh-TW 288）；**字表完整正名单**（185 候选）；
> **移动端 8 语言**；**远端运维菜单的中文**；**`SPECIFIED_MODEL_DISPLAY_LABEL`**；
> **`scanner.ts` 的 6 形态采集**。
> ⇒ ★ **本轮未验证（如实记）**：登录后页面**未人工浏览**（无凭据，用户选择跳过接管）
> ⇒ 由 2836 个通过的测试兜底（其中 `errorSurface.test.ts` 就是在断言渲染文本等于语言包的值）。
> ⇒ ★ **本轮发现但未改（既有缺陷）**：`LanguageSelector.vue` 的
> `:aria-label="'Switch language'"` 是**硬编码英文**（同文件的导航 aria-label 已用 `t(...)`）。
> ⇒ 审计档 `docs/audit/2026-10-27-第16增量-错误出口轮.md`；
> 通用式见 `LESSONS.md` **§429**（12 条形态：★★★ 三元分支的顺序决定「谁上屏」/
> ★★★「上一轮修好了」≠「上屏了」/ ★★★ 无参 `t()` 会把 `{name}` 占位符吃掉 /
> ★★★ 噪声过滤必须精确到「这一条错误自己的那个」/ ★★★ 批量迁移里「没匹配上」必须响亮地失败 /
> ★★★ 阴性结论也要当缺陷一样复核 / ★★「门绿」的第三种形态是「覆盖面没了」/
> ★★ 同一把尺子只能有一份实现 / ★★ 数字键会让 AST 写入器静默写出重复键 /
> ★★「我以为的数字」必须当场重测 / ★★「疑似产品缺陷」要用能控住变量的量具复核 /
> ★★ 文案结构变了断言的结构也要跟着变 / ★★「内部码该不该上屏」本身是个产品判断）。
> **2026-10-26 多模态标准接口轮：本地音频（ASR/TTS）+ 文本三件套**（goal 会话，
> 承接 network-switch 仓《嵌入与会话总结优化方案-2026-10-26》）：
> ★ 目标：完善 llm.itestu.cn —— 接入本地模型，提供**嵌入/标题总结/内容总结/ASR/语音发音**
> 标准接口。全部落地并 e2e（详见审计档 §4 时刻表）。
> ⇒ ★ **新端点三件套** `domains/streaming/summarize.go`：`/v1/summarize`（短单发/长
> map-reduce-render）、`/v1/summarize/title`（规则硬校验+实体择优）、`/v1/classify`
> （受控词表）——环回 chat 面架构（与 audio_transform 同款），鉴权抽成包级
> `authenticateNoRPM`、环回抽成 `loopbackChatCompletions`（transform 改委托，
> 同一把尺子一份实现）；提示词移植自已验收的三个 SKILL.md；`<think>` 剥离；
> 环回 429 单次重试；map 层 maxTokens=3000（thinking 模型预算不被烧穿）。
> ⇒ ★ **whisper.cpp 传输** `audio_whisper_stt.go`：whisper-server 1.9.5 **没有**
> OpenAI 兼容路径（实测 404），钉 `/inference` 方言，判据 **catalog_code**（绝不按
> 模型名猜——云端也有 whisper-* 模型名）；base_url 注册约定**不带 /v1**。
> ⇒ ★ **catalog 迁移 911**（原取 910，未及推送被上游 R166 的 910_routing_analytics 占号 ⇒ 按「已落 origin 者保留原号」纪律让位重取；登记线由并行会话 merge 恢复）：whisper-cpp(:8081)/whisper-cpp-large(:8082)/macos-say(:8086)
> 三条 kind='local' 条目 + manifest 模型清单（whisper 无 /v1/models ⇒ API 发现失败
> 自动回落 manifest）；**7+1 点登记全齐**（正本/embeddata/installer×2/tsv/升级序列/
> 基线/db-changelog）。
> ⇒ ★★ **修复三个潜伏缺陷**（都被本地供应商注册踩出）：①启动播种把占位 key
> **明文**写进 secret_ciphertext（播种已改走真加密 + admin decryptCred 对已知占位
> 常量精确直通）；②同一明文在**运行时路由面**把候选整个丢掉（llamacpp/lmstudio/
> mlx/vllm 四家「从未真正可路由」的根因；常量提升到 provider/catalog，两缝同修）；
> ③**探针不认 thinking 模型**：max_tokens=32 探针载荷被 reasoning 烧穿 ⇒ content 空
> ⇒ inconclusive ⇒ node_probe_failed 活锁——chat 臂加认 reasoning/reasoning_content
> （「探针问的是活着吗，r1029 管的是能不能交付」，业务面一字不动；负例夹具按新契约
> 移为 chat 正例，理由在注释）。
> ⇒ ★ **Mac 侧**：whisper 双档 plist 0.0.0.0 + TTS 垫片 `~/bin/modelhub-tts-server.py`
> （纯标准库，say+afconvert，OpenAI /v1/audio/speech 形态，wav/m4a，mp3 如实 400，
> launchd :8086）。
> ⇒ ★★ **部署坑（重要）**：**多 checkout 机器上 DD_REPO_ROOT 会漂**——第 5 次部署
> 同步到 211 的竟是上游 abc92693 的树，镜像静默丢掉本轮全部端点（/v1/summarize 404
> 而非 400）⇒ **部署必须 `LLM_GATEWAY_ROOT=<绝对路径>` 钉根**；另 deploy-211.sh
> 无参路径的 bash3.2+set -u ARGS[*] unbound 已补守卫。
> ⇒ ★ **e2e 读数**（llm.itestu.cn，8 次蓝绿部署后）：embeddings **4096 维** ✓、
> ASR 双档转写正确 ✓、TTS wav ✓、title/classify/短总结多次通过（质量达 SKILL 规格）
> ✓；**长路径 map-reduce 端到端打通（10-27 03:04：5207 字 → 91s 完整纪要成品）**。
> 打通前挖掉两个真根因：★★ **ollama 对非 thinking 模型收到 reasoning_effort 直接
> 400**（"does not support thinking"）⇒ 修在 **reqprobe**（thinking 能力族判据 →
> 剥参免费重试，全调用方受益；summarize 层另加同判据兜底）——★ 教训：**chat 面把
> 上游 400 吞成 503**，上游细节只在 upstream_http_attempt 日志里；★ **nginx 300s
> proxy_read_timeout 掐断长渲染**（且掐断的在途请求会打脏渲染模型的探针状态，
> 下一轮假象 503）⇒ 211 nginx llm 网关 location 已提 660s。
> ⇒ ★★ **Mac 侧遗留（需用户处置）**：Ollama.app 与 launchd 双实例之争复发（坑#2），
> MAX_LOADED_MODELS=5 的 plist 补丁未落到实际在跑的实例 ⇒ 模型 LRU 驱逐与探针
> 超时的活锁会间歇复发；退 app + 重启 Mac 一次即根治。embedding:8b@32K 占 10GB，
> 与双 chat 大模型共驻内存吃紧。
> ⇒ ★ **asm 在线优先委托 + 批判审计轮（2026-10-27 续）**：长文本总结在
> ai-session-manager 在线时委托其 `POST /api/v1/summarize` 智能体端点（asm 侧
> 同轮新增，grounded 纪要；其 LLM 上游 env 指向本地模型），离线/失败响亮降级
> 回网关管线——响应 `engine` 字段区分 asm|gateway；211 实测三态闭环
> （在线 22s=asm / 停机 78s=gateway / 恢复 22s=asm）。
> ⇒ ★★★ **审计轮抓到并修掉的回归**：探针 liveness 修复曾通过共享判据
> `credentialProbeHasOutput` 越界改变 node/model/active 探针语义（bg 4 红，
> 上一轮「探针测试全绿」实为 `-run` 子集 + tail -2 吞输出的**无实证声明**）⇒
> 语义分层：`credentialProbeHasOutput` 恢复 deliverability（reasoning 一律不算
> 输出），新增 `credentialProbeHasLivenessEvidence` **只许 credential 探针**
> 叠加；教训 = **改共享判据先枚举全部消费方 + `-run` 子集绿 ≠ 包绿**。
> ⇒ ★ 迁移号订正：910 被上游 R166 占号，按让位纪律由并行 merge 重取 **911**
> 完整恢复登记（五点复核 + 内容逐字等价 + 契约门绿）。
> ⇒ 审计档 `docs/audit/2026-10-26-多模态标准接口轮-本地音频与文本三件套.md`
> （坑清单 §5 共 8 条 + §7 审计轮补记：语义越界/撞号让位/死代码清理）。
> **2026-10-27 第 17 增量：AI 痕迹门轮（impeccable 的 Do-Not 落成可执行判据）**（会话
> `mvs_dc5e181989fc4539805f343f39be2b5d`）：★ 承接用户总目标（「学习 impeccable 的技能与规范，
> 优化整个网关的 UI —— 减少 AI 化、增加人味与艺术化」）⇒ **前 16 增量清的是 i18n 覆盖与文案语义，
> 视觉层只被一次性清理过**（`DESIGN.md` 2026-10-24 的问题清单**没有门守护**）⇒ 本轮把
> impeccable `skill/reference/craft-floor.md` 的 Refuse/codex 段**逐条落成可执行判据**。
> ⇒ ★ **新门** `web/src/design/aiSmells.ts`（判据唯一实现，9 条 V1–V9）+
> `web/scripts/design-ai-smell-guard.mjs`（薄壳，CLI `--json`/`--list`/`--selftest`）+
> `web/scripts/design-ai-smell-registry.json`（豁免登记簿 5 条，每条写明理由）；
> 已注册 `design-ai-smell:check` / `:selftest` / `:list`。
> ⇒ ★★ **本轮最值钱的一处发现**：**门报 0 之后去看真实页面，页面上还挂着 `☾` 字形**
> （主题切换按钮）⇒ 顺着它挖下去 ⇒ 判据只认**字面量形态**，**插值形态**整族漏判 ⇒ **59 处**
> ⇒ ★★★ **「门绿」与「界面已改」之间隔着一次外部观测**（这正是 §419「阴性结论要当缺陷一样复核」
> 在视觉层的形态）。★ 同一个字形有**三种实现形态**：纯文本 / 插值字面量 / **数据变量**
> （`ExamplesView` 的 4 个客户端 emoji、`i18n.ts` 的 8 个语种国旗）⇒ 第三种**静态不可知**，
> 门原理上抓不到 ⇒ 显式记为盲区并写明实测数量，不许藏在「门是绿的」后面。
> ⇒ ★★ **判据自身的 8 个坑**（都是自证/真仓抓到的）：`border-radius` 被当描边、`color-mix` 的嵌套逗号、
> 零无 `px` 的偏移、`isColoredValue` 不认 `color-mix`、**V3 只认单行 `class="…"`**、
> **V3 只认 icon/glyph/symbol/emoji 四个词**、**V3 排除插值**、**门报的行号指向开标签而非字符**。
> ⇒ ★★ **位置信号比字形本身更决定判红**：同一个 `—` 在 `text-muted` 里是正文占位、在 `btn-icon` 里是图标位；
> 同一个 `→` 在 `gw__arrow` 里是流程连线、在 `<button>` 里是返回按钮 ⇒ 判据分【位置】【形状】两层
> +「流程箭头在非按钮位不判」的例外条款。
> ⇒ ★★ **清零 137 处 / 62 文件**（V1 幽灵卡片 28 / V2 彩色侧边条 5 / **V3 字形当图标 109** /
> V4 · 1 / V5 · 2 / V9 · 6）⇒ 字形全部换成 `@element-plus/icons-vue` SVG
> ⇒ ★★ **裸 `<svg viewBox>` 不带 width/height ⇒ 浏览器按包含块宽度渲染 ⇒ 每处必须显式定尺寸**
> （沿用原 `font-size` 观感）⇒ 另加第三道校验（CSS 规则里的类名必须存在且规则必须落盘 ⇒ 死规则 = 布局会炸）。
> ⇒ ★★ **连带修的两处「量具口径」**：① 硬编码中文棘轮被顶到 4617 ⇒ 根因是判据文件里的中文在
> **字符串字面量**里（自证 desc / `reason`）⇒ 属「量具量错了对象」（判据说明永不上屏）⇒ 与 `src/i18n/`
> 同惯例把 `design` 加入排除目录 ⇒ **4617 → 4484 = 原基线（棘轮未升）**；
> ② 未决占位符登记册按 `(file, line)` 记账 ⇒ 本轮加一行 import 就让调用点 193 → 194 ⇒ 同步行号。
> ⇒ ★ **读数**：门 **PASS（0 违规）** / 自证 **45/45** / 真仓变异臂 **12/12**（9 条判据各 1 臂 +
> V2 伪装形态 + V3 两种形态 + **1 负控**；每臂「注入→rc=1→还原→rc=0→md5 逐字一致」）/
> `vue-tsc` **0 错** / `vitest` **2836 通过 / 2 跳过** / `build` ✓ 11.40s /
> 8 语种键集合完全一致 = **6190 键**（+4：`requestDetail.attachments.kindLabel.*`）/
> 占位符 **6190 对零失配** / 硬编码中文 **4484 = 基线** / 共有死键 **0**。
> ⇒ ★ **浏览器读数（公开面）**：登录弹窗的 `<Close />` 实测 **14×14** ⇒ SVG 尺寸规则生效、没撑爆布局。
> ⇒ ★ **本轮未验证（如实记）**：登录后页面**未人工浏览**（无凭据，用户选择跳过接管）
> ⇒ 由 2836 个通过的测试 + CSS 规则存在性静态校验兜底。
> ⇒ ★★ **本轮明确没做（留给下一轮，别重复排查）**：
> ★★ **V3 第三形态（数据里的字形）**（8 个语种国旗 + 4 个客户端 emoji ⇒ 需要「跨文件数据流」判据）；
> ★★ **令牌体系迁移**（旧词表 5175 处 / 235 文件 → `--kx-*`）+ **阴影词汇归一**（74 种取值 → `--kx-shadow-sm/md`，
> ⇒ 同时要回写 `DESIGN.md`，它自己要求「冲突时要回来改本文件」）；
> ★★ **「源码抄语言包」族**（632 处 / 107 文件）；**页面级硬编码中文**（4484 段，含访客页 en 语种里混进的
> 「完整协议 ↗」、`TenantDetailView` MaaS 快捷入口 5 处硬编码中文标签）；
> **后端下发的中文**（admin 1852 处）；**移动端 8 语言**；**扫描器剥注释**（幽灵引用 11 处 / 8 文件）；
> **字表完整正名单**（185 候选）；**语言包死键**（ja-JP 294 / zh-TW 288）。
> ⇒ 审计档 `docs/audit/2026-10-27-第17增量-AI痕迹门轮.md`；
> 通用式见 `LESSONS.md` **§431**（11 条形态：★★★「按形状判」还必须覆盖同一形状的【所有实现形态】/
> ★★★ 门报的行号必须指向【问题字符本身】/ ★★★ 浏览器实测是「阴性结论当缺陷一样复核」最好的触发器/
> ★★★ 位置信号比字形本身更决定判红 / ★★ 批量替换必须两阶段 / ★★ 裸 `<svg viewBox>` 没有 width/height /
> ★★ 排除目录是「量具口径变更」/ ★★ 登记册按行号记账会漂移 / ★★ 断言字形文本要改成更强的断言）。
> **2026-10-27 第 18 增量：浮起词汇归一轮（阴影从「手写配方」收敛成「四支词表」）**（会话
> `mvs_dc5e181989fc4539805f343f39be2b5d`）：★ 承接第 17 增量遗留清单里的
> 「**阴影词汇归一**（⇒ 同时要回写 `DESIGN.md`，它自己要求「冲突时要回来改本文件」）」，
> 把视觉治理从**文本层**推进到**几何层** ⇒ ★★ 普查实测：**121 处 `box-shadow` 声明 /
> 71 种取值 / 66 个文件** ⇒ ★★★ **同一个概念有 71 种写法，本身就是 AI 生成代码的典型痕迹**
> （每次生成重新发明一遍配方）。
> ⇒ ★ **新门** `web/src/design/elevation.ts`（判据唯一实现 E1）+ `web/scripts/design-elevation-guard.mjs`
> （薄壳，CLI `--json`/`--list`/`--selftest`/`--update-baseline`）+
> `web/scripts/design-elevation-baseline.json`（棘轮登记簿，**只降不升**）；
> 已注册 `design-elevation:check` / `:selftest` / `:list` / `:update-baseline`。
> ⇒ ★★ **四支 token**（唯一定义处 `web/src/style.css`）：
> `--kx-shadow-xs`（贴面 · 选中态标签）/ `--kx-shadow-sm`（浮起 · 弹层下拉）/
> `--kx-shadow-md`（覆盖 · 模态对话框）/ `--kx-shadow-md-left`（侧向覆盖 · 右侧抽屉）。
> ⇒ ★★★ **三条设计决定**：① **几何沿用仓内既有主流值**，不发明数值（视觉差异接近零，收益全在工程一致性）；
> ② **色相统一为蓝调黑**（纯黑阴影本身是 AI 痕迹）；③ **alpha 只分两档**
> （浮起 `.06`/`.28`、覆盖 `.10`/`.40`）—— ★ **级别靠【锐度】而不是【浓度】区分**，
> 加浓会让暗色主题糊成一片、级别就消失了。
> ⇒ ★★ **读数**：**55 处 token 引用**（md 25 / sm 15 / md-left 9 / xs 6）落在 **50 个 `.vue` + `style.css`**；
> **不同取值 34 → 4**；★ **幽灵卡片处置 13 处 / 9 文件**（抬升级删闭合描边 8 处、
> 表面级删静态阴影 5 处）；★ `HotPartitionManager` 的 `box-shadow: -1px 0 0 0 var(--accent)`
> （零模糊 + 有偏移 + 彩色）**整条删除**（依据 `DESIGN.md` §6「禁止 1px 以上的彩色 border-left」
> + craft-floor 禁零模糊硬偏移 + 该元素已有三重状态表达）。
> ⇒ ★★★ **本轮最值钱的一处发现**：★ **第 17 增量的 V1 门在全部 17 处 `var(--kx-shadow-*)`
> 元素上整族假绿** —— 因为 **V1 要判「是不是抬升」就得读【偏移】，而偏移搬进 token 之后，
> 静态判据从 `var(...)` 里取不到** ⇒ ★★ **通用式：★ 「引用」与「实际几何」之间隔着一个【解析步骤】
> ⇒ 判据跳不过去 ⇒ 它就是在【已经合规的元素】上假绿** ⇒ 修法：`ELEVATION_TOKENS` 名单
> （**只放一处**，`aiSmells.ts` 导出、`elevation.ts` re-export）+ 「`--kx-shadow-*` 一律是抬升」。
> ⇒ ★★ **同轮另修三处真盲区**：① **V7 硬偏移判据要求第 2 段必须带 `px`**
> ⇒ `-1px 0 0 0 var(--accent)`（零模糊 + 有偏移）漏判 ⇒ 改为**逐层判 + 长度单位可省略**；
> ② **V7 只取块内第一条 `box-shadow`** ⇒ 同块第二条完全看不见（**真仓变异臂 M3 抓到**）
> ⇒ 抽出共享原语 `boxShadowDeclarations(body)`，**V7 与 elevation.ts 共用**（同一口径只有一份实现）；
> ③ **V1 把「单边描边」当「闭合描边」** ⇒ 4 处抽屉假阳性 ⇒ ★ **单边描边（画在面板内侧）
> + 外侧投影不重叠 ⇒ 那是一条边，不构成幽灵卡片** ⇒ 口径已细化并写进 `DESIGN.md` §5。
> ⇒ ★★★ **判据的分类是第一位的**：★ 同一段 CSS 文本里有**两种语义** —— **抬升层**（有偏移 ⇒ 深度）
> 与 **零偏移环**（`0 0 0 Npx` ⇒ 焦点圈 / 探测标记 ⇒ **不表示深度**）
> ⇒ 实测抬升 69 / 环 33 / `none` 12 / inset 7 ⇒ ★ **分类错了，后面全错**（当抬升判 ⇒ 假红；
> 当环判 ⇒ 假绿）⇒ 门里显式写死并让分母可被读数验证。
> ⇒ ★★★ **工具自身踩过的坑**：独立普查脚本（`/tmp/q18/shadow-census2.mjs`）**自身 2 个 bug**
> —— ① **逐行匹配漏多行声明**（`RequestTile.vue` 的 `.request-tile--probe` 跨 4 行）；
> ② **不剥注释抓出假阳性**（`style.css` 注释里的声明）⇒ 修完后与门**逐条对拍 53/53 一致**
> ⇒ ★★ **两个独立实现必须逐条对拍，这是发现「量具自己坏了」的唯一手段**。
> ⇒ ★★★ **源码合规 ≠ 运行时合规** ⇒ 补了一次**独立运行时核对**，且把工具**固化进仓**（`web/scripts/design-elevation-runtime-check.mjs`
> ⇒ `design-elevation:runtime` / `:runtime:selftest` 11/11；★ 无 Chrome 时打印 ⏭ SKIP「未测量」而不是伪装成通过）
> ⇒ 核对方式：头 Chrome + CDP，
> 遍历**全 DOM** 的计算阴影）：**9 页 × 2 主题 = 18 次扫描 ⇒ 非 token 抬升层 0 处**；
> 定点断言（13 处幽灵卡片处置）**0 条不通过**（`.topo-node` / `.gw` 计算阴影 = `none`；
> `.app-topbar__dropdown` / `.user-menu__dropdown` = `md` 阴影 + 四边 `0px`）；
> 浮层实测 `.journey-modal` = `rgba(0,0,0,.4) -12px 0 32px 0px`（**md-left 侧投影**）。
> ⇒ ★★ **读数**：浮起门 **0 非规范 / 4 取值 / 63 抬升层**（基线 0/4）+ 自证 **15/15**；
> AI 痕迹门 **0 违规** + 自证 **45 → 51/51**；**真仓变异臂 4/4**（M1 非 token 抬升 /
> M2 幽灵卡片 / M3 同块第二条声明 / **M4 负控：零偏移环 ⇒ rc=0**；每臂「注入→rc=1→还原→rc=0→
> md5 逐字一致」）；`vue-tsc` **0 错**；`vitest` **2836 通过 / 2 跳过**；`build` ✓ 40.82s；
> console 错误（9 页合计）**1**（`autoRoute.indexSnapshot` 500，**既有部署状态，与本轮 CSS 无关**）。
> ⇒ ★★ **本轮没做 / 如实记**：① **令牌双轨的 7 个「需处理」变量未迁**
> （`--bg-subtle` 跨主题语义不同：浅=primary-soft / 暗=sidebar；另有 `--bg-hover` / `--bg-secondary` /
> `--bg-tertiary` / `--error-bg` / `--cyan` / `--border-muted` ⇒ 全是 `color-mix` 派生值 ⇒ 机械替换会改语义，
> 需设计判断）；② **运行时只逐个打开了 3 类浮层**（journey-modal / topbar dropdown / user-menu dropdown），
> 另 63 层里多数需特定数据才挂载 ⇒ 运行时扫描是**子集核对**，全量靠源码门；
> ③ **浮起门没有「token 定义本身被改坏」的守卫**；④ **部署实例库只跑了部分迁移**
> （`public` 474 张表，`request_logs` / `prompt_injection_stats_enhanced` / `detection_score` **缺失**
> ⇒ 「请求日志」类页面在部署实例上会报错；迁移脚本在仓里
> `sql/migrations/startup/315_prompt_injection_detection.sql` / `364_prompt_injection_enhanced.sql`
> ⇒ **跑迁移属 DDL 有副作用操作，需用户授权** ⇒ **与本轮 CSS 无关**）。
> ⇒ ★★ **工具侧的一次实战**：★ 内嵌浏览器在**实时流页面**上的 `click` / `reload` / `console`
> 三类动作**持续 275s 超时**（`inspect` / `screenshot` / `open_tab` 正常 ⇒ 是「交互等待」这条路径坏了，
> 不是页面坏了）⇒ ★★ **换通道**（无头 Chrome + CDP）⇒ 同样 0 违规，且额外拿到浮层计算值与 console 清单
> ⇒ ★★★ **通用式：★ 「量具挂起」是【读不到】，不是【读到了 0】⇒ 两者必须分开写**。
> ⇒ ★★ **一次「改动不见了」的实战**：★ 本轮改动在会话之间被存成 **WIP 提交 `470454dd8`
> （分支 `wip/design-elevation`）** ⇒ 工作树「变干净了」⇒ ★★ **只看 `git status`（空）会以为改动没了**
> ⇒ ★★★ **开工先三处一起看**：`git status` / `git stash list` / 分支表；
> ★★ 恢复时**按「文件集合的交集」算冲突面**（57 文件 vs 上游 12 提交 ⇒ 只有 1 个重叠，
> 而那 1 个是**生成物** `menu-config.json` 的 `exported_at` 时间戳 ⇒ 丢弃上游新版）。
> ⇒ 审计档 `docs/audit/2026-10-27-第18增量-浮起词汇归一轮.md`（§0~§10）；
> 通用式见 `LESSONS.md` **§450**（9 条形态：★★★ 分类是第一位的 /
> ★★★★ 判据读 token 引用时必须同时解析 token 的定义 /
> ★★★★ 「有没有 X」在 X 有多种形态时必须先枚举形态 /
> ★★★★ 遍历必须覆盖同一条规则内的【所有】声明 /
> ★★★★ 判据里每个字段都要单独给「可省略」的语义 /
> ★★★★ 分析工具与门必须有同等的「区间 + 注释处理」能力 /
> ★★★★ 源码合规 ≠ 运行时合规（两把尺）/
> ★★★ 工具挂起时先换通道 / ★★★ 开工先确认改动落在哪）。
---
## 历史轮次 R 标记索引（2026-10-31 分档归档）

> ★★★ **为什么有这一节**：★ 2026-10-31 AGENTS.md 涨到 2.5MB（整体注入每次请求 ⇒
> 超 1M token 窗口 ⇒ 本仓所有 agent 请求报 `prompt is too long`）⇒ ⇒ 历史轮次
> 已整体移入仓库根目录 `AGENTS-archive-20261031.md`，**AGENTS.md 只保留维护者交接的
> 当前状态**。★ ⇒ ⇒ 但 **§143.8x~§143.10x 系列的交接门**要判「本轮结论是否落在交接面」
> ⇒ ⇒ 它们读的是 **AGENTS.md** ⇒ ⇒ 所以这里保留一份**逐轮 R 标记索引**：★
> ★ 每行只给「轮次号 + 节号 + 一句话主题」，★ 正文在归档文件里 ⇒ ⇒ ⇒
> ★ **索引是【指针】不是【副本】** ⇒ ⇒ ⇒ AGENTS.md 保持小体积（不进上下文炸弹）
> ★ 而冷启动助手仍然能从本文件查到「哪一轮做了什么」。

> - **R123**（§143.81）【累计 vs 现时】口径分离轮 —— 全文见 `AGENTS-archive-20261031.md`
> - **R124**（§143.82）【版本谱系 + 速率突变】轮 —— 全文见 `AGENTS-archive-20261031.md`
> - **R125**（§143.82-B）【速率突变归因轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R126**（§143.83）【读侧 ⓪ 的机制钉死与收益重算轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R127**（§143.84）【L3 调用方穷举轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R128**（§143.85）【落法 B 的具体设计轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R129**（§143.86）【⓪ 的落地实现轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R130**（§143.87）【库侧漏斗代数轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R132**（§143.88）【「v2 落空 → 读链进入」映射链轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R133**（§143.89）【「走到 L3」服务侧埋点落地轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R134**（§143.90）【跨源求交器落地轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R135 轮**（§143.91）【存储热点审计轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R136 轮**（§143.92）【正文族按列拆解 + 形态分类 + 内容级包含关系】轮 —— 全文见 `AGENTS-archive-20261031.md`
> - **R137 轮**（§143.93）【ursm 快照族存储审计轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R138 轮**（§143.94）【session_turns 族逐列拆解轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R139 轮**（§143.95）【session_turns 族留存归属 + hot 表排水归属轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R140**（§143.96）【会话族 final_success 自愈（copy-gap 修复）落仓轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R141**（§143.97）【会话族 hot 排水：session_bodies_hot 的两条缺口落仓轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R142**（§143.97 收尾）【惰性变异筛查轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R143**（§143.98）【会话正文族 promote 预算不变式轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R144**（§143.99）【会话正文族读侧「长尾放大」定位轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R145**（§143.100）【L3 回退 turns 腿的索引缺口定位轮】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R146**（§143.101/§143.102/§143.103）【存储效率综合轮：P5 索引合并 + 探测型短路落地】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R185**（§601）【nvidia 直通事故轮的批判式审计轮：厂商闸门 + 原厂码对齐 SSOT + 两份记忆同一 TTL + 剥参不误伤断言】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R186**（§602）【兄弟应用单点登录接入轮：跨仓身份票据 + 部署期共享密钥存活 + 免登三态实测 + 客户端识别适配方案】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R187**（§602 追加）【goacos 注册表同步登记的两个真缺陷 + 判据门桩的形状盲区】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R188**（§602 追加）【判据门的「挂起」失败形态：看门狗 + 元判据门（门能红且红得快）】 —— 全文见 `AGENTS-archive-20261031.md`
> - **R189**（§602 追加）【252 SQL 审计轮：951 索引根修 armor_judgments trim 顺扫 + installer 942-951 embed 缺口收口 + maas 回填水位短路】 —— 全文见 `AGENTS-archive-20261031.md`

> ★ **追加新轮次时**：★ 轮次正文追加到 `AGENTS-archive-20261031.md`，★ 同时在上面的
> 索引里加一行（**marker + 节号 + 一句话主题**）⇒ ⇒ ⇒ 交接门才判得到「结论在交接面」。

<!-- /AGENTS-R-MARKER-INDEX -->



> [2026-10-31 归档] 历史审计轮次（2026-10-11 起全部"# 追加"轮次报告）已移至仓库根目录 `AGENTS-archive-20261031.md`。后续轮次报告请追加到该归档文件；AGENTS.md 只保留维护者交接的当前状态。原因：AGENTS.md 会整体注入每次请求的上下文，超窗口导致所有请求失败。
> **2026-11-01 智能体识别与 IP 落库轮的批判式审计轮**（goal 会话）：审计对象＝上一批
> （88e277974）改动**与其文档断言**。落库验证正确，但**缺三样**：一处真缺陷（失败路径丢
> body）、一处真副作用（client_model 空）、**一整层回归门**。
> ⇒ ★★ **修复 1（真缺陷）**：`readRequestBody` 消费 body **不复原**（只有 `bufferRequestBody`
> 复原）⇒ responses/messages 里 `captureAttemptBody` 拿到空 ⇒ **系统提示词兜底恰好在
> 运维最需要的失败行上失效** ⇒ 新增 `readRequestBodyRestore(r, limit)`（读后复原；
> **读失败/超限不复原**——残缺字节流不能当完整 body 重放）⇒ 两处 handler 改用它。
> ⇒ ★★ **修复 2（真副作用）**：`EnsureCaptured`→`ensureRequestBodyBuffered` 在 Body 已非空
> 时提前 return ⇒ **顺带跳过模型提取** ⇒ 上一批的「预填 Body」使 `client_model` 恒空 ⇒
> 两处 handler 显式补 `SetClientModel(extractModelFromBody(bodyBytes))` + `"<unknown>"` 兜底。
> ⇒ ★★★ **修复 3（回归门，本轮最重要）**：新增
> `domains/streaming/identification_nonchat_test.go`——行为层（`BuildContextAttrsEntry`：
> 弱 UA + 系统提示词自报 ⇒ zcode/minimax-code；强 UA pi ⇒强名不被兜底覆盖）+ 接线层
> （**AST 级**断言两个 handler 各调 `SetKey(ki)` 与 `EnsureCaptured()`）。
> ⇒ ★★★★ **第一版接线门用 `strings.Contains` ⇒ 变异验证时仍全绿**（注释里还留着同样
> 字符串）⇒ 改 `go/ast`（注释对 AST 不可见）⇒同一变异红 4 处 ⇒ **断言形状含首参**
> （`SetKey(ki)` 而非 `SetKey`：文件里另有 rate-limit 分支的 `SetKey(keyInfo)`）。
> ⇒ ★★ **订正上一批文档的错误断言**：「turn_no 权威口径在 ROW_NUMBER 派生」**不属实**
> （全仓 `row_number()` 仅 `admin/annotation_handler.go:390` 一处且无关）⇒ 真况：
> `SetTurnNo` 无调用方 ⇒ 侧表 `request_context_attrs.turn_no` 恒 NULL **属实**；但
> **会话轮 turn_no 正常**（权威在会话写手 MAX+1，211 实测 4063 行 turn_no=1）。
> ⇒ ★★ **同族缺口登记不修**：Gemini 入口（`/v1beta/models/`、`/v1/models/`）整条无识别链、
> 也不写侧表 ⇒ **从不出现在「空名」统计里**（是没记录，不是识别成功）⇒ 211 侧表 48h 按协议
> 分布实测 Gemini 协议行 0 条 ⇒ 当前零流量 ⇒ 登记为待办（接线需新建整条日志写入路径）。
> ⇒ ★★★★ **修复 4（本轮最实质，★ 生产实测坐实）**：失败行的系统提示词兜底**根本没跑**
> —— 部署后真实流量实测：**同一请求成功行落 `agent_name=zcode`、失败行落
> `agent_name=go-client`**。根因：`meta.SystemPrompt` **只在 `EnsureCaptured()` 里生产**，
> `refreshMeta()` 只消费它 ⇒ responses/messages 的失败行构建器
> `recordFailedRequestWithKey` **只调 `refreshMeta()`** ⇒ 兜底链最后一环没有输入。
> 对照 chat 路径失败行走 `captureAndEmitFailure` → **已调 `EnsureCaptured()`**
> （211 读数：chat 失败行 2248 条全部带 `prompt_tokens`）⇒ 两条路径本来就不对齐。
> ⇒ 修复：失败行构建器改调 `EnsureCaptured()`（body 已在 ctx 上，不重读请求）。
> ⇒ ★★ **教训**：**「同一兜底在成功路径生效、在失败路径失效」是可观测的**（同一请求两行
> 落库值不同），比「全都不识别」更难发现；**失败行是运维最常看的行** ⇒ 只在成功路径
> 生效的识别，排障价值趋近于零 ⇒ **凡有兜底链的字段，逐条路径核销「兜底的输入由谁生产」**。
> ⇒ 审计档 §5b（含变异验证读数）；通用式见 `LESSONS.md` **§602**（7 条）。
> **2026-11-03 同步轮（合并轮）**：16 处冲突逐条定性 ⇒ 合并暴露 **4 条真缺陷**（2 条 P0＝spec/路由**重复注册** ⇒ ★「文本不冲突 ≠ 没重复」）⇒ 详见 `docs/audit/2026-11-03-拉取合并轮-冲突定性与合并暴露的四条真缺陷.md` ＋ `LESSONS.md` §625。
