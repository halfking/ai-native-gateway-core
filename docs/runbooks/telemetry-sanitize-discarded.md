# Telemetry sanitize discarded / rescued 告警处置

## Scope

本 runbook 处理以下告警（`deploy/prometheus/rules/telemetry-sanitize.yml`，规则组
`llmgw-telemetry-sanitize`）：

| 告警 | 触发条件 | 严重度 |
|---|---|---|
| `TelemetrySanitizeDiscarded` | 任一字段 `outcome="discarded"` 5m rate > 0 | critical |
| `TelemetrySanitizeDiscardedCriticalFields` | `request_body / outbound_body / attachments / tool_calls` 被丢弃 | critical |
| `TelemetrySanitizeRescuedHigh` | `outcome="rescued"` 持续 > 0.1 events/s 达 10m | warning |
| `TelemetryRequiredFieldGuardDiscarded` | `stage="required_field_guard"` 持续丢弃 | warning |

`discarded` 表示 `sanitizeUTF8JSON` 返回空串、对应 JSONB 列被 NULL 化，审计员看不到
原始数据；`rescued` 表示保留了 truncated JSON prefix（降级但可读）。

**计数器没有 `model` label**（label 集 = `{outcome, field, source, stage}`，共 144
series）。任何按模型归因的查询必须走 SQL（`request_logs_hot` / `request_wal_hot`），
不能依赖 metric。

## First response：跑 doctor 脚本

在网关主机（245 = `root@<env:HOST_245_IP>`）上运行调查脚本，它会输出决策树所需的全部
证据。DB / Prometheus 地址用环境变量覆盖：

```bash
scp scripts/diag/245-minimax-m3-requestlog-investigation.sh root@<env:HOST_245_IP>:/tmp/diag-245.sh
ssh root@<env:HOST_245_IP>
cd /tmp && eval $(python3 -c "
import re
url = open('/opt/llm-gateway-go/.env').read()
m = re.search(r'^LLM_GATEWAY_DATABASE_URL=(.*)$', url, re.M)
import sys
g = re.match(r'^postgres(ql)?://([^:]+):([^@]+)@([^:/]+):?(\d+)?/(\w+)', m.group(1).strip())
print(f'export PGUSER={g.group(2)} PGPASSWORD={g.group(3)} PGHOST={g.group(4)} PGPORT={g.group(5) or 5432} PGDATABASE={g.group(6)}')
")
LOG_DIR=/var/log/llm-gateway-go PROM_URL=http://127.0.0.1:9090 LOOKBACK_HOURS=24 bash /tmp/diag-245.sh
```

脚本只做只读查询；原始输出落在 `./diag-out/`。245 的 Prometheus 是原生 systemd 形态
（`127.0.0.1:9090`，非 compose 容器），Grafana 不在 245 上（见下）。

## 决策树

### 1. Step 4 指标确认告警真实性

脚本 Step 4 拉取 `telemetry_sanitize_events_total`。若对应 series 仍为 0，说明告警
已恢复（短时毛刺）或抓取目标不对——检查 Prometheus target 后收工。

### 2. 3b 有缺口 + 指标为 0 → 不是 sanitize，查 persist 失败

**2026-08-23 实测**：3b 显示 WAL → `request_logs_hot` 大量缺口（minimax-m3 约 98%、
gpt-5.6-terra 约 94% 的 WAL request_id 无最终行），但 Step 1 出现
`"telemetry request db persist failed; fallback written"` × 10k+（24h，仅活跃日志），
错误为 `numeric field overflow (SQLSTATE 22003)`（insert 与 update 均有）。

这条路径与 sanitize 无关：落库 SQL 因数值列溢出失败，只写了 WAL 兜底。排查方向：

- Step 1 输出中 `telemetry request db persist failed` 的 total 计数；
- `diag-out/` 里的原始行，确认 SQLSTATE；
- 定位溢出列（`request_logs_hot` / `request_logs_bodies_hot` 的 numeric 列精度 vs
  上游实际值，如 cost / token 累计量级）；
- 修复走 schema migration（加精度）或写入端 clamp，二选一后在 154 promote 前
  先在 245 复跑本脚本验证 3b 缺口收敛。

### 3. 3b 有缺口 + 指标非 0 → 真丢失（sanitize 路径）

`outcome="discarded"` 在增长说明 `sanitizeUTF8JSON` 持续返回空串。对照 3a/3d 的
`COALESCE(NULLIF(outbound_model), NULLIF(client_model))` 分布找出受影响模型，检查
`client.go` 的 `sanitizeJSONField` / `sanitizeRawJSONField` WARN 日志里的原始字节。

### 4. 3c / 3e → side-table 完整性

`request_logs_hot.request_body / response_body` **按设计为 NULL**，主表正文列空不
构成证据；正文必须 JOIN `request_logs_bodies_hot`（3c/3e 已按此实现）。若 minimax
的 side-table 行缺失率或正文 NULL 率显著高于对照模型（脚本用 gpt-5.6-terra），
检查 `sanitizeRequestLogEntry` / `upsertRequestLogBodies`。

### 5. rescued 高 → 上游发坏字节

`TelemetrySanitizeRescuedHigh` 触发时核对上游协议版本、UTF-8 编码、是否夹带
BOM / 控制字符。rescued 保留 prefix 是降级行为，不是预期行为。

## Grafana 说明

`deploy/prometheus/dashboards/telemetry-sanitize.json` 走 compose provisioning（挂载
`./dashboards:/etc/grafana/provisioning/telemetry-dashboards:ro`），只对运行
compose 监控栈的宿主机生效（参考 Phase0 文档：kaixuan-1）。**245 是原生二进制 +
systemd 的 Prometheus/Alertmanager，没有 Grafana**——不要在 245 上等 dashboard
出现；245 上的验证以 Prometheus API（`/api/v1/rules`、instant query）为准。

## 相关文件

- `deploy/prometheus/rules/telemetry-sanitize.yml` — 告警规则（已同步 245 并 reload，2026-08-23）
- `scripts/diag/245-minimax-m3-requestlog-investigation.sh` — doctor 脚本
- `docs/changelogs/2026-08-23-telemetry-rescued-pr.md` — rescued 行为变更记录
- `deploy/prometheus/NATIVE-245-DEPLOY.md` — 245 原生监控形态与运维
