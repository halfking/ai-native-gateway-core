# D01（IR 生命周期）+D02（协议适配）子代理报告（窗口 643735a28..876302d5e）

> 只读审计，主代理已亲读复核。复核结论见轮文档 §一。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | toolUseNameFromID 新旧双形态启发式双向歧义碰撞：`gemini_call_123_0`（全数字函数名+现行形态）legacy 剥离误执行返回 "0"；`gemini_call_0_get_2`（尾部数字名+旧形态）被 guard 误判为现行形态返回 "0_get"。受影响群体仅格式统一前的在途对话 | internal/ir/serialize_gemini.go:621、:284/:355 消费点、gemini_r34_regression_test.go:97-111（测试缺口） | 补显式决策或歧义日志 |
| 2 | P3 | R34 双面注册注释称 "serializers dedup by ID"，但 SerializeOpenAIResponse 的 message.tool_calls 数组路径不去重：混合形态上游会双发；content 形态无 id 时发射空 id 条目。anthropic（response.go:822-836）与 gemini（:1226-1237）有 dedup | internal/ir/response.go:495-501、:551-578 | 确认混合形态上游是否存在 |
| 3 | P3（既有） | responses 桥流式/非流式对 finish_reason=refusal 终态判定不一致：非流式 refusal→incomplete/content_filter；流式落成 completed 成功终态 | internal/ir/response.go:1050-1060、domains/streaming/responses_bridge.go:296-302/:318-321 | refusal 加入 openaiFinishReasonIsError（**已修 R36**） |
| 4 | P3 | 注释漂移：R34 插入 geminiThoughtMarker 时压掉了 geminiMediaBlock 的 doc 注释 | internal/ir/parse_gemini.go:281-288 | 顺带清账 |

## 二、核实为健康的面
- thought 双形态三解析点一致（parse_gemini.go:174、parse_gemini_stream.go:166-174、response_protocols.go:63-76 同经 geminiThoughtMarker）
- 出向 thought 形态全覆盖：serialize_gemini.go:366-369、response.go:1213、stream.go:1318 三出向点均 `{"text":…,"thought":true}`
- tool id 两侧统一 `gemini_call_<name>_<idx>`（response_protocols.go:95 = parse_gemini.go:230/251）
- refusal 四序列化器全齐（response.go:627-634/:785-789/:1215-1222/:990-993）
- responses_bridge 终态闩锁（R34 hunk）全终端事件走 emit()，写失败返回 errResponsesTerminalWrite，仅 nil 时 MarkTerminalRendered
- anthropic_stream Resumable 口径与 anthropic_bridge 空响应/error 事件分支一致（:535/:503）
- 4552a2021 契约锁测试与生产函数逐字段对得上（handler.go:6317-6339 = handler_test.go:859-986）
- 未知字段保真/Extensions 健康面未被窗口改动破坏

## 三、未覆盖项与原因
- Resumable=true 重试放大的 attempt 预算封顶（归 D04/生存协调域）
- 真机 wire 实证（真实 Gemini thought bool、Qwen content tool_use id）需凭据
- GLOBAL_G2 镜像全链未逐环追（归 D03）
