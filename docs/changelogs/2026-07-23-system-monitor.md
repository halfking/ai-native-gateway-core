# 系统监测模块（System Monitor）Phase 1+2 上线

> **日期**：2026-07-23
> **范围**：探测任务统一编排（SystemMonitor）、仪表盘系统监测面板、5 分钟请求成功跳过规则
> **设计依据**：[docs/会话优化v2/32-系统监测模块设计.md](../会话优化v2/32-系统监测模块设计.md)（680 行 8 章）
> **审计报告**：[docs/会话优化v2/33-系统监测Phase1-2-审计报告.md](../会话优化v2/33-系统监测Phase1-2-审计报告.md)

## Summary

将散布在 5+ 个 worker 的探测逻辑统一收口到 `bg/systemmonitor/` 包，并提供仪表盘"系统监测"页面。
后端引入 Redis FIFO 队列 + Lua atomic claim + 30s inflight dedup + 5min recent_success 跳过规则，承载 6 种 task_type（direct_ping / gateway_ping / chat_minimal / chat_tool / chat_stream / http_ping）的可观测化执行。
Phase 1+2 落地，Phase 3 旧 worker 收敛留作后续迁移任务。

## Delivered

### 后端核心
- **新建包 `bg/systemmonitor/`** (8 个 .go)
  - `types.go` — 6 TaskType + 2 Automaticity + 6 Status + Task 结构 + Validate
  - `redis_queue.go` — Queue (Submit/Claim/Complete/Requeue/Size) + marshalTaskForLua
  - `inflight_dedup.go` — MarkRecentSuccess + ShouldSkipAutoTask + MarkInflightSkip + Ping
  - `executor.go` — 6 Executor 分派 + http_ping（HEAD→GET fallback + DNS/TLS 计时）
  - `monitor.go` — SystemMonitor 顶层 + workerLoop + 5min 跳过 + backoff ladder + Fallback 内存 FIFO
  - `audit.go` — system_probe_runs INSERT（统一 NULL/字符串处理）
  - `lua_scripts.go` — embed Lua + LoadScripts + EVALSHA→EVAL fallback
  - `recent_success_hook.go` — 5min 跳过 hook（接入 telemetry onPersisted）
- **2 个 Lua 脚本**
  - `lua/claim.lua` — 原子 claim：30s dedup + scheduled_at + FIFO 循环
  - `lua/complete.lua` — 原子 complete：HSET status + SREM running
- **2 个 DB 迁移** (sql/migrations/domain/344 + 345 + 各自 down)
  - `system_probe_runs` — 按 created_at 分区的审计表 + 7 索引 + default partition
  - `self_check_settings.monitor_concurrency INT 1-32` — 并发上限配置

### admin 后端
- **`admin/systemmonitor_handlers.go`** — 9 个 REST 端点
  - `POST /api/admin/system-monitor/submit` — 单条入队（manual）
  - `POST /api/admin/system-monitor/start-all` — 仪表盘"开始全部任务"（防爆：>200 拒绝）
  - `POST /api/admin/system-monitor/stop-all` — 仪表盘"停止全部任务"（仅清空队列）
  - `POST /api/admin/system-monitor/by-credential/{id}` — 按凭据触发 direct_ping
  - `POST /api/admin/system-monitor/by-provider/{id}` — 按供应商触发 direct_ping + http_ping 1:1
  - `POST /api/admin/system-monitor/by-model/{name}` — 按模型触发
  - `GET /api/admin/system-monitor/stats` — 5s 轮询（队列 + running + concurrency + in_fallback）
  - `GET /api/admin/system-monitor/recent-runs?limit=50` — system_probe_runs 最近 50 条
  - `PATCH /api/admin/system-monitor/concurrency` — 改 monitor_concurrency (CHECK 1-32)
- **`admin/systemmonitor_stream_sse.go`** — SystemMonitorSSEHub
  - Redis Pub/Sub `llmgw:monitor:events` 订阅
  - 30s 心跳 SSE envelope
  - per-client write mutex + 64-buffered fan-out（满则断让 EventSource 自动重连）

### cmd/gateway 接入
- **`cmd/gateway/system_monitor_adapter.go`** — `*systemmonitor.SystemMonitor` → `admin.SystemMonitorBackend` 适配器
- **`cmd/gateway/main.go`** — env `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true` 启停
  - 调用 `systemmonitor.NewSystemMonitor` + `Start()`
  - 调用 `adminHandler.SetSystemMonitor(...)` + `SetSystemMonitorSSE(...)`
  - 调用 `telemetryClient.AddOnRequestLogPersisted(hook.Hook())` 实现 5min 跳过规则
  - 调用 `adminHandler.RegisterSystemMonitorRoutes(...)` 注册路由

### 前端 (Phase 2.4)
- **`web/src/api/api-system-monitor.ts`** (10 个端点 + EventSource 封装)
  - `submit` / `startAll` / `stopAll` / `startByCredential|Provider|Model`
  - `fetchSystemMonitorStats` / `fetchSystemMonitorRecentRuns`
  - `updateSystemMonitorConcurrency`
  - `openSystemMonitorStream(onEvent, onError)` 使用原生 EventSource
- **`web/src/views/SystemMonitorPanel.vue`** (≈500 行)
  - 4 张统计卡片（队列 / running / concurrency / 状态）
  - 7 个操作按钮（含对话框：按 provider/credential/model + 并发调整）
  - 队列任务分组泳道（SSE 实时事件按 task_type 分行）
  - http_ping 网络延时泳道（>1000ms 标红）
  - system_probe_runs 最近 50 条表格（按 status 着色）
- **`web/src/router.ts`** — 新路由 `'/system-monitor'` requiresSuper

### KEEP/FUTURE 标记（rule 09 §5.2.4）
- **`bg/credential_selfcheck.go:1`** — KEEP: 至 Phase 3 自动任务切流完成（review 2026-Q3）
- **`bg/asset_health_probe.go:1`** — FUTURE: 资产级别监控（trigger 2027-Q2）

## 关键决策与原理

| 决策 | 原理 |
|---|---|
| 6 种 task_type 严格枚举 + Valid() | rule 38 §4.4 幂等迁移；6 类型与设计 §4.1 严格对齐 |
| 自动性 = mandatory + automatic 二态 | 老板要求第 1 条新增；mandatory 永不被跳过 |
| Redis Lua atomic claim (claim.lua) | 避免多 worker race；30s dedup + scheduled_at 必须原子判定 |
| marshalTaskForLua 时间戳 → ms 数字 | Lua cjson 不支持 RFC3339 比较；number → redis.call('TIME') 直接对比 |
| 5min 跳过规则只在 worker 内 claim→exec 之间 | 全程唯一入口，必须保证 (1) 是 automatic；(2) 最近 5min 有真实 2xx 成功 |
| Fallback 内存 FIFO channel | rule 03 §6；Redis 不可达不阻塞主流程，slog warn 提示 |
| 默认 partition（替代日 partition） | 吸取 343 migration 教训（分区表无分区 INSERT 报错） |
| middleware RecentSuccessHook 2s 超时 + panic-recover | 不阻塞 telemetry worker；失败仅 WARN 不记 metric |
| 接口用投影 + cmd/gateway 适配器 | 保持 admin → bg/systemmonitor 解耦，与 liveStreamHub/AvailabilityReader 同构 |

## Verification

### go build / vet / test
| 项 | 命令 | 结果 |
|---|---|---|
| Go build (全量) | `go build ./...` | ✅ 无错误 |
| Go vet (全量) | `go vet ./...` | ✅ 0 issue |
| Unit tests (系统监测 + admin) | `go test ./bg/systemmonitor/ ./admin/... -race -count=1` | ✅ 4 packages 全过（含 systemmonitor 6 个 + admin suite） |
| Lint (systemmonitor) | `golangci-lint run ./bg/systemmonitor/... --timeout=2m` | ✅ 0 issue |
| Vue TypeScript | `cd web && npx vue-tsc --noEmit` | ✅ 0 error |

### SQL 迁移 dry-run（Docker PG 15）
| 迁移 | 结果 |
|---|---|
| `344_system_probe_runs.sql` | ✅ CREATE + 7 INDEX + DEFAULT partition + COMMENT；INSERT success + skipped 行 OK |
| `345_self_check_monitor_concurrency.sql` | ✅ ALTER ADD COLUMN + CHECK 触发（=0 拒绝、=32 接受、=5 默认） |

### 不动入参（rule 04 §1 红线对齐）
| 项 | 状态 |
|---|---|
| 旧 NodeProbeWorker / ActiveProbeWorker / CredentialSelfcheckWorker 代码 0 改动 | ✅ 旧流程 100% 不变 |
| `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED` 默认 false | ✅ 默认走旧路径（向后兼容） |
| 旧 self_check_runs / node_probe_state / provider_probe_runs 表 | ✅ 不删，data lifecycle 自动清理 |

## Deployment

启用步骤（245 验证完成后才能上 154）：

```bash
# 245 验证
scp ssh root@8.136.114.245 'systemctl stop llm-gateway-go'
scp build/llm-gateway-go root@8.136.114.245:/usr/local/bin/
psql -h 172.16.2.241 -U postgres -d <db> -f sql/migrations/domain/344_system_probe_runs.sql
psql -h 172.16.2.241 -U postgres -d <db> -f sql/migrations/domain/345_self_check_monitor_concurrency.sql

# env edit /etc/llm-gateway-go/env
LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true
LLM_GATEWAY_SYSTEM_MONITOR_WORKERS_PER_NODE=5

systemctl start llm-gateway-go
# L1-L4 验证（设计 §7.4）
```

详细 L1→L4 + 回滚步骤见设计文档 §6.5 回滚预案。

## 遗留与风险

| # | 项 | 解决路径 |
|---|---|---|
| 1 | **旧 worker 未改 Submit**（设计目标"唯一入口"未达成） | Phase 3 单独 PR；预留在 KEEP/FUTURE 表里 |
| 2 | **chat_stream 探测未实现**（仅占位 task_type_unimplemented） | 设计 §4.1 列了 FUTURE，Phase 3 之后 |
| 3 | **browser-use 实测未做**（rule 11 §6 红线） | 部署到 245 后用 browser-use 实测（缺失证据） |
| 4 | **245 / 154 L1-L4 验证未跑**（rule 03 §6） | 待部署步骤；本文档不展开 |
| 5 | **审计日志滚动**（slog.Default 未接 rotWriter） | Phase 3 后续 |
| 6 | **Dashboard UI 未做 browser-use 实测截图** | 同 #3 |

## Downstream / Cross-reference

- [docs/会话优化v2/32-系统监测模块设计.md](../会话优化v2/32-系统监测模块设计.md) — 设计文档（680 行 8 章）
- [docs/会话优化v2/33-系统监测Phase1-2-审计报告.md](../会话优化v2/33-系统监测Phase1-2-审计报告.md) — 自审报告
- [docs/changelogs/2026-07-13-error-triggered-probe.md](2026-07-13-error-triggered-probe.md) — 旧 probe 模式历史
- [docs/audits/2026-07-14-deployment-hardening-audit.md](../audits/2026-07-14-deployment-hardening-audit.md) — 部署安全审计基线（rule 03）

## Statistics

- **新建 .go 文件**：14 个（13 系统监测 + 1 适配器）
- **新建 .lua 文件**：2 个
- **新建 .sql 文件**：4 个（2 up + 2 down）
- **新建 .vue 文件**：1 个
- **新建 .ts 文件**：1 个
- **修改 .go 文件**：2 个（admin/handler.go + cmd/gateway/main.go）
- **修改 .ts 文件**：1 个（web/src/router.ts）
- **新增总行数**：约 2,200 行（按 commit 时 git diff 显示）

KEEP 标记：
- `bg/credential_selfcheck.go:1` KEEP (@monitoring, review 2026-Q3)
- `bg/asset_health_probe.go:1` FUTURE (@platform, trigger 2027-Q2)