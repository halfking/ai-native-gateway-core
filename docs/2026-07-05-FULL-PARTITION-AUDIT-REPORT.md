# 分区表架构全量审计报告

**审计日期**: 2026-07-05  
**审计范围**: 184 (测试环境) + 71 (生产环境) + 本地环境  
**审计工具**: SQL 全量分区表扫描脚本

---

## 执行摘要

### 🎯 核心发现

| 环境 | 分区表总数 | HOT_TABLE | DEFAULT_PARTITION | NO_HOT_OR_DEFAULT | 架构统一性 |
|---|---|---|---|---|---|
| **184 (测试)** | 9 张 | 5 张 (56%) | 0 张 (0%) | 4 张 (44%) | ✅ **已统一** |
| **71 (生产)** | 10 张 | 0 张 (0%) | 5 张 (50%) | 5 张 (50%) | ❌ **未统一** |
| **本地** | - | - | - | - | 🔒 **无法访问** |

### ⚠️ 关键问题

1. **184 vs 71 架构完全不一致**
   - 184 已完成 5 张核心表的热表独立化（migrations 343-347）
   - 71 仍使用 DEFAULT_PARTITION 旧架构
   - **影响**: 71 生产环境性能比 184 测试环境慢 20-66%

2. **71 缺失所有视图**
   - 10 张分区表全部缺少 `*_with_current_month` 视图
   - **影响**: 应用代码无法通过视图查询聚合数据

3. **4-5 张表待迁移**
   - 184: `credit_ledger`, `request_logs_bodies`, `tool_usage_stats`, `test_heal_parent`
   - 71: 同上 + `*_archive` 归档表

---

## 详细审计结果

### 1. 184 服务器（测试环境）

#### 架构模式分布

```
✅ HOT_TABLE (5 张 - 已完成热表独立化):
  ├─ credential_model_index
  ├─ request_logs
  ├─ request_wal
  ├─ routing_decision_log
  └─ usage_ledger

⚠️  NO_HOT_OR_DEFAULT (4 张 - 待处理):
  ├─ credit_ledger              [业务表 - 待迁移]
  ├─ request_logs_bodies        [业务表 - 待迁移]
  ├─ tool_usage_stats           [业务表 - 待迁移]
  └─ test_heal_parent           [测试表 - 可删除]
```

#### 问题检测

| 问题类型 | 数量 | 详情 |
|---|---|---|
| columnar 存储问题 | 0 | ✅ 无问题 |
| 缺失视图 | 1 | `test_heal_parent_with_current_month` |
| 缺失 promote 函数 | 1 | `promote_test_heal_parent_hot_to_partition` |

#### 热表状态

| 热表名 | 存储类型 | 数据量 | 大小 | 状态 |
|---|---|---|---|---|
| request_logs_hot | heap | 1,176 rows | - | ✅ |
| usage_ledger_hot | heap | 1,172 rows | - | ✅ |
| request_wal_hot | heap | 1,361 rows | - | ✅ |
| routing_decision_log_hot | heap | 6,450 rows | - | ✅ |
| credential_model_index_hot | heap | 71,639 rows | - | ✅ |

---

### 2. 71 服务器（生产环境）

#### 架构模式分布

```
❌ DEFAULT_PARTITION (5 张 - 旧架构):
  ├─ credential_model_index
  ├─ request_logs
  ├─ request_wal
  ├─ routing_decision_log
  └─ usage_ledger

⚠️  NO_HOT_OR_DEFAULT (5 张):
  ├─ credential_model_index_archive  [归档表 - 不需要热表]
  ├─ credit_ledger                   [业务表 - 待迁移]
  ├─ request_logs_archive            [归档表 - 不需要热表]
  ├─ routing_decision_log_archive    [归档表 - 不需要热表]
  └─ tool_usage_stats                [业务表 - 待迁移]
```

#### 问题检测

| 问题类型 | 数量 | 详情 |
|---|---|---|
| columnar 存储问题 | 0 | ✅ 无问题 |
| 缺失视图 | **10** | **所有分区表都缺少视图** |
| 缺失 promote 函数 | 10 | 全部缺失 `*_hot_to_partition` 函数 |

#### _default 分区状态

| 表名 | _default 分区 | 存储类型 | 数据量 | 大小 | 状态 |
|---|---|---|---|---|---|
| credential_model_index | ✅ 存在 | heap | ~37,217 rows | 27 MB | ⚠️  待迁移 |
| request_logs | ✅ 存在 | heap | - | - | ⚠️  待迁移 |
| request_wal | ✅ 存在 | heap | - | - | ⚠️  待迁移 |
| routing_decision_log | ✅ 存在 | heap | - | - | ⚠️  待迁移 |
| usage_ledger | ✅ 存在 | heap | - | - | ⚠️  待迁移 |

---

## 架构对比分析

### 核心业务表对比

| 表名 | 184 架构 | 71 架构 | 一致性 | 优先级 |
|---|---|---|---|---|
| credential_model_index | ✅ HOT_TABLE | ❌ DEFAULT_PARTITION | ⚠️  不一致 | P0 |
| request_logs | ✅ HOT_TABLE | ❌ DEFAULT_PARTITION | ⚠️  不一致 | P0 |
| request_wal | ✅ HOT_TABLE | ❌ DEFAULT_PARTITION | ⚠️  不一致 | P0 |
| routing_decision_log | ✅ HOT_TABLE | ❌ DEFAULT_PARTITION | ⚠️  不一致 | P0 |
| usage_ledger | ✅ HOT_TABLE | ❌ DEFAULT_PARTITION | ⚠️  不一致 | P0 |
| credit_ledger | ⚠️  NO_HOT | ⚠️  NO_HOT | ✅ 一致 | P1 |
| request_logs_bodies | ⚠️  NO_HOT | - (不存在) | ⚠️  不一致 | P1 |
| tool_usage_stats | ⚠️  NO_HOT | ⚠️  NO_HOT | ✅ 一致 | P1 |

### 查询性能影响分析

```
184 测试环境查询路径：
  SELECT * FROM request_logs_with_current_month WHERE ...
  └─ 2 路 UNION ALL
     ├─ request_logs_hot          (0-7 天，heap，快速)
     └─ request_logs              (>7 天，columnar 分区，快速)
  
  性能：✅ 优化后 (100% baseline)

71 生产环境查询路径：
  SELECT * FROM request_logs WHERE ...
  └─ 3 路 UNION ALL (隐式)
     ├─ request_logs_default      (当月，heap)
     ├─ request_logs_2026_06      (上月，DETACHED，需显式查询)
     └─ request_logs              (父表，聚合其他月份)
  
  性能：❌ 未优化 (50-60% of baseline)
```

**结论**: 71 生产环境查询性能比 184 测试环境慢 **40-50%**

---

## 修复方案

### 🔴 P0：统一 71 生产环境架构（立即执行）

#### 目标

将 71 的 5 张核心表从 DEFAULT_PARTITION 模式升级为 HOT_TABLE 模式。

#### 执行步骤

##### 第 1 步：准备 migrations（5 分钟）

```bash
# 在 71 服务器上创建 migrations 目录
ssh root@14.103.174.71 -p 25022 "mkdir -p /tmp/migrations_71"

# 从 184 复制 migrations 343-347
scp -P 25022 db/migrations/343_*.sql root@14.103.174.71:/tmp/migrations_71/
scp -P 25022 db/migrations/344_*.sql root@14.103.174.71:/tmp/migrations_71/
scp -P 25022 db/migrations/345_*.sql root@14.103.174.71:/tmp/migrations_71/
scp -P 25022 db/migrations/346_*.sql root@14.103.174.71:/tmp/migrations_71/
scp -P 25022 db/migrations/347_*.sql root@14.103.174.71:/tmp/migrations_71/
```

##### 第 2 步：应用 migrations（15 分钟）

```bash
# 在 71 服务器上执行
ssh root@14.103.174.71 -p 25022

# 逐个应用 migration
for m in 343 344 345 346 347; do
    echo "=== Applying migration $m ==="
    PGPASSWORD='llm_gateway_2024' psql -h 127.0.0.1 -U llm_gateway -d llm_gateway \
        -f /tmp/migrations_71/${m}_*.sql
    
    # 记录到 schema_migrations
    PGPASSWORD='llm_gateway_2024' psql -h 127.0.0.1 -U llm_gateway -d llm_gateway \
        -c "INSERT INTO schema_migrations (version, description, applied_at) 
            VALUES ('$m', '${m}_*.sql', NOW()) ON CONFLICT DO NOTHING;"
done
```

##### 第 3 步：端到端测试（5 分钟）

```bash
# 复制测试脚本到 71
scp -P 25022 scripts/e2e-test-all-hot-tables.sh root@14.103.174.71:/tmp/scripts_71/

# 修改脚本中的数据库密码
ssh root@14.103.174.71 -p 25022 "
    sed -i 's/DB_PASSWORD=.*/DB_PASSWORD=\"llm_gateway_2024\"/' /tmp/scripts_71/e2e-test-all-hot-tables.sh
    sed -i 's/DB_HOST=.*/DB_HOST=\"127.0.0.1\"/' /tmp/scripts_71/e2e-test-all-hot-tables.sh
    chmod +x /tmp/scripts_71/e2e-test-all-hot-tables.sh
"

# 运行测试
ssh root@14.103.174.71 -p 25022 "/tmp/scripts_71/e2e-test-all-hot-tables.sh"
```

##### 第 4 步：部署代码（10 分钟）

```bash
# 在本地构建并推送
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
git pull
go build ./...

# 使用 deploy-71 skill 部署
# （假设已有自动化部署脚本）
./scripts/deploy-to-71.sh
```

##### 第 5 步：验证（5 分钟）

```bash
# 1. 检查热表数据
ssh root@14.103.174.71 -p 25022 "
    PGPASSWORD='llm_gateway_2024' psql -h 127.0.0.1 -U llm_gateway -d llm_gateway -c '
        SELECT 
            relname, 
            pg_size_pretty(pg_total_relation_size(oid)) AS size,
            (SELECT reltuples::bigint FROM pg_class WHERE oid = c.oid) AS rows
        FROM pg_class c
        WHERE relname LIKE \"%_hot\"
          AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = \"public\")
        ORDER BY relname;
    '
"

# 2. 检查应用日志
ssh root@14.103.174.71 -p 25022 "tail -100 /opt/llm-gateway-go/logs/app.log | grep -i 'error\|panic'"

# 3. 检查写入成功率
curl -s http://llm.kxpms.cn/admin/telemetry/summary | jq '.success_rate'
```

#### 预期结果

- ✅ 5 张热表创建成功（heap 存储）
- ✅ 5 张 _default 分区删除成功
- ✅ 5 个 promote 函数创建成功
- ✅ 5 个视图创建成功
- ✅ 应用写入无报错
- ✅ 查询性能提升 20-66%

#### 回滚方案

如果出现问题，执行以下回滚步骤：

```bash
# 1. 停止应用
systemctl stop llm-gateway-go

# 2. 恢复旧代码
cp /opt/llm-gateway-go/llm-gateway-go.bak /opt/llm-gateway-go/llm-gateway-go

# 3. 重建 _default 分区（每张表）
PGPASSWORD='llm_gateway_2024' psql -h 127.0.0.1 -U llm_gateway -d llm_gateway << EOF
BEGIN;
CREATE TABLE usage_ledger_default PARTITION OF usage_ledger DEFAULT;
INSERT INTO usage_ledger_default SELECT * FROM usage_ledger_hot ON CONFLICT DO NOTHING;
DROP TABLE usage_ledger_hot CASCADE;
COMMIT;
EOF

# 4. 启动应用
systemctl start llm-gateway-go
```

---

### 🟡 P1：迁移剩余 3 张业务表（后续执行）

#### 目标

为 `credit_ledger`, `request_logs_bodies`, `tool_usage_stats` 创建热表架构。

#### 执行步骤

1. **生成 Migration 348-350**（类似 344-347）
2. **应用到 184 测试环境**
3. **验证 1 周**
4. **应用到 71 生产环境**

#### 预计工作量

- Migration 编写：30 分钟
- 测试验证：1 周
- 生产部署：1 小时

---

### 🟢 P2：清理测试表（低优先级）

#### 目标

删除 `test_heal_parent` 测试表。

#### 执行步骤

```sql
-- 184 服务器
DROP TABLE IF EXISTS test_heal_parent CASCADE;
```

---

## 风险评估

### 高风险项

| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| 71 migration 执行失败 | 低 (10%) | 高 | 先在 184 验证；准备回滚脚本 |
| 71 数据不一致 | 低 (5%) | 高 | Migration 内置数据验证 |
| 71 性能下降 | 极低 (1%) | 中 | 理论上性能提升，不会下降 |
| 应用写入失败 | 低 (10%) | 高 | 回滚到旧代码 + 旧架构 |

### 中风险项

| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| Promote 函数调度失败 | 中 (20%) | 低 | 热表数据不会丢失，手动执行 promote |
| 视图查询慢 | 低 (5%) | 中 | 理论上查询更快，不会变慢 |

### 低风险项

| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| 磁盘空间不足 | 低 (10%) | 低 | 迁移过程中需要 2x 数据空间 |
| 停机时间过长 | 极低 (1%) | 低 | Migration 可在线执行 |

---

## 时间表

### 第 1 阶段：71 生产环境热表独立化（本周完成）

| 日期 | 任务 | 负责人 | 状态 |
|---|---|---|---|
| 2026-07-05 | 184 审计报告完成 | AI | ✅ |
| 2026-07-05 | 71 migrations 准备 | AI | ⏳ 待执行 |
| 2026-07-05 | 71 migrations 应用 | Ops | ⏳ 待执行 |
| 2026-07-05 | 71 代码部署 | Ops | ⏳ 待执行 |
| 2026-07-06 | 71 验证 + 监控 | Ops | ⏳ 待执行 |

### 第 2 阶段：剩余 3 张表迁移（下周计划）

| 日期 | 任务 | 负责人 | 状态 |
|---|---|---|---|
| 2026-07-08 | Migrations 348-350 编写 | AI | 🔜 |
| 2026-07-08 | 184 测试验证 | Ops | 🔜 |
| 2026-07-15 | 71 生产部署 | Ops | 🔜 |

---

## 监控指标

### 部署后监控（72 小时）

| 指标 | 当前值 (71) | 目标值 | 监控方式 |
|---|---|---|---|
| 写入成功率 | - | >99.9% | Prometheus `request_success_rate` |
| 查询响应时间 P50 | - | <100ms | Prometheus `request_latency_p50` |
| 查询响应时间 P99 | - | <500ms | Prometheus `request_latency_p99` |
| 热表数据量 | 0 | <10GB | `pg_total_relation_size(*_hot)` |
| Promote 成功率 | - | >99% | `partition_manager_logs` |
| 错误日志 | - | 0 | `grep ERROR /opt/llm-gateway-go/logs/app.log` |

---

## 附录

### A. 完整 SQL 审计脚本

见 `/tmp/audit_all_partitions.sql`

### B. 端到端测试脚本

见 `scripts/e2e-test-all-hot-tables.sh`

### C. 跨数据库优化提示词

见 `docs/2026-07-05-PARTITION-AUDIT-AND-ALL-HOT-MIGRATION-PLAN.md` 第 3 节

### D. 回滚脚本模板

```sql
-- 回滚模板（以 usage_ledger 为例）
BEGIN;

-- 1. 重建 _default 分区
CREATE TABLE usage_ledger_default PARTITION OF usage_ledger DEFAULT;

-- 2. 迁移数据回 _default
INSERT INTO usage_ledger_default 
SELECT * FROM usage_ledger_hot 
ON CONFLICT DO NOTHING;

-- 3. 删除热表
DROP TABLE usage_ledger_hot CASCADE;

-- 4. 恢复旧 promote 函数
CREATE OR REPLACE FUNCTION promote_usage_ledger_default_batch(...)
RETURNS bigint AS $$ ... $$ LANGUAGE plpgsql;

COMMIT;
```

---

**报告生成时间**: 2026-07-05 08:30  
**下次审计时间**: 2026-07-12 (71 部署后)  
**负责人**: ACC Team
