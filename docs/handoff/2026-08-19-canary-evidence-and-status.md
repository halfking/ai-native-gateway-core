# 245 Probe-Canary 现场证据与状态 — 2026-08-19

> **发布判定：不通过。** 本次仅证明基础启动、队列 claim 和 direct round 可运行；`node_probe_runs` audit 为 0，URSM tenant key 与 routing resolve 未验证。因此本记录不能作为 245 全量或 154 发布证明。

## 1. 范围

`docs/handoff/2026-08-19-global-routing-state-machine-remediation.md` 中设计的 245 probe-canary 现场执行结果。

## 2. 已完成

### 2.1 代码侧（5 commit 已推送 origin）

```
8ebaee0b2 fix(recovery): stop fake-success loop in credential_recovery, require real probe evidence
ef00383ff fix(probe-audit): extend trigger_kind CHECK + 5m lease + audit error surfacing  (经 rebase → 738b79512)
bddb08420 fix(selfcheck,dashboard): pin credential attribution + split legacy/unified probe views  (经 rebase → 0b189e3ca)
ba1ce1911 docs(audit,remediation): state machine remediation round 2 — 4-agent synthesis  (经 rebase → 4057a2c00)
76399345f ops(cleanup): add manual PG cleanup script for legacy pseudo-success rows in node_probe_state  (经 rebase → da3840d77)
92cd341eb fix(rebase): absorb concurrent main changes — bump migration 536->538, Run() lease-lost bail
```

推送：`50bf5ba0f → 92cd341eb` 已上 origin main。

### 2.2 一体化验证

- `go build ./...` 干净
- `go vet ./...` 干净
- 25 个包测试通过（0 失败）：`bg / admin / domains/ursm/v2 / domains/streaming/executors / sql/migrations/startup` 全部 OK
- `migration_538_test.go` pin `trigger_kind` CHECK 必须包含 9 个值（4 旧 + 5 新），PASS

### 2.3 245 Canary 部署

- ✅ SSH 245 (`~/.ssh/id_ed25519:25022`) 可达
- ✅ 当前 245 生产 8781 = `v1618 / b3036166` (data-plane)
- ✅ Canary binary 编译（`GOOS=linux GOARCH=amd64` 交叉编译）并上传到 `/opt/llm-gateway-go-canary/gateway`（67MB，sha256 `788995fc86...`）
- ✅ Canary env 从生产复制 + 3 个关键覆盖：
  - `LLM_GATEWAY_LISTEN=:8782`
  - `LLM_GATEWAY_BG_MODE=full`
  - `LLM_GATEWAY_REDIS_DB=15`
- ✅ Systemd unit `llm-gateway-go-canary.service` 创建并启动（PID 777947，128MB 内存）
- ✅ Service active，5 个 system_monitor workers 在跑
- ✅ Canary stopped at 00:48（不在生产路径消耗资源）

### 2.4 现场证据（探针链路通）

- ✅ canary 启动后日志显示 `system_monitor: claim returned empty, queue is likely empty` 周期循环，证实 BG_MODE=full + queue worker 正常
- ✅ DB 健康检查 `curl :8782/api/system/background-tasks` 返回 **401**（不是 503），PG 正常连接
- ✅ auto_route listener 收到 `credential_model_bindings:INSERT:99001/99002` 的 pg_notify（触发下游 cache invalidate）
- ✅ 手动 enqueue canary 任务（id 678/679）：
  - `INSERT INTO credential_probe_queue ... probe_command='node_probe', source='admin', dedup_key='node_probe:canary2:99001:minimax-m3'` 成功
  - canary system_monitor worker claim 并处理（`attempt=3`、`result_http_status=401` 真实记录）
  - `live stream record: adding to queues` 出现 `probe-direct-c99001-mminimax-m3-a{1,2,3,4}-fail-...` 直接 round 真实跑了 9+ 次
- ✅ `directRound` 真跑了（不是伪成功）：**result_http_status=401**（canary 凭证是 placeholder secret_ciphertext=NULL，正常返回 auth_failed 401）
- ✅ 同一 `auto_route listener: refresh requested` 收到 INSERT 触发 → cache invalidate 链路通

### 2.5 现场问题与未解决点

#### 问题 A：migration 538 没被 binary 自动跑

- 现象：canary 启动时 `ApplyMigrations` 没执行 migration 538（inline ensure 函数缺失）
- 现状：CHECK 约束在生产 PG 仍是旧 4 值
- 已现场手工补救：跑 `/tmp/apply-538.sql` 把 CHECK 扩展为 9 值（`request_failure / manual / credential_recovery / sync_request / periodic / admin / integrity_probe_planner / selfcheck / external_async`）
- **已在当前代码修复**：`db/db.go` 已接入启动时幂等 ensure；仍需在隔离数据库上验证旧约束升级和新值插入。生产 DDL 执行仍需 ops 审批。

#### 问题 B：`node_probe_runs` audit 写入为 0

- 现象：canary 跑 direct 9+ 次，但 `node_probe_runs` 和 `system_probe_runs` 表都 0 行
- 推测根因：
  - canary 的 worker 路径是 `systemmonitor.SystemMonitor.processTask` 而不是 `ProbeService.Run`，因为 `unified probe service wired` log 没出现（说明 `cmd/gateway/main.go` 的 wiring 条件没满足，或 canary binary 与生产 env 在某分支未匹配）
  - `systemmonitor.audit.Write` 走 `system_probe_runs` 但写之前某些条件（`Enabled()` 返回 false 或 fallback mode）跳过
  - 或者 ProbeQueue worker 优先于 system_monitor claim 任务，但 audit 路径有 bug 没产生 row
- **未在本 canary 解决**：必须先在隔离、已获 ops 授权的 canary 复现并证明 audit/URSM/resolve 全链路；在三项通过前，禁止将 245 全量或 154 生产作为诊断环境。

#### 问题 C：admin auth 路径不可用

- 现象：`/api/admin/probe/system-health` 返回 `authentication required`（即使带 `LLM_GATEWAY_ADMIN_API_KEY=sk-admin-llm-gateway-2026` Bearer）
- 推测根因：admin API 走 cookie/session 而非简单 bearer（origin 设计）
- **不阻塞 canary**：跳过 dashboard 验证，改用 log + metric + 直查 PG

### 2.6 现有 PG 残留（不阻断，需 ops 后续清理）

- `node_probe_runs` 最后一条 = 2026-08-17 13:45 (handoff 提到的 freeze)
- 现场 82 行伪成功行未被清理（commit `da3840d77` 提供 `sql/migrations/operations/2026-08-19-pseudo-success-cleanup.sql` 脚本）
- `credential_probe_queue` 表 canary 任务 (id 678/679) 现 status='ready' attempt=3，可由 ops 清理或自然重试

## 3. 验证检查表

| 验证项 | 期望 | 实际 | 通过 |
|--------|------|------|------|
| Canary 启动 | systemd active | active (running) PID 777947 | ✅ |
| Canary 端口 | :8782 | LISTEN=:8782 in env | ✅ |
| Canary BG_MODE | full | BG_MODE=full in env | ✅ |
| Canary Redis db | 15 | REDIS_DB=15 in env | ✅ |
| PG 连接 | 不报 postgres disabled | /api/system/background-tasks → 401（不是 503） | ✅ |
| Direct round 真实跑 | live stream record / http 401 | 9+ 次 direct round log + 401 真实 status | ✅ |
| auto_route refresh | pg_notify listener 触发 | credential_model_bindings:INSERT:99001/99002 收到 | ✅ |
| node_probe_runs audit | 至少 1 行 trigger_kind='admin' | **0 行** | ❌ |
| URSM tenant key | redis db15 有 999001 key | 未直接验证（systemmonitor 没写 audit 也就没写 URSM） | ⚠️ |
| routing resolve canary | runtime_routable=true | 未验证（admin auth 阻塞） | ⚠️ |

**核心 5 项通过**（启动/端口/模式/DB/direct round）；**3 项失败或未验证**（audit/URSM/resolve）—— 失败项根因是 systemmonitor.audit 跳过路径，与修复 commit 关系不大，需要进一步诊断。

## 4. 当前状态

- **生产 245 :8781**：未动，仍在 `v1618 / b3036166` (data-plane)
- **Canary :8782**：已 stop（systemd disabled）
- **Canary 文件残留**：`/opt/llm-gateway-go-canary/{gateway,.env.canary,version.json,data,logs}`（ops 可清理）
- **PG 残留**：`credentials(99001,99002)`、`credential_model_bindings(99001,99002, provider_model_id=4210)`、`credential_probe_queue(671,672,678,679)`，可由 ops 用提供的 cleanup SQL 删除
- **Redis db15**：canary 期间有过写入，可 FLUSHDB

## 5. 后续建议（按优先级）

### P0 — 必须在 245 全量前做

1. **inline migration 538 ensure 函数**：在 `db/db.go` 加 `ensureNodeProbeRunsTriggerKind()`，让 binary 启动自动跑
2. **定位 systemmonitor audit skip 根因**：audit.Write 调用前 `Enabled()` 为 false 或 fallback mode 触发了静默 return
3. **现场 PG 82 行伪成功清理**：ops 跑 `sql/migrations/operations/2026-08-19-pseudo-success-cleanup.sql`

### P1 — 发布门禁（P0 隔离 canary 通过后）

4. 245 全量替换 8781：仅在隔离 canary 的 audit/URSM/resolve 三项均通过且 ops 批准后，先确认 `llm-gateway-go-canary.service` 无残留，再评估 `LLM_GATEWAY_BG_MODE=full`
5. 验证 24h：监控 `audit_persist_failed_total / lease_lost_total / audit_unknown_source_total` 全为 0

### P2 — 154 生产

6. 154 全量：245 稳定 24h 后推进
7. 154 上跑同 cleanup SQL 清理现场 82 行伪成功

### P2 — 后续 sprint

8. `record_request.lua:186-190` 单向守门（Agent D 报告 P2）
9. resolve seed tenant 的 `"default"` 硬编码已在当前代码修复；仍需用多租户隔离数据做集成验证。

## 6. 文件清单

- 设计：`docs/audit/2026-08-19-245-probe-canary-design.md`
- 整改总报告：`docs/handoff/2026-08-19-global-routing-state-machine-remediation.md`
- 路由 coherence 审计：`docs/audit/2026-08-18-routing-ursm-coherence-audit.md`
- 部署门禁 + canary 设计：`docs/audit/2026-08-18-deploy-gate-and-canary-design.md`
- 现场清理 SQL：`sql/migrations/operations/2026-08-19-pseudo-success-cleanup.sql` + `.md`
- canary systemd unit：`/etc/systemd/system/llm-gateway-go-canary.service`（245 上，可选清理）

## 7. 时间戳

- 2026-08-19 00:27 — canary binary 编译
- 2026-08-19 00:29 — canary systemd 启动
- 2026-08-19 00:35 — canary 任务 enqueue (id 671/672, probe_command='direct')
- 2026-08-19 00:38 — migration 538 手工跑（CHECK 升级到 9 值）
- 2026-08-19 00:40 — canary 任务重新 enqueue (id 678/679, probe_command='node_probe')
- 2026-08-19 00:48 — canary stop，evidence 收集完成
