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

### 2.5 健康态基线（回归参照，2026-09-05 06:49 CST 后）
- PG 日志：**不应再出现**本项目相关 ERROR（§1 表中 4 类）；允许残留 §1 末段所列其他应用噪音。
- `provider_error_aggregator_state.last_source_id` 随每 tick（默认 10min）单调前进；`provider_error_details.updated_at` 持续刷新。
- partition_manager / MV refresher / 启动迁移：零 ERROR（`node_probe_worker "not eligible"` 为既有业务提示，见 §5 P1）。
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

## 5. 下一阶段待执行事项（按优先级）
- **P0 提交入库**：§4 变更 review 后 commit（建议拆两个 commit：修复主体 + 版本/部署副产物，或合一并注明）。
- **P1 迁移补齐审计**：逐个核对 647/649/651/652/653/654 —— 是否已有等价 Go ensure（如 655 之于 db.go）？无则评估幂等性后加入修复序列；同步消除 §3.4 的 632 镜像旱雷（改写 SQL 文件与 db.go 对齐或在文件头声明只读告警）。
- **P1 node_probe_worker 降噪**：`"automatic probe task is not eligible"` 每次启动刷 ~75 条 ERROR。入队前先做资格预检，或降级 WARN + 去重。
- **P2 跨应用日志噪音清单化**：对 §1 末段所列其他应用错误产出归属报告（库/表/来源应用/建议）；仅当对应仓库本地可得且明确授权时才修改对方代码，否则只交付报告。
- **P2 `canceling statement due to user request` 归因**：抓取带 STATEMENT 的样本定位来源（本项目 statement_timeout vs 其他应用）。
- **P3 观测与防回归加固**：a) PG 日志 ERROR 归属监控（脚本或 Prometheus 指标：按错误类别打标 project/external）；b) 防回归测试：聚合器 SQL 禁止引用视图合成列、全仓静态扫描“`[]byte` 直接作为 jsonb 列参数”。
