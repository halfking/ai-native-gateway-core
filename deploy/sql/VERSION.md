# LLM Gateway 数据库 Schema 版本说明

## 版本信息

| 项目 | 版本/信息 |
|------|----------|
| **源服务器** | __INTERNAL_PUBLIC_IP__ (184核心应用节点) |
| **导出日期** | 2026-06-27 |
| **数据库名** | llm_gateway |
| **数据库版本** | PostgreSQL 15.3-1.pgdg120+1 |

## 扩展组件

| 组件 | 说明 |
|------|------|
| **Citus** | 分布式数据库扩展 |
| **Columnar** | 列式存储扩展（用于历史数据归档） |

## PostgreSQL 要求

- **最低版本**: PostgreSQL 15.x
- **推荐版本**: PostgreSQL 15.3 或更高
- **必需扩展**: Citus, columnar

### 安装扩展

```sql
-- 确保扩展已安装
CREATE EXTENSION IF NOT EXISTS citus;
CREATE EXTENSION IF NOT EXISTS columnar;
```

## Columnar 存储策略

### 什么是 Columnar

Columnar 是一种列式存储引擎，相比传统的行式存储（heap）：
- **优势**: 压缩率高、聚合查询快、只读取需要的列
- **适用场景**: 历史数据归档、分析型查询
- **限制**: 不支持 UPDATE/DELETE（只支持 INSERT 和批量删除）

### 本项目的 Columnar 使用

| 表类型 | 存储方式 | 说明 |
|--------|----------|------|
| request_logs | heap | 当前月份数据，使用行式存储保证写入性能 |
| request_logs_archive | columnar | 历史归档数据，使用列式存储节省空间 |

### 创建 Columnar 分区的示例

```sql
-- 创建按月分区的 columnar 表
CREATE TABLE request_logs_archive_2026_06
    PARTITION OF request_logs_archive
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00')
    USING columnar;

-- 查看分区存储类型
SELECT 
    relname,
    relkind,
    CASE 
        WHEN relstorage = 'c' THEN 'columnar'
        WHEN relstorage = 'h' THEN 'heap'
        WHEN relstorage = 'a' THEN 'append-optimized'
        ELSE relstorage
    END as storage_type
FROM pg_class 
WHERE relname LIKE 'request_logs%';
```

## 分区表

### 主分区表

1. **request_logs** - 请求日志（按月分区）
2. **request_logs_archive** - 请求日志归档（按月分区，columnar）
3. **credit_ledger** - 积分账本（按月分区）
4. **usage_ledger** - 使用量账本（按月分区）
5. **tool_usage_stats** - 工具使用统计（按月分区）
6. **request_wal** - 请求WAL日志（按月分区）

### 分区命名规则

```
{表名}_{YYYY_MM}
例如: request_logs_2026_07, usage_ledger_2026_06
```

## RLS（行级安全策略）

所有租户相关表都启用了RLS，策略使用 `get_current_tenant()` 函数进行租户隔离。

```sql
-- 查看当前租户
SELECT get_current_tenant();

-- 设置会话租户
SET app.current_tenant = 'default';
```

## 初始化数据说明

### 保留的数据

| 表 | 说明 |
|----|------|
| tenants | 仅 default 租户 |
| users | 仅 default 租户的 admin 用户 |
| applications | admin 和 applicant 应用 |
| providers | 提供商配置（不含凭据） |
| work_type_config | 工作类型配置 |

### 不包含的数据

- api_keys (API密钥)
- credentials (凭据信息)
- request_logs* (请求日志)
- billing_orders (计费订单)
- 其他业务明细数据

## 环境变量

```bash
# 设置数据库连接
export LLM_GATEWAY_DATABASE_URL="postgresql://user:pass@host:5432/llm_gateway"
```

## 部署检查清单

- [ ] PostgreSQL 15.x 已安装
- [ ] Citus 扩展已启用
- [ ] Columnar 扩展已启用
- [ ] 数据库用户权限已配置
- [ ] 表结构已创建
- [ ] 初始数据已导入
- [ ] 分区表已创建
- [ ] RLS 策略已生效