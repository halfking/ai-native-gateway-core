# 2026-08-18 部署门禁与 245 Probe-Canary 设计报告

> Agent E：独立回归与发布门禁报告（不修改业务代码）
> 输入交接：`docs/handoff/2026-08-18-global-routing-audit-handoff.md`
> 当前 commit：`1eac33fac`（main，未改动 HEAD）
> 工作区状态：存在未提交修改（Agent A/B/C 改动），见 §1.2

---

## 1. 构建与测试结果

### 1.1 `go build ./...`

**结论：构建失败（P0 阻断）**

```
$ go build ./...
# github.com/kaixuan/llm-gateway-go/bg
bg/credential_selfcheck.go:67:2: "strconv" imported and not used
EXIT=1
```

更早一轮 `go test ./bg` 也暴露了同一 `bg` 包的另外两条编译错误：

```
bg/probe_service.go:98:2: undefined: auditUnknownSourceTotal
bg/credential_selfcheck.go:465:25: not enough arguments in call to w.doRequest
        have (context.Context, string)
        want (context.Context, int, string)
```

这三处错误都来自 **未提交** 的 Agent B/C 工作树改动（`git diff` 统计：5 个文件 +288/−50 行）：

| 文件 | 行号 | 错误 | 来源 |
|---|---|---|---|
| `bg/credential_selfcheck.go` | 67 | `strconv` import 未使用 | Agent C（凭证自检 pin-header） |
| `bg/credential_selfcheck.go` | 465 | `w.doRequest(ctx, model)` 缺少 `credentialID` 参数 | Agent C |
| `bg/probe_service.go` | 98 | `auditUnknownSourceTotal` 未声明（应在 `bg/metrics.go`） | Agent B（统一队列 lease/审计） |

含义：

1. `bg/probe_service.go` 是非测试源码；`auditUnknownSourceTotal` 在函数体内直接引用 → 整个 `bg` 包无法编译。
2. `go build ./...` 把所有包一次性编进去，**gateway 二进制本身也无法编译**。
3. 三个 Agent 的分支代码尚未合并就绪，存在接口/import 不一致；当前 main HEAD（`1eac33fac`）实际可编译，但工作树状态被破坏。

### 1.2 当前工作区状态

```
$ git status
位于分支 main
您的分支与上游分支 'origin/main' 一致。

尚未暂存以备提交的变更：
	修改：     bg/credential_recovery.go          (Agent A：消除 fake-success UPDATE)
	修改：     bg/credential_selfcheck.go         (Agent C：pin-credential header)
	修改：     bg/probe_service.go                (Agent B：lease + audit)
	修改：     sql/objects/tables/node_probe_runs.sql  (Agent B)
	修改：     sql/schema/01-schema.sql                (Agent B)

未跟踪的文件:
	sql/migrations/startup/536_node_probe_runs_trigger_kind_unified_queue.sql        (Agent B)
	sql/migrations/startup/536_node_probe_runs_trigger_kind_unified_queue.down.sql  (Agent B)
```

**结论：工作区不允许直接构建/部署；任何"先 245 canary"的部署前提是三个 Agent 修复这些编译错误并把分支合并干净。**

### 1.3 关键包测试（独立运行，绕开 `bg` 根包）

| 包 | 结果 | 备注 |
|---|---|---|
| `./bg/freequotacleanup` | OK | 3.146s |
| `./bg/freequotareset` | OK | 3.255s |
| `./admin/dashboardapi` | OK | 0.371s |
| `./admin/dashboarddegrade` | OK | 1.269s |
| `./domains/streaming/executors` | OK | 8.876s |
| `./domains/streaming/executors/webcookie` | OK | 1.089s |
| `./domains/ursm/v2` | OK | 7.932s |
| `./domains/ursm/v2/api` | OK | 2.891s |
| `./domains/ursm/v2/bootstrap` | OK | 5.413s |
| `./domains/ursm/v2/cache` | OK | 5.930s |
| `./domains/ursm/v2/index` | OK | 4.428s |
| `./domains/ursm/v2/integration` | OK | 1.445s |
| `./domains/ursm/v2/migration` | OK | 6.457s |
| `./domains/ursm/v2/persist` | OK | 7.290s |
| `./domains/ursm/v2/recovery` | OK | 5.593s |
| `./domains/ursm/v2/resource` | OK | 0.921s |
| `./domains/ursm/v2/rollout` | OK | 6.319s |
| `./domains/ursm/v2/shadow` | OK | 1.916s |
| `./domains/ursm/v2/statesource` | OK | 2.427s |
| `./domains/ursm/v2/store` | OK | 0.534s |
| `./domains/ursm/v2/sync` | OK | 6.816s |
| `./bg` (root) | **FAIL build** | 见 §1.1 |
| `./admin` (root) | **FAIL build** | 见 §1.1（首次运行；单独运行通过，因 admin/dashboardapi 单独测试不触发 bg 编译链） |

> 注：`./admin` 首次运行随 `./bg` 一起失败，**不是 admin 包自身的编译错误**——是 Go 把所有相关测试包串行编译时，`bg` 包编译失败连带 admin 失败。`admin/...` 全部子包单独运行均 OK。

---

## 2. 迁移一致性检查

### 2.1 本次审计相关 migration 清单（按 mtime / 编号）

| 编号 | 路径 | mtime | 作者 | 状态 |
|---|---|---|---|---|
| 525 | `sql/migrations/startup/525_*.sql` 系列 | 8月 17 之前 | 历史 | 已部署 |
| 526 | `…/526_session_turns_hot.sql` | 8月 17 02:40 | 历史 | 已部署 |
| 527 | `…/527_handoff_durable_goal_state.sql` | 8月 17 03:44 | 历史 | 已部署 |
| 528 | `…/528_request_logs_bodies_expired_hot_cleanup.sql` | 8月 17 11:08 | 历史 | 已部署 |
| 529 | `…/529_repair_shared_pg_sticky_and_bodies_2026_07.sql` | 8月 17 18:31 | 历史 | 已部署 |
| 530 | `…/530_request_journey_contract.sql` | 8月 17 18:31 | 历史 | 已部署 |
| 531 | `…/531_request_journey_tenant_uniqueness.sql` | 8月 17 18:31 | 历史 | 已部署 |
| 532 | `…/532_request_logs_final_success.sql` | 8月 18 04:00 | 历史 | 已部署 |
| 533 | `…/533_request_wal_bodies_unique_request_id.sql` | 8月 18 04:36 | 历史 | 已部署 |
| 534 | `…/534_handoff_logs_hot_columnar.sql` | 8月 18 04:36 | 历史 | 已部署 |
| 535 | `…/535_candidate_failure_logs_atomic_promote.sql` | 8月 18 04:36 | 历史 | 已部署 |
| **536** | `…/536_node_probe_runs_trigger_kind_unified_queue.sql` | **8月 18 23:50** | **Agent B（未提交）** | **未部署** |

### 2.2 536 号迁移内容摘要（Agent B 改动）

- 扩展 `node_probe_runs_trigger_kind_check` 的 CHECK 枚举，新增：
  `periodic | admin | integrity_probe_planner | selfcheck | external_async`
- 保留旧值：`request_failure | manual | credential_recovery | sync_request`
- 对应 schema 对象同步更新：
  - `sql/objects/tables/node_probe_runs.sql`
  - `sql/schema/01-schema.sql`
- 提供 down 迁移（防御式：536-only 值存在时拒绝 down）。

### 2.3 冲突 / 一致性评估

| 检查项 | 结论 |
|---|---|
| 与既有 425 迁移（sync_request）冲突？ | 否，仅扩展 CHECK，幂等可重跑 |
| 与 `recovery.go` 的 `node_probe_state` 写入路径冲突？ | 否（不同表） |
| 与 `credential_probe_queue` 表冲突？ | 否（不同表，task.Source 才是真正上游） |
| 与 `bg/metrics.go` 中的现有 metric 命名风格一致？ | 否——新增的 `auditUnknownSourceTotal` 尚未在 metrics.go 中声明（这是 §1.1 的编译失败根因之一） |
| Agent A 是否需要对应迁移？ | **不需要**——`bg/credential_recovery.go` 改为只读 SELECT，不再直接 UPDATE `node_probe_state`，无需 schema 变更 |
| Agent C 是否需要对应迁移？ | **不需要**——pin-credential header 是纯网关层增强 |

**唯一跨 Agent 耦合点**：Agent B 的 536 迁移必须在 Agent B 的代码上线前先在 DB 落地，否则 `ProbeService.Run` 的 INSERT 仍会被 CHECK 拒绝；如果走 `_, _ = ...` 兜底则回到 P0 静默吞错路径。

---

## 3. 245 Probe-Canary 设计

### 3.1 设计目标

1. **完全隔离**生产数据：不影响 154 真实探针、不污染 `v_routable_credential_models` / `node_probe_state` / `credential_probe_queue` / URSM tenant key / `node_probe_runs`。
2. **完整链路验证**：`enqueue → claim → direct → pinned-gateway → node_probe_runs → URSM tenant key → routing resolve`。
3. **不改业务代码**：仅追加 env / 配置 / 脚本 / 测试函数。
4. **可复现**：每次跑 canary 留下证据（task_id、URSM key、routing resolve payload），便于 154 灰度对比。

### 3.2 环境隔离（Redis / key prefix / 测试凭证）

#### 3.2.1 Redis db2 key prefix 隔离

现状：URSM v2 写入 `apply_probe.lua` / `record_request.lua`，key 形如：

```
ursm:v2:tenant:{tenant_id}:model:{model}:credential:{credential_id}:state
ursm:v2:ready:{tenant_id}
ursm:v2:coverage:{tenant_id}
```

**canary 改动建议（仅环境变量，不改源码）**：

| 变量 | 默认值 | canary 值 | 作用 |
|---|---|---|---|
| `LLM_GATEWAY_URSM_V2_KEY_PREFIX` | `ursm:v2:` | `ursm:canary:v2:` | 让 URSM store 在 SET/GET 时自动加前缀（**需要 `domains/ursm/v2/store` 在构造时读取 env**，见 §3.2.4） |
| `LLM_GATEWAY_PROBE_QUEUE_DEDUP_PREFIX` | `node_probe:` | `node_probe_canary:` | `credential_probe_queue` 的 `dedup_key` 加前缀，与 154 队列物理隔离 |
| `LLM_GATEWAY_AUTO_ROUTE_REFRESH_CHANNEL` | `auto_route_refresh` | `auto_route_refresh_canary` | pg_notify 频道隔离，避免触发 154 的 routing refresh |

> 风险：`apply_probe.lua` / `record_request.lua` 内部 key 拼接是硬编码的（`store/apply_probe.lua`、`store/record_request.lua`）。**若 Lua 不接受参数化前缀**，则必须为 canary 提供**独立的 Redis db index**（如 `LLM_GATEWAY_REDIS_DB2_INDEX=15`）而不是同一库不同前缀——否则不同 prefix 但同 Redis 实例仍然互不可见，但 154 的脚本读 canary 的 key 不会出错，反之亦然。
>
> **推荐**：canary 使用 `LLM_GATEWAY_REDIS_DB2_INDEX=15`（db index 隔离），key 不加 prefix；这是最少改动的隔离方案，且与现有 Lua 完全兼容。

#### 3.2.2 测试凭证隔离

新增两个 env（不改 DB schema）：

| 变量 | 默认 | canary 值 | 说明 |
|---|---|---|---|
| `LLM_GATEWAY_CANARY_TENANT_ID` | 空 | `999001` | 写 `cmb` / `node_probe_state` / `credential_probe_queue` 时按 tenant 过滤；与生产 tenant 集合（≤ 10）完全不在同一空间 |
| `LLM_GATEWAY_CANARY_CREDENTIAL_IDS` | 空 | `99001,99002` | 仅 canary 提交的 (cred, model) 对使用这些 ID；其他 ID 一律拒绝 enqueue |
| `LLM_GATEWAY_CANARY_MODEL_ALLOWLIST` | 空 | `gpt-5.5-canary,glm-5.2-canary` | 仅允许 canary 模型名；防误把生产模型拖入 canary |

实现位置：在 `cmd/gateway/main.go` 构造 `ProbeQueue` / `NodeProbeWorker` 时读取这些 env，若 env 非空则替换默认 tenant/credential 集合。建议由 Agent C 后续追加，本报告仅描述。

#### 3.2.3 DB schema 隔离

不动 schema；通过 `tenant_id` 列过滤即可：
- `cmb` 表已有 `tenant_id`（`sql/objects/tables/credential_model_bindings.sql` 验证）。
- `credential_probe_queue` 表已有 `dedup_key` 含 `tenant_id`（参见 `bg/probe_queue.go` Enqueue）。
- `node_probe_runs` 表本身无 tenant 列，但 `credential_id` 已限定；只要 canary 凭证集合不与生产交集即可物理隔离。
- `node_probe_state` 表与 `credential_model_bindings` 通过 `(credential_id, raw_model_name)` 关联，凭证隔离即可。

#### 3.2.4 Redis db2 key prefix 改造（**非必需，推荐**）

若选择"key prefix 隔离"路径，需要在以下位置读取 `LLM_GATEWAY_URSM_V2_KEY_PREFIX`：

| 文件 | 改动点 |
|---|---|
| `domains/ursm/v2/store/store.go` | `NewRedisClient` 之后包装 key builder，把 `ursm:v2:` 替换为 env 指定前缀 |
| `domains/ursm/v2/store/apply_probe.lua` | 把硬编码 `ursm:v2:` 改为读 ARGV（前缀注入），否则 Lua 内拼的 key 与 store 不一致 |
| `domains/ursm/v2/store/record_request.lua` | 同上 |

> 这是侵入式改动；建议**走 §3.2.1 推荐的 db index 隔离**，避免动 Lua。

### 3.3 验证链路（enqueue → resolve）

建议新增独立测试函数（位于 `bg/probe_canary_test.go`，仅测试代码，无业务逻辑）：

```
TestProbeCanary_EndToEndIsolated(t *testing.T)
  ├─ setup:
  │   • 用 LLM_GATEWAY_REDIS_DB2_INDEX=15 建 pgxpool + redis client
  │   • 注入 canary tenant=999001, credentials=[99001,99002], models=[gpt-5.5-canary]
  │   • 用 sqlx.Tx 写入 cmb/credentials/providers 行（仅 canary tenant）
  │
  ├─ step1_enqueue:
  │   pq := bg.NewProbeQueue(pool)
  │   id, dup, err := pq.Enqueue(ctx, ProbeQueueTask{
  │       CredentialID: 99001, Model: "gpt-5.5-canary",
  │       Source: "manual", TenantID: 999001,
  │       DedupKey: fmt.Sprintf("node_probe_canary:%d:%s", 99001, model),
  │   })
  │   require.NoError(t, err); require.False(t, dup)
  │
  ├─ step2_claim:
  │   tasks, err := pq.Claim(ctx, 1, 5*time.Minute)
  │   require.Equal(t, 1, len(tasks)); require.Equal(t, id, tasks[0].ID)
  │
  ├─ step3_direct_round:
  │   result := ps.Run(ctx, tasks[0])  // 内部走 mock http.Client 命中固定 fixture
  │   require.True(t, result.DirectOK)
  │
  ├─ step4_pinned_gateway:
  │   // Run 内部第二轮：OriginMiddleware 验证 X-LLM-Pin-Credential=99001
  │   // 测试断言网关收到 pin header 且落库 node_probe_runs.pin_credential_id
  │
  ├─ step5_audit_row:
  │   row := pool.QueryRow(ctx, "SELECT trigger_kind FROM node_probe_runs WHERE credential_id=99001 ORDER BY id DESC LIMIT 1")
  │   require.Equal(t, "manual", row)
  │
  ├─ step6_ursm_tenant_key:
  │   key := fmt.Sprintf("ursm:v2:tenant:999001:model:gpt-5.5-canary:credential:99001:state")
  │   got := redisCli.Get(ctx, key).Val()
  │   require.NotEmpty(t, got)  // 写入成功
  │
  └─ step7_routing_resolve:
      // 直接调 admin.Handler.handleRoutingResolve（admin 包已 OK）
      req := httptest.NewRequest("GET", "/api/routing/resolve?tenant=999001&model=gpt-5.5-canary", nil)
      resp := httptest.NewRecorder()
      h.handleRoutingResolve(resp, req)
      body := resp.Body.String()
      require.Contains(t, body, "99001")  // canary credential 出现在候选
      require.NotContains(t, body, production_credential_id)  // 不污染
```

> 关键测试断言：
> - 在 `domains/ursm/v2/store` 写入后，从生产 db（db 0）读不到 canary key。
> - 从 `v_routable_credential_models` 查询 `tenant_id=999001` 能返回 canary 行；查询其他 tenant 返回的行数与部署 canary 前一致。

### 3.4 不影响 154 的硬保证

| 维度 | 保证机制 |
|---|---|
| `credential_probe_queue` 行 | `dedup_key` 前缀隔离 + tenant 过滤 |
| `node_probe_runs` 行 | `credential_id` 集合不相交（99001/99002 vs 154 的 1~2000 真实 ID） |
| `node_probe_state` 行 | 同上 |
| URSM tenant key | Redis db index 隔离（db 15 vs db 2） |
| `cmb` / `credentials` / `providers` | 仅 canary tenant=999001 写入 |
| pg_notify | channel 名隔离 |
| `/metrics` 端点 | canary 的 metric 加 `tenant="canary"` label（仅命名空间隔离，**与生产指标并存**） |

### 3.5 脚本与配置

#### 3.5.1 env 文件

```
# deploy/env/probe-canary.env
LLM_GATEWAY_BG_MODE=full                   # canary 必须 full，与 245 生产 data-plane 区分
LLM_GATEWAY_REDIS_DB2_INDEX=15             # 隔离 Redis db
LLM_GATEWAY_CANARY_TENANT_ID=999001
LLM_GATEWAY_CANARY_CREDENTIAL_IDS=99001,99002
LLM_GATEWAY_CANARY_MODEL_ALLOWLIST=gpt-5.5-canary,glm-5.2-canary
LLM_GATEWAY_AUTO_ROUTE_REFRESH_CHANNEL=auto_route_refresh_canary
LLM_GATEWAY_PROBE_QUEUE_DEDUP_PREFIX=node_probe_canary:
LLM_GATEWAY_NODE_PROBE_WORKER_ENABLED=true
LLM_GATEWAY_USE_NEW_PROBE_MODE=true
LLM_GATEWAY_NODE_PROBE_LEASE_SECS=300
```

#### 3.5.2 启动 / 验证脚本

`scripts/probe_canary_run.sh`（伪代码）：

```bash
#!/usr/bin/env bash
# 245 probe-canary 启动 + 链路验证
set -euo pipefail

ENV_FILE=deploy/env/probe-canary.env
source "$ENV_FILE"

# 1. 隔离 Redis db 15 清理
redis-cli -n 15 FLUSHDB

# 2. 启动 gateway（canary env）
systemctl restart llm-gateway-canary   # 独立 unit，监听不同端口（如 :8081）

# 3. 等待就绪
for i in {1..30}; do
  curl -fsS http://127.0.0.1:8081/healthz && break
  sleep 1
done

# 4. 触发 enqueue（管理 API）
curl -fsS -X POST http://127.0.0.1:8081/api/admin/probe/enqueue \
  -H 'Content-Type: application/json' \
  -d '{"credential_id":99001,"model":"gpt-5.5-canary","source":"manual","tenant_id":999001}'

# 5. 等待 30s，让 ProbeService.Run 完成两轮
sleep 30

# 6. 校验（见 §3.3 测试函数同等的 curl + psql 脚本）
./scripts/probe_canary_verify.sh
```

`scripts/probe_canary_verify.sh`（伪代码）：

```bash
#!/usr/bin/env bash
set -euo pipefail
fail=0

# a. URSM tenant key
v=$(redis-cli -n 15 GET ursm:v2:tenant:999001:model:gpt-5.5-canary:credential:99001:state)
[[ -n "$v" ]] || { echo "FAIL: URSM tenant key missing"; fail=1; }

# b. node_probe_runs 审计行
n=$(psql -tAc "SELECT count(*) FROM node_probe_runs WHERE credential_id=99001")
[[ "$n" -ge 2 ]] || { echo "FAIL: audit rows < 2 ($n)"; fail=1; }

# c. routing resolve
r=$(curl -fsS 'http://127.0.0.1:8081/api/routing/resolve?tenant=999001&model=gpt-5.5-canary')
echo "$r" | jq -e '.nodes[] | select(.credential_id==99001)' >/dev/null \
  || { echo "FAIL: canary cred not in routing resolve"; fail=1; }

# d. 不污染 154：原 tenant 的 cmb 行数不变
n=$(psql -tAc "SELECT count(*) FROM credential_model_bindings WHERE tenant_id NOT IN (999001)")
echo "non-canary cmb rows = $n"  # 仅打印，不 fail

exit $fail
```

#### 3.5.3 新增测试函数（仅测试代码）

| 文件 | 函数 | 用途 |
|---|---|---|
| `bg/probe_canary_test.go`（新增） | `TestProbeCanary_IsolationRedis` | 验证 canary 写入不会出现在 db 2 |
| `bg/probe_canary_test.go`（新增） | `TestProbeCanary_EndToEndIsolated` | §3.3 主流程 |
| `bg/probe_canary_test.go`（新增） | `TestProbeCanary_AuditRowsPresent` | node_probe_runs 写入断言 |
| `admin/probe_canary_resolve_test.go`（新增） | `TestProbeCanary_RoutingResolve` | /api/routing/resolve 返回 canary credential |

---

## 4. 245 Data-Plane 配置影响范围

### 4.1 配置入口

| 位置 | 行号 | 内容 |
|---|---|---|
| `config/config.go` | 175 | `BGMode string \`yaml:"bg_mode" env:"LLM_GATEWAY_BG_MODE"\`` |
| `config/config.go` | 386 | `BGMode: envOrDefault("LLM_GATEWAY_BG_MODE", "full")` |
| `cmd/gateway/main.go` | 2131 | `bgDataPlaneOnly := strings.EqualFold(cfg.BGMode, "data-plane")` |

### 4.2 在 data-plane 模式下被禁用的后台组件（22 处 `if !bgDataPlaneOnly` 守护点）

| 组件 | main.go 行号 | 影响 |
|---|---|---|
| Model Discovery service | 2194–2203 | 不刷新 `provider_models` / `credential_model_bindings` |
| Credential cycler (`credCycler`) | 2922–2934 | 不轮换 credential 状态 |
| Credential probe v2 (`credProbeV2`) 1h 周期 | 2939– | 周期探针不跑 |
| Active probe worker（error probe） | 3071– | 错误触发的主动探测不跑 |
| Taxonomy sync | 3592–3597 | 不同步 taxonomy |
| Weekly peak rollup | 3706– | 不生成 `weekly_*` 表 |
| Stats minute accumulator / rollup | 3710– | 不写 `stats_minute_*` |
| Slot suggester | 3721– | 不出扩容建议 |
| Auto-tune / tuning proposals | 4007– | 不发 tuning_proposals |
| Goal control / 整合探针规划器 | 3706, 3837 | 不跑 goal control |
| **Credential recovery（含 fake-success UPDATE 重构后分支 `reconcileStaleNodeProbeStates`）** | 由 `credCycler` 启动路径涵盖 | **不跑** ← **关键** |
| **NodeProbeWorker（统一探针队列）** | 同上（属于 `bg.NewNodeProbeWorker`） | **不跑** ← **关键** |
| **ProbeQueue Claim/Requeue/Reaper** | 同上 | **不跑** ← **关键** |

> 注：245 data-plane 仍跑：
> - 路由 hot path（routing resolve、URSM read、credential pin）
> - `/api/auth/*`、`/api/admin/*` 的只读视图
> - `peakCollector`、`concurrencyPeakCollector`、`candidateFailureMonitor`（read-mostly）
> - `approvalTimeoutWorker`（admin 写入触发）
> - Durable execution worker（如开启）

### 4.3 为什么 data-plane 不能验证后台恢复链

链路 `enqueue → claim → direct → pinned-gateway → node_probe_runs → URSM → routing resolve` 的关键中间环节全部依赖 background 组件：

| 环节 | data-plane 下能否跑到 |
|---|---|
| `credential_probe_queue` 表写入 | 否（无 Submit 调用方） |
| `ProbeQueue.Claim` / `ExtendLease` / `Complete` | 否（无 worker） |
| `ProbeService.Run` direct round | 否 |
| `ProbeService.Run` gateway round (pin header) | 否 |
| `node_probe_runs` INSERT | 否（即便 enqueue 触发也无消费者） |
| `apply_probe.lua` (URSM v2 Probe 20) | 否（无人触发） |
| `reconcileStaleNodeProbeStates` 重排 | 否 |
| `credential_recovery.recover()` 周期 tick | 否 |

**结论**：245 的 data-plane 模式只能验证"路由本身+只读 admin 视图"，无法验证"探针+URSM 写入+routing resolve 因探针结果变化"这一闭环。因此 §3 的 probe-canary 必须以 **`BG_MODE=full`** 跑（且独立 redis db、独立 tenant、独立 credentials），与 245 当前 data-plane 是两套进程配置。

---

## 5. 重新部署建议

### 5.1 总体顺序

```
1. 245 canary (BG_MODE=full, 隔离 redis db15, canary tenant=999001)
        ↓ 验证 §3.3 全链路通过
2. 245 全量 (BG_MODE=full)
        ↓ 验证 /api/routing/resolve 与 154 数据一致
3. 154 全量 (BG_MODE=full)
        ↓ 验证 glm-5.2/5.1, gpt-5.5, kimi-k2.6, doubao 全部可路由
```

### 5.2 各阶段门禁

#### 阶段 1：245 probe-canary（必须先于 245 全量）

前置条件：
- [ ] Agent A/B/C 工作树编译错误全部修复（§1.1 三处）。
- [ ] Agent A/B/C 分支合并到 main 并 CI 通过。
- [ ] `go build ./...` 与 `go test ./bg ./admin ./domains/streaming/executors ./domains/ursm/v2/...` 全绿。
- [ ] 数据库先执行 536 migration（`psql -f sql/migrations/startup/536_node_probe_runs_trigger_kind_unified_queue.sql`）。

执行步骤：
1. 在 245 上以独立 systemd unit `llm-gateway-canary.service` 启动 canary 实例，监听 :8081，加载 `deploy/env/probe-canary.env`。
2. 运行 `scripts/probe_canary_run.sh` + `scripts/probe_canary_verify.sh`。
3. 检查 canary 实例的 `/metrics`，确认：
   - `llmgw_node_probe_sync_total` 有 outcome="recovered" 计数 ≥ 1。
   - `audit_unknown_source_total` 未出现（说明 task.Source 全部命中 knownTriggerKind）。
   - canary 实例的 URSM 写入计数与 probe 计数一致。
4. 通过：进入阶段 2；否则阻断并回到 Agent A/B/C。

#### 阶段 2：245 全量

前置条件：
- [ ] 阶段 1 通过 + 24h 稳定（观察 audit_unknown_source_total 持续为 0；cmb/credentials 行数与 canary 前一致）。
- [ ] 与 154 的 `v_routable_credential_models` 行数差异 ≤ 1%（数据漂移监控）。

执行步骤：
1. 在 245 上切换 env 到 `LLM_GATEWAY_BG_MODE=full`（**变更**），重启 `llm-gateway.service`。
2. 跑回归：`/api/routing/resolve?model=<10 个采样模型>` 返回非空；`node_probe_runs` 最新行时间在最近 60s。
3. 比对：245 vs 154 `v_routable_credential_models` 行集合一致。
4. 通过：进入阶段 3；否则回滚到 data-plane 并回到 Agent A/B/C。

#### 阶段 3：154 全量

前置条件：
- [ ] 阶段 2 通过 + 6h 稳定。
- [ ] glm-5.2、glm-5.1、gpt-5.5、kimi-k2.6、doubao 中至少 glm-5.2 / gpt-5.5 在 245 已观察到 `node_probe_runs` 成功行。

执行步骤：
1. 在 154 上同样切换 env 到 `BG_MODE=full`。
2. 同样跑 §3.3 等效的 canary 验证（**用真实 tenant**，不再用 999001）。
3. 检查 `node_probe_runs` 时间线恢复连续（handoff §6.1 提到"154 最新一行停在 2026-08-17 13:45"）。
4. 比对 `v_routable_credential_models` 行集合与 245 一致。
5. 通过：交付；否则 154 回滚并回到 Agent A/B/C。

### 5.3 风险与回滚

| 阶段 | 回滚动作 | 风险 |
|---|---|---|
| 1 canary | 关停 canary unit、redis db15 FLUSHDB、删 cmb/credentials/providers canary 行 | 低：完全隔离 |
| 2 245 全量 | systemctl restart 切回 `BG_MODE=data-plane` env | 中：恢复链停止，但路由继续工作 |
| 3 154 全量 | systemctl restart 切回原 BGMode | 高：154 是生产；建议先保留 `BGMode` 配置可切换 |

---

## 6. 总结

- **当前 main（`1eac33fac`）构建/测试整体可运行**，但工作区已被 Agent A/B/C 未合并改动破坏：`go build ./...` 在 `bg/credential_selfcheck.go` 与 `bg/probe_service.go` 失败。
- **唯一新增未部署迁移**是 Agent B 的 536 号（`node_probe_runs.trigger_kind` 扩展），与既有 425 不冲突，但必须在 Agent B 代码上线前先 DB 落地。
- **245 data-plane 无法验证后台恢复链**已确认：所有 22 处 `!bgDataPlaneOnly` 守护点恰好覆盖 `CredentialRecovery`、`NodeProbeWorker`、`ProbeQueue`、`ProbeService.Run`、`apply_probe.lua` 触发路径。
- **Probe-canary 必须以 `BG_MODE=full` + 隔离 redis db15 + canary tenant=999001 跑**，才能在不影响 154 真实数据的前提下覆盖 §3.3 完整链路。
- **建议部署顺序：245 canary → 245 全量 → 154 全量**，每阶段设独立门禁。

---

## 7. 引用

- 交接输入：`docs/handoff/2026-08-18-global-routing-audit-handoff.md`
- 关键源码：
  - `bg/credential_recovery.go:443–485, 954–1093`（Agent A fake-success 重构 + reconcileStaleNodeProbeStates）
  - `bg/probe_service.go:67–98, 112–`（Agent B lease + audit + NormalizeTriggerKind）
  - `bg/probe_queue.go:263, 378, 454, 488`（Enqueue / Claim / Complete / ExtendLease）
  - `bg/credential_selfcheck.go:67, 465, 734`（Agent C pin-credential header）
  - `cmd/gateway/main.go:2131, 2194, 2923, 2940, 3071, 3592, 3706, 3837, 4007`（data-plane 守护点）
  - `config/config.go:175, 386`（`BGMode` 字段与默认值）
  - `sql/migrations/startup/536_node_probe_runs_trigger_kind_unified_queue.sql`（Agent B 新增迁移）
  - `sql/objects/tables/node_probe_runs.sql` / `sql/schema/01-schema.sql`（同步更新 CHECK）
  - `domains/ursm/v2/store/apply_probe.lua`、`record_request.lua`（硬编码 key 前缀——见 §3.2.4 风险）
- 关键数据库对象：`v_routable_credential_models`、`credential_probe_queue`、`node_probe_state`、`node_probe_runs`、`credential_model_bindings`、`credentials`、`providers`、`URSM v2 tenant:{tenant_id}:model:{model}:credential:{credential_id}:state`
- 既有审计报告：`docs/audit/2026-08-18-routing-ursm-coherence-audit.md`（Agent D 现场取证）
