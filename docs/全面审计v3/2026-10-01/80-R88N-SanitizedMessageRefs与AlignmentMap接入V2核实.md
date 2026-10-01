# 80 号报告：R88-n `SanitizedMessageRefs` / `AlignmentMap` 接入 V2 metadata —— 核实通过，无缺陷

- 日期：2026-10-01
- 轮次：R88-n
- 变更面：**纯文档**，零代码改动
- 来源：objective 同一句的另两项（79 号已核实「跨进程 offset 原子预占」）

---

## 0. 结论先说

**生产链路与 V2 接入都存在，且白名单设计正确。** objective 这条要求**已实现**。

**但本轮我差点误报**，过程值得记（见 §4）。

---

## 1. 生产侧：三处调用，写入 `compression_meta`

`domains/streaming/request_log_pipeline.go:1498` `buildOutboundProvenance(r, alignment)`：

```go
// alignment_map —— 压缩器的 identity/occurrence 映射
prov["alignment_map"] = alignment

// sanitize_message_refs —— sanitizer 的消息级引用
if info, ok := compression.SanitizeInfoFromContext(r.Context()); ok && len(info.MessageRefs) > 0 {
    refs := info.MessageRefs
    prov["sanitize_message_refs"] = refs
}
```

- 三处调用点：`domains/streaming/handler.go:3999`（chat，带 `scResult.AlignmentMap`）、
  `responses.go:272`、��messages.go:262`（后两处 alignment 传 nil，只出 sanitizer 那一半）；
- 产物进 `logCtx.OutboundProvenance`（`request_log_pipeline.go:142`）→ `compression_meta` JSONB；
- 三重保护：单键截断 256 条（`*_truncated` 标记）、整体 128KB 上限、
  超限时**降级为只保留 counters**（`:1541-1547`）。

---

## 2. V2 侧：**白名单**搬运，不整体复制

`internal/sessionv2mirror/hook.go:401` `safeCompressionMeta`，其注释说明了为什么
**不能**整体复制：

> Request-log compression_meta is an extensible JSON object, so copying it wholesale
> would accidentally persist raw payloads or **future PII fields**.

逐键白名单（`:420-432`）：

| 键 | 处理 |
|---|---|
| `cut_marker` | `safeCutMarkerMeta` 过滤，并把 `pre_sanitize_offset_range` 提升到顶层 |
| **`alignment_map`** | `safeAlignmentRecords` |
| **`sanitize_message_refs`** | `safeSanitizeRefs` |
| `sanitize_map_ref` | 校验必须等于 `sanitizeMapRef(tenantID, sessionID)` |
| 其它一切 | **不搬运** |

**这是白名单而非黑名单** —— 将来 `compression_meta` 新增任何字段都**默认不出 V2**，
要进 V2 必须显式改这里。**这个默认值选对了**（fail-closed 而非 fail-open）。

---

## 3. 测试：往里塞毒值，看它出不来

`internal/sessionv2mirror/hook_test.go:318-360` 一道用例同时埋了 5 个 `must-not-mirror`：

- **嵌套**：`cut_marker.summary_text`、`alignment_map[0].raw_content`、`sanitize_message_refs[0].plaintext`
- **顶层**：`request_body`、`pii`

断言分两类，这才是关键：

1. **毒值出不来**：`alignment[0]["raw_content"] != nil || refs[0]["plaintext"] != nil` 即红；
   `forbidden := {"request_body","pii"}` 任一出现在 `CompressionMeta` 即红；
2. **正常数据仍在**：`summary_marker`、`cut_marker.cut_index`、`sanitize_map_ref` 必须保留
   ——**只测「挡住」不测「放过」，白名单就会被改成空实现而门依然绿**。

---

## 4. 【本轮最值得记】我差点误报，原因是**拿注释里的概念名去 grep**

`SanitizedMessageRefs` 全仓只命中 **1 个文件**——`request_log_pipeline.go:1492` 的**注释**。
再搜 snake_case 的 `sanitized_message_refs` → **0 命中**。
两个信号叠加，**差一步就报「该字段从未实现」**。

真读函数体才发现：注释里的 `SanitizedMessageRefs` 是**概念名**，
代码里的实际标识是：

| 注释（概念名） | 代码（真实标识） |
|---|---|
| `SanitizedMessageRefs` | `compression.SanitizeInfo.MessageRefs` / JSON 键 `sanitize_message_refs` |
| `AlignmentMap` | `compression.AlignmentMap` / JSON 键 `alignment_map` |

**这是「假 0 命中」家族的第 10 次，也是新变种**：
前 9 次是「搜错范围 / 模式写错 / 截断」，**这一次是「用了注释里的名字而不是代码里的名字」**。
注释用自然语言描述一个概念，代码用另一套标识——两者不必同名。

**判据**：判定「某字段/结构是否存在」时，
**先读产出它的那段函数体，确认真实标识符，再决定用什么去搜**。
只凭注释里的字面量去搜，**搜的是文档作者的记忆，不是代码**。

---

## 5. 本轮未做

- 未核实 `AlignmentMap` 的**上游**是否覆盖所有压缩策略（只看了
  `session_compressor.go` 有 14 处引用，未逐条追覆盖度）；
- 未核实 V2 侧 `safeAlignmentRecords` / `safeSanitizeRefs` 的字段白名单是否与 V1 侧
  `AlignmentInfo` / `MessageRefs` 的结构**完全对齐**（新增字段会不会被 V2 静默丢弃）——
  **这是本轮最值得后续补的一步**，登记为待办；
- 未做真库/多进程实证（本轮为**结构性核实**：读代码 + 读测试）。
