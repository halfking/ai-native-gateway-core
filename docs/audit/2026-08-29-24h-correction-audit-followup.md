# 2026-08-29 24小时修正审计：闭环跟进

## 范围与基线

本次复核基线为 `2a52ecbb`（合并后的 `main`），覆盖最近 24 小时的 streaming/native Responses、URSM/Redis、请求详情、代理管理、会话/缓存、hot+分区存储、供应商错误可观测性和管理 UI 变更。

审计期间工作区曾被外部过程切换分支并出现大规模文档树删除/冲突的瞬态状态。已在系统临时目录保留完整工作树与 index 补丁；最终以无冲突、与 `origin/main` 一致的子仓库状态执行修复。没有将文档重组或父仓库子模块指针变更混入本次提交。

## 本轮确认与修复

| 优先级 | 项目 | 结论与处置 |
| --- | --- | --- |
| P1 | `/readyz` 严格就绪语义 | 修复。DB 或 Redis connector 为 nil 时原实现会返回 ready；现在 DB 和 Redis 都必须存在且 2 秒 Ping 成功，才返回 200。`/healthz` 仍是 200 liveness，只报告 `ready=false`；匿名路径不泄露依赖错误。 |
| P1 | 版本读取漂移 | 修复。删除重复的 version metadata writer，并让旧的 health 版本字符串复用 `version.json` 元数据解析路径，避免 `/version`、`/healthz` 和部署预检使用不同回退逻辑。 |
| P1 | 成功但 response body 缺失 | 修复同步观测闭环。request log 成功落库且响应体为空时，写结构化 warning 并递增无高基数标签的 `llm_gateway_success_response_body_missing_total`；该观测不改变结算或请求结果。 |
| P1 | Proxy Prometheus 注册 | 修复。重复创建 `proxy.Manager` 曾对全局 registry 重复注册同名 collector 并 panic；现在复用同 registry 的已注册 collector。 |
| P2 | Durable recovery race 验证 | 修复测试夹具竞态。worker 的有界 stop 语义保持不变；测试 runner/store 的跨 goroutine读写改为同步，`-race` 通过。 |
| P2 | hot retention 配置文档 | 修复陈旧注释。运行时默认 hot 保留已是 8 小时；`cmd/gateway/main.go` 中 hot cron 环境变量注释从 24 修正为 8。 |

## 已确认闭环

- **供应商错误**：正常候选失败已写入 `candidate_failure_logs_hot`，包含 credential/provider/model、错误分类、HTTP 状态、去边界化响应片段和 tenant/request 关联；管理端 credential error API 和 Provider Detail ErrorDetailTab 已消费该数据。写入为独立 3 秒 best-effort，不中断主会话和 failover。
- **hot + partition**：`PartitionManager` 默认 8 小时、每小时 promote、每表 advisory lock、批量原子迁移和失败保留源数据已存在；admin cron 默认也为 8 小时。`request_logs_bodies` 的 24 小时 promote/7 天历史清理是独立 body 存储策略，不属于普通 hot 表违例。
- **native Responses 与 streaming**：能力门控、原始 SSE 转发、行长度上限、reader panic recover、stream-only dispatch gate 和异常终态测试均已落地。官方 SDK/真实供应商互操作仍需要有授权的联网 canary 验证。
- **URSM**：SafeHGetAll、Lua nil guard、ledger append/fsync、subscriber double-start/reconnect/close wait 已落地。当前构造器只启动一次；如果将来开放 Close 后 restart，需要先定义并测试新的 lifecycle 契约。

## 未关闭项

1. `provider_error_details` 表仍没有统一 Go writer；normal candidate failures 已有可用 credential 明细，但 circuit/limiter/key-rotation 等前置失败未完全归一到同一错误集合。应在不影响会话的前提下扩展统一 writer，并新增 handler/PG contract tests。
2. `JournalSnapshot` 已有事件数截断和内存消费者授权测试；生产 `dispatchJourneyJournalAdapter` 尚没有 caller auth context、snapshot version 和 durable idempotency。ADR 仍为 Proposed，需先接受设计后实施。
3. `success_response_body_missing_total` 当前是同步实时观测；尚无针对历史记录的异步扫描/告警阈值与 dashboard 消费规则。
4. 历史分区的真实 PostgreSQL promote 函数需在目标数据库以 SQL integration suite 验证；本轮已验证 Go scheduler/设置/单元测试，未对生产数据库执行迁移或数据移动。
5. UI 完整性（Provider ErrorDetailTab、ProxyView locale/ARIA）和官方 SDK/live provider TCP 互操作需要有浏览器/供应商授权的环境做端到端验收。

## 验证

- `go test -race ./domains/streaming ./domains/ursm/v2 ./middleware -count=1 -timeout 240s`
- `go build ./...`
- `go vet ./...`
- `go test ./... -count=1 -timeout 240s`

以上均通过。全量测试曾暴露 proxy 指标重复注册 panic；修复后重新运行全量测试通过。
