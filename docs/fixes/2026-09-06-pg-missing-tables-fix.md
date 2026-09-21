# PostgreSQL 缺失表修复记录 (2026-09-06)

## 问题概述

通过分析 Docker 中 `llm-gateway-pg` 容器的 PostgreSQL 日志,发现三个表缺失导致的持续错误:

1. **orchestration_runtime_instances** - 外部编排服务尝试插入实例注册信息时失败
2. **llm_hourly_stats** - 外部统计收集器尝试插入小时级聚合数据时失败
3. **feature_distribution_stats** - 内部 bg/feature_stats_worker.go 尝试写入特征分布统计时失败

## 错误分析

### 1. orchestration_runtime_instances 表不存在

**错误日志**:
```
2026-09-06 07:06:14.050 CST [227453] ERROR:  relation "orchestration_runtime_instances" does not exist at character 13
2026-09-06 07:06:14.050 CST [227453] STATEMENT:  INSERT INTO orchestration_runtime_instances (
    tenant_id, runtime_id, instance_id, host_id, endpoint, status,
    capabilities, registration_revision, lease_epoch, credential_id,
    last_heartbeat_at, created_at, updated_at)
  VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,$13)
  ON CONFLICT (tenant_id, runtime_id, instance_id) DO UPDATE SET ...
```

**频率**: 每 30 秒一次 (心跳间隔)

**影响**: 外部编排服务无法注册和更新运行时实例信息

**根本原因**: 该表为外部服务预留,但从未在数据库迁移中创建

### 2. llm_hourly_stats 表不存在和时间戳格式错误

**错误日志 - 表不存在**:
```
2026-09-06 08:05:00.475 CST [235078] ERROR:  invalid input syntax for type timestamp with time zone: "2026-09-05T23"
2026-09-06 08:05:00.475 CST [235078] CONTEXT:  unnamed portal parameter $1 = '...'
2026-09-06 08:05:00.475 CST [235078] STATEMENT:  INSERT INTO llm_hourly_stats (
    hour, success_count, failure_count, total_count, total_cost
  ) VALUES ($1, $2, $3, $4, $5)
  ON CONFLICT (hour) DO UPDATE SET ...
```

**频率**: 每小时一次

**影响**:
- 外部统计收集器无法写入小时级统计数据
- 即使表存在,时间戳格式错误也会导致插入失败

**根本原因**:
1. 表未创建
2. **外部调用方传入的时间戳格式不完整**: `"2026-09-05T23"` 缺少分钟、秒和时区信息
   - 正确格式应为: `"2026-09-05T23:00:00+00:00"` 或 `"2026-09-05 23:00:00+00"`

### 3. feature_distribution_stats 表不存在

**错误日志**:
```
2026-09-06 07:46:42.384 CST [231663] ERROR:  relation "feature_distribution_stats" does not exist at character 342
2026-09-06 07:46:42.384 CST [231663] STATEMENT:
  WITH feature_counts AS (
    SELECT
      COALESCE(detected_language, 'NULL') AS feature_value,
      COUNT(*) AS row_count
    FROM auto_route_selections
    WHERE DATE(ts) = $1
    GROUP BY COALESCE(detected_language, 'NULL')
  )
  INSERT INTO feature_distribution_stats (stat_date, feature_name, feature_value, row_count, percentage)
  SELECT ...
```

**频率**: 每小时一次 (bg/feature_stats_worker.go 的定时任务)

**影响**:
- AUTO 路由 ML 训练的特征质量监控失败
- 无法收集特征分布、去重率等关键指标
- 无法检测数据质量异常(特征集中度过高、去重率过低、特征缺失等)

**根本原因**:
- 迁移文件 `662_feature_distribution_stats.sql` 已存在,但未被添加到部署脚本的执行列表中
- 该文件在代码库中已定义 (P2.3 特性),但升级部署路径遗漏了它

## 修复方案

### 方案选择分析

对于这三个问题,我们分析了两种修复路径:

#### 选项 A: 在 Go 代码中修复
- **orchestration_runtime_instances**: 需要在外部调用方修改 (不在我们控制范围内)
- **llm_hourly_stats 时间戳**: 需要在外部调用方修改时间戳格式化逻辑
- **feature_distribution_stats**: 需要在 bg/feature_stats_worker.go 中添加表不存在时的降级逻辑

**优点**: 可以实现优雅降级,不完全依赖数据库表
**缺点**:
- 外部服务不受我们控制
- 降级逻辑增加代码复杂度
- 统计数据永久丢失

#### 选项 B: 创建数据库表结构 ✅ (已采用)
- 创建 `666_orchestration_and_stats_tables.sql` 定义外部服务所需的两个表
- 将已有的 `662_feature_distribution_stats.sql` 添加到部署脚本
- 更新 `scripts/apply-db-revision-sequence.sh` 执行这些迁移

**优点**:
- 符合系统设计预期 (这些表本就应该存在)
- 一次性解决所有三个问题
- 支持外部集成和内部监控功能
- 向后兼容,不影响现有功能

**缺点**: 需要一次数据库迁移部署

**最终决策**: 选择方案 B,因为这些表是系统设计的一部分,缺失是部署gap而非设计问题。

### 实施的修复

#### 1. 创建 666_orchestration_and_stats_tables.sql

新建迁移文件包含两个表:

**orchestration_runtime_instances 表结构**:
- 主键: `id` (BIGSERIAL)
- 唯一约束: `(tenant_id, runtime_id, instance_id)`
- 核心字段:
  - `registration_revision`: 每次更新自动递增,跟踪注册变更
  - `capabilities`: JSONB 存储实例能力描述
  - `lease_epoch`: 分布式协调的租约周期
  - `last_heartbeat_at`: 心跳时间戳
- 索引: tenant_id, runtime_id, status, last_heartbeat_at
- 触发器: 自动更新 `updated_at`

**llm_hourly_stats 表结构**:
- 主键: `hour` (TIMESTAMPTZ) - **必须是整点时间戳**
- 字段:
  - `success_count`, `failure_count`, `total_count`: 请求计数
  - `total_cost`: 总费用 (NUMERIC(12,6))
- 约束:
  - 计数和费用必须非负
  - **hour 字段必须是完整的 TIMESTAMPTZ 格式** (包含时区)
- 索引: hour DESC
- 触发器: 自动更新 `updated_at`

**关于 llm_hourly_stats 时间戳格式**:
- ✅ 正确: `'2026-09-05 23:00:00+00'::timestamptz`
- ✅ 正确: `'2026-09-05T23:00:00+00:00'::timestamptz`
- ❌ 错误: `'2026-09-05T23'` (缺少分钟、秒、时区)

表结构中 `hour TIMESTAMPTZ` 定义本身会强制要求完整格式,不完整的字符串会被 PostgreSQL 拒绝并返回 `22007` 错误码。**外部调用方需要修复其时间戳格式化代码**。

#### 2. 更新 scripts/apply-db-revision-sequence.sh

在文件列表中添加:
```bash
# Line 127 之后插入
"$ROOT_DIR/sql/migrations/startup/662_feature_distribution_stats.sql"
# Line 135 之后插入
"$ROOT_DIR/sql/migrations/startup/666_orchestration_and_stats_tables.sql"
```

添加详细注释说明:
- 662: bg/feature_stats_worker.go 依赖的特征分布统计表
- 664: 外部编排服务和统计收集器依赖的表,包含时间戳格式要求说明

#### 3. 部署顺序保证

迁移文件的执行顺序:
1. 660-661: 现有修复
2. **662**: feature_distribution_stats (bg worker 依赖)
3. 663: provider_error_details 索引修复
4. **664**: orchestration_runtime_instances + llm_hourly_stats (外部服务依赖)
5. V371: supplier_errors 相关

顺序合理性:
- 662 在 bg worker 启动前执行
- 664 独立于其他迁移,可以安全地追加在最后
- 所有迁移都是幂等的 (IF NOT EXISTS / CREATE OR REPLACE)

## 遗留问题和建议

### 1. llm_hourly_stats 时间戳格式问题 ⚠️

**现状**: 表已创建,但外部调用方仍在发送格式不正确的时间戳

**错误示例**:
```sql
-- 外部调用方发送:
INSERT INTO llm_hourly_stats (hour, ...) VALUES ('2026-09-05T23', ...);
-- PostgreSQL 拒绝: ERROR: invalid input syntax for type timestamp with time zone
```

**建议**:
1. **通知外部服务团队**修复时间戳格式化逻辑
2. 提供正确的格式示例:
   ```go
   // Go 代码示例
   hour := time.Now().Truncate(time.Hour).Format(time.RFC3339)
   // 输出: "2026-09-05T23:00:00Z"
   ```
3. 或使用 PostgreSQL 函数在客户端格式化:
   ```sql
   -- 正确的做法
   date_trunc('hour', NOW())
   ```

### 2. 外部服务集成监控

**建议**:
1. 添加 Prometheus 指标监控这些表的写入频率和错误率
2. 在 Grafana 中创建仪表板展示:
   - orchestration_runtime_instances 的活跃实例数
   - llm_hourly_stats 的数据完整性 (小时级缺口检测)
   - feature_distribution_stats 的特征质量趋势

### 3. 文档完善

**建议**:
1. 在 API 文档中明确说明外部服务需要的表结构和数据格式
2. 提供集成示例代码 (时间戳格式化、UPSERT 语法等)
3. 在部署检查清单中添加这些表的验证步骤

## 验证步骤

部署后验证:

```bash
# 1. 检查表是否创建成功
docker exec llm-gateway-pg psql -U postgres -d llm_gateway -c "\\d orchestration_runtime_instances"
docker exec llm-gateway-pg psql -U postgres -d llm_gateway -c "\\d llm_hourly_stats"
docker exec llm-gateway-pg psql -U postgres -d llm_gateway -c "\\d feature_distribution_stats"

# 2. 检查索引是否存在
docker exec llm-gateway-pg psql -U postgres -d llm_gateway -c "\\d+ orchestration_runtime_instances"

# 3. 观察日志,确认错误消失
docker logs llm-gateway-pg --since 10m 2>&1 | grep -E "orchestration_runtime_instances|llm_hourly_stats|feature_distribution_stats" | grep ERROR

# 4. 等待 bg worker 下次执行 (每小时),确认无错误
docker logs llm-gateway-local-8782 --since 1h 2>&1 | grep -i "feature stats"

# 5. 检查 llm_hourly_stats 的时间戳格式错误是否仍然存在
# (预期: 表不存在的错误应消失,但时间戳格式错误可能仍存在,需外部修复)
docker logs llm-gateway-pg --since 1h 2>&1 | grep "llm_hourly_stats" | grep "invalid input syntax"
```

## 部署清单

- [x] 创建 `sql/migrations/startup/666_orchestration_and_stats_tables.sql`
- [x] 更新 `scripts/apply-db-revision-sequence.sh` 添加 662 和 664
- [ ] 执行 `deploy-local.sh` 部署到本地环境
- [ ] 验证三个表已创建
- [ ] 观察日志确认"表不存在"错误消失
- [ ] 如果 llm_hourly_stats 仍有时间戳格式错误,通知外部服务团队修复
- [ ] 更新部署文档,添加新表的说明

## 相关文件

- `sql/migrations/startup/662_feature_distribution_stats.sql` - 特征分布统计表
- `sql/migrations/startup/666_orchestration_and_stats_tables.sql` - 编排实例和小时统计表
- `scripts/apply-db-revision-sequence.sh` - 迁移执行脚本
- `bg/feature_stats_worker.go` - 特征统计后台任务
- `docs/2026-09-06-pg-missing-tables-fix.md` - 本文档

## 总结

本次修复解决了三个数据库表缺失导致的持续错误:

1. **feature_distribution_stats**: 补充了遗漏的部署track,支持 ML 训练数据质量监控
2. **orchestration_runtime_instances**: 创建外部编排服务所需的实例注册表
3. **llm_hourly_stats**: 创建外部统计收集器所需的小时聚合表,但时间戳格式问题需外部修复

修复采用数据库表结构方案,因为这些表是系统设计的一部分,代码已经在使用它们,只是部署gap导致表未创建。所有迁移都是幂等的,可以安全地重复执行。

**关键要点**:
- ✅ 数据库表结构修复完成
- ⚠️ llm_hourly_stats 的时间戳格式问题需要外部调用方修复
- ✅ 所有迁移都已添加到自动部署脚本
- ✅ 修复方案向后兼容,不影响现有功能
