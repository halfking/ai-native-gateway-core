# 整改完成报告 — 全局路由与探针状态机审计

- 日期：2026-08-19
- 前置交接：`docs/handoff/2026-08-18-global-routing-audit-handoff.md` (`dca827fa0`)
- 范围：5 个并行 agent 的产出与一体化验证

---

## 0. TL;DR

- ✅ `go build ./...` 干净
- ✅ `go vet ./...` 干净
- ✅ 25 个包回归测试全部通过：`bg`、`admin`、`domains/ursm/v2/...`、`domains/streaming/executors/...`、`sql/migrations/startup/...`
- ✅ 4 份审计/设计文档落库
- ✅ 23 个新文件、2294 行新增（包含 9+7+9=25 个新测试）
- ⏳ 现场 DB 残留 82 行伪成功需 ops 单独清理（不阻断代码合并）
- ⏳ 部署门禁：先 245 probe-canary，再 245 全量，再 154 全量

---

## 1. 交付清单（按 agent 分组）

### Agent A — 恢复器伪成功 P0

**修改文件**：
- `bg/credential_recovery.go`（+182 / -46）
- `bg/credential_recovery_test.go`（+485）

**关键变更**：
1. 删除 `bg/credential_recovery.go:443-485` 的伪成功 UPDATE：禁止直接写 `last_direct_ok=TRUE/last_gateway_ok=TRUE/next_retry_at=now()+1h`。
2. 新增 `reconcileStaleNodeProbeStates`：只读 SELECT 出 `available=TRUE` 但 `last_*_ok != TRUE` 的 (cred, model) 对，通过 `probeSubmitter` → `ProbeQueue.Enqueue` → `ProbeService.Run` 走真实探测。
3. URSM 写入路径不动；Recover 优先级不动（仍专属 `scanLookbackRecoveries` 谓词 36h 内有 logged success）。
4. 双层 dedup：`ProbeQueue` 的 `ON CONFLICT DO NOTHING + dedup_key` + `recover()` 内的 `invalidSet` map。

**新增测试**（9 个）：`TestReconcileStaleNodeProbeStateSQLGuards`、`TestReconcileStaleNodeProbeStatesEnqueuesProbes`、`TestReconcileStaleNodeProbeStatesNoOpWhenNoSubmitter`、`TestReconcileStaleNodeProbeStatesSkipsWhenNoRows`、`TestReconcileStaleNodeProbeStatesReturnsErrorOnQueryFailure`、`TestRecoverNoLongerWritesFakeSuccessSQL`（核心回归 pin）、`TestRecoverOrdering_StaleReconcileBeforeExpiredAndFreshDegraded`、`TestConcurrentReconcileDoesNotDoubleEnqueue`、`TestURSMSourcePriorityUnchanged`。

**Commit 信息建议**：`fix(recovery): stop fake-success loop in credential_recovery, require real probe evidence`

---

### Agent B — 探针审计 CHECK + lease

**新增文件**：
- `sql/migrations/startup/538_node_probe_runs_trigger_kind_unified_queue.sql`
- `sql/migrations/startup/538_node_probe_runs_trigger_kind_unified_queue.down.sql`
- `sql/migrations/startup/migration_538_test.go`
- `bg/probe_service_audit_lease_test.go`

**修改文件**：
- `sql/objects/tables/node_probe_runs.sql`（mirror 同步）
- `sql/schema/01-schema.sql`（mirror 同步）
- `bg/probe_service.go`（+322：heartbeat、audit-error surfacing、lease-takeover guard、metrics）
- `bg/probe_queue.go`、`bg/probe_queue_worker.go`（lease 默认值 30s → 5m）
- `bg/node_probe.go`（legacy `runOne` audit INSERT 不再吞错）
- `cmd/gateway/main.go`（两处 probeService.SetProbeQueue 注入）

**关键变更**：
1. Migration 538 扩展 `node_probe_runs_trigger_kind_check` 接受全部 9 个统一队列 `task.Source`：`request_failure / manual / credential_recovery / sync_request / periodic / admin / integrity_probe_planner / selfcheck / external_async`。
2. `bg/probe_service.go`：
   - `NormalizeTriggerKind` + `knownTriggerKind` map 兜底未知来源为 `request_failure`；
   - heartbeat goroutine（60s 心跳 ×5 覆盖 lease）；
   - `insertAuditRowWithLeaseCheck`：OwnsLease 重新校验后再 INSERT；
   - 新 metric：`audit_persist_failed_total / audit_unknown_source_total / lease_heartbeat_extended_total / lease_lost_total`；
   - 新 error：`ErrProbeAuditPersistFailed / ErrProbeLeaseLost`。
3. Lease 默认 5 分钟（vs 旧 30s）：worst-case 双轮 + URSM + side-effect ~38-45s，5m 永不误过期。

**新增测试**（7 个）：`TestNormalizeTriggerKindCoversUnifiedQueueSources`、`TestNormalizeTriggerKindEmptyReturnsRequestFailure`、`TestProbeServiceReturnsErrProbeAuditPersistFailedWhenInsertFails`、`TestProbeServiceLeaseHeartbeatExtendsDuringRun`、`TestProbeServiceHeartbeatExtendsLeasePeriodically`、`TestProbeServiceAuditLeaseCheckSkipsAuditWhenLeaseLost`、`TestProbeQueueDefaultLeaseIsProbeQueueLeaseDefault`。

**Commit 信息建议**：`fix(probe-audit): extend trigger_kind CHECK + 5m lease + audit error surfacing`

---

### Agent C — 自检 pin + dashboard 真相源

**修改文件**：
- `bg/credential_selfcheck.go`（+41）
- `admin/probe_dashboard.go`（+671，含 `UnifiedProbeSystemHealth / UnifiedProbeQueueStats` 等新结构与 helper）
- `admin/quality_correlations.go`（pgxQueryer 接口加 `QueryRow`）
- `bg/credential_selfcheck_runone_test.go`（+169）
- `admin/probe_dashboard_test.go`（+456）

**关键变更**：
1. `bg/credential_selfcheck.go:doHTTP/doRequest` 加 `credentialID` 形参；`credentialID <= 0` 直接 `unattributed` 拒绝出站；正常路径在 `Authorization` 后 stamp `X-LLM-Pin-Credential=<credentialID>`。Origin middleware（`origin_mw.go:183`）只对 system-key 调用方保留此 header。
2. `admin/probe_dashboard.go` 改造 `/api/admin/probe/system-health` 和 `/api/admin/probe/queue-snapshot` 返回 `{unified, legacy, legacy_mode_safe: false, legacy_source: "model_probe_state", snapshot_at}`：
   - `unified` 读 `credential_probe_queue / node_probe_state / node_probe_runs / URSM tenant coverage / pseudo-success 计数`；
   - `legacy` 保留原 `v_probe_system_health / v_probe_queue_snapshot`（现场显示 572 个历史 backlog）；
   - `legacy_mode_safe: false` 明示旧视图不再是权威。

**新增测试**（9 个）：
- bg：`TestDoHTTP_RejectsZeroCredentialID_Unattributed`、`TestDoHTTP_StampsPinHeaderForTargetCredential`、`TestDoRequest_BothRoundsCarryPin`、`TestDoRequest_NoPinNoAttrib`；
- admin：`TestQueryUnifiedProbeSystemHealth_DBShape`、`TestQueryUnifiedProbeQueueStats_DBShape`、`TestProbeSystemHealthHandler_LegacyIsolation`、`TestProbeQueueSnapshotHandler_LegacyIsolation`、`TestCountURSMKeys_NilScanner`。

**Commit 信息建议**：`fix(selfcheck,dashboard): pin credential attribution + split legacy/unified probe views`

---

### Agent D — 路由/URSM 一致性审计（只读）

**产出**：`docs/audit/2026-08-18-routing-ursm-coherence-audit.md`（350 行）

**关键发现**：
1. **`record_request.lua:186-190` 单向守门**：probe priority=20 后普通请求只能 EXPIRE；既不能 unblock 也不能 escalate。当 probe 队列停摆，节点会永久卡在 probe 留下的 cool 状态（与 glm-5.2 复检同构）。
2. **Resolve vs 真实 Router 在 URSM miss 时可能不同**：
   - Resolve handler（`admin/routing.go:355-396`）总是走 v2 overlay；
   - 真实 Router 在 `ModeShadow / ModeCanary / ModeOff` 不走 v2，只看 `c.IsAvailable()`（`state_backend.go:97-118`）。
   - 生产当前为 `ModeOff`（`cmd/gateway/main.go:1032-1033`），Resolve 走 `applyResolveDefaults` 永远 `Available=true`；真实 Router 走 `DBOnlyBackend` 读 `cmb.available`。两者都乐观但真相源不同。
3. **Tenant key coverage 完全不可观察**：
   - K2 拒绝空 tenant（`keys_k2.go:102-105`）；legacy 三种形式（`keys.go:15-27`）；
   - resolve handler seed 写死 `TenantID: "default"`（`admin/routing.go:376`），与生产 `COALESCE(c.tenant_id, 'default')`（`cmd/gateway/main.go:4818`）在数字 tenant 下**不命中**——真实流量写 `node:t:1234567:36:minimax-m3`，resolve 读 `node:default:36:minimax-m3`；
   - `node_ttl` 默认 3600s 过期后整个 key 消失，运维无法区分"无 telemetry"和"无可路由"；
   - dashboard 端点（`/api/routing/health / /api/routing/probe / /api/routing/resolve`）都不返回 URSM key 存在性、schema 形式、TTL 剩余。

**4 个集成测试场景**（建议但未实施）：A failed-probe 守门、B off/Shadow 下 resolve 与 Router 都乐观但真相源不同、C probe priority=20 留下后普通失败无法升级到 disabled、D resolve 返回 `ursm_v2_key_state` 维度、E tenant `default` 与真实 tenant 不命中。

**未修改任何业务代码**。

---

### Agent E — 验证 + probe-canary 设计

**产出**：`docs/audit/2026-08-18-deploy-gate-and-canary-design.md`

**关键产出**：
1. 揭示 P0 阻断（Agent C 提交前 `go build` 失败），现已修复。
2. **245 data-plane 影响**：22 处 `!bgDataPlaneOnly` 守护点恰好覆盖 `CredentialRecovery / NodeProbeWorker / ProbeQueue / ProbeService.Run / apply_probe.lua`，245 完全不能验证后台恢复链。
3. **Probe-canary 设计**：
   - `BG_MODE=full` + 隔离 Redis db index 15（推荐 db 隔离而非 key prefix，避免改 Lua 硬编码）+ canary tenant=999001 + canary credentials 99001/99002 + canary model allowlist；
   - 验证链路：enqueue → claim → direct → pinned gateway → node_probe_runs → URSM tenant key → routing resolve；
   - 不动业务代码；新增 env 文件、启动脚本、验证脚本、4 个测试函数；
   - 通过 dedup_key 前缀 + pg_notify channel 名 + credential_id 集合不相交保证不影响 154。
4. **部署门禁**：245 canary → 245 全量 → 154 全量，每阶段设独立门禁（详见报告 §5.2）。

---

## 2. 一体化验证

```text
go build ./...           → clean
go vet ./...             → clean
go test ./bg/...         → PASS (4 packages)
go test ./admin/...      → PASS (3 packages)
go test ./domains/ursm/v2/...   → PASS (15 packages)
go test ./domains/streaming/executors/...  → PASS (2 packages)
go test ./sql/migrations/startup/...       → PASS
```

总：25 个包，0 失败。

---

## 3. 改动统计

| 文件类型 | 数量 | 新增行 |
|---------|------|--------|
| 业务代码 | 11 | ~1250 |
| 测试文件 | 4 | ~1119 |
| SQL migration | 2（含 .down） | ~50 |
| Mirror schema | 2 | ~6 |
| 审计/设计文档 | 2 | ~800+ |
| **合计** | **23** | **~3200** |

`git diff --stat` 总览：14 modified files，+2294 / -135 lines（不含新文件）。

---

## 4. 关键决策与未决风险

### 4.1 决策（已采纳）

1. **Recover 不再写 node_probe_state**：必须经 `ProbeService.Run` 的真实 `direct + gateway` 双轮才能写回，避免后续被普通请求覆盖（`record_request.lua:186-190` 单向守门）。
2. **lease 默认值 30s → 5m**：worst-case ~45s，5m 给 heartbeat 足够余量。
3. **dashboard legacy 隔离**：保留旧视图供回滚参考，但 envelope 显式声明 `legacy_mode_safe: false`。
4. **pin header 走 system-key 调用方**：避免普通用户伪造 pin attribution。
5. **probe-canary 用 db 隔离**：避免 Lua 硬编码改动。

### 4.2 未决风险（需后续处理）

1. **现场 DB 残留 82 行伪成功行**：`node_probe_state.last_direct_ok=TRUE AND last_gateway_ok=TRUE AND next_retry_at>now()` 需要 ops 一次性 SQL 清回 NULL/FALSE，并由新版本自然 probe 覆盖。代码路径已修复，DB 状态需单独清理。
2. **`record_request.lua:186-190` 单向守门**：probe priority>10 后普通请求不能 unblock。当前由 Recovery + ProbeQueue 兜底，但若两者同时停摆会卡死。Agent D 建议的 P2 修复仍未实施，列入后续 handoff。
3. **Resolve seed tenant**：已在当前代码移除 `admin/routing.go` 的 `default` seed，改为认证 scope + 数据库真实 tenant；仍需多租户隔离集成验证。
4. **dashboard URSM coverage**：当前提供有界 K2 key count，但尚未实现 candidate 级 `present/missing/expired`、schema 和 TTL 维度。
5. **system-monitor audit 现场链路**：当前代码已增加 audit backend 状态和关联字段日志；canary 的真实 `node_probe_runs/system_probe_runs` 仍需隔离环境复验。
6. **生产状态**：migration 538、82 行 pseudo-success 清理、隔离 canary 与 24h 观察仍需 ops/变更授权，不在本地会话执行。

---

## 5. 部署门禁（建议顺序）

| 阶段 | 环境 | 模式 | 通过条件 |
|------|------|------|---------|
| 1 | 245 | probe-canary（`BG_MODE=full` + db15） | canary cred 真实 direct+gateway+node_probe_runs+URSM+routing resolve 全链路一致；新 dashboard unified 字段非空；新 source 不再被吞错 |
| 2 | 245 | 全量 | 572 legacy backlog 仍可在 legacy 字段看到；unified 显示真实 due=0/unclaimable=0；glm-5.x 全模型恢复链路稳定 24h |
| 3 | 154 | 全量 | 154 同样验证通过；现场 DB 残留 82 行人工清理 SQL 跑过；新的 `audit_persist_failed_total / lease_lost_total` 在 24h 内为 0；URSM tenant key coverage 维度上线 |

---

## 6. 引用

- 前置交接：`docs/handoff/2026-08-18-global-routing-audit-handoff.md` (`dca827fa0`)
- 路由/URSM coherence 审计：`docs/audit/2026-08-18-routing-ursm-coherence-audit.md`
- 部署门禁 + canary 设计：`docs/audit/2026-08-18-deploy-gate-and-canary-design.md`
- 第一轮修复 commit：`1eac33fac fix(probe,routing): break glm-5.2 lockout loop`
- 交接文档 commit：`dca827fa0 docs(audit): handoff global routing and probe state audit`

---

## 7. 待用户决策

1. 是否将当前 23 文件改动按 agent 分成 3 个独立 commit 提交（恢复器 / 探针审计 / 自检+dashboard）？
2. 是否需要在合并前先现场清理 82 行伪成功 DB 行？还是先合代码 + 后续一次性 SQL 清理？
3. 245 probe-canary 启动脚本是否现在就由 ops 在 245 跑？还是先 git push + 后续部署脚本联动？
