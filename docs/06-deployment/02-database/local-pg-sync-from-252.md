# Local PostgreSQL (llm-gateway-pg) — 从 252 同步流程

**版本**: 1.11
**日期**: 2026-09-01
**状态**: 强制执行
**作用范围**: 本地开发用的 docker PG 容器 (`llm-gateway-pg`)

---

## 一、环境概览

| 环境       | 标识       | 地址                  | 用途                          |
|------------|------------|-----------------------|-------------------------------|
| **本地开发**  | `local`    | `127.0.0.1:5432`（Docker 容器 `llm-gateway-pg`，容器内 5432 直映宿主机 5432） | 本地开发、smoke、debug  |
| **252 测试**  | `test`     | `<env:HOST_252>`（经受控 SSH 隧道访问）         | 集成测试、staging 验证 |
| **RDS 生产**  | `prod`     | RDS 实例（内部）                              | 真实生产流量             |

> **端口布局（2026-08-31 起的稳定状态）**:
> - `127.0.0.1:5432` = **本集群**（`llm-gateway-pg` 容器）。此前的占用者 Homebrew 原生 `postgresql@17`（及其遗留的 `postgresql@15` 数据目录）已于 2026-08-31 移除，移除前已做全量备份（`~/backups/homebrew-pg17-5432-final-20260831.sql`、`~/backups/homebrew-pg15-datadir-orphan-20260831.tar.gz`）。
> - `127.0.0.1:15432` = **252 SSH 隧道专用**（`configs/env-252.sh` 的 `TUNNEL_LOCAL_PORT`）。容器不再映射该端口；隧道由 `scripts/lib/252-db-tunnel.sh` 以**所有权语义**管理：只复用健康 listener、拒绝顶替 15432 上的未知进程、清理时只 `kill` 自己创建并记录的 PID（`db252_tunnel_teardown`）。调用方不得自行按端口扫描后 kill listener，也不得手写 `ssh -L` 隧道。
> - 连接本地建议显式 `-h 127.0.0.1`，避免 `localhost` 解析歧义。

> 当前唯一同步方向是：**252 → local**（单向）。**禁止从 RDS 同步到任何环境**。

---

## 二、凭据对齐

**单一来源**: 通过 `~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go` 加载的 `COMMON_PG_SUPERUSER_PASS`。`configs/env-252.sh` 仅在内存中将它映射为兼容变量；不要保存第二份 252/local 密码或在脚本中写入密码。

所有环境的 `llm_gateway` 用户必须使用**同一个密码**。不一致会导致应用层 `LLM_GATEWAY_DATABASE_URL` 解析失败 → 网关起不来。

> **策略（2026-08-31 起强制）— 用户/密码只可新增，禁止自动化修改**
> 任何脚本、技能、文档流程都**不允许自动修改已有用户或密码**，包括但不限于：
> `ALTER ROLE/USER ... PASSWORD`、DROP 后重建同名用户、借容器 `POSTGRES_PASSWORD` env 重置已有集群密码。
> 自动化只允许**新增**（角色不存在时 `CREATE ROLE`，幂等守卫 `IF NOT EXISTS`）。
> 密码变更属于**人工操作**：人工执行 `ALTER ROLE` 后，必须同步更新 envs SSOT（`envs/common/database.yaml`）。

### 2.1 验证当前密码是否一致

```bash
# 1. 加载运行时 env。SSH 认证由 ~/.ssh/config 的 252 host alias 和证书/密钥处理，
#    不设置、不记录 SSH 密码或 SSH_PASS_* fallback。
source ~/workspace/ai-native-tools/envs/loader.sh \
  --project llm-gateway-go
source configs/env-252.sh

# 2. Shared helper resolves pg-252-pg17's current Podman IP only at invocation
#    time. It reuses only a healthy listener, refuses to replace an unknown
#    process on 15432, and tears down only the listener it created.
source scripts/lib/252-db-tunnel.sh
trap db252_tunnel_teardown EXIT HUP INT TERM
db252_tunnel_ensure

# 3. Host `psql` may be a shim into llm-gateway-pg and therefore cannot see the
#    host tunnel. Use the native libpq clients selected by configs/env-252.sh.
PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" \
  -h 127.0.0.1 -p "$TUNNEL_LOCAL_PORT" -U "$PG_USER" -d "$PG_DB" -c 'SELECT 1'
"$PG_DUMP_BIN" --version

# local 侧
docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg psql -U llm_gateway -d llm_gateway -c 'SELECT 1'
```

### 2.2 凭据不一致时 → 人工修正（禁止脚本自动改密）

**机制更正（2026-08-31 实测）**: 本镜像（`kx-citus-pg17`）的 entrypoint 只在**首次 initdb**（数据目录为空）时通过 `--pwfile` 写入 `POSTGRES_PASSWORD`；已初始化的数据目录在后续启动时**不会改写任何用户密码**。因此集群内 `ALTER ROLE ... PASSWORD` 是持久的。历史文档声称"entrypoint 每次启动会用 env 重新 ALTER USER"为错误结论——实际观察到的"密码被重置"是数据目录被清空重建（触发首次初始化）或换了另一个数据目录所致。

人工修正流程（**不要写进任何脚本/自动化**）：

```bash
# 1. 确认目标密码（envs SSOT）
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go

# 2. 人工执行改密，随后同步 envs/common/database.yaml
docker exec -it llm-gateway-pg psql -U <超级用户> -d postgres \
  -c "ALTER ROLE llm_gateway PASSWORD '<新密码>';"
```

`scripts/local-dev/recreate-llm-gateway-pg.sh` 的 `POSTGRES_PASSWORD` env 只对**全新数据目录的首次初始化**生效，不会修改已存在集群的任何用户（见脚本内策略守卫）。

> **本机连接角色（2026-09-01 勘误）**：`configs/env-local.sh` 的 `PG_USER` 现为 `llm_gateway`（即容器内唯一的登录角色）。早期文件曾标注 `(kxuser)` 与 `local_kxuser_pw`，那是历史残留 —— 实际 `kxuser` 角色从未在 `kx-citus-pg17` 镜像初始化时被创建。若之后再次出现 `FATAL: role "kxuser" does not exist`，请直接 grep `kxuser` 排除引用，不要回退 commit `3896f727d`。

---

## 三、同步流程（252 → local）

### 3.1 同步入口

```bash
# 推荐：一键包装入口（pre-sync 备份 → 自动建/拆隧道 → 同步 → 强制双审计）
bash scripts/local-host-sync-db.sh                 # full (schema + cold data)
bash scripts/local-host-sync-db.sh --schema-only   # 仅 schema
bash scripts/local-host-sync-db.sh --verify        # 仅跑双审计

# 直接调用底层引擎（不管理隧道；需自行按 §2.1 建隧道、跑 §3.5 双审计）
scripts/pg-table-copy.sh \
  --source configs/env-252.sh \
  --target configs/env-local.sh
```

行为：

- 默认导出 252 全 schema + 全数据（normal 表）
- 自动识别并跳过 hot 表：catalog 判定（分区父表 `relkind='p'`、任何子分区 `relispartition`，覆盖全部年月分区含 2025）+ 名称模式 `*_hot`、`*_2026_*`/`*_2027_*`/`*_2028_*`、`*_archived`/`*_archive`（后缀匹配；可用 `--hot-patterns` 覆盖默认集合）
- 仅导 schema，跳过 data（hot 表数据在 252 实时生产中，不拷贝）
- 默认不 DROP 或清空目标；精确数据替换必须显式使用 `--replace-data`，schema 重建必须显式使用 `--clean-schema`，并在运行前确认本地数据可覆盖
- `pg-table-copy.sh` 本身不管理隧道：直接调用前必须按 §2.1 建立隧道（调用时动态解析 `pg-252-pg17` 的 Podman IP，结束用 `db252_tunnel_teardown` 清理）；`local-host-sync-db.sh` 包装入口会自动完成建/拆隧道

`scripts/sync-from-252.sh` 与 `scripts/sync-schema-to-252.sh` 已退休，禁止作为同步入口。推荐用 `scripts/local-host-sync-db.sh`（自动：pre-sync `pg_dump` 备份 → 建隧道 → 调 `pg-table-copy.sh` → 同步后强制执行 §3.5 双审计；支持 `--schema-only` / `--verify` / `--backup-only` / `--root <path>`，其中 `--backup-only` 与其余模式互斥）；或直接使用 `scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-local.sh`（隧道与审计需手动处理）。不得恢复或新建旧入口的自动化调用。

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

### 3.3 "重启后密码变回去"（踩坑点 #2，2026-08-31 机制更正）

**症状**: 本地改完密码 → 重启/重建容器 → 密码回到旧值。

**原因**: entrypoint **不会**在已有数据目录上改写密码（见 §2.2 机制更正）。出现该现象说明容器实际挂载的数据目录与预期不同（用 `docker inspect llm-gateway-pg --format '{{json .Mounts}}'` 核对；当前容器与 `recreate-llm-gateway-pg.sh` 均指向 `~/.agents-cache/llm-gateway-pg-data`，若看到其他路径说明挂的是另一个集群），或数据目录被清空后触发首次 initdb。

**解决**: 先 `docker inspect llm-gateway-pg --format '{{json .Mounts}}'` 确认实际数据目录；密码按 §2.2 人工修正，禁止用脚本自动重置。

### 3.4 同步后行数漂移

**预期行为**: 同步后立即跑行数对比，会看到 sub-percent 漂移（生产端实时写入导致）。

```bash
# 抽样对比
docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg psql -U llm_gateway -d llm_gateway -tAc \
  "SELECT count(*) FROM credentials"
# 252 端相同命令应得到几乎一致的结果（差 1-2% 视为正常）
```

### 3.5 同步完成门禁：结构与数据双审计

每次同步（包括 schema-only 或 data-only）后，**必须**运行并通过以下两项只读审计，才可宣称同步完成：

- `scripts/local-dev/verify-db-consistency.sh --verify`：表、列、视图、索引、约束、序列、函数及分区/热表结构契约
- `scripts/local-dev/verify-db-data-consistency.sh`：普通表完整集合、逐表精确行数和顺序无关且重复敏感的内容摘要

审计连接 252 时同样必须在隧道调用阶段动态解析容器 IP，结束用 `db252_tunnel_teardown` 清理（只清理自建 listener）。迁移跟踪表和表名/行数抽样均不能代替这两项审计；热表和分区数据的明确排除只可由审计契约判定。

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

**答**: 默认 schema 模式**不发出 DROP**（安全模式），失败不会破坏目标现有数据，直接重跑同步即可；导入失败只置失败标志并在 SUMMARY `exit 1`，脚本**没有**自动回滚机制，所以直接调引擎前建议手动 dump 一份当前 local schema 备份（`local-host-sync-db.sh` 包装入口会自动做 pre-sync 备份）。若需重建 schema 用显式 `--clean-schema`（该模式确实会 `--clean --if-exists`，先 DROP 目标对象再导入，仅限一次性本地环境）。

### Q2: 同步后容器里某些视图查询失败？

**答**: 不要用 `pg_reload_conf()` 处理 schema 依赖。先重跑 schema 同步，再检查依赖对象和显式列清单；分区表还要执行 rule 49 要求的 view rebuild 校验。

### Q3: 业务 smoke 失败，错误提到 `_xxx` 表？

**答**: 业务代码不应该引用 `_` 前缀表。如果发现，说明：

1. 业务硬编码了历史表名（需要修代码）
2. 或者 view/migration 没正确重建（需要重跑 schema import）

按 rule 19 §11 走清理流程，而不是回滚 rename。

### Q4: 重建容器后 Navicat 连不上？

**答**: Navicat 走 TCP，使用 `127.0.0.1:5432`（本集群容器直映，见 §一端口布局）。使用 `llm_gateway` 以及 envs loader 提供的当前凭据登录；不要把密码复制到文档或连接备注。

### Q5: 把本地 feature 表 DDL 灌到 252 时报 `columnar_insert_only_parents() does not exist`？

**答**: `pg_dump --schema-only` 会在导出 SQL 里写 `SELECT pg_catalog.set_config('search_path', '', false)`（安全考虑）。252 上有事件触发器 `enforce_columnar_trigger`，每当执行表 DDL 就会触发，并调用未加 schema 限定的函数 `columnar_insert_only_parents()`；`search_path=''` 下找不到该函数，于是**每张 CREATE/DROP TABLE 都失败**。

修复：导出后先剔除这一行再执行（dump 里所有对象都已用 `public.` 显式限定，剔除无副作用）：

```bash
grep -vE "set_config\('search_path', '', false\)" feature.sql > feature.fixed.sql
# 使用受控 helper 建立隧道；不要手工杀掉未知 listener。
source configs/env-252.sh
source scripts/lib/252-db-tunnel.sh
db252_tunnel_ensure
trap db252_tunnel_teardown EXIT
PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" \
  -v ON_ERROR_STOP=1 -f feature.fixed.sql
```

另外注意：`pg_dump -t` 每张表要单独写一个 `-t`（`-t "a b c"` 会被当成单个表名而报 "too many command-line arguments"）；用 bash 数组 `"${args[@]}"` 传给 `docker exec ... pg_dump` 在 zsh 下也会塌缩，建议循环逐表 dump 再追加到文件。月度分区（如 `request_logs_bodies_2026_10`）命中 hot 表过滤，仅 252 有、本地无，属**预期差异**，不必回灌。

---

## 六、相关脚本与文档

| 文件 | 作用 |
|------|------|
| `scripts/local-host-sync-db.sh` | **推荐同步入口**（一键包装）：pre-sync `pg_dump` 备份 → 自动建/拆 252 隧道 → 调 `pg-table-copy.sh` → 同步后强制执行双审计；支持 `--schema-only` / `--verify` / `--backup-only` / `--root <path>` |
| `scripts/pg-table-copy.sh` | 底层同步引擎（不管理隧道）：默认不 DROP/不清空目标，`--clean-schema` 与 `--replace-data` 均为显式危险选项；catalog 识别分区，manifest 驱动逐表导出/导入，导出/导入失败即 exit 1 |
| `scripts/sync-from-252.sh`、`scripts/sync-schema-to-252.sh` | **已退休**；不得执行或在新流程中引用，改用 `scripts/pg-table-copy.sh` |
| `scripts/local-dev/recreate-llm-gateway-pg.sh` | 重建 docker 容器（保留数据目录）；`POSTGRES_PASSWORD` 仅在全新数据目录首次 initdb 时生效，**不修改已有集群用户**（策略：只新增）|
| `scripts/local-dev/verify-db-consistency.sh` | 252 ↔ local 七维结构校验（表/列/视图/索引/约束/序列/函数）；含 gated `--reconcile` 回灌模式。**每次同步后必须执行**——迁移跟踪表随数据复制，不能反映真实结构 |
| `scripts/local-dev/verify-db-data-consistency.sh` | 252 ↔ local 普通表数据校验；比较完整表集合、逐表精确行数和顺序无关/重复敏感内容摘要；hot/分区数据按契约跳过 |
| `scripts/local-dev/apply-routing-mv-fixup.sh` | v1.1 补齐 252 三对象（matview ×2 + columnar helper）；**已被 `pg-table-copy.sh` PHASE 8.5 自动调用**（仅本地 docker 容器、非 dry-run / data-only 时执行），幂等；仍可独立手工调用 |
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
| 2026-08-30 | 1.3  | 数据同步安全修复：默认安全 schema 模式不 DROP，精确替换必须显式 `--replace-data`；catalog 识别分区并以 manifest 驱动数据导入；新增普通表数据签名审计；修复 local `request_logs_default` DEFAULT 分区关系及索引漂移；最终结构与数据双审计通过 |
| 2026-08-31 | 1.4  | `verify-db-consistency.sh` 升至 v1.2：新增**函数维度**（name\|参数身份\|md5(prosrc)），闭环原六维审计不比较函数 DDL 的盲区（迁移 628 等函数迁移此前不可见）；复验发现 252 缺 6 个函数（来自已提交迁移 382/383/433/534，本地因自跑 startup 迁移而具备），已按 gated reconcile 思路回灌 252，七维结构 + 261 表数据双审计全绿 |
| 2026-08-31 | 1.5  | **用户/密码策略**：自动化只允许新增用户，禁止自动修改已有用户或密码；更正 entrypoint 密码机制（仅首次 initdb 写入，已有集群不会被 env 覆写）；§2.2/§3.3 从"重建容器自动对齐密码"改为人工修正流程；recreate 脚本加入策略守卫 |
| 2026-08-31 | 1.6  | **端口陷阱更正**：本地开发地址从 `127.0.0.1:5432` 更正为 `127.0.0.1:15432`（5432 是 Homebrew 原生 postgresql@17，非本集群）；记录 15432 与 252 SSH 隧道的 IPv4/IPv6 双栈冲突及 `-h 127.0.0.1` 规约；recreate 脚本对齐真实容器（数据目录 `~/.agents-cache/llm-gateway-pg-data`、端口 15432、网络 shared-infra） |
| 2026-08-31 | 1.7  | **端口布局定稿**：移除 Homebrew 原生 `postgresql@17`（及遗留 `postgresql@15` 数据目录，移除前全量备份至 `~/backups/`）；`llm-gateway-pg` 容器直映宿主机 `5432`；`15432` 归还 252 隧道专用，双栈冲突消除；recreate 脚本 PORT_BIND 改为 `127.0.0.1:5432:5432`；6 账号经发布端口 5432 全部验证通过 |
| 2026-08-31 | 1.8  | **本地库大合并**：容器成为唯一本地 PG（库：`llm_gateway`/`acc_db`/`kaixuan`/`pocket` + 从 Homebrew 备份恢复 `memora`/`redclaw_local`/`redclaw_test`/`smm_data`）。修复：acc_db 补迁移 105→121+184/185 并归正属主 acc_app；宿主机 acc-go 改指 acc_db 并重启；应用 V360（credential_probe_queue.automatic）与 request_logs.model_name 补列；GRANT fencing.lease_ledger→platform_app；补建 task_assigner_* / task_assignments / mcp_registry。例外：`kx-citus`(15433) 是 opencode-pocket 容器栈的活库（pocket 33 表），未合并，待该项目自行迁移 |
| 2026-09-01 | 1.9 | 凭据改为仅通过 env loader 的 `COMMON_PG_SUPERUSER_PASS` alias 使用；SSH 改为 config/证书认证；隧道调用时动态解析 Podman IP 并以专属 control socket 清理；结构与数据审计均为完成门禁；退休两个旧同步入口 |
| 2026-09-01 | 1.10 | **pg-table-copy.sh PHASE 8.5 自动调用 routing-mv fixup**：闭环 252-only 的 `routing_analytics_7d` / `routing_audit_summary_7d` 物化视图与 `columnar_insert_only_parents()` 函数漂移；fixup 脚本 v1.0 → v1.1，新增 `PG_FIXUP_DB` env（默认 `llm_gateway`）便于被主脚本通过 `PG_FIXUP_CONTAINER`/`USER`/`PASS`/`DB` 注入参数；同步流程不再需要手工跑 fixup（仍可独立调用，幂等）|
| 2026-09-01 | 1.11 | **文档-代码一致性审计修订**（4 任务并行审计 + 本地复现验证）：① Q1 更正——默认 schema 模式不 DROP、无自动回滚（原文与代码相反）；② 移除不存在的 "SSH control socket" 机制描述（实现是所有权 PID 语义：只复用健康 listener、只 kill 自建 PID）；③ hot 表模式与脚本默认对齐（catalog 判定含 2025 分区；`*_archived`/`*_archive` 为后缀匹配，非中缀）；④ §3.3 数据目录表述更新（recreate 脚本与容器现均指向 `~/.agents-cache/llm-gateway-pg-data`）；⑤ 补录 `local-host-sync-db.sh` 包装入口（§3.1/§六），明确 `pg-table-copy.sh` 不管理隧道；⑥ 头部版本对齐变更记录 |
