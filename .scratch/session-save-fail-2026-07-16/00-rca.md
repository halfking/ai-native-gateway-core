# Session Save Failure + DB Table Size Audit — 2026-07-16

**Target**: 245 pre-prod (8.136.114.245, gateway PID 3173065) → DB `172.16.2.210:5432/llm_gateway`

## 1. 现状速览（线上实测）

| 表 | 大小 | 行数 | 风险 |
|---|---|---|---|
| `request_logs_hot` | **2191 MB** | 5615 | **CRITICAL** — heap 仅 12 MB，TOAST 2171 MB，每行 ~390 KB |
| `request_logs_hot` TOAST | 2171 MB + 44 MB idx | — | 单行 `request_body` 平均 247 KB，最大 1.6 MB |
| `request_logs_bodies_hot` | 32 KB | **0** | bodies 拆分**完全未生效**（拆分表 0 行） |
| `request_logs_2026_07/08` | 296/312 KB | 0/0 | 月度分区空（promote 未启动过 — 数据仍 < 7 天） |
| `request_wal_hot` | 12 MB | 24028 | 正常 |
| `request_wal_2026_07` | 14 MB | 27379 | 正常 |
| `routing_decision_log_hot` | 13 MB | 4415 | 正常（最近 promote 在跑） |
| `routing_audit_log` | — | 4618 | 写入中，但偶发 22P02 |
| `handoff_logs` | 235 MB | 5831 | 无 DDL 在 objects/tables 中，需补迁移 |
| `session_titles` | 64 KB | 16 | 上次写入 2026-06-19，**近 1 月没动** |
| `session_summaries` | 96 KB | 0 | **全空** |
| `session_audit_records` | 40 KB | 0 | 全空 |
| `session_memora_extraction_log` | 64 KB | 4 | 极少触发 |
| `session_intent_evolution` | 64 KB | 0 | 全空 |
| `request_context_attrs` | 1024 KB | 892 | 18:59 ~ 19:16 报"does not exist"（表 19:18 才出现，**已自动恢复**） |

**死锁**：5 个 gateway 连接占用 25.7 小时（pg_stat_activity）。

## 2. 5 类会话/审计写入失败（stderr 累计）

| 类型 | 计数 | 真实原因 | 严重度 |
|---|---|---|---|
| `apihub watcher: register LLM asset failed` | **170,090** | 22P02 (invalid JSON)；3 个固定 ref_id 1139198-200，每分钟整点重试，**当前仍在持续丢数据** | HIGH |
| `telemetry request db persist failed` | 12 | 22P02 / 42703 (origin_stage 列缺失) | MEDIUM |
| `telemetry decision db insert failed` | 1 | 22P02 | LOW |
| `telemetry context_attrs persist failed` | 19 | 42P01 (RCA 表未及时创建) | LOW (已恢复) |
| `audit_log insert failed` | 4 | 22P02 (routing_audit_log.after_json 非合法 JSON) | MEDIUM |
| `candidate_failure_logger: insert failed` | 3 | 22021 (UTF-8 `0xe3 0x3a 0x20`) | LOW |
| `partition_manager: promote failed` | 多次 | 53100 (历史磁盘满) | RESOLVED |
| `connection` 57P01/57P03 | 81 | 历史 PG 重启 | RESOLVED |

## 3. 根因（Go 代码层）

### 3.1 `apihub/pg_store.go:100-107` — `marshalStringMap`/`marshalAny`

```go
tagsJSON, err := marshalStringMap(a.Tags)        // []byte → JSONB
metadataJSON, err := marshalAny(a.Metadata)
```

`json.Marshal` 对 `map[string]any` 中的 `float64(NaN/Inf)`、非法 UTF-8、控制字符（`\u0000`）**会产生无效 JSON**。PG 报 22P02 后，goroutine 仍每分钟重试同 3 个 ref_id。

**修复**：marshal 前 sanitize（剔除 NaN/Inf、`utf8.ValidString`、剔除 `\x00`），失败 fallback 到 `{}`。

### 3.2 `admin/users.go:80-83` — `auditLog`

```go
_, err := h.db.Exec(ctx, `INSERT INTO routing_audit_log (... after_json) VALUES (... $5)`,
    actor, action, targetType, targetID, payload)  // payload 是 []byte (JSONB)
if err != nil {
    slog.Warn("audit_log insert failed", ...)
}
```

`payload = json.Marshal(details)`，如果 `details` 是含控制字符或 NaN 的字符串，PG 拒收。**仅 Warn，不重试也不上报**。

**修复**：marshal 前 sanitize，JSONB 拒收时降级为 TEXT（如不可解析则存原始字符串）。

### 3.3 `admin/telemetry.go:354` — `persistRequestLog` 失败吞错

```go
if err != nil {
    slog.Warn("telemetry ingest request_logs failed", "error", err)
    return
}
```

调用方 `handleTelemetryRequestLog` 直接返回 `"status": "queued"` 200 OK，**永远不会让客户端知道失败**。

**修复**：
- 区分 transient（连接/超时/死锁）vs permanent（schema/约束/类型），transient 重试 1-2 次；
- permanent 时让 handler 返回 500 或至少在 metric 中递增失败计数；
- `onPersisted` 已经包含成功路径，但要加 `onFailed` hook 暴露失败面给上层。

### 3.4 `domains/hooks/observability/telemetry/client.go:397-407`

```go
if err := c.persistRequestLog(entry); err != nil {
    if c.fallback != nil {
        ... fallbackErr = c.fallback.WriteRequestLog(...)
    } else {
        slog.Warn("telemetry request sync persist failed", ...)
    }
}
```

fallback 文件只写 **request_log** 类型；sync 路径失败的写入**没有 metric 暴露**。

**修复**：加 `telemetry_persist_failures_total{op}` counter + 在 Prometheus metrics 中暴露。

### 3.5 `request_logs_hot` TOAST 膨胀

5615 行 2.1 GB，平均 390 KB/行；`request_body` 平均 247 KB，最大 1.6 MB。

**根因**：`domains/hooks/observability/telemetry/client.go:884` 把 `entry.RequestBody`（含 system + 全部 user/assistant 消息 + tools）整条塞进 JSONB 列，**bodies 拆分迁移（328a）从未生效**（拆分表 0 行）。

**本次不修 SQL**（你选了 Go 侧修复）。但在 Go 侧加：
1. 强制走 `LLM_GATEWAY_KEEP_ALL_BODIES=false` 默认（成功请求 drop body，失败保留）；
2. 文档化 bodies 拆分路径已存在但未启用。

## 4. 风险评估

| 风险 | 触发条件 | 当前状态 | 影响 |
|---|---|---|---|
| `request_logs_hot` 持续增长 | 每天 5615/7d ≈ 800 行/天 × 390 KB | 活跃 | **3 天后 ~3.5 GB**，触发 PG 磁盘满（7/12-7/13 已发生过） |
| apihub watcher 持续刷屏 22P02 | 每分钟 3 次 × 永远 | 持续 | 写入循环无意义刷 stderr，掩盖真问题 |
| telemetry request 失败无 metric | 客户端 200 OK 但行没入库 | 已存在 | 用户看不到"行丢了" |
| audit_log 失败静默 | 22P02 触发 | 偶发 | 审计不完整 |
| `session_*` 表全空 | session 写入路径未触发或失效 | 长期 | **核心会话元数据无持久化** |

## 5. 修复范围（本任务只做 Go 侧）

1. `apihub/pg_store.go` marshal 增强（NaN/Inf/控制字符 sanitise）
2. `admin/users.go` auditLog payload sanitise + 重试
3. `admin/telemetry.go` ingest 失败区分 transient/permanent + 暴露 metric
4. `domains/hooks/observability/telemetry/client.go` 加失败 counter

**不做**：
- SQL 迁移（bodies 拆分补救、删除过期 candidate_failure_logs）
- VACUUM FULL hot 表
- session_* 表重新启用（需要上游链路诊断）

## 6. 验证

- `go vet` + `go build` + `golangci-lint`
- 部署 245 → 验证 `request_logs_hot` 写入路径不再 22P02（人工检查 stderr 1 小时）
- browser-use 登录 https://llmgo.kxpms.cn/admin (Veritrans/9527)，浏览只读路径