# R59 48h 审计轮 — S4 组：三层缓存 provenance (D03) + 自动压缩 (D05) 审计报告

- 审计员：S4（三层缓存 provenance + 自动压缩）
- 日期：2026-09-23
- 仓库：llm-gateway-go-2 @ main `9f7b0ea3f`
- 职责：如实盘点清单项现状（已实现生产 / 半成品 / 仅测试 / 未实现），给证据 file:line，不臆断
- 验证：`go build ./...` 通过；`go test ./domains/transformation/... ./internal/ir/... ./security/sanitize/... ./domains/hooks/compression/... ./domains/session/v2/... ./domains/streaming/... ./internal/sessionv2mirror/...` 全绿

---

## 一、现状盘点表

| # | 清单项 | 状态 | 证据（file:line） |
|---|--------|------|-------------------|
| 1a | 三层缓存 original→sanitized→compressed（SessionState v8 字段） | **已实现生产**（V1 Redis 会话缓存侧） | `domains/hooks/compression/session_cache.go:148-233`：L1 raw（`raw_snapshot/raw_te/raw_mc`）、L2 compressed（`cmp_te/cmp_mc/cmp_ph/cmp_quality`）、L3 sanitized（`sanitize_ref/sanitize_stats/sanitize_message_refs`）；Redis 序列化/回读 `session_cache.go:782-826 / 867-890`（`algn`、`san_ref`、`san_stats`、`san_msg_refs`、`cm_psor0/1`） |
| 1b | 三层完整 identity/occurrence 映射 | **已实现生产（仅压缩层完整）** | AlignmentMap：`domains/hooks/compression/alignment.go:25-82`（`buildAlignmentMapForProtocol`，per-original-message 一条 `AlignmentInfo`，含 `Hash`+`Occurrence` 消重复、`TargetKind`(retained/summary/dropped)+`TargetSpace`）；类型 `domains/hooks/compression/types.go:46-60`。sanitize 层 `SanitizedMessageRef`（`sanitize_info.go:53-63`）为 hash-only 消息级映射（raw_index/sanitized_index/raw_hash/sanitized_hash/changed/placeholder_count），**无 occurrence 字段**（按 index 1:1 对齐，语义上足够但与「完整 occurrence 映射」表述有差距） |
| 1c | 三层一致性校验（threetier 包） | **仅测试/库代码（零生产消费）** | `domains/hooks/compression/threetier/`（doc.go、tiers、align.go `BuildAlignments`/`DetectMisalignment`）。全仓 grep `hooks/compression/threetier` 导入：**包外零引用**（仅自身 `tiers_test.go` 等）。三层 offset 语义校验在生产路径不运行 |
| 1d | sanitize↔compress 层 offset 串接（CutMarker pre-sanitize range） | **已实现生产** | `domains/hooks/compression/cut_marker.go:212`（`pre_sanitize_offset_range` 序列化）、`session_cache.go:952-973`（CutMarker⇄SessionState 互转）、`recovery_coordinator.go:353-397`（V2 metadata 读取）、`session_compressor.go:1352-1359` |
| 2 | SanitizedMessageRefs + AlignmentMap 生产链路接入 V2 metadata | **已实现生产（R35/R36，2026-09-17），其中 sanitize_map_ref 写侧缺席（半成品）** | 写链：`security/sanitize/smart_sani_guard.go:170-194`（middleware 产 messageRefs → `compression.WithSanitizeInfo` ctx）→ `domains/streaming/handler.go:3720-3726`（`buildOutboundProvenance(r, scResult.AlignmentMap)` → `logCtx.OutboundProvenance`）→ `domains/streaming/request_log_pipeline.go:1439-1502`（`buildOutboundProvenance`：window_source + alignment_map + sanitize_message_refs，256 条/128KB 截断）→ `request_log_pipeline.go:1389-1437`（`mergeCompressionMetaV3` 并入 entry.CompressionMeta）→ `internal/sessionv2mirror/hook.go:410-455`（白名单：alignment_map/sanitize_message_refs/window_source/两个 truncated 标志；`sanitize_map_ref` 在 :418-423 有校验白名单）→ sessions_v2 metadata。读链：`domains/session/v2/cache_v2.go:369-393`（`CompressionMetadata`）+ `:755-820`（`applyCompressionMeta`）→ `session_compressor.go:1323-1366`（`loadV2CompressionState`）。**缺口**：request 链路无任何生产者把 `sanitize_map_ref` 写进 entry.CompressionMeta（见 F1） |
| 3 | sanitizer 跨进程 offset 原子预占 | **已实现生产（best-effort 锁，R35 P0-2）** | `security/sanitize/smart_sani_guard.go:221-237`（load→assign→save 临界区，Redis 锁串行化）、`:327-334`（check-and-del 释放脚本）、`:336-379`（`acquireOffsetsLock` SET NX PX 5s + 3×50ms 重试；Redis 错误/竞争超时/token rand 失败均降级 unlocked，Warn 可观测）、`:386-437`（`loadOffsets` + offsets hash 过期后从主 map 字段名恢复）、`:440-501`（`saveMapAndOffsets`：HSet 主 map + 刷 offset hash + TTL 同步 + 706 `session_censors` DB 双写 best-effort）。in-process `stateMu`（:169-171）+ 跨进程 Redis 锁双层。残留风险见 F4 |
| 4a | 超长会话发送前自动压缩 | **已实现生产** | 触发：provider-aware 80% trigger / 60% target（≤1M 窗口封顶 200K）`domains/transformation/ctx_compress.go:26-67`；接线 `domains/streaming/executors/executor_chat.go:2167` 与 `:2305-2308`（OpenAI 路径，`CompressMessagesIfNeeded[WithReserve]`，reserve=max_tokens）、executor_anthropic.go（Anthropic 路径 `CompressAnthropicMessagesIfNeeded`）；v7 策略层 `runCompressionStrategiesWithMeta`（mode=auto_threshold，`ctx_compress.go:554-568` `NeedsCompression`）；会话级窗口触发摘要 `domains/streaming/handler.go:3637`（`sessionCompressor.Prepare`）。preflight 不裁剪（窗口未知），`domains/streaming/preflight_compress.go:3-16` |
| 4b | 供应商返回上下文超长：保持连接、自动压缩并重试、不报错给客户端 | **已实现生产（pre-commit 两条链）+ 按设计保留的拒绝面** | ① executor 分级恢复：`domains/streaming/executors/context_summarize.go:991-1330` `handleContextLengthRecovery`——错误体解析真实上限并回写 `credential_model_bindings.context_window_override`（:1017-1079，仅 primary 模式可信）→ smart window recovery（RecoveryCoord，:1099-1167）→ strategy runner（:1174-1189）→ 机械裁剪 aggressive 60%（:1195-1238）→ Memora（:1247+）→ LLM summary；每档成功经 `OnNodeJump` SSE thinking 事件告知客户端（:1141-1146, :1220-1225）。② 流中 survival：R36 one-shot compress-and-retry，`domains/streaming/survival_coordinator.go:491-497`（决策覆盖）+ `:737-772`（body 半边，`survivalCompressBodyForRetry`）+ `:1021-1050`（仅 messages 形状 body；responses 协议不支持→terminal；一次性预算）。③ 已提交（committed）后溢出仍 terminal/resume_blocked（commit-block 规则不动，:1031-1032）。④ 预算 guard：`domains/streaming/handler.go:2292-2303` + `request_meta.go:34-80`（`gateway.max_prompt_tokens`，默认/上限均 2M，env 可调，forensics #12 现场为 1M=1048576）在 candidate 解析前 413 `prompt_too_large` 拒绝——与 `docs/audit/2026-09-23-stream-error-forensics.md` #12「按设计拒绝（2026-09-17 审计轮已闭环）」及遗留节「#12 prompt_too_large 按设计不放宽」声明一致，本轮核实**确为设计行为而非缺陷**。regression 锚：`domains/streaming/survival_ctxlen_compress_retry_test.go` |
| 5 | 压缩/脱敏数据在 IR 的标记（客户端/上游可区分） | **未实现（IR 层）；wire 层有部分标记** | `internal/ir/types.go:259-269` `Message` 结构无任何 provenance/tier/sanitize 字段；`internal/ir/prompt_compress.go` 的 `CompressMessages` **全仓零生产调用方**（grep 仅包内+自身测试，见 F2）。wire 层可区分性仅靠：`smm_v1:<sha256>` summary marker 文本注入（`session_compressor.go:992` injectSummaryMarker）、`X-Gw-Compression-Degraded` 响应头（`handler.go:3702-3704`）、OnNodeJump SSE thinking、request_logs `compression_meta`（落库可观测，非协议内标记） |

---

## 二、发现清单（按严重度）

### F1（中）`sanitize_map_ref` 在 V2 metadata 写链无生产者，读侧字段恒空
- 证据：mirror 白名单 `internal/sessionv2mirror/hook.go:418-423` 与 V2 读侧 `cache_v2.go:387,771,814-816`、`session_compressor.go:1360-1362` 均支持 `sanitize_map_ref`；但生产链唯一的 metadata 生产者 `buildOutboundProvenance`（`request_log_pipeline.go:1447-1502`）只产出 `window_source` / `alignment_map` / `sanitize_message_refs` / 两个 truncated 标志，不产 `sanitize_map_ref`；全仓 `entry.CompressionMeta` 写入点亦无该键。
- 影响：V1 Redis 侧 `san_ref`（`session_cache.go:818-820`）有写，但 sessions_v2 metadata 的 `sanitize_map_ref` 实际恒空——跨进程经 V2 读回时 L3 引用丢失（`loadV2CompressionState` 拿不到 ref），三层 provenance 在 V2 路径上是 2.5/3 层。

### F2（中）`ir.CompressMessages` 零生产调用，注释误导；IR 无压缩/脱敏标记
- 证据：`internal/ir/prompt_compress.go:80-89` 声称 "production defaults used by the dispatcher's inbound long-context path"，但全仓 grep `CompressMessages(` 无任何包外调用（仅 `prompt_compress_test.go`）。`ir.Message`（`types.go:259-269`）无 provenance 字段。
- 影响：清单项 5 的 IR 侧标记不存在；压缩/脱敏后的消息在 IR 表示与原消息不可区分（协议可区分性仅靠 smm_v1 文本标记与带外 metadata）。死代码（库就绪未接线）。

### F3（中）threetier 包零外部消费者——三层一致性校验不在生产路径运行
- 证据：`domains/hooks/compression/threetier/`（`align.go:24-45` `BuildAlignments`、`:64-107` `DetectMisalignment`）全仓无包外导入（仅自身测试）。
- 影响：包 doc（doc.go）描述的「三层 offset 一致性校验」从不执行；三层各自有字段与写入，但无运行时回归检测（如 compressed tokens > raw tokens 这类回归只能靠该包，而它未接线）。属「仅测试/库代码」。

### F4（低）sanitize offset 锁为 best-effort，三条降级路径重新打开跨进程 race；锁内做全 body sanitize，5s TTL 偏紧
- 证据：`smart_sani_guard.go:346-351`（token rand 失败→unlocked）、`:362-368`（Redis 错误→unlocked）、`:376-378`（竞争 3 次超时→unlocked），降级均为 Warn；临界区 `sanitizeRequestBody`（:229-304）在锁内对所有消息跑检测+替换，大 body（数 MB）可能超 5s 租约，租约过期后另一副本可进入 → last-writer-wins 撞号窗口重现（即 R35 P0-2 描述的事故形态，概率低但存在）。
- 说明：代码注释明确这是有意取舍（不阻塞热路径），非未完成项；登记为已知风险。

### F5（低）survival 压缩重试仅支持 messages 形状 body，窗口按 len/4 粗估；responses 协议直接 terminal
- 证据：`survival_coordinator.go:1037-1050`——`CompressMessagesIfNeededBody(body, len(body)/4)`，非 messages 形状（openai-responses）返回 unchanged → `context_length_compress_retry_failed` terminal；仅一次性预算（:1028 retried 即 false）。
- 影响：流中溢出的 responses 协议请求拿不到压缩重试，与 executor 链（有 `CompressResponsesInputAggressively`，`context_summarize.go:1204-1205`）能力不对称。流式 responses 请求 mid-stream 溢出仍会报错给客户端。

### F6（低）token 估算为全 body 字节/3.5 启发式（含 JSON 框架与 tools 开销）
- 证据：`ctx_compress.go:64-67,481-486`；注释自认保守（over-count，安全方向）。已知设计，不做缺陷计，但意味着 80% 触发点对 tools 重的请求偏晚/偏早不可控。

### F7（info）SanitizedMessageRef 无 occurrence；「完整 identity/occurrence 映射」仅在压缩层成立
- 证据：`sanitize_info.go:56-63` vs `types.go:54-60`（AlignmentInfo 有 `Occurrence`）。sanitize refs 按 index 一一对齐（`smart_sani_guard.go:244-288`，无 PII 的消息也记录 ref 以证明坐标对齐——注释明示该契约），同 hash 重复消息场景 sanitize 层无需 occurrence。满足功能但与清单措辞有出入。

### F8（info）provenance 落库有完整性上限
- 证据：`request_log_pipeline.go:1466-1501`——>256 条截断（保留 `*_truncated` 标志）；>128KB 降级为 counters-only（`window_source`）。超长会话（正是压缩主场景）的完整映射大概率被截断，完整版只在 V1 Redis SessionState（TTL 生命周期内）。

---

## 三、与用户预期差异的核对

- 预期「预算 guard 只拒绝 >1M，压缩重试可能未实现」：**前者证实**（guard 在 admission 阶段拒绝，默认 2M、forensics 现场配 1M；发生在 provider-aware 压缩之前，超预算 body 不获压缩机会——按设计）；**后者已被证伪**——压缩重试已存在且是双链（executor 4xx 分级恢复 + survival 流中 one-shot），R36（2026-09-17）已补齐 KindContextLength 的 survival 预算漏洞并有 wire 级回归测试锚定。
- 「压缩重试未实现」的旧预期对应的状态已闭环：`docs/audit/2026-09-23-stream-error-forensics.md` #12 仅声明 *guard 拒绝面* 按设计保留，并未声明压缩重试缺失。

## 四、结论

三层缓存的三层数据本体（SessionState v8 + Redis 落库 + sanitize↔compress offset 串接）、AlignmentMap/ SanitizedMessageRefs 的生产写入与 V2 metadata 接入（R35/R36）、sanitizer 跨进程 offset 锁（R35 P0-2）、发送前压缩与 4xx/流中压缩重试（R36）均已**在生产链路实现并有测试锚定**。主要残差集中在「读侧有、写侧无」的 `sanitize_map_ref`（F1）、两个未接线的库包（`ir.CompressMessages` F2、`threetier` F3）与 IR 层无 provenance 标记（清单项 5 未实现）。
