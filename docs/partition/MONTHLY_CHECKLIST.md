# 分区表月度维护清单

**日期**: 2026-07-05
**版本**: 1.0

## 修订历史

| 版本 | 日期 | 变更 | 作者 |
|------|------|------|------|
| 1.0 | 2026-07-05 | 初始版本（合并自 DEPLOYMENT_CHECKLIST_PARTITION_ARCHIVE / DEPLOYMENT_CHECKLIST_20260630 两份旧文档） | Infrastructure Team |

---

## 概述

本文档定义分区表架构的月度维护任务。维护窗口建议在每月 **1 号凌晨 2:00-4:00**（低峰期）。

---

## 维护日历

| 日期 | 任务 | 预计时间 | 自动化 |
|------|------|---------|--------|
| 每月 1 号 | 更新 *_with_current_month VIEW | 10 分钟 | ❌ 手动 |
| 每月 1 号 | DETACH 上月分区 | 5 分钟 | ⚠️ 半自动 |
| 每月 1 号 | ATTACH 上月分区到归档 | 5 分钟 | ❌ 手动 |
| 每周 | 健康检查 | 5 分钟 | ✅ 脚本 |
| 每日 | promote 函数执行 | 自动 | ✅ 后台 |

---

## 任务 1：更新查询 VIEW（每月 1 号）

### 1.1 背景

当月分区 DETACHED 后，`*_with_current_month` VIEW 需要手动更新 UNION ALL 列表，加入新月份。

**示例**（8 月 1 号）：
```sql
-- 旧 VIEW（7 月）
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_07  -- 7 月
UNION ALL
SELECT * FROM request_logs_default;

-- 新 VIEW（8 月）
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_08  -- 8 月
UNION ALL
SELECT * FROM request_logs_default;
```

### 1.2 执行步骤

```bash
# 1. 连接到数据库
psql -h <host> -U kxuser -d llm_gateway

# 2. 确认当前月份
SELECT now(), to_char(now(), 'YYYY_MM');

# 3. 执行 VIEW 更新（使用 transaction）
BEGIN;

-- 8 表 × 1 条 UPDATE = 8 条
DROP VIEW IF EXISTS request_logs_with_current_month;
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_08
UNION ALL
SELECT * FROM request_logs_default;

-- 对其他 7 个表重复...

COMMIT;

# 4. 验证
SELECT viewname FROM pg_views WHERE viewname LIKE '%_with_current_month';
```

### 1.3 自动脚本（待实现）

```bash
# 计划：migration 341 将实现自动化
# ./scripts/partition/update-monthly-views.sh --month 2026_08
```

### 1.4 验证

```sql
-- 测试跨月查询
SELECT count(*) FROM request_logs_with_current_month
WHERE ts >= '2026-08-01';

-- 对比父表（应包含更多数据）
SELECT count(*) FROM request_logs
WHERE ts >= '2026-08-01';
```

---

## 任务 2：分区轮转（每月 1 号）

### 2.1 背景

每月 1 号需要进行以下分区轮转：

1. **DETACH 上月分区**（从 ACTIVE 变为 INACTIVE）
2. **迁移上月数据到历史归档**（可选：转 Columnar）
3. **确保本月分区已创建**

### 2.2 DETACH 上月分区

```sql
-- 8 月 1 号执行，DETACH 7 月分区
DO $$
DECLARE
  prev_month text := to_char(now() - interval '1 month', 'YYYY_MM');
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'request_logs',
    'request_wal', 
    'usage_ledger',
    'routing_decision_log',
    'credential_model_index',
    'request_logs_bodies',
    'credit_ledger',
    'tool_usage_stats'
  ] LOOP
    EXECUTE format(
      'ALTER TABLE %I DETACH PARTITION %I',
      table_name,
      table_name || '_' || prev_month
    );
    RAISE NOTICE 'DETACHED %', table_name || '_' || prev_month;
  END LOOP;
END $$;
```

### 2.3 验证分区状态

```bash
./scripts/partition/check-partition-health.sh --env 71
```

预期结果：
- 2026_07 分区：DETACHED
- 2026_08 分区：DETACHED（新月份）
- *_default：ATTACHED

### 2.4 Columnar 转换（可选）

```sql
-- 将上月分区转为 Columnar（节省 60%+ 存储）
-- 仅适用于历史数据（不再频繁更新）

BEGIN;

-- 1. 创建 Columnar 版本
CREATE TABLE request_logs_2026_07_archive (
  LIKE request_logs_2026_07 INCLUDING ALL
) USING columnar;

-- 2. 复制数据
INSERT INTO request_logs_2026_07_archive
SELECT * FROM request_logs_2026_07;

-- 3. 删除 heap 版本
DROP TABLE request_logs_2026_07;

-- 4. 重命名
ALTER TABLE request_logs_2026_07_archive 
RENAME TO request_logs_2026_07;

-- 5. ATTACH 到归档（只读）
ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_07
  FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- 6. 验证
SELECT c.relname, am.amname
FROM pg_class c
JOIN pg_am am ON am.oid = c.relam
WHERE c.relname = 'request_logs_2026_07';

COMMIT;
```

---

## 任务 3：确保本月分区已创建

### 3.1 检查

```sql
-- 检查当前月份分区
SELECT c.relname,
       CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END
FROM pg_class c
LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
WHERE c.relname LIKE 'request_logs_2026%';
```

### 3.2 如果缺失，创建

```sql
-- 创建 2026_08 分区
SELECT ensure_request_logs_partition('2026-08-01'::timestamptz);

-- 验证
SELECT c.relname FROM pg_class c
WHERE c.relname = 'request_logs_2026_08';
```

### 3.3 DETACH 新月份（关键）

```sql
-- 确保新月份分区是 DETACHED（这样 *_default 才能接收写入）
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_08;
```

---

## 任务 4：Promote 函数积压检查

### 4.1 检查积压

```bash
./scripts/partition/check-partition-health.sh --env 71 --report-only --format json
```

### 4.2 手动清理（如果需要）

```bash
# 如果 *_default 大小异常，执行手动迁移
./scripts/partition/manual-promote-default.sh --all --retention 7 --batch 5000
```

---

## 任务 5：备份验证（每周）

### 5.1 验证备份完整性

```sql
-- 检查备份保留
SELECT 
  backup_id,
  start_time,
  end_time,
  status,
  pg_size_pretty(total_size)
FROM backup_history
ORDER BY start_time DESC
LIMIT 10;
```

### 5.2 测试恢复（季度）

```bash
# 从备份恢复到一个临时实例
pg_restore -h test-instance -U kxuser -d llm_gateway_test backup.dump

# 验证分区状态
psql -h test-instance -c "SELECT count(*) FROM request_logs_with_current_month;"
```

---

## 任务 6：监控指标审查（每周）

### 6.1 检查 Prometheus 告警历史

```bash
# 查看过去一周的告警
curl -s 'http://prometheus:9090/api/v1/alerts?active=true' | \
  jq '.data.alerts[] | select(.labels.component=="partition")'
```

### 6.2 关键指标趋势

| 指标 | 正常范围 | 关注阈值 |
|------|---------|---------|
| `*_default` 大小 | < 5GB | > 5GB |
| promote 执行延迟 | < 1 小时 | > 2 小时 |
| 约束冲突错误 | 0 | > 0 |
| 死元组比例 | < 5% | > 10% |

---

## 月度检查清单（可打印）

```
分区表月度维护 - [日期: _______]

□ 1. 更新 VIEW（每月 1 号）
  □ 连接数据库
  □ 执行 VIEW 更新 SQL
  □ 验证跨月查询

□ 2. 分区轮转
  □ DETACH 上月分区
  □ 验证分区状态
  □ （可选）Columnar 转换

□ 3. 确保本月分区
  □ 检查本月分区存在
  □ 创建缺失的分区
  □ DETACH 本月分区

□ 4. Promote 积压检查
  □ 运行报告脚本
  □ 手动清理（如需要）

□ 5. 备份验证（每周）
  □ 检查备份状态
  □ 验证恢复能力

□ 6. 监控审查（每周）
  □ 检查告警历史
  □ 趋势分析

签名：_______________
日期：_______________
```

---

## 自动化路线图

| 任务 | 当前状态 | 计划实现 | 依赖 |
|------|---------|---------|------|
| VIEW 自动更新 | 手动 | migration 341 | update_monthly_views() 函数 |
| 分区 DETACH 自动 | 手动 | bg/partition_manager.go | 月度调度逻辑 |
| Columnar 转换脚本 | 手动 | scripts/ | 存储团队 |
| 告警自动处理 | 告警 | PagerDuty 集成 | on-call 流程 |

---

## 常见问题

### Q1：忘记更新 VIEW 会怎样？

**A**：跨月查询会遗漏上个月的 DETACHED 分区数据。例如 8 月 15 号查询 7 月数据时，如果 VIEW 未更新，会丢失 7 月数据。

**处理**：立即更新 VIEW：
```sql
DROP VIEW request_logs_with_current_month;
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_07
UNION ALL
SELECT * FROM request_logs_default;
```

### Q2：忘记 DETACH 新月份会怎样？

**A**：写入 `*_default` 会失败，报分区约束冲突错误。

**处理**：立即 DETACH：
```sql
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_08;
```

### Q3：Promote 函数停止工作很久了怎么办？

**A**：
1. 检查 bg/partition_manager.go 日志
2. 手动执行 promote：
   ```bash
   ./scripts/partition/manual-promote-default.sh --all
   ```
3. 如果 `*_default` 数据量很大（> 10GB），分批处理：
   ```bash
   # 先清理 7 天前的数据
   ./scripts/partition/manual-promote-default.sh --all --retention 7 --batch 5000
   ```

---

## 部署前分区归档验证清单（合并自 DEPLOYMENT_CHECKLIST_PARTITION_ARCHIVE.md）

### 1. 部署前检查

#### 代码部署
- [ ] 拉取最新代码：`git pull origin main`
- [ ] 验证提交存在：`git log --oneline | grep "feat(admin): add partition table columnar archive"`
- [ ] 检查文件完整性：
  ```bash
  ls -la admin/data_lifecycle_partition.go
  ls -la db/migrations/305_partition_archive_functions.sql
  ```

#### 环境准备
- [ ] 确认数据库连接正常
- [ ] 确认 Citus columnar 扩展已安装：
  ```sql
  SELECT * FROM pg_extension WHERE extname = 'citus_columnar';
  ```
- [ ] 备份数据库（建议）：
  ```bash
  pg_dump -h $DB_HOST -U $DB_USER -d llm_gateway > backup_before_migration_305.sql
  ```

#### 构建和测试
- [ ] 编译项目：`go build ./cmd/gateway`
- [ ] 运行单元测试：`go test ./admin -run TestPartition -v`
- [ ] 检查测试通过

### 2. 部署步骤

#### Step 1：应用数据库迁移

```bash
psql -h $DB_HOST -U $DB_USER -d llm_gateway

# 检查当前迁移状态
SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5;

# 应用 Migration 305
\i db/migrations/305_partition_archive_functions.sql
```

#### Step 2：验证数据库对象

```sql
-- 验证归档函数存在
\df archive_request_*

-- 应该看到：
-- archive_request_logs(date)
-- archive_request_wal(date)

-- 验证归档表存在
\d request_wal_archive
```

#### Step 3：重启服务

```bash
# 方式 1: systemd
sudo systemctl restart llm-gateway-go

# 方式 2: kubernetes
kubectl rollout restart deployment/llm-gateway-go -n production
```

#### Step 4：验证 API 端点

```bash
# 1. 查询分区状态
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions | jq .

# 2. 测试试运行归档
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive | jq .
```

#### Step 5：功能测试

```bash
# 使用非 super_admin token 测试归档端点（应该失败）
curl -X POST \
  -H "Authorization: Bearer $NON_SUPER_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive
# 预期：403 Forbidden
```

### 3. 回滚计划

#### Option 1：仅回滚代码（保留数据库更改）

```bash
git revert cec00d34
go build ./cmd/gateway
sudo systemctl restart llm-gateway-go
```

#### Option 2：完全回滚（包括数据库）

```bash
# 1. 回滚代码
git revert cec00d34

# 2. 回滚数据库迁移
psql -h $DB_HOST -U $DB_USER -d llm_gateway \
  -f db/migrations/305_partition_archive_functions.down.sql

# 3. 重新构建和部署
go build ./cmd/gateway
sudo systemctl restart llm-gateway-go
```

### 4. 4-Table 分区与归档部署检查（合并自 DEPLOYMENT_CHECKLIST_20260630.md）

#### 前置条件

```bash
# SSH 到 184
ssh -p 25022 root@__INTERNAL_PUBLIC_IP__

# 检查 migrations 是否已应用
export PGPASSWORD='__REDACTED_DB_PASSWORD__'
POD=$(kubectl get pod -n pms-test -l app=llm-gateway-pg -o jsonpath="{.items[0].metadata.name}")

kubectl exec -n pms-test $POD -c citus -- psql -U llm_gateway -d llm_gateway -c "
SELECT
  '317' AS migration,
  CASE WHEN EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index' AND relkind = 'p')
    THEN '✓ CMI is partitioned' ELSE '✗ CMI not partitioned' END AS status
UNION ALL
SELECT '318', CASE WHEN EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'archive_request_logs' AND pg_get_functiondef(oid) LIKE '%CHUNK_SIZE%')
    THEN '✓ archive functions fixed' ELSE '✗ archive functions not fixed' END
UNION ALL
SELECT '319', CASE WHEN EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'ensure_credential_model_index_partition')
    THEN '✓ ensure functions added' ELSE '✗ ensure functions missing' END;
"

# 预期 SHA256：7e80d9aa6f886c484009839f6dc876a96f61c7547b7e464aefe1d6b8c7d23efd
ls -lh /opt/databackup/pg-daily/184/pg-full-184-20260630.dump
sha256sum /opt/databackup/pg-daily/184/pg-full-184-20260630.dump

# Cron 检查
crontab -l | grep columnar
# 预期输出：0 4 1-3 * * /opt/scripts/columnar-monthly-cron.sh ...
```

#### 镜像信息（参考 2026-06-30 部署）

- 镜像名：`registry.internal.example.com/kx-llm-gateway-go:gitsha-0b0d80e8`
- 构建时间：2026-06-29T22:48:35Z
- Git SHA：0b0d80e8
- 部署日期：2026-06-30
- 备份位置：`184:/opt/databackup/pg-daily/184/pg-full-184-20260630.dump`

#### 部署命令

```bash
# 清理异常 Pods
kubectl delete pod -n pms-test -l app=llm-gateway-go \
  --field-selector="status.phase!=Running" \
  --force --grace-period=0

# 更新镜像
kubectl set image deployment/llm-gateway-go-deployment -n pms-test \
  llm-gateway-go=registry.internal.example.com/kx-llm-gateway-go:gitsha-0b0d80e8

# 监控 Rollout
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=180s
```

#### 一键部署 / 验证 / 回滚

```bash
# 一键部署
kubectl set image deployment/llm-gateway-go-deployment -n pms-test \
  llm-gateway-go=registry.internal.example.com/kx-llm-gateway-go:gitsha-0b0d80e8 && \
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test

# 一键验证
curl http://localhost:30082/healthz && \
kubectl logs -n pms-test -l app=llm-gateway-go --tail=50 | grep partition_manager

# 一键回滚
kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test && \
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test
```

### 5. 已知问题（合并自 DEPLOYMENT_CHECKLIST_20260630.md）

| 问题 | 原因 | 影响 | 缓解 |
|------|------|------|------|
| `request_logs_archive` 使用 heap | JSONB 列太大（>1MB/行），columnar 会 OOM | 压缩比低于其他 3 个表 | 拆分 JSONB 到独立表 |
| `credential_model_index` 7d cutoff | 只归档 7 天前的数据 | 主表行数较多（~186K 行） | 定期检查主表行数增长 |
| Cron 执行时间 | 每月 1-3 日 04:00 | 分散在 day1/2/3 | 查看 `/var/log/columnar-monthly.log` |

---

## 合并来源

本文档于 2026-07-05 合并以下旧文档：

- `docs/DEPLOYMENT_CHECKLIST_PARTITION_ARCHIVE.md`（已迁移到 `_to-be-deprecated/`）— 列存储归档功能部署前验证、API 端点测试、回滚计划
- `docs/DEPLOYMENT_CHECKLIST_20260630.md`（已迁移到 `_to-be-deprecated/`）— 4-Table 分区与归档部署清单 2026-06-30、镜像信息、k8s 部署步骤

---

**维护团队**: Infrastructure Team
**最后更新**: 2026-07-05
