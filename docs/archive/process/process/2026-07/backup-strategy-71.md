# 71 PG 备份策略（Backup Strategy for 71 PostgreSQL）

> **Last updated:** 2026-06-30
> **Status:** ✅ Production (daily running since 2026-06-30)
> **Maintainer:** Kaixuan DevOps Team
> **Scope:** 71 server (__INTERNAL_K8S_HOST__ / __SECRET_1__) — `llm-gateway-pg-71-replica` 容器
> **Environment:** volcano 71 (formerly a streaming replica, now promoted primary after 184 outage)

---

## 1. 概述 (Overview)

71 上的 PostgreSQL（`llm-gateway-pg-71-replica` 容器，运行 `citusdata/citus:11.3.0` 镜像）承载 **15 个业务数据库 + globals**，包含：

- 14 个业务库：`llm_gateway`, `casdoor`, `__USER_2__`, `trendaradar`, `crm`, `brandmind`, `brandmind_test`, `doc_tools`, `geo_flow`, `smart_bidding`, `stock_trading`, `port_email`, `memos`, `aicms_db`
- 全局用户/角色 (`pg_dumpall --globals-only`)
- **4 个按月分区的列存大表**：`request_logs` (RANGE ts), `credential_model_index` (RANGE bucket), `routing_decision_log` (RANGE ts), `request_wal` (RANGE created_at)
- **4 个 archive 子分区**（列存 `citus_columnar` 11.3）

### 凭据 (Credentials, 2026-06-30 验证)

| User | Password | 库 | 用途 |
|------|----------|----|----|
| **`llm_gateway`** | `__SECRET_2__` | 14 个库（superuser） | 主超级用户（备份 + 跨库 admin） |
| `__USER_2___user` | `__USER_2___pass123` | `__USER_2__` | 开轩主应用 |
| `casdoor_user` | `casdoor_pass123` | `casdoor` | Casdoor 认证 |
| `crm_user` | `crm_pass123` | `crm` | CRM |
| `doc_tools_user` | `doc_tools_pass123` | `doc_tools` | 文档工具 |
| `kxuser` (旧) | `__SECRET_2__` | 11 个库 | 184 流复制继承的访问账号 |
| `casdoor_user` (旧) | `__SECRET_2__` | 多个库 | 184 默认密码 |

**凭据加载机制（脚本 `load_secret()` 函数）**：
1. **环境变量**：`PG_PASSWORD` / `REMOTE_SSHPASS`
2. **SOPS 解密**：`.secrets/secrets.enc.yaml` 的 `volcano_71_pg_business_users.pg_superuser_password` / `backup_mirror_ssh.remote_password`
3. **兜底默认值**：脚本里硬编码的密码（应急用，仅在 SOPS 不可用时）

**71 上需要 SOPS 工具**：
- `sops` 二进制（v3.9.0+，从 `https://github.com/getsops/sops/releases` 下载 linux-amd64）
- `~/.config/sops/age/keys.txt`（age 私钥，mode 600）
- `/opt/databackups/.secrets/` 目录（包含 `secrets.enc.yaml` + `.sops.yaml`）

**完整备份方案**包含：
1. **Daily 全量 dump**（每天 02:30，14 库 + globals）
2. **异地同步到 56**（rsync over sshpass，每天备份后自动同步）
3. **3 天本地保留**（`/opt/databackups/daily/<DATE>/`）
4. **Restore test**（验证 backup 可恢复 + archive 列存数据完整）
5. **MANIFEST.txt** + **SHA256 校验**

## 2. 目录结构 (Directory Layout)

### 本地（71）

```
/opt/databackups/
├── daily/                           # 每日全量 dump
│   ├── 2026-06-28/                  # 3 天前（最旧保留）
│   │   ├── aicms_db.dump            # 单库 custom-format dump
│   │   ├── brandmind.dump
│   │   ├── ...
│   │   ├── globals.sql              # pg_dumpall --globals-only
│   │   ├── pg-full-71-2026-06-28.dump    # tar 合成总文件
│   │   ├── pg-full-71-2026-06-28.dump.sha256
│   │   ├── MANIFEST.txt             # 元信息（PG 版本 / 大小 / SHA256 / DB 列表）
│   │   └── backup.log
│   ├── 2026-06-29/
│   └── 2026-06-30/                  # 今天（最新）
│
├── logs/
│   ├── backup.log                   # 所有 backup run 累计日志
│   └── cron.log                     # cron 调度日志（如果配置 cron）
│
└── restore-test/                    # restore test 临时工作区
    └── restore-test-<DATE>-<TIME>/  # 验证完自动清理
```

### 异地（56 容灾）

```
/opt/databackups-71-mirror/         # 56 上 rsync 镜像
└── <和 71 daily 同步>
```

## 3. 备份策略 (Backup Policy)

| 维度 | 设置 | 说明 |
|------|------|------|
| **频率** | 每天 02:30 | cron 调度（详见 §6） |
| **保留** | 3 天 | `find -mtime +3 -exec rm -rf {}` 自动清理 |
| **大小** | 1.8 GB/day | 实际值：1.86 GB（包含 14 库 + globals + 4 个列存 archive） |
| **磁盘占用** | 5.6 GB | 3 天 × 1.86 GB |
| **容灾** | rsync 56 每天 | 每天 backup 完成后自动同步 |
| **异地保留** | 同 71 | 56 上同样 3 天 |
| **算法** | `pg_dump -Fc --no-acl --no-owner` | custom format，无 ACL/owner 跨主机问题 |
| **压缩** | gzip (pg_dump 内置) | custom format 默认 zstd/gzip |
| **校验** | SHA256 | 每个 dump 文件单独校验 |
| **恢复点目标 (RPO)** | ≤ 24h | 上一日 02:30 备份 |
| **恢复时间目标 (RTO)** | ~10 分钟 | 1.86 GB dump restore + 启动 |

## 4. 关键决策 (Key Decisions)

### 4.1 使用 `llm_gateway` superuser 而非 `kxuser`

**问题**：原本用 `kxuser`（业务访问账号）dump 失败，因为：
- `approval_queue` 表有 `relforcerowsecurity = t`（即使 owner 也要 RLS）
- `crm_activities` 等表对 kxuser 没权限
- `doc_tools_llm_providers` 有 RLS 限制

**解决**：改用 `llm_gateway`（超级用户 + `bypassrls = t`）— 71 副本 promote 后这个用户能 dump 所有库。

**代价**：dump 文件 owner 变成 `llm_gateway`（kxuser 之前的）。restore 时通过 `--no-acl --no-owner` 忽略 owner 差异。

### 4.2 71 promote 之前/之后的 backup 差异

71 在 2026-06-30 13:00 前是 **streaming replica**。期间：
- `pg_dump -Fc` 在 replica 上对 `citus_columnar` 11.3 的 access method 处理有 bug
- archive 子分区（columnar）的 DATA 不被 dump 出来
- 13/14 个 archive 对象在 backup 中丢失

**71 promote 之后**（当前状态）：
- `pg_dump -Fc` 完整 dump 所有对象
- 4 个 archive 父表 + 4 个 archive 子分区 + 4 段 TABLE DATA + 4 个 archive 函数全在
- 总 dump 大小从 60MB（replica 时期）→ 1.86GB（promote 之后），**30x 增长**

### 4.3 异地同步用 sshpass 而非 SSH key

71 上没有 56 的 SSH 密钥。`/opt/scripts/backup-pg-71.sh` 用 `sshpass` + 56 的 SSH 密码（`REMOTE_SSHPASS` 环境变量）做 rsync over SSH。

**安全考虑**：
- 密码是 56 的 SSH 密码（root）
- 不入 git，不入 SOPS
- 只在脚本顶部作为 `REMOTE_SSHPASS="${REMOTE_SSHPASS:-__SECRET_3__}"` 兜底
- 实际生产建议用 vault 注入

## 5. 脚本 (Scripts)

### 5.1 `/opt/scripts/backup-pg-71.sh`

主备份脚本，支持 4 个模式：

```bash
bash /opt/scripts/backup-pg-71.sh daily          # 每天 02:30 跑
bash /opt/scripts/backup-pg-71.sh restore-test   # 验证 backup 可恢复（TOC 层面）
bash /opt/scripts/backup-pg-71.sh sync-remote    # 仅同步到 56
bash /opt/scripts/backup-pg-71.sh list           # 列出所有 backup（本地 + 异地）
```

**关键参数**：
- `RETENTION_DAYS=3`
- `DOCKER_CONTAINER=llm-gateway-pg-71-replica`
- `PG_USER="llm_gateway"` (superuser)
- `PG_PASSWORD="__SECRET_2__"`
- `REMOTE_HOST="root@__HOST_56_IP__"`
- `REMOTE_PORT="__PORT_1__"`
- `REMOTE_DIR="/opt/databackups-71-mirror"`

**14 业务库**（按 AGENTS.md 14 库清单）：
```bash
DATABASES=(
    "llm_gateway" "casdoor" "__USER_2__" "trendaradar" "crm"
    "brandmind" "brandmind_test" "doc_tools" "geo_flow"
    "smart_bidding" "stock_trading" "port_email" "memos" "aicms_db"
)
```

### 5.2 `/opt/scripts/restore_test_v8.sh`

**真实 restore test**（100% 隔离，不碰 71 生产）：
- 起独立 `citusdata/citus:11.3.0` 容器
- data dir 用 **tmpfs 8GB**（不是 bind mount）
- port **55499**（不是 __PORT_5__/__PORT_6__/__PORT_7__）
- 不挂 `/data/llm-gateway-pg-71-replica`
- 恢复 backup 到**新库 `llm_gateway_iso_<TS>`**（不污染原库名）
- 验证 4 个 archive 子表数据 + 4 个 archive 函数
- 验证主表行数
- drop 临时库 + stop/rm 容器 + 删 workdir
- **二次确认生产 PG 仍在跑**

## 6. 调度 (Scheduling)

### 6.1 cron 配置

```bash
# 每天 02:30 跑（71 上 systemd timer 或 cron.daily）
0 2 * * * /opt/scripts/backup-pg-71.sh daily >> /opt/databackups/logs/cron.log 2>&1
```

**注意**：71 上原本有 `backup-pg-daily.sh`（老脚本，输出到 `/opt/databackup/pg-daily/71/`）— **新脚本接管后已废弃**。需要禁用老 cron / systemd timer。

### 6.2 手动触发

```bash
# 立即跑一次（不等 cron）
ssh 71 "bash /opt/scripts/backup-pg-71.sh daily"

# 验证 backup 可恢复
ssh 71 "bash /opt/scripts/restore_test_v8.sh"

# 列出现有 backup（本地 + 56）
ssh 71 "bash /opt/scripts/backup-pg-71.sh list"
```

## 7. 验证 (Verification)

### 7.1 daily 完成后

```bash
# 看 71 本地
ls -la /opt/databackups/daily/$(date +%Y-%m-%d)/
cat /opt/databackups/daily/$(date +%Y-%m-%d)/MANIFEST.txt

# 验证 SHA256
cd /opt/databackups/daily/$(date +%Y-%m-%d)/
sha256sum -c pg-full-71-*.dump.sha256

# 看 56 镜像
ssh 56 "ls -la /opt/databackups-71-mirror/$(date +%Y-%m-%d)/"
```

### 7.2 restore test 验证项

| 验证项 | 期望 |
|--------|------|
| `credential_model_index_archive_2026_06` | > 0 行，storage=columnar |
| `request_logs_archive_2026_06` | > 0 行，storage=heap（JSONB 走 heap） |
| `routing_decision_log_archive_2026_06` | > 0 行，storage=columnar |
| `request_wal_archive_2026_06` | > 0 行，storage=columnar |
| `request_logs` (主表) | > 0 行 |
| `credential_model_index` (主表) | > 0 行 |
| `archive_credential_model_index` 函数 | 存在 |
| `archive_request_logs` 函数 | 存在 |
| `archive_routing_decision_log` 函数 | 存在 |
| `archive_request_wal` 函数 | 存在 |
| `providers` (业务表) | > 0 行 |
| 生产 `llm-gateway-pg-71-replica` | 仍在跑（test 完后验证） |

### 7.3 真实测试结果（2026-06-30）

| 验证项 | 实际 |
|--------|------|
| `credential_model_index_archive_2026_06` | 9 行，columnar ✅ |
| `request_logs_archive_2026_06` | 11,328 行，heap ✅ |
| `routing_decision_log_archive_2026_06` | 21,758 行，columnar ✅ |
| `request_wal_archive_2026_06` | 13,848 行，columnar ✅ |
| `request_logs` | 84 行 ✅ |
| `credential_model_index` | 186,422 行 ✅ |
| 4 个 archive 函数 | 全部存在 ✅ |
| `providers` | 42 行 ✅ |
| 生产 71 PG | 仍在跑 ✅ |

## 8. 恢复流程 (Recovery Procedure)

### 8.1 完整恢复（灾难场景）

```bash
# 1. 在 71 上找最近一个完整 backup
LATEST=$(ssh 71 "ls -1t /opt/databackups/daily | head -1")
echo "Latest backup: $LATEST"

# 2. 解 tar
ssh 71 "cd /tmp && mkdir -p restore && cd restore && \
  scp root@71:/opt/databackups/daily/$LATEST/pg-full-71-$LATEST.dump . && \
  tar xf pg-full-71-$LATEST.dump"

# 3. 在隔离环境验证（不起生产实例）
ssh 71 "bash /opt/scripts/restore_test_v8.sh"

# 4. 如果生产 PG 挂了，stop 旧容器 + 起新容器 + restore
ssh 71 "docker stop llm-gateway-pg-71-replica && docker rm llm-gateway-pg-71-replica"
ssh 71 "cd /opt/databackups/daily/$LATEST && tar xf pg-full-71-$LATEST.dump"

# 5. 起新容器
ssh 71 "docker run -d --name llm-gateway-pg-71-replica \
  --network host --restart=no \
  -v /data/llm-gateway-pg-71-replica:/var/lib/postgresql/data \
  -e POSTGRES_USER=llm_gateway \
  -e POSTGRES_PASSWORD=__SECRET_2__ \
  citusdata/citus:11.3.0 \
  -c shared_preload_libraries=citus,citus_columnar -c max_connections=1000"

# 6. 改 env __SERVER_PATH_3__/env 指向 127.0.0.1（如果之前指 184）
ssh 71 "sed -i 's|@__INTERNAL_K8S_HOST__:__PORT_5__|@127.0.0.1:__PORT_5__|g' __SERVER_PATH_3__/env"
```

### 8.2 应急恢复（71 → 56 同步 + 56 上启动）

```bash
# 56 上有完整 mirror
ssh 56 "ls -la /opt/databackups-71-mirror/"

# 56 上起一个独立 PG 实例恢复（同样用 v8 脚本思路）
# 注意：56 是 nps 入口，PG 流量不要直接暴露
```

## 9. 监控 (Monitoring)

### 9.1 健康检查

```bash
# 1. 最近 backup 是否成功
ssh 71 "tail -20 /opt/databackups/logs/backup.log"

# 2. SHA256 校验
ssh 71 "find /opt/databackups/daily -name '*.sha256' -exec sha256sum -c {} \;"

# 3. 56 mirror 是否同步
ssh 56 "ls -la /opt/databackups-71-mirror/$(date +%Y-%m-%d)/"

# 4. 71 disk usage
ssh 71 "df -h /opt/databackups/ && du -sh /opt/databackups/*"
```

### 9.2 告警条件（建议）

| 条件 | 告警级别 |
|------|---------|
| daily 任务超过 30 分钟未完成 | WARN |
| 最近 backup 文件 < 1GB（说明 dump 失败或不全） | CRITICAL |
| 71 /opt/databackups 占用 > 80% (10GB) | WARN |
| 56 上 mirror 比 71 落后 > 24h | WARN |
| restore-test 失败 | CRITICAL |

## 10. 已知限制 (Known Limitations)

### 10.1 RPO = 24 小时

每天 02:30 一次全量，**丢失最近 24h 写入**。
**改进方向**：加 WAL archive 实现 PITR（point-in-time recovery），但需要：
- `archive_mode = on`
- `archive_command` 指向 rsync 到 56
- 每次 restore 时 base backup + apply WAL

### 10.2 没有异地异地

56 是火山引擎同一集群内不同物理机。**没有真正跨可用区容灾**。
**改进方向**：再 rsync 一份到 252（itestu.cn 体系）或对象存储。

### 10.3 备份文件本身没加密

`pg-full-71-*.dump` 含明文业务数据。dump 文件权限 644（root 可读）。
**改进方向**：`gpg --symmetric` 加密，密钥入 vault。

### 10.4 SSH 密码硬编码在脚本中

`REMOTE_SSHPASS` 默认值是 56 的 SSH 密码（注释里写明）。生产建议：
- 改为环境变量注入
- 或用 71 跳 56 的 SSH 密钥

### 10.5 老备份脚本未禁用

`/opt/scripts/backup-pg-daily.sh`（旧 v1）仍在 71 上，每天 02:30 会**重复跑**输出到 `/opt/databackup/pg-daily/71/`。需要：
- 找到 cron / systemd timer 入口
- 禁用或删除

## 11. 事故历史 (Incident History)

### 2026-06-30 13:00 — 184 整体宕机

- **症状**：184 上 k3s / Casdoor / ACC / llm-gateway-go / PG __PORT_5__ 全部不可达
- **影响**：71 流复制副本断流，71 上 trendaradar-go / brandmind-go / __USER_2__ 全部 Restarting
- **响应**：
  1. 71 副本 promote 成独立主库（`pg_promote()`）
  2. llm-gateway-go env 改 184 → 127.0.0.1
  3. 备份脚本从 kxuser 改 llm_gateway（superuser）
  4. 创建 `/opt/databackups/` 全新结构

### 2026-06-30 13:15 — Restore test 误挂 71 data dir

- **症状**：测试脚本用 `-v /data/llm-gateway-pg-71-replica:/var/lib/postgresql/data` 把生产 data 挂到测试容器
- **影响**：测试容器启动 PG 时往 71 的 data 写 `postmaster.pid`，与生产容器 lock file 冲突，生产 PG 异常退出
- **恢复**：docker rm 旧容器 + 用同样参数 docker run 新容器，__PORT_5__ 重新监听，186,422 行数据完整
- **修复**：所有 restore test 改用 `--tmpfs /var/lib/postgresql/data:rw,size=8g` 隔离

## 12. 关联文档 (Related Docs)

- `services/llm-gateway-go/db/migrations/` — archive 函数 + 分区表 schema 演进
- `services/llm-gateway-go/bg/partition_manager.go` — 月度转储 Go worker
- `deploy/数据库/request-logs-hot-cold-tiering.md` — 早期热冷分层方案
- `docs/architecture/184-71-streaming-replication.md` — 184↔71 复制历史

## 13. 变更日志 (Changelog)

| 日期 | 变更 | 作者 |
|------|------|------|
| 2026-06-30 | v2.0 创建（daily 3d + 56 mirror + restore test） | agent |
| 2026-06-30 | 切换 dump 用户 kxuser → llm_gateway（解决 RLS/权限） | agent |
| 2026-06-30 | 加 restore test v8（tmpfs 8GB 隔离） | agent |
| 2026-06-30 | 文档定稿 | agent |
