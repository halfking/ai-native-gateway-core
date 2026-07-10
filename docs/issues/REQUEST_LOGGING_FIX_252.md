# 请求日志未保存到252数据库问题修复

## 问题描述

154服务器上 `llm.kxpms.cn` 的所有请求没有保存到252的PostgreSQL数据库中。

## 根本原因

代码使用 `request_wal_hot` 表进行请求日志记录，但252数据库中缺少该表。

### 代码位置

**文件**: `domains/hooks/observability/telemetry/request_logger.go`

- **第114行**: `INSERT INTO request_wal_hot` - 创建初始请求记录
- **第257行**: `UPDATE request_wal_hot` - 更新请求状态和统计信息
- **第294行**: `INSERT INTO request_wal_bodies` - 保存请求体

### 表依赖关系

```
代码写入流程:
1. CreateInitial() → INSERT INTO request_wal_hot
2. Update() → UPDATE request_wal_hot  
3. (可选) → INSERT INTO request_wal_bodies

查询流程:
- request_wal_with_current_month 视图
  ├─ request_wal_hot (热表, 0-7天)
  └─ request_wal (父表, 包含月度分区)
```

## 为什么会出现这个问题

`request_wal_hot` 表是通过迁移脚本创建的，而不是在基础schema中：

**迁移脚本**: `sql/migrations/startup/345_request_wal_hot_independence.sql`

该迁移脚本的作用：
1. 创建独立的 `request_wal_hot` 热表（替代 `request_wal_default` 分区）
2. 创建 `request_wal_with_current_month` 视图
3. 迁移现有数据
4. 优化查询性能（热表架构）

如果252数据库：
- 只执行了基础schema（`sql/schema/01-schema.sql`）
- 没有执行启动迁移脚本
- 或者是从旧版本数据库恢复的

就会缺少 `request_wal_hot` 表，导致所有请求日志写入失败。

## 解决方案

### 方案1：执行修复脚本（推荐）

创建了幂等的修复脚本，可以安全地重复执行：

```bash
# 连接到252数据库并执行
cd /Users/xutaohuang/workspace/llm-gateway-go-cursor
psql -h 192.168.0.252 -U postgres -d llm_gateway \
  -f sql/fixes/fix-missing-request-wal-hot.sql
```

修复脚本会：
- ✓ 检查并创建 `request_wal_hot` 表
- ✓ 检查并创建 `request_wal_bodies` 表
- ✓ 检查并创建 `request_wal_with_current_month` 视图
- ✓ 迁移 `request_wal_default` 数据（如果存在）
- ✓ 验证表结构和数据完整性

### 方案2：执行原始迁移脚本

```bash
psql -h 192.168.0.252 -U postgres -d llm_gateway \
  -f sql/migrations/startup/345_request_wal_hot_independence.sql
```

### 方案3：使用验证脚本（自动化）

```bash
# 交互式诊断和修复
./scripts/verify-request-logging-252.sh
```

该脚本会：
1. 检查当前状态
2. 应用修复（需要确认）
3. 验证表结构
4. 测试写入功能
5. 显示统计信息

## 验证修复

### 1. 检查表是否存在

```sql
SELECT EXISTS (
    SELECT 1 FROM pg_class 
    WHERE relname = 'request_wal_hot' 
    AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
) AS table_exists;
```

预期结果: `t` (true)

### 2. 检查表结构

```sql
SELECT column_name, data_type 
FROM information_schema.columns 
WHERE table_name = 'request_wal_hot' 
ORDER BY ordinal_position;
```

预期字段：
- request_id
- tenant_id
- gw_session_id
- status
- stage
- client_model
- upstream_provider_id
- upstream_credential_id
- completion_tokens
- prompt_tokens
- created_at
- completed_at
- upstream_request_at
- upstream_response_at
- error
- compression_strategy
- compression_meta

### 3. 测试写入

```sql
-- 插入测试记录
INSERT INTO request_wal_hot (
    request_id, tenant_id, status, stage, client_model, created_at
) VALUES (
    'test_' || extract(epoch from now())::text, 
    'test', 
    'pending', 
    0, 
    'gpt-4', 
    NOW()
) ON CONFLICT (request_id, created_at) DO NOTHING
RETURNING request_id;

-- 清理测试数据
DELETE FROM request_wal_hot WHERE tenant_id = 'test';
```

### 4. 检查实际请求日志

```sql
-- 最近1小时的请求
SELECT COUNT(*), MAX(created_at) 
FROM request_wal_hot 
WHERE created_at > NOW() - INTERVAL '1 hour';

-- 最近的请求详情
SELECT 
    request_id,
    status,
    stage,
    client_model,
    prompt_tokens,
    completion_tokens,
    created_at
FROM request_wal_hot 
ORDER BY created_at DESC 
LIMIT 10;
```

### 5. 重启154服务器上的网关

修复数据库后，需要确保154上的应用正常连接：

```bash
ssh root@192.168.0.154 'systemctl restart llm-gateway'
ssh root@192.168.0.154 'systemctl status llm-gateway'
```

### 6. 发送测试请求

```bash
curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### 7. 验证请求已记录

```sql
-- 检查最近5分钟的新请求
SELECT COUNT(*), MAX(created_at) 
FROM request_wal_hot 
WHERE created_at > NOW() - INTERVAL '5 minutes';
```

## 预防措施

### 1. 部署检查清单

在任何新环境部署时，确保执行：
- ✓ 基础schema: `sql/schema/01-schema.sql`
- ✓ 启动迁移: `sql/migrations/startup/*.sql`（按数字顺序）
- ✓ 必要的修复: `sql/fixes/*.sql`

### 2. 健康检查

添加启动时的健康检查，验证必要的表存在：

```go
// 在应用启动时检查
requiredTables := []string{
    "request_wal_hot",
    "request_wal_bodies",
}

for _, table := range requiredTables {
    var exists bool
    err := db.QueryRow(
        "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)",
        table,
    ).Scan(&exists)
    
    if err != nil || !exists {
        log.Fatalf("Required table %s does not exist", table)
    }
}
```

### 3. 监控

设置监控告警：
- 请求日志写入失败率
- `request_wal_hot` 表增长趋势
- 数据库连接错误

### 4. 文档更新

更新部署文档：
- 明确标注 `request_wal_hot` 是必需的
- 包含验证步骤
- 提供快速修复指南

## 相关文件

### 创建的修复工具

1. **sql/fixes/fix-missing-request-wal-hot.sql** - 幂等修复脚本
2. **scripts/verify-request-logging-252.sh** - 自动化验证脚本
3. **scripts/diagnose-request-logging.sh** - 诊断工具

### 相关代码

1. **domains/hooks/observability/telemetry/request_logger.go** - 请求日志记录逻辑
2. **sql/migrations/startup/345_request_wal_hot_independence.sql** - 原始迁移脚本
3. **sql/schema/01-schema.sql** - 基础schema定义

## 时间线

- **问题发现**: 2026-07-09 - 154上的请求未保存到252数据库
- **根因分析**: 2026-07-09 - 确认缺少 `request_wal_hot` 表
- **修复方案**: 2026-07-09 - 创建修复脚本和验证工具
- **待执行**: 在252数据库上应用修复

## 影响评估

### 数据丢失
- ✗ 修复前的所有请求日志丢失
- ✓ 修复后的请求将正常记录

### 系统影响
- ✗ 请求统计功能不可用
- ✗ 请求审计日志缺失
- ✗ 计费数据不完整
- ✓ 核心转发功能正常（请求日志写入是异步的，失败不影响转发）

### 优先级
**P0 - 紧急** - 影响数据完整性和计费

## 联系人

- 技术负责人: @xutaohuang
- 日期: 2026-07-09
