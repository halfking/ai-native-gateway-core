# 2026-10-13 claude-sonnet-5「返回有 tools 但没有成功执行」—— r1022 流式还原吞掉工具参数 JSON 结构字符

> 会话目标：用户报告经本地网关（:8782，host-first）请求 claude-sonnet-5 时
> 「出现 tools 解析错误，返回有 tools，但是没有成功执行」，模型反复重发
> 同一 Bash 调用并自述 "The fetch command's result didn't come through"。
> 已定位根因、修复、变异验证、本地部署 2591 并四组端到端复验通过。

## 一、取证链（怎么定位的）

1. **会话与协议**：网关日志定位到失败 agent 会话（09:20:54–09:22:23，
   remote 192.168.31.33）全部走 **`/v1/responses`**（ZCode 的 AI-SDK
   Responses 客户端），stream 与非 stream 交替——每轮主请求 + 标题/回填请求。
   上游为 vapeur（provider 36 / credential 63，OpenAI chat 线格式），
   `finalizeOpenAIUpstreamBody: legacy IR path validated`，请求成功率 100%，
   **网关侧无任何 tool/sanitize/异常日志**。会话每轮 +2 条消息
   （3→5→7→9），说明客户端确实回传了 function_call/function_call_output，
   但模型仍说「结果没到」。
2. **排除法复现**：新建测试 key 依次复现——单发工具调用（合法）、
   `reasoning` 参数（合法）、手机号遮蔽回显（合法但出现空 delta）、
   多轮 tool_result 回传（合法）、非流式（合法）。基础路径全部干净，
   缺陷是**条件性**的。
3. **客户端解析侧**：解包 ZCode（Vercel AI-SDK openai-responses）：
   每帧过 Zod 校验；**工具调用的 `input` 取自 `response.output_item.done`
   帧的 `arguments` 字符串**，agent 循环对其 `JSON.parse`——参数字符串
   非法 JSON 即「tools 解析错误」，工具不执行，模型下轮重发（用户看到的
   死循环）。`response.function_call_arguments.done`/`output_text.done`/
   `content_part.added` 落入 unknown_chunk 容忍分支，不致错。
4. **触发复现（决定性）**：让模型在命令里输出 `{sensitive_value}` 字样
   （`printf "%s" "{sensitive_value}" && echo ok`），五连跑全部观测到
   客户端侧 delta 序列被改写：

   ```
   deltas: ["{\"command\":", " \"echo \\"", "\"c", "onfig ", "", "",
            "[REDACTED]", " && echo ", "don", "e\"}"]
   ```

   `sensitive_flag} = 1\"` 一段整体消失（含 JSON 转义引号）；多轮观测
   `&&` 被吞 1–2 个字符不等；done 帧与 delta 流遮蔽不一致（一轮保留原文、
   一轮 [REDACTED]）。**吞掉 JSON 结构字符的跨度一旦覆盖收尾引号/大括号，
   拼接即非法 JSON**——这就是「返回有 tools 但没有成功执行」。

## 二、根因

`security/sanitize/smart_sani_guard.go` 的 `restoreStreamStringField`
（三个协议的流式还原共用：openai `tool_calls.arguments`、anthropic
`partial_json`/`input`、responses `function_call_arguments.delta`）有两个
**整帧遮蔽毒分支**：

- 分支 A（restoreText 掩蔽完成）：`object[field] = redactedMarker`；
- 分支 B（保留前缀被续帧证伪，`reservedMarkerStarted` 要求前缀已含完整
  `{sensitive` 词）：`object[field] = redactedMarker`。

两者都把**整段 delta** 换成 `[REDACTED]`。而工具参数 lane 的 delta 是
**JSON 文档的碎片**，marker 样文本（模型自造 `{sensitive_flag}`，或回显
请求侧遮蔽 marker——会话上下文含 IP/邮箱/手机号时必然产生）与结构字符
（`\"`、`}`、`&&`）相邻是常态；分片边界落在其间即吞掉结构字符。

该拦截器**逐会话常开**（`loadMap` 后即使空 SanitizeMap 也跑毒分支判定），
因此无需请求侧真的发生过遮蔽——模型自造的 marker 样文本即可触发。

## 三、修复（提交 25eca54fa，随本地 2591 上线）

JSON 参数 lane（`.arguments` / `.partial_json` / `anthropic.*.input` /
`response.function_call_arguments.*`）改为**跨度级遮蔽**：

- 分支 A：透传 `restored`（restoreText 本就只掩蔽 marker 跨度）；
- 分支 B：`redactedMarker + value`——只遮蔽扣住的前缀，本帧字节原样重发。

散文 lane（普通文本增量）保持历史整段遮蔽语义不变（r244 契约，有控制组
判据钉住）。

## 四、判据与变异验证

`security/sanitize/r1022_tool_args_stream_integrity_test.go`（同层驱动
`InterceptStreamChunk`，真实捕获分片形状）：

| 判据 | 覆盖 |
|---|---|
| `TestR1022ToolArgsContinuationKeepsJSONStructure`（masked/plain 两形状） | 分支 A/B：拼接 JSON 合法 + `echo` 段存活 + marker 词不泄漏 |
| `TestR1022HeldPrefixActuallyWithheld` | 前提在场：前缀确实被扣（防「看不见」假绿） |
| `TestR1022MaskedCompletionKeepsTail` | 分支 A 掩蔽时非 marker 尾部存活 |
| `TestR1022LiveCapturedShapeStaysValid` | 线上真实分片序列回放 |
| `TestR1022ProseLaneStillRedactsWholeDelta` | 控制组：散文 lane 整段遮蔽不回退 |

变异验证：M1（分支 A 回退）→ MaskedCompletionKeepsTail 红；M2（分支 B
回退）→ ContinuationKeepsJSONStructure 红；修复态全绿，恢复后字节级
diff 确认一致。**踩坑记录**：首轮 M2 不红——夹具用 `{sens` 短前缀，
而 `reservedMarkerStarted` 要求完整 `{sensitive` 词才进毒分支；用探针
（shape 3：`{sensitive` + ` stuff"}`）找准触发条件后补齐。

## 五、部署与端到端复验（release 2.5.8.2591，25eca54f，active 8782）

- 从干净 worktree 部署（避开并行会话 18 个未提交文件，r1020 教训）；
  绕 netstat 量具坑走 Tier 1 显式 DSN。双端口凭据冒烟通过。
- E2E 1（原缺陷形状）：`printf "%s" "{sensitive_value}" && echo ok` →
  客户端侧拼接 `{"command": "printf \"%s\" \"[REDACTED]\" && echo ok"}`
  **VALID，`&& echo ok` 完整保留**（marker 仍正确遮蔽）。
- E2E 2（用户原始命令形状）：git fetch/prune/`2>&1`/rev-list 全链完好。
- E2E 3（真实遮蔽回显，手机号）：delta/done 双侧一致且合法。
- E2E 4（多轮回传）：tool_call → tool_result → 模型正确读到结果
  （"Git version 2.49.0 is installed."），死循环闭环解除。
- /v1/messages（Anthropic 协议 partial_json lane）回归合法。
- 测试：`security/...` 全绿、`domains/outputcompliance`、`domains/streaming`
  （152s）绿；`CGO_ENABLED=0 go build ./...`、vet、gofmt 干净。
  （`go build ./cmd/auto-testbench` 挂已知 macOS 27.0 SDK tapi 问题，
  与本改动无关。）

## 六、遗留与登记（未修）

1. **Responses 桥 `output_index` 冲突**：message item 与 function_call
   item 同用 `output_index: 0`（后者取 OpenAI tool_calls index）。当前
   AI-SDK 按 item_id/output_index 分键碰巧不炸，但违反 Responses 规范
   （output_index 应为输出数组位置）。登记待修，未动。
2. **done 帧与 delta 流遮蔽口径**：本轮修复后两侧均跨度级、实测一致；
   但 `restoreStreamResponsesTerminal` 对 output_item.done 内嵌
   function_call.arguments 的 loose-marker 处理与 delta 侧不是同一实现，
   长期应收敛到单一跨度遮蔽器。
3. 生产 154/245/252 未部署本轮修复（本地网关问题）；如生产出现同形状
  （coding-agent 流量 + 大上下文含被遮蔽值），同修复适用。
4. 测试 key id=196 已停用；worktree 与 .env.local 副本已清理。
