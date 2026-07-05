# deploy/sql/ — 数据库部署资产

> **用途**：存放部署相关的 SQL 资产，与 `sql/` 目录互补但不重复。
> **关系**：`sql/` 是开发时的 SSOT，`deploy/sql/` 是部署时的资产。

## 快速导航

- [目录结构](#目录结构)
- [与 sql/ 的分工](#与-sql-的分工)
- [使用指南](#使用指南)
- [部署流程](#部署流程)
- [维护约定](#维护约定)

---

## 目录结构

```
deploy/sql/
├── README.md                          # 本文档
├── DEPLOYMENT_PLAN.md                 # 数据库部署方案
├── STRUCTURE_PLAN.md                  # 目录结构设计文档
├── MIGRATION_SUMMARY.md               # 迁移总结
├── migrate-sql-files.sh               # SQL文件迁移脚本
├── sync-objects.sh                    # 对象同步脚本
├── verify-migration.sh                # 验证脚本
│
├── schemas/                           # Schema快照
│   ├── baseline/                      # 基线schema（installer专用）
│   │   ├── 00-prereqs.sql             # PostgreSQL扩展（与sql/schema/同步）
│   │   ├── 01-schema.sql              # 完整DDL（与sql/schema/同步）
│   │   └── 02-seed.sql                # 系统初始数据（与sql/schema/同步）
│   └── snapshots/                     # 各版本快照
│       └── r9XX-YYYYMMDD.sql          # 版本快照（待生成）
│
├── objects/                           # 数据库对象（按类型分类，839个文件）
│   ├── tables/                        # 表定义（103个）
│   ├── views/                         # 视图定义（9个）
│   ├── functions/                     # 函数定义（18个）
│   ├── sequences/                     # 序列定义（113个）
│   ├── triggers/                      # 触发器定义（14个）
│   ├── indexes/                       # 索引定义（425个）
│   ├── constraints/                   # 约束定义（127个）
│   └── policies/                      # RLS策略定义（30个）
│
├── migrations/                        # 部署时的数据迁移
│   └── README.md                      # 迁移说明（待生成）
│
├── scripts/                           # 部署运维脚本
│   ├── init-database.sh               # 数据库初始化（待创建）
│   ├── apply-migrations.sh            # 应用迁移（待创建）
│   └── verify-schema.sh               # Schema验证（待创建）
│
├── cron/                              # 定时任务SQL
│   ├── README.md                      # 定时任务说明（待生成）
│   └── 2026_06_12_pricing_refresh_log.sql  # 定价刷新日志
│
├── tests/                             # 部署验证测试
│   ├── README.md                      # 测试说明（待生成）
│   └── 038_adaptive_probe_test.sql    # 自适应探针测试
│
├── docs/                              # 文档化的SQL示例
│   ├── features/                      # 功能相关SQL
│   │   ├── 2026-06-14-peak-stats.sql
│   │   ├── 2026-06-15-auto-route-mode.sql
│   │   ├── 2026-06-15-auto-route-mode.down.sql
│   │   ├── 2026-06-15-auto-route-mode-cost-table.sql
│   │   ├── 2026-06-15-auto-route-mode-cost-table.down.sql
│   │   ├── 2026-06-15-auto-route-mode-realtime-trigger.sql
│   │   ├── 2026-06-15-auto-route-mode-realtime-trigger.down.sql
│   │   ├── 2026-06-15-auto-route-mode-realtime-trigger-fix.sql
│   │   ├── 2026-06-22-explicit-model-stats.sql
│   │   └── 2026-06-22-explicit-model-stats.down.sql
│   ├── pricing/                       # 定价相关SQL
│   │   └── xiaomi_tokenplan_tier2.sql
│   └── experiments/                   # 实验性SQL
│       └── archived/                  # 已废弃的实验
│
└── templates/                         # SQL模板（待创建）
    ├── migration_template.sql
    ├── rollback_template.sql
    └── cronjob_template.sql
```

---

## 与 sql/ 的分工

| 目录 | 用途 | 内容 | 维护者 |
|------|------|------|--------|
| **sql/** | 开发时的SSOT | objects/, migrations/startup, migrations/domain, schema/ | 开发人员 |
| **deploy/sql/** | 部署时的资产 | schemas/baseline, objects/, cron/, tests/, docs/, 部署脚本 | 运维/DevOps |

### 关键原则

1. **schemas/baseline/** 与 **sql/schema/** 内容完全相同
   - `schemas/baseline/` 是 installer 嵌入使用的副本
   - 通过 `migrate-sql-files.sh` 保持同步
   
2. **objects/** 与 **sql/objects/** 内容完全相同
   - `deploy/sql/objects/` 是数据库对象的部署副本
   - 通过 `sync-objects.sh` 从 `sql/objects/` 自动同步
   - 按对象类型分类：tables, views, functions, sequences, triggers, indexes, constraints, policies
   - 总计 839 个对象文件
   
3. **不重复 sql/migrations/**
   - `sql/migrations/startup/` 由 Go 代码自动应用
   - `sql/migrations/domain/` 由部署脚本应用
   - `deploy/sql/` 不再维护独立的迁移文件

4. **docs/** 用于文档化功能演进
   - 每个功能的 SQL 变更历史
   - 包含正向迁移 (.sql) 和回滚 (.down.sql)

---

## 使用指南

### 1. 初始化新数据库

使用 baseline schema 初始化：

```bash
# 方式1: 使用 installer（推荐）
cd installer/cmd/llm-gw-installer
go run main.go --db-url="postgresql://user:pass@host:5432/dbname?sslmode=disable"

# 方式2: 手动应用
export DATABASE_URL="postgresql://user:pass@host:5432/dbname?sslmode=disable"
psql "$DATABASE_URL" -f deploy/sql/schemas/baseline/00-prereqs.sql
psql "$DATABASE_URL" -f deploy/sql/schemas/baseline/01-schema.sql
psql "$DATABASE_URL" -f deploy/sql/schemas/baseline/02-seed.sql
```

### 2. 应用迁移

```bash
# 应用 startup 迁移（通常由 Go 应用自动执行）
for f in sql/migrations/startup/[0-9]*.sql; do
  psql "$DATABASE_URL" -f "$f"
done

# 应用 domain 迁移（部署时手动执行）
for f in sql/migrations/domain/[0-9]*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

### 3. 定时任务部署

定时任务通过 K8s CronJob 调度：

```bash
# 部署定时任务
kubectl apply -f deploy/k8s/cron/pricing-refresh-cronjob.yaml
```

### 4. 验证测试

```bash
# 运行部署验证测试
psql "$DATABASE_URL" -f deploy/sql/tests/038_adaptive_probe_test.sql
```

### 5. 同步数据库对象

当 `sql/objects/` 更新后，运行同步脚本：

```bash
cd deploy/sql
bash sync-objects.sh
```

这会同步 839 个数据库对象文件到 `deploy/sql/objects/`，按类型分类：
- tables: 103 个
- views: 9 个
- functions: 18 个
- sequences: 113 个
- triggers: 14 个
- indexes: 425 个
- constraints: 127 个
- policies: 30 个

---

## 部署流程

```
┌─────────────────┐
│   开始部署      │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ 1. 备份数据库   │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ 2. 验证schema版本│
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ 3. 应用migrations│
│   (sql/migrations)│
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ 4. 运行测试     │
│  (deploy/sql/tests)│
└────────┬────────┘
         │
    ┌────▼────┐
    │测试通过？│
    └─┬─────┬─┘
      │     │
    是│     │否
      │     │
      ▼     ▼
   ┌────┐ ┌────────┐
   │完成│ │回滚部署│
   └────┘ └────────┘
```

详见 [DEPLOYMENT_PLAN.md](./DEPLOYMENT_PLAN.md)

---

## 维护约定

### 1. 文件命名规范

| 类型 | 命名格式 | 示例 |
|------|----------|------|
| 迁移文件 | `<序号>_<描述>.sql` | `001_baseline_schema.sql` |
| 功能SQL | `<日期>-<功能名>.sql` | `2026-06-15-auto-route-mode.sql` |
| 回滚文件 | `<对应文件>.down.sql` | `auto-route-mode.down.sql` |
| 定时任务 | `<日期>_<任务名>.sql` | `2026_06_12_pricing_refresh_log.sql` |
| 测试文件 | `<序号>_<测试对象>_test.sql` | `038_adaptive_probe_test.sql` |

### 2. SQL文件头部注释

每个 SQL 文件必须包含：

```sql
-- ============================================
-- 文件名: xxx.sql
-- 用途: [简要说明]
-- 依赖: [依赖的表/函数/扩展]
-- 环境: [dev/test/prod/all]
-- 作者: [姓名]
-- 日期: [YYYY-MM-DD]
-- ============================================
```

### 3. 幂等性要求

所有 SQL 必须可重复执行：

```sql
-- ✓ 正确：使用 IF NOT EXISTS
CREATE TABLE IF NOT EXISTS my_table (...);

-- ✓ 正确：使用 ON CONFLICT
INSERT INTO my_table (id, name) VALUES (1, 'test')
ON CONFLICT (id) DO NOTHING;

-- ✗ 错误：直接 CREATE
CREATE TABLE my_table (...);
```

### 4. 版本控制

- 所有 SQL 文件纳入 Git 版本控制
- 禁止修改已应用的迁移文件
- 新变更必须创建新的迁移文件

### 5. 同步脚本

**同步 schemas/baseline/**

当 `sql/schema/` 更新后，运行迁移脚本同步：

```bash
cd deploy/sql
bash migrate-sql-files.sh
```

**同步 objects/**

当 `sql/objects/` 更新后，运行同步脚本：

```bash
cd deploy/sql
bash sync-objects.sh
```

---

## 相关文档

- [sql/README.md](../../sql/README.md) - 开发时的 SQL 资产 SSOT
- [DEPLOYMENT_PLAN.md](./DEPLOYMENT_PLAN.md) - 数据库部署方案
- [STRUCTURE_PLAN.md](./STRUCTURE_PLAN.md) - 目录结构设计文档
- [deploy/DEPLOYMENT_GUIDE.md](../DEPLOYMENT_GUIDE.md) - 完整部署指南

---

## 常见问题

### Q1: schemas/baseline/ 和 sql/schema/ 有什么区别？

**A**: 内容完全相同，但用途不同：
- `sql/schema/` 是开发时的 SSOT，由 `pg_dump` 从生产库导出
- `schemas/baseline/` 是 installer 嵌入使用的副本，供新环境初始化

### Q2: 为什么不把 sql/migrations/ 也复制到 deploy/sql/？

**A**: 避免重复和不一致：
- `sql/migrations/startup/` 由 Go 代码自动应用，无需人工干预
- `sql/migrations/domain/` 由部署脚本直接引用，无需复制
- 单一数据源，避免维护两份相同内容

### Q3: docs/features/ 中的 SQL 是做什么用的？

**A**: 文档化功能演进历史：
- 记录每个功能的数据库变更
- 提供回滚脚本 (.down.sql)
- 供开发人员参考和学习
- **不作为部署使用**，实际迁移在 `sql/migrations/`

### Q4: objects/ 和 sql/objects/ 有什么区别？

**A**: 内容完全相同，但用途不同：
- `sql/objects/` 是开发时的 SSOT（839个对象文件）
- `deploy/sql/objects/` 是部署时的副本，通过 `sync-objects.sh` 自动同步
- 按对象类型分类：tables, views, functions, sequences, triggers, indexes, constraints, policies

### Q5: 如何判断应该放在 sql/ 还是 deploy/sql/？

**A**: 遵循以下原则：
- **开发产出** → `sql/` (objects, migrations, schema)
- **部署资产** → `deploy/sql/` (baseline, cron, tests, docs)
- **运维脚本** → `deploy/sql/scripts/`

---

**最后更新**: 2026-07-06  
**维护者**: DevOps Team
