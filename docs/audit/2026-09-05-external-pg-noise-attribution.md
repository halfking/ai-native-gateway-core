# 2026-09-05 跨应用 PG 日志噪音与超时归因报告

> 任务：对共享实例 `docker llm-gateway-pg` 日志中**非本项目（llm-gateway-go）**的错误做逐类归属（目标库/表、来源应用与连接特征、证据、处置建议），并对 `canceling statement due to user request` 单独判定是否涉及本网关。对应《2026-09-05-pg-error-audit-and-environment.md》§5 两条 P2 待办。
> 本报告**严格只读**：仅使用 `docker logs`、`docker exec ... psql`（SELECT / information_schema / pg_catalog / pg_stat_activity）、`docker inspect`（环境变量，密码脱敏）与本仓库代码检索；未写入任何库、未改动任何容器与他应用文件。

---

## 0. 已知本项目残留（不计入外部噪音）

- `XX000 cache lookup failed for attribute source of relation`：窗口内 121 条（约每 10 min 一条，与聚合器 tick 同频）。系**旧镜像回归**（citus columnar × 视图合成列），2026-09-05 已在 `bg/provider_error_aggregator.go` 修复并随 2.5.0.1940 部署，主代理统一收尾，见审计文档 §1#1。
- `column "model_name" does not exist`（`INSERT INTO provider_metrics_minute`）：37 条，**仅出现在窗口最初 ~1 小时**（09-03 22:54~23:59 CST）后消失。该表属本仓库（`internal/quality/minute_aggregator.go`），为旧镜像残留，非外部噪音。
- 其余 §1 表中 4 类本项目错误按审计文档已修复，本窗口未见复发。

## 1. 概述：窗口 / 方法 / 总量

### 1.1 实际可用日志窗口（重要，非 48h）

| 项 | 值 |
|---|---|
| `docker logs llm-gateway-pg` 实际内容范围 | **2026-09-03 22:54:30 CST ~ 2026-09-04 16:04:08 CST**（UTC 09-03T14:54:30Z ~ 09-04T08:04:08Z），约 **17.2 小时**，共 55,244 行 |
| 窗口受限原因 | PG 容器 2026-09-05 00:17 CST 重启后 **stderr 不再进入 docker logs**。活体探针证实：09-05 15:47 CST 执行只读 `SELECT 1/0;`（ERROR 级必被记录，`log_min_messages=warning`），docker logs `--since` 无任何输出 |
| 影响 | a) 本报告统计窗口以上述 17.2h 为准；b) `scripts/monitoring/pg-error-classifier.sh` 同样失效（实测报 `no log input: container 'llm-gateway-pg' produced no output in the last 30m`）；c) 审计文档 §2.5 的「快速体检命令」对 PG 侧已失明。**建议主代理/DBA 优先修复容器日志管道**（`logging_collector=off`、log_destination=stderr 均未变，疑为重启后 PID1 stderr fd 未接 docker log-driver，需重建容器验证） |

### 1.2 方法

1. `docker logs`（不带 `--timestamps`，PG 自带 `%m [%p]` 前缀，UTC 转 CST 需 +8h）全量拉取 → ERROR/FATAL 消息归一化计数（`sort | uniq -c`）；
2. 8 类目标错误逐类 `grep -A` 抽取 STATEMENT 块，按语句指纹聚 类；
3. 库/表归属：遍历实例全部 35 个库的 `information_schema.tables/columns` 建立矩阵，与 STATEMENT 引用的表/列比对（日志前缀**不含 db/user**，无法逐行定库，归属靠「语句所需列集合 × 各库 schema」推断 + 应用侧日志/环境变量佐证，逐类标注置信度）；
4. 连接特征：`pg_stat_activity` 连接清单 + 高频采样（1s×180 次、200ms×700 次两轮，均未捕获活跃外部查询——窗口期噪音客户端当前已静默）；`docker inspect` 各容器 DB 环境变量（密码脱敏）；
5. 本网关比对：本仓库代码全文检索 STATEMENT 指纹；网关侧日志（容器 stdout 仅覆盖今日 15:02 CST 起；宿主机 `~/kaixuan/llm-gateway-go/logs/` 轮转文件仅覆盖今日 00:02 CST 起）——**与 PG 可用窗口（止于 09-04 16:04 CST）无时间重叠**，跨比以代码级指纹证据为主，见 §3。

### 1.3 总量（窗口 17.2h）

| 类别 | 条数 | 折合速率 |
|---|---|---|
| ERROR 总计 | 2,952 | ~172 条/h |
| FATAL 总计 | 1,623（口令失败 1,352 + 库不存在 237 + 启动中 32 + 未就绪 2） | ~94 条/h |
| 其中本网关残留（§0） | 158（cache lookup 121 + model_name 37） | 已修复，窗口后段归零 |

## 2. 逐类归属

### 2.1 汇总表

| # | 错误类别 | 窗口条数 | 目标库/表 | 来源应用与连接特征 | 置信度 | 处置建议 |
|---|---|---|---|---|---|---|
| 1 | `column e.employee_id does not exist` | 920（char23 版 878 + char86 版 42） | `acc_db.employees`、`acc_db.employee_agent_configs` | **acc 应用（容器 acc-blue）的 `idle-lifecycle-worker`**，每分钟 tick 轮询 warm_idle 员工 | **高**（acc 应用日志同串报错、时间窗完全重叠；acc 仅连 acc_db：`ACC_DB_NAME=acc_db`，活跃连接 `acc_app@acc_db`） | **修他应用代码**：worker 的 SQL 按 kaixuan 族 employees schema 书写（`COALESCE(e.id, e.employee_id)`），而 acc_db.employees 主键列为 `id`、无 `employee_id`。需 acc 仓库授权后改 SQL（`e.id` 即可）；本次不改 |
| 2 | `multiple assignments to same column "last_heartbeat_at"` | **0**（审计文档口径 357/天） | （历史）acc/kaixuan 族 `UPDATE employees ... SET last_heartbeat_at=..., last_heartbeat_at=...` | 窗口内未发生；仅存 `task_assignments` 心跳收割 SELECT 引用 `last_heartbeat_at`（见 #5 同源） | — | **无害可忽略**（已消失）；如复发按 #1 渠道找 acc/kaixuan 侧修复 UPDATE 列重复 |
| 3 | `column "days" does not exist`（daily_kline） | 149 | `smm_data.daily_kline` | **smm/股票数据探索会话**：同一句分析型 SQL 短间隔换 PID 反复执行（03:41 CST 起突发），同秒伴随 `postgres`/`xutaohuang` 口令试错 FATAL——交互式/编码代理式连库探索，非驻留服务 | 中 | **实例侧缓解 + 修他应用**：SQL 应写 `HAVING COUNT(*) >= 30`（别名不能用于 HAVING）；建议 DBA 侧对分析型连接受限/单独账号。他应用/工具侧修 SQL |
| 4 | `relation "llm_usage_records" does not exist` | 17 | 目标库缺表（全实例仅 `kaixuan` 库有此表；`acc_db`/`crm`/`llm_gateway` 均无） | 小时报表任务：每小时 `:00/:05` 准点一条 `SELECT * FROM llm_usage_records WHERE created_at >= $1 AND created_at < $2` | 中低 | **修他应用配置**：疑似 kaixuan/crm 侧组件 DSN 指错库（本仓库 `.env.local` 中 `KAIXUAN_DATABASE_URL` 即指向 `${LOCAL_PG_CRM_DATABASE}`=crm，而 crm 库无任何相关表）。需对方仓库确认目标库 |
| 5 | `relation "agent_groups" does not exist` | 9 | 目标库缺表 | **监控/巡检族**：`agent_groups` 与 `audit_hash_chain` 统计在**同一秒、相邻 PID**（[521]/[523]）共发，15 分钟周期——同一客户端一次巡检连发多表探测；连带同族缺表：`task_assignments` 213、`audit_hash_chain` 197、`documents/documents.*` 82、`memory` 31、`knowledge_bases` 27、`user_profiles` 13 等。嫌疑主对象：**redclaw 栈**（5 组件 `DB_NAME=llm_gateway`，`platform_app` 角色当前 24 条空闲连接挂在 llm_gateway 库，而该库 public schema 无上述任何表；其自有对象集中在 `orchestrator/dal/authagent/fencing/workflow/integration` 等 schema） | 中（redclaw 方向）；「巡检客户端连错库」总体判定为高 | **修他应用代码/配置**：redclaw 组件应指向其实际迁移过的 schema/库，或补齐迁移；未授权不改。另 `memora`/`pocket` 族缺表（`memora.auth_users`、`chat_sessions` 等）各 1~7 条，量小同因 |
| 6 | `audit_logs` RLS 拒绝类 | **0**（`row-level security` 精确匹配为 0；真实 `permission denied` 亦为 0） | — | 窗口内不存在 RLS 拒绝。实际 audit 相关噪音为缺表/列不匹配：`orchestrator.audit_logs` 查询报 `column "created_at"/"action" does not exist`（表在 llm_gateway 库存在但列集不同，04:05 CST 周期出现）+ `public.audit_logs`/`audit.rls_violation`/`audit_alerts` 缺表 | 中高（audit 列不匹配归 redclaw 监控族） | **无害可忽略为主**；`orchestrator.audit_logs` 列不匹配随 #5 一并由 redclaw 侧修 |
| 7 | `no schema has been selected to create in` | 20 | 目标库 search_path 为空的会话 | **一次性引导脚本**：09-04 09:14:21~09:14:36 CST 共 14 秒内 20 连发（3 类 DDL 各路重试）：`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, username ..., password_hash ..., role ...)` + `ALTER TABLE users ADD COLUMN IF NOT EXISTS email/email_verified`。此后未再发生 | 中（应用名未定；users+password_hash 形态与 pocket/openpocket 族认证引导最吻合） | **无害可忽略**（一次性、已自愈）；建议该引导脚本显式 `SET search_path` 或 DSN 里带 `options=-csearch_path=<schema>` |
| 8 | `canceling statement due to user request` | 553 | **全部为本网关 llm_gateway 库对象**（credentials / provider_models / session_turns / request_logs_hot / request_logs_bodies_hot / session_bodies_hot / node_probe_state / route_incident_events / stats_event_dedup / 网关自建函数） | **本网关自身**（详见 §3 专节） | **高** | **本网关优化**（见 §3.3） |

### 2.2 证据样本（脱敏，时间已转 CST）

**#1 employee_id（v1，char 23）**
```
2026-09-03 23:05:15.762 CST [935] ERROR:  column e.employee_id does not exist at character 23
STATEMENT:  SELECT COALESCE(e.id, e.employee_id) AS agent_id,
                     e.agent_type, e.idle_ttl_sec, e.last_activity
            FROM employees e
            WHERE e.lifecycle_tier = 'warm_idle' AND e.status = 'online' ... LIMIT $1
```
acc 应用侧同串报错（`docker logs acc-blue`，487 条，窗口 09-03 23:50 ~ 09-04 16:04 CST 与 PG 侧完全重叠）：
```
[idle-lifecycle-worker] tick error: column e.employee_id does not exist
```
**#1 employee_id（v2，char 86，JOIN 版）**
```
2026-09-03 22:58:15.646 CST [388] ERROR:  column e.employee_id does not exist at character 86
HINT:  Perhaps you meant to reference the column "eac.employee_id".
STATEMENT:  SELECT eac.employee_id FROM employee_agent_configs eac
            JOIN employees e ON e.employee_id = eac.employee_id
            WHERE eac.tenant_id = $1 AND eac.activated = true ...
```

**#3 daily_kline**（同秒伴随口令试错）
```
2026-09-04 03:41:07.844 CST [19885] ERROR:  column "days" does not exist at character 69
HINT:  Perhaps you meant to reference the column "daily_kline.date".
STATEMENT:  SELECT code, COUNT(*) as days FROM daily_kline GROUP BY code HAVING days >= 30 ORDER BY days DESC LIMIT 10
2026-09-04 03:41:13.201 CST [19955] FATAL:  password authentication failed for user "postgres"
2026-09-04 03:41:20.425 CST [20027] FATAL:  password authentication failed for user "xutaohuang"
```

**#4 llm_usage_records**（每小时准点）
```
2026-09-03 23:05:00.043 CST [990] ERROR:  relation "llm_usage_records" does not exist at character 15
STATEMENT:  SELECT * FROM llm_usage_records
            WHERE created_at >= $1 AND created_at < $2 ORDER BY created_at
```

**#5 agent_groups + audit_hash_chain 同秒共发**
```
2026-09-03 23:30:05.146 CST [521] ERROR:  relation "agent_groups" does not exist at character 110
STATEMENT:  SELECT group_id, name, description, created_by, status, config, metadata, created_at, updated_at
            FROM agent_groups WHERE status = 'active' ORDER BY created_at
2026-09-03 23:30:05.148 CST [523] ERROR:  relation "audit_hash_chain" does not exist at character 98
STATEMENT:  SELECT COUNT(*) as cnt, COUNT(DISTINCT source_agent_id) as agent_count FROM audit_hash_chain
```

**#6 audit 族列不匹配**（orchestrator schema 在 llm_gateway 库内）
```
2026-09-04 04:05:21.440 CST [39489] ERROR:  column "created_at" does not exist at character 22
STATEMENT:  SELECT count(*), max(created_at) FROM orchestrator.audit_logs;
```

**#7 no schema**
```
2026-09-04 09:14:21.966 CST [116277] ERROR:  no schema has been selected to create in
STATEMENT:  CREATE TABLE IF NOT EXISTS users (
                id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
                password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'user', ...)
```

**#8 canceling**（样本见 §3）

## 3. `canceling statement due to user request` 归因专节

**结论：涉及本网关（llm_gateway 库）。553 条（窗口内）全部为本网关连接发起的查询被取消，未发现他应用证据。证据等级：高。**

### 3.1 证据

1. **STATEMENT 指纹 100% 命中网关 schema 对象**（全量 553 条按块提取指纹）：

   | 条数 | 语句指纹（首行） | 对应网关表/代码 |
   |---|---|---|
   | 128 | `SELECT c.id, COALESCE(c.label, ''), c.provider_id, ...` | `cmd/gateway/main_livestream.go:487`（admin 节点矩阵） |
   | 53+24+2 | `SELECT EXISTS ( ... FROM credentials c ...` | 网关凭证探测/路由 |
   | 21 | `SELECT cmb.credential_id, pm.raw_model_name ...` | `main_livestream.go:550` |
   | 11 | `SELECT COALESCE(MAX(turn_no), 0) + 1` | `domains/session/v2/turn_writer.go:221` |
   | 5+2+2+2+1+1+1 | `UPDATE request_logs_hot` / `UPDATE node_probe_state` / `UPDATE credential_model_bindings` / `INSERT INTO route_incident_events|request_logs_bodies_hot|session_bodies_hot|session_turns_hot|stats_event_dedup` | 网关记录/探测/事件写路径 |
   | 4 | （带 CONTEXT）`SQL function "orchestration_active_tenants"`、`PL/pgSQL function bump_candidate_binding_...`、`while inserting index tuple` | 网关自建函数/索引 |

   本仓库全文检索确认上述 SQL 文本逐字存在；同时检索 `employee_id/daily_kline/task_assignments/llm_usage_records/warm_idle` 均**无命中**——本仓库不产生 §2 其余外部噪音，两向印证。

2. **取消来源为客户端协议级 CancelRequest，非服务端超时**：实例 `statement_timeout=0`（全局关闭，实测 `pg_settings`）。网关侧仅两处主动超时机制：a) `main_livestream.go:484` 对节点矩阵查询设 **1500ms** ctx 预算——128 条主指纹即该查询超预算被 pgx 取消；b) `internal/dbx/vacuum_mutex.go:146` 对锁连接 `SET LOCAL statement_timeout`——对应 1 条 `pg_advisory_xact_lock` CONTEXT 样本。其余为请求 ctx 随客户端断开而取消（流式请求中断）。
   ```
   2026-09-04 07:42:54.463 CST [93167] ERROR:  canceling statement due to user request
   CONTEXT:  SQL statement "SELECT pg_advisory_xact_lock(hashtextextended(s.canonical_id::text, 0)) ..."
   ```

3. **他应用排除**：窗口期在 llm_gateway 库活跃的外部应用仅 redclaw 栈（`platform_app`），其当前 24 条空闲连接的 last_query 全部落在自有 schema（`integration.outbox`、`fencing.lease_ledger`、`authagent.sessions`、`workflow.timers` 等），无触碰 public.* 网关表的记录；acc 连 `acc_db`、pocket 连 `pocket` 库。且 553 条无一指纹属于他应用语句形态。

4. **交叉比对的时限说明**：PG 可用窗口止于 09-04 16:04 CST，网关侧可得日志最早为今日（容器 stdout 15:02 CST 起、宿主机轮转日志 00:02 CST 起），**无时间重叠**，无法做逐条 PID/时间对齐；今日网关日志（10 个轮转 gz + 当前 stdout）中 `canceled/57014/canceling` 均为 0，说明当日该类取消低发或不落网关日志（网关对 57014 仅在 `admin/telemetry.go` 按预期错误类处理）。归属判定因此以代码级指纹为主（该证据不受时间窗影响）。

5. **pg_stat_activity 只读观察**：两轮采样（1s×180、200ms×700）未捕获任何活跃外部查询——窗口期的噪音客户端（acc worker、巡检族、探索会话）当前已静默或单条语句亚秒级。

### 3.2 时间分布

canceling 集中于工作时段：09-03 23h UTC 63 条、09-04 00h UTC **152** 条（08:00-08:59 CST）、02h UTC **124** 条（10:00-10:59 CST）——与 admin 节点矩阵（1.5s 预算查询）的使用高峰吻合，进一步支持"网关 admin 面板慢查询被取消"为主因。

### 3.3 处置建议（本网关侧，主代理决策）

1. `main_livestream.go` 节点矩阵查询 1500ms 预算在高峰期不够：加索引/降采样/放宽预算（二选一），或在取消路径上降噪（对 57014 记 DEBUG 而非向上抛）；
2. PG 日志侧该类消息属预期行为，可在 `log_min_messages` 不动的前提下由 DBA 用 `audit`/`log_line_prefix` 过滤规则降权；
3. 无需他应用配合，**不涉及安全或数据一致性问题**（取消是网关主动行为）。

## 4. 其他观测（附赠，供 DBA 参考）

- **口令试错 FATAL 1,352 条**：`xutaohuang` 1,070（宿主用户名当 DB 用户，深夜 00:00-06:00 CST 交互/代理会话，与 #3 daily_kline 会话同时段）、`postgres` 168、`user` 74、`kxuser` 14、`casdoor` 14、`memora_app/root` 10。建议：废弃角色的连接尝试可在 `pg_hba.conf` 收紧网段，或对 0.0.0.0/0 scram 行做来源限制。
- **坏 DSN 一次性爆发**：`FATAL: database "llm_gateway\" does not exist`（库名带尾随反斜杠）237 条，集中在 09-03 23:34~23:59 CST（恰为 PG 容器创建后 40 分钟内），峰值 57 条/分钟，此后自愈——某初始化/部署脚本 DSN 转义错误，一次性。
- **多租户共库现状**：llm_gateway 库内并存 30+ 业务 schema（orchestrator/dal/redclaw/memora/authagent/fencing/workflow/integration/legal_qa/qa_libraries/opencode_pocket/...），redclaw 全栈 5 组件以 `platform_app` 直连本库。建议 DBA 评估：为重噪音客户端开 `log_line_prefix='%m [%p] %u %d '`（当前仅 `%m [%p]`，是本次逐行定库困难的根因），并考虑按角色设置 `statement_timeout`/连接数上限。

## 5. 附录：可复跑统计命令（全部只读）

```bash
# 窗口与总量
docker logs --timestamps llm-gateway-pg 2>&1 | sed -n '1p;$p'
docker logs llm-gateway-pg 2>&1 | grep -c "ERROR:"
docker logs llm-gateway-pg 2>&1 | grep "ERROR:" | sed -E 's/^.*ERROR:  //' | sort | uniq -c | sort -rn | head -30

# 8 类计数
L=$(docker logs llm-gateway-pg 2>&1)
for p in "column e.employee_id does not exist" 'multiple assignments to same column "last_heartbeat_at"' \
  'column "days" does not exist' 'relation "llm_usage_records" does not exist' 'relation "agent_groups" does not exist' \
  "row-level security" "no schema has been selected to create in" "canceling statement due to user request" \
  "cache lookup failed for attribute"; do echo "$(echo "$L" | grep -cF "$p")  $p"; done

# 证据抽样（示例）
docker logs llm-gateway-pg 2>&1 | grep -A8 'column "days" does not exist' | head -24
docker logs llm-gateway-pg 2>&1 | grep -A6 'relation "agent_groups" does not exist' | head -18

# canceling 指纹（awk 状态机按块收集 STATEMENT，见 §3.1）
docker logs llm-gateway-pg 2>&1 | awk '/canceling statement due to user request/{state=1;stmt="";next}
state==1&&/STATEMENT:/{state=2;line=$0;sub(/^.*STATEMENT: */,"",line);stmt=line;next}
state==2&&/^2026-[0-9-]+ [0-9:.]+ CST \[/{gsub(/^[ \t]+|[ \t]+$/,"",stmt);print substr(stmt,1,110);state=0;next}
state==2{stmt=stmt" "$0}' | sort | uniq -c | sort -rn

# 归属矩阵（逐库 × 目标表）
for db in $(docker exec llm-gateway-pg psql -X -U llm_gateway -d llm_gateway -Atqc \
  "SELECT datname FROM pg_database WHERE NOT datistemplate"); do
  r=$(docker exec llm-gateway-pg psql -X -U llm_gateway -d "$db" -Atqc \
    "SELECT string_agg(DISTINCT table_name,',') FROM information_schema.tables
     WHERE table_name IN ('employees','daily_kline','task_assignments','audit_hash_chain','llm_usage_records','agent_groups','audit_logs')" 2>/dev/null)
  [ -n "$r" ] && echo "$db => $r"
done

# 连接特征（当前）
docker exec llm-gateway-pg psql -X -U llm_gateway -d llm_gateway -c \
  "SELECT datname,usename,COALESCE(client_addr::text,'local') addr,state,count(*),
          left(regexp_replace(query,E'[\n\r]+',' ','g'),110) last_query
   FROM pg_stat_activity WHERE pid<>pg_backend_pid() GROUP BY 1,2,3,4,6 ORDER BY 1,2;"

# 分类器（当前因 stderr 断流不可用，修复后可复用）
bash scripts/monitoring/pg-error-classifier.sh
```

---

*报告生成：2026-09-05 16:20 CST；分析窗口：2026-09-03 22:54 ~ 2026-09-04 16:04 CST（PG 容器日志实际可得范围，原因见 §1.1）。*
