# 2026-09-05 PG 错误审计、修复落地与环境基准

> 事件：对 docker `llm-gateway-pg` 数据库日志做 24h 错误审计，定位并修复本项目（llm-gateway-go）相关的全部 SQL 错误，经 `deploy-local.sh` 部署 2.5.0.1940 并观察验证通过。
> 本文是该次审计的结论沉淀 + **本地环境基准**（后续会话/代理应以本文为环境事实来源），并列出下一阶段待执行事项。

---

## 1. 错误结论摘要（本项目相关，均已修复）

| # | 现象（PG/网关日志） | 根因 | 修正 | 验证结果 |
|---|---|---|---|---|
| 1 | `provider_error_aggregator: cache lookup failed for attribute source of relation 1148425`（每 tick 失败，9/3 起水印停滞，`provider_error_details` 停更 2 天） | `candidate_failure_logs` 历史月分区全部为 **citus columnar** 归档；聚合查询经 `candidate_failure_logs_unified`（hot UNION ALL 父表）读取视图合成的 **`source` 常量列**时，citus 13.3 属性映射崩溃（错误消息中的 "source" 即列名）。带 `c.source` 必崩，仅选具体列正常 | `bg/provider_error_aggregator.go`：事务内先用普通 CTAS 将源行暂存临时表 `provider_error_agg_src`（`source` 以 `NULL::text` 占位），聚合管道改读临时表；锁/聚合/水位推进仍单事务。顺带修复被旧 bug 掩盖的 Scan 缺陷（SELECT 5 列、Scan 4 目标，补 `endpoint_unknown`） | 水印 → 16483；details 5275 行；聚合器零错误 |
| 2 | `function ensure/promote_auto_route_selections_partition does not exist`、`relation auto_route_selections_hot/all does not exist` | `scripts/apply-db-revision-sequence.sh` 用**序列级单标记**，后追加的 650/656 被已有标记静默跳过 | 脚本改为**按文件**标记（`<序列名>:<basename>`），追加 650（父表补 `experiment_id/treatment/assignment_version/assignment_key_hash`，656 的前置） | 函数+2026_08/09/10 分区就绪；partition_manager 零错误 |
| 3 | `invalid input syntax for type json`（`model_integrity_events` INSERT，历史日志） | 网关 pgx 全局 `QueryExecModeSimpleProtocol`（`db/db.go:54`）：`[]byte` 参数被内联为 bytea 十六进制串，写 jsonb 列必炸 | 主路径 recorder 此前已修；本次修复残留的 `bg/integrity_probe_sink.go`（`string(contextJSON)` + `$11::text::jsonb`） | 预防性；该路径当前低频 |
| 4 | （部署阻塞）`cmd/gateway` 在 `CGO_ENABLED=0 GOOS=linux` 下编译失败：`conn.Exec undefined` | `storage/sqlite` 依赖 mattn/go-sqlite3；no-cgo 构建时 `SQLiteConn` 是无方法壳（`static_mock.go`） | ConnectHook 按 build tag 拆分：`schema_cgo.go`（真实执行 PRAGMA）/ `schema_nocgo.go`（连接即快速失败） | 两种构建模式均通过 |

**非本项目、未处理**（同一 PG 实例上 crm 等其他应用的噪音，见 §5 待办）：`employees` 表（`column e.employee_id does not exist` 613 次/天、`multiple assignments to same column "last_heartbeat_at"` 357 次/天）、`daily_kline`（`column "days" does not exist`）、`llm_usage_records` 不存在、`audit_logs` RLS 拒绝、`no schema has been selected to create in`、`agent_groups` 不存在、`canceling statement due to user request`。

---

## 2. 环境基准（local）

### 2.1 容器拓扑
| 组件 | 值 |
|---|---|
| PostgreSQL | docker `llm-gateway-pg`（镜像 `kx-citus-pg17:offline-arm64`，PG17 + citus 13.3 + citus_columnar 13.3），`127.0.0.1:5432`，数据盘 `~/kaixuan/postgres`，用户/库均为 `llm_gateway` |
| 网关实例 | docker `llm-gateway-local-8782`（镜像 `kx-llm-gateway-local:<seq>`），端口 `8782`，候选端口 `8781` |
| Redis | `nbjl-redis`（deploy 自动发现） |
| 共库其他应用 | `crm` 等库与本网关共用 PG 实例（日志噪音来源） |

### 2.2 部署流程（`scripts/deploy-local.sh deploy`）
```
bump-version（seq+1）→ 构建后端（CGO_ENABLED=0 GOOS=linux，注意 §3.5）
→ 前端 vite build → scripts/apply-db-revision-sequence.sh（SQL 修复序列）
→ go run ./cmd/gateway migrate（Go 迁移）→ 候选实例 8781 → /healthz /readyz /version 门禁
→ 受控重启切换 8782 → record_success
```
- DSN 源：`.env.local` 的 `LLM_GATEWAY_DATABASE_URL`；**其密码必须与 PG 容器 `POSTGRES_PASSWORD` 一致**（2026-09-05 曾过期导致部署连库失败，已同步；容器内 `docker exec llm-gateway-pg printenv POSTGRES_PASSWORD` 可比对）。

### 2.3 迁移体系（双轨）
- **Go 驱动轨**：`db/db.go` 各 ensure*（含 startup/up/*.sql 回放），记录于 `repository_schema_migrations`（本库至 646）。
- **SQL 修复序列轨**：`scripts/apply-db-revision-sequence.sh`，按文件记录于 `gateway_db_revision_sequences`（序列名 `session-summary-and-integrity-2026-09:<文件名>`）。**向 files[] 追加新文件即可被下次部署补跑；文件必须幂等**。当前序列：655/560/572/606/563/564/644/645/650/656。
- startup 目录中 647/649/651/652/653/654 **尚不在任何一轨**（见 §5 P1）。

### 2.4 关键 schema 事实
- `candidate_failure_logs`：父表分区（月），全部历史分区 **citus columnar**；hot 表 heap；统一视图 `candidate_failure_logs_unified` = hot UNION ALL 父表（合成列 `source`，合成列 `aggregation_id` 历史侧为 `COALESCE(aggregation_id, -id)` 负值）。
- `provider_error_aggregator_state.id=1.last_source_id`：聚合水印；首次成功 tick 已从 bigint-min 前进（历史负 id 行已补聚合）。
- `model_integrity_events.context`：**jsonb**（写作规范见 §3.2）。
- `auto_route_selections`：父表（含 650 的 4 个实验列）+ `_hot` heap + default/月分区 + ensure/promote 函数 + `auto_route_selections_all` 视图。
- 路由分析 MV：`routing_analytics_7d`/`routing_audit_summary_7d`，源为窄视图 `routing_analytics_source`（db.go `routingAnalyticsMVSQL`；`sql/migrations/startup/up/632_*.sql` 仅是 DBA 镜像，见 §3.4）。

### 2.5 健康态基线（回归参照，2026-09-05 06:49 CST 后；16:02 起随 4cbcee0a 构建恢复并加强）
> **⚠️ 2026-09-05 16:00 补记：PG 容器 09-05 00:17 重启后 stderr 不再进入 docker logs**（可用日志止于 09-04 16:04 CST，只读探针 `SELECT 1/0` 实测证实，详见外部噪音报告 §1.1）。下述第 1 条 PG 侧体检命令当前**失明不可用**，健康验证以第 2、3 条（网关侧日志 + 水印）为准；日志管道修复（需重建 PG 容器，本次受约束未执行）是 DBA 待办。
- PG 日志（经网关侧观察等效）：**不应再出现**本项目相关 ERROR（§1 表中 4 类）；允许残留 §1 末段所列其他应用噪音。
- `provider_error_aggregator_state.last_source_id` 随每 tick（默认 10min）单调前进；`provider_error_details.updated_at` 持续刷新。
- partition_manager / MV refresher / 启动迁移：零 ERROR（node_probe "not eligible" 已于 16:02 起 4cbcee0a 构建根治，见 §6）。
- 快速体检命令：
```bash
docker logs llm-gateway-pg --since 30m 2>&1 | grep ERROR | sort | uniq -c
docker logs llm-gateway-local-8782 2>&1 | grep '"level":"ERROR"' | grep -v node_probe_worker
docker exec llm-gateway-pg psql -X -U llm_gateway -d llm_gateway -Atqc \
  "SELECT last_source_id, updated_at FROM provider_error_aggregator_state WHERE id=1;"
```

---

## 3. 已知陷阱（必读）
1. **citus columnar × UNION ALL 视图合成列**：对 `candidate_failure_logs_unified`（或任何经 columnar 分区的查询）引用合成列 `source` 会触发 `XX000 cache lookup failed for attribute source of relation`。规避：先普通 CTAS/简单投影暂存，复杂管道读暂存结果；不引用 `source`。修改 `bg/provider_error_aggregator.go` 的 SQL 时尤其注意（契约测试 `bg/provider_error_aggregator_contract_test.go` 已固化关键片段）。
2. **pgx SimpleProtocol 的 []byte**：全库池在 `db/db.go` 强制 `QueryExecModeSimpleProtocol`，`[]byte` 被内联为 bytea hex → 写 jsonb/json 列必炸。规范：`internal/dbx.NormalizeJSONB`，或 `string(...)` + SQL 里 `::text::jsonb`。新增 JSON 列写入时先查 `internal/trace/stage_events.go`、`proxy/store_pg.go` 的既有注释。
3. **修复序列标记机制**：序列文件必须幂等；追加文件无需清理标记（按文件标记自动补跑）。历史教训：单标记会静默吞掉后追加的文件。
4. **632 SQL 镜像不一致旱雷**：`sql/migrations/startup/up/632_routing_analytics_materialized_view.sql` 仍从 `request_logs_with_current_month_without_customer_id` 读取（该包装视图缺 `origin_stage`），而实际行为由 db.go `routingAnalyticsMVSQL`（走 `routing_analytics_source`）决定。任何直接回放该 SQL 文件的做法会在缺列库上失败（2026-09-04 日志已出现）。
5. **构建**：部署后端 = `CGO_ENABLED=0 GOOS=linux`。mattn/go-sqlite3 在 no-cgo 下只有无方法壳——`storage/sqlite` 中一切依赖 `SQLiteConn` 方法的代码必须走 build tag 拆分（参见 `schema_cgo.go`/`schema_nocgo.go`）。darwin 本机 `go build ./...` 与部署构建不等价，验证以部署同参构建为准。
6. **版本 SSOT 漂移**：`version.json.build_seq` 与 `~/kaixuan/llm-gateway-go/bin/` 已发布序号可能脱节（他处部署所致）；`deploy-local.sh` 报 "release already exists" 时，手动跑 `scripts/bump-version.sh` 至空闲序号再部署。

---

## 4. 本次变更清单（待提交）
```
M  bg/provider_error_aggregator.go            # 临时表暂存重构 + NULL source + FOR UPDATE + Scan 5 列
M  bg/provider_error_aggregator_contract_test.go
M  bg/integrity_probe_sink.go                 # []byte→string + ::text::jsonb
M  scripts/apply-db-revision-sequence.sh      # 按文件标记 + 追加 650
M  storage/sqlite/schema.go                   # ConnectHook 抽出
?? storage/sqlite/schema_cgo.go               # 新增（cgo 钩子）
?? storage/sqlite/schema_nocgo.go             # 新增（no-cgo 快速失败）
M  VERSION / version.json / web/public/version.json / web/public/menu-config.json  # 部署副产物（版本号/时间戳）
```

---

## 5. 下一阶段待执行事项（按优先级）——**2026-09-05 下午第二阶段已全部完成**，勾销如下
- ~~**P0 提交入库**~~ ✅ 已提交：`3344f3f17`（修复主体）+ `6f61d68c2`（版本副产物）；后续 `dc3c43015`（迁移补齐+node_probe 降噪，A/B 子代理产出）与 `5731a585`（jsonb 静态扫描修复 10 处，D 子代理产出）均已推上 origin/main。
- ~~**P1 迁移补齐审计**~~ ✅ 结论：647/649 有 Go 等价（`ensureGoalClientSignalSchema` / `ensureRoutingAnalyticsMaterializedViews`+fast-path 重建），不入序列；651/652/653/654 无等价且幂等，已入修复序列并于 15:45 部署时在存量库**首次补跑成功**（`gateway_db_revision_sequences` 按文件标记齐全）；632 已重写为与 `routingAnalyticsMVSQL` 对齐的可重放版本（走 `routing_analytics_source`）。
- ~~**P1 node_probe_worker 降噪**~~ ✅ 双管齐下：pump 候选 SQL 内联资格预筛（不合格行不再被选出）+ 确定性 gate 拒绝不消耗重试（单条 Info + `skipped_*` 指标）。16:02 起 4cbcee0a 构建上线，刷屏归零。
- ~~**P2 跨应用日志噪音清单化**~~ ✅ 交付 `docs/2026-09-05-external-pg-noise-attribution.md`（8 类归属 + canceling statement 专节：553 条全部为本网关自身查询被客户端取消，主因 admin 节点矩阵查询 1500ms 预算，见其 §3.3 建议）。
- ~~**P2 `canceling statement due to user request` 归因**~~ ✅ 并入上条报告 §3。
- ~~**P3 观测与防回归加固**~~ ✅ a) `scripts/monitoring/pg-error-classifier.sh`（project/external/unknown 三类打标 + TSV/JSONL + 退出码 2 告警；**注意：PG docker logs 修复前该脚本无输入可分类**，已在历史窗口实测验证）；b) 契约测试新增禁止聚合 SQL 引用视图合成列 `source`（保留 `NULL::text AS source` 占位）；c) `internal/dbx/jsonb_param_static_test.go` 全仓静态扫描 []byte→jsonb（934 文件 0 违例，行注释 `// dbx:jsonb-safe` 可豁免），并修复扫出的 10 处真实违例。

## 6. 第二阶段补记（2026-09-05 下午，环境事实更新）

### 6.1 镜像回退事故与多副本部署竞争（重要环境事实）
- 本仓库（syncfield/llm-gateway-go-2）**不是唯一构建部署源**：至少存在另一工作副本从 origin 拉代码构建并部署同一容器 `llm-gateway-local-8782`，双方共享 `~/kaixuan/llm-gateway-go/bin/` 发布目录与版本序号空间，且各自 bump（出现过 2.4.7.1940/1942/1944 与 2.5.0.1941/1942 并行的双版本线）。
- 06:49 验证通过的修复版 2.5.0.1940 在 5 分钟内（06:54）被另一副本构建的 **2.4.7.1940（不含修复，二进制无 `provider_error_agg_src` 标记）** 顶替，导致聚合器 XX000 每 10min 失败持续约 9h、水印停在 16483。15:45 本阶段主代理部署 2.5.0.1941（git 5731a585）恢复；15:53、16:02 对方流水线又两度重建，16:02 起 `2.5.0.1942`（git 4cbcee0a，origin/main 已含本阶段全部提交）为当前运行版本，**全部修复生效**。
- **教训**：排查"修复为何复发"先 `docker inspect .Config.Image` + `strings 容器内 /opt/llm-gateway-go/gateway` 验二进制标记，再查代码；`/version` 的 git_sha 是判定运行版本的事实源。

### 6.2 新发现的待办（非本阶段范围，按优先级）
1. **PG 容器日志管道断裂**（P1，DBA）：09-05 00:17 重启后 stderr 不再进 docker logs。需重建容器验证（本次受"不重启 llm-gateway-pg"约束未执行）；修复前 `pg-error-classifier.sh` 与 §2.5 第 1 条体检命令无输入。
2. **`supplier_error_stats` 缺表**（P1，闭环1 遗留）：`bg/supplier_error_stats_aggregator.go` 每 10min rollup 失败（42P01，WARN 级有 fallback），全仓无该表 ensure/迁移——与 647-654 同类的"迁移缺口"，趋势 API（/api/errors/trend）读源为空。
3. **telemetry 数字溢出**（P2）：`telemetry request db persist failed — numeric field overflow (SQLSTATE 22003)`，WARN 有 fallback，telemetry 列精度需加宽。
4. **admin 节点矩阵查询 1500ms 预算不足**（P2）：canceling 噪音主因，见外部噪音报告 §3.3（加索引/降采样/放宽预算三选一）。
5. `bin/current` 符号链接与运行容器可能不一致（双流水线竞争，16:02 时指向 2.4.7.1942 而容器跑 2.5.0.1942）——排查时以容器为准。
