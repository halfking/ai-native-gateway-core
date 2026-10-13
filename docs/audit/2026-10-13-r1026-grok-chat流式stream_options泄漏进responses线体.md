# r1026 — grok-4.6 chat 流式整流失败：stream_options 泄漏进 openai-responses 线体

> 2026-10-13 下午轮。用户报告本地网关（127.0.0.1:8782）grok-4.6 chat/responses
> 两模式出错。提交：见 git log（r1026）。本地部署 2608 验证通过。

## 一、取证链（全部实测）

1. **复现**：裸 curl chat 流式（不带 stream_options）成功——说明问题在客户端
   附加参数。带 `"stream_options":{"include_usage":true}`（AI-SDK/ZCode 流式
   恒发）→ 503 `No available provider for model 'grok-4.6'. All 1 candidates failed.`。
2. **日志**：`executor failed` × 6（本窗口）+ 8（上一轮转日志），全部
   `[client_bug] upstream 400: Unknown parameter: 'stream_options.include_usage'`，
   时间点 16:34/16:36（用户）+ 17:04（我的复现）。
3. **raw-logs 地面真值**（request_id 13c96a5d…）：
   - client_request（openai-chat）：`{"model":"grok-4.6","stream":true,"stream_options":{"include_usage":true},...}`
   - upstream_request（**openai-responses**）：`{"input":[...],"model":"grok-4.6","stream":true,"stream_options":{"include_usage":true}}`
     —— Responses 线格式根本没有这个字段，vapeur 严格校验直接 400。
   - upstream_response：`Unknown parameter: 'stream_options.include_usage'`（invalid_request_error）。
4. **路由面**：grok-4.6 唯一候选是 provider 36 vapeur（protocol=openai-responses，
   catalog_code 为空 → TargetProvider 走 unknown-relay 哨兵 → dst=DialectUnknown）。
   单候选失败 → 整请求 503，且错误码文案（model_not_found/transient）误导。

## 二、根因

`stream_options` 是 **openai-chat 线格式专属参数**（Responses 流的 usage 恒随
response.completed 返回）。IR 不 parse 它 → 落 Extensions；paramreg 注册为
KindPortable（透传）。两条还原路径都把它写进任意线格式的出站体：

- **pass1**：`internal/ir/extensions_restore.go` 的 `restoreExtensions`（各
  serialize_* 内部）——KindPortable 无条件 ActionRestore；
- **pass2**：`domains/transformation/ir_converter.go` 的
  `restoreExtensionsWithParamreg`（transport 层第二遍）——同逻辑独立实现。

这正是 r1021 教训的同构：**两条路径对同一字段的方言政策必须一致**，只修一条
另一条会写回去（pass1 裁掉、pass2 从 Extensions 重新合并）。

## 三、修复（两层同形守卫）

目标线格式为 Responses / Anthropic / Gemini 时裁剪 `stream_options` 并上报
`ir_protocol_loss`（reason=wire_format_scoped_field）；openai-chat 线格式
（含 unknown-relay 目标，即联界/智码/vapeur-chat 形态）保持透传。

**为何不用 RejectedByUnknown**：它会对 chat 形态的 unknown 中转也裁剪，
客户端显式 `include_usage=false` 会被 executor 的 `injectStreamOptions`
再注入成 true（语义反转，重演 fields.go:8-13 的 stream_options 静默丢失
事故的镜像）。协议级裁剪零爆炸半径。

**usage 契约不损失**：R32（P2-B）的 responses→chat 桥本就从
response.completed 无条件提取 usage 并在终态发 chat usage 帧——实测
裁剪后客户端仍收到 `"usage":{"prompt_tokens":6,"completion_tokens":216,...}`。

注册表（internal/paramreg/registry.go）同步订正来源标注：
Dialects 移除 DialectResponses（该线格式无此字段，标注误导）。

## 四、判据（同层驱动 + 前提在场 + 真实形状回放）

- `internal/ir/r1026_stream_options_wire_scope_test.go`：
  真实 `ParseOpenAI`（请求体取自 raw-logs 捕获的失败请求原文）→ 先断言
  stream_options 落进 Extensions（前提在场，防「看不见」假绿）→
  SerializeResponsesRequest/Anthropic/Gemini 出站体不含 stream_options；
  SerializeOpenAI（含未登记 TargetProvider）保留（过裁剪守卫）。
- `domains/transformation/r1026_stream_options_pass2_test.go`：
  inner mock 刻意返回**不含** stream_options 的 body（模拟 pass1 已裁），
  Extensions 带该字段 → pass2 不得写回（anthropic 目标）；chat 目标保留。
- **变异验证**：M1（pass1 守卫键名变异）两个 Drop 判据红，错误信息正是
  生产泄漏形状；M2（case 加 chat 线过裁剪）Kept 判据红（over-trim）；
  M3（pass2 守卫键名变异）pass2 判据红。恢复后全绿。
- 回归：internal/ir、internal/paramreg、domains/transformation、
  domains/streaming/...（含 executors，152s+44s）全绿。

## 五、E2E（2608，本地 8782，全部通过）

| 用例 | 结果 |
|---|---|
| chat + stream_options + 短提示 | 200，usage 帧在场 |
| chat + stream_options + 样本提示词（完整） | 200，23KB 内容，usage 帧 |
| chat + tools + stream_options | 200，tool_calls 参数 JSON 合法，finish_reason=tool_calls |
| responses + 样本提示词 | 200，response.completed |
| responses + tools | 200，arguments JSON 合法（delta/done/item 三处一致） |
| 稳定性 3 轮交替两腿 | 全绿，无熔断残留 |
| 非流式 chat | 200 + usage |
| raw-logs 出站体抽查（4 条 grok） | 无 stream_options |

## 六、本轮日志排查中登记不动的问题（非请求路径 / 预登记项）

1. **nvidia 系探针模型 503**（nemotron/llama-vision/gpt-oss 等）：
   `provider requires egress proxy but none is available` + circuit open +
   pool closed——模型目录漂移与 egress 代理缺失属 owner 拍板项（AGENTS 已登记），
   非代码缺陷。
2. `routing_analytics_7d` 刷新 duplicate key（23505）：部署前即在，后台
   analytics 刷新语句缺 ON CONFLICT 或并发刷新竞争，不影响请求服务。
3. `ursm_node_snapshot_min is not partitioned`（42P17）：本地库该表形态与
   partition_manager 预期不符，后台噪音。
4. `v1_write_liveness: DEAD`：S4 停写切换后的**设计内**监控输出（v1 写腿
   停写、会话腿在写），不是故障。

## 七、判据纪律增量

无新条目；r1021 的「两路径政策必须一致」教训本轮再次实证（pass1/pass2），
M1-M3 变异验证覆盖两条路径各自的红。
