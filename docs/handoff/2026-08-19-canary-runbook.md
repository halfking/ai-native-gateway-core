# 隔离 Probe-Canary 验证 Runbook

> 读者：拿到 ops 批准、独立 PG/Redis、canary tenant/credential/model allowlist 后执行验证的工程师
> 范围：**仅在隔离环境**内执行 enqueue → claim → 双轮探测 → audit → URSM K2 → routing resolve 的只读核验
> 状态：本 runbook 是空运行；等待 ops 输入（见 `2026-08-19-canary-inputs-from-ops.md`）后方可执行
> 前置交接：`/tmp/handoff-20260819-probe-canary-audit-fix.md`、`docs/handoff/2026-08-19-canary-evidence-and-status.md`

---

## 0. 硬性前提（任意一条不满足 → 终止）

1. **PG/Redis 完全独立**：
   - `LLMGW_PG_DSN` 指向独立 PostgreSQL（与 245、154、生产的任何 schema/db 都不共享）
   - `LLMGW_REDIS_ADDR` 指向独立 Redis（独立 namespace 或独立实例）
   - 启动 fail-closed：`cmd/gateway/main.go:695-705` 要求 strict-canary 模式下 `redisClientForCache != nil` 且 `dbConn.Enabled()`，否则进程退出
2. **Allowlist 已注入且唯一**：
   - `URSM_V2_STRICT_CANARY=true`、`URSM_V2_MODE=canary`、`URSM_V2_CANARY_PERCENT=0`
   - `URSM_V2_CANARY_TENANTS`、`URSM_V2_CANARY_CREDENTIALS`、`URSM_V2_CANARY_MODELS` 各至少 1 项，全部为本环境私有
   - `URSM_V2_REDIS_KEY_PREFIX` 非默认值且以 `:` 结尾（`domains/ursm/v2/scope.go:72,77`）
3. **环境资产隔离**：
   - canary tenant 与生产 tenant 字符串不可重叠
   - canary credential ID 与生产 credential ID 不重叠（建议新建，勿复用历史 ID）
   - canary raw model 字符串与生产 raw model 不重叠（如果上游 alias 共享，使用独立 prefix）
4. **本环境不属于诊断候补**：不允许把 245/154 当作隔离 canary 验证环境（handoff §7）

---

## 1. 启动前静态校验（10 分钟内可完成，不修改任何状态）

### 1.1 二进制与配置检查

```bash
# 二进制版本必须能溯源到 commit 47482be4f（或更新同源 commit）
./bin/llm-gateway-go -version   # 期望 build 注释里出现 47482be4f / audit: close probe canary state gaps
git -C /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4 log -1 --format='%H %s'
```

不通过 → 终止：当前 `version.json` 的 `build_seq=1621` 是历史元数据，不能证明 `47482be4f` 已部署。

### 1.2 Strict-canary 启动校验（不发起任何流量）

```bash
# 期望：进程退出码 != 0，stderr 出现下列其一：
#   "ursm.v2: strict canary requires reachable isolated Redis"
#   "ursm.v2: strict canary requires reachable isolated PostgreSQL"
# 任一出现即视为 fail-closed 启动已生效
LLMGW_PG_DSN=... LLMGW_REDIS_ADDR=... \
  URSM_V2_STRICT_CANARY=true URSM_V2_MODE=canary URSM_V2_CANARY_PERCENT=0 \
  URSM_V2_CANARY_TENANTS=__placeholder__ \
  ./bin/llm-gateway-go 2>&1 | tee /tmp/canary-startup.log
```

随后用真实 allowlist 重跑一次，要求正常 listen。

### 1.3 Migration 538 在独立 PG 上预演（不写生产）

```sql
-- 仅连接到隔离 PG 执行；命中数为 0 表示升级前干净
SELECT conname, pg_get_constraintdef(oid)
FROM pg_constraint
WHERE conrelid = 'public.node_probe_runs'::regclass
  AND conname = 'node_probe_runs_trigger_kind_check';

SELECT conname, pg_get_constraintdef(oid)
FROM pg_constraint
WHERE conrelid = 'public.credential_probe_queue'::regclass
  AND conname = 'credential_probe_queue_source_check';
```

预期（旧 425 状态）：
- `node_probe_runs_trigger_kind_check` 包含 `request_failure | manual | credential_recovery | sync_request`
- `credential_probe_queue_source_check` 不包含 `selfcheck`

随后用隔离 PG 启动一次让 migration runner 应用 538，再跑同一查询：
- 预期 `node_probe_runs_trigger_kind_check` 额外包含 `periodic | admin | integrity_probe_planner | selfcheck | external_async`
- 预期 `credential_probe_queue_source_check` 额外包含 `selfcheck`

> 538 是 startup 迁移，runner 会自动应用；这里只是验证升级前后的实际行级行为。

---

## 2. 唯一 Task ID 全链路验证（核心）

### 2.1 Task ID 命名规范

```
task_id = canary-<UTC timestamp YYYYMMDDTHHMMSSZ>-<uuid8>
dedup_key = task_id    # ProbeQueueTask.DedupKey == task_id
```

将 `task_id` 记录到 `docs/handoff/2026-08-19-canary-<task_id>.md`（仅本环境产出，不污染生产 docs），便于下面 6 个只读 SQL 复用同一个 ID 串。

### 2.2 注入任务（enqueue）

```bash
# 用 admin API 注入；严格使用 allowlist 中的 tenant/credential/model
curl -sS -X POST http://127.0.0.1:8080/api/admin/probe/tasks \
  -H "X-Admin-Token: $LLMGW_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"tenant_id\": \"$CANARY_TENANT\",
    \"credential_id\": $CANARY_CRED_ID,
    \"raw_model\": \"$CANARY_MODEL\",
    \"command\": \"chat\",
    \"mode\": \"single\",
    \"source\": \"admin\",
    \"dedup_key\": \"$TASK_ID\"
  }"
```

期望响应 `200 OK`，body 含 `id`（即 `credential_probe_queue.id`）。

**Fail-closed 注入**：故意用 allowlist 外的 tenant/credential/model 重发一次：

```bash
curl -sS -o /tmp/scope-out.json -w "%{http_code}\n" -X POST ...  # 期望 5xx 或 4xx
```

预期：服务端 `bg/probe_queue.go:285-287` 立刻 `ErrProbeOutOfScope`，**不应**写入 `credential_probe_queue` 任何新行。

### 2.3 只读核验 #1 — enqueue 落库与 scope fail-closed

```sql
-- A. task_id 在 queue 里有且仅有一行（INSERT ON CONFLICT 也会 idempotent）
SELECT id, tenant_id, credential_id, raw_model, source, dedup_key,
       status, attempt, next_run_at, expires_at, lease_token
FROM credential_probe_queue
WHERE dedup_key = '$TASK_ID';

-- B. scope 外尝试不落库
SELECT count(*) AS out_of_scope_queue_rows
FROM credential_probe_queue
WHERE tenant_id <> '$CANARY_TENANT'
   OR credential_id <> $CANARY_CRED_ID
   OR raw_model <> '$CANARY_MODEL';
-- 期望：0（隔离环境应当纯净；如有 >0 立即停止 runbook 并报告）
```

### 2.4 等待 Claim + 执行（被动观察）

```bash
# 触发一个 claim tick（具体 API 取决于启动路径；最简单是让 30s 周期 worker 自然 claim）
# 或者直接打 POST /api/admin/probe/tasks 后等待 ~60s，观察 SSE 自检 tab 出现 in-flight → ok/fail
sleep 60
```

期间可用只读 SQL 监控 lease 状态：

```sql
SELECT id, status, attempt, lease_until, lease_token, last_error
FROM credential_probe_queue
WHERE dedup_key = '$TASK_ID';
```

合法状态序列：`ready → running（lease_until 在 ProbeQueueLeaseDefault=5m 内）→ completed/failed`。

### 2.5 只读核验 #2 — audit INSERT 真正写入 + trigger_kind 合法

```sql
-- A. audit 行存在；trigger_kind 必须是 source 字段的实际值（这里 'admin'）
-- 注意：535 之前的代码会在 source='selfcheck'/'periodic'/'integrity_probe_planner'/'admin'
-- 时被 CHECK 拒掉并被 _, _ = ... 吞掉；538 + 47482be4f 修复后应当能直接写入
SELECT run_id, credential_id, raw_model, trigger_kind, source,
       success, latency_ms, http_status, error_code,
       probe_kind, scope_tenant
FROM node_probe_runs
WHERE dedup_key = '$TASK_ID'
ORDER BY started_at DESC;

-- B. 关联自检流；selfcheck 的 audit 也走 system_probe_runs（如有该路径）
SELECT run_id, credential_id, raw_model, trigger_kind, success,
       latency_ms, http_status
FROM system_probe_runs
WHERE dedup_key = '$TASK_ID'
ORDER BY started_at DESC;

-- C. fail-closed 度量必须为 0（这两条是修复的关键证据）
-- 等价 SQL：从 Prometheus 拉 counter
--   llmgw_node_probe_audit_persist_failed_total
--   llmgw_node_probe_audit_unknown_source_total
--   llmgw_node_probe_lease_lost_total
-- 期望：本环境全部为 0（隔离环境无历史噪音）
```

### 2.6 只读核验 #3 — URSM K2 key 真正落地

```bash
# 用隔离 Redis CLI 直接看 K2 key 是否存在
redis-cli -h $REDIS_HOST -p $REDIS_PORT --scan --pattern "${URSM_V2_REDIS_KEY_PREFIX}${CANARY_TENANT}:${CANARY_CRED_ID}:${CANARY_MODEL}*"
```

预期（依据 `domains/ursm/v2/probe.go:54-67` + `store.NodeKeyForTenant`/`store.K2NodeKeyForTenant`）：
- `URSM_V2_KEY_SCHEMA_MODE=legacy` → 只看到 legacy key 存在
- `URSM_V2_KEY_SCHEMA_MODE=dual` → legacy + K2 同步存在（`ApplyProbeDualScript` 原子双写）
- `URSM_V2_KEY_SCHEMA_MODE=canonical` → 只看到 K2 key 存在（`keys[0]=k2Key`）

TTL 反向校验（基于 `domains/ursm/v2/probe.go:30,74-84`）：

```bash
redis-cli -h $REDIS_HOST -p $REDIS_PORT TTL "${URSM_V2_REDIS_KEY_PREFIX}${CANARY_TENANT}:${CANARY_CRED_ID}:${CANARY_MODEL}"
# 期望 ≥ probeWriteTTLFloor = 7h（25200 秒），覆盖 NodeProbeBackoffChain 6h 上限
```

scope 外写入零核验：

```bash
redis-cli -h $REDIS_HOST -p $REDIS_PORT --scan --pattern "${URSM_V2_REDIS_KEY_PREFIX}*" \
  | awk -F: '{ for(i=1;i<=NF;i++) if($i=="'$CANARY_TENANT'") print; }' \
  | sort -u
# 期望：所有出现 CANARY_TENANT 的 key 全部命中 allowlist 的 credential/model 组合
```

---

## 3. Routing Resolve 验证（不破坏生产流量）

### 3.1 主动发起一次 strict-canary 路由请求

```bash
# 使用 canary tenant 的有效凭据 + canary model 发起一个 chat 请求
curl -sS -X POST http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $CANARY_USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"model\": \"$CANARY_MODEL\",
    \"messages\": [{\"role\":\"user\",\"content\":\"ping\"}]
  }"
```

期望：路由走 `domains/streaming/executors/router.go:493-516` 的 `ModeCanary + StrictCanary` 分支，最终从 allowlist 候选中选出 `CANARY_CRED_ID`。

### 3.2 只读核验 #4 — resolve 走 URSM v2 状态源

```sql
-- 通过请求日志（如果启用了 requestjourney）反查 routing state source
SELECT request_id, tenant_id, model, state_source, candidate_credential_id,
       candidate_model, used_ursm_v2, fallback_reason
FROM requestjourney_events
WHERE request_id = '$REQUEST_ID'
ORDER BY ts;
```

合法序列：`StateSourceCanary`（URSM v2 命中）→ 不应出现 `StateSourceFallback`（除非 allowlist 内无候选，这是另一个问题）。

### 3.3 Fail-closed resolve：故意把 allowlist 临时去掉一项

仅在隔离环境临时改环境变量重启 gateway：

```bash
URSM_V2_CANARY_CREDENTIALS="__empty__"  # 让 allowlist 校验失败 → 进程退出
./bin/llm-gateway-go 2>&1 | grep "URSM_V2_STRICT_CANARY"
```

预期日志：`URSM_V2_STRICT_CANARY requires non-empty tenant, credential, and model allowlists`（`domains/ursm/v2/scope.go:71-73`）。进程退出码 != 0。

---

## 4. Candidate-level URSM Coverage / TTL / Schema 观测

> 截至 `47482be4f`，candidate-level 观测尚未实现；本节是验证它是否存在、若不存在则记录缺口，不要求本会话内补实现。

### 4.1 现有可观测点（白盒）

```sql
-- URSM K1/K2 key 的全局覆盖：按 schema_mode 分桶
SELECT
  CASE
    WHEN key LIKE '${URSM_V2_REDIS_KEY_PREFIX}%:k1:%' THEN 'k1'
    WHEN key LIKE '${URSM_V2_REDIS_KEY_PREFIX}%:k2:%' THEN 'k2'
    ELSE 'other'
  END AS schema_bucket,
  count(*) AS n_keys
FROM (
  SELECT redis_key_name AS key FROM ursm_audit_log WHERE ts > now() - interval '1 hour'
) t
GROUP BY 1;
```

> `ursm_audit_log` 是观察项；如果当前实现没有这张表，记录为缺口，不臆造。

### 4.2 TTL / schema / read-source 缺口（必须如实记录）

| 观测维度 | 期望 | 当前实现状态（按 commit `47482be4f`） | 行动 |
|---------|------|-------------------------------------|------|
| candidate-level `present/missing/expired` 计数 | per (tenant,credential,model) 维度 | 缺口：现有 `apply_probe.lua` 仅返回成功/失败布尔 | 记入候选实现清单 |
| TTL 剩余秒数分布 | histogram，按 schema bucket | 缺口：仅 Redis TTL 命令可外部采样 | 记入候选实现清单 |
| read-source parity（resolve 走 legacy vs K2） | metric | 部分：router 有 `recordOuterSource(StateSourceCanary/Fallback/Off)`，但未区分 K1/K2 | 记入候选实现清单 |

不要在本 runbook 中假装上述观测已经存在；如果 ops 在评审时把它们当成已交付，会破坏隔离验证的可信度。

---

## 5. 24h 观察窗口

启动一次连续运行（最小化清理脚本，避免干扰观察）：

```bash
# 后台跑 24h：每 60s 拉一次关键 metric + 一次只读 SQL
nohup bash -c '
  while true; do
    ts=$(date -u +%Y%m%dT%H%M%SZ)
    echo "=== $ts ===" >> /tmp/canary-24h.log
    curl -sS http://127.0.0.1:8080/metrics | grep -E \
      "llmgw_node_probe_audit_persist_failed_total|llmgw_node_probe_audit_unknown_source_total|llmgw_node_probe_lease_lost_total|llmgw_ursm_v2_out_of_scope_total" \
      >> /tmp/canary-24h.log
    psql "$LLMGW_PG_DSN" -c "
      SELECT count(*) FILTER (WHERE trigger_kind NOT IN ('request_failure','manual','credential_recovery','sync_request')) AS new_source_rows
      FROM node_probe_runs
      WHERE started_at > now() - interval '5 minutes';
    " >> /tmp/canary-24h.log 2>&1
    sleep 60
  done
' &
```

### 5.1 通过条件（任意一条失败 → 不进入 245 评估）

| 检查项 | 通过阈值 | 来源 |
|-------|---------|------|
| `audit_persist_failed_total` 增量 | 24h 内 = 0 | `47482be4f` 修复目标 |
| `audit_unknown_source_total` 增量 | 24h 内 = 0 | 538 迁移已覆盖所有 source |
| `lease_lost_total` 增量 | 24h 内 = 0 | `ProbeService.Run` heartbeat 修复 |
| `credential_probe_queue` 残留 `status='running'` | 任何时刻 = 0 | heartbeat 保证 |
| URSM K2 key TTL | 持续 ≥ 7h | `probeWriteTTLFloor=7h`（`probe.go:30`） |
| scope 外写入（Redis key 或 queue row） | 24h 内 = 0 | `ErrOutOfScope` fail-closed |

### 5.2 回滚阈值（任意一条触发 → 立即隔离环境清理）

- `audit_persist_failed_total` 增量 > 0
- URSM scope 外写入 > 0
- `splitStrictCanaryCandidates` 出现 0 个 scoped 候选但路由仍命中（candidate filter bug）
- 任何 panic / OOM / 进程重启循环

回滚动作：删除隔离环境实例（不让任何 canary 状态泄露到共享组件），不依赖生产 ops；本环境的失败不应牵连 245 / 154 的评估。

---

## 6. 隔离环境清理（runbook 完成后执行）

1. 删除隔离 Redis namespace 中的所有 canary key
2. `TRUNCATE` 隔离 PG 中的 `credential_probe_queue` / `node_probe_runs` / `system_probe_runs` 本次验证产生的行（保留 schema）
3. 保留 24h 观察日志到 `docs/handoff/2026-08-19-canary-<task_id>.md` 作为只读证据
4. 销毁 canary tenant/credential/model allowlist 配置

**严禁**：把任何 canary 凭据、key prefix、SQL 模板带回生产；245 / 154 的 allowlist 由 ops 单独签发。

---

## 7. 与 245 / 154 的关系

- 本 runbook 通过 + 24h 观察通过 → 才能向 ops 申请 245 canary 评估（245 与生产共享 PG，**仍不是真正的隔离**，仅作为预发烟囱）
- 245 观察通过（独立 24h）→ 才能申请 154 生产评估
- 154 通过 ops 单独授权：见 `docs/handoff/2026-08-19-canary-inputs-from-ops.md` §4

本 runbook 不包含 245 / 154 的任何部署 / SQL / systemd 操作；执行人需独立授权。
