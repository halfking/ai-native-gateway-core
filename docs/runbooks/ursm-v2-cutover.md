# URSM v2 切流操作手册（Runbook）

> **配套审计**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md` §7.1 R-7.1  
> **配套方案**：`docs/design/2026-07-28-llm-gateway-flow-improvements.md` §2 P0-3  
> **适用范围**：URSM v2 切换（off → shadow → canary → authoritative）的完整操作流程  
> **最后更新**：2026-07-29

---

## 0. 何时使用本手册

当以下任一情况成立时，按本手册执行 URSM v2 切流：

- P0-3 改进项已经完成（`shadow double-write` 框架就绪 + migration script + rollback script + 上述 4 项依赖决策已经拍板）
- 当前生产模式 `URSM_V2_MODE=off`，运营方准备进入 7 天 shadow 对比窗口
- 245 staging 已经走完一轮切流验证，准备推 154 生产

## 1. 前置决策（不可跳过）

老板必须先拍板以下 4 项（per audit §10 + design §10）：

| 决策点 | 选项 |
|---|---|
| 切换时机 | A 245 验证后立即切 / B 再观察 1 月 / C 季度内分阶段 |
| 双层 sticky 保留层 | A 保留 executor / B 保留 handler |
| OTel 后端投入 | A 立即接入 / B 先 stdout 后接 |
| URSM v2 数据迁移 | A 一次性脚本 / B 接受历史断档 / C 双轨 30 天 |

**未拍板不要切流**。本文档假设决策已完成。

## 2. 切流四阶段

```
[Stage 0: off]                       (生产现状)
    ↓ 一次性 migration
[Stage 1: shadow 7d]                 (路由走 legacy, sidecar 写 URSM v2)
    ↓ drift < 1%
[Stage 2: canary 灰度]               (按 tenant 灰度)
    ↓ 100% 通过
[Stage 3: authoritative]             (URSM v2 接管路由)
```

每个 stage 之间都是 1 行 env var + 1 次 restart + 1 轮 L1-L4 验证。

---

## 3. Stage 0 → Stage 1：进入 Shadow 7 天对比

### 3.1 一次性数据迁移（仅一次）

把 `public.node_probe_state` 的当前状态写到 URSM v2 Redis 命名空间。否则 URSM v2 在 shadow 模式会因 T4 保护性拒绝（"no observed telemetry = not available"）把所有候选视为不可用，shadow 对比窗口毫无意义。

```bash
# 154 生产
./bin/migrate-ursm-v2 --pg="$LLM_GATEWAY_DATABASE_URL" --redis="$REDIS_URL" --apply

# 默认 --dry-run，先看清楚要写多少
./bin/migrate-ursm-v2 --pg="$LLM_GATEWAY_DATABASE_URL" --redis="$REDIS_URL"
# → 输出：
#   ✅ Connected to PG (postgres://***@host:5432/db)
#   ✅ Connected to Redis (redis://***@host:6379/0)
#   ✅ Read N legacy node_probe_state rows
#   [DRY-RUN] would write N URSM v2 nodes:
#     - available (healthy)         : X
#     - in cool (>= 3 fails)        : Y
#     - manual_hold (legacy paused) : Z
#   (no Redis writes; pass --apply to commit)

# 实际跑（245 staging 先验证，再 154 生产）
./bin/migrate-ursm-v2 --pg=... --redis=... --apply
# → 输出：
#   ✓ wrote 100 / 287
#   ✓ wrote 200 / 287
#   ✓ wrote 287 / 287
#   ✅ Wrote 287 URSM v2 nodes to Redis
```

### 3.2 进入 shadow 模式

**关键**：`URSM_V2_MODE=shadow` + `URSM_V2_SHADOW_DOUBLE_WRITE=1`。两个开关缺一不可：

```bash
# 154 生产部署
export URSM_V2_MODE=shadow
export URSM_V2_SHADOW_DOUBLE_WRITE=1

# 重启 gateway（按部署系统调整）
sudo systemctl restart llm-gateway-go
# 或
docker run -e URSM_V2_MODE=shadow -e URSM_V2_SHADOW_DOUBLE_WRITE=1 ...
```

启动后日志应包含：
```
v2 pipeline: LLM_GATEWAY_USE_V2_PIPELINE=...        # 无关
ursm_v2: rollout mode=shadow shadow_double_write=true   # 看到这一行说明启用成功
```

### 3.3 L1-L4 验证（rule 03 §6.0）

| 层 | 命令 | 通过条件 |
|---|---|---|
| L1 | `curl /healthz` | 200 + `{"status":"ok"}` |
| L2 | `curl /internal/ready/db` | OK |
| L3 | `curl /internal/smoke/echo` | 返回 trace_id |
| L4 | `curl /api/providers` | 无 `credential_decrypt_error` |

**额外**：路由行为应**完全不变**。验证方法：

```bash
# 取一次成功请求的 trace，credential_id 应来自 legacy credentialstate
curl /internal/smoke/decision -X POST -d '{"model": "minimax-m3"}' | jq .credential_id
# 再查 credential_state_log 表确认本次走的是 legacy 路径（不是 URSM v2）
psql -c "SELECT * FROM credential_state_log WHERE credential_id = X ORDER BY ts DESC LIMIT 5"
```

### 3.4 7 天观察 + 关键 metric

```bash
# 每小时采样（建议加进 Grafana dashboard）
curl /metrics | grep -E 'ursm_v2_shadow_records_total|credential_state_log_writes' | head
```

期望：

```yaml
llm_gateway_ursm_v2_shadow_records_total{result="recorded"}:
  7d 累计 ≈ legacy credentialstate.UpdateOnSuccess/UpdateOnFailure 7d 累计
  (允许 ±5% 偏差 — URSM v2 的 rollout controller 会对 canary subset 抽样，
  shadow 模式下应为 100%，因为 ShadowDoubleWrite=true)

llm_gateway_ursm_v2_shadow_records_total{result="skipped"}:
  应为 0（因为 ShadowDoubleWrite=true 让 shadow 模式变为 recorded）

llm_gateway_ursm_v2_shadow_records_total{result="failed"}:
  偶发（Redis 抖动）允许；>5/m 触发告警
```

**Drift 计算**（关键判断点）：

```
URSM_v2_records - legacy_state_writes
delta = -------------------------  < 1%  ← 通过
              legacy_state_writes
```

如果 `|delta| >= 1%`：
- 看 URSM v2 端 `result="failed"` 是否激增（Redis 抖动）
- 看 legacy 端是否有 batch_writer 丢事件（已有 audit R-3.3 修复 metric `shadow_write_failed_total`）
- 不修复就**不能进 Stage 2**

## 4. Stage 1 → Stage 2：Canary 灰度

**进入条件**：7 天 drift < 1% + 老板拍板"按 tenant 灰度"。

### 4.1 启用 canary 配置

```bash
export URSM_V2_MODE=canary
# 白名单 tenant 立即走 URSM v2
export URSM_V2_CANARY_TENANTS="tenant-vip,tenant-canary-1,tenant-canary-2"
# 或按百分比（10% 起步）
export URSM_V2_CANARY_PERCENT=10
unset URSM_V2_SHADOW_DOUBLE_WRITE   # canary 模式下不再需要双写
```

### 4.2 灰度阶梯

```
10% 流量 1h  → 错误率基线比对
   ↓
50% 流量 30min → 错误率 / P99 latency 比对
   ↓
100% 流量 24h  → 全量稳定性
```

任何阶梯错误率 > baseline × 1.5 立即回退到上一阶段（rule 03 §7.2）。

### 4.3 回退阶梯

```bash
# 从 canary 回到 shadow（保留数据，再观察）
export URSM_V2_MODE=shadow
export URSM_V2_SHADOW_DOUBLE_WRITE=1

# 从 canary 直接回 off（一键，紧急情况）
bash scripts/rollback/ursm_v2_to_legacy.sh --env=prod
```

## 5. Stage 2 → Stage 3：Authoritative 全量接管

**进入条件**：canary 100% 流量 24h 无回归 + 老板拍板。

### 5.1 全量接管

```bash
export URSM_V2_MODE=authoritative
unset URSM_V2_CANARY_TENANTS URSM_V2_CANARY_PERCENT URSM_V2_SHADOW_DOUBLE_WRITE
sudo systemctl restart llm-gateway-go
```

### 5.2 切流后必看

```bash
# URSM v2 端应该承担全部路由
curl /metrics | grep ursm_v2_filter_and_score_call_total
# legacy credentialstate.FilterAvailable 调用应该清零（或极少量——故障回退路径）
curl /metrics | grep credential_state_filter_total
```

### 5.3 双轨运行期（可选）

如果决策是 "双轨 30 天后切换"（决策 4 选项 C），保留 legacy credentialstate 包但停止其 FilterAvailable 调用：

```bash
export LLM_GATEWAY_LEGACY_STATE_DISABLED=1   # 暂时不支持，需要加 config 字段
```

## 6. 紧急回退（任何阶段）

任意阶段发现以下任一情况立即回退：

- 错误率 > baseline × 1.5
- P99 latency > baseline × 5
- URSM v2 Redis 频繁 Ready=false（Redis 抖动）
- 人工判断节点可用性数据异常

```bash
# 一键回退
bash scripts/rollback/ursm_v2_to_legacy.sh --env=prod
```

回退后**保留 URSM v2 Redis 数据**（不 DEL），便于事后定位。再次切回时直接重跑 stage 1+。

## 7. 关键 file:line 参考

- Rollout controller：`domains/ursm/v2/rollout/controller.go` (4 个 Mode + ShadowDoubleWrite)
- Config / LoadFromEnv：`domains/ursm/v2/config.go`（`URSM_V2_SHADOW_DOUBLE_WRITE` env 读取）
- Manager RecordRequest：`domains/ursm/v2/manager.go:514`（sidecar 写入 + metric）
- StateBackend 选择：`domains/streaming/executors/state_backend.go:146`（`selectStateBackendWithReady`）
- URSM v2 node Redis hash schema：`domains/ursm/v2/store/record_request.lua`
- Migration 工具：`cmd/migrate-ursm-v2/main.go`
- Rollback 脚本：`scripts/rollback/ursm_v2_to_legacy.sh`

## 8. 关联文档

- **审计**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md` §7.1 R-7.1
- **方案**：`docs/design/2026-07-28-llm-gateway-flow-improvements.md` §2 P0-3
- **历史审计**：`AUDIT_URSMV2_CONCURRENCY_20260728.md`（URSM v2 收尾 / 已闭环）
- **历史审计**：`AUDIT_CONCURRENCY_HARDENING_20260727.md`（77 文件大修 / 已闭环）
- **告警规则**：`deploy/monitoring/grafana-alerts/shadow-write-failures.yaml`（P0-2 的 4 条规则继续生效）

---

**下次审视**：URSM v2 切流完成后 1 周（验证 Stage 3 流量稳定性 + drift 趋势）。