# 2026-08-23 telemetry sanitize observability — rescued label + required-field guard

## TL;DR

将 `domains/hooks/observability/telemetry` 子系统的两条日志信号（`slog.Warn` "telemetry JSON/JSONB field discarded/repaired by truncation"）落地为 Prometheus counter `telemetry_sanitize_events_total{outcome, field, source, stage}`，让运维能够 rate() 告警 `discarded`，把 `rescued`（保留 truncated prefix）与 `discarded`（整字段 NULL）在 metric 维度上拆开。同时给 `EmitRequestLogUpdate` 加了 RequestID 必填字段守卫，避免孤儿 UPSERT 在 WAL 上无法 reconcile。

## Root cause (1 paragraph)

245 环境上 `minimax-m3` 请求在网关侧行为正常，但 `request_logs_hot` 表里对应行缺失或关键 JSONB 列（`request_body`/`outbound_body`/`attachments`/`routing_attempts`）被 NULL 化。对比 `gpt-5.6-terra` 同期一切正常锁定到 `sanitizeJSONField` / `sanitizeRawJSONField`：当流字节里包含 invalid UTF-8 序列（minimax-m3 的 reproducer 是 0xE5 0xBC 0xE2 三字节片段）时，这两个函数把字段整个 NULL 化丢掉，而 `/request-logs` UI 依赖这些 JSONB 列做审计回放。在被 fix 之前，唯一信号是 `slog.Warn`，不可达 Prometheus、不可被告警，只能靠人工 grep 日志发现。

## What changed

### Layer 1 — Prometheus counter (Step 5)

`domains/hooks/observability/telemetry/sanitize_prometheus.go`（新文件）：

- `prometheus.CounterVec` 名字：`telemetry_sanitize_events_total`
- 标签集合（**bounded cardinality，pre-init at boot**）：
  - `outcome` ∈ `{discarded, rescued}`（重命名后）
  - `field` ∈ `{request_body, response_body, outbound_body, compression_meta, discard_events, outbound_msg_hashes, quality_fix_actions, tool_calls, attachments, routing_attempts, auto_decision}` — 11 个
  - `source` ∈ `{string_field, json_field, raw_json_field}` — 3 个
  - `stage` ∈ `{sanitize, required_field_guard}` — 2 个
  - 总共 12 × 2 × 3 × 2 = 144 个 series（含 required-field guard 的 `request_id`），PromQL 维度完全可枚举
- `init()` 用 `Add(0)` 把 144 个组合全部 pre-init，避免首次 inc 时新增 series。
- `incSanitizeEvent(outcome, field, source, stage)` 是单一 Inc seam。

### Layer 2 — 串接到所有 slog.Warn 处（Step 5）

`domains/hooks/observability/telemetry/client.go` 的 4 处 slog.Warn 各加一次 `incSanitizeEvent()`：

- `sanitizeJSONField` 在 `discarded` / `truncate` 路径分别 inc
- `sanitizeRawJSONField` 同样

### Layer 3 — required-field guard (Step 5)

`EmitRequestLogUpdate(entry)`：

```go
if entry.RequestID == "" {
    slog.Error("telemetry EmitRequestLogUpdate dropped: missing request_id", ...)
    incSanitizeEvent("discarded", "request_id", "string_field", "required_field_guard")
    return
}
entry.TenantID = nonEmpty(entry.TenantID, "default")   // 1b8911573 fix
c.EmitRequestLog(entry)
```

- 阻止孤儿 UPSERT 进 PG（`persistRequestLog → updateRequestLog` 路径依赖 RequestID 做 upsert key，空的话 WAL 无法 reconcile）。
- TenantID 兜底走 `nonEmpty` helper，与 `insertRequestLog` 共用一套语义，避免漂移（commit `1b8911573` 修了 hardcode "default" 的潜在 bug）。
- Success/RequestStatus 是 UPDATE 的可选字段，不强制。

### Layer 4 — outcome label 重命名 (Step 6)

`repaired` → `rescued`：

- 旧 label `repaired` 与 `discarded` 在 outcome 维度上语义混在一起——审计员看到 metric rate 上升无法判断是「整字段 NULL」还是「partial body」。Step 6 把 outcome 拆成两档 severity：
  - `discarded` — sanitizeUTF8JSON 返回 ""，字段被 NULL 化。**Severity 高**：request_body / outbound_body 列被 NULL 时 `/request-logs` 看不到。alertable。
  - `rescued` — sanitizeUTF8JSON 返回非空 truncated 字符串，**保留 truncated prefix**（旧版虽然也保留，但 label 没区分），让审计员在 UI 仍能看到 partial body。Severity 中：非 alert by itself，但持续高 rate() 暗示上游在发坏字节。
- pre-init label 列表、`Help` string、4 处 slog.Warn 文案、`sanitize_prometheus_test.go` 测试断言、Step 6 诊断脚本 grep 模式 —— 全部同步重命名。

### Layer 5 — 测试覆盖

`domains/hooks/observability/telemetry/sanitize_prometheus_test.go`（新文件）：

- `TestSanitizeJSONField_DiscardsAndIncrementsMetric`：复现 2026-06-11 incident 的 invalid UTF-8 三字节序列，断言 `discarded{request_body,json_field,sanitize}` delta = 1，且输出被置 nil。
- `TestSanitizeRawJSONField_DiscardsAndIncrementsMetric`：JSONB 路径同样覆盖。
- `TestSanitizeJSONField_ValidInputDoesNotIncrement`：负向断言 valid JSON 既不 bump `discarded` 也不 bump `rescued`。
- `TestEmitRequestLogUpdate_RejectsEmptyRequestID`：构造 `Client{}` 零值（无 DB），断言 guard 阻止后续 queue send。
- `TestSanitizeJSONField_RescuedKeepsTruncatedPrefix`（Step 6）：回归保险——确保未来 refactor 不会把 rescued 路径退化成 discarded，导致 partial body 被吞。输入 `{"a":"v"}garbage\xff\xfe` 断言：
  - 输出非 nil 且首字符 `{`
  - `rescued{request_body,json_field,sanitize}` delta = 1
  - `discarded` 在可 rescued 输入上不 bump
- `TestSanitizeRawJSONField_RescuedKeepsTruncatedPrefix`：`[1,2,3]garbage\xff` → `[1,2,3]`，同样断言 outcome=rescued。

### Layer 6 — 245 诊断脚本

`scripts/diag/245-minimax-m3-requestlog-investigation.sh`（新文件）：

- 5 步全栈诊断：
  - Step 1 grep `LOG_DIR` 里的 8 个特征串（含 `telemetry EmitRequestLogUpdate dropped: missing request_id` 与新文案）
  - Step 2 grep minimax-m3 的 5xx/client_disconnect/stream_interrupted 上下文
  - Step 3 PostgreSQL 4 个对照查询（model counts / WAL vs logs_hot / null-rates / failure-detail）
  - Step 3e（Step 6 加）按 `octet_length(request_body)` 分组抽样，识别 sanitize truncate 救回比例
  - Step 4 拉取 `telemetry_sanitize_events_total` 当前值
  - Step 5 给出 4 类结论分支 + Prometheus 告警 PromQL
- 输出落 `./diag-out/` 便于贴 issue；`bash -n` + `shellcheck` 已 verify。

## Before / after

### Before

| 场景 | 信号 | 可告警？ |
|---|---|---|
| 字段被 NULL 化 | `slog.Warn "telemetry JSON field discarded, storing NULL"` | ❌ 不可达 Prometheus |
| truncate 保留 prefix | `slog.Warn "telemetry JSON field repaired by truncation"` | ❌ 不可达 Prometheus |
| RequestID 空导致孤儿 UPSERT | 无 | ❌ 完全静默 |
| `/request-logs` UI 显示 NULL | DBA 手工查表 | ❌ 无 metric |

### After

| 场景 | metric label tuple | 推荐告警 |
|---|---|---|
| 字段被 NULL 化 | `telemetry_sanitize_events_total{outcome="discarded",field="request_body",source="json_field",stage="sanitize"}` | `rate(...)[5m] > 0` |
| truncate 保留 prefix | `telemetry_sanitize_events_total{outcome="rescued",...}` | `rate(...)[5m] > 1`（持续 rescue 暗示上游坏字节） |
| RequestID 必填守卫 | `telemetry_sanitize_events_total{outcome="discarded",field="request_id",source="string_field",stage="required_field_guard"}` | `rate(...)[5m] > 0` |
| 审计员 UI 行为 | 拿到 `rescued` 时看到 partial body 而不是 NULL | 体验改善 |

## Upgrade / rollback

### Upgrade

无需 schema migration。只需重新 build 二进制并 deploy。Prometheus 端：

```yaml
# 建议（待 product 确认）
- alert: TelemetrySanitizeDiscarded
  expr: rate(telemetry_sanitize_events_total{outcome="discarded",field=~"request_body|outbound_body|attachments"}[5m]) > 0
  for: 2m
  annotations:
    summary: "telemetry sanitize is NULLing out JSONB columns"
- alert: TelemetrySanitizeRescuedHigh
  expr: rate(telemetry_sanitize_events_total{outcome="rescued"}[5m]) > 1
  for: 5m
  annotations:
    summary: "telemetry is rescuing >1 rows/sec — upstream is sending bad bytes"
```

### Rollback

- **回退 5 个 commit**：`git revert 5c16cfb9f 42739c0ec a815b2544 1b8911573 3e30967b9`。
- 回退后 metric `telemetry_sanitize_events_total` 消失，PromQL `rate(...)` 会立即返回无数据——属预期。
- `repaired` label 在回退后会重新出现（Step 6 之前的历史 metric series 会重新可见）。
- `scripts/diag/245-...sh` 在回退后仍可跑，但 Step 1 grep 关键字需改成旧文案；Step 4 拉不到 metric。
- **零 schema migration 风险**：所有改动是纯 Go 代码 + Prometheus metric 注册。

## Compatibility

- **上游调用方**：`sanitizeJSONField` / `sanitizeRawJSONField` / `EmitRequestLogUpdate` 的签名未变。
- **下游 metric 消费方**：132 个新 series 一次性 pre-init，无启动期未注册风险；旧 series（如 `repaired`）在升级后立即停止 inc（Step 6 label 重命名切断），降级后会重新 inc。
- **其他 handler**：`EmitRequestLogUpdate` guard 在所有调用点统一生效，调用方若曾依赖「RequestID 为空也能写 UPDATE」会看到 silent drop——此路径本就是 bug，守卫是 fix。
- **测试**：`go vet ./...` / `go build ./...` / `go test -count=1 ./domains/hooks/observability/telemetry/...` 全绿（本会话已 verify）。

## Files touched

```
docs/changelogs/2026-08-23-telemetry-rescued-pr.md           | NEW (PR 描述)
docs/handoff/2026-08-23-preferred-credential-followup.md     | (4c6833906 既存，本会话结束记录)
docs/changelogs/2026-08-23-preferred-credential-admin-pin.md | (8e71eb4e1 既存)
domains/hooks/observability/telemetry/client.go              | 守卫 + 4 处 incSanitizeEvent + label rename + TenantID fallback
domains/hooks/observability/telemetry/sanitize_prometheus.go | NEW (counter + pre-init)
domains/hooks/observability/telemetry/sanitize_prometheus_test.go | NEW (6 测试覆盖)
scripts/diag/245-minimax-m3-requestlog-investigation.sh      | NEW (5 步诊断 + 告警建议)
```

## Verification commands

```bash
# 代码
go vet ./domains/hooks/observability/telemetry/...
go build ./...
go test -count=1 -v ./domains/hooks/observability/telemetry/... | tee /tmp/telemetry-test.log

# 脚本
bash -n scripts/diag/245-minimax-m3-requestlog-investigation.sh
shellcheck scripts/diag/245-minimax-m3-requestlog-investigation.sh

# 245 主机（升级后）
scp scripts/diag/245-minimax-m3-requestlog-investigation.sh gateway-245:/tmp/
ssh gateway-245 'sudo bash /tmp/245-minimax-m3-requestlog-investigation.sh'
# 收集 ./diag-out/ 回贴

# Prometheus（升级后）
curl -sf http://gateway-245:9090/api/v1/query?query=telemetry_sanitize_events_total | jq '.data.result | length'
# 期望 132（11 field × 2 outcome × 3 source × 2 stage）
```
