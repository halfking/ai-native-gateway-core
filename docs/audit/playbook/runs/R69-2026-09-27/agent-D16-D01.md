# D16+D01 会话身份闭环与 IR 回归 子代理报告（窗口：092ab1b61..908256008）

> 主代理复核结论（R69 收口时回填）：发现#1 实锤（活跃写入者不含 primary_request_id 列）→本轮 F3 修复（upsertSessionSnapshot 补列 + 首值优先冲突臂 + 测试更新）；发现#2 实锤→本轮修复（stripMarkdownNoise + summarizeMessages 接线 + 3 测试）；发现#3 实锤（非流式同协议折叠）→本轮 F4 修复（InternalResponse.StopReason 原生槽 + anthropic/gemini parse 填槽 + serialize 同协议守卫透传 + 6 钉桩）；发现#4 登记不处置（responses failed 需先证实线上形态）；发现#5 留档更正。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **resolveSessionID 的 gw_session_id 反向映射臂依赖 `sessions.primary_request_id`，而该列无热路径 Go 写入者**——唯一 Go 写入点是离线修复工具与 backfill 脚本；活跃管道 `upsertSessionSnapshot` 的 INSERT/ON CONFLICT 不含该列。触发路径：admin 传旧式 `gw_session_id` 查详情，direct miss（sessions 行缺失/聚合滞后）落入反向分支 → `primary_request_id IS NULL` 恒查不中 → 404，尽管 request_logs.gw_session_id 明明有该会话行。窗口内 9785c2398 重写的歧义守卫本身正确，但其数据源对活跃写入数据是空的 | 消费：admin/session_detail_v2.go:491-499；活跃写入者不含该列：domains/session/v2/session_aggregator.go:218-223；仅存写入者：sql/scripts/backfill_sessions_v2.sql:321,340 与 cmd/tools/validate_sessions_v2/repair.go:351,355 | 补活跃写入（upsert 首值优先固化）或收缩反向分支注释/契约 |
| 2 | P3 | **轮次/会话人类可读摘要无"去格式"步骤**：`summarizeMessages` 取首条消息原始文本截 200 runes，markdown（# / ``` / ** 等）原样进入 title/summary/last_request_summary/last_response_summary 四处。触发路径：任意成功请求落 turn → checklist 的"去除格式供人查看"在确定性摘要层未实现 | domains/session/v2/session_writer_v2.go:1004-1025（实现）；四处调用 :511-512, :672-673, :760-761 | summarizeMessages 加轻量去格式，或复核 LLM summarizer prompt 后定性 |
| 3 | P2（待复核） | **非流式响应路径无原生 stop_reason 保留——A#2/R68 同类缺口的"非流式面"**：`InternalResponse` 只有归一化 FinishReason，无 StopReason 字段。anthropic→anthropic 非流式：`pause_turn`/`model_context_window_exceeded` 等原生值被 default→"stop" 折叠后回写为 `end_turn`；gemini→gemini 非流式：RECITATION→content_filter→回写 SAFETY（正是 R68 钉桩禁止的折叠项）。触发路径：客户端非流式（stream=false）同协议请求经 IR 往返 | response.go:42-46（字段定义）；折叠点：response.go:217 + mapAnthropicFinishReason response.go:1135-1149；response_protocols.go:61 + mapGeminiFinishReason parse_gemini_stream.go:271-284；回写：response.go:741,765 与 mapFinishReasonToGemini response.go:1352 | 确认同协议非流式是否总有裸字节透传短路；若确实走 IR，按 A#2/R68 模式补原生字段+同协议透传 |
| 4 | P3（备注） | `mapResponsesStatus` 把上游 `status:"failed"` 折叠为 "stop"：responses 上游若以 200 + failed 终态返回，FinishReason 被归为正常完成 | internal/ir/response_protocols.go:238-247 | 待复核：确认 responses 上游 failed 是否恒走 HTTP 错误通道；是则降级为注释澄清 |
| 5 | 信息 | 任务预期"internal/ir 近 24h 只有 45c02e915"不完全成立：R67 的 96a6629c2 也触碰 internal/ir——但实核仅 parse_ollama.go 注释 1 行，无行为变化 | 96a6629c2 diff：internal/ir/parse_ollama.go:22 | 无需处置，留档更正 |

## 二、核实为健康的面

**1. 五类 ID 闭环链路图（来源→去处）**

- **request_id**：产生 = 客户端 `X-Request-Id`（domains/streaming/handler.go:1760）或网关生成（handler.go:8176）→ logCtx.RequestID → telemetry hook → 存储 `request_logs_hot.request_id`（telemetry/client.go:1226 INSERT；promote upsert :2040）与 `session_turns.request_id`（turn_writer.go:335）→ 消费 admin 详情轮次列表（admin/session_detail_v2.go:114）。
- **attempt_id**：产生 = dispatch 管道 `reserveAttempt` 的 uuid（domains/dispatch/journey.go:74）→ `Observation.Attempt.AttemptID`（observation.go:121，Validate 强制非空 :215）→ 存储 `request_journey.attempt_id`（530_request_journey_contract.sql:22）→ 消费 `/api/admin/quality/attempts`（admin/attempt_quality_api.go:18）。
- **gw_session_id**：产生 = 客户端头（domains/session/middleware.go:31；清洗 request_context.go:233）或网关生成（session_v2.go:14；兜底 handler.go:5801-5803）→ `reqLog.GwSessionID` 成功路径补写（handler.go:5790-5809）→ 存储 `request_logs_hot.gw_session_id`（telemetry/client.go:1226,2201；promote COALESCE :2040）→ 消费 admin 反向解析 + auto-title/summary 触发 guard（handler.go:6395-6418）。**写入点真实存在且每请求终态持续写；发现#1 的缺口在 join 的另一侧（primary_request_id），不在 gw_session_id 写入侧。**
- **session_id（V2 服务端）**：产生 = 与 gw_session_id 同值写入（session_aggregator.go:218-263，带租户守卫）→ 存储 `public.sessions` + `session_turns.session_id` → 消费 admin direct-hit 与 unified turns。
- **SessionPK（sessions.id 数值）**：仅管理面内部消费（admin/session_online.go:252）；对外契约已封死（SessionV2.ID 与 SessionTurnV2.ID 均 json:"-"），契约钉桩 tests/session_identity_contract/ 窗口内新增。

**2. D01 IR 无损回归**：窗口内 internal/ir 改动 = 45c02e915（R68 修复本体）+ 96a6629c2（仅注释）；`go test ./internal/ir/ -run "TestSerializeGemini|TestParseGeminiStreamChunk" -count=1` → ok，R68 五钉桩全绿。

**3. R68 守卫模式落点核验**：parse 侧原生保留（parse_gemini_stream.go:229-234）+ serialize 侧 SourceProtocol 守卫（stream.go:1375-1384）+ Anthropic 同款先行（stream.go:1035-1041, 638-645）；OpenAI 流式直通无损（stream.go:338-339, 409-410）。

**4. admin 旁路与热路径零耦合**：admin 包 importer 全集在 cmd/*，domains/ 与 internal/ 零引用；会话查询端点挂管理 mux（main.go:6874-6878，wrapAdmin），请求热路径不含 admin 包。

**5. D13 窗口修复健康**：claim promoted 臂补齐谓词（telemetry/client.go:2587-2604）与 R6.2 基准一致（client.go:2433-2448）。

**6. 协议对 stop_reason/finish_reason 抽样判定**：openai_chat→openai_chat 无损；anthropic→anthropic 流式无损、非流式有折叠（发现#3）；gemini→gemini 流式无损（R68）、非流式有折叠（发现#3）；responses 双向映射基本对称，仅 failed→stop 折叠点存疑（发现#4）；ollama 折叠 "load"/"unload"→"stop" 为注释声明的有意语义，实际影响面≈0。

## 三、未覆盖项与原因

- 真库实查 sessions.primary_request_id 空置率与 request_logs.gw_session_id 覆盖率——需真库凭据（F3 修复后该数据面自然闭合）。
- responses 流式响应桥 stop 语义逐行核对——窗口外文件。
- LLM auto-summary prompt 是否约束纯文本——本轮只核了确定性摘要层。
- web 前端对 turns title/summary 的消费渲染——web/ 超出窗口。
- 非流式同协议裸透传短路（发现#3 定性前提）——主代理复核确认走 IR（F4 修复即按此定性落地）。
