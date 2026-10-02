# R91 域 C 报告：sanitize compliance lane 扩容定位 + 边缘载体收口方案

HEAD=ad34b2bc5，工作树干净，`go build ./...` rc=0，`go test ./security/sanitize/ ./domains/hooks/outputcompliance/ ./domains/outputcompliance/` 全绿。审计日 2026-10-02。

## 一、发现（候选 + 方案设计）

## 0. 先纠正一处任务书证据路径

任务书给的证据路径 `security/sanitize/protocol_text.go:212-222/329-360` 不存在。实际文件是 **`domains/hooks/outputcompliance/protocol_text.go`**，行号吻合。#5 的缺口在这份文件，不在 sanitize 包。

## 1. sanitize 链完整拓扑（全部 file:line 来自本次实读）

**input 侧脱敏**（入口：`cmd/gateway/goal_control.go:542` installSmartSaniGuard → `chatHandler.SetSanitizeInputMiddleware`，声明在 `domains/streaming/handler.go:1418`）：
- 中间件 Wrap：`security/sanitize/smart_sani_guard.go:192-298`；主体 `sanitizeRequestBody` 同文件 :303-391
- 协议分信封：`security/sanitize/input_protocols.go:41-107`
- Responses 逐 item 分发：`input_protocols.go:282-374`；reasoning :378-399；mcp_call :403-428；web_search_call :432-465；file_search_call :469-526；shell_call_output :530-563；code_interpreter_call :567-611；apply_patch_call :615-648；shell/local_shell/computer call（R30）:654-676；未知类型 default 直通 :346-352
- 工具参数递归：`security/sanitize/input_tools.go:67-124`（tool_calls）与 :138-237（sanitizeToolValue，credential 键名整值替换 :157-175）
- 内容块：`input_protocols.go:678-718`（sanitizeContent）、:720-773（sanitizeContentBlock）

**restore 侧还原**：
- 非流式入口：`smart_sani_guard.go:708-857` InterceptNonStream；native 形态分派 :773-792
- Anthropic messages 还原：`security/sanitize/native_restore.go:79-99`；内容块 :239-295
- Responses 还原：`native_restore.go:101-237`（message :117-129、function_call :130-135、custom_tool_call :136-141、reasoning :142-156、mcp_call :157-167、web_search_call :168-175、file_search_call :176-190、local_shell/apply_patch output :191-196、shell_call_output/code_interpreter :197-211、apply_patch_call :212-219、shell 族 action :220-233）
- legacy chat choices：`smart_sani_guard.go:1576-1632`
- **协议转换接缝**：链上看到的已是"转换成客户端协议后的 body"（`domains/streaming/native_response_intercept.go:17-19` + :30-62；调用点 `domains/streaming/messages.go:1520`、`responses.go:1395`）
- **流式接缝**：`domains/streaming/handler.go:104-160` interceptingStreamWriter 包装（native 流 `messages.go:814` / `responses.go:815`，chat `handler.go:4686`），逐帧走 chain.InterceptStreamChunk（:341）；chunk 级还原 `smart_sani_guard.go:866-932` + `security/sanitize/sse_restore.go:62-110`；三协议 delta 还原 `smart_sani_guard.go:1171-1243`（OpenAI）、:1247-1285（Anthropic）、:1289-1316（Responses——即 #6 引用行段）；InterceptStreamEnd 仅观测不写回 :1022-1044

**compliance/output 检查 lane**（链上两个闸共用一个拦截器实现）：
- 强制 output_sensitive 闸（还原**前**）：`security/sanitize/output_sensitive.go:24-26`（→ `domains/hooks/outputcompliance/interceptor.go:51-58` mandatory 变体，stateKey "sanitize.output.pending"）；检测器 `output_sensitive.go:33-66` CheckField（credential 键名硬阻断 :62-64）、:115-142 Check
- owner 合规闸（还原**后**）：`interceptor.go:65-69` 构造、:118-129 InterceptNonStream、:186-256 processBody（`transformVisibleJSON` 调用 :212；指标接线 :218-254）
- 协议游走器（两闸共用）：`domains/hooks/outputcompliance/protocol_text.go:48-98` transformVisibleJSON；collectVisibleText :100-367
- 流式合规暂存：`domains/hooks/outputcompliance/stream_compliance.go:93-159`（1 MiB 上限 :28/:154-157）、帧检查 :362-448、终态释放 :178-230
- **链序**：`cmd/gateway/goal_control.go:560-586`——最终序 = output_sensitive(前) → sanitize_restore → output_compliance(后)；chain 顺序执行且前者 ModifiedBody 喂后者（`domains/hooks/response/chain.go:52-118`）

## 2. 九载体清单与覆盖矩阵

R30 提交信息（`git show 777db9858`）钉死口径："R29 六载体+本轮三 call 侧共9载体"。对应钉测 `security/sanitize/native_restore_test.go:214-267`：

| # | 载体（item.字段 lane） | input 脱敏 | restore 还原 | compliance 复检 | 流式 restore |
|---|---|---|---|---|---|
| 1 | local_shell_call_output.output | ✔ input_protocols.go:322-325 | ✔ native_restore.go:191-196 | **✘** | **✘** |
| 2 | apply_patch_call_output.output | ✔ :326-328 | ✔ :191-196 | **✘** | **✘** |
| 3 | shell_call_output.outputs[].text | ✔ :329-332 | ✔ :197-211 | **✘** | **✘** |
| 4 | code_interpreter_call.code | ✔ :333-336 | ✔ :197-211 | **✘** | **✘** |
| 5 | code_interpreter_call.outputs[].logs | ✔ :333-336 | ✔ :197-211 | **✘** | **✘** |
| 6 | apply_patch_call.action.content | ✔ :615-648 | ✔ :212-219 | **✘** | **✘** |
| 7 | shell_call.action 子树 | ✔ :654-676 | ✔ :220-233 | **✘** | **✘** |
| 8 | local_shell_call.action 子树 | ✔ :654-676 | ✔ :220-233 | **✘** | **✘** |
| 9 | computer_call.action 子树 | ✔ :654-676 | ✔ :220-233 | **✘** | **✘** |

（流式列的 ✘ 即 #6：`smart_sani_guard.go:1289-1316` switch 无 output_item.done/completed 快照分支。）

**#5 缺口比"九载体"更大**：同表对 R28 三载体同样成立——mcp_call.arguments+output（restore native_restore.go:157-167）、web_search_call.action.query（:168-175）、file_search_call.queries+results（:176-190）也全都不在 compliance 游走器内。对照面：addOutputItem（protocol_text.go:212-222）只认 message/function_call/custom_tool_call/reasoning 四型；流式 added/done（:326-360）同四型。

**精确缺口位置与明文流向**：
1. 模型把新造敏感值（非占位符）写进工具载体 lane → restore 无 marker 不动（`RestoreOutputOrMask` 只认 `{SENSITIVE:}`，sanitizer.go:260-280）→ 强制闸游走器不访该 lane → owner 闸还原后复检用同一份窄游走器 → 直过客户端。
2. 流式同构：`stream_compliance.go:411` 用 `collectVisibleText(root,true)`，output_item.done → addOutputItem → 同样漏。
3. 对比：message 文本 lane 的还原后明文**会**被 owner 闸复检（interceptor.go:186-256）——这就是"还原后不被合规复检"的差异面。

## 3. #5 扩容方案（diff 级）→【已由主代理落地，见轮文档 §二#8】

最小侵入接缝：只扩 `collectVisibleText`/`addOutputItem`，不改任何函数签名。两个闸和非流式/流式全部经由这一处游走器，一处扩容四处生效。复检同源（一份 walker 服务两闸）、检查器各自独立、失败方向全沿用现有语义、指标零新增 series（决策自动落入现有 `output_gate_decisions_total` 闭集，metrics/output_gate_metrics.go:23-26/39-54/56-73；接线 interceptor.go:218-254 与 stream_compliance.go:197/:375/:428）。

## 4. #7 边缘载体收口方案 →【已由主代理落地，见轮文档 §二#7】

三处现状：仅 `input_protocols.go:350` 注释提及；input 侧落 default 直通分支（:346-352），restore 侧落 native_restore.go switch 之外。
- input：switch 加 `case "tool_search_call", "mcp_approval_request": field = "arguments"`（与 function_call 同构）；web_search action.results 扩 sanitizeWebSearchCallItem（query 之后，形态口径与 file_search_call.results 同族：缺失/null 直通、非数组拒单、字符串元素直洗/对象洗 .text）。
- restore：`case "tool_search_call"` → restoreNativeArgumentField(requireJSON=true，镜像 function_call)；`case "mcp_approval_request"` → requireJSON=false（镜像 mcp_call.arguments）；web_search_call case 扩 action["results"] 走 restoreNativeNestedValue。

## 5. 风险评估

1. **不会恶化 #6 非对称**：扩容落点在两闸共用的游走器，流式经 added/done 镜像自动同步覆盖；扩容不触碰 restore，#6 原样保留，须独立立项。
2. **与 marker 守卫 fail-closed 无冲突**：强制闸新访的 lane 在还原前是占位符文本——精确 marker 命中 isMarker 豁免（output_sensitive.go:42/:62/:74）；credential 叶按 input 侧契约必为整值占位符（input_tools.go:157-175）。唯一新增 block 类：模型把 credential 叶 marker 包进更长文本（非精确 marker）→ credential 键名硬阻断——与 tool_use.input 今日策略一致且自此有曲线，应登记为已知语义收紧。
3. **流式/非流式拦截方向一致性**：owner 闸对标量 lane 是 redact/observe 不是 block；强制闸 mask/block 由 env 决定，流式经合规暂存同样生效。
4. 次要：游走器扩容增加逐 lane detector 调用，regex 有界；transformVisibleJSON 重序列化（map 键排序）是既有行为非新增。

## 二、核实为健康的面

- `go build ./...` rc=0；`security/sanitize`、`hooks/outputcompliance`、`outputcompliance` 三包测试全绿。
- R30 双侧钉测确实承重：input 八形态口径钉 + restore 9 载体逐值 Equal。
- 链序契约（guard→restore→compliance）在 goal_control.go:560-586 显式重组并有注释锚；chain 对 FailClosed 拦截器错误 fail-closed（chain.go:63-73）。
- 还原后 marker 残留守卫完备：native 形态残留 marker 整体 block（smart_sani_guard.go:810-825）、malformed block、restore 后再验（:839-846）、未知信封 mask 兜底（:941-975）。
- Anthropic messages 侧 compliance/restore 对齐良好，无明显 #5 型缺口。
- 占位符伪造观测：validatePlaceholders → SanitizePlaceholderTamperingTotal（smart_sani_guard.go:749-761）。

## 三、未覆盖项与原因

1. **三个边缘载体的真实上游 schema 未验证**：仓内唯一出处是注释，无现网样本；形态从 function_call/mcp_call/file_search_call 同族推断，钉测按该口径钉死。
2. **#6 立项细节只摸了结构未出方案**（流式九载体还原需在 restoreStreamResponsesDelta 增加 output_item.added/done + response.completed 快照分支并引入按 item 的 carry 状态）。
3. `domains/outputcompliance/checker.go`（848 行）内部策略逻辑只读到接口层。
4. anthropic_bridge.go / responses_bridge.go 的转换事件枚举未逐事件核对；桥内若产出游走器不识别的新事件类型属潜在同类缺口，未穷举。
5. 扩容的时延影响未测量（仅静态论证有界）。
