# 会话日志：消除静默数据丢弃（H1-H3 / M1-M8）

- **日期**：2026-07-26
- **基线**：`826921bd`
- **前序**：`a6cdb2a7`（request_body 落 NULL 根因修复）、`408e1820`（同类缺陷）

## 1. 需求

在前序修复（telemetry sanitize 丢弃合法 JSON）之后，用户要求：
1. 修正同类缺陷扫描发现的全部静默丢弃点；
2. 将这些错误记录到错误日志，供后期检查；
3. 将异常数据记录到 `https://llm.kxpms.cn/format-anomalies` 对应的数据中。

## 2. 缺陷类别

统一为一个缺陷类：**在降级/兜底路径上丢弃用户或上游数据，且不留任何日志或指标**。丢弃后的 NULL/空值与"本来就没有该数据"同形，因此在生产中不可见——这正是 `request_body` 丢失问题跨越 6 次修复仍未被发现的原因。

## 3. 修改的功能点

### 高危（丢的是请求/响应正文）

| 编号 | 位置 | 丢失内容 | 处理 |
|-----|------|---------|------|
| H1 | `domains/streaming/handler.go:2196` | `tools` 数组。`_tools_cached` 还原失败时，请求**带着占位符发给上游**且不含 tools；"模型不能调工具"与"模型选择不调"完全同形 | 三个失败分支全部记录 `tools_restore_failed`(high) |
| H2 | `request_log_pipeline.go` `EnsureCaptured` + `responses.go` + `messages.go` | 读超时/超限时 `readRequestBody` 返回**部分** body，且被装回 `r.Body`——截断的 prompt 既落库又转发上游 | 记录 `request_body_truncated`，按 `body_too_large`/`read_timeout`/`client_canceled` 分级 |
| H3 | `admin/session_detail_v2.go`（6 处）、`session_summary_v2.go`（2 处） | 存储的 body 解码失败 → 字段为 null，与"该轮次未存 body"同形 | 抽出 `decodeStoredJSON` 统一记录 |

### 中危

| 编号 | 位置 | 处理 |
|-----|------|------|
| M1 | `RecordFix` | 解码失败会**覆盖**此前全部 fix actions；两处均记录 |
| M2 | `mergeCompressionMetaV3` | 解码失败时并入空 map 会**删除** v7 字段。改为返回 `(结果, error)`，失败时**原样保留** existing |
| M3 | `auto_decision` marshal 失败 | 记录（否则 `is_auto_request=true` 配 NULL，与旧行同形）|
| M4 | `routing_attempts` | 记录。`ToJSONBytes` 合法返回 `(nil,nil)`，原写法使错误分支被完全掩盖 |
| M5 | `attachmentsJSON` | 改为返回 `(结果, error)`——原 `nil` 与"无附件"同形 |
| M6 | `executors/executor.go`（3 处）、`candidate_failure_logger.go`（2 处） | **字节**切片截断劈开多字节字符 → 非法 UTF-8 → PG SQLSTATE 22021 **丢弃整行**。这是 2026-06-11 事故的重现，仓库内已有 `truncateText` 记录该事故但这些调用点未使用。新增 `executors.truncateUTF8` 并全部改用 |
| M7 | `extractFirstUserMessage` | body 不可解析 → hook 收到空 content → **审计降级 Pass**。改为返回 `(text, parsed)`，旁路记录为 `body_decode_failed`(high) |
| M8 | `admin/message_display.go`（2 处） | 解码失败且无 preview 时，轮次渲染成「没有内容」。补日志 |

### 记录通道

新增 `domains/streaming/data_loss_anomaly.go`：
- 定义 4 个 anomaly_type：`tools_restore_failed` / `request_body_truncated` / `body_decode_failed` / `metadata_dropped`
- `ChatHandler.recordDataLoss` 同时写 `slog.Warn`（错误日志）与 `RecordDataAnomaly` → `response_format_anomalies` 表（即 `/format-anomalies` 页面数据源）
- `recorder` 为 nil 时仍打日志，未接线的部署也能看到丢失
- 使用 `context.WithoutCancel`：客户端断连正是截断高发场景，请求 ctx 已取消时仍须记录
- **只记尺寸与原因，绝不记内容**——这些字段承载用户 prompt

## 4. 影响分析

- **admin 为只读路径**，无 anomaly recorder，按既有约定仅打日志（H3/M8）。
- `mergeCompressionMetaV3` / `attachmentsJSON` / `extractFirstUserMessage` 三个函数改了签名，均为包内私有，调用点已全量搜索确认封闭。
- M6 修正后，超限截断不再产生非法 UTF-8，**间接减少 request_logs 整行丢失**。
- 新增 anomaly_type 会使 `/format-anomalies` 页面出现新类别数据，属预期。

## 5. 验证

| 项 | 结果 |
|----|------|
| 端到端：模拟 `RecordDataAnomaly` 写入 + 页面实际查询语句读取 | 通过（真实 PG，事务内验证后 ROLLBACK）|
| 新测试跑在旧代码上 | **编译失败**（4 处签名断言），证明确实锁住了行为变更 |
| 旧字节截断产生非法 UTF-8 | **已在旧代码上复现**（1024%3≠0 切在 rune 中间；preview 200 同样）|
| `truncateUTF8` 不变量 | 5 种输入 × 15 个边界（含 200/320/1024 真实上限）——始终合法 UTF-8、不超限、是原串前缀 |
| `go build ./...` | 通过 |
| `domains/streaming`、`executors`、`admin` 测试 | 全部通过 |
| `go vet` | 干净 |

**已知失败（非本次引入）**：`domains/credential` 的 `TestLimiter_GetPressure_AfterRelease` —— 已在**干净 main 上复现同样失败**。同理 4 个 `gofmt` 未格式化文件（`session_detail_v2.go`、`session_summary_v2.go`、`handler.go`、`candidate_failure_logger.go`）在干净 main 上即如此，未顺手修改以免扩大范围。

## 6. 审计

- **门禁**：仓库无 `.acc-session-policy`，非 opt-in。
- **audit.degraded**：`true` —— 并行 sub-agent 双轴审查在前序会话返回 "Upstream access forbidden"，本次沿用轻量自审。
- **范围控制**：`web/public/menu-config.json`（既存时间戳改动）与 `docs/superpowers/plans/2026-07-26-provider-profile-*.md`（他处产生的未跟踪文件）**均未纳入提交**。

## 7. 修复中发现的自身缺陷

第一版 `TestTruncateUTF8_NeverSplitsRunes` 用 200 个「中」（600 字节）配 1024 上限——**根本不会触发截断**，是一个恒真测试。改用 400 个「中」（1200 字节）后才真正覆盖跨界场景，并补 `TestTruncateUTF8_FixesByteSliceRegression` 断言 fixture 仍能复现原 bug。这正是规范 §2.4「没在旧代码上失败过的回归测试不是回归测试」要防的情形。

**判定：GO**
