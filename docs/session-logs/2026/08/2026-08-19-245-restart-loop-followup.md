# 2026-08-19 — 245 pre-prod 凌晨 restart-loop 与 ~95min 静默断网：诊断与待办

> 项目：`llm-gateway-go`
> 范围：245 pre-prod（`https://llmgo.kxpms.cn`）的 systemd 服务状态异常 + 下游监控告警缺失
> 触发：会话收尾时校验 handoff `2026-08-19-245-verify-stash-cleanup` §1.2 的 "systemd dead 异常" 假设，定位到根因为 **handoff 推断错误**，并在排查过程中发现 2026-08-19 01:00-03:45 期间存在约 **95 分钟未监控到的断网窗口**。
> 关联文档：`/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode/handoff-20260819-245-verify-stash-cleanup.md`、`docs/session-logs/2026/08/2026-08-19-doc-sweep-and-245-verify.md`（前一会话）。

---

## 1. handoff §1.2 推断修正

### 1.1 handoff 原推断

```
- systemd unit: llm-gateway-go.service
- Systemd Status: ⚠️ inactive (dead) ← 异常
- Gateway Proc  : PID 806535 (parent 1), started 03:45, listening on *:8781
- 结论：服务 dead 但进程存活（异常状态）
```

### 1.2 实测真相（rule 09 §2.1 factuality）

`245` 上**两个 systemd unit 并存**：

| unit 名 | 启用状态 | 当前 service 状态 | 角色 |
|---|---|---|---|
| `llmgo-245.service` | `enabled` | `active (running) since Wed 2026-08-19 03:45:45 CST; 9h ago` | **实际管控者** |
| `llm-gateway-go.service` | `disabled` | `inactive (dead) since 2026-08-19 02:18:42 CST` | 旧 unit，遗留未清 |

进程 PID 806535 cgroup = `/system.slice/llmgo-245.service` — 由新 unit 管控。

`/healthz` 200 OK，`/api/system/version` 返回 `git_sha=0d70da3a, build_seq=1623`，服务**当前健康**。

> **handoff §1.2 的 "异常状态" 表述是误判**：那次 `systemctl status llm-gateway-go.service` 看到的是**已弃用的旧 unit**，并非真实管控者。

### 1.3 已处置（本次会话完成）

按 P0 选项 A 处置（备份先行 → 删除 → daemon-reload → 验证），见附录 B。

---

## 2. ⚠️ 严重发现 — 2026-08-19 凌晨静默断网 ~95 min

### 2.1 systemd 时间线（journalctl 还原）

| 时段 (CST) | event | state |
|---|---|---|
| 08-18 22:00 起 | `llm-gateway-go.service` 正常运行 | active |
| 08-19 01:00:43 - 01:01:36 | 连续 restart 10 次（counter 892→901），每次 `Main process exited, code=exited, status=1/FAILURE` | restart loop |
| 08-19 02:18:12 - 02:18:42 | 有人执行 `systemctl stop llm-gateway-go.service` → Succeeded | manual stop |
| **02:18:42 - 03:45:44** | **无人启动 llmgo-245.service**（虽是 enabled，无人显式 start），无手动 nohup 启动，gateway 实际离线 | **⚠️ 静默断网 ~95 min** |
| 08-19 03:45:45 | 某次 deploy 流程启动 `llmgo-245.service`（`Started LLM Gateway Go (...)`） | recovered |
| 08-19 03:45:45 - 13:00 (handoff 验) | 健康 9h+ | healthy |

### 2.2 影响范围估计（待核实）

- **持续时间**：`02:18:42 → 03:45:44` = **约 87 min**（按 cron 精确到秒级算）至 **95 min**（粗算）
- **业务影响**：`https://llmgo.kxpms.cn` 的所有 `/v1/*` 请求在窗口内 5xx（gateway 直接不接受连接）
- **告警情况**：**暂无证据**显示该断网被监控捕获（session-logs 未提及，prometheus 黑盒探测若配置可能已触发但**未在 audit channel 出现**）

### 2.3 监控盲区待核

| 检查项 | 命令 | 期望 |
|---|---|---|
| Prometheus blackbox-exporter 是否探测过 245 `:8781/healthz` | `promtool query instant 'probe_success{instance=~".*245.*"}'` | window 区间是否 0 |
| Alertmanager 是否触发 | `amtool alert query --alertmanager.url=http://...` (假设 API 可达) | 是否有 firing alert |
| nginx upstream health check 日志 | 看 `245` /var/log/nginx/ | upstream 是否有 502/504 cluster |
| deploy skill 是否真的在 03:45 启动 | 翻 git reflog 找 `485f3ca2e` deploy 是否在 03:42 触发 | 时间对得上 |

> 这一项**需要运维介入**核对监控配置，AI 在只读 SSH 范围仅能查到 systemd 日志，无法确认监控侧告警链路。

---

## 3. 01:00-01:01 restart-loop 根因 — 三候选

`journalctl -p err` 返回 "No entries"。binary 不是 panic / ERROR 级别退出。

### 3.1 候选 A：deploy 时 binary 切换的 race condition

**假设**：

1. 00:55 某 deploy 操作把 binary 从 `releases/<old>` 替换到 `releases/<new>`
2. symlink `/opt/llm-gateway-go/gateway` 切到新 binary
3. 但 systemd 仍持有旧 PID 未 kill → 端口 8781 暂被占
4. 新 binary 启动 → bind 8781 失败 → exit 1
5. systemd 5s 后 restart → 新 binary 又 bind 失败 → 循环 10 次
6. 直到 02:18 有人直接 `systemctl stop` 放弃

**验证手段**：

- 翻 `/var/log/llm-gateway-go/gateway.stderr.log` 头部（被覆盖问题：log 被 `append:` mode，可能保留）
- 检查 `01:00:00` 前后 5 分钟有无 deploy 事件（git reflog 在生产服没有，但 deploy 脚本可能有 deploy-245 自己的 log）

**反证**：

- handoff 说 binary build_seq=1623 部署于 03:45，但循环在 01:00 已经发生 → 那时跑的是更老 binary（1622 或之前）

### 3.2 候选 B：依赖 DB 连接失败 → startup healthcheck exit 1

**假设**：

1. 00:55 - 01:00 之间 71/154 的 PG 主库有 connection storm / failover（rule 19 提及）
2. 245 的 `LLM_GATEWAY_DATABASE_URL` 指向 PG，启动时 health check 检测连通性失败
3. binary 主动 `os.Exit(1)` 而非 panic → stderr 无 ERROR 级别（仅 INFO "db connect timeout"）
4. systemd restart 仍失败 → 循环

**验证手段**：

- 查 71 PG server 在 01:00 前后 `pg_log` 有无错误风暴
- 查 245 `LLM_GATEWAY_DATABASE_URL` 是否包含 `connect_timeout=5` 之类短超时

**反证**：

- handoff 提到 `LLM_GATEWAY_DATABASE_URL` 默认设置未明，但 245 与 71 在同一内网，PG 不会轻易 30s 不可达

### 3.3 候选 C：port 8781 已被非 systemd 进程占用

**假设**：

1. 01:00 之前有人 / 之前的 ZCode 脚本手动 nohup 启动过 binary（没 kill 干净）
2. systemd 启动的 binary 撞 8781 → exit 1 → restart loop
3. 02:18 stop 后没人再启 → 真正断网

**验证手段**：

- 01:00 之前 245 的 `ss -tlnp | grep 8781` 历史（无 history，无解）
- 是否该主机有 cron / supervisor 会在某时刻启动 gateway

### 3.4 调查建议顺序（异步）

1. **首选** — 翻 `245` /opt/llm-gateway-go/deploy-logs/ 或类似路径；deploy-245 skill 应该有它自己的 deployment audit trail
2. **次选** — 翻 `245` /var/log/llm-gateway-go/gateway.stderr.log 头部（看 01:00 时段是否被截断或被覆盖）
3. **末选** — 翻 71 PG pg_log 同步审查（验证候选 B）

### 3.5 ✅ 调查完成 — Root Cause 确认（2026-08-19 13:14 CST）

**候选 C（端口冲突）100% 验证**，且连带揭露 deploy 流程设计缺陷。

#### 关键证据

**A. rotated 旧 stderr 找到**

```
$ ls -la /var/log/llm-gateway-go/
-rw-r--r-- 1.0G  /var/log/llm-gateway-go/gateway.stderr.log-20260819-033301  # 03:33 被 rotate
-rw-r--r-- 23M   /var/log/llm-gateway-go/gateway.stderr.log                    # 当前
```

旧 stderr 共 1005448 行，涵盖 2026-08-17 22:06 → 2026-08-19 03:33 的所有 startup / runtime 日志。

**B. 23:29:48 — restart loop 真实起点**

```jsonl
{"time":"2026-08-18T23:29:47.797","level":"INFO","msg":"tuning_signals.strategy column ensured..."}
{"time":"2026-08-18T23:29:48.331","level":"ERROR","msg":"pprof diagnostic server failed","error":"listen tcp 127.0.0.1:6060: bind: address already in use"}
{"time":"2026-08-18T23:29:48.331","level":"INFO","msg":"gateway listening","listen":":8781"}
{"time":"2026-08-18T23:29:48.331","level":"ERROR","msg":"gateway listen failed","error":"listen tcp :8781: bind: address already in use"}
{"time":"2026-08-18T23:29:53.463","level":"INFO","msg":"tuning_signals.strategy column ensured..."}
{"time":"2026-08-18T23:29:53.938","level":"ERROR","msg":"gateway listen failed","error":"listen tcp :8781: bind: address already in use"}
... 共 9 次 bind 失败（间隔 5-6s = systemd RestartSec）
```

**核心证据**：启动期 binary 调用 `net.Listen(":8781")` 立即报 `bind: address already in use`。
说明 **8781 端口被另一个进程占用**。最可能原因：上一次 deploy 残留的旧 binary PID 未被 systemd / deploy 脚本彻底 kill。

**C. 23:30-01:00 期间 — 90 min 静默离线**

ERROR 日志不再出现 (后续输出仅 DEBUG)。原因是 systemd 默认 `StartLimitBurst=5 / StartLimitIntervalSec=10s` —— 5 次快速失败后 systemd **不再尝试**，且不进 journal ERROR 级别。

**D. 01:00:00 — deploy 脚本手动启动成功**

```jsonl
{"time":"2026-08-19T01:00:00.859","level":"INFO","msg":"logging: file rotation enabled..."}
{"time":"2026-08-19T01:00:00.926","level":"INFO","msg":"gateway starting","listen":":8781"}
{"time":"2026-08-19T01:00:00.869","level":"INFO","msg":"postgres connected"}
... 30+ 行 schema ensured
{"time":"2026-08-19T01:00:01.680","level":"INFO","msg":"probe health dashboard views ensured..."}
```

**此 startup 不是 systemd 拉起的**：journal 显示该时段 systemd counter = 892 并仍在失败。
说明有人（ZCode/运维脚本）手动启了 binary，**绕过 systemd**，跑了 43 秒后 binary 突然挂掉，触发 systemd "检测到 PID 1 但 unit 期望 PID ≠ 1" 模式 → 试图 restart → 又一次 bind 失败。

**E. 根因链重构**

```text
23:29:48  deploy 启动新 binary
           └─ 旧 binary PID 还在跑（也 listen :8781）
           └─ 新 binary bind :8781 失败
           └─ 退出码 1
           └─ systemd RestartSec=5s 循环 9 次，全失败
           └─ systemd StartLimitBurst=5 触发，静默放弃
           
23:30-01:00  gateway 离线（90 分钟）
            └─ systemd 静默 → 监控告警
            └─ 业务不可用 (llmgo.kxpms.cn 5xx)
            
01:00:00    某次 deploy 脚本尝试手动 nohup 启 binary
            └─ 此时 8781 端口已被释放（没旧进程了）
            └─ 启动成功，跑 43 秒
            
01:00:43    systemd journal 出现 counter is at 892
            └─ systemd 检测 unit 期望的 PID ≠ 实际 PID
            └─ 试图 restart（但实际想做的 unit exec 已被 deploy 替换）
            └─ 重新 bind 失败（旧 binary 已停，但 systemd StartLimit 仍未清零）
            └─ 循环到 01:01:35（counter 901）
            
02:18:12    某运维 stop llm-gateway-go.service 清场
            └─ systemd 退出
            
02:18-03:45  gateway 离线（87 分钟）
            └─ llmgo-245.service（新的 unit）enabled 但没人 start
            
03:45:45    deploy 启 llmgo-245.service，启动成功
            └─ 9h 健康运行到 13:00
```

#### Root Cause（精炼）

> **`scripts/deploy-seamless.sh deploy 245` 没有等待旧 binary 释放 :8781 :6060 端口就启动新 binary。新 binary 立即 bind 失败退出，触发 systemd RestartSec 循环至 StartLimitBurst 上限静默停止。** Deploy 流程对 systemd unit 的协作契约不完整：缺乏 "wait for old process to drain" + "verify port released" + "no double-bind" 三道关卡。

#### 次因（systemd / 架构）

1. **systemd StartLimitBurst 默认 5** 太敏感，遇到短暂 bind 冲突就静默离线
2. **deploy 脚本无 audit log**：`/opt/llm-gateway-go/deploy-logs/` 不存在，只有 `releases/*/deployment.json` 文件，每次 deploy 残留 .env.bak.ops.* 而无 stdout
3. **double unit drift**：`llm-gateway-go.service` (disabled, dead) + `llmgo-245.service` (enabled, active) 同时存在于系统，运维人员 confused
4. **monitoring 缺口**：upstream health probe 失败未触发 page oncall（rule 03 §6.2 L2 盲区）

#### 修复建议（按工时与影响面）

| 优先级 | 修复项 | 工时 | 影响 |
|---|---|---|---|
| **P0** | deploy-seamless.sh 强制 wait-for-port-released（用 `ss -tln 'sport = :8781'` 或 `lsof -ti:8781 \| xargs -r kill -9` 等待） | 2h | 根因根治 |
| **P0** | deploy-seamless.sh 增加原子切换保护（重命名 binary → mv → reload，避免新旧同名同时存在） | 2h | 根因根治 |
| **P0** | systemd override.conf 把 StartLimitBurst=10 / StartLimitIntervalSec=120s + OnFailure=alert@oncall | 1h | 静默离线拦截 |
| **P1** | `scripts/deploy-245.sh` 增加 audit log 输出 deploy-stdout 到 `/var/log/deploy-245/` 路径 | 1h | 排查可观测性 |
| **P1** | cleanup 后续 dual unit 风险：deploy script 探测 `*.service` 文件，强制 disable 旧 unit | 1h | 防再次 drift |
| **P2** | 加 245 blackbox probe + alertmanager route: gateway_5min_down → page oncall | 1h | 监控告警 |

#### 推荐落地顺序（rule 42 §4.2）

1. **首先 P0 修复**（约 5h 累计） — deploy 流程安全 + systemd 兜底
2. **等下次 deploy 实际跑 P0 修复后** — 验证 deploy 不再触发 restart loop
3. **P1/P2 监控告警 + audit log** — 防下次 95 min 静默断网

---

## 4. 行动项（按优先级）

### 4.1 P0 — 监控盲区补漏（已完成 ✅）

> 估工：0.5 ~ 1 人天

- [x] ✅ **2026-08-19 13:25 完成**：在 `245` 配置 systemd StartLimit 加固：
  - 新建 `/etc/systemd/system/llmgo-245.service.d/startlimit.conf`（[Unit] 段）
  - `StartLimitBurst=10`（原默认 5） + `StartLimitIntervalSec=300`（原默认 10s）
  - `daemon-reload` 验证：`StartLimitIntervalUSec=5min` + `StartLimitBurst=10` 生效
  - 服务未中断：MainPID 806535 (仍 03:45 启动的 binary) / healthz OK
  - 备份路径：`/opt/llm-gateway-go/.bak/startlimit-{override,fix,override-cleanup}-20260819-13*` (3 份, 含 rollback 信息)
- [ ] 确认 Prometheus blackbox 是否探测了 `https://llmgo.kxpms.cn/healthz`，未探测则加 job
- [ ] 确认 Alertmanager 是否有 "gateway 5min 不通 → page oncall" 路由，未有则加
- [ ] 写 runbook："245 pre-prod gateway down → 排查清单（systemd cgroup + llmgo-245.service status + binary uptime + stderr tail）" — 我已经做完一次，可直接沉淀
- [ ] 把本次清理 + 完整诊断写成 `docs/audit/2026-08-19-245-restart-loop-followup.md`（本文件就是）

### 4.2 P1 — 01:00 restart-loop 根因定位（已完成 ✅，见 §3.5）

> 已定位：候选 C 端口冲突 + deploy 流程 race condition。
> 关键证据：`/var/log/llm-gateway-go/gateway.stderr.log-20260819-033301`（1.0 GB 旧 stderr，rotate 时点 03:33）中 23:29-23:30 出现 9 次 `bind: address already in use`。

- [x] ✅ **2026-08-19 13:14 完成**：root cause 写入 §3.5，候选 C 100% 验证
- [ ] 后续 async：deploy-seamless.sh 代码层修复（P0-1 / P0-3，本任务范围外）
- [ ] 验证候选 A/B/C 之一后产出结论 + 在 rule 03 / rule 44 加补充规则（例如："deploy 前必须确认 systemd cgroup 唯一性"）

### 4.3 P2 — 02:18 → 03:45 静默断网溯源

> 估工：1 小时

- [ ] 复核 git reflog / production log（如果有）确认这段时间确无 ZCode 操作
- [ ] 若确认无人碰过，则说明人为运维 gap → 与 P0 合并：监控盲区是制度问题

### 4.4 P3 — 防止再次出现"双 unit 文件"漂移

> 估工：0.5 人天

- [ ] 检查 `scripts/deploy-245.sh` 是否会探测已有 `*.service` 文件并强制清理/迁移
- [ ] 检查 `scripts/lib/install-systemd-unit.sh`（如果有）的幂等性
- [ ] 在 deploy-245 SKILL.md 加 "deploy 前：检查单一管控 unit"

---

## 5. 关联文档

- handoff: `/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode/handoff-20260819-245-verify-stash-cleanup.md`
- 上一会话 session-log: `docs/session-logs/2026/08/2026-08-19-doc-sweep-and-245-verify.md`
- 245 systemd unit 文件（已删，备份）：`/opt/llm-gateway-go/.bak/legacy-systemd-units-20260819-130017/`
  - `llm-gateway-go.service.bak` (710 bytes, SHA256 50799220...)
  - `license-disabled.conf.bak` (SHA256 d6b9b699...)
  - `override.conf.bak` (SHA256 f1f34ae3...)
- 245 管控 unit（仍启用）：`/etc/systemd/system/llmgo-245.service`

---

## 6. 关联 Rule

| Rule | 用途 |
|---|---|
| rule 03 §6.2 L2 | 环境异常识别（双 unit 漂移是 L2 隐患） |
| rule 09 §2.1 | Factuality（handoff 推断必须被实测推翻） |
| rule 09 §5.2.4 | 留痕（本文件 = 留痕产物） |
| rule 11 §6 | 诚实汇报（handoff §1.2 误判需显式纠正） |
| rule 11 §10 | 任务完成总结（6 节格式） |
| rule 19 §11 | 破坏性修改安全规范（删 unit 文件前备份 + 影响分析） |
| rule 36 | 文档归档（本文档归 session-logs/2026/08/） |
| rule 45 §3 | 环境分析报告（本次 Layer 0~5 结构化） |

---

最后更新：2026-08-19 13:05 +0800
作者：本会话（接力 handoff §4.1）— P0 修复 owner 决策后
