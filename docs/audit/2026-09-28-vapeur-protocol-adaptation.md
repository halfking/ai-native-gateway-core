# Vapeur AI 协议适配审计（2026-09-28，R-vapeur）

**触发**：用户报告经本地网关以凭据 hxt-local（credential 126，provider 36 vapeur，
protocol=openai-responses）访问 gpt-5.6-terra / claude-sonnet-5 / claude-opus-5 /
gpt-6-sol / gpt-6-astra「经常出错不通」。

## 一、上游实测（hxt-local 直连 api.vapeur.ai，2026-09-28 12:2x–12:4x +08）

| 模型 | /v1/chat/completions | /v1/responses |
|---|---|---|
| gpt-5.6-terra | 200 | 200 |
| gpt-6-sol | 200 | 200 |
| gpt-6-astra | 200 | 200 |
| claude-sonnet-5 | 200 | **400「该供应商不支持 Responses API」(unsupported_operation)** |
| claude-opus-5 | 200 | **400 同上** |
| qwen 系（node_probe_state 佐证） | 探针记录 | **502「QWEN provider does not support the Responses API (/responses). Please use /v1/chat/completions instead.」** |
| doubao 系 | 同上 | 502 同型 |
| gemini 系 | 同上 | 400 同型 / timeout |

结论：vapeur 是多厂商聚合中转，**/v1/responses 仅对 GPT 系开放**；模型族协议
能力是「按模型」而非「按供应商」的——provider.protocol=openai-responses 的一刀切
假设不成立。

## 二、根因（三层叠加）

1. **探针按供应商协议一刀切打 /v1/responses**
   （providercap.Resolve openai-responses → EpResponses；node_probe/model_probe/
   active_probe/credential_probe_v2 四条链路同源）。非 GPT 模型族全部被上游 400/502
   拒绝 → node_probe_state 红 → URSM v2 节点视图 available=0 → **路由整体排除
   vapeur**（request_logs 实证：claude 系一直走 suyun 13092、terra 走 apigpt 314，
   provider 36 从未被选中）。上游剩余单点（apigpt 90s 挂起）时用户面即 503/超时。
2. **credential_probe_v2 step2 形态错配**：openai-responses 供应商的 step2 把
   chat 体（messages/max_tokens）POST 到 /responses URL——任何合规 Responses 端点
   都回 400 "Unsupported parameter: 'messages'"（实测），凭据级健康判定被污染。
3. **URSM v2 authoritative 全拒静默**：router.go filteredByViews 返回 0 候选时
   直接 return nil，零日志；executor 侧只见推断值 availability_check_failed:N，
   事故无法从请求日志归因。

另：reqprobe 数据面同请求回退（Responses→Chat）已存在，但文案匹配表缺 CJK
「该供应商不支持 Responses API」变体（语序与英文 "responses api is not" 相反），
vapeur 400 不触发回退。

## 三、修复（全部落地 + 单测）

| # | 文件 | 内容 |
|---|---|---|
| 1 | internal/providercap/capability.go | 新增 `ResponsesUnsupportedError(status, body)`：状态码白名单(400/404/405/415/422/501/502) + 「提及 Responses API 且带否定/重定向 chat」文案判定；参数拒绝类（"Unsupported parameter: 'messages'"）不误判 |
| 2 | bg/probe_responses_fallback.go（新） | 共享降级原语 `responsesChatFallbackPing`（chat/completions、max_tokens=10、Bearer）+ 统一注记 `responsesUnsupportedDetail` |
| 3 | bg/node_probe.go probeDirect | responses 探针命中「不支持」裁决 → chat 降级复探；chat 绿则本轮 ok（chatFallback 标记）；红则维持原结论+附注 |
| 4 | bg/probe_http.go probeWithRetry | Layer 4（model_probe 链）同款降级，阻断 broken_confirmed → binding 压红 |
| 5 | bg/active_probe_executor.go Run | 主动探针同款降级（结果回灌 CredentialStateManager 前纠正） |
| 6 | bg/credential_probe_v2.go | step2 对 openai-responses 改发原生 `miniResponses`（{"input","max_output_tokens"}）；命中不支持裁决 → chat 降级 |
| 7 | internal/reqprobe/diagnose.go | modeMismatchHints 补 CJK/中转变体（"不支持 responses api"、"does not support the responses api"）→ 数据面同请求回退可命中 |
| 8 | domains/streaming/executors/router.go | authoritative 全拒补 WARN（view_sample），消除静默 |

单测：internal/providercap/responses_unsupported_test.go（10 例含误判反例）、
bg/probe_responses_fallback_test.go（5 个行为测试）、reqprobe_test.go（CJK/英文
2 例）。`go test ./bg/ ./internal/providercap/ ./internal/reqprobe/
./domains/streaming/executors/` 全绿；`go build ./...` 通过；gofmt 干净。

## 四、部署与验证（本地 8782）

- 部署 2.5.6.2295→2296（2296 修正密钥漂移：deploy 从 .env.local 取到 43 位错误
  SECRET_KEY，凭据解密 12/16 失败；以旧容器真值 64/44 位导出重部署后 failed=0。
  教训：**本地部署密钥必须显式导出，.env.local 的 SECRET_KEY 是错值**）。
- 探针生产证据（node_probe_runs，2296 上）：
  - claude-opus-5-5 / claude-haiku-4-5：direct_ok=t，
    detail="responses probe rejected (HTTP 400: Responses API unsupported for this
    model); chat fallback probe OK (HTTP 200)"；URSM 视图翻绿；claude-opus-5 视图
    亦翻绿。
  - credential_model_bindings 8 个目标模型 available 全 t。
- 数据面矩阵（2296，测试 key 用后即删）：
  - /v1/chat/completions 入口：5 模型全 200（gpt-6-astra 经 vapeur chat 上游
    200——修复前该路径被探红排除）。
  - /v1/responses 入口：5 模型全 200（GPT 系 resp_ id、Claude 系经翻译 msg_ id）。
- 遗留（非阻断）：URSM 视图 claude-sonnet-5/qwen 系随探针队列（1 worker）慢速
  自然翻绿；apigpt(apiclaude.cc) 上游偶发 60-90s 挂起为独立供应商问题；
  glm-5「Agent capabilities are not enabled」为 GLM 上游对探针的真实拒绝；
  gemini 系 responses 超时为上游真慢——均与本修复无关。

## 五、「默认使用 responses」的语义澄清

用户期望「默认走 responses」在网关侧的现状与边界：
- **上游出站**：openai-responses 供应商默认端点=/v1/responses（保持）；探针/数据面
  在被上游判「不支持」时自动降级 chat（本修复）。原生 Responses 传输（保留
  Responses 信封不翻译）仍由 credential_model_capabilities 能力闸门控制
  （SupportsNativeResponses(Stream)，目前仅 grok-4.6 有记录）。
- **vapeur 的现实**：responses 端点仅 GPT 系可用，Claude/qwen/doubao/gemini 必须
  chat——「默认 responses + 按模型降级 chat」即最优策略，已实现。

## 六、批判式复审（2026-09-28 22:5x，第二轮——修正本文件首版的过度声明）

首版文档与提交信息里有三处声明强于证据，现修正：

1. **「credential_probe_v2 step2 污染凭据级健康判定」——过度声明，降级为
   「错配存在，健康受损未证实」**。chat 体 POST /responses 必 400 是实测事实；
   但修复前（当日 12:2x）credential 126 的 health_status 就是 healthy、
   health_error 为空——旧代码如何通过（step2 被跳过？错误分类非致命？）未深究。
   修复后 21:21 复检仍 healthy。结论：形态修复正确且无回归，但「污染」影响
   面首版没有证据支撑。
2. **「绑定恢复」措辞不准**。credential_model_bindings 在整个事故期间从未被
   压红（available 恒 t）；被压红的是 node_probe_state（探针态）与 URSM v2
   Redis 节点视图（available=0）——路由排除发生在视图层。首版混用「绑定」
   一词误导。
3. **reqprobe CJK 补丁是防御性路径，生产当前不触发**。数据面同请求回退仅在
   native responses 传输（capability 表 SupportsNativeResponses(Stream) 有行）
   时才会遇到「responses 被拒」文案；5 个目标模型 capability 表无行 → 走 chat
   URL → 不触发该路径。该补丁的价值在未来 capability 启用时。证据级别：单测。

**证据分级表（实事求是）**：

| 修复点 | 证据级别 |
|---|---|
| probeDirect responses→chat 降级 | **生产实证**：node_probe_runs 17:22-17:23 claude-opus-5-5/haiku-4-5 记录 "chat fallback probe OK (HTTP 200)"；22:45 复查 URSM 视图 claude 全系/qwen 系/gpt-6-astra 全部 available=1（首版时仅部分翻绿，「队列会消化」当时是推测，现已实证） |
| probeWithRetry（model_probe Layer4）降级 | 单测（httptest）；生产触发依赖 featured 模型集合，未单独验证 |
| active_probe_executor 降级 | 单测；生产由同判定器+同原语支撑（链路一致性由十九轮复核背书） |
| credential_probe_v2 miniResponses | 单测 + 间接一致（21:21 healthy 与新代码一致，不排他） |
| reqprobe CJK hints | 单测；生产不触发（见上） |
| URSM 全拒 WARN | 未触发场景验证（纯日志，低风险） |
| 数据面 5 模型×2 入口 | 生产实测全 200（含 gpt-6-astra 经 vapeur chat 上游） |

**十九轮并行审计复核**（commit c3428bdec，docs/12小时内修订审计-20260928-2220.md）：
四链路修复深审「全属实」；并落地三项本文件作者遗漏的收口——N19-1 检测器
参数名词排除（"does not support the 'messages' parameter" 语序误判）、
N19-2 降级失败注记不伪造 "(HTTP 0, 0ms)"、N19-3 egress 阻断保留原失败上下文。
本轮复审认可全部三项；检测器末尾 chat/completions 宽松重定向为**有意权衡**
（注释在案：真实中转裁决几乎都携带它；残余误判面=多打一发 chat 探针+注记，
不污染可用性方向），不改为强匹配。

**新发现登记（本轮批判复审产出，均非阻断）**：
- `credential_health_checks` 表 0 行（全库）——该审计写入路径死代码，探针结果
  只落 credentials.health_* 列，无逐次明细。移交存储/审计轨道。
- grok-4.6（cred 126）URSM 视图 available=0 但 node_probe_state 无行：21:53
  由流量失败路径写入的孤儿视图（非目标模型，responses 上游本身 200）；无探针
  行则爬梯不会复探，依赖流量自愈或人工触发。移交探针轨道。
- 本地 PG 22:40-22:44 crash recovery（"not properly shut down"，redo 9s 完成，
  网关 degraded→recovered 自动恢复）：发生在本任务活动窗口（12:2x-17:3x）之外，
  与已知宿主 OOM 崩 PG 模式一致（环境债，非本修复引入）。

## 七、第二轮批判复审追加（2026-09-28 23:1x）——部署管线连环坑与治本

第一轮复审后复测数据面发现 **5/5 全 503 复发**，顺藤查出三个部署管线问题
（均非 vapeur 修复代码缺陷，但直接造成用户可见的"经常不通"回归）：

1. **密钥漂移复发（根因）**：并行会话 23:00 部署 2300 时从 .env.local 取到
   错误密钥——该文件第 34/35 行 SK=CEK=同值 44 位（非正确 64 位 SK），
   容器解密全挂 → enrichWithAPIKeys 把全部候选标记不可用 → dispatch 层
   ErrNoRoute → 503。22:37 起日志实锤（decrypt circuit OPEN）。
   **治本**：.env.local 两行已修为 2296 验证过的正确值（SK 64 位、CEK 44 位，
   两种 padding 形态等价，经验裁决 decrypt cred 126 → sk-UiDq… 前缀一致）。
   此后任何会话直接 deploy 不再需要手工 export 密钥。
2. **web 构建门假死**：0d5230bfb（前端依赖安全升级，vitest→^5）后
   node_modules 未同步（仍 1.6.1），vue-tsc 对两个测试文件的 vitest-5 风格
   mock 报类型错 → npm run build exit 2 → 部署在 build_frontend 夭折
   （表象：版本号连跳 2301-2306 无部署、构建锁 die）。npm install 同步
   后 vue-tsc 零改动通过。教训：**依赖升级提交必须伴随 lockfile 安装验证**。
3. **PG 22:40 crash recovery**：见第六节登记，与部署问题无因果（时间在前）。

**修复动作**：.env.local 密钥治本 + npm install 同步 → 部署 2.5.6.2308
（含十九轮 c3428bdec + 本轮全部内容），解密冒烟 failed=0（双端口）。
**最终矩阵（2308，5 模型 × chat/responses 双入口）= 9/10 通过**；唯一失败
gpt-6-sol chat 入口 90s 超时，归因 apigpt(apiclaude.cc) 上游挂起 89.7s
（err=canceled，同上游随后 13.8s 成功）——独立供应商问题，本会话两次复现，
移交名单外另登记。

**假声明自查结论**：第一轮文档中「部署 2296 后问题解决」的表述在并行部署
覆盖 2300 后失效过约 80 分钟（23:00-23:1x）；本节记录了完整因果链。
"部署即修复"的声明必须绑定"且无后续并行部署回滚密钥"才成立——治本点
落在 .env.local 而非部署动作本身。
