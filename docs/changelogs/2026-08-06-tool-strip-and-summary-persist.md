# tool-strip 破坏工具链 + 无条件执行 + summarystore 首次 INSERT 全失败 (P0)

- **日期**: 2026-08-06
- **影响**: agent 会话历史被大规模破坏（生产实测单请求 11.9MB → 356KB，约 97% 数据删除），且生成的摘要无法落库（161/161 persist 全失败），数据不可恢复。
- **优先级**: P0（静默数据丢失，无错误码、无告警、审计记为成功）
- **审计基线**: `87e4a5d5`（`e0ad0638` 父提交）

## 四个已证实缺陷

### 缺陷 1：StripToolInfo 产生孤儿 tool result（strip.go）

`detectToolRounds` 返回扁平 `map[int]bool`，把 assistant.tool_calls 和 tool results 混在一起。`filterMessages` 为保留"最后一轮"，执行 `delete(remove, lastCompletedRoundEnd)`——只恢复删除集合里**索引最大的单条消息**（即最后一条 tool result），其 `assistant.tool_calls` 锚点仍被删。

结果：留下的 tool result 没有匹配的 `tool_call_id`，下游 `SanitizeToolMessages` 将其删除（生产 `removing tool message without tool_call_id` 70 次/请求）。

独立复现：43 条消息 / 20 个合法 tool pair → 4 条 / 1 个孤儿。

### 缺陷 2：StripToolInfo 在窗口判断之前无条件执行（session_compressor.go）

smart/aggressive 模式下，Phase 4a 的 `StripToolInfo` 跑在 Phase 5 `ShouldTriggerWindow` **之前**。即使请求远未超预算，也先永久删除工具历史。设计意图是 strip 与 LLM 摘要配对（tool 输出进摘要 Key References），但摘要在 strip **之后**才尝试，且可能失败。

生产实测：`v4: tool info stripped` 每次约 3000 条 tool 消息被删除，即使大部分请求未触发窗口。

### 缺陷 3：summarystore INSERT 缺 NOT NULL 时间列（store.go）

`session_summaries.first_request_at` 和 `last_request_at` 是 `NOT NULL`（migration 310:13-14）。`summarystore.Upsert` 的 INSERT 语句**未写这两列**，`Summary` 结构也没有这两个字段。

结果：首次 INSERT 必然失败（SQLSTATE 23502）。生产 2 小时内 161 次摘要生成成功，**0 次落库**——所有摘要全部丢失。

这与缺陷 1+2 形成危险组合：strip 先删历史 → 摘要生成成功 → 摘要落库失败 → 无可恢复的摘要。

### 缺陷 4：ToolsRequested 一并禁用了 retry replay（executor.go）

`e0ad0638` 的 `!params.ToolsRequested` 包住了 continuation trim 和 retry replay 两条逻辑。安全理由只针对 destructive trim。retry replay 是缓存命中不改 body，工具会话应保留此能力。

## 修复

### 修复 1+2：结构化 round + 完整性 guard + 执行顺序（strip.go + session_compressor.go）

- `detectToolRounds` 返回 `[]toolRound{anchor, results, complete}` 替代 `map[int]bool`。
- `filterMessages` 原子保留/删除整轮（anchor + 所有 results），保留最近 `keepLastRounds=2` 轮。
- 新增 `toolChainIntact()` 后置 guard：strip 后若任何 tool result 无匹配 anchor，**fail-open 返回原 body**。
- `StripThinkingBlocksOnly()` 独立函数：只剥 thinking block，不碰 tool round，安全无条件执行。
- session_compressor 顺序改为：thinking strip（始终）→ ShouldTriggerWindow → 仅触发时 StripToolInfo。

### 修复 3：summarystore 补时间列（store.go）

- `Summary` 增加 `FirstRequestAt` / `LastRequestAt`。
- INSERT 显式写这两列。
- 零值回退：`FirstRequestAt` 缺省 `LastSummarized`，再缺省 `time.Now()`；`LastRequestAt` 缺省 `time.Now()`。
- UPDATE 分支用 `GREATEST(session_summaries.last_request_at, EXCLUDED.last_request_at)`。

### 修复 4：拆分 trim/retry 豁免（executor.go）

工具会话只跳过 trim，不跳过 retry replay。`isContinue && !isAutoReq && !params.ToolsRequested` 控制 trim；retry 块对所有会话开放。

## 测试

| 测试 | 覆盖 |
|---|---|
| `TestStripToolInfo_NeverOrphansToolResults` | 20 round agent 历史 strip 后 0 孤儿 |
| `TestStripToolInfo_FailOpenOnCorruption` | `toolChainIntact` 检测孤儿 |
| `TestStripToolInfo_PreservesLastRounds` | 末轮 anchor+result 配对保留 |
| `TestStripToolInfo_IncompleteRound` | 不完整 round 必须保留 |
| `TestStripToolInfo_OpenAI_CompletedRound` | 4 round 删最老 |
| `TestDetectToolRounds` | 结构化 round 契约 |
| `TestDetectToolRounds_ParallelCallsAreOneRound` | 并行 calls 计一轮 |
| `TestDetectToolRounds_IncompleteNotComplete` | 缺 result → complete=false |

修正的旧测试（把坏行为固化成预期的）：
- `TestStripToolInfo_PreservesLastRound` → `TestStripToolInfo_PreservesLastRounds`（完整末轮配对）
- `TestStripToolInfo_IncompleteRound`（反转：从"pin 坏行为"到"必须保留"）
- `TestDetectToolRounds`（从扁平 map 到结构化 round）

`go build ./...`、`go vet`、`go test -race ./domains/hooks/compression/... ./domains/streaming/... ./internal/summarystore/... ./internal/ir/... ./domains/transformation/...` 全部通过，无 race。

## 生产预期

修复后：
- smart/aggressive 模式下未触发窗口的请求**不再删工具历史**。
- 触发窗口时只删 complete round，保留末 2 轮，且完整性 guard 保证无孤儿。
- 摘要首次 INSERT 不再因缺 NOT NULL 列失败。
- 工具会话可正常 retry replay。
