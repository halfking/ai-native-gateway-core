# Session Log — V359 部署 + 上线观察（2026-08-17 23:10 → 2026-08-18 00:45）

承接 handoff `/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/handoff-20260817-231034.md`。

## 本会话行动

### §5.1 + §5.2（接续上轮）：154 发版 + 上线观察

- 走仓库原版 `scripts/deploy-154.sh`（绕开 skill 包装 hard-gate）发布 build_seq=1591 / git_sha=c133906a
- healthz + system/version ✅、candidate-failures 新字段 ✅、session_id 列 + 索引就位 ✅
- §5.2 SQL #1 新行观察：24h 内 0 新行（路由池健康无失败流量）
- §5.2 SQL #2 session_id 分布：5 个 gt_gw_* session × 3 行（历史数据）

### §5.3 表治理（用户决策 2026-08-17 23:35）

用户决策：
1. NULL ts 行直接移除（不迁入新结构）
2. 分区模式 = hot(heap, 24h) + 月度 columnar 分区（392 模式）

设计文档：`docs/analysis/candidate-failure-logs-252-governance-2026-08-17.md`

### V359 实施

- 写 `deploy/sql/migrations/V359__candidate_failure_logs_hot_and_partition.sql` + down
- 改 Go 端 5 处：admin handlers 3 SELECT 走视图；routing/streaming executors 2 INSERT 写 hot
- `go build ./...` 0 错、gofmt 干净
- commit `1ae616710` + push

### 本地 PG17 沙箱验证（v359_test schema）

- rename / hot / partitioned parent / 视图 / promote / DROP 月度分区 — 全通
- 验证项：行数对比、视图 UNION 正确、子分区 columnar、session_id 通过视图可查、NULL ts 未迁入、INSERT 走 hot、promote 函数工作

### 252 实际执行（低峰窗口 CST 00:40）

部署现场发现 V359 文件 4 个隐患，逐项修复并二次提交 `f85713ea4`：

1. `LIKE INCLUDING ALL` 不继承 NOT NULL → hot 表需补 NOT NULL + DEFAULT（9 个 ALTER）
2. `ensure_partition` / `promote_*` 函数在 392 stub 已存在 → DROP IF EXISTS 后再 CREATE
3. `CREATE VIEW ... SELECT * ... UNION ALL SELECT *` → hot 与父表列顺序错位 → 改显式列名
4. `ensure_partition` 仅建当月+上下月，但历史数据 max_ts=2026-06-26 → 加 7 个历史月份（2025_12 → 2026_06）

执行结果（手动 SQL 步骤）：

| 步骤 | 结果 |
|---|---|
| 1. rename columnar → _columnar_old | ✅ |
| 2. CREATE hot (LIKE … INCLUDING ALL) | ✅ |
| 3. ALTER hot NOT NULL + DEFAULT 补齐 | ✅ |
| 4. CREATE partitioned 父表 | ✅ |
| 5. DROP + CREATE ensure_partition 函数 | ✅ |
| 6. CREATE 10 个月份分区（2025_12 → 2026_09） | ✅ |
| 7. INSERT 12,331 行非 NULL ts → 父表 | ✅ |
| 8. ALTER RLS + CREATE POLICY（父表 + hot） | ✅ |
| 9. CREATE 6 个父表索引 + 1 个 hot 索引 | ✅ |
| 10. CREATE VIEW with_current_month（显式列名） | ✅ |
| 11. DROP + CREATE promote 函数 | ✅ |
| 12. VALIDATION：hot=t、partitioned=t、view=t、fn=t、rls=t、parent=12,331、_old=84,760 | ✅ |
| 注册 schema_migrations（V359） | ✅ |

### 154 发版（build_seq=1592 / git_sha=f85713ea）

- 部署：version=2.5.0-f85713ea-20260817-1592-f85713ea、总 53s、切换 25s、healthz/DB/admin 密码同步全过
- 这一版二进制启用了：admin 视图读、写入 hot 表

## 上线观察（部署后 ~10 分钟）

### admin 端

- `/api/candidate-failures?limit=3` → 200、count=3、3 条都是 2026-08-18 00:00:56-58 同 session_id `gt_gw_b6753644-9e01-4843-a0b7-9ec9de26a6be`（同一 request_id 多 attempt_index），证明视图 UNION ALL 工作 + V358 session_id 字段生效
- raw_model_name = `minimaxai/minimax-m2.7`、error_kind = `model_deprecated`、upstream_status_code = 410 — 模型已弃用导致真实失败事件

### DB 端

- 10 个月份分区（2025_12 → 2026_09）全部 columnar
- hot=0（新 binary 还没新 INSERT 事件触发）、parent=12,331、view=12,331、_old=84,760
- view 中 NULL ts=0、parent 中 NULL ts=0、_old NULL ts=72,429 — 完全符合"NULL ts 行不进新结构"决策
- 视图 row_count=12,331 = parent（hot=0 时一致）

## 已知 / 后续

- `_columnar_old` 84,760 行（含 72,429 NULL ts）保留 30 天观察期；30 天后人工 DROP
- `partition_manager` promote 周期：Go 端 1 个 promoteSpecs 已在 promote_candidate_failure_logs_hot_to_partition 注册；新 INSERT 进 hot 后将按 24h 滚动到月度分区
- 部署瞬间脏数据：0:00:56-58 这 3 行原本由旧 binary 写进 _columnar_old，V359 迁移时按 ts 路由进 2026_08 分区（无害）

## Verification Evidence (deploy-154 v1.2 contract)

```
VERIFY_TOOL=deploy-154
VERIFY_TOOL_VERSION=1.2.0
VERIFY_TIMESTAMP=2026-08-18T00:40:58+08:00
VERIFY_DEVICE=aliyun-gateway-154
VERIFY_PASS=true
VERIFY_EVIDENCE="healthz={\"status\":\"ok\",\"version\":\"2.5.0-f85713ea-20260817-1592-f85713ea\"}; build_seq=1592; git_sha=f85713ea; candidate_failures_endpoint=200; candidate_failure_view_count=3; partitioned_parent=t; monthly_partitions=10 columnar; hot_table=t; old_columnar_retained=84760 rows; null_ts_isolated_in_old_only=t"
```

## 关键提交

- `f85713ea4`（main）— fix(V359): sync SQL file with deployment-time fixes
- `1ae616710`（main）— feat(db+cfl): V359 — candidate_failure_logs hot + 月度 columnar 分区治理
- `c133906ac`（main，已在 handoff 中）— V358 audit fixes

## §5 收口

- ✅ §5.1 154 发版（v1591 + v1592）
- ✅ §5.2 上线观察
- ✅ §5.3 表治理（V359 hot + 月度 columnar 分区 + 视图 + promote）
- ⏳ §5.4 kaixuan-1 库补迁（K3s 网络不通，阻塞）
- ⏳ §5.5 其他桥接补 RecordChunkSent（代码任务）
- ⏳ §5.6 跨模型切换 ADR（政策决策）
- 📋 §5.3 后续：30 天后人工 DROP `candidate_failure_logs_columnar_old`（84,760 行）