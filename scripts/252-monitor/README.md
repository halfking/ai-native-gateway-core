# 252 PG17 硬盘 + 数据库长期防护方案

> 创建：2026-07-15，伴随 252 硬盘从 100% 紧急扩容到 200 GB 事件而加。
> 部署位置：aliyun server 252 (`115.29.212.252:25022`, `root` 证书登录),PG 容器 `pg-252-pg17`。
> 脚本目录：`/opt/scripts/`（所有脚本 755 + 备份 `*.bak.YYYYMMDD-HHMMSS`）。

## 1. 现状快照（清理 + 部署后）

| 指标 | 状态 |
|---|---|
| 系统盘 `df -h /` | **66 GB / 197 GB (35%)**，122 GB 可用 |
| docker storage | 41 GB（含 31 GB PG overlay） |
| `llm_gateway` DB | **15 GB**（清理前 28 GB） |
| `columnar_internal.chunk`（元数据） | 9.84 GB |
| `request_logs_hot` 单表风险 | 3.5 GB（JSONB TOAST，单表最大） |
| `model_probe_runs_2026_07` 分区（已止血） | 88 KB（涨速从 8 GB/h → 0） |

## 2. 六层防护脚本与 cron 频率

| # | 脚本 | 频率 | 行为 |
|---|---|---|---|
| 1 | `pg17-disk-watch.sh` | `*/10 * * * *` | 13 项指标 + 7 项告警阈值 + cooldown + webhook 推送 |
| 2 | `pg17-proactive-empty-table-cleanup.sh` | 每日 02:00 | **预防性空表清理**：DROP n_live_tup=0 AND COUNT(*)=0 且 ≥100MB 的表（零风险，2026-09-06 新增） |
| 3 | `pg17-emergency-cleanup.sh --auto` | `*/15 * * * *` | `disk >= 90%` 自动 L2（**中风险**：DROP 空表 + VACUUM FULL，已修复默认分区验证逻辑） |
| 4 | `pg17-vacuum-bloat.sh` | 周日 03:15 | 周级 VACUUM FULL bloat > 30% / size > 256 MB 表 |
| 5 | `pg17-drop-old-columnar-partitions.sh` | 每月 1 号 02:30 | DROP 老月分区 + VACUUM FULL 列存元数据 |
| 6 | `notify.sh` | 被动调用 | Feishu (HMAC-SHA256 签名) / DingTalk / Telegram / Generic |

部署位置：

```
/etc/cron.d/pg17                           # 5 个 cron + 注释
/opt/scripts/notify.sh                     # webhook helper (含飞书签名)
/opt/scripts/pg17-disk-watch.sh
/opt/scripts/pg17-proactive-empty-table-cleanup.sh  # 预防性清理 (2026-09-06 新增)
/opt/scripts/pg17-drop-old-columnar-partitions.sh
/opt/scripts/pg17-emergency-cleanup.sh
/opt/scripts/pg17-vacuum-bloat.sh
/usr/local/bin/llmgw-source                 # cron env 加载包装
/etc/profile.d/llmgw-notify.sh             # 交互 shell env 加载
/etc/llmgw/notify.conf                     # webhook 凭证 (mode 600)
/var/log/pg17-disk-watch.log               # 采样 log
/var/log/pg17-disk-watch.alert.log         # 触发 alert log
/var/log/pg17-proactive-cleanup.log        # 预防性清理日志 (2026-09-06 新增)
/var/log/llmgw-notify.log                  # 推送记录
/var/tmp/pg17-disk-watch.cooldown          # cooldown 状态 (防噪声)
/var/tmp/pg17-proactive-cleanup.cooldown   # 预防性清理 cooldown (2026-09-06 新增)
```

## 3. 监控阈值（可被 `/etc/llmgw/pg17.conf` 覆盖）

| 指标 | warn | critical | 备注 |
|---|---|---|---|
| 系统盘 `df /` 已用% | ≥ 60% | ≥ 75% | 留出"还能跑清理"的时间窗 |
| 系统盘剩余 GB | ≤ 30 GB | — | 200G 系统的 15% |
| `llm_gateway` DB | ≥ 20 GB | ≥ 28 GB | 之前一次触发时 22 GB |
| `request_logs_hot`（单表风险） | ≥ 2 GB | ≥ 3 GB | JSONB TOAST 风险 |
| `model_probe_runs_hot` | ≥ 500 MB | — | 14 天 TTL 满约 1 GB |
| `columnar_internal.chunk`（元数据） | ≥ 15 GB | — | DROP 列存表后未 VACUUM FULL 才会膨胀 |
| `pg_wal` 拥堵 | ≥ 3 GB | — | `max_wal_size` 默认 2 GB |

**Cooldown 防噪声**：

| Level | 默认冷却 | 自定义环境变量 |
|---|---|---|
| critical | 60 min | `PG17_CRIT_COOLDOWN_MIN` |
| warning | 30 min | `PG17_WARN_COOLDOWN_MIN` |
| info | 1440 min（几乎不推） | `PG17_INFO_COOLDOWN_MIN` |

同 fingerprint 在 cooldown 内只在 `/var/log/pg17-disk-watch.alert.log` 写，不推 IM。

## 4. Webhook 配置（env 加载模式）

### 4.1 配置文件 `/etc/llmgw/notify.conf` (mode 600, root:root)

```bash
WEBHOOK_TYPE=feishu                  # feishu | dingtalk | telegram | generic
FEISHU_URL=https://open.feishu.cn/open-apis/bot/v2/hook/<TOKEN>
FEISHU_SECRET=<signing_key>          # 加签校验模式必填,免签可省
```

实际填写示例（**仅做配置格式参考,真实值在生产服务器上**）：

```bash
# 当前 252 上配置（飞书"股龙"群,加签校验）
WEBHOOK_TYPE=feishu
FEISHU_URL=https://open.feishu.cn/open-apis/bot/v2/hook/<YOUR_TOKEN>
FEISHU_SECRET=<YOUR_SIGNING_KEY>
```

### 4.2 加载方式（三选一）

| 场景 | 方式 | 实现位置 |
|---|---|---|
| 交互 shell 登录 | 自动加载 | `/etc/profile.d/llmgw-notify.sh` (已部署) |
| cron / systemd | 自动包装 | `/usr/local/bin/llmgw-source` (已部署,所有 cron 走它) |
| ad-hoc 调用 | 显式 source | `source /etc/llmgw/notify.conf && /opt/scripts/notify.sh ...` |

### 4.3 飞书签名校验流程（HMAC-SHA256）

```bash
ts=$(date +%s)
s2s="${ts}\n${FEISHU_SECRET}"
sign=$(python3 -c "import hmac,hashlib,base64,sys; s=sys.argv[1].encode(); m=sys.argv[2].encode(); print(base64.b64encode(hmac.new(s,m,hashlib.sha256).digest()).decode())" "$FEISHU_SECRET" "$s2s")
URL="${FEISHU_URL}?timestamp=${ts}&sign=${sign}"
# POST {"msg_type":"text","content":{"text":"..."}}
```

## 5. 三级 emergency-cleanup

| Level | 行为 | 风险 | 自动触发？ |
|---|---|---|---|
| L1 | DROP n_live_tup=0 AND size≥100MB 的表 + 默认分区 | **零风险** | ✅（disk≥90% 自动）|
| L2 | L1 + VACUUM FULL 列存 3 张元数据 + bloat>50% 业务表 | **低**：VACUUM FULL 锁表 5-30 分钟 | ✅（disk≥90% 自动）|
| L3 | L1 + L2 + TRUNCATE 大清零表（casdoor token/record）| **中**：影响审计 | ❌ 需要 `--yes` 双重确认 |

手动运行：

```bash
/opt/scripts/pg17-emergency-cleanup.sh --level L1 --yes
/opt/scripts/pg17-emergency-cleanup.sh --level L2 --yes
/opt/scripts/pg17-emergency-cleanup.sh --level L3 --yes
```

## 6. 飞书机器人配置示例（生产）

> ⚠️ webhook URL / 签名密钥属于 secret,真实值只放 252 `/etc/llmgw/notify.conf`,不要进 git。
> 飞书群机器人配置流程（飞书客户端）：
> 1. 群聊天 → 设置 → 群机器人 → 添加机器人 → 自定义机器人
> 2. 选"加签校验"获取 secret
> 3. webhook 复制到 `/etc/llmgw/notify.conf`
> 4. 通知脚本会自动用 timestamp+signature 计算 `?timestamp=...&sign=...` 拼到 URL

测试推送：
```bash
/opt/scripts/notify.sh -l info -t '252 防护测试' -b '请确认股龙群可收到'
```

## 7. cron 配置

`/etc/cron.d/pg17`（已部署）：

```cron
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
MAILTO=""

# === 高频监控 ===
*/10 * * * * root llmgw-source /opt/scripts/pg17-disk-watch.sh >/dev/null 2>&1
*/15 * * * * root llmgw-source /opt/scripts/pg17-emergency-cleanup.sh --auto >/dev/null 2>&1

# === 周级 bloat 清理 ===
15 3 * * 0 root llmgw-source /opt/scripts/pg17-vacuum-bloat.sh >/dev/null 2>&1

# === 月度列存分区清理 ===
30 2 1 * * root llmgw-source /opt/scripts/pg17-drop-old-columnar-partitions.sh >/dev/null 2>&1
```

## 8. 验证步骤（已执行）

```bash
# 1. 语法检查
for f in /opt/scripts/*.sh; do bash -n "$f"; done
# 2. conf 模式
/opt/scripts/notify.sh -l info -t "测试 conf" -b "text"
# 3. cron env -i 包装模式
env -i PATH=/usr/bin:/bin /usr/local/bin/llmgw-source /opt/scripts/pg17-disk-watch.sh
# 4. emergency L1 测试
/opt/scripts/pg17-emergency-cleanup.sh --level L1 --yes
# 5. cron 注册
crontab -l  # 应看到 llmgw-source * /opt/scripts/pg17-disk-watch.sh 等 4 行
```

## 9. 风险与已知盲区

| 项 | 说明 | 缓解 |
|---|---|---|
| `request_logs_hot` 单表 3.5 GB 是真实风险 | JSONB 200 KB/行,7 天内可涨到 15 GB | 监控 critical 阈值 3 GB 已生效,业务侧需评估是否截断 `request_body` 到前 1KB |
| `request_logs_bodies_*` 表都是 0 行 | 可能 promote 没工作 / 配置错 | `promote_request_logs_bodies_hot_to_partition` 函数存在但 154 上未触发（需要 24h+ 老数据） |
| 154/245 v1024 binary 仍是旧版 | 模型 promote 函数被 PG 端替换空操作堵住 | 仍等下次 deploy 154 升级 |
| 飞书 cooldown 在 critical 60min | 如出现 critical 1h 内不再重复推 | 实际运维需要可能调小或临时 disable |
| `webhook secret` 仍在 `/etc/llmgw/notify.conf` | 如需更严:`chattr +i` 或更安全方式保管 | 视团队偏好, secret 不在 git 中已是底线 |

## 10. 加挂第二种通知通道（可选）

如钉钉 / Telegram 也能同时收到,可在 notify.conf 加：

```bash
WEBHOOK_TYPE=feishu,dingtalk      # 暂不支持多 channel,需改 notify.sh
```

或同时复制脚本 + 配置做多群发（每个 channel 各自一行 cron）。
