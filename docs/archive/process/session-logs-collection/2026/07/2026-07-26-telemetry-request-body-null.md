# 会话日志：修复 request_body 静默落库为 NULL

- **日期**：2026-07-26
- **基线**：`bb6f4fa7`
- **分支**：`fix/telemetry-request-body-null` → `main`

## 1. 需求

请求 `6caf29420778e5ab8547f10c2a6315b8` 状态为成功，但「请求消息」为空。用户指出该问题**反复多次，改好又改坏**，要求找到根因并从方法论上解决回归。

## 2. 根因

`request_logs_bodies_hot` 中该行**存在**：`response_body` 有 1538 字节，`request_body` 为 NULL；同时 `request_logs_hot.request_preview` **有值**。preview 与 body 来自同一份 `requestBody` 字节，因此 body 在写入时确实存在——排除了视图缺 hot 表、JOIN 条件、两表拆分、并发时序等历次修复方向（见 §6）。

丢弃点为 `sanitizeUTF8JSON`。commit `36998a96` 引入的 `escapeInvalidEscape` 逐字节扫描时不理解 JSON 转义语义：JSON 中一个字面反斜杠编码为 `\\`，扫描器读到第二个反斜杠后将其当作新转义的开头，把**合法 JSON 改写为非法 JSON**；随后 `truncateToValidJSON` 找不到合法前缀返回 `""`，`sanitizeJSONField` 置 nil，静默落为 SQL NULL。

**原 commit 的前提是反的**：它要防的 `\uZZZZ`（非 hex）在合法 JSON 中不可能出现（`json.Valid` 先行拒绝）；真正触发 22P05 的是 `\u0000`——Go 认为合法、PostgreSQL 拒收。实测旧代码丢弃 4 条合法 body，**且对其声称修复的 `\u0000` 仍报 22P05**。

## 3. 修改的功能点

`domains/hooks/observability/telemetry/client.go`：

1. `escapeInvalidEscape` 改为转义感知——反斜杠恒定消费下一字符，`\\` 整体拷贝，不再被重扫描。
2. `sanitizeUTF8JSON` 增加 `json.Valid` 快速路径：**修复启发式仅作用于本就非法的 JSON**（本质修复）。保留对非法输入的原有兜底。
3. 新增 `neutralizeNullUnicodeEscape`：将真正的 22P05 元凶 `\u0000` 改写为 `\ufffd` 而非丢弃。
4. `sanitizeJSONField` 增加 `field` 参数并在丢弃/截断时 `slog.Warn`（仅记长度，不记内容——该字段承载用户 prompt）。

## 4. 影响分析

- **数据面**：`request_body` / `response_body` / `auto_decision` 不再因合法内容被丢弃。经 `sanitizeRawJSONField` 的另 7 个 JSONB 字段（`outbound_body`、`compression_meta` 等）共用 `sanitizeUTF8JSON`，**同步获得修复**（已用探针测试确认）。
- **生产影响面**：近 6 小时 1080 条中 173 条 `request_body` 为 NULL（其中 168 条 response 正常），7/26 全天丢失率 3.7%；minimax-m2.7 达 26/26。丢失呈突发性，与会话内容含反斜杠相关。
- **兼容性**：`sanitizeJSONField` 为包内私有函数，全部 5 处调用点（含 2 处测试）已更新；仓库内无其他引用。
- **不修复存量**：本次仅止损，已丢失的历史 body 无法恢复。

## 5. 验证

| 项 | 结果 |
|----|------|
| 真实 PostgreSQL `jsonb` cast（7 种真实形态） | 全部通过，含 `\u0000` 用例 |
| 同一检查下的旧代码 | 丢弃 4 条合法 body，且 `\u0000` 仍报 22P05 |
| 新增测试跑在旧代码上 | **失败**（证明是真回归防护） |
| 新增测试跑在新代码上 | 通过 |
| 属性测试（576 组合） | 通过——任意内容经 `json.Marshal` 必产合法 JSON，合法 JSON 必须存活 |
| 模糊测试（20 万随机输入） | 无 panic，无非空非法 JSON 输出 |
| `go build ./...` | 通过 |
| telemetry 包全量测试 | 通过 |
| `gofmt` / `go vet`（改动文件） | 干净 |

**已知失败（非本次引入）**：`domains/credential` 的 `TestLimiter_GetPressure_AfterRelease`、`TestRedisHealthStore_LoadFromRedis` 在**干净 main 上同样失败**，已核实与本次改动无关。同理 `gofmt` 报告的 3 个文件（`context_attrs.go` 等）在干净 main 上即未格式化，未顺手修改以免扩大范围。

## 6. 历次回归记录（同一症状）

`a3dfc1be`、`d86e1ec8`、`4b0e53bd`、`fe4ded9e`、`946e2c46`、`6bc538ae` — 均改读取路径/JOIN/表结构，均未定位到 sanitize 阶段的写入丢弃。

## 7. 审计

- **门禁**：本仓库无 `.acc-session-policy`，非 opt-in；无 `scripts/session-governance.sh` 与 `verify.sh`。
- **audit.degraded**：`true` —— 双轴审查的并行 sub-agent 调用返回 "Upstream access forbidden"，按 skill 要求降级为轻量自审，并在此记录原因。
- **降级自审覆盖**：手写扫描器的索引边界（以 20 万随机输入的模糊测试替代人工推演，含末尾截断转义）、非法输入兜底不弱于旧码、全部调用点更新、日志约定与包内既有 `slog.Warn` 一致（key 命名、不记内容）、无范围蔓延。
- **范围控制**：`web/public/menu-config.json` 为会话开始前既存的无关改动（仅 `exported_at` 时间戳），**未纳入本次提交**。

**判定：GO**
