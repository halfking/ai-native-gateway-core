# 经验总结：PG17 容器日志暴增撑爆磁盘 (2026-07-29)

本文档记录了 2026-07-29 pg-252-pg17 容器因日志文件 49GB 撑爆磁盘导致 PG 崩溃的根因分析、修复过程和预防措施。

## 1. 背景

252 服务器（115.29.212.252）上运行 pg-252-pg17 容器，使用 podman 的默认 `k8s-file` 日志驱动。该驱动将容器 stdout/stderr 写入 `ctr.log` 且**无任何日志轮转机制**。

## 2. 事故时间线

| 时间 | 事件 |
|---|---|
| 2026-07-29 03:09 | PG 容器连续 25 次启动失败：`could not write lock file "postmaster.pid": No space left on device` |
| 2026-07-29 03:09:21 | 容器退出（ExitCode=1） |
| ~5 小时后 | 发现并诊断 |
| 2026-07-29 08:11 | 清理日志（58GB）+ 重启容器 → PG 恢复 |

## 3. 根因分析

### 3.1 直接原因：ctr.log 49GB 撑爆磁盘

```
ctr.log (current):      47 GB
ctr.log-20260728.gz:    6.8 GB
ctr.log-20260727.gz:    4.9 GB
ctr.log-20260726.gz:    831 MB
总计:                   ~59 GB
```

容器日志文件积累了 `pg_dump`、`pg_restore`、`VACUUM` 等大量运维操作输出，加上定期备份脚本的 stdout。

### 3.2 根本原因：k8s-file 驱动无固有日志轮转

Podman 默认的 `k8s-file` 日志驱动无线大小限制，所有 stdout/stderr 持续追加到单一文件。

### 3.3 磁盘分布（修复前）

```
/dev/vda3 197G 已用 159G 可用 30G (85%)
  ├─ 58G  pg-252-pg17/userdata/ (ctr.log + rotated)
  ├─ 55G  overlay storage
  ├─ 21G  PG 数据目录
  └─ 25G  其他
```

## 4. 修复措施

### 4.1 即时修复

1. **截断 47GB ctr.log**
2. **删除旧轮转日志**：`ctr.log-202607{26,27,28}.gz`
3. **重启容器**：`podman start pg-252-pg17`
4. **结果**：磁盘从 85% → 54%，PG 恢复

### 4.2 磁盘全面清理

| 清理项 | 释放空间 |
|---|---|
| 容器日志 | ~58 GB |
| 旧 kx-verifyserver-wlt 镜像 (4 旧版) | ~23 GB |
| /root/ 旧部署残留 | ~50 MB |
| **总计** | **~71 GB** |

最终磁盘：**47% 使用（88G/197G），102G 可用**

### 4.3 长期预防

1. **容器 log driver 切换**：`k8s-file` → `json-file` + `--log-opt max-size=100m --log-opt max-file=3`
2. **logrotate 兜底**：`/etc/logrotate.d/podman-pg-252`（daily, maxsize 100M, rotate 3, copytruncate）
3. **启动脚本文档化**：`/opt/scripts/pg17-start.sh`

## 5. 关键经验

### 5.1 CRITICAL — 容器日志驱动必须设置轮转

```bash
# ❌ k8s-file 默认无轮转，日志无限增长
podman run -d --name pg-252-pg17 kx-citus-pg17:amd64

# ✅ json-file + 轮转参数
podman run -d --name pg-252-pg17 \
  --log-driver json-file \
  --log-opt max-size=100m \
  --log-opt max-file=3 \
  kx-citus-pg17:amd64
```

所有长期运行容器必须设置日志轮转。

### 5.2 服务"活着" ≠ 业务"正常"

PG 反复 crash（25 次重启），日志全是 `No space left on device`。磁盘监控阈值应在 60-75% 告警。

### 5.3 容器日志包含所有 exec 命令输出

`podman exec` 运行的 psql/备份输出也写入容器日志，加速增长。

### 5.4 重建容器安全（bind mount）

PG 数据在 bind mount `/data/pg-data-252-pg17`，重建容器不丢数据。PG 自动从 WAL 恢复。

## 6. 相关文件

| 文件 | 用途 |
|---|---|
| `/opt/scripts/pg17-start.sh` | 容器启动参考脚本 |
| `/etc/logrotate.d/podman-pg-252` | logrotate 配置 |
| `scripts/252-monitor/pg17-disk-watch.sh` | 磁盘监控 |
| `deploy-to-252.sh` | 部署脚本 |
| `configs/env-252.sh` | 252 环境配置 |
