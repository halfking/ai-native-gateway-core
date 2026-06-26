# LLM Gateway 数据库结构与初始化数据

本目录包含从184服务器（pms-prod环境）导出的数据库结构定义和初始化数据，用于新环境部署时的数据库初始化。

## 目录结构

```
deploy/sql/
├── README.md                          # 本文件
├── VERSION.md                         # 版本说明和Columnar配置
├── llm_gateway_schema_2026-06-26.sql  # 完整schema快照（参考用）
├── 00_schema/                         # 表结构定义（按功能分文件）
│   ├── 001_base_tables.sql            # 基础表（租户、用户、应用、系统配置）
│   ├── 002_providers_and_models.sql   # 提供商和模型相关表
│   ├── 003_routing_tables.sql         # 路由相关表（凭据、探测、覆盖）
│   ├── 004_tuning_and_work_types.sql  # 调优和工作类型表
│   ├── 005_maas_billing.sql           # MaaS计费相关表
│   ├── 006_request_logs.sql           # 请求日志分区表（heap存储）
│   ├── 007_archive_and_ledger.sql      # 归档表和WAL表（columnar存储）
│   └── 008_tools_registry.sql          # 工具注册表和辅助表
├── 01_functions/                      # 函数和触发器
│   └── functions.sql                  # 所有函数定义
├── 02_seed_data/                      # 初始化数据
│   ├── 001_basic.sql                  # 基础数据（租户、用户、应用）
│   ├── 002_providers.sql              # 提供商配置
│   └── 003_work_types.sql             # 工作类型配置
└── scripts/                           # 辅助脚本
    └── init-db.sh                     # 数据库初始化脚本
```

## 快速开始

### 1. 创建数据库和扩展

```bash
# 连接到PostgreSQL
psql -h localhost -U postgres -d postgres

# 创建数据库
CREATE DATABASE llm_gateway;
\c llm_gateway

# 启用扩展
CREATE EXTENSION IF NOT EXISTS citus;
CREATE EXTENSION IF NOT EXISTS columnar;
```

### 2. 初始化表结构

```bash
# 按顺序执行schema文件
psql -h localhost -U postgres -d llm_gateway -f 00_schema/001_base_tables.sql
psql -h localhost -U postgres -d llm_gateway -f 00_schema/002_providers_and_models.sql
psql -h localhost -U postgres -d llm_gateway -f 00_schema/003_routing_tables.sql
psql -h localhost -U postgres -d llm_gateway -f 00_schema/004_tuning_and_work_types.sql
psql -h localhost -U postgres -d llm_gateway -f 00_schema/005_maas_billing.sql
psql -h localhost -U postgres -d llm_gateway -f 00_schema/006_request_logs.sql
psql -h localhost -U postgres -d llm_gateway -f 00_schema/007_archive_and_ledger.sql
psql -h localhost -U postgres -d llm_gateway -f 00_schema/008_tools_registry.sql

# 创建函数和触发器
psql -h localhost -U postgres -d llm_gateway -f 01_functions/functions.sql
```

### 3. 初始化基础数据

```bash
psql -h localhost -U postgres -d llm_gateway -f 02_seed_data/001_basic.sql
psql -h localhost -U postgres -d llm_gateway -f 02_seed_data/002_providers.sql
psql -h localhost -U postgres -d llm_gateway -f 02_seed_data/003_work_types.sql
```

## 重要说明

### Columnar 存储

生产环境使用 PostgreSQL Columnar 扩展存储历史归档数据：
- **request_logs_archive**: 历史数据使用 columnar 格式存储
- **当前月份**: 仍使用 heap 存储，保证写入性能

```sql
-- 创建 columnar 分区示例
CREATE TABLE request_logs_archive_2026_09
    PARTITION OF request_logs_archive
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01')
    USING columnar;
```

### 数据初始化策略

**保留的数据**:
- `tenants`: 仅 `default` 租户
- `users`: 仅 `default` 租户的 `admin` 用户
- `providers`: 提供商配置（不含敏感凭据）
- `work_type_config`: 工作类型配置

**不包含的数据**:
- `api_keys`: API密钥（凭据数据）
- `credentials`: 凭据信息
- `request_logs*`: 请求日志（数据量大）
- `billing_orders`: 计费订单
- 其他业务明细数据

### RLS（行级安全策略）

所有租户相关表都启用了RLS，使用 `get_current_tenant()` 函数进行隔离：

```sql
-- 设置当前租户
SET app.current_tenant = 'default';
SELECT get_current_tenant();  -- 返回 'default'
```

## 版本信息

详见 [VERSION.md](./VERSION.md)

## 源数据

- **服务器**: __INTERNAL_PUBLIC_IP__ (184核心应用节点)
- **数据库**: llm_gateway
- **PostgreSQL版本**: 15.3-1.pgdg120+1
- **扩展**: Citus, Columnar
- **导出日期**: 2026-06-27