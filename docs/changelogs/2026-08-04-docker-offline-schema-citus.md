# 2026-08-04 — Docker 离线包: schema 完整还原 + citus 镜像

## 目标

交付**完全离线**的 Docker 部署包（PostgreSQL 17 citus+vector+columnar + Redis 7 + LLM Gateway），仅 arm64，一键部署 + L1-L4 验证，初始数据仅 admin/admin。

## 背景与问题

- 原离线包用 `postgres:17-alpine`，**无 citus / vector / columnar 扩展**，无法应用 00-prereqs。
- 01-schema.sql 是 pg_dump 按 OID 排序导出，**破坏依赖序**：SQL 函数前向引用、COMMENT 前向引用、columnar 分区残留 gin 索引，导致无法完整重建。
- 网关的 `ApplyMigrations` 只做增量 ALTER，**不能建基础 271 表**——必须由 initdb 先建全 schema。

## 修改清单

| 文件 | 变更 |
|------|------|
| `deploy/sql/schemas/baseline/01-schema.sql` | 修复 8 个前向引用函数重排 + 移除 columnar 分区全部残留索引（139 块 + 3 pkey） |
| `deploy/sql/schemas/baseline/00-prereqs.sql` | citus / citus_columnar 的 `WITH SCHEMA` 改为 `pg_catalog` |
| `dist/docker-offline/v2.4.9/staging/docker-compose.yml` | DB→`kx-citus-pg17:offline-arm64`、Redis→`kx-redis:offline-arm64`、新增 `schemas/` initdb 挂载 |
| `dist/docker-offline/v2.4.9/staging/install.sh` | REQUIRED_IMAGES 同步新镜像名 |
| `dist/docker-offline/v2.4.9/staging/README.md` | 镜像名 + schema 初始化说明 |
| `dist/docker-offline/v2.4.9/staging/schemas/{00,01,02}*.sql` | 拷入基线 schema + seed |

## 关键修复

### 1. SQL 函数前向引用（8 个）

pg_dump 按 OID 排序，函数定义出现在其依赖的 COMMENT / POLICY / VIEW 之后。修复策略：

- `get_current_tenant()` → 移入 `request_logs` 表之后、RLS policy 之前（被 CREATE POLICY 引用）
- `model_probe_credential_concurrency(bigint)` → 插入到引用它的 VIEW `v_suspicious_probe_targets` 之前
- 其余 5 个 + `system_health_status()` → 文件末尾（依赖序：columnar_insert_only_parents → columnar_healthcheck → columnar_drift_report）

### 2. columnar 分区 gin 索引

```
ERROR: unsupported access method for the index on columnar table request_logs_2026_07
```

- columnar 表**支持 btree/hash，不支持 gin**。
- `request_logs_2026_07/08` 的 `quality_flags_idx`、`tool_calls_idx` 是转列存前的遗留索引。
- 处理：移除 columnar 分区**全部**残留索引（CREATE INDEX + ATTACH PARTITION 共 139 块）+ 3 个 pkey 约束（`request_logs_2026_07_pkey`、`request_logs_bodies_2026_07/08_pkey`），与生产 252 转列存前状态对齐。

### 3. 干净容器全量验证

```
00-prereqs:  exit=0
01-schema:   exit=0
271 BASE TABLE / 55 views / 491 functions
request_logs_2026_07/08: relam=columnar，无索引
```

## 离线包端到端验证

- 消费者视角：解压 tar.gz → `bash install.sh --port 18781 --reset`（删卷全新初始化）
- L1 `/healthz` 200 → L2 PG/Redis → L3 `/v1/models` 200 → L4 admin/admin 登录成功
- 重启幂等：`down`（保留卷）→ `up` → 仍 271 表（initdb 不重跑）
- 02-seed 仅含配置字典（providers / model_aliases / pricing_plans 等），**无 users / credentials / 业务数据**
- admin 由网关 `EnsureSeedAdmin` 自动创建（`LLM_GATEWAY_SEED_ADMIN_PASSWORD=admin`）

## 已知说明

- 启动日志有 `request_logs_2026_09 would overlap request_logs_2026_08 (42P17)`：生产 dump 的 08 分区边界为 `08:00` 起点（人工/历史异常），网关 partition_manager 建 09 时重叠。**一次性无害**（仅启动 + 每 24h 各报 1 次，不阻断），与生产 252 状态一致，保留忠实还原。
- `citus_columnar` 扩展在生产单独存在，本镜像 citus 13 已内建 columnar access method，功能等价。
- restricted mode / fernet key 警告为测试环境未配置导致，不影响核心功能。

## 产物

- `dist/docker-offline/v2.4.9/llm-gateway-go-docker-arm64-v2.4.9-offline.tar.gz`（337MB，gitignore）
- `dist/docker-offline/v2.4.9/SHA256SUMS`

## 上传

待确认 files.kxpms.cn（cloudreve）上传方式后执行。
