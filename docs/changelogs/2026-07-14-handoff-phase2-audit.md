# Phase 2 Handoff Audit

日期：2026-07-14
范围：交接文档第 4 节 A1-A10、远端提交 `6bdc19406` 以及 Phase 2A-2D 顺序约束。

## 总结

- `6bdc19406` 已经是 `HEAD` 和 `origin/main`，没有待拉取的后续提交。
- Phase 2A 的 1-7 项已有代码落地，但仍存在契约注释、完整入口覆盖和验收工具不足，不能据此宣称 HTTP/供应商验收完成。
- Phase 2B 被 files.kxpms.cn 上传契约阻塞；当前 Cloudreve v4 只能证明存在上传会话/凭据/完成回调链，不能证明存在网关可调用的通用 `POST /upload`。
- Phase 2C 尚未建立 `request_attachments` 表；历史 `request_logs.request_body` 的 backfill、TTL 和权限策略仍待设计。
- Phase 2D 尚未完成：`provider/client.go` 和 `resolve/resolve.go` 仍会用 `lowerUnique` 污染 `RawModels`，且 `provider/client.go` 仍保留 `lower(ma.raw_name) = lower($1)` 旁路。

## A1-A10

### A1：融合方案与 S17-S28 矩阵

- 结论：两份文档主线一致，均要求 strict 默认、附件 manifest/failover 复用、三层模型名称和 S17-S28。缺项在于验收矩阵要求 HTTP 结果、供应商 body、request_logs、request_attachments、文件系统、指标和重试次数，但现有工具只跑 Go 单测且没有这些字段。
- 文件:行：`docs/会话优化v2/05-融合实施方案-附件与模型契约.md:162-181`；`docs/全方面测试/09-多模态与模型契约场景.md:30-57`；`docs/全方面测试/tools/attachment_audit.py:26-71`
- 是否影响实现顺序：是。先补可运行的本地证据工具，再将其输出与完整 HTTP/供应商/browser 验收明确分层。
- 风险等级：高
- 当前状态：部分完成

### A2：最新提交审计

- 结论：当前 `HEAD`、`origin/main` 和 `origin/HEAD` 均为 `6bdc19406`，其父链包含 Phase 2A 相关提交；没有比该提交更新的远端提交可供先拉取。该提交修正了 SHA256 两级路径、附件状态文档、strict 默认说明，并引入 `canonical_raw_name` 方案语义。
- 文件:行：Git `6bdc19406`；`domains/attachments/storage.go:13-19,276-307`；`docs/会话优化v2/02-多模态能力审计报告.md:86-119`；`docs/会话优化v2/05-融合实施方案-附件与模型契约.md:29-45,134-160`
- 是否影响实现顺序：是。后续审计必须以 `canonical_raw_name`/raw casing 分层为基线，不能按旧 `req_<id>` 或无条件 lower raw 的方案继续实现。
- 风险等级：中
- 当前状态：已完成

### A3：旧/新附件落盘路径迁移点

- 结论：新写入已采用 `YYYY/MM/{aa}/{bb}/{sha256}{ext}`；读取接口仍接受旧相对路径，且路径逃逸校验存在。未发现迁移扫描或旧数据搬迁逻辑，但方案只要求兼容读取，不要求立即搬迁。`SaveBase64Image` 仍将完整解码内容读入内存，注释与方案的“大文件流式”表述不完全一致。
- 文件:行：`domains/attachments/storage.go:243-257,276-327,355-369,571-603`；`domains/attachments/storage_test.go:130-160`
- 是否影响实现顺序：否，兼容读取可先保持；若扩大附件大小或支持多类型，需另立内存上限任务。
- 风险等级：中
- 当前状态：已完成（路径）；部分完成（大文件内存语义）

### A4：extractResult.Failed 可观测性

- 结论：提取器现在把失败附件放入 `Attachments` 并设置 `store_failed`，但调用方是否将失败状态写入日志并由 strict 策略阻断，必须逐入口验证。当前 Chat 入口只展示了提取调用，工具和文档没有证明失败响应、指标和前端状态完整闭环。
- 文件:行：`domains/attachments/extractor.go:41-52,186-214`；`domains/streaming/handler.go:1026-1043`；`domains/streaming/request_log_pipeline.go:79-96,458-502`
- 是否影响实现顺序：是。先完成失败状态传播和 strict 断言，再做供应商引用替换，避免 Phase 2B 放大静默丢附件。
- 风险等级：高
- 当前状态：部分完成

### A5：request_logs.request_body 中 base64 残留路径

- 结论：已存在附件 body 脱敏实现，但必须按 OpenAI Chat、Anthropic Messages、Gemini、embeddings、responses 和 stream/non-stream 分支做路径盘点。当前仓库证据显示 request body 仍是通用日志字段，不能据工具通过推断所有分支均已脱敏；Phase 2C 的历史数据处理也未落地。
- 文件:行：`sql/schema/01-schema.sql:1593`；`domains/streaming/request_log_pipeline.go:458-502`；`domains/streaming/handler.go:1009-1033`；`docs/会话优化v2/05-融合实施方案-附件与模型契约.md:112-120`
- 是否影响实现顺序：是。必须先完成脱敏覆盖矩阵和历史 TTL/权限方案，再新增 `request_attachments` 或 URL 改写。
- 风险等级：高
- 当前状态：部分完成

### A6：压缩/清理旁路

- 结论：Phase 1 已有附件保留相关代码和 Gemini/OpenAI IR 测试，但不能仅以 `hasThinkingBlocks` 作为附件完整性证明。需覆盖 compression、sanitize、resolve、identity 和协议序列化的统一引用计数/哈希校验；当前未找到 S23 的独立回归证据。
- 文件:行：`domains/hooks/compression/strip.go`；`domains/transformation/sanitizer.go`；`internal/ir/serialize_openai.go:577`；`internal/ir/gemini_test.go:54-90`；`docs/会话优化v2/05-融合实施方案-附件与模型契约.md:122-132`
- 是否影响实现顺序：是。S23/S24 应在继续扩展供应商附件引用前完成，避免压缩与 failover 破坏 manifest。
- 风险等级：中高
- 当前状态：部分完成

### A7：strict 与重试/failover 闭环

- 结论：failover body 复用和 strict 状态已有 Phase 2A 代码/测试，但 `storage.go` 的公开注释仍称存储失败“不应阻塞请求转发”，与 strict 默认契约冲突；现有测试还不足以证明 provider A 失败后 B 不重复物理上传、取消轨迹可追溯且不会发送不完整 body。
- 文件:行：`domains/attachments/storage.go:8-11,243-257`；`domains/streaming/handler.go:1009-1043`；`domains/streaming/executors/executor_chat.go`；`domains/streaming/executors/executor_chat_test.go:99-110`；`docs/会话优化v2/05-融合实施方案-附件与模型契约.md:86-96`
- 是否影响实现顺序：是。先补 S22/S24 和修正契约注释，再实现 Phase 2B uploader。
- 风险等级：高
- 当前状态：部分完成

### A8：迁移编号占用

- 结论：`391` 至 `397` 均已占用，不能复用；`392`、`393`、`397` 等均有对应 migration，且已有 down 文件约定。任何新的 Phase 2C/2D migration 应从当前最大编号之后继续，并配套 down.sql。
- 文件:行：`sql/migrations/startup/391_state_table_storage_hardening.sql:1`；`sql/migrations/startup/392_candidate_failure_logs_monthly_partition.sql:1`；`sql/migrations/startup/393_request_logs_hot_partial_index.sql:1`；`sql/migrations/startup/397_runtime_logs_lowercase.sql:1-7`
- 是否影响实现顺序：是。先完成 schema 兼容读策略和 migration 设计，再写新 migration；本轮不新增重复编号。
- 风险等级：高
- 当前状态：已完成（审计约束）

### A9：files.kxpms.cn 内部 API 形态

- 结论：Cloudreve v4 源码显示上传是上传会话、凭据/分片和完成回调链，未提供足以作为网关稳定契约的通用 `POST /upload` 证据。当前不能实现假 uploader；需要网关自有 internal API 或双方确认的 Cloudreve contract，包括认证、multipart schema、响应、TTL 和下载/缩略图语义。
- 文件:行：`/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/pkg/cluster/routes/routes.go:143`；`/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/service/explorer/upload.go`；`/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/pkg/filemanager/manager/upload.go`；`docs/会话优化v2/03-多模态技术方案.md:79-91`
- 是否影响实现顺序：是，硬阻塞 Phase 2B。
- 风险等级：阻塞
- 当前状态：未完成（外部契约待确认）

### A10：browser-use UI 类型覆盖

- 结论：当前前端 Request Logs 相关展示仍以本地 `/api/attachments/...` 读取路径为基线；未实现 Phase 3 的 Cloudreve thumbnail/download contract，也没有本轮图片、video、audio、PDF、通用文件的 browser-use 视频级证据。因此不能宣称 UI 全类型验收。
- 文件:行：`web/src/views/RequestLogsView.vue:1285-1290,1396-1404,1527-1534`；`web/src/views/RequestLogDrawer.vue`；`docs/会话优化v2/05-融合实施方案-附件与模型契约.md:214-219`
- 是否影响实现顺序：否，对 Phase 2A 后端不构成前置；但在任何前端 URL/预览改动后必须执行 vue-tsc、lint 和 browser-use 实测并留证。
- 风险等级：中
- 当前状态：未完成（Phase 3/UI 验收）

## 远端提交影响

`6bdc19406` 的路径和 strict 文档修正与当前 Phase 2A 实现方向一致，但没有完成 Anthropic 全入口、Phase 2B 上传契约、Phase 2C 数据模型或 Phase 2D raw casing 治理。尤其是 `provider/client.go:547-551` 的注释声明所有 SQL 已等值匹配，但 `provider/client.go:643-649` 仍存在 `lower(...)` fallback；`provider/client.go:596,636,664` 和 `resolve/resolve.go:153,193,224` 仍对 `RawModels` 使用 `lowerUnique`。这些是 S25-S28 的直接阻塞证据。

## 顺序结论

1. 完成本审计文档和独立 S17-S28 证据工具。
2. 在不实现假接口的前提下完成 Cloudreve/gateway 上传契约阻塞报告。
3. 补 Gemini/多类型附件边界测试，先测试后实现最小 vertical slice。
4. 设计 Phase 2C 的 request_attachments、历史兼容读、脱敏 backfill/TTL/权限，再使用编号 `398+` migration。
5. 修复 Phase 2D raw casing 和 SQL equality，并补 S25-S28。
6. 最后进入 Phase 2B uploader 和 Phase 3 UI/browser 验收。
