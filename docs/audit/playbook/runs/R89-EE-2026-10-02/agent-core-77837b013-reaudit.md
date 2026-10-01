# 核心组：77837b013 六修复独立复审（2026-10-02）

复审对象：HEAD 提交 77837b013。全部 6 项逐 hunk 核对，关键脚本直跑实测。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---------|------|---------------|---------|
| F1 | P1/P2 | custom_tool_call_output 同型绕过（修复②残留姊妹洞）：sanitizeResponsesItem 处理了 function_call_output 与 custom_tool_call，但没有 custom_tool_call_output——明文 output 字段落 default 分支整 item 直通。store=false 无状态多轮回放时客户端把上一轮 custom_tool_call_output 原样塞回 input 数组即绕过。custom_tool_call_output 全仓 grep 零命中，而 custom_tool_call 输出侧被明确支持（outputcompliance/protocol_text.go:216、native_restore.go:136） | security/sanitize/input_protocols.go:290-308 | 补 case（本轮已修=A1） |
| F2 | P2 | default 分支对其余输出型 item 类型整体 fail-open：web_search_call(action.query)、file_search_call(queries[]/results[].text)、mcp_call(arguments/output)、item_reference(无害)均直通；响应侧保留这些 item（ir/response_protocols.go:193），无状态回放必以 input 形态回流 | security/sanitize/input_protocols.go:307-308 | 已知类型补 case（本轮已修=A1），未知类型登记待办 |
| F3 | P2 | reasoning_text 恢复侧缺口：输入侧补了 reasoning_text，但 restoreNativeContentBlocks 名单无 reasoning_text → 模型学舌占位符时残留 {SENSITIVE: → 810 行守卫整响应 ShouldBlock（fail-closed 可用性损失）。native_restore_test.go:68 只测 summary 恰好绕开 | security/sanitize/native_restore.go:171 | 补 case+用例（本轮已修=A3） |
| F4 | P3 | content block type:"refusal" 明文直通（恢复侧反而处理 refusal，两侧不对称） | security/sanitize/input_protocols.go:416-418 | 补 case（本轮已修=A1） |
| F5 | P3 | 修复④引用 534:289-307，实际 DO 块 289-306（尾行号差 1） | ssot-decision.md:164 / runner.go:142 | 可不改 |

## 二、六修复逐项判定表

| 修复 | 判定 | 一句话 |
|------|------|--------|
| ① 730 SHA 追认 | ✅ | 直跑 rc=0、149 verified；新旧行并存；canonical↔embeddata cmp 零差异 |
| ② P1 脱敏绕过 | ⚠️ | reasoning 分支真实正确+钉测承重；但同族残留 F1/F2/F3 |
| ③ 执行位 | ✅ | 100755；全 workflows 扫描无第四、第五个同类 |
| ④ SSOT 订正 | ✅ | 与 534 SQL 实际 DO 块逐行吻合（仅尾行号 nit） |
| ⑤ 612 守卫 | ✅(静态) | 断言存在且语义正确；TEST_PG_URL 未设未实跑 |
| ⑥ 注释订正 | ✅ | 纯注释零行为面；两包 vet 干净 |

## 三、核实为健康的面
- verify-migration-checksums 全绿（149 verified / 644 warn-only，追认机制实测有效）
- 流式响应侧 reasoning 恢复完整（smart_sani_guard.go:1293-1295 四事件形态、lane 隔离）
- 修复⑤断言为链内唯一门禁的论证成立；修复⑥无行为面
- TestInputSanitizerProtocolTextAndOpaqueMedia 实跑绿（走 miniredis 无外部依赖）

## 四、未覆盖项与原因
1. 修复⑤实跑（TEST_PG_URL 未设，建一次性库属写操作）
2. 修复②钉测变异验证实跑（需改源文件；以静态推演替代——主会话后续已补真变异）
3. F1/F2 端到端触发实证（需外部 LLM 上游；fail-open 由代码直读确证）
