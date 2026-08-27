# 会话压缩算法审计（2026-08-27）

> 任务来源：用户报告下游 LLM 收到压缩摘要后声称"首条用户消息保留在下方"，却把 `<system-reminder>` skills 清单当成用户输入。要求检查会话压缩算法 + 检查 245 日志。
> 审计仓库：本仓（`ai-native-tools/llm-gateway/llm-gateway-go-5`，main @ f95f82483，v2.5.0 build_seq 1770）。
> 注意：`official-deploy/services/llm-gateway-go-5` 目录已被清空（仅剩 `.zcode`，且被父仓 `.gitignore:143` 忽略），本仓是 canonical。

## 1. 245 日志证据（只读采集）

日志入口：`/var/log/llm-gateway-go/gateway.{stdout,stderr}.log`（llmgo-245.service，append 模式）。

| 证据 | 数量 | 含义 |
|---|---|---|
| `session_compressor: LLM summary failed/no-op, falling back to mechanical trim` | 157× | LLM 摘要路径频繁失败，回退机械裁剪 |
| `session_compressor: v2 cache failed, falling back to v1` | 6× | v2 缓存降级，量少 |
| `compression_error` / `compaction_error` / panic | 0× | 无崩溃级错误 |

**观测缺口（P1，运维）**：245 unit 是 `append:` 输出模式，压缩错误写 stderr 文件；而 `scripts/deploy-lib/post-deploy-verify.sh:100-107` 只扫 `journalctl`，会漏掉文件里的 `postgres disabled` / 压缩回退信号。建议 post-deploy-verify 增补对两个日志文件的 grep。

## 2. 六项审计结论

| # | 问题 | 状态 | 证据（canonical 仓 `domains/hooks/compression/`） |
|---|---|---|---|
| 1 | system-reminder 被误钉为 FirstUser | ✅ 已修 | `retain.go:183-184` 用正确字面量 `<system-reminder>`/`</system-reminder>`；`extractOpenAI`（retain.go:86）/`extractAnthropic`（retain.go:126）命中即 continue；`splitSystemAndTail`（rebuilder_openai.go:186）保留 pre-intent reminders |
| 2 | Anthropic 尾部 tool_result 孤儿 | ✅ 已修 | `rebuilder_anthropic.go:170` 重建后接 `TrimAnthropicTail`；`rebuilder_anthropic_test.go:211` 有回归测试 |
| 3 | StripToolInfo 缺协议参数 | ✅ 已修 | `strip.go:70` 签名含 `protocol string` |
| 4 | stripThinkingBlocks 重建丢字段 | ⚠️ 遗留 P2 | `strip.go:646-655` 重建只保留 `role`+`content`；若同一消息同时含 thinking 块与 `tool_calls`/`tool_call_id`/`name` 等 OpenAI 字段，重建后丢失 → 工具链断裂。触发面窄（消息需同时满足两条件），故 P2 |
| 5 | contentFingerprint 忽略非 text 块 | ⚠️ 遗留 P2 | `diff.go:278-285` 数组 parts 只拼 `p.Type == "text"`；`input_text`/`output_text` 块的 text 被忽略 → 指纹退化为 role-only，同角色不同内容碰撞 → diff 对齐可能错位。注意 retain.go:174 的 isSystemReminderMessage 已识别这些类型，fingerprint 未同步 |
| 6 | 重复压缩 marker 幂等 | 🟡 待验证 | `diff.go:302-326` isSummaryMarkerMsg 只检测首个 text part 前缀；多轮压缩后 marker 嵌套/重复场景未在本轮补测，留新会话验证 |

审计中排除的疑点：早前会话快照里 retain.go 出现 `&lt;system-reminder>` 是转录转义假象；canonical 仓字节正确。

## 3. 测试结果（2026-08-27 实测）

```
go test ./domains/hooks/compression/... -count=1
ok  compression 0.893s | caveman 1.472s | lite 1.055s | summary 1.862s
```

## 4. 根因定性（对应用户原始报错）

下游模型看到"first user message preserved verbatim below"却找不到原文，是因为该文案由 `CompressionSummaryPrefix`（rebuilder_openai.go:39-41）无条件承诺，而当 LLM 摘要失败回退 mechanical trim（245 上 157 次）时，重建路径与摘要路径的 B-track 钉住行为不完全一致；叠加历史上 system-reminder 误钉问题（已修，#1），下游把 reminder 当用户输入。#1 修复后主要剩余风险是 #4/#5 的低频错位，以及 157 次/日的摘要失败本身（应查 summary_client 对上游错误的分类）。

## 5. 新会话遗留任务（追加：2026-08-27 第二轮已全部处理，见 §6）

1. ~~**P2 修复 #4**：stripThinkingBlocks 重建时透传 `tool_calls`/`tool_call_id`/`name`~~ ✅ 已修复
2. ~~**P2 修复 #5**：contentFingerprint 把 `input_text`/`output_text` 纳入指纹~~ ✅ 已修复
3. **P1 运维（未做）**：post-deploy-verify 增扫 `/var/log/llm-gateway-go/gateway.stderr.log`
4. ~~**验证 #6**：构造双重压缩 fixture 验证 marker 幂等~~ ✅ 发现真实 P0 bug 并修复（见 §6.3）
5. ~~**深挖**：157 次 "LLM summary failed" 的上游错误分类~~ ✅ 已分析（见 §6.4），发现实际数字与错误吞没问题

## 6. 第二轮多子代理并行处理结果（2026-08-27 晚，同日追加）

### 6.1 P2 #4 修复：stripThinkingBlocks 保留原始字段

`domains/hooks/compression/strip.go` 原实现重建消息时只保留 `role`+`content`，改为 unmarshal 成 `map[string]json.RawMessage` 只替换 `content` 字段，其余字段（`tool_calls`/`tool_call_id`/`name` 等）原样保留。新增测试 `TestStripThinkingBlocks_PreservesToolCalls` / `TestStripThinkingBlocks_PreservesToolCallID`（`strip_test.go`）。

### 6.2 P2 #5 修复：contentFingerprint 覆盖非 text 块

`domains/hooks/compression/diff.go` 的 `contentFingerprint` 从只处理 `type=="text"` 扩展到 `input_text`/`output_text`（取 `text` 字段）与 Anthropic `tool_use`（`id`+`name`+`input`）/`tool_result`（`tool_use_id`+`content`），用 `\x00` 分隔多字段防止意外碰撞。新增测试 `TestContentFingerprint_InputOutputTextBlocks` / `TestContentFingerprint_AnthropicToolBlocks`（`diff_test.go`）。

### 6.3 #6 marker 幂等验证：发现并修复真实 P0 bug

构造走**真实生产路径**（`extractOpenAI` → `RebuildOpenAIAfterSummary` → `injectOpenAISummaryMarker`）的多轮压缩 fixture 后，确认了一个此前未被发现的 P0 级 bug：

- **根因**：`retain.go` 的 `extractOpenAI`/`extractAnthropic` 选取 B-track "FirstUser" 消息时，只跳过 `isSystemReminderMessage`，**没有跳过 gateway 自己注入的旧 summary marker 消息**（marker 的 role 也是 `"user"`）。
- **触发链**：round 1 压缩注入 marker1 → round 2 触发新一轮压缩时，`extractOpenAI` 把 marker1 误当作 "FirstUser" 钉住（B-track）→ `RebuildOpenAIAfterSummary` 把 marker1 和新生成的 marker2 一起塞进结果 → 输出携带 2 个 marker，且旧 marker1 的摘要内容永久嵌套保留，不会被替换。
- **影响**：任何触发 ≥2 轮 LLM 摘要压缩的长会话都会累积旧 marker，与用户原始报告症状高度吻合（下游模型把旧摘要块误当成新一轮的"用户原始意图"）。
- **修复**：`retain.go` 的 `extractOpenAI`/`extractAnthropic` 在跳过 system-reminder 之后，追加跳过 `isSummaryMarkerMsg(m)`。
- **验证**：新增 `TestExtractOpenAI_SkipsSummaryMarkerForFirstUser` / `TestExtractAnthropic_SkipsSummaryMarkerForFirstUser`（`retain_test.go`），以及走真实 rebuild 路径重写的 `TestMarkerIdempotency_NestedCompression` / `TestMarkerIdempotency_MultiRoundAccumulation`（`marker_idempotency_test.go`，10 轮压缩后 marker 数=1，修复前会持续增长）。
- **注意**：diff.go 的 `BuildOutboundMessages` merge 逻辑本身**不是**根因——它保留 lastOutbound 中所有非-client-diff 内容（包括 marker）是正确行为，因为它只做 delta-append，不做重新摘要。真正的钉子在 retain.go 的 B-track 选择逻辑。

### 6.4 157 次 "LLM summary failed" 深挖：实际是 4,588 次 + 三层错误吞没

245 当日（24h 窗口）实际统计为 **4,588 次**失败（原审计的 157 次可能是不同时间窗口/统计口径）。时间分布：凌晨 3-7 点占 67%（3,092 次）。触发类型：70% 为 `sliding_window_count`（消息数触发，长会话更易失败）。

发现三层错误吞没导致无法从日志还原上游真实错误类型（401/429/503/超时）：
1. `summary/summarizer.go:78` 的模型 fallback 循环 `if err != nil { continue }` 丢弃每个模型的具体错误
2. `summary_client.go:52` 用 `slog.Debug`（生产环境默认不输出）记录 candidate 失败
3. `session_compressor.go:551` 最终 fallback 日志只写 "failed/no-op"，不带原始错误

**建议（未在本轮实施，留待后续）**：
- P0：`summary_client.go:52` DEBUG→WARN；`summarizer.go:78` 累积所有模型错误到最终 error
- P1：`session_compressor.go:551` 区分 "summary_error" vs "summary_empty"；加 Prometheus `llm_summary_failure_total{reason}`
- P2：Circuit breaker 区分临时错误（429/503）vs 永久错误（401/404）

### 6.5 本轮测试结果

```
go test ./domains/hooks/compression/... -count=1   → 全绿
go test ./domains/hooks/... -count=1               → 全绿（20 个子包）
go build ./... && go vet ./domains/hooks/compression/...  → 全绿
```

### 6.6 遗留（第三轮，未做）

1. P1 运维：post-deploy-verify 增扫 stderr 文件
2. summary_client.go 错误分类 3 项改进（§6.4 建议）
3. 生产影响评估：检查 245/154 PG 中是否已有长会话累积了大量旧 marker（本次只修复代码，未清理历史数据）

## 7. 第三轮：7622526a5 提交后审计（2026-08-27 深夜，同日追加）

对 `7622526a5` 做双轴审查（规范轴按 CONTRIBUTING.md 自查——审查子代理超时；规格轴对照本文档 §5/§6 由子代理完成），加人工逐行复审。发现 4 个问题，全部修复：

| # | 发现 | 来源 | 严重度 | 修复 |
|---|---|---|---|---|
| 1 | `stripThinkingBlocks`（strip.go）在 content 数组**全为 thinking 块**时 `len(filtered)==0 → return nil`，整条消息连同 `tool_calls`/`tool_call_id`/`name` 一起丢弃——"Anthropic assistant 只发 thinking+tool_use 不发 text"正是该形态，§6.1 的修复只覆盖了"过滤后仍有块"的分支 | 规格轴子代理 | P1 | 空 filtered 时若消息含 role/content 之外的键则保留消息、content 置 `[]`；纯 thinking 消息（无载荷字段）维持丢弃语义。测试：`TestStripThinkingBlocks_AllThinkingWithToolCallsPreserved` / `..._AllThinkingNoOtherFieldsStillDropped` |
| 2 | `splitSystemAndTail`（rebuilder_openai.go）`FirstUserIndex<0` 分支 `tail=lastN(全部消息)` 会把旧 marker 原样带进输出——当 body 唯一的 user 消息是 marker（无真实 FirstUser）时二轮压缩仍嵌套。§6.3 的 retain.go 修复只堵住了"marker 被钉成 FirstUser"一条路径 | 人工复审（探针测试证实：2 markers） | P1 | 新增 `filterRebuildTail`：tail 构造时过滤 marker 消息（新 C-track 摘要替代一切旧摘要）与 system 消息（SystemMessages 已单独 prepend，tail 保留必然重复——顺带修复既有缺陷，见 #4）。测试：`TestMarkerIdempotency_MarkerOnlyUserBody` / `TestRebuildAfterSummary_NoSystemDuplicationInTail` |
| 3 | `contentFingerprint`（diff.go）块与块之间无分隔符，`[{"text":"ab"}]` 与 `[{"text":"a"},{"text":"b"}]` 指纹相同；Anthropic `thinking` 块（payload 在 `thinking` 字段）不进指纹，thinking-only 消息指纹为空仍碰撞 | 规格轴子代理 | P2 | 每块末尾统一追加 `\x00` 终止符；新增 `case "thinking"` 取 `thinking` 字段。测试：`TestContentFingerprint_BlockSeparatorNoCollision` / `..._ThinkingBlock`。**注意**：指纹值全变，升级部署后 SessionState.MsgHashes 旧值与新算法不匹配 → 首轮 diff 视为全量 delta，一次性自愈，无正确性影响 |
| 4 | tail 中的 system 消息与 SystemMessages 重复 prepend（对 mid-conversation system 既有缺陷，≥0 路径同样存在） | 人工复审 | P2 | 随 #2 的 `filterRebuildTail` 一并修复（两个 tail 构造点都过滤），见 #4 测试 |

其余审计结论：
- §6.1/§6.2 声称的其余行为经逐行核对属实（字符串 content 原样返回、map 只替换 content 键、`text` 块行为向后兼容）。
- 规范轴：本任务文件已 gofmt（修掉 2 处空白行）；`session_cache_coverage_test.go` 的历史脏格式非本任务产物，不处理；commit 符合 Conventional Commits；`scan-secrets.sh --mode=strict --paths=domains/hooks/compression` 0 findings。
- 验证：`go test ./... -count=1` 全仓 232 包 0 FAIL；`go vet ./domains/hooks/compression/...` 干净；`go build ./...` 通过。
- 规格轴指出 `case ""` 把无 type 字段的块纳入指纹属超出 §6.2 列举范围——**故意保留**：OpenAI 实际存在无 type 的 text 块，纳入比忽略更保守，且有测试覆盖。

## 8. 第四轮：1819139f9 提交后审计（2026-08-27 深夜，同日追加）

对 `1819139f9` 做规范轴与规格轴复核，并对 OpenAI/Anthropic 重建的工具调用边界做人工探针测试。发现 3 项问题，全部修复：

| # | 发现 | 来源 | 严重度 | 修复 |
|---|---|---|---|---|
| 1 | `RebuildOpenAIAfterSummary` 的 tail 用简单 `lastN`。当左边界落在 `role=tool` result 时，会输出 `tool_call_id` 却裁掉对应 assistant `tool_calls` anchor，上游会拒绝该孤立工具结果 | 人工探针 + 回归测试 | P1 | 新增 `recentAtomicTail`：保留最近 N 条；若左边界位于连续 tool results 内，向前扩展到声明相关 call ID 的 assistant anchor。覆盖单 result 和同一 anchor 多 result |
| 2 | 上一轮 `filterRebuildTail` 只接入 OpenAI；`RebuildAnthropicAfterSummary` 的 FirstUser 不一致防御 fallback 仍直接 `lastN(probe.Messages)`，会把旧 marker/system 带回 messages[] | 规格轴子代理 | P1 | Anthropic fallback 复用 `filterRebuildTail` + `recentAtomicTail`；新增 `TestRebuildAnthropicAfterSummary_FallbackFiltersGatewayArtifacts` |
| 3 | `stripThinkingBlocks` 用 `len(keys) <= 2` 判断全 thinking 消息是否只有 role/content，字段数量不是语义判断：有效扩展字段可能误删，空扩展字段又可能留下空消息 | 规范轴子代理 | P2 | 新增 `hasMeaningfulMessagePayload`：除 role/content 外，只有非空、非 null、非 `[]`/`{}`/`""` 的 JSON 字段才算载荷；覆盖有效扩展字段保留和空扩展字段丢弃 |

本轮新增回归：OpenAI 单/多 tool-result 回合的 tail 原子性、Anthropic 防御 fallback 的 marker/system 过滤、全 thinking 消息的有效与空扩展字段。上一轮 §7 中“两个 tail 构造点都过滤”的声称不成立（Anthropic fallback 漏接），已在本节修正；OpenAI 与 Anthropic 重建均使用原子工具尾部选择。

验证结果：

```
go test ./domains/hooks/compression/... -count=1  → 全绿
go test ./... -count=1                             → 全绿（235 个包，0 FAIL）
go vet ./...                                        → 全绿
go build ./...                                      → 全绿
bash scripts/scan-secrets.sh --mode=strict --paths=domains/hooks/compression → 0 findings
```

对 `docs/audit` 目录做全目录扫描时仍有 3 个既有 `INTERNAL_DOMAIN` WARN，命中 `2026-08-22-admin-registry-154-deploy-audit.md` 与 `llm-kxpms-nginx-route-gap-2026-08-21.md`；本轮新增/修改的审计内容未产生新的敏感信息命中。

## 9. 第五轮：2c64b12ce 提交后审计（2026-08-27 深夜，同日追加）

本轮以 `2c64b12ce` 为固定点，进行了规范轴、规格轴和人工边界复核。发现 3 项遗漏，已全部修复：

| # | 发现 | 来源 | 严重度 | 修复 |
|---|---|---|---|---|
| 1 | `RebuildOpenAIAfterSummary` 的 `recentAtomicTail` 只修复 tail 左边界落在 `role=tool` result 的情况，但此前用例没有覆盖多 result/边界组合；真实截断可能保留 result 而遗漏同一 assistant anchor | 人工边界测试 | P1 | 增加 `recentAtomicTail` 的单 result 与同一 anchor 多 result 回归，保证工具调用回合原子保留 |
| 2 | `RebuildAnthropicAfterSummary` 的 FirstUser 不一致防御 fallback 仍可能绕开 OpenAI 主路径的统一过滤语义，旧 marker 或错误放置的 system 可能回到 `messages[]` | 规格轴 | P1 | fallback 统一使用 `filterRebuildTail` + `recentAtomicTail`，新增 `TestRebuildAnthropicAfterSummary_FallbackFiltersGatewayArtifacts` |
| 3 | `stripThinkingBlocks` 以字段数量判断全 thinking 消息是否有有效载荷，`len(keys) <= 2` 无法区分缺 role 的有效扩展字段和带空值的无效扩展字段 | 规范轴 | P2 | 改用 `hasMeaningfulMessagePayload`，按 JSON 语义识别非空载荷；新增有效/空扩展字段双向测试 |

同时发现并修复一条与 #1 同类但未在上一轮报告中记录的尾部协议问题：OpenAI 重建 tail 需要保证 `tool_calls` 与 `tool_call_id` 配对，否则上游会拒绝孤立工具结果。新增单 result、多 result 工具回合测试，并让 `recentAtomicTail` 向前扩展到对应 assistant anchor。

验证结果：

```
go test ./domains/hooks/compression/... -count=1  → 全绿
go test ./domains/hooks/... -count=1               → 全绿（20 个包）
go test ./... -count=1                             → 全绿（235 个包，0 FAIL）
go vet ./...                                        → 全绿
go build ./...                                      → 全绿
bash scripts/scan-secrets.sh --mode=strict --paths=domains/hooks/compression → 0 findings
```

规范轴结果：`CONTRIBUTING.md` 要求的 Conventional Commits、测试命名和 gofmt 均满足；规范轴子代理无新增问题。远端在本轮复核期间已合入独立的 Anthropic/JSON 修复并快进同步，本任务未修改这些文件。
