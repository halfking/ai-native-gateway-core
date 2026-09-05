# 03 — 多轮消息拼接与历史重建

> 主题：客户端多轮 body 如何被识别“新增”、如何与缓存的历史 outbound 合并、如何重建完整历史送上游。本仓库的增量 delta + submit-mode 探测**已优于** omniroute 的“全量重发”假设；主要债务是 V2 读路径被关闭。

## 1. 现状对比

| 维度 | llm-gateway-go | omniroute |
|---|---|---|
| **客户端假设** | 客户端可能发**全量**或**增量**，由 submit-mode 探测决定（`v2/submit_mode_detector.go:63`） | **假设客户端永远全量重发**；无 delta 概念 |
| **新增识别（V1）** | LCS delta-append：SHA256(role+512B content+toolID) 指纹，从尾向前找最后共享 hash，之后为 delta tail（`diff.go:68,227-243`） | 无（全量即全部） |
| **新增识别（V2）** | 增量 delta：每轮只存 `RequestDelta`+`ResponseDelta`（`bodies_writer.go:69-86`） | 无 |
| **历史重建** | V1：`lastOutboundBody + deltaTail` 拼接（`BuildOutboundMessages`）；V2：`OutboundBuilder.BuildFromDeltas`/`BuildFromLatestOutbound`（`outbound_builder.go:49,95`） | 客户端自带；`contextManager.compressContext` 做窗口内裁剪（3 层：tool/image/thinking/purify） |
| **submit-mode 探测** | 5 级优先：header > msg-count 回退 > summary marker > orphan tool_result > LCS overlap(0.7) > first-turn（`submit_mode_detector.go:63-108`） | 无 |
| **生产读路径** | **V2 读被关闭**：`shouldUseV2` 硬编码 `return false`（`session_compressor.go:691`） `SOURCE-VERIFIED`；走 V1 LCS | N/A |
| **消息指纹** | 两套：V1 SHA256(512B)（`diff.go`）vs V2 role+首100字符（`session_writer_v2.go:506-513`） | N/A |
| **工具对完整性** | 剥离前 `toolChainIntact` 预检 fail-open（`strip.go:158-180`） | `fixToolPairs` 事后修补 orphan（`contextManager.ts:608`） |
| **系统提示保持** | `preserveAnthropicSystem` 透传 top-level system（`diff.go:344-359`） | roleNormalizer 折叠/保留（`roleNormalizer.ts:269`） |

## 2. omniroute 的优点（可吸收）

1. **上下文窗口自校正**（`contextWindowResolver.ts:47,125`）：provider 声明窗口 vs catalog 发现值不一致时，以 discovery 为准 pin，24h 定时 reconcile。本仓库 `contextWindow` 来自调用方传入（`Prepare` 参数），无自校正——**可吸收**（避免 provider 虚标窗口导致压缩过晚触发）。
2. **3 层预适配裁剪**（`contextManager.ts:401`）：tool_result 截断(maxChars=2000) → 旧内联图片替换为占位(keep=2) → thinking 块剥离 → 二分搜索 purifyHistory。其中**旧内联图片占位化**对本仓库多模态会话是直接收益（图片 base64 极占 token）。**建议吸收图片裁剪**。
3. **多格式内联图片识别**（`contextManager.ts:121`）：统一识别 OpenAI image_url / AI SDK image / Claude source.base64 / Gemini inlineData 四种 shape。本仓库 IR 有 `image`/`input_audio` block（`ir/types.go:222-266`）但无“按 token 预算丢弃旧图片”逻辑。**建议吸收**。

## 3. 本仓库的优点（保留）

1. **增量 delta 存储**（V2）：omniroute 每轮全量。本仓库 V2 存 delta，磁盘省 60–80%（README 数据）。
2. **submit-mode 5 级探测**：robust，处理 attachment-only 变化、orphan tool_result 等边缘。
3. **`toolChainIntact` 预检** 优于 omniroute 事后 `fixToolPairs`：前者不产生 orphan，后者产生后修。
4. **summary marker 免索引**（`diff.go:292-316`）：压缩 marker 不参与 LCS，避免被当成新消息重复处理。

## 4. 缺口 / 债务

| 编号 | 债务 | 证据 | 影响 |
|---|---|---|---|
| A1 | **V2 读路径被硬编码关闭** | `session_compressor.go:691` `return false` | V2 builder/cache 全实现但生产 dead code；无法享受 delta 重建 |
| A2 | **两套消息指纹不一致** | `diff.go` SHA256(512B) vs `session_writer_v2.go:506-513` role+100字符 | V2 切读后去重语义不一致，可能误判 delta 边界 |
| A3 | **无旧内联图片裁剪** | 无图片占位化逻辑 | 多模态长会话 token 爆炸，压缩触发过晚 |
| A4 | **无上下文窗口自校正** | `Prepare` 参数传入，无 reconcile | provider 虚标窗口 → 压缩时机错 |
| A5 | **V2 `Message` 弱类型**（`Content string`、`ToolCalls []map[string]any`） | `bodies_writer.go:45-51` | 与 `ir.Message`（`[]ContentBlock`）转换有损；多模态/工具块信息丢失 |
| A6 | **V1 摘要从 `request_logs` 读，V2 切后需改读 `session_bodies`** | `summarizer.go:376` | V2 全切后摘要会断，除非重写读取层 |

## 5. 优化建议（带优先级）

### A1（P0→P1）按租户灰度放开 V2 读 `NEW-DESIGN`
- 步骤：① 把 `shouldUseV2` 的硬编码 `return false` 改为真读 `settings.GetTenantBool(tenantID,"sessions_v2_compression_read",false)`（注释已在 `:687`）；② 先在 1–2 个低风险租户开启；③ 监控 `OutboundBuilder.BuildFromDeltas` 重建结果与 V1 LCS 的一致性（diff hash 对比）；④ 全量。
- 前置：A2（指纹统一）、A6（摘要读源切换）必须先做，否则 V2 读出的历史喂给 V1 摘要会断。
- 回滚：flag 关闭即回 V1。
- 验收：灰度租户的压缩触发、token 节省、错误率与 V1 持平 ±5%。

### A2（P0）统一消息指纹 `SOURCE-VERIFIED` → 已实现
把 V2 `messageKey`（原 `role + ":" + 首100字符`，无 tool id、冒号分隔易冲突）升级为与 V1 `msgHash`（`diff.go:227`）一致的 **`sha256(role + \x00 + 首512B content + \x00 + toolCallID)`[:16]**。
- 落点：`domains/session/v2/session_writer_v2.go` `messageKey`。
- 修复点：两条 tool_result 若前 100 字符相同但 `tool_call_id` 不同，旧实现会误判为重复（与 `SanitizeToolMessages` 防的同一类 bug）；新实现区分。
- 验收：见 `TestMessageKey_V1V2Parity`（V2 指纹与 V1 `msgHash` 在等价输入上产出相同前 32 hex）；`TestMessageKey_ToolCallIDDisambiguates`。

### A6（P0）摘要读源抽象 `NEW-DESIGN` → 已实现（接口层 + V2 实现）
在 `summarizer` 与存储之间引入 `MessageSource` 接口（`GetSessionMessages` / `GetMessagesSince`，`domains/sessionsummary/summarizer.go`）。
- **已落地（接口层）**：① 默认实现 `pgRequestLogsSource`（原 `request_logs`/`request_logs_bodies` SQL 原样搬入，行为不变）；② `Summarizer` 新增 `messageSource` 字段，`NewSummarizer` 自动装默认源；③ `SetMessageSource(src)` 注入器（nil 忽略）；④ `getSessionMessages`/`getMessagesSince` 改为委托。测试覆盖：可替换性、nil 忽略、错误透传、默认源类型。
- **已落地（V2 实现）**：`v2SessionBodiesSource`（`message_source_v2.go`）join `gateway.session_bodies` + `gateway.session_turns`（取 `model`），把每轮 `request_delta` 折叠为**最后一条消息**——与 V1 的 `messages->-1` 语义对齐，保证两源喂给摘要器的输入可比。nil-safety / 角色默认 `user` / 不可用 turn 跳过 / 升序 / LIMIT 20 全部与 V1 对齐。测试覆盖：折叠选最后一条、角色默认、不可用 payload 跳过、升序+跳过、nil pool 报错、接口实现编译期保证。
- **未落地（待 A1 灰度时）**：在 `main_pipeline` 里按 `sessions_v2_compression_read` flag 调 `summaryService.SetMessageSource(&v2SessionBodiesSource{pool})`。代码层已就绪，A1 只需翻 flag + 接线一行。

### A5（P1）V2 Message 强类型化 `NEW-DESIGN`
把 `v2.Message` 的 `Content string` / `ToolCalls []map[string]any` 替换为复用 `ir.Message`（或 `[]ir.ContentBlock`）。`sessionv2mirror/hook.go` 的 `toMessage` 改为无损转换。涉及迁移（旧 JSONB 兼容读）。
- 风险：存量 `session_bodies` JSONB 是弱类型格式，需读时兼容。

### A3（P1）旧内联图片按预算裁剪 `NEW-DESIGN`（吸收 omniroute）
在压缩窗口触发前（或 token 估算阶段），识别 IR `image`/`input_audio` block，按 token 预算从最旧开始替换为占位 `"[Earlier image removed to fit context window]"`，保留最新 N=2。依赖 C4（统一 token 估算，含图片 token 数学）。

### A4（P2）上下文窗口自校正 `NEW-DESIGN`（吸收 omniroute）
引入 `contextwindow.Reconciler`：比对 provider catalog 声明窗口与 discovery 探测值，pin discovery 值。定时任务（如 24h）+ 启动时跑一次。`Prepare` 的 `contextWindow` 从 reconciler 取而非调用方硬传。

## 6. 不做（明确非目标）

- **不改“客户端可能全量”的兼容假设**：submit-mode 探测是为兼容 Cursor/RooCode/OpenCode 等不同客户端行为，必须保留。
- **不引入 omniroute 的二分搜索 purifyHistory**：本仓库已有 token/count/idle 三触发 + LCS delta，二分搜索会与之重叠；本仓库策略是“触发即摘要/裁剪”，不是“每次都二分压”。

## 7. 与其他主题的依赖

- A1（V2 读放开）是**整个会话域现代化的总开关**，被 01-M2（统一事实源）、02（压缩读 V2）、04（V2 cache）依赖。
- A2/A6 是 A1 的前置（P0）。
- A3 依赖 02-C4（token 统一）。
