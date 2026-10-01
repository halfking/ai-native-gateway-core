# 81 号报告：R88-o V2 provenance 白名单对齐核实 + all-or-nothing 丢弃钉桩

- 日期：2026-10-01
- 轮次：R88-o
- 变更面：**新增 1 个测试文件**（`internal/sessionv2mirror/safe_metadata_test.go`），**零生产代码改动**
- 承接：80 号 §5 自列的「最值得后续补的一步」

---

## 0. 结论先说

1. **对齐核实：干净。** `alignment_map` 8/8 字段、`sanitize_message_refs` 6/6 字段
   在 V1 结构与 V2 白名单之间**完全一致，无静默字段丢失**；
2. **顺带查出一处此前零覆盖的行为**：两个 safe 函数是 **all-or-nothing**——
   任一记录不合法即**整段丢弃**，`return nil`，**不记日志、不打指标、此前无任何测试**；
3. 本轮用「断言当前行为」把它**钉住**（`conventions.md` §9.5「红门不能进主干」），
   **不改生产行为**；可观测性缺口登记为待裁决。

---

## 1. 对齐核实：两侧字段逐一吻合

### 1.1 `alignment_map`

| V1 `compression.AlignmentInfo`（`types.go:54`） | V2 `safeAlignmentRecords`（`hook.go:492`） |
|---|---|
| `original_index` / `compressed_index` / `compressed_into` / `is_compressed` / `hash` | 必填，5/5 ✔ |
| `occurrence` / `target_kind` / `target_space` | 可选，3/3 ✔（`hook.go:517-526`，注释明写系 R35 审计 P2 刻意补的） |

### 1.2 `sanitize_message_refs`

| V1 `compression.SanitizedMessageRef`（`sanitize_info.go:56`） | V2 `safeSanitizeRefs`（`hook.go:551`） |
|---|---|
| `raw_index` / `sanitized_index` / `raw_hash` / `sanitized_hash` / `changed` | 必填，5/5 ✔ |
| `placeholder_count` | 可选 ✔（`:574-576`） |

⇒ **无「V1 新增字段被 V2 静默丢弃」的风险**（就当前结构而言）。

**顺带把 80 号那个命名错位钉实了**：Go 类型真名是 `SanitizedMessageRef`（**无 s**），
`SanitizeInfo.MessageRefs` 才是切片字段。objective 与注释里写的 `SanitizedMessageRefs`
是把「类型名 + 复数」拼在一起的**概念名**——这正是 R88-n 差点误报的根因。

---

## 2. 查出的行为：all-or-nothing，且完全静默

`safeAlignmentRecords`（`:506-508`）与 `safeSanitizeRefs`（`:567-569`）：

```go
if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
    return nil          // 整段丢弃，不是「跳过这一条」
}
```

**三个后果**：

1. **单条坏记录 ⇒ 整段 provenance 从 V2 消失**（不是丢那一条）；
2. **零可观测性**：不 `slog`、不计数、不打指标。生产上无法区分
   「producer 根本没产出」与「V2 把整段都滤掉了」；
3. **此前零直接单测**：`grep 'safeAlignmentRecords\|safeSanitizeRefs' --include=*_test.go`
   **0 命中**；`malformed` / 坏记录相关用例同样 0 命中。

**方向本身是对的**（fail-closed：宁可不镜像也不镜像错数据，且注释解释了
「整体复制会持久化原始载荷或未来的 PII 字段」）。**本轮不改这个决策**——
改成「跳过坏记录」会改变 V2 落库内容，属对外行为变更。

---

## 3. 本轮交付：把它钉住，而不是改它

新增 `internal/sessionv2mirror/safe_metadata_test.go`，两��用例共 11 个子测试：

- **`TestSafeAlignmentRecordsDropsWholeArrayOnOneBadRecord`**
  对照组：全合法 ⇒ 8 个字段（含 3 个可选）全保留；
  变异组：6 种必填字段破坏（类型错/负数/缺失/非整数/hash 越界），
  **坏记录放第二位**（第一条合法）仍须整段变 nil —— 这一条是关键，
  它把「丢坏的那条」与「丢整段」区分开；
  另测**可选字段非法不触发整段丢弃**（optional 走单独的 `if ok`）。
- **`TestSafeSanitizeRefsDropsWholeArrayOnOneBadRecord`**
  同构，覆盖 5 种破坏。

`FIXME(R88-o-1)` 标注：可观测性缺口属运维裁决，**本轮不擅自加计数器**。

---

## 4. 变异验证（2 项 + 反向对照）

| 变异 | 期望 | 实测 |
|---|---|---|
| M0 基线 | 0 | 0 |
| M1 把 `return nil` 改成 `continue`（跳过坏记录） | 1 | **1** |
| M2 丢弃 `occurrence` 可选字段的保留 | 1 | **1** |
| 还原后 | 0 | 0，`cmp` 逐字节一致 |

**并按 §9.4 第 1 步确认「变异生效了吗」**：M1 的失败详情是
`safe_metadata_test.go:82: 坏记录应导致整段丢弃，实际返回 [...]`，
**是断言失败而非编译失败**——门有真实判别力。失败信息自带
「这说明语义已变，本测试需同步更新」，将来真要改语义时，
这道门会明确告诉下一个人「你改的是被钉住的行为」。

---

## 5. 验证

- `gofmt` 干净；
- `go test ./internal/sessionv2mirror/ -count=1` → **ok**（该包现有 27+2 个测试函数）；
- `go build ./...` OK。

## 6. 本轮未做

- 未改生产代码（**刻意**：改 all-or-nothing 语义会改变 V2 落库内容）；
- 未加计数器/日志（**属运维裁决**，登记见 §3 的 FIXME）；
- 未核实 `maxMetadataRecords`（4096）超限时整段丢弃是否也该可观测——
  形态同上，**未验证**。
