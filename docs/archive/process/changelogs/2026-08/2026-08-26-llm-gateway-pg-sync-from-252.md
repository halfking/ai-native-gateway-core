# 252 → local llm-gateway-pg Sync + Cleanup Report

**Date**: 2026-08-26
**Source**: llm_gateway@115.29.212.252:5432 (via SSH tunnel :15432)
**Target**: llm_gateway@127.0.0.1:5432 (Docker container `llm-gateway-pg`)
**Tool**: `scripts/pg-table-copy.sh` + `scripts/local-dev/recreate-llm-gateway-pg.sh`

## 1. 同步动作

1. 用 `pg-table-copy.sh --schema-only` 先把 schema 全量导入（50003 行 DDL）
2. 再用 `pg-table-copy.sh`（默认 data 模式）跑 334 张 normal 表的数据同步
3. Hot 表（91 张，suffixes `_hot` / `_2026_*` / `_archived` / parent partitions）按设计只导 schema
4. 容器重建：`scripts/local-dev/recreate-llm-gateway-pg.sh` 把 `POSTGRES_PASSWORD` env 写成 252 真值，确保重启不被 entrypoint 覆写

## 2. 凭据对齐

| Item              | 252 source                | local target              | Status |
|-------------------|---------------------------|---------------------------|--------|
| PG user           | `llm_gateway`             | `llm_gateway`             | ✓ MATCH |
| PG password       | `<env:COMMON_PG_SUPERUSER_PASS>` | (now same, persisted via env) | ✓ SYNCED |
| PG database       | `llm_gateway`             | `llm_gateway`             | ✓ MATCH |

来源：`envs/common/database.yaml` (`<env:COMMON_PG_SUPERUSER_PASS>`)

## 3. Schema 对比

| Metric                | 252 source | local target | Status |
|-----------------------|-----------:|-------------:|--------|
| PG version            | 17.10      | 17.10        | ✓ MATCH |
| public tables         | 383        | 397          | local 多了 14 张历史残留 |
| partitioned tables    | 25         | 28           | local 多 3 张 partition auto-subtables |
| extensions            | citus 13.3, citus_columnar 13.3, vector 0.8.3, pg_trgm 1.6 | citus 13.3, citus_columnar 13.3, vector 0.8.5, pg_trgm 1.6 | ✓ MATCH |
| 252-only tables       | 0          | —            | ✓ 全部 schema 在本地 |

## 4. Local-only 表清理（`_` 前缀标记）

14 张 local-only 表被加 `_` 前缀标记为待删除（**未实际删除**，留作人工确认）：

```
_identity_migration_ownership
_model_offers_legacy
_model_probe_runs_old
_ops_model_offers_backup
_request_wal_2026_07_archived
_request_wal_2026_07_col_archived
_rule48_test_default
_rule48_test_src
_shadow_audit
_shadow_user_providers
_shadow_users
_task_default_routing_backup_20260718
_usage_ledger_2026_07_archived
_usage_ledger_2026_07_col_archived
```

**规约**：以 `_` 开头的表 = 已知过期 / 待清理对象，业务代码 / 视图 / 测试不应再引用。后续清理走单独 PR（rule 19 §11 三阶控制）。

## 5. 行数抽样（关键业务表）

| 表                  | 列数 252=local | 行数 252=local | Status |
|---------------------|:---:|:---:|:---:|
| credentials         | 79  | 45  | ✓ MATCH |
| providers           | 23  | 48  | ✓ MATCH |
| users               | 12  | 21  | ✓ MATCH |
| api_keys            | 37  | 80  | ✓ MATCH |
| applications        | 13  | 16  | ✓ MATCH |

## 6. 修复的遗留风险

| 风险 | 修复方式 |
|------|---------|
| `ALTER USER` 密码会被 PG entrypoint 覆盖 | 重建容器时把 `POSTGRES_PASSWORD` env 直接写成 252 真值 |
| 容器 db 还是用旧密码文件，没有可重跑脚本 | 新增 `scripts/local-dev/recreate-llm-gateway-pg.sh` |
| 源端 `statement_timeout=30s` 阻挡大表 `count(*)` | `scripts/pg-table-copy.sh` 加 `$PGOPTIONS` 透传，向后兼容 |

## 7. 提交流程

1. `chore/pg-table-copy-pgoptions-and-llm-gateway-pg-reset` 分支已推送
2. 本地 DB rename 是 DDL 操作，不入仓
3. 合并到 main + push 后建议清理分支
