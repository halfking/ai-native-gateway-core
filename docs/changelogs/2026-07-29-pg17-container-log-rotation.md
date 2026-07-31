# 2026-07-29 — PG17 容器日志暴增修复 + 日志轮转配置

## 变更摘要

修复 252 服务器 pg-252-pg17 容器因日志文件 49GB 撑爆磁盘导致 PG 崩溃。

## 改动清单

| 操作 | 详情 |
|------|------|
| 容器日志清理 | 截断 47GB ctr.log，删除旧轮转日志（~12GB），释放 ~58GB |
| 磁盘全面清理 | 删除旧 kx-verifyserver-wlt 镜像（4 旧版）、部署残留，释放 ~71GB |
| 容器 log driver | k8s-file → json-file + max-size=100m + max-file=3 |
| logrotate 配置 | `/etc/logrotate.d/podman-pg-252`（daily, 100M, rotate 3） |
| 启动脚本 | `/opt/scripts/pg17-start.sh` |

## 验证结果

| 项目 | 结果 |
|---|---|
| PG 运行 | Up, accepting connections |
| 数据库 | 326 张表正常，18.7GB 数据无丢失 |
| 磁盘 | 47%（原 85%），102GB 可用 |
| 日志驱动 | json-file with max-size=100m, max-file=3 |

## 经验文档

`docs/lessons-learned-2026-07-29-pg17-ctr-log-explosion.md`
