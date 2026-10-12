# 252 PG17 硬盘 + 数据库长期防护方案

> 创建：2026-07-15，伴随 252 硬盘从 100% 紧急扩容到 200 GB 事件而加。
> 部署位置：aliyun server 252 (`<env:HOST_252_IP>:25022`, `root` 证书登录),PG 容器 `pg-252-pg17`。
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
| 5b | `pg17-index-bloat.sh` | 周日 04:15 | **索引死页**巡检（pgstatindex deleted_pages），`--report` 只读 + 超阈值告警；`--fix` 走 REINDEX CONCURRENTLY（2026-10-03 新增）|
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
/opt/scripts/pg17-index-bloat.sh                  # 索引死页巡检 (2026-10-03 新增)
/usr/local/bin/llmgw-source                 # cron env 加载包装
/etc/profile.d/llmgw-notify.sh             # 交互 shell env 加载
/etc/llmgw/notify.conf                     # webhook 凭证 (mode 600)
/var/log/pg17-disk-watch.log               # 采样 log
/var/log/pg17-disk-watch.alert.log         # 触发 alert log
/var/log/pg17-proactive-cleanup.log        # 预防性清理日志 (2026-09-06 新增)
/var/log/pg17-index-bloat.log            # 索引死页巡检日志 (2026-10-03 新增)
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

正典文件：`scripts/252-monitor/etc.cron.d.pg17` ⇒ 服务器 `/etc/cron.d/pg17`。

> ★ **部署是整文件覆盖，不是合并。** 模板少一条 = 下次部署把那条生产任务删掉。
> 改模板后**必须** diff 服务器现网复核：
>
> ```bash
> diff <(ssh 252 'cat /etc/cron.d/pg17') scripts/252-monitor/etc.cron.d.pg17
> ```
>
> 门 `scripts/ursmcheck/cron_registration_test.go` 只能守仓库侧
> （新增巡检脚本忘了在模板里注册）；「模板有、服务器没有」那一半抓不到，
> 只能靠上面这条人工 diff。2026-10-03 与 2026-10-04 各违反过一次。

当前 7 条任务（`## 7` 这段曾长期停留在 4 条的老版本，2026-10-04 才跟上）：

| 频率 | 任务 | 说明 |
|---|---|---|
| `*/10` | `pg17-disk-watch.sh` | 磁盘采样 + 告警推送 |
| `*/15` | `pg17-emergency-cleanup.sh --auto` | disk≥90% 自动 L2 |
| 周日 03:15 | `pg17-vacuum-bloat.sh` | VACUUM FULL 防 dead tuple 堆积 |
| 周日 04:40 | `pg17-index-bloat.sh --report` | 索引死页只读巡检 |
| 每月 1 号 02:30 | `pg17-drop-old-columnar-partitions.sh` | DROP 老列存分区 |
| 每日 02:00 | `pg17-proactive-empty-table-cleanup.sh` | 预防性清空表 |
| 每小时 :17 | `ursm-snapshot-health.sh` | 快照写入健康三态判据（0/1/2/3） |
| 每小时 :23 | `ursm-snapshot-payload-bloat.sh` | payload 膨胀，818 收益的回归防护（0/1/3） |

★ 两条 URSM 巡检**不能**写成 `>/dev/null 2>&1`：它们靠退出码分级报警，
全部丢弃等于没巡检。同理 `llmgw-source` 包装不能去掉（cron 不加载 `/etc/profile`）。
门 `TestCronFileActuallyHasTaskLines` 钉住这两点。

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

## 11. 2026-10-10 事故：巡检把被巡检对象压垮了

### 现场

154 网关持续返回 503，日志里刷满
`SQLSTATE 57P03 (the database system is not yet accepting connections)`。
顺着网关的 DSN 找到 252（`172.16.2.210`，正是 154 的 PG / Redis 落点）：

| 指标 | 现场值 | 正常 |
|---|---|---|
| load average (4 核) | **278 ~ 304** | < 4 |
| 进程总数 | **2915** | ~200 |
| `pg17-disk-watch.sh` 副本 | **310 份**，最早一份挂了 6.7 小时 | 1 份，跑完即退 |
| 僵死 `docker exec pg-252-pg17 psql` | **149 个** | 0 |
| 内存 | 13.8G / 15G，**swap 4095/4095 全占** | swap 空闲 |
| `du` 卡 D 状态 | 11 个 | 0 |
| 154 一小时 503 | **194 个** | 0 |

### 成因链

1. PG「半死」：容器还在 `listen`，但事务卡死。
2. `pg17-disk-watch.sh` 的 `de()` 直接 `docker exec ... psql`，
   **既无 `statement_timeout` 也无 `connect_timeout`** ⇒ psql 一直挂着。
   同一轮还跑 `du -s /var/lib/containers/storage/`（全量遍历容器存储）。
3. `etc.cron.d.pg17` 里它是 `*/10 * * * *`。上一轮永远不会结束，
   下一轮照常触发 ⇒ **进程只增不减**。
4. 310 份副本 ×（1 个 docker exec + 1 次全量 du）⇒ 内存耗尽 + IO 风暴。
5. PG 因此无法服务 ⇒ 154 网关 57P03 ⇒ 503。

⇒ **每 10 分钟一跑的巡检，在最需要它的时刻（PG 出问题）恰恰是最不能多跑的时刻。**

### 修复

两道闸，缺一不可：

| 闸 | 作用 | 没有它会怎样 |
|---|---|---|
| 单实例闸门（`flock -n`） | 最多一份副本在跑 | 副本只增不减（现场 310 份） |
| 查询 / 连接 / exec 超时 | 一份副本**一定会结束** | 那唯一的一份挂成永久僵尸（现场 149 个僵死 exec） |

只有闸门没有超时，等于把「310 份僵尸」缩成「1 份僵尸」——问题没解决。

- 闸门 **inline** 在每个脚本里，不 source 公共 helper：本目录按
  **整文件覆盖**部署到 `/opt/scripts/`，helper 没跟着传上去会让整个目录哑掉。
- 闸门**非阻塞**（`flock -n`）：阻塞式 flock 会让 cron 每轮排队等锁，
  每 10 分钟多一个等待进程——乱堆换成整齐地堆，一样会爆。
- 闸门**不可用时 fail-open**：拿不到锁就放行。反过来做（失败退出）会让
  监控整体静默，而静默的监控不会被任何人发现已经坏了。
  `pg17-disk-watch.sh` 的采样行会记 `gate=guarded|FAIL_OPEN`，让 fail-open 本身可见。
- `pg17-pg-availability-check.sh` 的闸门排在 **EXIT trap 之后**：
  它的 trap 负责写 `pg-availability.lastrun`（「巡检还在跑吗」的判据），
  抢锁失败直接 `exit 0` 会让 lastrun 不更新，运维会误判心跳已停。

### 回归门

```bash
bash tests/monitor_single_instance_gate_test.sh
```

静态部分（任何 runner，含 macOS）：每个 cron 调度的脚本都有非阻塞闸门、
闸门 fail-open、心跳闸门排在 trap 之后、disk-watch 带查询与连接超时。
行为部分（需要 `flock`，即 Linux/252）：8 份并发恰好 1 份进入主体、
被跳过的立即以 0 退出、上一轮结束后锁正常释放。

已接入 `verify.sh`。

## 12. 2026-10-11 批判式审计：上一版修复自己带来的问题

上一节（§11）的修复方向是对的，但**审计在自己的改动里找出 5 个问题**，
其中 3 个是我引入的、2 个是先前就存在但一直没暴露的。逐条记在这里，
因为它们的形态都不是"写得对就行"，而是"自己以为修好了"。

### 12.1 最重要的一个：**取不到值被当成一切正常**（假健康）

`de()` 失败时返回 `"0"`，而 §11 的每一条阈值判断都是「>= 阈值」。
于是 **0 永远不触发告警** ⇒ 「一条都没测到」与「一切正常」在判据里完全同形。

后果已经被现网证实：2026-10-10 全天 psql 全部超时，采样行却始终是
`level=ok`，`dead_tup_top3` 还显示成 `clean`。
**监控在完全瞎掉的状态下报了 8 小时健康** —— 而那 8 小时正是 154 被
Redis/PG 拖垮、却无人能从监控侧发现的时间窗。

修法：`de()` 每次调用把 ok/fail 记进文件（**不能记变量**——调用方一律
`x=$(de ...)`，那是子 shell，自增出不来），判定阶段只要有失败就落到
`gauge_unavailable`，并按本目录契约退出码 3；`top5` / `dead_tup_top3`
在查不到时写 `unavailable(量具故障)` 而不是 `clean`。

> 判据自证：同一个"伪容器"场景，修复前 `rc=0 / level=ok`，
> 修复后 `rc=3 / level=gauge_unavailable / gauge=9/9`。

### 12.2 我引入的回归：`du` 超时把指标**永久打瞎**

`du -s /var/lib/containers/storage/` 全量遍历容器存储。审计实测：
**在 252 上负载已回落之后，它仍然跑 280 秒未返回**。事故当天现场有 11 个
`du` 卡在 D 状态，它本就是两个负载源之一。

我上一轮给它加了 30s 上界，结果是这个原本能取到值的指标**永远取不到**
（采样行长期 `docker_storage=n/a`）。
一个"有界的失败"比一个"慢但正确"更糟：它让人以为这块已经有人管了。

修法：改用 `df -P` 取容器存储**所在文件系统**的用量（瞬时返回），
日志字段改名 `storage_fs_gb` 以免与"容器总量"混淆。
该指标只写日志、不参与任何阈值判断，故不丢告警能力。

### 12.3 我引入的漏洞：超时闸自己漏进程

`_llmgw_run_bounded` 的纯 bash 回退路径原本是 `kill -9 <直接子进程>`。
实测：`bash -c 'sleep 45; …'` 里那条 `sleep` **会活下来**，连调 3 次漏 3 个。

「超时的进程留下来继续跑」正是 §11 事故的形态——**超时这道闸若自己漏
进程，等于把事故原样复现一遍**。

修法：函数内 `set -m` 让后台作业自成进程组，`kill -9 -$pid` 整组回收。
方案取舍实测：`( set -m; exec … ) &` 子 shell 写法**仍漏 3 个**，故不用。

### 12.4 先前就存在：告警投递通路在生产上是**断的**

cooldown 的 fingerprint 里写了 `"${ts:0:7}"`，而 `ts` 在整个脚本里
**从未被赋值**（模板残留）。

- macOS bash 3.2：静默取空，fingerprint 退化，尚可用；
- **生产的 bash 4.4 上 `set -u` 直接报 `ts: unbound variable` 并退出 1**
  （已在 252 实测复现）。

后果：**任何一次真告警都会在最后一步崩掉，IM 永远推不出去**
（alert log 写了，但没人收到）。即 252 的告警投递在这次事故之前就是断的。

修法：改用 `$(date +%Y-%m)`；并给 `COOLDOWN_MIN` 的 case 加 `*)` 兜底
（它的结果直接进 `$((COOLDOWN_MIN * 60))`，一旦有新 level 漏了分支，
同样是 `set -u` 下崩在告警路径上）。`gauge_unavailable` 按 critical 推送。

### 12.5 先前就存在：bash 3.2 上一条告警都没触发反而会崩

`for a in "${alerts[@]}"` 在 bash 3.2 + `set -u` 下展开**空数组**会报
`alerts[@]: unbound variable` 并退出 —— 也就是"一条阈值都没触发"
（最常见的那种情况）反而让脚本当场死掉、连采样行都写不出来。
bash 4.4+ 才把空数组展开当合法，故 252 上一直没暴露。

修法：按 `${#alerts[@]}` 守卫，与本仓 deploy 侧
「空 ARGS 在 bash 3.2 下 unbound」同源。

### 12.6 回归门自己也踩的坑（记录下来防止重犯）

`tests/monitor_single_instance_gate_test.sh` 本轮加的第 9 节，
在自己的迭代里出了三次**"看着有门、其实没执行"**：

1. **无 flock 时直接 `exit 0`**，导致后面不依赖 flock 的断言全部不执行。
   改成置 `HAVE_FLOCK` 标志，只跳过 6/7/8。
2. **静态断言匹配到自己的注释**——注释里写了 `du -s /var/lib/containers…`
   和 `${ts:0:7}` 这两个反面教材，于是脚本改对了门却报 FAIL。
   改成只匹配**代码行**（`grep -vE '^[[:space:]]*#'`）。
3. **孤儿回收断言空转**：写成 `eval "$wd_fn"' 2 bash -c …'`，
   而 eval 不改变位置参数，函数里的 `local secs="$1"` 读到的是外层脚本的 `$1`，
   看门狗**根本没执行** ⇒ 恒为"零孤儿" ⇒ 这条断言永远绿。
   改为落到真实脚本文件、用真实参数调用，并加**探针自证**
   （先证明探针确实跑且返回 124，再看孤儿数）。

以上三个都不是理论风险，是本轮变异验证**当场抓出来的**。
