# 数据库全量 SQL 审计与优化（2026-09-17）

> 目标：拉取最新代码后，检查项目中**所有 SQL 语句与数据表结构**，绘制**数据库结构图**，
> 对**每条 SQL** 进行分析，编写**优化方案**并**实施验证**。
> 基线 commit：`cd76a7f0e`（2026-09-17 `git pull --ff-only` 后，origin/main）。

## 产物索引

| 产物 | 位置 |
|---|---|
| 数据库结构图（Mermaid ER，9 域 17 张子图） | [schema-diagram.md](schema-diagram.md) |
| 表结构全量清单（271 表逐列） | [schema-inventory.md](schema-inventory.md) |
| 全量 SQL 语句目录（2510 条，JSONL，含 file:line） | [scripts/sql_catalog.jsonl](scripts/sql_catalog.jsonl) |
| 表结构机器可读清单（tables.json） | [scripts/tables.json](scripts/tables.json) |
| 提取/生成脚本（可复跑） | [scripts/](scripts/) |

## 一、方法与覆盖面

1. **表结构**：以 `sql/schema/01-schema.sql`（30,315 行，安装器内嵌的权威 schema）为源，
   `parse_schema.py` 解析出 **271 张表 / 4,697 列 / 621 个索引 / 34 条物理外键**
   （外键集中在 schema 尾部 `ALTER TABLE … ADD CONSTRAINT … FOREIGN KEY`；其余关系为应用层逻辑外键）。
2. **SQL 语句**：`extract_sql.py` 扫描全部非测试 Go 源码（剔除 vendor/testdata），提取反引号与
   双引号字符串中的 SQL，得 **2510 条**语句，逐条登记 `file / line / 类型 / 涉及表 / 反模式标记 / 原文`。
3. **语义分析**：7 个分域探索代理（admin、domains、bg+durable、请求热路径、路由、基础层、其余业务模块）
   逐文件核对语句用途、触发路径（请求热路径 / 后台周期 / 启动期 / 管理面）、与 schema 索引匹配度。
   按《docs/audit/playbook/conventions.md》纪律，**主会话对每条拟动手的发现逐一亲读复核**后才实施；
   未复核的代理线索在 §四 中标注"待复核"。
4. **无真库环境声明**：本机 Docker 守护进程未运行、无 `TEST_DATABASE_URL`，EXPLAIN/真库实跑不可用。
   因此：**不新登记编号迁移**（遵守"新迁移必须至少在一个存量真库实跑后再定稿"纪律），
   索引类修复全部以启动自愈 ensure（仓库既有惯例）或方案草案形式给出。

## 二、SQL 目录统计

按语句类型：SELECT 1269 · SELECT（嵌入）87 · INSERT 385 · DELETE 111 · UPDATE 31 ·
WITH/CTE 57 · DDL/ADMIN 135 · 其他（SET/GUC/事务等）435。

按模块（前 10）：admin 847 · domains 524 · bg 300 · db 146 · internal 94 · cmd 91 · durable 55 ·
maas 40 · licensing 36 · storage 34。

按涉及表（前 10）：credentials 223 · providers 146 · credential_model_bindings 108 ·
provider_models 100 · models_canonical 89 · request_logs_with_current_month 64 ·
request_logs_hot 53 · session_summaries 52 · request_logs 50 · model_offers 47。

自动标记的反模式（含误报，人工复核见 §四）：select_no_limit 735（多数为配置小表/聚合表，健康）、
order_by_no_limit 476、on_conflict 228（幂等写，健康面）、or_condition 193、sprintf 164
（逐一核实无用户输入拼接，见 §五.7）、fn_on_predicate 163、for_update 49、offset 21。

## 三、架构健康面（审计确认的良好设计）

- **热/冷分区体系**：request_logs / request_wal / usage_ledger / credit_ledger / sessions /
  session_turns / credential_model_index / routing_decision_log 等大表统一
  `_hot` 表 + 月分区 + `*_with_current_month` UNION 视图 + promote/drop 存储函数，
  `bg/partition_manager` 统一维护（advisory lock + bypass GUC）。
- **队列领取范式统一**：6+ 处 outbox/队列全部 `FOR UPDATE SKIP LOCKED`（+LIMIT/fencing/租约），
  专用部分索引支撑（probe_queue、durable 三表、settlement、system_monitor 等）。
- **幂等写**：228 处 `ON CONFLICT`，逐一核对冲突目标与唯一约束匹配（本次修复前唯一的例外是
  `api_key_auto_profile`，见 §五.F12）。
- **参数化彻底**：2510 条中**未发现用户输入拼接 SQL**；99 处 sprintf 均为内部常量/白名单
  （orderBy 二态、占位符位置、分区名 regex 白名单 + quoteIdent、escapeTenant GUC）。
- **RLS 纪律**：bypass/super_admin GUC 仅出现在后台维护事务，租户上下文走 `set_config(…, true)` 事务级。
- **保留期清理覆盖广**：audit_trimmer / opslog_trimmer / handoff 系列 / session_summaries_trimmer
  统一 `id IN (SELECT … ORDER BY time LIMIT 5000)` 分批范式。

## 四、分域逐条分析（发现汇总）

> 完整逐文件清单由 7 份分域报告产出（原始报告见会话记录；此处汇总全部 P1/P2 级发现）。
> 状态标注：✅ 已亲读复核并实施；📋 已亲读复核，需真库/迁移通道，入方案；🔎 代理线索，未复核，仅登记。

### 4.1 domains（会话/遥测/统计，524 条）

| # | 级别 | 发现 | 证据 | 状态 |
|---|---|---|---|---|
| D1 | P1 | 每请求在写事务内对分区父表无界 COUNT（无分区裁剪，逐分区索引探测） | domains/hooks/observability/telemetry/client.go:3424 | ✅ F3 |
| D2 | P2 | request_logs 父表按 gw_session_id 无时间界查询簇（后台路径） | domains/analysis/optimizer.go:189 等 4 处 | 📋 §六.4 |
| D3 | P2 | digest 回填候选批全量过滤 `digest IS NULL` 无部分索引 | domains/session/v2/session_digest_backfill.go:286 | 📋 §六.2 |
| D4 | P2 | stats_event_dedup/inbox 无保留期清理，无限增长 | domains/stats/event_writer.go:175 | 📋 §六.6 |
| D5 | P2 | promptinjection/outputcompliance 运行时 CREATE TABLE+种子，绕过迁移审计 | domains/promptinjection/detector.go:413 | 📋 §六.7 |
| D6 | P1 | session_summaries 回写租户硬编码 'default' | domains/sessionforensics/export.go:452 | ✅ F5 |
| D7 | P2 | request_attachments 单条无界 DELETE | domains/attachments/repository.go:286 | ✅ F6 |
| D8 | P2 | 预算校验对当月账本聚合（usage_ledger 无 api_key_id 索引） | domains/authentication/verifier.go:786 | 📋 §六.1 |
| D9 | P2 | requestjourney 三条租户级无界 DISTINCT 枚举 | domains/requestjourney/repository.go:135/155/240 | 🔎 |
| D10 | P3 | handoff trigger 对同一 session_summaries 行 6 次 PK 点查 | domains/hooks/handoff/trigger_hook.go:726-829 | 🔎（健康，可合并） |

### 4.2 bg + durable（后台与持久化队列，355 条）

| # | 级别 | 发现 | 证据 | 状态 |
|---|---|---|---|---|
| B1 | P1 | sticky_sessions 无 expires_at 索引，双路径 DELETE 全表扫 | bg/credential_cycler.go:187、bg/sticky_cleaner.go:54 | 📋 §六.2 |
| B2 | P1 | request_envelope 零索引，DELETE 全表扫 | bg/envelope_cleaner.go:54 | 📋 §六.2 |
| B3 | P1 | 结算 worker LATERAL 按 gw_session_id 关联 request_logs_hot，热表无该索引 | bg/auto_route_settle_worker.go:377 | 📋 §六.2 |
| B4 | P1 | node_probe_runs / request_context_attrs 单语句全量 DELETE（30s 超时） | bg/partition_manager.go:583/613 | 📋 §六.5 |
| B5 | P1 | 每周对分区父表执行 `VACUUM FULL request_logs_bodies`（父表无存储，无效且占全集群锁窗口） | bg/vacuum_worker.go:176 | ✅ F8 |
| B6 | P2 | stats_minute_rollup 每分钟对同一窗口 10 次重复扫描 | bg/stats_minute_rollup.go:145-271 | 📋 §六.8 |
| B7 | P2 | auto_index_refresher DELETE+INSERT 两条语句各自重算最重 CTE 且不在同一事务 | bg/auto_index_refresher.go:199-229 | 📋 §六.8 |
| B8 | P2 | `WHERE DATE(ts) = $1` 不可索引 | bg/feature_stats_worker.go:168 | ✅ F9 |
| B9 | P2 | routing_audit_log / armor_judgments 保留期删除无单列时间索引 | bg/audit_trimmer.go:110/127 | 📋 §六.2 |
| B10 | P2 | durable_llm_tasks/_events 无保留策略，无界增长 | durable/（全仓无 DELETE） | 📋 §六.6 |
| B11 | P2 | pending_outbox 持行锁期间做 Redis 网络往返 + 逐行 DELETE | durable/pending_outbox.go:48-163 | 🔎（涉架构，登记） |
| B12 | P2 | bandit_flusher 单事务逐凭据 UPDATE credentials 热表 | bg/bandit_flusher.go:101 | 🔎 |
| B13 | P2 | system_probe_runs 零索引且无清理 | bg/systemmonitor/metrics_collector.go:52-95 | 📋 §六.2 |

### 4.3 admin（管理面，847 条）

| # | 级别 | 发现 | 证据 | 状态 |
|---|---|---|---|---|
| A1 | P2 | `SET LOCAL lock_timeout` 在自动提交下不生效，VACUUM FULL/REINDEX 实际无锁超时保护 | admin/data_lifecycle_storage.go:743 | ✅ F2 |
| A2 | P2 | usage_ledger 缺 (api_key_id, ts) 索引；budget SUM 无时间上界扫全部分区 | admin/keys.go:966 + usage 聚合簇 | 📋 §六.1（索引）/🔎（上界语义需产品确认） |
| A3 | P2 | routing_decision_log 明细窗口聚合 + 逐行 jsonb 展开，无索引支撑 | admin/analytics.go:903 | 📋 §六.8 |
| A4 | P2 | OFFSET 深翻页 9 处（logs/attachments/审核队列等） | admin/logs.go:780、credential_routing_log.go:457 等 | 📋 §六.9 |
| A5 | P3 | models_canonical/provider_models/key_applications/routing_policy 缺 id/常用列约束或索引 | admin 多处点查 | 📋 §六.2 |
| A6 | P3 | ILIKE/lower() 谓词不可走 btree 精确索引 | admin/routing.go:5198、free_pool_extra.go:1667 等 | 📋 §六.3 |
| A7 | P3 | bootstrap 循环逐行 UPDATE credentials | admin/routing.go:4778-4784 | ✅ F10 |
| A8 | P3 | 同请求重复读 routing_policy（7 处同构 featured_models 查询） | admin/routing.go 多处 | 🔎（低频管理面） |

### 4.4 请求热路径（proxy/adapter/upstream/pool/registry/resolve/…，42 条）

| # | 级别 | 发现 | 证据 | 状态 |
|---|---|---|---|---|
| H1 | P2 | model_aliases 等值查询与 `(lower(raw_name), status) WHERE status='active'` 函数部分索引不匹配 → 冷解析顺序扫 | resolve/resolve.go:195/236 | ✅ F7 |
| H2 | P2 | adapter/upstream/pool/ratelimit/credentialfpslot 确认零 SQL（内存/Redis），热路径 DB 压力集中在 credentials 选择（autoroute，缓存化良好） | 全目录 + 源码核对 | 健康面 |
| H3 | P3 | credentialhealth 恢复扫描带 COALESCE+INTERVAL 表达式不可索引 | credentialhealth/checker.go:589 | 🔎（行数=不可用绑定，量小） |

### 4.5 路由与目录（autoroute/routingopt/modelcatalog/apihub，72 条）

| # | 级别 | 发现 | 证据 | 状态 |
|---|---|---|---|---|
| R1 | P1 | api_key_auto_profile 无 PK/唯一约束 → `ON CONFLICT (api_key_id)` 每次写入 42P10 报错（调用方仅 Warn），sticky-profile 功能静默失效；点查顺序扫 | sql/objects/tables/api_key_auto_profile.sql + autoroute/decision.go:868/896 | ✅ F11 |
| R2 | P2 | 决策路径候选过滤用拼接键 `(credential_id::text||':'||lower(canonical_name)) = ANY($1)`，不可 sargable | autoroute/recommend_v2.go:377 | 📋 §六.10 |
| R3 | P2 | 每 5min 索引快照 latest_bucket 对视图全量 GROUP BY，无法 loose scan | autoroute/index.go:520 | 📋 §六.8 |
| R4 | P2 | 48h 热门聚合按 canonical_id 过滤，表上只有 canonical_model（文本）索引 | autoroute/recommend_v2.go:475 | 📋 §六.2（有 2min 缓存缓解） |
| R5 | P3 | modelbinding 回退解析 EXISTS `ma.raw_name = ANY($2)` 与函数索引不匹配 | modelbinding/resolver.go:109 | 🔎（aliases 表小） |

### 4.6 基础层（db/internal/cmd/storage/settings 等，387 条）

| # | 级别 | 发现 | 证据 | 状态 |
|---|---|---|---|---|
| I1 | P1 | settings 平台读取无缓存，36+ 请求路径调用点每请求 2-8 次 settings_kv 往返 | settings/spec.go:279 + store_db.go:32（调用点 domains/session/v2/pipeline_hook.go:55 等） | ⚠️ 方案修订：见 §五 F4 |
| I2 | P2 | 压缩 L3 冷启动回退查分区父表（tenant+gw_session+body IS NOT NULL ORDER BY ts LIMIT 1），靠分区部分索引兜底 | cmd/gateway/main_v3_wiring.go:104 | 📋 §六.2 |
| I3 | P2 | outbox 监控 COUNT 每轮询（5s）两次全扫 | internal/outbox/dispatcher.go:392/399 | ✅ F7b |
| I4 | P2 | pgx 全局 Simple Protocol（禁语句缓存，自估 ~5% 损耗，换 schema 变更正确性） | db/db.go:57 | 登记不动（正确性取舍） |
| I5 | P3 | 启动期串行 50+ ensure、689 分区 ensure 10 分钟大事务 | db/db.go:120-443/662-909 | 🔎（有 3min 超时+重试，登记） |
| I6 | P3 | AUTO 路由 TenantResolver 每次实时查 api_keys 无缓存 | cmd/gateway/main.go:5117 | 🔎 |
| I7 | P3 | 权威清单缺表信号：outbox_events/session_dim/request_state_transitions 等约 45 张被引用表不在 01-schema.sql（由后续迁移/ensure 创建）——建议下轮 schema 快照同步 | 本审计 tables.json vs 引用面 | 📋 §六.11 |

### 4.7 其余业务模块（maas/licensing/center/vibecoding/…，约 210 条）

| # | 级别 | 发现 | 证据 | 状态 |
|---|---|---|---|---|
| M1 | P1 | 查询不存在的表 `runtime_alerts`（42P01 必现故障） | center/runtime_metrics.go:163（真表 `runtime_alert_events`，迁移 403） | ✅ F1 |
| M2 | P2 | exporter `DATE(ts)` 谓词 + 全窗口载入内存过滤/去重 | exporter/training_exporter.go:282-363 | 📋 §六.10（低频 CLI） |
| M3 | P2 | telemetry flush 逐条 INSERT（缓冲 50 条/10s） | telemetry/dashboard_events.go:294-300 | 🔎 |
| M4 | P2 | maas usage 页同窗口三连扫（总/按模型/按日） | maas/usage.go:106-177 | 📋 §六.8 |
| M5 | P2 | ChargeRequest 每请求双 FOR UPDATE 行锁串行化（索引均命中，属并发设计取舍） | maas/service.go:151-241 | 🔎（登记） |
| M6 | P3 | instance_heartbeats/center_commands/fault_events/offline_activation_requests 等无 TTL | center/store_pgx.go:159 等 | 📋 §六.6 |
| M7 | P3 | fault 检测器四个取数函数为 stub（恒返 0） | fault/detector.go:141-158 | 🔎（功能缺失，非 SQL） |

## 五、已实施优化（全部经 build/vet/test 验证）

| # | 修复 | 位置 | 说明 |
|---|---|---|---|
| F1 | `runtime_alerts` → `runtime_alert_events` | center/runtime_metrics.go:163 | 列与索引（idx_rae_instance_status）完全匹配查询；修复必现 42P01 |
| F2 | `SET LOCAL` → 会话级 `SET` + 归还前 `RESET` | admin/data_lifecycle_storage.go:743 | 自动提交下 SET LOCAL 无效；连接由 Acquire 独占，会话级安全；defer 次序保证 RESET 先于 Release |
| F3 | turn 计数加 30 天 ts 窗口 | domains/hooks/observability/telemetry/client.go:3424 | 恢复分区裁剪（1-2 个月分区）；写事务内每请求省去全分区索引探测；超 30 天会话轮次重计（实际会话远短，权威 turn_no 由 session/v2 聚合器持有） |
| F4 | settings 缓存改为**可选 API**（修订） | settings/ttl_cache.go、store_db.go | 初版直接给 `GetPlatform*` 加 5s TTL 后，domains/hooks/compression 6 个钉桩热加载测试失败（这些测试直接改 backend store 并断言下一次调用立即可见，且会整体替换 `settings.Global`）——证明"即时热加载"是被测试钉住的平台契约。最终：helpers 保持零缓存并加 IMPORTANT 注释；ttl_cache 新增 `CachedPlatformBool/String/Float` 可选族（`cachedEffectiveRaw` 带 Global 实例守卫防测试串值）；`InvalidatePlatformValue` 挂入 StoreDB.Set/Rollback/Delete 平台分支。**逐调用点切换需先核对该键的热加载钉桩测试**，列为 §六.12 |
| F5 | UpsertSummary 租户参数化 | domains/sessionforensics/export.go:431、service.go:181 | `$2` 传租户，空值回退 default；修复多租户归属写错 |
| F6 | attachments 保留期删除分批（1 万/批） | domains/attachments/repository.go:286 | 复用 ctid 自选 LIMIT 范式，防长事务/WAL 峰值 |
| F7 | resolve 别名谓词改写 `lower(ma.raw_name) = lower($1) AND ma.status = 'active'` | resolve/resolve.go:195/236 | 命中 `idx_model_aliases_lower_raw_name_status`；输入经 CanonicalizeClientModel 已小写，行为对既有可达行不变 |
| F7b | outbox 监控 COUNT 节流 5s→30s，并以互斥锁保护节流窗口 | internal/outbox/dispatcher.go:388 | 监控仪表 30s 陈旧度可接受，读负载降 6 倍；互斥避免并发轮询同时穿透 `lastGaugeAt` 后重复执行 COUNT |
| F8 | 移除对分区父表的 VACUUM FULL | bg/vacuum_worker.go:146 | request_logs_bodies 已分区，父表无存储可重写；空间回收归分区 DROP（已在 partition_manager），保留 hot 表 VACUUM (ANALYZE) |
| F9 | `DATE(ts) = $1` → `ts >= $1 AND ts < $1 + 1day` | bg/feature_stats_worker.go:168 | 可走索引/分区裁剪 |
| F10 | bootstrap 逐行 UPDATE → `WHERE id = ANY($1)` | admin/routing.go:4778 | 单语句批量 |
| F11 | api_key_auto_profile 写入身份修复 | db/db.go `ensureApiKeyAutoProfileIdentity` + 两份权威 schema | 新库以 `api_key_id` 主键创建；存量库启动时独立探测唯一有效索引，并在专用连接中以 2s `lock_timeout`、55P03 重试创建唯一索引，修复 sticky-profile 的 42P10 静默失效 |
| F12 | （同 F11 的读路径收益） | autoroute/decision.go:868 | 点查从顺序扫变索引扫 |

**验证记录**（截至 2026-09-17）：
- `go test -race -run='TestApiKeyAutoProfile' -v ./db`：F11 的启动接线与两份 canonical schema 主键断言均通过。
- `go test -race -run='TestDispatcher.*Throttle' -v ./internal/outbox`：F10 并发节流回归通过（16 个并发调用只消耗一次 pending/dlq COUNT 查询）；`go test -run='TestDispatcher' ./internal/outbox` 亦通过。
- `go build ./internal/outbox ./db ./settings`、`go test -short -timeout=90s ./internal/outbox ./db ./settings` 与 `go vet ./db ./internal/outbox ./settings` 均通过。
- 其他已改且可在本机编译的包（`domains/hooks/observability/telemetry`、`domains/sessionforensics`、`resolve`、`center`）已通过 `go test -short`；其 `go vet` 为零输出。
- 全量验证受既有环境限制阻断，不能将其归因于本次改动：Windows 下 `bg/storage_retention_worker.go` 使用 Linux 专属 `syscall.Statfs*`，导致 `bg` 及依赖它的 `admin` 不能编译；attachments 的两项测试硬编码 POSIX 分隔符而失败。`GOOS=linux GOARCH=amd64 go build ./...` 则受 vendored `onnxruntime_go` 的 build constraints 阻断。这些项目需在具备 ONNX Runtime 的 Linux CI 与 PostgreSQL staging 中完成全仓/真库门禁。
- 无 `TEST_DATABASE_URL` 且本机 Docker 守护进程不可用，故未执行真实 PostgreSQL `EXPLAIN (ANALYZE, BUFFERS)`；索引收益仍待 staging 真库实测。

## 六、待实施方案（需真库验证 / 迁移通道，按收益排序）

> 以下 DDL 草案**未登记**编号迁移（纪律：新迁移须真库实跑后定稿）。建议按仓库 revision-sequence
> 通道登记并在 staging 真库 EXPLAIN 前后对比后合入。启动自愈 ensure（F11 模式）可作为过渡通道。

### 六.1 索引补齐（收益：高）

```sql
-- usage_ledger：预算校验/用量聚合（月分区索引，父表创建后 ATTACH 各分区）
CREATE INDEX IF NOT EXISTS idx_usage_ledger_api_key_ts ON usage_ledger (api_key_id, ts);
-- request_logs_hot：结算 LATERAL / 摘要门控 / 会话查询
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_session_ts ON request_logs_hot (gw_session_id, ts DESC);
-- sticky_sessions / request_envelope / system_probe_runs：清理与轮询
CREATE INDEX IF NOT EXISTS idx_sticky_sessions_expires ON sticky_sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_request_envelope_expires ON request_envelope (expires_at);
CREATE INDEX IF NOT EXISTS idx_system_probe_runs_started ON system_probe_runs (started_at);
-- routing_audit_log / armor_judgments：保留期删除排序下沉
CREATE INDEX IF NOT EXISTS idx_routing_audit_log_ts ON routing_audit_log (ts);
CREATE INDEX IF NOT EXISTS idx_armor_judgments_created ON armor_judgments (created_at);
-- session_summaries：健康巡检/近窗扫描
CREATE INDEX IF NOT EXISTS idx_session_summaries_last_req_unhealthy
    ON session_summaries (last_request_at) WHERE health_score IS NULL;
CREATE INDEX IF NOT EXISTS idx_session_summaries_gw_session ON session_summaries (gw_session_id);
-- session_turns hot+分区：digest 回填
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_digest_backfill
    ON session_turns_hot (ts, id) WHERE digest IS NULL;
-- api_keys 常用过滤（A1）
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant_enabled ON api_keys (tenant_id, enabled);
CREATE INDEX IF NOT EXISTS idx_api_keys_app_tenant ON api_keys (application_id, tenant_id);
-- 小表约束补齐（A5，另需数据审计确认无重复后再加 UNIQUE）
CREATE INDEX IF NOT EXISTS idx_key_applications_fingerprint ON key_applications (fingerprint);
CREATE INDEX IF NOT EXISTS idx_routing_policy_tenant ON routing_policy (tenant_id);
```

### 六.2 其余高价值索引（收益：中）

- `credential_model_bindings (provider_model_id)`：孤儿清理反连接（modelcatalog/upsert.go:294）。
- `provider_error_details (updated_at)`、`routing_feedback_log (created_at)`：partition_manager 删除。
- request_logs 月分区 `(tenant_id, ts DESC)`（现仅有两个部分索引）：租户统计跨月退化防护。
- `model_probe_runs`/`request_context_attrs` 已有时间索引，删除语句改分批即可（六.5）。
- `auto_route_selections (ts)`（若不存在）：feature_stats 每日聚合。

### 六.3 函数索引/谓词统一（收益：低-中）

- `models_canonical (lower(canonical_name))`：替换 4 处 `lower(canonical_name)=lower($1)` /
  `ILIKE $1` 点查（admin/routing.go:5198、free_pool_extra.go:1667、auto_route_defaults.go:497、
  model_policies.go:555）。canonical_name 已由迁移 396 统一小写，亦可直接改写为 `=`。
- `modelbinding/resolver.go:109` EXISTS 改 `lower(ma.raw_name) = ANY($2::text[])`（调用方传小写数组）。

### 六.4 request_logs 父表无界查询加时间界（收益：高，与 F3 同模式）

- domains/analysis/optimizer.go:189（3 个相关子查询）、request_summary.go:125、
  hooks/goal/history_store.go:81、sessionsummary/system_prompt_prefix.go:174：
  统一追加 `AND rl.ts >= NOW() - INTERVAL '<会话生命周期上限>'`（建议 30d，与 F3 对齐）。

### 六.5 大表清理语句分批化（收益：高）

- bg/partition_manager.go:583（node_probe_runs）/ 613（request_context_attrs）/ 552 / 1315 / 1396 / 1432：
  改 `DELETE WHERE id IN (SELECT id … ORDER BY <time> LIMIT 5000)` 循环（audit_trimmer 范式）。
- settings/audit.go:115、domains/attachments 同款改造已做（F6）。

### 六.6 保留期缺口（收益：中，防膨胀）

新增 TTL：`stats_event_dedup`、`stats_event_inbox`、`durable_llm_tasks`（终态+N 天，事件表 ON DELETE CASCADE）、
`durable_llm_task_events`、`instance_heartbeats`、`center_commands`、`fault_events`、
`offline_activation_requests`、`runtime_telemetry_consent_events`、`upgrade_logs`、`system_probe_runs`。
挂入 bg/partition_manager 的 TTL 表册。

### 六.7 运行时 DDL 下沉（收益：中）

- domains/promptinjection/detector.go:413、domains/outputcompliance/checker.go:351/411 的
  CREATE TABLE + 种子下沉至 `sql/migrations/`；运行时仅 SELECT，缺失告警。

### 六.8 聚合重构（收益：中-高）

- stats_minute_rollup：10 条 INSERT..SELECT 合并为单次 CTE 扫描 + GROUPING SETS；
  并为该 worker 补 distlock（多副本双倍执行）。
- admin/analytics.go:903：避免对 routing_decision_log 明细逐行 `jsonb_array_length`，
  改用既有 routing_analytics_* 增量物化体系。
- maas/usage.go：三连扫合并为 GROUPING SETS 单查。
- bg/auto_index_refresher.go:199-229：数据修改 CTE（DELETE…RETURNING + INSERT…SELECT）
  单事务单次计算，或为 RefreshOnce 加单飞锁（已有 2026-09-10 双触发事故注释）。
- autoroute/index.go:520 latest_bucket 限定 `bucket > now() - interval '1 day'`。

### 六.9 分页现代化（收益：中）

OFFSET 深翻页 9 处（admin/logs.go:780、credential_routing_log.go:457、data_lifecycle_attachments.go:180、
output_compliance_handler.go:557、prompt_injection_handler.go:1188、user_profile.go:122、
provider_models.go:261/447、routing.go:3179）改 keyset `(ts, id)` 游标；
sessionaudit/approval_manager.go:243、clientprofile/store.go:161 同。

### 六.10 应用层下推/改写（收益：中）

- autoroute/recommend_v2.go:377：拼接键 ANY 改两列数组/VALUES join（sargable）；
  autoroute/index.go:473 泄漏探针同。
- exporter/training_exporter.go:282-363：`DATE(ts)` 改范围谓词；过滤/去重下推
  `DISTINCT ON (content_hash)`；pgx 流式消费。
- telemetry/dashboard_events.go:294-300：flush 改 pgx.Batch/CopyFrom。
- bg/bandit_flusher.go:101：逐凭据 UPDATE 合并 `UPDATE … FROM (VALUES …)`。
- durable/pending_outbox.go：两阶段化（先 claim 后事务外 Redis，再批量 DELETE `= ANY($1)`）。

### 六.11 schema 治理（收益：运维性）

- 权威 `sql/schema/01-schema.sql` 缺约 45 张被引用表（outbox_events、session_dim、
  request_state_transitions、journal_snapshot_receipts、durable 四表、hosted_task 三表、
  free_quota_tracker、supplier_error 系列等，均在 `sql/migrations/` 或 db/db.go ensure 中创建）。
  建议从真库 `pg_dump --schema-only` 重新对齐快照，避免下轮审计误判"表不存在"。
- 建议部署 PgBouncer 或按角色拆分连接池（32 conns × 31 pods ≈ 992/1000，扩容即触顶，db/db.go:44 注释自述）。

### 六.12 settings 热路径缓存按点切换（收益：中，有契约前置）

- 基础设施已就位（§五 F4：`CachedPlatformBool/String/Float` + 写路径失效钩子）。
- 候选调用点：`domains/session/v2/pipeline_hook.go:55/58`、`domains/session/v2/session_writer_v2.go:541/580/594`、
  `domains/streaming/redact_body.go:157/164`（每请求/每流式响应各 2-4 次 settings_kv 往返）。
- **前置**：逐键确认无"下一次调用必须立即可见"类钉桩测试（domains/hooks/compression
  session_cache_ttl_test.go 即反例）；确认后改用 Cached* 族即可。


## 七、复跑方式

```bash
cd .db-audit
python parse_schema.py     # 重新解析 sql/schema/01-schema.sql → tmp/tables.json|tsv
python extract_sql.py      # 重新提取全仓 Go SQL → tmp/sql_catalog.jsonl + 统计
python gen_docs.py         # 重新生成 docs/database/2026-09-17-db-audit/ 两份文档
```

## 八、遗留登记

- 无 EXPLAIN 实证：所有收益评级为基于索引定义、触发频率与行数量级的定性推断，待真库复核。
- 🔎 标记的代理线索（§四）未亲读复核，下轮按 playbook 证据纪律处理。
- storage/sqlite（lite 模式）仅做参数化抽查，未逐条分析。
- bg 包在本机（Windows）不可完整编译为既有环境限制，bg 内 3 处改动建议 CI（linux）复核。
