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

## 6. 【R88-p 补】我漏掉了白名单的**第三个键** `cut_marker`

R88-o 写测试时只钉了 `safeAlignmentRecords` / `safeSanitizeRefs`，
**漏了 `cut_marker` 所走的 `safeCutMarkerMeta`**——而它同样是 all-or-nothing
（`hook.go:590-592`），**且比前两个多一层跨字段自洽校验**。

**这是我刚交付的那道门自己的漏洞**，已补 `safe_cutmarker_test.go`：

- 6 必填 + 4 可选字段的保留/丢弃；
- **两个跨字段校验**：`cut <= 0`、`system + cut > source`；
  后者是「字段之间说不说得通」而非「字段在不在」，
  是整个 cut_marker 校验里**最有价值也最容易在重构中被悄悄删掉**的一条；
- **反向对照（关键）**：`system + cut == source` 必须**放行**。
  这条专门防「把 `>` 写成 `>=`」的**过度收紧**——那会让**合法**数据被静默丢弃，
  而只测 nil 情况的门**完全测不到**。

**变异验证 3 项全红**（M1 删 `system+cut>source`、M2 `>`→`>=` 过度收紧、
M3 删 `pre_sanitize_offset_range` 越界判断），并按 §9.4 第 1 步确认 M2 是
`safe_cutmarker_test.go:93` 的**断言失败**而非编译失败，`cmp` 逐字节还原。

**教训**：白名单有 N 个分支就要覆盖 N 个。
我按「我上一步写过的两个函数」去写下一步，**没回头数白名单实际有几条**——
这与 R88-i 踩的「拿读到的几个函数代表整个决策面」是**同型的取样错误**，
只是发生在**测试侧**而不是审计侧。

**校正**：§3 说这两个函数「此前零直接单测」仍然成立；
但 §2 提到的 `cut_marker` 在 `hook_test.go` 里有一条**端到端**用例覆盖正常路径，
**不是零覆盖**——本轮补的是它的**校验分支**。

## 7. 本轮未做

- 未改生产代码（**刻意**：改 all-or-nothing 语义会改变 V2 落库内容）；
- 未加计数器/日志（**属运维裁决**，登记见 §3 的 FIXME）；
- 未核实 `maxMetadataRecords`（4096）超限时整段丢弃是否也该可观测——
  形态同上，**未验证**。

## 8. 【R88-q 关闭 §7 第 2 项】查实：**两条生产路径的封顶策略不一致**

### 8.1 V2 的 4096 上限在 chat/messages 车道**不可达**（双重保险）

- V1 `buildOutboundProvenance` 在 `request_log_pipeline.go:1517` 就地封顶
  `maxRecords = 256`，并在截断时打 `alignment_map_truncated` / `sanitize_refs_truncated`；
- V2 `safeAlignmentRecords` 的上限是 `maxMetadataRecords = 4096`（**16 倍余量**）；
- ⇒ 正常路径下 `len(items) <= 256 < 4096`，**V2 那条丢弃分支永远走不到**。

### 8.2 但**存在第二个生产者**，它没有任何封顶 ⇒ 分支可达

`domains/streaming/executors/executor_responses_provenance.go:45`
`recordResponsesInputTrimMeta` 写 `payload["alignment_map"] = alignment`，
数据来自 `compression.BuildResponsesAlignmentMap(before, after)`
（`responses_alignment.go:76` → `buildAlignmentMapWithExtractor`）。
**该函数内部除 nil 输入外无任何 cap/limit。**

产物确实汇入同一个 `compression_meta`：
`executor_chat.go:1684`
`CompressionMeta: mergeCompressionMeta(contextLenRecovery.lastMeta, mergeCompressionMeta(responsesProvenance, preTrimMeta))`
⇒ 进 `request_logs.compression_meta` ⇒ 被 `sessionv2mirror` 读到 ⇒ 进 `safeAlignmentRecords`。

### 8.3 后果（比「静默」多一层）

一个 **input item 数 > 4096 的合法 responses 请求**：

1. V1 这条路径**不截断、不打 `*_truncated` 标记**（标记是 chat 车道的逻辑）；
2. V2 `safeAlignmentRecords` 判 `len(items) > 4096` ⇒ **整段丢弃**；
3. **连 `*_truncated` 标记也一起没了**——因为整个 key 都没进 V2。

⇒ 运维在 V2 上既看不到映射，也看不到「被丢弃了」这个事实，
**两个车道对同一份数据的可观测性完全不同**。

### 8.4 定级与建议（本轮不改）

**P3**：fail-closed 方向没错，且落的是**纯观测**数据（不参与路由、不影响客户端观感）。
但**不对称本身是可修的**：让 responses 车道沿用 chat 车道的 256 封顶 +
`*_truncated` 标记，即可同时做到 ① V2 的 4096 分支变回不可达
② 标记能随 key 一起进 V2 ③ 顺带约束 JSONB 体积。

**未实施的理由**：改它会改变 `compression_meta` 的**落库内容**（外部可查的
JSONB 字段），属读数变更 ⇒ 登记**待裁决第 33 条**，主代理不擅自动手。

**明确未验证**：本轮**未测量**真实的 responses 请求 input item 数分布，
因此**「>4096 实际发生率」未知**。§8.3 描述的是**可达性**，
不是**已发生的事实**——按 `conventions.md` §9.2，不得把可达性直接说成影响面。

## 9. 【R88-r 补上 §8 的测量】真库实测：分支**从未被走到**

窗口：近 7 天，`request_logs`，只读查询。

### 9.1 先纠正一个我自己的误读

我一度以为「responses 车道最大值也恰好是 256」说明**存在我没找到的封顶**。
**不是。** 证据：

- `domains/hooks/compression/` 内**没有任何 256 相关常量**
  （`session_cache.go:81` 的 `l1MaxBytes = 256<<20` 与此无关）；
- `buildAlignmentMapWithExtractor` 逐行读过，**每个 `beforeMsgs` 追加一条、无 cap**；
- `extractResponsesInputMessages` 亦无 cap；
- **决定性**：恰好 256 的 **76 行**全部**同时带** `trim_phase` 与
  `alignment_map_truncated`；而**恰好 255 的行数 = 0**。
  若无封顶，自然分布不会在 255 处为空。

⇒ 那 76 行是 **`mergeCompressionMeta` 的「先写入者胜」**让
**chat 车道的封顶值盖过了 responses 车道那份**（chat 侧顺带打了 truncated 标记）。
**不是 responses 路径有封顶**——§8 的读码结论成立。

### 9.2 纯 responses 行的真实分布

剔除上述 76 行合并样本后，**27,998 条**纯 responses 行：

| 量 | 值 |
|---|---|
| `alignment_map` **max** | **245** |
| p99 | **127** |
| **> 256 的行数** | **0** |
| > 4096 的行数 | **0** |

（参照：V2 丢弃阈值 = **4096**，是实测最大值的 **约 17 倍**。）

### 9.3 结论

**§8.3 描述的后果在观察窗口内一次都没有发生。**
R88-q 的 P3 定级**维持**，但**紧迫性下调**：
分支**代码上可达**（已证无封顶），而**运行期未命中**，
且实测最大值距阈值有 17 倍余量。

**这正是 §9.2 那条纪律的兑现**：我在 R88-q 里拒绝把「可达」写成「影响面」，
本轮的测量证明那个克制是对的——若当初按「静默丢数据」上报，
就会是一个**基于可达性、零运行期证据**的 P3。

### 9.4 顺带查出的次级事实

`mergeCompressionMeta` 的「先写入者胜」意味着：当两条车道都产出
`alignment_map` 时，**chat 那份（已封顶、会丢记录）会静默盖掉
responses 那份（未封顶、更完整）**。`alignment_map_truncated` 标记只说明
「发生了截断」，**不说明另一条车道当时有一份不同的视图**。
属**观测口径**问题，不影响正确性 ⇒ 登记为 §8 待裁决第 33 条的补充说明。

### 9.5 本节未做

- 只测了 **7 天**窗口，未做全量（`request_logs` 200 万行）扫描；
- 未区分不同 provider / 协议族（responses 车道按 provider 分布可能差异很大）；
- 只读查询，**未修改任何数据**。



