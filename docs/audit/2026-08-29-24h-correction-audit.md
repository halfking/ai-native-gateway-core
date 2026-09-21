# 24 小时修正全面审计报告

**审计日期**：2026-08-29  
**范围**：按 `git log --since="24 hours ago"`，覆盖 Session V2、回填/校验工具、hot 生命周期、流式 failover、请求详情、缓存/队列与前端改动。  
**分支**：`feat/session-turns-v2`

## 结论

本次已修复一组可在代码层安全闭环的问题，并通过受影响 Go 包测试。供应商失败的主路径已经具备记录、fallback 和 credential 详情反馈，但 `provider_error_details` 仍没有业务写入；`session_bodies` 仍直接写月分区、没有 `session_bodies_hot`，因此不能宣布“所有 V2 正文均满足 hot+8h+批量 promote”。在完成后述 schema/数据迁移前，生产切换结论为 **No-Go**。

## 已修复

1. **跨租户正文隔离**：`admin/session_turns_v2.go` 的 metadata/body JOIN 增加 `tenant_id`，避免同 session/turn 在不同租户间拼接正文。
2. **请求正文时间与租户闭环**：`request_logs_bodies_hot` INSERT 从同一事务的 `request_logs_hot` 读取 `tenant_id` 与原始 `ts`；冲突更新不再刷新 `ts`，并只在 tenant 为空时补齐。正文查询和归档可继续使用 request metadata 的时间语义。
3. **Hot 窗口统一为 8 小时**：settings、PartitionManager、手动/异步/cron 默认值均统一为 8；显式配置仍可覆盖。
4. **生命周期运维闭环**：admin promote 白名单补齐 scheduler 已支持的 `candidate_failure_logs_hot`、`session_turns_hot`、`handoff_logs_hot`、`session_module_executions_hot`、`dashboard_access_events_hot`。
5. **批量校验性能**：`validate_sessions_v2.LoadSessionsInRange` 将每会话 `MAX(ts)` N+1 查询改为单条 `GROUP BY ... HAVING MAX(ts)`。
6. **V2 正文序列化可观测性**：`safeJSONMarshal` 不再将编码失败伪装为空数组/对象，编码失败会返回错误并阻止错误正文写入。
7. **压缩缓存恢复性**：Redis 编解码补齐 v8 raw/compressed counters、quality、sanitize map/stat 字段，并增加 round-trip 测试。
8. **管理端错误脱敏**：新 V2 turns list 不再把 SQL/驱动错误原文返回给客户端，详细错误保留结构化日志。

## 流程、数据与反馈闭环

### 请求与会话

- V1 元数据：`request_logs_hot` → 月分区；正文：`request_logs_bodies_hot` → 月分区。
- V2 元数据：`session_turns_hot` → `session_turns` 月分区；V2 正文当前直接写 `session_bodies` 月分区。
- V2 writer 的 turn + body 实时写入具备事务与 session advisory lock，但已 promote 的 `session_turns` 仍存在 parent UPDATE 路径。
- 回填工具支持 hot 表来源、delta 推导和幂等写入；校验工具支持 V1/V2 分层读取。

### 供应商错误与客户端反馈

当前 authoritative path：

`供应商失败 → candidate_failure_logs_hot → 月分区/current-month view → credential detail API`

流式 failover 通过 `DispatchNotice` 桥接到 `: thinking:` SSE comment channel，不进入 conversation data；commit gate 在已提交 semantic output 后禁止透明重试。credential 详情返回错误类型、次数、最近失败、状态码、preview 和质量分数。

缺口：

- `provider_error_details` 只有 schema/view，没有 Go writer/upsert，属于未使用死表。
- circuit-open、limiter/concurrency、key-rotation 耗尽等 preflight failure 发生在统一 failure logger 之前，未必写入 candidate failure 集合。
- SurvivalCoordinator 自身的 recovery retry 可能只发 keepalive，不一定有带原因的 thinking notice。

本次不在请求主路径中新增同步聚合或无界异步队列；这些应作为有界、可重试、带失败计数的后续设计。

## 并发、资源与大内存审计

- `hotJobManager` 使用 RWMutex，异步任务有 24h context、cancel 和 panic recovery；任务数按表/全局有界。
- PartitionManager 使用事务 advisory lock，避免多副本同时 promote 同一 hot 表。
- RequestJourney outbox 使用 lease/SKIP LOCKED/fencing；但 durable outbox 入队队列满时会标记 degraded 而不让 Apply 失败，需后续明确“best-effort”或同步落库契约。
- outbox 投递若超过 lease 且无 heartbeat，可能产生跨副本重复 Redis side effect；需增加小于 lease 的单次 deadline/heartbeat。
- request-detail capture forwarder 已按单条大小做 guard，但元素容量 × 单条上限仍可能形成很高堆峰；应后续采用总字节预算和 drop-bytes 指标。
- 附件解析有 MaxSize 与哈希去重，但当前 `io.ReadAll` 仍把完整附件置于内存；真正恒定内存需要后端 `io.Reader` 接口迁移。
- 所有检查到的 rows.Close/context cancel/worker done 路径未发现确定性的句柄泄漏；应在 CI 持续使用 race detector。

## 请求转换、溢出、网络/TCP、密钥/API

- 回填/校验中 token、成本、延迟字段使用有界数据库类型；批大小、limit、retention 已有上下限。
- JSON 转换对多模态内容采用 compact JSON 投影；这是已知信息损失边界，应在 schema/version 契约中标注。
- 流式 unexpected EOF、timeout、client cancel 已区分；已提交输出不会透明重放。
- provider credential 详情 API 有 ID、时间窗口和超时校验；具体密钥可用性依赖现有 self-check/probe 任务，未在本地凭据缺失时伪造通过。
- 未执行 staging/生产数据写入、清理、DROP 或密钥验证；本报告不包含任何明文凭据。

## UI、可观测性与交互

- Session V2 API 与详情页已具备 loading/fallback 基础路径，但超过 200 轮时前端忽略 `has_more`，可能只显示最旧部分；应后续实现 cursor/虚拟列表或明确截断提示。
- 附件 signed URL 前端 API 当前对应服务端 404；应删除未启用入口或接入 tenant/object-key 授权后再暴露。
- SessionTurnsSyncPane 存在硬编码文案和嵌套 button，未完全复用 detail 控件/i18n 规范；应后续统一 locale key、键盘语义和控件组件。
- 生命周期与供应商详情 API 已有结构化错误日志；建议继续补齐 metrics：hot oldest age、promote failures、body tenant-null、body ts drift、candidate logger latency、outbox drops。

## No-Go / 后续迁移门禁

1. 为 `session_bodies` 设计 `session_bodies_hot`、统一读 view、原子批量 promote 和回滚窗口；在迁移完成前不要把正文分区切为 columnar。
2. 禁止对已 promote 的 columnar `session_turns`/bodies 执行可变 UPDATE；late enrichment/aggregate claim 需采用 hot patch 或 append-only side table。
3. 只读盘点历史 body `tenant_id IS NULL`、metadata/body `ts` drift、非 heap 正文分区及目标月份分区缺失；不得直接批量 UPDATE/DELETE。
4. 确认 staging/生产已部署 bodies atomic promote migration，并验证 target partition 可写。
5. 在至少 100 个 settled 会话完成 V1/V2 reconstruction/parity、前端详情抽样、双写错误率和 p99 延迟门禁后，才允许切换读路径。

## 验证结果

通过：

```text
go test ./domains/session/v2 ./domains/hooks/compression ./domains/hooks/observability/telemetry ./cmd/tools/validate_sessions_v2 ./admin ./bg
```

并执行 `gofmt`、`git diff --check`。受影响包测试通过：`domains/session/v2`、`domains/hooks/compression`、`domains/hooks/observability/telemetry`、`cmd/tools/validate_sessions_v2`、`admin`、`bg`。全仓 `go test ./...` 另外被两个与本次变更无关的既有门禁阻断：`sql/migrations/domain` 的 baseline seed drift，以及 `sql/migrations/startup` 的重复 migration version 611。前端测试与构建通过。未执行生产/staging 破坏性操作。
