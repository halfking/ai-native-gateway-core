# LLM Gateway 数据库结构与初始化数据

本目录保存 `llm_gateway` 的数据库结构和最小初始化数据。
当前拆分 SQL 以 `184` 节点真实数据库为基线整理，并已额外核对 `71` 与本地 `llm_gateway` 容器环境。

## 基线与校验

- 基线环境：`184 / llm_gateway / PostgreSQL 15.3`
- 对比环境：`71 / llm_gateway`、本地 `local-main-pg / llm_gateway`
- 已完成本地临时库整套回放验证：
  - `00_schema/*.sql`
  - `01_functions/functions.sql`
  - `02_seed_data/*.sql`
- 回放验证依赖扩展：`pg_trgm`、`citus_columnar`；本地容器同时启用了 `citus`

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
- `184`、`71`、本地在具体月份分区上并不完全一致，详见 [VERSION.md](__DEV_HOME__/workspace/llm-gateway-go-2/deploy/sql/VERSION.md)。

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

这批文件是“以 184 为部署基线的可执行整理版”，目标是满足新环境初始化，而不是保留完整生产业务数据镜像。

如果后续需要继续追求“与 184 当前实库一比一完全等价”，优先方式仍然是基于 `pg_dump --schema-only` 继续精确覆盖，而不是再手工扩写结构。