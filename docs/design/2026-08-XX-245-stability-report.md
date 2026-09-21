# 245 pre-prod 稳定性报告

> **生成日期**: 2026-08-XX (pre-prod 验收 1 周后填写)
> **作者**: on-call / infra-team
> **关联**: docs/06-deployment/04-runbooks/ops/245-runbook.md §11

---

## 0. TL;DR

| 指标 | 目标 | 实测 | 状态 |
|---|---|---|---|
| 可用性 | ≥ 99.9% | XX.XX% | ⬜ |
| P99 latency | < 2s | X.XXs | ⬜ |
| Error rate | < 0.1% | X.XX% | ⬜ |
| OOM 事件 | 0 | X | ⬜ |
| SIGKILL 事件 | 0 | X | ⬜ |

---

## 1. 验收标准核对（245-runbook §11）

| # | 检查项 | 结果 | 备注 |
|---|---|---|---|
| 1 | `curl healthz` 返回 200 + version | ⬜ | |
| 2 | `curl minimax-m3` 一次 (live, 用 hermes api key) | ⬜ | |
| 3 | `curl claude-sonnet-5` 一次 | ⬜ | |
| 4 | `journalctl -u llmgo-245 --since "5 min" \| grep -E "WARN\|ERROR\|panic" \| head -10` 无报错 | ⬜ | |
| 5 | §5 所有监控指标 1 周稳态无漂移 | ⬜ | 附 Grafana dashboard 截图 |
| 6 | `deploy-seamless.sh status 245` 显示 verified release | ⬜ | |

---

## 2. 关键指标走势（1 周）

### 2.1 可用性 & Latency
- Grafana dashboard: `llm-gateway-245-overview`
- P50/P95/P99 latency 走势图

### 2.2 Error Rate
- 5xx rate by endpoint
- 429 rate (rate limit)

### 2.3 Memory & OOM
- llmgo-245 RSS trend (Grafana: `llmgo-245-memory`)
- OOM killer events: `dmesg \| grep -i oom`
- systemd SIGKILL: `journalctl -u llmgo-245 \| grep -i kill`

### 2.4 System Resources
- CPU usage (Grafana: `node_cpu_seconds_total`)
- Disk usage: `df -h / /opt`
- Network I/O

---

## 3. 关键事件记录

| 日期 | 事件类型 | 影响 | 处理 | 根因 |
|---|---|---|---|---|
| 2026-08-XX | | | | |

---

## 4. 依赖服务健康度

| 服务 | 状态 | 备注 |
|---|---|---|
| 252 PG17 (<env:HOST_252_INTERNAL_IP>) | ⬜ | |
| 252 Redis (6389) | ⬜ | |
| 252 nginx (upstream) | ⬜ | |
| 245 nginx | ⬜ | |
| 245 alertmanager/prometheus | ⬜ | |

---

## 5. 部署记录

| 日期 | 版本 | 变更类型 | 部署耗时 | 回滚 |
|---|---|---|---|---|
| 2026-08-XX | vX.Y.Z-seq | feature/fix | Xm | 无/有 |

---

## 6. 已知问题 & 风险

| 风险 | 可能性 | 影响 | 缓解措施 |
|---|---|---|---|
| INSERT 路径 SQL/参数错位 (unused argument: 95) | 高 | telemetry 落库失败 | P0 follow-up 修复 |
| systemd TimeoutStopSec 仍可能触发 SIGKILL | 中 | restart 短暂不可用 | main.go timeout 已优化，观察中 |
| FEISHU webhook 未接通 | 高 | 告警无法触达 | 等待团队注册机器人 |

---

## 7. 结论 & 签收

**结论**: 245 pre-prod [通过/不通过] 1 周验收

**签收人**: _______________  **日期**: _______________

---

**附件**:
- Grafana dashboard 导出 (PDF)
- 关键告警记录导出
- `deploy-seamless.sh status 245` 输出
