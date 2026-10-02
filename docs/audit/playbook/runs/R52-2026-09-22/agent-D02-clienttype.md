# D02 协议适配与双向解析 子代理报告（窗口：119981c02..5dc4e9f3b）

> 主代理复核结论（2026-09-22）：#1 与 D01-#1 同根，已修（R52-F1）；#2 已修（R52-F7：body-marker 移入 refreshMeta，fillAttemptMeta 之后、仅覆盖弱名 + memo）；#3 已修（R52-F7：model 前缀须与工具集共现，单信号不判，测试同步重写）；#4 已修（R52-F7：deepseek-coder/deepseek-v3 coding 锚定自述形态）；#5 留档（提交信息失真，见轮文档）；#6 登记顺延（三副本口径统一为低风险重构）；#7 已修（R52-F19：ExtractAgentType + sessionmeta agentRole 补齐）；#8 部分缓解（body-marker memo 一次，重复全 body 扫描消除）；#9 已 RESERVED 标注（R52-F12）。

窗口内 D02 相关改动 = ce85e767a（三层客户端识别链）+ 5dc4e9f3b（IR 字段升级，名义上是"reasoning_content 前置"）。已读：conventions.md、D02 域文档、意图基线 conversion-audit.md、两个 commit 的全部代码 diff 及关联调用链。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line |
|---|---|---|---|
| 1 | P1候选 | MiniMax 私有字段出向泄漏：bot_setting/mask_sensitive_info 从 KindDialectOnly 升级为 KindIRHandled 后原"非 MiniMax 目标即 Drop"守卫失效；serialize_openai.go:159 注释声称的守卫无实现；严格校验上游 400；BotSetting 结构只保留两键其余折叠 | internal/ir/serialize_openai.go:157-176；internal/ir/parse_openai.go:80-82；internal/paramreg/decide.go:92-130；internal/ir/extensions_restore.go:47；internal/paramreg/spec.go:32-35 |
| 2 | P2 | 识别链优先级与文档倒置：主路径 body-marker 先于 header/UA 执行——serveWithExecutor 在 handler.go:1960 首次 EnsureCaptured 时 meta.AgentName 尚为 ""（fillAttemptMeta 要到 :2102 SetKey→refreshMeta 才跑），`c.meta.AgentName == ""` 恒真 → body 结论无条件压过 UA 与 X-Agent-Name。UA=zcode/2.0 + model=deepseek-* 的真实客户端被记成 deepseek-code | domains/streaming/request_log_pipeline.go:581-585；domains/streaming/handler.go:1960,2102；conversion-audit.md:127-134 |
| 3 | P2 | model 前缀与通用工具名把"模型选择"当"客户端身份"：`deepseek-` 前缀即判 deepseek-code（任何 SDK 客户端调 deepseek-chat 误归）；read_file/write_file 等是通用编码工具名；测试将误伤固化为预期（body_client_marker_test.go:72-76）；污染 agent_name 落库面与 Dashboard 过滤（不影响 FpSlot holder） | telemetry/body_client_marker.go:74-78,113-131 |
| 4 | P2候选 | 系统提示词层重新引入裸内容词，违背 2026-09-03 收紧教训：`deepseek-coder`、`deepseek-v3 coding` 无锚定，提及即命中；同文件 :38-49 注释正记录裸 token 误判 Cursor 的历史 | telemetry/request_metadata.go:75-82（对照 :38-49）；消费方 request_meta.go:324-328、sessionmeta/extractor_rules.go:21、sessionsummary/summarizer.go:604 |
| 5 | P2 | 提交信息与实际内容失真（同 D01-#7）：ReasoningDelta/TestParseOpenAIReasoningContent 不存在；reasoning_content 能力预存在 | git show 5dc4e9f3b；internal/ir/response.go:38-40,443-444,556-557,989-995；internal/ir/stream.go:109 |
| 6 | P3 | 三处 UA 匹配口径不统一 + zcode 子串误伤仍在：agent_name 轨道裸 Contains(ua,"zcode")；holder 轨道两份拷贝用 "zcode/"/"zcode-" 锚定但 Contains 下 "azcode/1.0" 仍命中；同一 UA 两轨道结论可不同；ExtractAgentNameFromRequest body-aware 路径无生产调用方 | telemetry/request_metadata.go:377,396；domains/streaming/client_fingerprint.go:38,59-60；domains/streaming/executors/executor.go:1017-1018,1035-1044 |
| 7 | P3 | 注册点遗漏两处：ExtractAgentType 未加 minimax-code/deepseek-code → source_channel 落空；sessionmeta agentRole 缺两客户端 → role=other_agent；summarizer.go:595 注释"16 类"已漂移（现 17） | telemetry/request_metadata.go:459-471；domains/analysis/sessionmeta/extractor_rules.go:199-210 |
| 8 | P3 | 弱识别未决时每请求重复全 body JSON 扫描：EnsureCaptured 6 个调用点均全文 unmarshal，maxBodySize=128MB 下瞬时内存近翻倍；tools 遍历在原始 JSON 上且在 IR 解析前的热路径 | request_log_pipeline.go:581；handler.go:1817,1940,1960,1993,2020,2044；body_client_marker.go:41-49,102-110 |
| 9 | P3 | 死代码：ProtocolOllamaChat 与 CumulativeContent（同 D01-#5） | internal/ir/types.go:37-40；internal/ir/stream.go:53-60 |

## 二、核实为健康的面

- 数据闭环存在：meta.AgentName → enrichRequestLogFromMeta → request_logs_hot.agent_name + request_context_attrs.agent_name；admin 前端消费链（logs.ts/LiveRequestStreamV2.vue/RequestOverviewPanel.vue/useLiveStreamFilters）在位。
- 两份 UA 拷贝同步纪律守住（executor.go:1035-1044 与 client_fingerprint.go:59-70 逐字一致 + 注释互指）。
- 弱名覆盖策略两处镜像逐字一致（shouldOverrideWithBodyMarker / shouldOverrideAgentName）。
- 单次 EnsureCaptured 内 body→system-prompt 顺序与文档一致；语义层不翻掉 body 层结论。
- Prometheus 标签基数防膨胀有钉（Normalize 白名单 + anti-cases）。
- reasoning_content 响应侧双向解析预存在且对称（DeepSeek Code 响应侧透传已具备）。
- repetition_penalty roundtrip 钉桩在位；跨方言泄漏为预存在（KindPortable→ActionRestore）。
- DetectAgentFromSystemPrompt 开销形态预存在且短路合理（仅弱名触发）。

## 三、未覆盖项与原因

- 发现 #1 真实爆炸半径（严格上游 400 还是忽略）需真机实测；strip_request_fields 现网覆盖未查。
- messages.go/responses.go 端点的 EnsureCaptured/SetKey 精确先后未逐行核对（#2 结论钉死在 chat 主路径）。
- admin 前端 agent 维度显示映射未逐文件核完（纯展示层）。
