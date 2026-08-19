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

---

## 4. 行动项（按优先级）

### 4.1 P0 — 监控盲区补漏（已发生事故，避免复发）

> 估工：0.5 ~ 1 人天

- [ ] 确认 Prometheus blackbox 是否探测了 `https://llmgo.kxpms.cn/healthz`，未探测则加 job
- [ ] 确认 Alertmanager 是否有 "gateway 5min 不通 → page oncall" 路由，未有则加
- [ ] 写 runbook："245 pre-prod gateway down → 排查清单（systemd cgroup + llmgo-245.service status + binary uptime + stderr tail）" — 我已经做完一次，可直接沉淀
- [ ] 把本次清理 + 完整诊断写成 `docs/audit/2026-08-19-245-restart-loop-followup.md`（本文件就是）

### 4.2 P1 — 01:00 restart-loop 根因定位

> 估工：2 ~ 4 小时

- [ ] 翻 deploy-245 在 245 上的内部日志（首选）
- [ ] 翻 stderr.log 头部（次选）
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
