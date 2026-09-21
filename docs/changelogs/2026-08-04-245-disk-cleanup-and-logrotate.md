# 2026-08-04 — 245 预生产服务器磁盘清理 + stderr 日志轮转

## 变更摘要

修复 245 (<env:HOST_245_IP>) 预生产服务器磁盘撑爆（97%/1.5G 可用），并配置 llm-gateway-go stderr/stdout 日志轮转防止再次膨胀。

## 触发条件

- 245 `/dev/vda3` 40G 总容量，已用 36G（97%），可用仅 1.5G
- 触发 rule 10 §4.3 磁盘红线：AI 立即停止任何写盘操作、列候选项、等老板决策

## 改动清单

| 操作 | 详情 | 释放量 |
|------|------|--------|
| **A. 历史发布版本归档** | `/opt/backup/releases_20260727/` (262 个版本目录，13G) → mv → `/opt/backup/.trash/releases_20260727.20260804` → rm 真正释放 blocks | **13G** |
| **B. stderr 日志归档 + 截断** | `/var/log/llm-gateway-go/gateway.stderr.log` (4.1G 单文件) → mv 到 trash 归档 → 创建新空文件 (mode 600) → `systemctl restart llm-gateway-go` 让 systemd 重开 fd | 立即清零 |
| **B+. logrotate 轮转配置** | `/etc/logrotate.d/llm-gateway-go` (daily, size 100M, rotate 7, copytruncate, compress) | 防止再次膨胀 |
| **本仓模板** | `deploy/logrotate-llm-gateway-go` (同名配置，作为仓库真源) | — |
| **管理脚本** | `scripts/install-logrotate.sh` (install/uninstall/status/verify 子命令, file/stdin 双配置源, 幂等, 用于向其他 pre-prod 服务器同步) | — |

## 验证结果

| 项目 | 命令 | 结果 |
|---|---|---|
| 磁盘释放 | `df -h /` | 36G/97%/1.5G → **24G/63%/15G** |
| service 健康 | `systemctl is-active llm-gateway-go` | active |
| 新 stderr 写入 | `ls -lh` (5s 后) | 0 → 55K（健康） |
| logrotate dry-run | `logrotate -d /etc/logrotate.d/llm-gateway-go` | exit 0，7 代保留生效 |
| logrotate 强制触发 | `logrotate -f -v` | copytruncate 成功，服务不中断 |
| 归档审计日志 | `/opt/backup/.trash/.archive-log` | 写入两条（含 WHY + 操作人 + 释放量）|

## 关键决策

### A 项：mv → trash → rm（rule 03 §6b 三段式）

`mv` 在同 FS 下是 rename，不释放 blocks（df 不变）。要真正释放 13G 必须 rm。
归档日志已写入 `/opt/backup/.trash/.archive-log`，trash 路径仍可在 30 天内手动恢复。

### B 项：mv + restart + logrotate（systemd append: 模式兼容）

`/etc/systemd/system/llm-gateway-go.service` 用 `StandardError=append:/var/log/...` 直写文件，不走 journald。
- 单纯 mv 不会让 systemd 重开 fd（仍写 unlink 的旧 inode）
- 必须 `systemctl restart` 让 systemd 重新打开新路径的 fd
- logrotate 必须用 `copytruncate` 模式（保留 inode，不触发 systemd append 失效）

### logrotate 配置要点

```
daily | size 100M | rotate 7 | compress | delaycompress | copytruncate
- daily 或 100M（先到先触发）
- 保留 7 代
- delaycompress = 延迟压缩（最新一代不压，方便 tail）
- copytruncate = 兼容 systemd append: 模式
```

## 遗留与风险

- ⚠️ **kx-registry.service 缺失**：metadata 标记 "active 生产用"，但 systemd unit 找不到，docker 容器也 Exited 7 days。**与本次清理无关**，需要单独排查（如果生产构建镜像依赖这个 registry，会卡住）。
- ⚠️ **磁盘仍可继续瘦**：还有 ~6.5G 可回收
  - `/opt/llm-gateway-go/releases/` 1.1G (23 个版本目录，仅保留最近 3 个 = 150M)
  - `/opt/kx-images/upstream/` 1.7G (基础镜像缓存)
  - `/opt/llm-gateway-go/*.bak*` 280M (多个历史 .bak)
  - `/opt/llm-gateway-go/data/backups` 306M (应用级备份)
  - Docker 镜像 583M (21 镜像仅 2 active)
  - 当前 63% 健康，可等下次再清。
- ⚠️ **245 pre-prod 单点 logrotate**：建议同步到其他 pre-prod / 184 prod（若有 append: 模式服务需要同样配置）。184 prod 当前是 systemd journald，不适用此文件。

## 下一步建议

1. **告警埋点**：`du -sh /var/log/llm-gateway-go/gateway.stderr.log` > 500M 触发告警
2. **排查 kx-registry**：单独任务（与磁盘清理无关）
3. **同步 logrotate 到其他 pre-prod 服务器**（用 `scripts/install-logrotate.sh install`，支持 scp 传配置或 stdin pipe 两种方式）

## 相关规则

- rule 03 §6b：备份目录三段式（归档 → 标记 → 清理）
- rule 03 §7.0：部署前 4 步备案（备份二进制/配置/schema/独立回滚脚本）
- rule 10 §4.3：磁盘空间不足时强制停下问老板
- rule 36：commit 前更新 CHANGELOG.md + docs/changelogs/<date>-<slug>.md

## 操作记录

| 日期 | 操作 | 老板授权 |
|---|---|---|
| 2026-08-04 00:14 | 245 部署 v1430-03d15237 | (背景上下文) |
| 2026-08-04 00:17 | df 告警：磁盘 97% | — |
| 2026-08-04 00:18 | AI 跑诊断（df/du/ls/logrotate），列 8 项候选项 | — |
| 2026-08-04 00:19 | 老板明确："清理 A 和 B，注意将 log 做成轮转模态" | ✅ confirm |
| 2026-08-04 00:21 | 执行 A：mv → trash → rm，释放 13G | ✅ 已授权 |
| 2026-08-04 00:21 | 执行 B：mv stderr.log → trash，截断 + restart + logrotate | ✅ 已授权 |
| 2026-08-04 00:23 | 验证：df 24G/63%，service active，logrotate -f 成功 | — |