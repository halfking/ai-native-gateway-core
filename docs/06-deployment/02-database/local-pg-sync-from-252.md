# Local PostgreSQL (llm-gateway-pg) — 从 252 同步流程

**版本**: 1.0
**日期**: 2026-08-27
**状态**: 强制执行
**作用范围**: 本地开发用的 docker PG 容器 (`llm-gateway-pg`)

---

## 一、环境概览

| 环境       | 标识       | 地址                  | 用途                          |
|------------|------------|-----------------------|-------------------------------|
| **本地开发**  | `local`    | `127.0.0.1:5432`（Docker 容器 `llm-gateway-pg`） | 本地开发、smoke、debug  |
| **252 测试**  | `test`     | `<env:HOST_252>`（经受控 SSH 隧道访问）         | 集成测试、staging 验证 |
| **RDS 生产**  | `prod`     | RDS 实例（内部）                              | 真实生产流量             |

> 当前唯一同步方向是：**252 → local**（单向）。**禁止从 RDS 同步到任何环境**。

---

## 二、凭据对齐

**单一来源**: `~/workspace/ai-native-tools/envs/common/database.yaml`（`COMMON_PG_SUPERUSER_PASS`）

所有环境的 `llm_gateway` 用户必须使用**同一个密码**。不一致会导致：

1. 应用层 `LLM_GATEWAY_DATABASE_URL` 解析失败 → 网关起不来
2. `ALTER USER` 在 PG entrypoint 启动时被 `POSTGRES_PASSWORD` env 覆盖 → 改动不持久

### 2.1 验证当前密码是否一致

```bash
# 1. 加载 env
source ~/workspace/ai-native-tools/envs/loader.sh \
  --project llm-gateway-go
export PG_PASS_252="$COMMON_PG_SUPERUSER_PASS"
export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
source configs/env-252.sh

# 2. 252 侧。隧道目标从 configs/env-252.sh 获取，不能硬编码。
ssh -f -N -L "$TUNNEL_LOCAL_PORT:$TUNNEL_REMOTE_TARGET" 252
PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" \
  psql -h localhost -p 15432 -U llm_gateway -d llm_gateway -c 'SELECT 1'
TUNNEL_PID=$(lsof -tiTCP:15432 -sTCP:LISTEN)
[[ -n "$TUNNEL_PID" ]] && kill "$TUNNEL_PID"

# local 侧
docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg psql -U llm_gateway -d llm_gateway -c 'SELECT 1'
```

### 2.2 凭据不一致时 → 重建容器

```bash
# scripts/local-dev/recreate-llm-gateway-pg.sh
# - 自动从 envs loader 拿密码
# - 保留现有 data dir (/Users/xutaohuang/data/docker/llm-gateway-pg17/data)
# - 重启容器时 POSTGRES_PASSWORD env 直接写入密码 → 不被 PG entrypoint 覆写
bash scripts/local-dev/recreate-llm-gateway-pg.sh
```

---

## 三、同步流程（252 → local）

### 3.1 同步脚本

```bash
scripts/pg-table-copy.sh \
  --source configs/env-252.sh \
  --target configs/env-local.sh
```

行为：

- 默认导出 252 全 schema + 全数据（normal 表）
- 自动识别并跳过 91 张 hot 表（模式: `*_hot`, `*_2026_*`, `*_archive*`, parent partitions）
- 仅导 schema，跳过 data（hot 表数据在 252 实时生产中，不拷贝）
- 会用 `--clean --if-exists` 重建 local 对象；运行前先确认本地数据可覆盖

### 3.2 大表 `count(*)` 超时（踩坑点 #1）

**症状**:

```
ERROR:  canceling statement due to statement timeout
LINE 1: SELECT count(*) FROM <huge_table>
```

**原因**: 252 服务端 `statement_timeout = 30s` 硬限；大表全表 count 超时。

**解决**: 用 `PGOPTIONS` 透传（已在 `pg-table-copy.sh` 内置支持）：

```bash
# 任何 source 端 psql 调用都会读到这条环境变量
export PGOPTIONS='-c statement_timeout=0'
bash scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-local.sh
```

### 3.3 容器密码漂移（踩坑点 #2）

**症状**: 本地刚改完密码 → 重启容器 → 密码回到旧值。

**原因**: PG `docker-entrypoint.sh` 在每次启动时都会拿 `POSTGRES_PASSWORD` env 重新 `ALTER USER`。在容器内 `ALTER USER` 不持久。

**解决**: 见 §二.2.2，用 `recreate-llm-gateway-pg.sh` 重建容器。

### 3.4 同步后行数漂移

**预期行为**: 同步后立即跑行数对比，会看到 sub-percent 漂移（生产端实时写入导致）。

```bash
# 抽样对比
docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg psql -U llm_gateway -d llm_gateway -tAc \
  "SELECT count(*) FROM credentials"
# 252 端相同命令应得到几乎一致的结果（差 1-2% 视为正常）
```

---

## 四、Local-only 表规约：以 `_` 开头的表

### 4.1 规约

| 标志        | 含义                                                                  |
|-------------|-----------------------------------------------------------------------|
| `_` 前缀     | **待清理对象** — 已知过期 / 历史残留 / 同步副作用。下一步会删除。业务代码 / 视图 / 测试 **不应再引用**。  |

> 类比：`*.bak` / `*_deprecated` / `# TODO REMOVE` 标记。

### 4.2 当前实例（2026-08-26 同步后）

下面 14 张表是从历史同步残留里识别出来的，已统一加 `_` 前缀（**不删数据**，留给人工审计）：

```
_identity_migration_ownership
_model_offers_legacy
_model_probe_runs_old
_ops_model_offers_backup
_request_wal_2026_07_archived
_request_wal_2026_07_col_archived
_rule48_test_default
_rule48_test_view_src
_shadow_audit
_shadow_user_providers
_shadow_users
_task_default_routing_backup_20260718
_usage_ledger_2026_07_archived
_usage_ledger_2026_07_col_archived
```

### 4.3 清理流程

按 rule 19 §11 三阶控制走：

1. **阶段一** — 影响分析：grep 业务代码、views、外部工具硬编码引用
2. **阶段二** — 二次审计：列出删除列表，找最近修改人确认
3. **阶段三** — 人工确认 + 备份后删除（建议先做 schema dump 备份）

不要直接在生产连删。

---

## 五、常见问题

### Q1: 同步失败了，表里有数据但 schema 不对？

**答**: 脚本默认 `--clean --if-exists`，会先 drop 目标对象再导入。失败时回滚到原状（脚本自带），但建议跑前手动 dump 一份当前 local schema 备份。

### Q2: 同步后容器里某些视图查询失败？

**答**: 不要用 `pg_reload_conf()` 处理 schema 依赖。先重跑 schema 同步，再检查依赖对象和显式列清单；分区表还要执行 rule 49 要求的 view rebuild 校验。

### Q3: 业务 smoke 失败，错误提到 `_xxx` 表？

**答**: 业务代码不应该引用 `_` 前缀表。如果发现，说明：

1. 业务硬编码了历史表名（需要修代码）
2. 或者 view/migration 没正确重建（需要重跑 schema import）

按 rule 19 §11 走清理流程，而不是回滚 rename。

### Q4: 重建容器后 Navicat 连不上？

**答**: Navicat 走 TCP，端口 5432 仍然开放。使用 `llm_gateway` 以及 envs loader 提供的当前凭据重新登录；不要把密码复制到文档或连接备注。

### Q5: 把本地 feature 表 DDL 灌到 252 时报 `columnar_insert_only_parents() does not exist`？

**答**: `pg_dump --schema-only` 会在导出 SQL 里写 `SELECT pg_catalog.set_config('search_path', '', false)`（安全考虑）。252 上有事件触发器 `enforce_columnar_trigger`，每当执行表 DDL 就会触发，并调用未加 schema 限定的函数 `columnar_insert_only_parents()`；`search_path=''` 下找不到该函数，于是**每张 CREATE/DROP TABLE 都失败**。

修复：导出后先剔除这一行再执行（dump 里所有对象都已用 `public.` 显式限定，剔除无副作用）：

```bash
grep -vE "set_config\('search_path', '', false\)" feature.sql > feature.fixed.sql
PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -h localhost -p 15432 -U llm_gateway -d llm_gateway \
  -v ON_ERROR_STOP=1 -f feature.fixed.sql
```

另外注意：`pg_dump -t` 每张表要单独写一个 `-t`（`-t "a b c"` 会被当成单个表名而报 "too many command-line arguments"）；用 bash 数组 `"${args[@]}"` 传给 `docker exec ... pg_dump` 在 zsh 下也会塌缩，建议循环逐表 dump 再追加到文件。月度分区（如 `request_logs_bodies_2026_10`）命中 hot 表过滤，仅 252 有、本地无，属**预期差异**，不必回灌。

---

## 六、相关脚本与文档

| 文件 | 作用 |
|------|------|
| `scripts/pg-table-copy.sh` | 252 → local 同步入口（含 hot 表过滤、PGOPTIONS 透传；导入前剔除 search_path 守卫、导入日志强制查错，失败即 exit 1） |
| `scripts/local-dev/recreate-llm-gateway-pg.sh` | 用 envs 252 密码重建 docker 容器 |
| `scripts/local-dev/verify-db-consistency.sh` | 252 ↔ local 六维结构校验（表/列/视图/索引/约束/序列）；含 gated `--reconcile` 回灌模式。**每次同步后必须执行**——迁移跟踪表随数据复制，不能反映真实结构 |
| `docs/audit/2026-08-31-db-structure-consistency-audit.md` | 2026-08-31 结构一致性审计报告（P0 脚本静默失败根因 + 修复清单）|
| `configs/env-252.sh` | 252 端连接配置 |
| `configs/env-local.sh` | local 端连接配置 |
| `docs/archive/process/changelogs/2026-08/2026-08-26-llm-gateway-pg-sync-from-252.md` | 同步执行 + 验证报告 |

---

## 七、变更记录

| 日期       | 版本 | 说明 |
|------------|------|------|
| 2026-08-27 | 1.0  | 初版（从 252 → local 完整同步流程 + `_` 前缀表规约 + recreate 脚本说明）|
| 2026-08-31 | 1.1  | 新增 `verify-db-consistency.sh`（一致性校验 + 252←local feature 表回灌）；补充 `pg_dump` search_path 触发 columnar 事件触发器、`-t` 逐表、月度分区预期差异等踩坑点 |
| 2026-08-31 | 1.2  | 结构审计修复：`pg-table-copy.sh` 导入前剔除 search_path 守卫 + 强制扫描导入日志错误（修复 08-26 同步静默失败根因，见 `docs/audit/2026-08-31-db-structure-consistency-audit.md`）；`verify-db-consistency.sh` 升级 v1.1 六维校验；local 补 573/610/364 欠账，252 补 364 §4/§5 |
