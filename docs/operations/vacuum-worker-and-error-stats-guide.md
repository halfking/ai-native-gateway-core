# VacuumWorker 和 Admin API 错误统计集成指南

## 概述

本文档说明如何使用新集成的 VacuumWorker 和 Admin API 错误统计功能。

---

## 1. VacuumWorker - 自动 VACUUM 功能

### 功能说明

VacuumWorker 定期对 `request_logs_bodies` 表执行 VACUUM FULL，回收 TOAST 表空间，防止数据库膨胀。

### 默认配置

- **执行频率**: 每周一次（7天）
- **执行时间**: 周日凌晨 2:00
- **目标表**: `request_logs_bodies` 和 `request_logs_bodies_hot`

### 环境变量配置

#### `LLM_GATEWAY_VACUUM_INTERVAL_HOURS`

设置 VACUUM 执行间隔（小时）。

```bash
# 每3天执行一次
export LLM_GATEWAY_VACUUM_INTERVAL_HOURS=72

# 每周执行一次（默认）
export LLM_GATEWAY_VACUUM_INTERVAL_HOURS=168
```

#### `LLM_GATEWAY_VACUUM_HOUR`

设置每天的执行时间（0-23小时）。

```bash
# 凌晨3点执行
export LLM_GATEWAY_VACUUM_HOUR=3

# 凌晨2点执行（默认）
export LLM_GATEWAY_VACUUM_HOUR=2

# 晚上11点执行
export LLM_GATEWAY_VACUUM_HOUR=23
```

### 示例配置

#### 生产环境（推荐）
```bash
# 每周日凌晨2点执行（默认配置，无需设置）
# VACUUM 会锁表，选择业务低峰期
```

#### 测试环境
```bash
# 每天凌晨3点执行，便于观察效果
export LLM_GATEWAY_VACUUM_INTERVAL_HOURS=24
export LLM_GATEWAY_VACUUM_HOUR=3
```

#### 快速测试
```bash
# 每小时执行一次，用于验证功能
export LLM_GATEWAY_VACUUM_INTERVAL_HOURS=1
# 设置为当前小时，启动后会立即执行
export LLM_GATEWAY_VACUUM_HOUR=$(date +%H)
```

### 日志监控

VacuumWorker 会输出详细的执行日志：

```log
# 启动日志
INFO vacuum worker started default_schedule="weekly Sunday 2am"

# 执行开始
INFO vacuum worker: starting VACUUM FULL on request_logs_bodies

# 执行完成
INFO vacuum worker: VACUUM FULL completed elapsed_seconds=125.43

# Hot表VACUUM
INFO vacuum worker: VACUUM on hot table completed

# 错误日志
ERROR vacuum worker: VACUUM FULL failed error="..." elapsed_seconds=30.12
```

### 监控指标

建议监控以下指标：

1. **表大小趋势**
   ```sql
   SELECT 
       pg_size_pretty(pg_total_relation_size('request_logs_bodies')) AS total_size,
       pg_size_pretty(pg_relation_size('request_logs_bodies')) AS table_size,
       pg_size_pretty(pg_total_relation_size('request_logs_bodies') - pg_relation_size('request_logs_bodies')) AS toast_size;
   ```

2. **VACUUM 执行时长**
   - 观察日志中的 `elapsed_seconds`
   - 正常情况下应在 2-10 分钟内完成

3. **空间回收效果**
   - 执行前后对比表大小
   - TOAST 表应明显减小

### 注意事项

⚠️ **VACUUM FULL 会锁表**
- 执行期间无法对表进行写操作
- 务必选择业务低峰期（如周末凌晨）
- 默认设置已考虑此因素

⚠️ **执行时长**
- 执行时间取决于表大小和数据量
- 大表可能需要 10-30 分钟
- 设置了 30 分钟超时保护

💡 **最佳实践**
- 生产环境使用默认配置（每周日凌晨2点）
- 首次启用时观察一次完整执行
- 定期检查空间回收效果

---

## 2. Admin API 错误统计功能

### API 端点

```
GET /api/providers/{provider_id}/error-stats
```

### 查询参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `hours` | int | 24 | 统计时间范围（小时），最大 720 |
| `limit` | int | 100 | 返回记录数限制，最大 1000 |
| `resolved` | string | all | 过滤条件：`true`, `false`, `all` |

### 请求示例

#### 查询最近24小时的所有错误
```bash
curl -X GET "http://localhost:8080/api/providers/1/error-stats" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### 查询最近7天的前50个未解决错误
```bash
curl -X GET "http://localhost:8080/api/providers/1/error-stats?hours=168&limit=50&resolved=false" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### 查询最近1小时的已解决错误
```bash
curl -X GET "http://localhost:8080/api/providers/1/error-stats?hours=1&resolved=true" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

### 响应格式

```json
{
  "provider_id": 1,
  "time_range_hours": 24,
  "total_errors": 15,
  "total_occurrences": 342,
  "resolved_count": 3,
  "unresolved_count": 12,
  "errors": [
    {
      "model_name": "gpt-4",
      "endpoint": "https://api.openai.com/v1/chat/completions",
      "error_type": "rate_limit",
      "error_code": "429",
      "error_message": "Rate limit exceeded",
      "aggregation_bucket": "2026-08-30T10:00:00Z",
      "occurrences": 156,
      "first_seen_at": "2026-08-30T10:03:21Z",
      "last_seen_at": "2026-08-30T10:58:43Z",
      "resolved": false,
      "created_at": "2026-08-30T10:10:00Z",
      "updated_at": "2026-08-30T11:00:00Z"
    }
  ]
}
```

### 响应字段说明

#### 顶层字段
- `provider_id`: Provider ID
- `time_range_hours`: 查询的时间范围（小时）
- `total_errors`: 错误类型总数
- `total_occurrences`: 错误发生总次数
- `resolved_count`: 已解决的错误数
- `unresolved_count`: 未解决的错误数

#### 错误详情字段
- `model_name`: 模型名称
- `endpoint`: API 端点
- `error_type`: 错误类型（如 `rate_limit`, `timeout`, `auth_error`）
- `error_code`: HTTP 状态码或错误代码
- `error_message`: 错误消息
- `aggregation_bucket`: 聚合时间桶（10分钟粒度）
- `occurrences`: 该时间桶内的发生次数
- `first_seen_at`: 首次出现时间
- `last_seen_at`: 最后出现时间
- `resolved`: 是否已解决
- `created_at`: 记录创建时间
- `updated_at`: 记录更新时间

### 使用场景

#### 1. 监控面板
定期查询最近24小时的未解决错误，展示在监控面板上：
```bash
GET /api/providers/1/error-stats?hours=24&resolved=false&limit=10
```

#### 2. 告警系统
当 `unresolved_count` 或 `total_occurrences` 超过阈值时触发告警。

#### 3. 问题排查
查询特定时间范围的错误，定位问题发生时间：
```bash
GET /api/providers/1/error-stats?hours=1&limit=100
```

#### 4. 趋势分析
定期采集数据，分析错误趋势和模式。

### 权限要求

- 需要 `super_admin` 或 `admin` 角色
- 已集成到现有的 Admin API 权限体系

### 数据来源

错误统计数据来自 `provider_error_details` 表，该表由 `ProviderErrorAggregator` 后台任务自动聚合：

- 每 10 分钟聚合一次
- 从 `candidate_failure_logs_hot` 表读取原始错误
- 按 provider、model、endpoint、error_type 聚合

---

## 3. 部署清单

### 启动前检查

- [x] VacuumWorker 已集成到 main.go
- [x] Admin API 路由已注册
- [x] 环境变量配置（如需自定义）
- [x] 数据库表 `provider_error_details` 已创建

### 部署步骤

1. **编译**
   ```bash
   go build ./cmd/gateway/...
   ```

2. **配置环境变量**（可选）
   ```bash
   export LLM_GATEWAY_VACUUM_INTERVAL_HOURS=168
   export LLM_GATEWAY_VACUUM_HOUR=2
   ```

3. **启动服务**
   ```bash
   ./gateway
   ```

4. **验证启动日志**
   ```log
   INFO vacuum worker started default_schedule="weekly Sunday 2am"
   ```

5. **测试 API**
   ```bash
   curl -X GET "http://localhost:8080/api/providers/1/error-stats"
   ```

### 回滚方案

如果需要禁用 VacuumWorker：

1. 注释 main.go 中的 VacuumWorker 初始化代码
2. 重新编译部署
3. 或者设置一个极大的间隔值（事实上禁用）

---

## 4. 故障排查

### VacuumWorker 未执行

**症状**: 日志中没有 VACUUM 执行记录

**检查**:
1. 确认启动日志中有 "vacuum worker started"
2. 检查当前时间是否匹配 `executeHour`
3. 检查是否距离上次执行已超过 `interval`

**解决**:
- 设置环境变量强制执行：
  ```bash
  export LLM_GATEWAY_VACUUM_HOUR=$(date +%H)
  export LLM_GATEWAY_VACUUM_INTERVAL_HOURS=1
  ```

### API 返回空结果

**症状**: `total_errors` 为 0

**可能原因**:
1. `provider_error_details` 表为空
2. `ProviderErrorAggregator` 未启动
3. 时间范围内确实没有错误

**检查**:
```sql
SELECT COUNT(*) FROM provider_error_details WHERE provider_id = 1;
```

### VACUUM 执行超时

**症状**: 日志显示 "VACUUM FULL failed" 且 error 包含 "context deadline exceeded"

**解决**:
- 表太大，30分钟超时不够
- 考虑在更低峰期执行
- 或者先手动执行普通 VACUUM，再执行 VACUUM FULL

---

## 5. 性能影响

### VacuumWorker

- **CPU**: 执行期间 CPU 使用率会上升 10-30%
- **I/O**: 大量磁盘读写
- **锁**: 锁表期间无法写入（读取不受影响）
- **建议**: 在业务低峰期执行

### Admin API 错误统计

- **查询性能**: 通常 < 100ms
- **数据库负载**: 轻量级，有索引支持
- **并发**: 支持高并发查询

---

## 6. 常见问题

**Q: VACUUM 执行期间服务会中断吗？**  
A: 不会。只是 `request_logs_bodies` 表无法写入，但该表有 24 小时保留期，短时间锁表不影响服务。

**Q: 可以手动触发 VACUUM 吗？**  
A: 可以。直接执行 SQL: `VACUUM FULL request_logs_bodies;`

**Q: 错误统计数据保留多久？**  
A: 由 `provider_error_details` 表的 TTL 策略决定，建议保留 30-90 天。

**Q: 为什么有些错误没有出现在统计中？**  
A: 聚合任务每 10 分钟运行一次，存在最多 10 分钟的延迟。

---

## 联系支持

如有问题，请查看日志或联系运维团队。
