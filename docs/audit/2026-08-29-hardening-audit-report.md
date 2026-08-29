# 2026-08-29 审计修复与特性需求闭环报告

## 1. 审计范围与基线

- 仓库：`llm-gateway-go-3`
- 修复分支：`audit/2026-08-29-hardening`
- 基线：修复开始时的最新 `origin/main`
- 依据：
  - `docs/audit/2026-08-29-comprehensive-24h-audit.md`
  - `docs/audit/2026-08-29-task-execution-report.md`
  - `docs/audit/2026-08-29-p2-task-execution-report.md`
  - `.handoff/2026-08-29-*` 中与当前仓库相关的补充记录

不同 handoff 文档指向不同项目根目录和 commit，且部分“已完成”描述与 staging/生产复选框冲突。因此本报告只把当前仓库代码、自动化测试和可复现的静态检查作为代码验收证据；staging、真实 PostgreSQL、Prometheus 运行时和前端验证仍单独列出。

## 2. 特性需求整理

### 2.1 Hot table promote

- promote 失败必须产生可告警的 Prometheus 指标。
- advisory lock 竞争必须可观测，不能因 failure counter 为零而隐藏积压。
- 单个 promote cycle 必须有时间/批次上限，不能阻塞分区创建、归档和清理。
- promote scheduler 可禁用，且不能因 `time.NewTicker(0)` panic。

### 2.2 Provider error aggregation

- source failure row 只能被聚合一次，调度窗口重叠不能重复计数。
- endpoint 必须参与错误指纹；缺失时写入明确的 `unknown`，不能把所有请求标为 chat。
- 错误复发时必须恢复 `resolved=false`。
- worker 启停必须幂等，负 interval 不能导致 ticker panic。

### 2.3 HTTP response body cleanup

- 非流式 HTTP 调用必须在关闭前读取/排空 response body，尽量复用连接。
- 错误响应允许只保留有限前缀，但必须继续排空尾部。
- 流式转发路径不能被通用清理函数截断。
- webhook 错误 body 必须真实读取，不能使用零长度目标切片。

### 2.4 Provider error TTL

- 只删除 `resolved=true` 且 `updated_at` 超过 TTL 的记录。
- 未解决错误和近期错误必须保留。
- 删除条件必须有匹配的部分索引。
- cleanup 调度必须独立于每日归档任务，支持取消。

## 3. 发现的问题与修复

### 3.1 PartitionManager

修复 `bg/partition_manager.go`：

- `Start`/`Stop` 幂等；未启动 Stop 立即返回，避免重复关闭 channel。
- `SetPromoteInterval` 使用 mutex；非正值安全禁用 promote。
- ensure/archive、promote、provider error cleanup 分为独立 worker。
- promote cycle 增加 5 分钟时间预算和 100 批上限。
- promote 失败、commit 失败、锁查询失败、锁竞争和空批次均记录耗时/结果。
- 修正 retention 注释，从已废弃的 7 天描述改为当前 8 小时 hot window。

### 3.2 Provider error aggregation

修复 `bg/provider_error_aggregator.go`：

- `Start`/`Stop` 幂等，interval <= 0 使用安全默认值。
- 使用 migration 620 新增的单调 `aggregation_id`，不再错误依赖来源表声明为非唯一的 legacy `id`。
- 在 advisory lock 和事务内读取/推进 watermark，避免重叠窗口重复聚合。
- 从 context 的 `endpoint`、`client_endpoint`、`upstream_endpoint` 提取 endpoint，缺失时为 `unknown`。
- UPSERT 复发时设置 `resolved=false`。
- 使用真实开始时间记录聚合耗时。

新增：

- `sql/migrations/startup/620_provider_error_aggregator_state.sql`
- `sql/migrations/startup/620_provider_error_aggregator_state.down.sql`
- `sql/migrations/startup/621_provider_error_details_cleanup_index.sql`
- `sql/migrations/startup/621_provider_error_details_cleanup_index.down.sql`

### 3.3 HTTP response body

修复 `pkg/httputil` 和非流式调用点：

- `ReadPrefixAndDrain` 捕获有限前缀、排空剩余 body、关闭并合并错误。
- `DrainAndClose` 使用该实现，避免“只读 64 KiB 后直接 Close”的错误语义。
- 接入 health、inference、quota、analysis、memory、proxy、provider profile、webhook 等非流式路径。
- 保留 streaming executor/native responses 的长连接处理不变。
- 修复 webhook `make([]byte, 0, 1024)` 导致永远读不到错误 body 的问题。

### 3.4 Prometheus 与 SQL 契约

- 在 `deploy/prometheus/rules/alerts.yml` 实际加入：
  - `HotTablePromoteFailures`
  - `HotTablePromoteLockContention`
  - `HotTablePromoteSlow`
- 新增告警 YAML 契约测试，校验 metric、for、低基数 table label。
- 修复 migration 616 down 脚本的顶层裸 `RAISE NOTICE`。
- 新增 migration 616、620、621 契约测试。

### 3.5 基线测试兼容性

- `domains/streaming/stream_eof_test.go` 补齐 `RecordMalformedSSEFrame` 测试 double。
- 更新旧错误码断言为当前 SSE 校验层的 `malformed_sse_frame`，不改变生产处理逻辑。

## 4. 验证结果

已通过：

```text
go test -race ./bg ./pkg/httputil ./domains/notification ./domains/health \
  ./domains/quotafetcher ./domains/analysis ./proxy ./domains/providerprofile \
  ./domains/memory/client ./deploy/prometheus/rules ./sql/migrations/startup

go test ./domains/streaming -race -short
go build ./...
go vet ./bg ./pkg/httputil ./domains/notification ./deploy/prometheus/rules
git diff --check
```

全量命令：

```text
go test ./... -race -short
```

结果：✅ 通过（全仓库包均完成测试；macOS 编译器仅输出 go-m1cpu 的 VLA 扩展 warning）。此前基线中的 streaming 测试 double 与错误码断言也已同步到当前 SSE 校验语义。

`promtool` 在本机不可用，因此告警表达式未执行 promtool lint；已通过仓库现有 Go YAML 契约测试和 `go build ./...` 验证。真实 Prometheus rule 加载仍需 staging 验证。

未执行或无法由本地代码证明：

- 真实 PostgreSQL migration/upsert/watermark/TTL 查询计划。
- staging hot-table promote、Prometheus scrape/alert firing。
- 100 个会话双写、前端详情抽样、真实 provider/SDK/live traffic E2E。
- 错误率 < 0.1% 和 p99 < 500ms 的生产门禁。

## 5. 部署注意事项

1. 按顺序执行 migration 620、621；migration 616 已应用，不应直接重写其 up 文件。
2. 生产执行 migration 620 前应备份 `candidate_failure_logs_hot`，并观察 `aggregation_id` 回填耗时。
3. 先在 staging 验证 `provider_error_details` 聚合和 resolved TTL 删除，再启用生产告警。
4. Prometheus 规则目录必须加载 `deploy/prometheus/rules/*.yml`，确认 `alerts.yml` 被目标 Prometheus 读取。
5. 监控 `llm_gateway_hot_table_promote_failures_total`、`...skipped_total` 和 `...duration_seconds`，并检查 hot 表 backlog 是否下降。

## 6. 结论

本次代码审计已修正会导致 worker panic、生命周期泄漏、聚合重复计数、活跃错误被误标 resolved、HTTP 错误体丢失、TTL 全表扫描和告警未接入等问题。代码层面验证完成；外部环境验收仍需按第 4、5 节执行并留存证据。
