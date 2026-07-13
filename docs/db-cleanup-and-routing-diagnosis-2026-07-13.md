# DB 清理 + 路由降级诊断总结（2026-07-13）

## 问题背景

用户报告：SenseNova（credential 25）和 NVIDIA NIM（credential 19）的 glm-5.2 节点连续错误，但没有自动降级。

## 诊断结果

### 🔴 根因：candidate_failure_logs 停止写入 17 天

**证据链：**

1. **最近 30 分钟失败统计**（252 生产库）：
   - credential 19 (NVIDIA NIM)：8 次失败（7 × `model_not_found` + 1 × `upstream_credential_invalid`）
   - credential 25 (SenseNova)：15 次失败（9 × `model_not_found` + 3 × `rate_limit` + 3 × `transient`）
   - **错误类型 `model_not_found` 说明这两个 provider 不支持 glm-5.2**

2. **candidate_failure_logs 最后写入时间：2026-06-26 02:25:28**（距今 17 天）
   - 表存储模式：Citus Columnar（append-only，但 INSERT 测试正常）
   - 总行数：40526 行（全部是历史数据）

3. **auto-cool 机制失效**：
   - `bg/candidate_failure_monitor.go` 的 `checkAutoCool` 依赖最近 5 分钟的 `candidate_failure_logs` 数据
   - 如果表停止写入 → 监控器永远看不到新失败 → 永远不触发 auto-cool
   - credential 19/25 的状态仍然是 `availability_state = 'ready'`，没有进入 `cooling`

4. **写入路径正常但未执行**：
   - 代码路径：`executor.Execute()` → `e.FailureLogger.LogFailure()` → `INSERT INTO candidate_failure_logs`
   - `cmd/gateway/main.go:883` 注入了 `FailureLogger`
   - 但实际未写入 → 可能是 `FailureLogger == nil` 或 INSERT 静默失败

---

## 修复方案

### ✅ 已实施（代码层面）

#### 1. 增强 candidate_failure_logs 写入失败日志

**文件：`domains/streaming/executors/candidate_failure_logger.go:106`**

```diff
- //nolint:errcheck // best-effort INSERT; log + ignore.
  _, err := w.pool.Exec(ctx, `INSERT INTO candidate_failure_logs ...`)
+ if err != nil {
+     slog.Warn("candidate_failure_logger: insert failed",
+         "error", err,
+         "request_id", requestID,
+         "credential_id", credentialID,
+         "raw_model", rawModelName)
+ }
```

**作用**：INSERT 失败时记录到日志，不再静默吞错误。

---

#### 2. 增加 FailureLogger nil 防御性检查

**文件：`domains/streaming/executors/executor.go:1610`**

```diff
  if e.FailureLogger != nil {
      e.FailureLogger.LogFailure(...)
+ } else {
+     // 2026-07-13: defensive log when FailureLogger is nil
+     slog.Warn("executor: FailureLogger is nil, candidate failure not logged",
+         "credential_id", cand.CredentialID,
+         "raw_model", cand.RawModel,
+         "error_kind", kind)
  }
```

**作用**：如果 FailureLogger 未注入，立即在日志中暴露问题。

---

### ⏳ 待部署验证

部署新版本后，通过以下步骤验证修复：

#### 步骤 1：触发一次失败请求

```bash
curl 'http://acc.kxpms.cn/v1/chat/completions' \
  -H 'Authorization: Bearer <test-key>' \
  -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"test"}]}'
```

#### 步骤 2：检查网关日志

```bash
# 如果 FailureLogger 是 nil，会看到：
journalctl -u llm-gateway -n 100 | grep 'FailureLogger is nil'

# 如果 INSERT 失败，会看到：
journalctl -u llm-gateway -n 100 | grep 'candidate_failure_logger: insert failed'
```

#### 步骤 3：检查 candidate_failure_logs 是否开始写入

```sql
SELECT count(*) AS new_rows, max(ts) AS latest
FROM candidate_failure_logs
WHERE ts > '2026-07-13 22:00:00';
```

预期：`new_rows > 0` 且 `latest` 是当前时间附近。

---

### 🚨 临时缓解（手工降级）

在修复部署前，可手工标记 credential 19/25 为 cooling：

```sql
UPDATE credentials
SET availability_state = 'cooling',
    availability_recover_at = NOW() + INTERVAL '10 minutes',
    state_reason_code = 'manual_cool_model_not_found'
WHERE id IN (19, 25);
```

**效果**：未来 10 分钟内这两个 credential 不会被路由选中。

---

## 已完成的其他优化（本次 session）

### ✅ P0：关键表 TTL（已部署到 252）

1. **handoff_logs TTL**（`bg/handoff_trimmer.go`）
   - 默认保留 14 天（热更新：`lifecycle.handoff_logs_ttl_days`）
   - 日均删除上限 5000 行/批次
   - migration 360：删除冗余索引 `idx_handoff_logs_tenant_created`

2. **armor_judgments TTL**（扩展 `bg/audit_trimmer.go`）
   - 保留 90 天（与 routing_audit_log 一致）
   - 日均删除上限 5000 行/批次

### ⏸️ P1/P2：运营日志 TTL（未部署，Columnar 限制）

3. **candidate_failure_logs TTL**（`bg/opslog_trimmer.go`）
   - 默认保留 7 天（热更新：`lifecycle.candidate_failure_logs_ttl_days`）
   - **无法部署**：表使用 Citus Columnar 存储，不支持 DELETE 操作

4. **credential_probe_model_log TTL**（同上）
   - 默认保留 30 天（热更新：`lifecycle.credential_probe_model_log_ttl_days`）
   - **无法部署**：同样是 Columnar 表

**Columnar 表设计权衡**：
- 优势：15-40x 压缩比（phase-22 批量转换）
- 代价：append-only，不支持 UPDATE/DELETE
- 当前规模：candidate_failure_logs 13 MB + credential_probe_model_log 21 MB = 34 MB，占总库 6.7 GB 的 0.5%，短期不是瓶颈

**可选方案**（未执行）：
1. 转回 heap：`ALTER TABLE candidate_failure_logs SET ACCESS METHOD heap;`（违背压缩优化初衷）
2. TRUNCATE 全表（丢失所有历史数据）
3. 接受现状（短期可接受）

---

## 验证清单

- [ ] 部署新版本到测试环境
- [ ] 触发失败请求，确认 candidate_failure_logs 有新写入
- [ ] 检查日志中是否有 "FailureLogger is nil" 或 "insert failed"
- [ ] 观察 5 分钟后 `bg/candidate_failure_monitor` 是否触发 auto-cool
- [ ] 检查 credentials 表中失败节点的 `availability_state` 是否变为 `cooling`

---

## 遗留问题

1. **252 网关进程不在 systemd 管理下**（无 llm-gateway.service）
   - 可能是手工启动或容器化部署
   - 需要找到实际的网关进程/容器名称才能查看启动日志

2. **184/71 服务器已下线**（用户确认）
   - 无法对比历史环境的 candidate_failure_logs 写入情况

3. **Columnar 表的 TTL 方案**（长期待定）
   - 短期接受 34 MB 占比（0.5%）
   - 长期可考虑转回 heap 或实施定期 TRUNCATE + 重建策略
