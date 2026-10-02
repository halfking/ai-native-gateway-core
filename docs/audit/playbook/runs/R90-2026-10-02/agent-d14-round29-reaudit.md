# D14 第三十轮复审报告 —— 20fbdba74 sanitize 链 + bg listener 独立复核

HEAD=20fbdba74，三份涉审文件工作树与提交一致（无后续改动）。只读审计，未修改任何文件。

## 一、发现（候选，待主代理复核）

### D14-F1（P2）：`shell_call` / `local_shell_call`（call 侧）仍是 input default fail-open + restore 双侧无 case —— 本提交修了 output 侧三兄弟却漏了 call 侧配对项
- 证据：`security/sanitize/input_protocols.go:290-348` switch 全集含 `apply_patch_call`（:337）与三个 `*_output`，但**没有** `shell_call` / `local_shell_call`（call 项，`action.command` 为 []string 明文，Codex shell 调用的请求侧）。`security/sanitize/native_restore.go:116-220` restore switch 同样两者皆无。
- 触发路径：input 侧与 `function_call.arguments` / `apply_patch_call.action.content` 完全同型 —— 模型看到占位符、生成的 command 含占位符、网关 restore 后客户端拿到**真实值**、无状态回放时 call 项必然随 input 回流，real plaintext 直送上游。响应侧反现为可用性打击：模型把占位符学进 `shell_call.action.command` → restore 不认识该 item → 残留 marker 守卫整响应 block（`smart_sani_guard.go:810-825`）；流式则整流 block。
- 本提交修 `apply_patch_call`（call 侧）时证明了 call 侧在威胁模型内，却未处理同族的 `shell_call`/`local_shell_call`。与已登记遗留 `tool_search_call.arguments` 不同，这两项是本次所修 output 项的必然配对项（有 output 必有 call 且回放成对）。建议 P2。

### D14-F2（P3）：`computer_call` input item 未入洗
- 证据：`security/sanitize/input_protocols.go` 无 case；`domains/transformation/responses_compress.go:219` 明确把 `computer_call` 列为合法 input item（"Multimodal and computer-use items"）；restore 侧、compliance 侧均无。
- 触发路径：`action.text`（type 动作的键入明文，如自动登录输密码）回放时明文直达上游。计算机使用流量在本网关出现频率存疑，建议 P3（若 Codex 族是主场景可升 P2）。

### D14-F3（P3）：`apply_patch_call` 显式 null action 与同提交 web_search_call 修复后的语义自相矛盾
- 证据：`security/sanitize/input_protocols.go:616-618` —— `apply_patch_call` 的 `action: null` → `errInvalidSanitizeInput` → 整请求 400（`smart_sani_guard.go:238-239`）；而 `:433-439` web_search_call 的 null 本轮刚改为等同缺失直通；且 `:611-613` apply_patch_call **缺失** action 是直通的。同一个提交里写出的两个 handler，对"null 拒单 vs 缺失直通"的不一致恰好复刻了 R29 自己修掉的问题。apply_patch_call `action: null` 的合法形态存疑（fail-closed 有辩护空间），但至少应与 web_search_call 口径统一。建议 P3。

### D14-F4（P3）：outputcompliance 检查器的 Responses item 覆盖窄于 restore，六新载体还原为真实值后不再被合规复检
- 证据：`domains/hooks/outputcompliance/protocol_text.go:212-222`（非流式 `addOutputItem`）与 `:329-360`（流式 `response.output_item.done`）只认 message / function_call / custom_tool_call / reasoning；六个新载体及 mcp_call / web_search_call / file_search_call 均不在列。而管线顺序是 restore 先于 compliance（`cmd/gateway/goal_control.go:561-566`）。
- 触发路径：模型在这些 item 里**新造**（非回声）的敏感内容（如 code_interpreter 的 code、shell 输出里出现新密钥形态）由 detector 本应被 compliance mask/block，但走不到检查 lane，客户端可见。对旧四项是存量问题，本提交把差距扩大到新六项。建议 P3 登记。

### D14-F5（P3，测试债）：restore 侧六载体零钉测，提交自称"镜像对称"无测试承重
- 证据：本提交测试改动仅 `security/sanitize/input_protocols_test.go`（`git show 20fbdba74 --stat`）；`native_restore_test.go` 未动。删掉任一 restore case 不会被任何测试抓红，只表现为线上整响应 block 的可用性事故。提交说明里的"钉测变异承重（废 code_interpreter_call case → 红）"仅覆盖 input 侧，且系人工验证、树内不可复核。建议 P3（补 restore 侧镜像钉测）。

### D14-F6（P3，登记项）：流式路径对六新载体零 restore，失败形态为整流 block
- 证据：流式无通用递归——`restoreStreamResponsesDelta`（`smart_sani_guard.go:1289-1316`）只认 delta/done 文本 lane；六载体出现在 `response.output_item.done` 事件里时落到 `containsReservedJSONToken`（`sse_restore.go:99-102`）→ `state.blocked` → 整 chunk/整流 block（`smart_sani_guard.go:905-914`）。是 fail-closed 非泄漏，但意味着：input 侧把占位符放进这些载体后，流式响应里模型一学舌即整流可用性受损，与非流式（可还原）不对称。建议登记为已知不对称。

## 二、核实为健康的面

1. **六载体 input 侧真实接线（逐 case 验过，非空 case）**：`local_shell_call_output`/`apply_patch_call_output`（`input_protocols.go:322-328`）→ 共享尾部分派 `field="output"` → `sanitizeContent`（:361）→ 字符串走 `sanitizeText`；`shell_call_output` → `sanitizeShellCallOutputItem`（:525-558，outputs[].text 逐元素洗、写回 `item["outputs"]`）；`code_interpreter_call` → `sanitizeCodeInterpreterCallItem`（:562-606，code + outputs[].logs，image 输出因无 logs 键天然跳过）；`apply_patch_call` → `sanitizeApplyPatchCallItem`（:610-638，action.content）。畸形形态（outputs 非对象数组、action 非对象）→ 400 fail-closed，符合全仓口径。
2. **钉测形态正确**：`input_protocols_test.go:82-99` 每载体唯一敏感值（13800138011-16 六个独立号码，全落 phone 检测模式）+ 逐值 `NotContains` 循环（:124-126）为承重断言；占位符计数与 Redis 映射长度（:152）为辅助。单类型退回 default 必红。
3. **restore 侧镜像对称（超集方向，安全向）**：`native_restore.go:191-219` —— output 双载体走 `restoreNativeTextField("output")`；`shell_call_output`+`code_interpreter_call` 对 `outputs`/`code` 走 `restoreNativeNestedValue` 递归（shell 的 text、code_interpreter 的 logs 均被覆盖；image 输出被 `nativeMediaObject`:374-381 跳过，与 input 侧只读 logs 一致）；`apply_patch_call` 走 action.content。restore 覆盖 ⊇ input 覆盖，无"input 洗了 restore 不认"的误拦方向缺口（其余见 F5 流式注记）。
4. **web_search_call null 修复边界正确**：缺失直通（:428-431）；显式 `null`（含空白变体）直通（:436-438）；非对象形态（string/array/number/bool，unmarshal 进 map 必错）仍 400 拒单（:439）。restore 侧对 null action 的类型断言天然跳过（`native_restore.go:169`），双侧一致。
5. **Ready() 修复属实且语义自洽**：`bg/auto_route_realtime_listener.go:95-104` nil receiver 返回预关闭 channel，"永久阻塞 select"风险消除；与 `Start()`（:109 nil no-op）、`Stop()`（:131 nil 安全）的"nil=功能关闭不等待"语义一致。现网唯一调用方是集成测试（`bg/auto_route_realtime_listener_integration_test.go:150`）；生产路径（`cmd/gateway/main.go:3704/5311/7792`）只 Start/Stop 从不调 Ready()，故此修复纯防御性、无行为回归面。
6. **"漏 restore = 整响应 block 而非泄漏"的前提成立**：残留 marker 守卫对 native Responses 体无条件 block（`smart_sani_guard.go:810-825`），无 session/map 降级路径也先 mask/block（:713-742）。
7. **生产接线完好**：input 中间件 `goal_control.go:549-554` → `handler.go:1899-1906` 入口生效；restore 拦截器先于 output_compliance（`goal_control.go:561-566`）。

## 三、未覆盖项与原因

1. 未运行 go test / 变异验证：只读授权（go test 会写构建缓存）；"变异承重"主张仅能静态核对测试形态，不能复核执行结果。
2. item 类型全集无仓内权威定义：仓内未 vendor OpenAI Responses SDK，类型清单只能从仓内四处 switch（`input_protocols.go`、`native_restore.go`、`responses_compress.go:193-226`、`protocol_text.go:212-222`）+ API 通识枚举。D14-F1/F2 基于配对关系与仓内旁证（compress 白名单）推断，外部 spec 逐项比对无法离线完成。
3. `tool_search_call.arguments`：确认仍开放且已登记（`input_protocols.go:345`、审计报告：71），未重新裁定优先级。
4. `mcp_approval_request.arguments` / `web_search_call` action.results 等边缘子载体：全历史回放场景下理论可回流，证据不足以定级，仅在此登记。
5. 本提交其余 6 项（installer/迁移/部署脚本/secrets baseline/down 注释）不在 D14 域，未审。
