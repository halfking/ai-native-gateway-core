# 2026-09-18 MiniMax `thinking.type=enabled` 拒收事故（hzx-2 / minimax-prod-v2 强启即降级）

- 环境：245 预发布（pre-prod），provider 14 = `minimax`（https://api.minimaxi.com/v1）
- 受影响凭据：cred 42（hzx-2）、cred 21（minimax-prod-v2），均绑定 `MiniMax-M3` 等模型
- 修复 commit：`6d474a725`（translateThinking v1）→ `4bf39f963`（v2：最小对象改写）
  → `c6de4699d`（**v3：executor 接线，真正生效的一笔**）
- 状态：**E2E 已闭环**（2137 上游 200 且返回真实思考内容）；详见 §5.2

## 1. 现象

- 客户端直连 MiniMax 原厂（`MiniMax-M3` + thinking）正常；
  走网关同一模型 100% 失败。
- 管理端对 cred 42/21 执行 force_enable 后，数十秒内再次被降级
  （breaker 开→关循环，冷却 30s 一轮）。

## 2. 根因

MiniMax 上游（2026-09 起）将 `thinking.type` 白名单收窄为
`adaptive | disabled`，对 `enabled` 返回 HTTP 400：

```
{"type":"error","error":{"type":"bad_request_error",
 "message":"invalid params, invalid thinking.type: \"enabled\" (allowed: adaptive, disabled) (2013)",
 "http_code":"400"}}
```

（journald `candidate_failure_alert`，2026-09-18 05:13，cred 21。）

这是生态级变更，非本网关独有：Kilo Code 同期因发送 `enabled` 出现同类故障
（github.com/Kilo-Org/kilocode/issues/11203）。

网关侧的传导链（OpenAI 协议入向，zcode 的路径）：

1. `internal/ir/parse_openai.go`：`thinking` 不在 knownFields →
   进入 `req.Extensions["thinking"]`（`ir.Thinking` 保持 nil）。
2. `internal/ir/serialize_openai.go:182` → `restoreExtensions(..., ProtocolOpenAIChat)`。
3. `internal/ir/extensions_restore.go:78` →
   `paramreg.Apply("thinking", val, src, dst=DialectMiniMax)`。
4. 修复前 `thinking` 登记为 `KindIRHandled`，`Decide()` 返回
   `ActionRestore`（decide.go:117-134 的 2026-08-11 设计决策：Extensions
   里的字段必然是某条 parser 路径未消费的，不能 Skip）。
5. 客户端原值 `{"type":"enabled",...}` 被原样写进 MiniMax-bound body →
   上游 400。
6. `credstate` 连续失败计数达阈值(2) → breaker open → 「强启即降级」。
   force_enable 链（DB + 内存缓存 + URSM v2，`admin/routing.go:5436`
   applyForceEnable）本身行为正确——每个新请求仍携带坏 body，
   所以状态修好又被同一坏请求打回。

关键佐证：事发时 DB 状态全部健康
（`circuit_state=closed / consecutive_failures=0 / availability_state=ready`），
且 400 与 force_enable 无时间耦合，只与请求到达耦合。

## 3. 修复

三轮迭代（每轮都被下一轮的实测推翻过一次，教训在 §5.3）：

**v1（`6d474a725`）`internal/paramreg`**：
- `translate.go` 新增 `translateThinking`；`registry.go` 把 `thinking`
  从 `KindIRHandled` 升为 `KindTranslatable`。方向正确但（见 v3）在生产
  主路径上从未被执行。

**v2（`4bf39f963`）**：MiniMax 改写时输出最小对象 `{"type":"adaptive"}`
（剥离 budget_tokens 等——M3 无 budget 概念，见
platform.minimax.io/docs/api-reference/responses-create：effort 档位只是
adaptive 的兼容别名）。

**v3（`c6de4699d`，生效的关键）`executor 接线`**：
- 根因补全：`executor_chat.go` 的 legacy 分支（OpenAI 协议入向 = zcode
  实际路径）经 `TransportIRConverter.ParseOpenAI` 解析，而该 converter
  **不会**把 `TransportContext.UpstreamCatalogCode` 盖到
  `irReq.TargetProvider`（只 stamp 请求类别）；该分支也不像 anthropic
  分支那样调用 `SetContext`。于是 `resolveTargetDialect` 回退
  `DialectOpenAIChat` ≠ MiniMax → `translateThinking` 透传 → 依然 400。
- 修复：legacy_with_ir 分支与断路器兜底分支在 SerializeOpenAI 前设置
  `irReq.TargetProvider = cand.CatalogCode`；`applyInlineValidation`
  （e.IR==nil 路径）新增 `targetCatalogCode` 参数同样赋值。
  `handler_gemini` 有意不补：它在路由前序列化（无 candidate），出向
  序列化随后在 executor 内带 candidate 上下文重跑。
- 回归测试 `TestSerializeOpenAI_ThinkingDialectByTargetProvider`
  钉死契约：同一 IR，TargetProvider 缺失时原样透传、`minimax` 时输出
  最小 `{"type":"adaptive"}`、`deepseek` 时透传。

附带修复：`cmd/gateway/main.go` 补 `net` import（commit `cd76a7f0e`
引入 `net.SplitHostPort` 但漏加 import，导致其后 main 分支本地构建必失败）。

## 4. 覆盖范围与已知缺口（实事求是）

| 路径 | 状态 |
|---|---|
| OpenAI 协议入向 + OpenAI 形态上游 → MiniMax（zcode 路径） | ✅ 本修复覆盖 |
| OpenAI 协议入向 → 非 MiniMax 上游 | 行为不变（透传） |
| Anthropic 协议入向 → OpenAI 形态上游（**含 MiniMax**） | ❌ **不在本修复内**：`parse_anthropic` 把 thinking 消费进 `ir.Thinking`（不进 Extensions），`serialize_openai` 对 `ir.Thinking` 仅上报 protocol loss、不输出 → thinking 被静默丢弃（不会 400，但推理意图丢失）。属 P5 reasonnorm 统一处理范畴，需单独排期 |
| `reasoning_effort`/`reasoning` 字段的方言翻译 | 不变（本事故未涉及） |

## 5. 部署与验证记录

### 5.1 时间线（含失误复盘）

- 05:28 首次 `deploy-245.sh`：构建 2127（含 v1 修复）成功上传，
  但 step 9.1 sync-admin 撞 252 共享 PG `too many clients`（当时
  38/100 连接占用）→ 脚本按契约自动回滚 nginx，未切流。
- 05:35 手工蓝绿：slots/8782 → 2127，restart，8782 成为 active。
  05:35–05:42 窗口真实流量验证：cred 21 上游 `200×11`、cred 42/21
  的 4xx=0（此前同型请求 100% 400，错误预览均为 thinking.type）。
- 06:14 **并发部署事故**：official-deploy worktree 的另一次部署上线
  2133/2134（构建于 06:14，早于 06:16 的修复 push，**不含修复**），
  并把 active 切到 8782@2134，同时清掉了 slots/8782 → 2127 的指向。
  教训：手工改 slot 无法对抗正规部署流程；修复必须先进 origin/main
  再靠下一次正规部署承接。
- 06:16 修复 push origin/main（`6d474a725`）→ 后续正规部署自动携带。
- 本轮（v2 修复后）：重新正规部署 + E2E 验证，结果见 §5.2。

### 5.2 E2E 验证（受控、可复现）

方法：admin 创建临时 API key（id=128）→ 经 `/v1/chat/completions` 发
`model=minimax-m3` + `thinking:{type:enabled,budget_tokens:1024}` →
对照 journald `upstream_http_attempt.upstream_status` → 验毕吊销 key
（disable 后复测 401）。

| build | 内容 | 同一请求结果 |
|---|---|---|
| 2135（4bf39f96，v1+v2 修复） | 翻译存在但 executor 未接线 | **upstream 400**（body_out=159） |
| 2137（c6de4699，+v3 接线） | TargetProvider 接线 | **upstream 200**，响应含 `<think>…</think>` 真实推理内容 |

同窗口真实用户流量（~172KB 流式，与此前 400 的 186KB 同类）：
`upstream 200`。两个凭据（21/42）force_enable 后不再被打回。

### 5.3 假阳性复盘（本事故最重要的一条教训）

v1 部署后（05:35–05:42）曾以「cred 21 上游 200×11、窗口内 4xx=0」
宣布修复生效——**该结论是错的**：2135 的受控 E2E 证明翻译在真实
执行路径上从未运行，那 11 个 200 请求大概率根本不带 thinking 字段。
错误根源：
1. 把「窗口内没有 400」当成「带 thinking 的请求通过了」，没有构造
   已知携带 thinking 的受控请求；
2. 用 `strings | grep translateThinking`（函数在二进制里）冒充
   「逻辑被执行」，二者不是一回事；
3. dummy key 撞 401 后没有追下去，错过了最早的验证机会。

正确姿势（本轮已执行）：有效 key + 确定性 payload + 上游状态码 +
响应内容（`<think>` 段）四重证据，且修复前后同请求对照。

## 6. 遗留风险

1. **Anthropic/Gemini 入向 thinking 丢弃**（§4）：Claude Code 等 Anthropic
   协议客户端经网关到 MiniMax/DeepSeek 等 OpenAI 形态上游时推理意图
   丢失（parse 消费进 `ir.Thinking`，serialize_openai 只上报 loss 不
   输出）。不会 400，但功能缺失；属 P5 reasonnorm 统一处理范畴。
2. **paramreg 方言决策依赖 caller 正确传 TargetProvider**：本次接线覆盖
   executor 三条 OpenAI 出向路径；若未来新增出向序列化点而忘记设置
   TargetProvider，同类「翻译不生效」会静默复发。回归测试只钉了
   internal/ir 层契约，executor 层无自动化测试覆盖（人工 E2E 已验证）。
3. **并发部署竞争**：多人/多 worktree 同时 deploy 245 仍可能互相覆盖
   slot 与 active-port（本轮实测发生两次：06:14 2134 覆盖、06:19 手工
   slot 被 deploy 清理）。deploy 锁只锁同机同脚本，不锁不同 worktree
   的并发 deploy-seamless。
4. **252 共享 PG 连接上限**（max_connections=100）：sync-admin 等部署
   步骤在高占用时段会 FATAL `too many clients` 触发不必要的自动回滚。
   `f6c8585ad` 已把池上限做成可配置（LLM_GATEWAY_DB_MAX_CONNS）+ 文档
   对齐，但 252 实例侧 max_connections 是否同步扩容需运维确认。
5. **version.json SSOT 多读路径**：`/api/system/version`（admin 读
   `/opt/llm-gateway-go/version.json`，进程内缓存到重启）与 `/healthz`
   （读 slots/%i/version.json）在手工换版本时可能短暂不一致。
   正规部署路径两者同步，不受影响。

## 7. 收尾轮（2026-09-18 当日，§4/§6 缺口闭环）

### 7.1 P5 补课：Anthropic/Gemini 入向 thinking 按 TargetProvider 方言出向（`7bb1708d5`）

- `serialize_openai` 新增 `applyThinkingToOpenAIChat`：`ir.Thinking`
  （anthropic 入向）/ `Reasoning.BudgetTokens`（gemini 入向 budget 形状）
  经 `reasonnorm.Render` 渲染进 OpenAI 线格式。**方言键 = TargetProvider，
  刻意不用 reasoncap.Resolve（按模型名推 caps 会复燃"方言与目标 provider
  脱钩"的本次事故失败类）**，caps 按 `resolveTargetDialect` 合成。
- 方言族：minimax（最小 `{"type":"adaptive"}`，与 openai 入向
  translateThinking v2 语义一致）/ deepseek / glm / ark（enabled|disabled
  toggle）/ qwen（enable_thinking+thinking_budget）。kimi / grok / mistral /
  vllm / ollama / 未知方言维持 loss 上报不出向；openai 协议入向的
  reasoning_effort 已原生出向，加守卫防二次表达。
- executor anthropic→openai 分支补 `irReq.TargetProvider = cand.CatalogCode`
  （此前只有 transport 层 Extensions 兜底，ir.Thinking 的方言渲染够不到）。
- loss 上报条件化：已渲染的 thinking 不再报 `ir_protocol_loss`；
  signature / redacted_thinking 仍按真丢失上报。

### 7.2 executor 层接线自动化测试（§6.2 缺口闭环）

`executor_target_provider_wiring_test.go` 把 `finalizeOpenAIUpstreamBody`
三条出向分支逐条钉死 CatalogCode 必须到达序列化层：legacy_with_ir（openai
入向主路径）/ 断路器兜底（包级 ir 函数路径）/ anthropic→openai IR 分支。
ir 层另有 P5 契约测试 `serialize_openai_thinking_p5_test.go`（含 gemini
budget 形状、openai 入向防双表达、intent 提取单测）。

### 7.3 154 生产同步部署 + 受控验证（2134-289c880e → 2138-73c8a6c5）

deploy-seamless 全门禁绿（healthz/readyz/版本指纹、DB 就绪、admin 密码
同步、凭据解密冒烟 providers=18,1,14 creds=12 failed=0、Nginx 切 8782）。

四重证据受控验证（专用 key id=130，e2e-app；同一确定性 payload
"17×23"，仅 thinking 开关与入向协议变化，2×2 共四次）：

| 入向协议 | thinking | 上游（provider 14 / cred 21, api.minimaxi.com） | 响应 |
|---|---|---|---|
| openai (/v1/chat/completions) | enabled | `upstream_status=200` | `<think>…391</think>` 真实推理 |
| anthropic (/v1/messages) | enabled | `upstream_status=200` | anthropic `thinking` block + text |
| openai | disabled | `upstream_status=200` | 无 `<think>`，直接答案 |
| anthropic | disabled | `upstream_status=200` | 仅 text block |

disabled 对照翻转了推理内容出现与否 —— 证明方言翻译**确实送达上游**
（若 thinking 被静默丢弃，M3 默认思考，对照不会翻转）。这也覆盖了
§4 表中 ❌ 行（anthropic 入向 → OpenAI 形态上游含 MiniMax）。
验毕 key 已禁用（复测 401）。

注：验证窗口内 journald 的 12 条 `candidate_failure` 全部归属
credential_id=35（与 thinking 无关、非 MiniMax 21/42，属既有独立问题，
另行排查）。

### 7.4 并发部署竞争收口（§6.3 缺口闭环；deploy-lib `2433e10` + 网关 `73c8a6c51`）

排查结论：官方入口（deploy-154.sh / deploy-245.sh）均委托
deploy-seamless，本地 per-target 锁 + 远端 mkdir 锁本应互斥；真实缺口三个：

1. **本地锁路径随 $TMPDIR 漂移**：`${TMPDIR:-/tmp}/...` 在交互终端
   (/var/folders/<user>)、cron/agent 沙箱（unset→/tmp 或私有 TMPDIR）下
   解析出不同路径，本地锁层对不同上下文的同目标部署静默失效。
   → 钉死机器级 `${LOCK_TMP_ROOT:-/tmp}/kx-llm-gateway-deploy-<target>.lock`
   （LOCK_TMP_ROOT 仅供测试隔离）。
2. **kill -0 把 EPERM 当 ESRCH**：跨用户存活部署进程被判死，
   force-recover 会拆活锁。→ 统一 `lock_pid_alive`（ps -p 判存在，与属主
   无关）；PID 复用场景 fail-closed。
3. **带外手工 slots/run 改写在锁外**（06:14/06:19 两次覆盖的直接原因）：
   → deploy-seamless 切流前新增漂移复核（预热前记 active-port +
   slots/<active> symlink 基线，破坏性动作前复核，不一致 fail-closed）。

`tests/deploy_lock_test.sh` 新增 AC-L15（默认路径无视 TMPDIR）/ AC-L16
（PID 1 存活时三路 force-recover 全拒绝），全套 51 项通过。本轮 2138
部署即运行在新锁代码上。

### 7.5 遗留（更新后）

- §6.1 / §6.2 / §6.3 已闭环（见 7.1–7.4）。
- §6.4（252 PG max_connections 扩容）运维侧仍需确认；代码侧
  `f6c8585ad` 已就绪，252 OOM 双层收口见 `be22017de`。
- §6.5（version.json SSOT 多读路径）维持观察，正规部署不受影响。
- 新增：credential_id=35 的 candidate_failure（非本事故范畴）待单独归因。
