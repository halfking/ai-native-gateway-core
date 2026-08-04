# 2026-08-04 — 154 stderr 走 journald, 用 drop-in 限制 journal 大小

## 变更摘要

实测发现 154 上的 `llm-gateway-go.service` 实际是 `StandardOutput=journal` / `StandardError=inherit` (systemd 默认),stderr 不写 `/var/log/`,而是写 systemd journal。**之前装的 logrotate 完全无效**(管不到 journal 文件),154 的 `/var/log/journal/` 已经 4.0G。

修正方案(老板 2026-08-04 决策):用 systemd-journald **drop-in** `/etc/systemd/journald.conf.d/llm-gateway-go.conf` 限制 journal 总大小 200M + 保留 14day。drop-in 是 systemd 标准机制,主配置不动,卸载 = rm 一个文件。

## 触发原因

| 项 | 现状 (ssh 154 实测) |
|---|---|
| **systemd unit** | `StandardOutput=journal` / `StandardError=inherit` (systemd 默认) |
| **仓里 unit 假设** | `deploy/llm-gateway-go.service` 有 `StandardOutput/StandardError=append:` — **跟 154 实际不一致** |
| **stderr 落点** | `/var/log/journal/` (4.0G,接近 systemd 默认 ~4G 上限) |
| **logrotate 范围** | logrotate 管不到 journal,只管 `/var/log/` 下文件 |
| **仓内现有 /etc/logrotate.d/llm-gateway-go** | **不存在** (154 没装过) |
| **journald.conf 实际配置** | systemd 默认值 (无 SystemMaxUse / MaxRetentionSec 限制) |
| **磁盘** | 51% 已用,健康 (但 journal 再涨就会撑爆) |

## 改动清单

| 文件 | 改动 | 行为 |
|------|------|------|
| `scripts/configure-journald.sh` | 新增 | drop-in 模式部署 journald 限制。子命令 install/uninstall/status/verify,幂等(内容相同 skip,不同备份 .bak 后覆盖),失败 warn 不 abort。 |
| `deploy/journald-conf-snippet.conf` | 新增 | `[Journal]` 段 + `SystemMaxUse=200M` + `MaxRetentionSec=14day` + `MaxFileSec=1month`(drop-in 内容,deploy 时 cp 到 `/etc/systemd/journald.conf.d/llm-gateway-go.conf`) |
| `scripts/deploy-seamless.sh` | 改 [9.6/9] | 加 `systemctl show llm-gateway-go --property=StandardOutput,StandardError` 检测分支 — `append:` 走 install-logrotate.sh,`journal/inherit` 走 configure-journald.sh |
| `deploy-154.sh` | 改 [安装 logrotate] 段 | 同上分支逻辑 (适配 154 走 journal 模式) |

## 集成点设计 (deploy 通用)

```
systemctl show llm-gateway-go --property=StandardOutput,StandardError
       │
       ├─ append:/*  或  */append:*  →  install-logrotate.sh (245 / 改 unit 后 154)
       │
       ├─ journal/*  /*:journal  /*:inherit  →  configure-journald.sh (154 现状)
       │
       └─ 其它  →  warn skip (无标准轮转路径)
```

## 154 实测结果 (deploy + 验证)

| 步骤 | 命令 | 结果 |
|------|------|------|
| 部署前备案 | `scp /tmp/lg154-backup-script.sh + ssh` | OK — `/tmp/lg154-backup-20260804-094256/` 含 binary (sha=51014d4...) / journald.conf.orig / service.orig / service.d.tar.gz |
| 传 snippet + 脚本 | `scp` | OK |
| install | `bash configure-journald.sh install /opt/lg154-deploy/journald-conf-snippet.conf` | OK — drop-in 创建 + systemd-journald restart 成功 (MainPID 355 → 10655) |
| **journal 实际大小** | `journalctl --disk-usage` | **4.0G → 248M** (释放 3.7G,稍超 200M 是压缩前/索引文件) |
| 主配置未被改 | `md5sum /etc/systemd/journald.conf` | `61493a9d3062a5d0f9a2e7297ed9497d` (跟部署前备案一致) ✓ |
| systemd-journald active | `systemctl is-active systemd-journald` | active (新 MainPID 10655) ✓ |
| llm-gateway-go active | `systemctl is-active llm-gateway-go` | active ✓ (restart systemd-journald 不影响 llm-gateway-go) |
| journal 仍可读 | `journalctl -u llm-gateway-go -n 5` | OK,最新日志 09:59:52 ✓ |
| 业务健康 (L4) | `curl https://llm.kxpms.cn/health` | 302 → /admin/login (正常) ✓ |

## 设计决策

### 1. drop-in 模式 vs 改主文件

| 维度 | drop-in (`/etc/systemd/journald.conf.d/`) | 直接改 `/etc/systemd/journald.conf` |
|------|------------------------------------------|-------------------------------------|
| 主文件 | 不动 | 改 |
| 卸载 | `rm` drop-in | 需备份 + 还原 |
| 跨 distro | 通用 (el7/debian/ubuntu 都支持) | 不通用 |
| 优先级 | drop-in > 主文件 | n/a |
| systemd 标准 | ✓ (官方推荐) | 不是 |
| **选用** | **✓** | ✗ |

### 2. 为什么不改成 `append:` 模式 (跟 245 / 仓里 unit 一致)

| 维度 | 改 unit 到 append: | 保持 journal + drop-in |
|------|-------------------|------------------------|
| 改动范围 | systemd unit + restart (停机 5-25s) + 旧 journal 4G 不迁移 | 只加 drop-in + restart journald |
| 风险 | 改生产 unit,restart 时服务停 | 不动 unit,无服务中断 |
| 历史兼容 | 仓里 deploy/llm-gateway-go.service 已经是 append: 版 (245 用) | 154 现实是 journal, 兼容现实 |
| **选用** | ✗ (大改动) | **✓** (最小改动) |

**未来动作**:长期可以把 154 的 unit 改成跟 245 一致(`append:` + logrotate),但那是独立 task,本次保持现状。

### 3. 为啥 install-logrotate.sh 装在 154 不生效

`install-logrotate.sh` 装 `/etc/logrotate.d/llm-gateway-go`,管的是 `/var/log/llm-gateway-go/gateway.stderr.log`。但 154 上 stderr **不在文件,在 journal**。logrotate 即使装上,`missingok` 跳过 — 等于没装。

**这是本次的副产物**:`install-logrotate.sh` 不动 (245 + 未来改 unit 的 154 用),但 deploy 脚本**先 systemctl show 检测**,自动选 install-logrotate.sh 还是 configure-journald.sh。

## 备份 / 回滚

部署前备案完整备份(在 `/tmp/lg154-backup-20260804-094256/`,本地副本 `/tmp/lg154-backup-20260804-094256/`):

```
llm-gateway-go.binary.51014d4180384c93cf3e21591b827c9959779a0ee33691cfa89d2750205247fc  (43MB)
journald.conf.orig                                                           (983B, 部署前 md5 一致)
llm-gateway-go.service.orig                                                  (373B)
llm-gateway-go.service.d.tar.gz                                              (1KB)
```

**回滚方案**:

```bash
# 1. 卸载 drop-in (3 行)
ssh root@154 'bash /opt/lg154-deploy/configure-journald.sh uninstall'

# 2. 如需还原主配置 (一般不需要, 因主配置未被改)
ssh root@154 'cp /tmp/lg154-backup-20260804-094256/journald.conf.orig /etc/systemd/journald.conf'

# 3. 验证 journal 又能无限增长 (但 systemd 默认 4G 上限保护)
ssh root@154 'systemctl show systemd-journald --property=ActiveState'
```

## 风险

- **风险 1**:`systemd-journald` restart 时 ~1-2s 不可用 — 已实测,服务无影响
- **风险 2**:drop-in 文件被手工删除 → journal 又会无限增长 — `configure-journald.sh status` 可检查
- **风险 3**:未来 el7 升级到 el8/9 时 drop-in 路径变化 — 不影响(所有 systemd 都支持)

## 关键决策点 (老板 2026-08-04)

| 问题 | 决策 |
|------|------|
| 154 stderr 在 journal, 怎么管? | 改 journald.conf drop-in (不动 unit) |
| journal 上限多大? | **200M / 14day** |
| 是否同时改 unit 到 append:? | 否,本次最小改动,未来再统一 |

## 关联

- 上一 commit: `f6279fee4` (install-logrotate.sh 初版 + 245 logrotate) — 本次是其延伸(154 journal 模式)
- 引用规则: rule 03 §6b (日志归档) + rule 03 §7.0 (部署前备案) + rule 11 §14 (持续验证) + rule 36 (变更归档)
- 配套文档: `docs/changelogs/2026-08-04-245-disk-cleanup-and-logrotate.md`