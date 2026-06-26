# LLM Gateway 数据库结构与初始化数据

本目录保存 `llm_gateway` 的数据库结构和最小初始化数据，面向单机目标库初始化。

## 校验说明

这批拆分 SQL 已完成一次完整回放验证，验证范围包括：
- `00_schema/*.sql`
- `01_functions/functions.sql`
- `02_seed_data/*.sql`

回放验证依赖的扩展能力：
- `pg_trgm`
- `citus_columnar`
- 如运行环境启用了 `citus`，不影响本目录下 SQL 的使用

## 目录结构

```text
deploy/sql/
├── README.md
├── VERSION.md
├── 00_schema/                          # 按功能拆分的表结构
├── 01_functions/                       # 函数/触发器
├── 02_seed_data/                       # 最小初始化数据
└── scripts/init-db.sh                  # 初始化脚本
```

## 数据策略

### 保留的初始化数据
- `tenants`: 仅 `default`
- `users`: 仅 `admin`
- `applications`: 仅 `admin` / `applicant`
- `providers`: 仅标准 provider 配置，不含 credentials，不含详细业务化自定义条目
- `work_type_config` / `work_type_model_route`

### 不初始化的数据
- `api_keys`
- `credentials`
- `request_logs*` 实际请求数据
- `request_wal*` 实际 WAL 数据
- `credit_ledger*` / `usage_ledger*` / `billing_orders`
- 详细审计、监控、运营、业务明细数据

## Columnar / 分区说明

- 当前活跃请求日志表 `request_logs` 使用 heap 分区。
- 历史归档表 `request_logs_archive` 使用 `citus_columnar` 列式分区。
- 月度分区需要根据实际时间窗口创建或预创建，详见 [VERSION.md](__DEV_HOME__/workspace/llm-gateway-go-2/deploy/sql/VERSION.md)。

## 初始化顺序

```bash
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/001_base_tables.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/002_providers_and_models.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/003_routing_tables.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/004_tuning_and_work_types.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/005_maas_billing.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/006_request_logs.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/007_archive_and_ledger.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/00_schema/008_tools_registry.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/01_functions/functions.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/02_seed_data/001_basic.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/02_seed_data/002_providers.sql
psql -h <host> -U <user> -d <db> -f deploy/sql/02_seed_data/003_work_types.sql
```

## 使用边界

这批文件用于单机目标库初始化，目标是提供可执行的结构和最小初始化数据，而不是保留完整生产业务数据镜像。

如果后续需要继续追求“与某一时点实库一比一完全等价”，优先方式仍然是基于 `pg_dump --schema-only` 继续精确覆盖，而不是再手工扩写结构。