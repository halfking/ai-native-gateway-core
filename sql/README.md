# sql/ — Database Assets

> 本目录是 llm-gateway-go 所有数据库资产的 **单一可信源 (SSOT)**。
> 原零散分布于 `deploy/sql/`, `db/migrations/`, `migrations/`, `installer/sql/` 等位置的 SQL 文件统一整合于此。

## 目录结构

```
sql/
├── README.md                          # 本文档
├── schema/                            # 完整 DDL 快照（pg_dump 输出）
│   ├── 00-prereqs.sql                 # PostgreSQL 扩展
│   ├── 01-schema.sql                  # 完整 public schema
│   └── 02-seed.sql                    # 系统级初始数据
│
├── objects/                           # 独立对象定义（从 01-schema.sql 拆分）
│   ├── tables/                        # 每个表一个文件（103 个）
│   ├── sequences/                     # 序列定义（57 + 56 个）
│   ├── functions/                     # 函数定义（18 个）
│   ├── views/                         # 视图定义（7 + 2 物化视图）
│   ├── triggers/                      # 触发器定义（14 个）
│   ├── indexes/                       # 索引定义（417 + 194 个）
│   ├── constraints/                   # 约束定义（127 个）
│   └── policies/                      # RLS 策略定义（15 + 15 个）
│
├── seed/                              # 初始数据（每个表一个文件）
│   └── ...                            # 从 02-seed.sql 拆分
│
├── migrations/                        # 数据库迁移
│   ├── startup/                       # Go 启动时应用的迁移（125 个）
│   │   ├── 001_users_table.sql        # 从 db/migrations/ 迁移
│   │   └── ...
│   └── domain/                        # 领域迁移（shell 脚本应用，21 个）
│       ├── 032_session_tenant_binding.sql
│       └── ...
│
├── scripts/                           # 部署 & 运维脚本
│   ├── init.sh                        # 初始化数据库（来自 deploy/sql/）
│   ├── verify.sh                      # 校验脚本
│   ├── dump-schema.sh                 # 从生产库重新导出 schema
│   ├── dump-seed.sh                   # 从生产库重新导出 seed
│   ├── phase2_db_setup.sql            # Phase 2 热度追踪诊断
│   ├── 03-local-mock-credential.sql   # 本地 mock credential
│   ├── 20260629_auto_control.sql      # Auto control 迁移
│   ├── phase-21-schema-sync/          # Phase 21 跨库 schema 同步
│   ├── phase-22-extension-and-role-sync/
│   ├── phase-23-columnar-invariant/
│   ├── verify-provider-model-join.sql # 临时诊断脚本
│   └── README.md                      # 原 deploy/sql/README.md
│
├── pricing/                           # 定价相关 SQL 操作
│   └── ...                            # 来自 docs/pricing/
│
└── tests/                             # 测试 SQL 文件
    └── partition_hot_table_tests.sql  # 分区热表测试
```

## 新旧路径映射

| 旧路径 | 新路径 |
|--------|--------|
| `deploy/sql/00-prereqs.sql` | `sql/schema/00-prereqs.sql` |
| `deploy/sql/01-schema.sql` | `sql/schema/01-schema.sql` |
| `deploy/sql/02-seed.sql` | `sql/schema/02-seed.sql` |
| `deploy/sql/init.sh` | `sql/scripts/init.sh` |
| `db/migrations/` | `sql/migrations/startup/` |
| `migrations/` | `sql/migrations/domain/` |
| `installer/sql/` | ❌ 废弃（使用 sql/schema/ 代替） |
| `scripts/verify-provider-model-join.sql` | `sql/scripts/` |
| `docs/pricing/*.sql` | `sql/pricing/` |
| `db/tests/partition_hot_table_tests.sql` | `sql/tests/` |

## 对象文件说明

`objects/` 目录下的文件由 `scripts/_lib/split-schema.py` 从 `01-schema.sql` 自动提取生成。
每个文件对应一个数据库对象（表、函数、视图、触发器、索引、约束、序列、RLS 策略），
包含该对象的完整 DDL 定义及其关联的 COMMENT。

**更新方式**：
1. 修改 `01-schema.sql`（通过 `pg_dump` 从生产库导出）
2. 重新运行 `scripts/_lib/split-schema.py` 同步对象文件
3. 或直接修改对象文件后，反向同步到 `01-schema.sql`

## 快速开始

### 初始化新数据库

```bash
DATABASE_URL='postgresql://user:pass@host:5432/db?ssl=false' \
  bash sql/scripts/init.sh
```

### 应用启动迁移

```bash
# 按文件名顺序应用
for f in sql/migrations/startup/[0-9]*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

### 应用领域迁移

```bash
for f in sql/migrations/domain/[0-9]*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

### 从生产库重新导出

```bash
DATABASE_URL='postgresql://...' bash sql/scripts/dump-schema.sh
DATABASE_URL='postgresql://...' bash sql/scripts/dump-seed.sh
# 然后运行 split-schema.py 同步对象文件
python3 scripts/_lib/split-schema.py sql/schema/01-schema.sql sql/objects/
```

## 维护约定

1. **对象文件幂等**：所有 `CREATE` / `ALTER` 使用 `IF NOT EXISTS`，可安全重复执行
2. **迁移文件不可变**：已应用的迁移文件禁止修改，新变更应创建新文件
3. **文件命名**：`<序号>_<描述>.sql`，序号用于控制执行顺序
4. **seed 数据**：`ON CONFLICT DO NOTHING`，可安全重复执行
