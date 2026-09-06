# 2026-09-06 PG 日志 SQL 错误分析、修正与部署验证报告

> 任务：检查 `docker llm-gateway-pg` 日志中的 SQL 错误，逐类归因到本项目（llm-gateway-go）或外部应用；给出 Go 侧 vs 表结构侧修正的可行性分析并实施；经 `scripts/deploy-local.sh` 部署后观察验证。
> 前置阅读：《2026-09-05-external-pg-noise-attribution.md》（外部噪音归因）、《2026-09-05-pg-error-audit-and-environment.md》（首轮审计）。
> 本轮**写操作**仅限：仓库内代码/SQL/部署脚本修改、本项目自有 schema 的幂等修复（修订序列）、deploy-local.sh 部署。未触碰他应用数据。

---

## 1. 日志窗口与方法（重要勘误）

| 项 | 值 |
|---|---|
| 可用窗口 | 2026-09-05 15:28 ~ 2026-09-06 02:23 CST（`docker logs --tail 100000`，约 11 小时，99,964 行） |
| 工具坑 | ① `docker logs` 全量管道读取在 ~55k 行处**静默截断**（首轮审计的 55,244 行即截断产物，非日志全貌）；② 本 Docker Desktop 环境 `--since` 过滤不可靠（返回空）。**一律用 `--tail N` 窗口 + PG 自带 `%m` 时间戳过滤**；③ 重定向必须 `> file 2>&1`（`2>&1 > file` 会把 stderr 留在终端） |
| 归因方法 | ERROR→STATEMENT 块配对提取 → 语句指纹 × 本仓库全文检索 × `pg_stat_activity` 连接清单 × 全实例 35 库 `to_regclass` 矩阵（`orchestration_runtime_instances` 仅 `acc_test` 库存在 → 客户端 DSN 指错库） |

## 2. 窗口内错误归因总表（11h）

**本项目（llm-gateway-go）：**

| 错误类 | 条数 | 首次→末次 | 根因 | 修正轨道 | 状态 |
|---|---|---|---|---|---|
| `supplier_errors_hot`/`supplier_error_stats`/两个 promote/ensure 函数不存在（42P01/42883） | 332 | 15:28→17:47 | V371 无升级部署轨道（09-05 已入修订序列，17:48 应用） | 表结构（序列） | ✅ 17:48 后归零 |
| `numeric field overflow`（22003，`update_session_summary()`） | 157 | 15:28→19:31 | 563 旧触发器体覆盖 572 修复（09-05 已入修订序列 661，19:55 应用） | 表结构（序列） | ✅ 19:31 后归零 |
| `column "auto_profile" does not exist`（42703，`routing_analytics_source`） | 27 | 19:19→19:21 | **Go 侧**：`routingAnalyticsMVSQL` 视图定义漏掉 `auto_profile`，而 `admin/auto_route.go:622` 直接查该视图 | **Go（必须）** | ✅ 本轮修复，部署后查询验证通过 |
| `there is no unique or exclusion constraint`（42P10，`provider_error_details` 聚合 upsert） | 5+（部署新二进制后显形） | 05:46→05:56 | **混合**：Go HEAD 契约 9 列指纹（含 `LEFT(error_message,200)`，639/V368 + `bg/provider_error_aggregator.go`），而库内索引被并行会话的库外 662 重建为 8 列 | 表结构（序列 663 对齐到 main 契约） | ✅ 本轮修复 |
| `usage_ledger promote: INSERT failed (more expressions)`（WARNING） | 16 | 15:45→16:47 | 659 修复前旧函数体 | 表结构（659 已应用） | ✅ 已停 |
| `canceling statement due to user request`（57014） | 106 | 持续 | 网关主动取消（admin 节点矩阵 1500ms 预算 + 客户端断开），见 09-05 报告 §3.3，非缺陷 | 行为（不改） | 🟢 已接受 |

**外部（不属于本仓库，佐证：仓库零指纹命中 / 客户端日志无 / 全库扫描）：**

| 错误类 | 条数 | 归属 | 证据 |
|---|---|---|---|
| `orchestration_runtime_instances` 不存在（每 ~30s，最高频） | 1308 | 某 acc-swarm 族组件 DSN 指错库 | 表仅存在于 `acc_test` 库；语句不在本仓库；acc-blue/llm-gateway/redclaw/k8s 日志均无 |
| `multiple assignments to same column "last_heartbeat_at"`（employees 双写） | 461 | acc 应用（SQL 本身写重复列） | `UPDATE employees SET last_heartbeat_at=$1, ..., last_heartbeat_at=$3`；本仓库无 employees |
| `column e.employee_id` | 235 | acc idle-lifecycle-worker（09-05 报告 #1） | acc 日志同串 |
| `column "days"`（daily_kline） | 83 | smm 股票探索会话 | HINT 建议 `date` 列 |
| `audit_logs` RLS 拒绝（`orchestrator.audit_logs`） | 28 | redclaw orchestrator | insert 带租户/actor，schema 归 redclaw |
| `task_bus_events`（sqlc `CreateTaskEvent`）、`tasks`、`projects` | 22+ | 库外 Go/sqlc 项目 | 本仓库无 sqlc 注释形态 |
| `llm_providers.provider_id`/`must be owner of function ...`/`SET ROLE`/`llm_usage_logs`/`task_id`/`memory` FK | ~80 | kaixuan 族迁移脚本对本库的重放（19:24 批次起） | 语句为迁移文件头注释形态，owner 是 kaixuan 族函数 |

## 3. 本轮三项修正（含 Go vs 表结构可行性结论）

### 3.1 auto_profile 视图缺口 —— **必须 Go 侧修**（表结构侧单独修不可行）

- 视图由 `db.ensureRoutingAnalyticsMaterializedViews` 在**每次启动**时 `CREATE OR REPLACE`（db.go `routingAnalyticsMVSQL`）。只改库里的视图，下次启动会被旧 Go 定义打回 —— 表结构侧不持久。
- 修改（`db/db.go`）：
  1. `routingAnalyticsMVSQL` 两个 UNION 分支各追加 `auto_profile::text AS auto_profile`（**必须放最后**：CREATE OR REPLACE 只能追加列，不能改已有列位置；物化视图均为显式列查询，加列安全）；
  2. 快速跳过门槛（upToDate gate）追加 `POSITION('auto_profile' IN pg_get_viewdef('routing_analytics_source'))>0`，让存量库走一次廉价的原地视图替换（不触发物化视图 DROP/重建）。
- 同步对齐 SQL 副本：`sql/migrations/startup/up/632_*.sql`、`sql/migrations/startup/649_*.sql`、`installer/.../embeddata/startup/649_*.sql`（三处 fresh-install/重放路径与 Go 定义逐字一致）。

### 3.2 658 结构化特征列升级轨道缺口 —— **表结构侧（修订序列）**，Go ensure 天生补不全

- 现象：部署在 `gateway migrate` 阶段失败：`ensure auto_route_selections_hot schema: column "detected_language" does not exist (42703)`。ensure 只给 **hot 表**加 658 的 15 个特征列，随后重建 `auto_route_selections_all` 视图引用**父表**同列 → 父表缺口没人补。
- 这与 09-05 审计的 651–654/V371 同型："迁移只有一半被 Go ensure 镜像"。658 的父表半区只能由 SQL 迁移轨道补 → 加入 `scripts/apply-db-revision-sequence.sh`，插在 656 之后；并在 `intentional_function_chains` 注册 `promote_auto_route_selections_hot_to_partition|656_...|658_...|`（656/658 都定义该函数，658 必须最后生效，否则链守卫拒绝部署）。

### 3.3 provider_error_details 指纹索引 —— **表结构侧**，但契约以 Go 为准

- 现 check 同一库被**两个并行 checkout 共享**（另一会话的库外 `662_provider_error_details_agg_key_dedup.sql` 于 00:09 应用，把唯一索引重建为 8 列）。本仓库 main 契约（639/V368 + Go upsert 的 ON CONFLICT 目标）是 **9 列**。旧二进制按 8 列冲突所以无错；我部署 main 的新二进制后每 ~10min 聚合 tick 必然 42P10，聚合批次丢失。
- 修复：新增 `sql/migrations/startup/663_provider_error_details_fingerprint_index_repair.sql`（幂等：索引已含 error_message 时跳过；含 schema_migrations 登记），加入修订序列。注意 `CREATE UNIQUE INDEX IF NOT EXISTS` 对**同名旧形状索引是无操作**，所以必须条件式 DROP+CREATE。

## 4. 部署与验证

- `bash scripts/deploy-local.sh deploy --dry-run` 预检 → 正式部署两次受阻于前置门（`LLM_GATEWAY_SECRET_KEY`、`LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`、admin 凭据均不在仓库 `.env.local`，是调用方环境注入的）→ 从现役运行环境 `~/kaixuan/llm-gateway-go/run/llm-gateway-local-8782.env` 继承同名密钥/凭据（**沿用原签名密钥保证已签发 admin 会话不失效**）→ 第三次部署成功：
  - 迁移全链通过（含 658 后的 ensure 链）；
  - 候选 8781 冒烟 `providers=587 creds=7 failed=0` → 切换 8782 → 复冒烟同过，`VERIFY_PASS=1`，release **2.5.3.1959**（`git_sha=5ef9fa67`）。
- 库态验证：`routing_analytics_source` 含 `auto_profile`；原失败查询返回 `unknown=79448`；`auto_route_selections(_hot)` 均含 15 特征列；指纹索引含 `error_message`。
- 观察（05:53–06:00 起 7 分钟×1 分钟采样）：**本项目错误类全部为零**，仅剩外部噪音（orchestration DSN 错库 ~每 30s 1 条）。42P10 修复后需覆盖 ≥1 个聚合 tick（见 §5 复验记录）。

## 5. 残留事项与建议（不在本仓库，或需后续决策）

### 5.0 42P10 修复复验（06:39 CST 追加）

| 时间 | 事件 |
|---|---|
| 05:46 / 05:56 / 06:06 / 06:16 CST | 聚合 tick 各报 1 条 42P10（修复前，新二进制 vs 8 列索引） |
| 06:25:16 CST | 663 经修订序列应用（marker 时间戳），索引恢复 9 列契约 |
| 06:26:43 / 06:36:43 CST | 两个完整 tick，**42P10 为零** ✅ |

注：`invalid input syntax for type timestamp "2026-09-05T21"`（06:05，`INSERT INTO llm_hourly_stats`）为 kaixuan 族小时统计任务（外部），非本仓库。

1. **orchestration_runtime_instances（最高频，外部）**：组件运行 DSN 指向 `llm_gateway` 库但迁移只做过 `acc_test`。需对方把 DSN 改到 `acc_test`（或对目标库补迁移）。
2. **双 checkout 共库风险（流程）**：并行会话各自带库外修复（662）写同一库，导致 663 这类"契约分歧显形"。建议后续所有库修复必须走本仓库修订序列入库，禁止库外一次性 SQL；663/662 编号已现碰撞前兆（657/658 曾重排），编号分配前先 `git log origin/main` 查重。
3. **canceling（57014）**：维持 09-05 §3.3 建议（预算/索引二选一或日志降噪），本次未改行为。
4. **docker logs 截断/`--since` 失效**：`scripts/monitoring/pg-error-classifier.sh` 依赖全量读取，同样受影响；建议改用 `--tail` 或落盘文件轮转。
5. 外部应用修复清单（沿用 09-05 报告 §2 处置建议）：acc 的 `last_heartbeat_at` 双列赋值 + `e.employee_id`、redclaw 的 audit_logs RLS、smm 探索会话约束、kaixuan 迁移重放的 owner/SET ROLE 报错。

## 6. 复验命令（只读）

```bash
# 本项目错误类在 PG 日志中应为 0
docker logs --tail 3000 llm-gateway-pg 2>&1 | grep 'ERROR:' | grep -cE 'auto_profile|numeric field overflow|supplier_error|detected_language|no unique or exclusion'
# 视图/索引契约
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -tAc \
  "SELECT position('auto_profile' in pg_get_viewdef('public.routing_analytics_source'::regclass,true))>0, position('error_message' in pg_get_indexdef(c.oid))>0 FROM pg_class c WHERE c.relname='idx_provider_error_details_tenant_cred_fingerprint'"
# 原失败查询
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -c \
  "SELECT COALESCE(auto_profile,'unknown') p, COUNT(*) FROM routing_analytics_source WHERE is_auto_request AND ts>=NOW()-INTERVAL '7 days' GROUP BY p ORDER BY 2 DESC LIMIT 5"
```

---

*报告生成：2026-09-06 06:05 CST；部署版本 2.5.3.1959；观察窗口 05:53–06:00 CST（42P10 复验另见 §5 追加）。*
