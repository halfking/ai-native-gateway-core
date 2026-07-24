# Sessions V2 Feature Flag 配置指南

## 概述

Sessions V2 是会话存储的优化架构，通过新的表体系（`gateway.sessions`, `gateway.session_turns`, `gateway.session_bodies`, `gateway.session_turn_logs`）替代原有的 `request_logs` 表，实现增量存储和更高效的查询。

## Feature Flags

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `sessions_v2.enabled` | bool | false | 总开关，必须开启才能使用其他功能 |
| `sessions_v2.shadow_write` | bool | false | 副写模式：同时写入V1和V2表 |
| `sessions_v2.rollout_percent` | int | 0 | 灰度百分比（0-100），基于session_id哈希 |
| `sessions_v2.write_timeout_ms` | int | 500 | V2写入超时（毫秒） |
| `sessions_v2.read_timeout_ms` | int | 300 | V2读取超时（毫秒） |
| `sessions_v2.compression_enabled` | bool | true | 启用压缩检测 |
| `sessions_v2.turn_logs_retention_hours` | int | 24 | 环节日志保留时间（小时） |

## 部署步骤

### 本地环境

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/scripts

# 连接本地数据库并执行
psql -U postgres -d llmgateway -f enable_sessions_v2.sql
```

**预期输出**:
```
                    key                     | value | type |         updated_at         
-------------------------------------------+-------+------+----------------------------
 sessions_v2.compression_enabled           | true  | bool | 2026-07-24 10:00:00.123456
 sessions_v2.enabled                       | true  | bool | 2026-07-24 10:00:00.123456
 sessions_v2.read_timeout_ms               | 300   | int  | 2026-07-24 10:00:00.123456
 sessions_v2.rollout_percent               | 100   | int  | 2026-07-24 10:00:00.123456
 sessions_v2.shadow_write                  | true  | bool | 2026-07-24 10:00:00.123456
 sessions_v2.turn_logs_retention_hours     | 24    | int  | 2026-07-24 10:00:00.123456
 sessions_v2.write_timeout_ms              | 500   | int  | 2026-07-24 10:00:00.123456
```

### 生产环境（154/245）

**⚠️ 重要：生产环境采用灰度发布，从1%开始**

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/scripts

# 154环境
psql -h 154.xxx.xxx.xxx -U postgres -d llmgateway -f enable_sessions_v2_prod.sql

# 245环境
psql -h 245.xxx.xxx.xxx -U postgres -d llmgateway -f enable_sessions_v2_prod.sql
```

## 灰度发布时间表（154/245）

### Day 1 (2026-07-24): 1% 灰度
- 执行 `enable_sessions_v2_prod.sql`
- 监控指标：
  ```sql
  -- 检查V2表写入情况
  SELECT COUNT(*) as total_turns, 
         COUNT(DISTINCT session_id) as unique_sessions
  FROM gateway.session_turns 
  WHERE ts > NOW() - INTERVAL '1 hour';
  
  -- 检查错误日志
  SELECT * FROM logs WHERE message LIKE '%session_persist_hook%' AND level = 'ERROR';
  ```
- 观察24小时

### Day 2 (2026-07-25): 10% 灰度
如果Day 1无异常，增加到10%：

```sql
UPDATE platform_settings 
SET value = '10', updated_at = NOW() 
WHERE key = 'sessions_v2.rollout_percent';
```

### Day 3 (2026-07-26): 100% 全量
如果Day 2无异常，增加到100%：

```sql
UPDATE platform_settings 
SET value = '100', updated_at = NOW() 
WHERE key = 'sessions_v2.rollout_percent';
```

## 验证

### 1. 检查Feature Flags状态

```sql
SELECT key, value, updated_at 
FROM platform_settings 
WHERE key LIKE 'sessions_v2.%'
ORDER BY key;
```

### 2. 检查V2表数据写入

```sql
-- 最近1小时的session_turns记录数
SELECT COUNT(*) FROM gateway.session_turns 
WHERE ts > NOW() - INTERVAL '1 hour';

-- 最近1小时的session_bodies记录数
SELECT COUNT(*) FROM gateway.session_bodies 
WHERE ts > NOW() - INTERVAL '1 hour';

-- 最近1小时的sessions记录数
SELECT COUNT(*) FROM gateway.sessions 
WHERE updated_at > NOW() - INTERVAL '1 hour';
```

### 3. 对比V1和V2数据一致性

```sql
-- 找一个V2已记录的session_id
SELECT session_id, COUNT(*) as turn_count 
FROM gateway.session_turns 
WHERE ts > NOW() - INTERVAL '1 hour'
GROUP BY session_id 
LIMIT 1;

-- 用这个session_id在request_logs中查找
SELECT COUNT(*) 
FROM request_logs 
WHERE gw_session_id = '<上面查到的session_id>';

-- 两个count应该相同
```

### 4. 查看应用日志

```bash
# 本地环境
tail -f /var/log/llm-gateway/gateway.log | grep session_persist

# 154/245环境
ssh 154.xxx.xxx.xxx "tail -f /var/log/llm-gateway/gateway.log | grep session_persist"
```

**预期日志**（无错误）：
```
INFO v2 pipeline: session persist hook wired (dual-write ready) pool_healthy=true
WARN session_persist_hook: V2 write failed (best-effort) ...  # 偶尔出现，可忽略
```

## 监控指标

### API端点
- `/api/admin/sessions/v2/status` - V2功能状态
- `/api/admin/sessions/v2/shadow_metrics` - 副写指标
- `/api/admin/sessions/v2/rollout_stats` - 灰度统计

### 关键指标
1. **写入成功率**: `session_turns` 写入成功/总请求 > 99%
2. **写入延迟**: P99 < 500ms
3. **数据一致性**: V1和V2的session记录数差异 < 1%

## 回滚

如果出现问题，立即回滚：

```sql
-- 关闭shadow_write
UPDATE platform_settings 
SET value = 'false', updated_at = NOW() 
WHERE key = 'sessions_v2.shadow_write';

-- 或者直接关闭总开关
UPDATE platform_settings 
SET value = 'false', updated_at = NOW() 
WHERE key = 'sessions_v2.enabled';
```

重启gateway服务以确保设置立即生效：
```bash
systemctl restart llm-gateway
```

## 故障排查

### 问题：V2表无数据写入

**检查**:
1. Feature flags是否已生效：
   ```sql
   SELECT * FROM platform_settings WHERE key LIKE 'sessions_v2.%';
   ```
2. Gateway是否已重启（feature flags需要重启生效）
3. 检查日志是否有错误：
   ```bash
   grep -i "session_persist\|sessions_v2" /var/log/llm-gateway/gateway.log | tail -50
   ```

### 问题：写入延迟过高

**检查**:
1. 数据库连接池是否耗尽
2. `sessions_v2.write_timeout_ms` 是否过小
3. 数据库负载是否过高

**临时缓解**:
```sql
-- 增加写入超时
UPDATE platform_settings 
SET value = '1000' 
WHERE key = 'sessions_v2.write_timeout_ms';

-- 降低灰度百分比
UPDATE platform_settings 
SET value = '10' 
WHERE key = 'sessions_v2.rollout_percent';
```

## 下一步

完成Feature Flags配置后，继续实施：
1. **后端API开发**: `admin/session_detail_v2.go` - 会话详情查询
2. **前端页面开发**: `web/src/views/SessionDetailView.vue` - 会话详情UI
3. **会话总结功能**: 调用LLM对会话内容进行总结

## 相关文档

- [Migration 430: Sessions V2 Schema](../sql/migrations/startup/430_sessions_v2_schema.sql)
- [SessionPersistHook Implementation](../domains/session/v2/pipeline_hook.go)
- [Settings Spec](../settings/spec_sessions_v2.go)
