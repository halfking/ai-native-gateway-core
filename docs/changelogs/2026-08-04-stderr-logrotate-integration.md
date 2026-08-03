# 2026-08-04 — stderr/stdout 日志轮转集成到 deploy 流程

## 变更摘要

把 `deploy/logrotate-llm-gateway-go` (仓内 SSOT) 通过 `scripts/install-logrotate.sh`
**真正落地到 154/245 服务器**——之前的 commit (`f6279fee4`) 创建了管理脚本但
**没有**集成到任何 deploy 路径。本次把两个 deploy 入口 (顶层 `deploy-154.sh` +
新入口 `scripts/deploy-seamless.sh`) 都接上,deploy 完成后自动 `install`+`verify`。

## 触发原因

- `f6279fee4` (2026-08-04 00:40) 提交了 `install-logrotate.sh`,但仅 245
  (上次 245 磁盘撑爆紧急处理时手工安装) 有 `/etc/logrotate.d/llm-gateway-go`
- 154 生产、71/184 历史上都没装 → stderr 还是会无限增长
- 集成到 deploy 后,任何对 154/245 的成功部署都会顺带保证 logrotate 配置存在

## 改动清单

| 文件 | 改动 | 行为 |
|------|------|------|
| `scripts/install-logrotate.sh` | 改进 | 修 3 个 bug:`SCRIPT_DIR` 用 realpath 解析 (防 cp/symlink 算错位置);`resolve_config` 加 `-t 0` 自动识别 stdin 重定向;`read_config` 改用 `mktemp` 避免 `$(...)` command substitution 抢走 stdin。配合 Docker (debian-slim) 端到端验证 9/9 用例通过 |
| `deploy-154.sh` | 新增 [安装 logrotate] 段 | 在 [重启服务] 之后, [验证部署] 之前。`ssh` + `cat` stdin pipe 把 config 和脚本传到 154 `/tmp/llm-gw-deploy-helpers/`,再 `ssh bash install-logrotate.sh install` 执行。失败 warn 不 abort (deploy 主体已完成) |
| `scripts/deploy-seamless.sh` | 新增 [9.6/9] 步骤 | 在 [9.5/9] admin 密码同步之后, prune 之前。`remote_ssh_pipe` 传 config + 脚本 (静默),`remote_ssh` 调 install (完整输出)。失败 warn 不 abort |

**未改动**:
- `deploy/logrotate-llm-gateway-go` — 245 验证过且工作正常的配置,本次不折腾
- `deploy-245.sh` (顶层不存在,走 `scripts/deploy-245.sh` → `deploy-seamless.sh`)
- `deploy-to-252.sh` — 252 只跑 PG17,没有 llm-gateway-go systemd 服务,不需要
- `install.sh` — 面向 macOS/Linux 本地装用户,不走 systemd,不需要
- `deploy-71.sh / deploy-184.sh / deploy-kaixuan1.sh` — 老板指示"仅生产+预生产"
- `deploy-154-quick.sh` — 应急快速部署,通常不重启服务,跳过 logrotate

## 集成点设计

### `deploy-seamless.sh` [9.6/9] 步骤

```
log "[9.6/9] 安装 logrotate 轮转配置 (/etc/logrotate.d/llm-gateway-go)"

# 1) 传 config + install-logrotate.sh 到 remote /tmp/
cat deploy/logrotate-llm-gateway-go | remote_ssh_pipe "cat > /tmp/llm-gw-deploy-helpers/llm-gateway-go.logrotate"
cat scripts/install-logrotate.sh     | remote_ssh_pipe "cat > /tmp/llm-gw-deploy-helpers/install-logrotate.sh"

# 2) 远程执行
remote_ssh "chmod +x /tmp/llm-gw-deploy-helpers/install-logrotate.sh && \
            bash /tmp/llm-gw-deploy-helpers/install-logrotate.sh install \
                /tmp/llm-gw-deploy-helpers/llm-gateway-go.logrotate"
```

### `deploy-154.sh` (老路径) [安装 logrotate] 段

```
# 同样的两阶段: 传文件 → 调 install
# 用 sshpass + ssh + cat < file 传 config
# 失败: warn (不影响 deploy 主体)
```

## 验证结果

| 项目 | 命令 | 结果 |
|------|------|------|
| `bash -n` 语法 | `bash -n scripts/install-logrotate.sh` | exit 0 |
| `bash -n` 语法 | `bash -n scripts/deploy-seamless.sh` | exit 0 |
| `bash -n` 语法 | `bash -n deploy-154.sh` | exit 0 |
| shellcheck 警告 | `shellcheck scripts/install-logrotate.sh` | 0 error, 2 info (SC2012 `ls` 输出,可接受) |
| Docker end-to-end | `docker run debian-slim + apt install logrotate` | 9/9 用例通过: install / status / verify / uninstall / 幂等 / stdin 显式 / stdin 隐式 / 空配置拒绝 / 缺文件拒绝 |
| logrotate -d 模拟 | `logrotate -d /etc/logrotate.d/llm-gateway-go` | exit 0, 7 rotations, size 100M 生效 |

### Docker 端到端测试用例(精简版)

| # | 场景 | 期望 | 实际 |
|---|------|------|------|
| T1 | `install < /src/deploy/logrotate-llm-gateway-go` (无参 + stdin redirect) | success | ✓ exit=0 |
| T2 | `install - < /src/...` (显式 stdin) | success + 幂等 skip | ✓ exit=0 |
| T3 | `install /src/...` (file path) | success + 幂等 skip | ✓ exit=0 |
| T4 | `install /no/such/file` | refuse | ✓ exit=1 |
| T5 | `install -` + 空 stdin | refuse (缺 /var/log/) | ✓ exit=1 |
| T6 | `status` | 显示 sha256 / mtime / rotate 目标 | ✓ |
| T7 | `uninstall` | 删除目标 | ✓ exit=0 |
| T8 | 重复 `install` (相同内容) | skip | ✓ |
| T9 | `install` 不同内容 | 备份 .bak.<ts> + 覆盖 | ✓ |

## 实际服务器验证(待执行)

老板 push 后,可在 154 / 245 各跑一次:

```bash
# 154 (生产)
bash deploy-154.sh              # 自动调 install-logrotate
ssh root@154 'logrotate -d /etc/logrotate.d/llm-gateway-go'  # 模拟
ssh root@154 'ls -lh /var/log/llm-gateway-go/'                # 看到 7 代内 (rotate 7)

# 245 (预生产, 已装)
bash scripts/deploy-245.sh      # 自动调 install-logrotate (幂等 skip)
ssh root@245 'bash /opt/llm-gateway-go/current/scripts/install-logrotate.sh status'
# 期望输出 sha256 + 已装标记
```

## 关键决策

### 1. 失败时 warn 不 abort

服务已经 healthz OK + DB ready,logrotate 装不装不影响服务运行。
warn 而非 abort:即使远端 `mkdir` 失败 / cat 失败 / bash 失败,deploy
主体成功完成,运维单独排查。

### 2. 传文件用 cat pipe 而非 scp

deploy-seamless.sh 的 `remote_ssh_pipe` 走 ControlMaster 复用(已有 tar
管道),无 scp 依赖,无 host key / sshpass 差异。deploy-154.sh 顶层老
脚本用 sshpass + cat < file,跟现有 ssh 调用风格一致。

### 3. install-logrotate.sh 留 /tmp/ 不清理

remote `/tmp/llm-gw-deploy-helpers/` 残留由 `/tmp` 自动清理策略
(245:7d, 154:10d) 处理,无需脚本显式 rm。运维如需手工跑,
直接 `ssh <server> 'bash /tmp/llm-gw-deploy-helpers/install-logrotate.sh status'`

### 4. logrotate 配置本身不动

`deploy/logrotate-llm-gateway-go` 在 245 验证过(2026-08-04 commit 805296786),
7 代保留,100M size 触发,copytruncate 兼容 systemd append:。
本次仅"接水管",不调整参数。

## 风险与回滚

- **风险 1**:logrotate 配错 → stderr 不轮转(等同当前状态,不会变糟)
  → 手动 `ssh <server> 'rm /etc/logrotate.d/llm-gateway-go'` 即可
- **风险 2**:deploy 失败率上升 → 失败 warn 不 abort,deploy 主体成功
- **回滚**:本 commit 包含 deploy 脚本改动,回滚 = `git revert <commit>`,
  revert 后 deploy 不会调 install-logrotate,行为退化到 f6279fee4 之前(无影响)

## 关联

- 上游 commit: `f6279fee4` (2026-08-04 00:40) — install-logrotate.sh 初版
- 上游 commit: `805296786` (2026-08-04 00:25) — 245 紧急处理 + logrotate 真源文件
- 引用规则: rule 03 §6b (日志归档) + rule 11 §14 (持续验证) + rule 36 (变更归档)
- 配套文档: `docs/changelogs/2026-08-04-245-disk-cleanup-and-logrotate.md`
