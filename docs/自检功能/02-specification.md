# 02-specification.md — 自检 / 自动探测 规范（2026-07-14 重塑）

> 本规范取代 01-design.md 中的 featured-model 自检设计，吸收 252
> 真实运行数据后重新校准。**所有旧探针 worker** 默认关闭（env
> `LLM_GATEWAY_USE_NEW_PROBE_MODE=false` 时仍可回滚）。

## 1. 规范概览

| 作业 | 调度 | 频度 | 单元 | worker | 关键文件 |
|---|---|---|---|---|---|
| A. 凭据自检 | 周期 | 24h × 1/凭据 | 常用模型 → 失败重选 → 未用随机 | `bg.CredentialSelfcheckWorker` | `bg/credential_selfcheck.go` |
| B. 节点探测 | 错误触发 | 7 级 backoff：5s / 30s / 60s / 5m / 1h / 2h / 24h（最多 1 天） | 直连上游 + 走网关 两轮多轮会话 | `bg.NodeProbeWorker` | `bg/node_probe.go` |
| C. 系统健康自检 | 周期 | 30s/次 | `request_logs_hot` 30s 窗口成功率 ≥ 80% 绿 / < 80% 红 / 0 请求灰 | `bg.SystemHealthWorker` | `bg/system_health.go` |

## 2. 凭据自检（§A）规则

### 2.1 选模策略

> 2026-07-15 修正：**优先探测特性模型**，只有当凭据不提供任何特性模型时才回退。

1. **featured**（首选）：该凭据可路由且在 `routing_policy.featured_models` 特性模型列表中的模型。多个特性模型时按 `standardized_name` 排序稳定选取，并依次填入 fallback 槽位。
2. **most_used**（无特性模型时）：credential × model 在 24h 内调用次数最多的模型，SQL 由 `credential_most_used_model(cred_id, 24)` 提供。
3. **fallback_N**（失败重选）：首选失败 → 按上述优先级换下一个模型（最多 3 次尝试，featured 优先于 most_used，最后随机）。
4. **random**（从未用过且无特性模型）：7 天内无任何成功调用、且不提供任何特性模型的凭据，从其可用模型中**随机**选一个。

> 与 `bg/shared_pick.go:PickProbeModelForCredential`、`bg/model_probe.go:featuredCycle` 共用同一特性模型判定谓词（`standardized_name` / `raw_model_name` = ANY(featured_models)），确保三层探测对"什么是特性模型"达成一致。

### 2.2 会话形态

ping + 1 轮工具调用（与原 `bg/self_check_worker.go` 的 3 轮相比节省带宽）。

### 2.3 结果表

`self_check_runs` 沿用，新增列：

- `selection_strategy text` — `featured | most_used | fallback_N | random`
- `attempted_models jsonb` — 该 run 试过的所有模型顺序

新增 status：`retrying`（预留，3 次 fallback 仍失败时使用 `failed`）。

### 2.4 失败处理

不重试 — 仅记入 `self_check_runs`，**不**触发 §B 节点探测（避免无限循环）。

### 2.5 顺序执行 + 跨进程互斥

凭据自检**严格按顺序逐个执行**，绝不同一时刻并发多个：

- **进程内**：`cycleOnce` 每 tick（5min）只取 1 个 due 凭据，`runOne` 同步执行完毕后才进入下一 tick。
- **跨进程**（154 / kaixuan-* 多实例写同一 PG）：`runOne` 入口用 `pg_try_advisory_xact_lock(credential_id)` 抢占事务级咨询锁。锁失败 = 另一实例正在处理该凭据 → 本实例跳过该 tick。
- 锁是事务级（xact_lock），worker 崩溃 / 进程退出时 PG 自动释放，不会死锁。
- 配合 5min tick，最坏情况：N 个实例同时启动，每个 tick 抢不同凭据，互不冲突。

## 3. 节点探测（§B）规则

### 3.1 触发

- 来源：`bg/credentialstate.Manager.UpdateOnFailure`（与 `bg.ActiveProbeWorker` 同源）
- 同 (cred, model) 5 分钟内不重入（`in_flight_until` 列 + `SELECT ... FOR UPDATE SKIP LOCKED` 跨进程互斥）

### 3.2 双轮会话

每轮独立，每轮 = 1 次 ping：

1. **direct** — POST provider 原始 base_url，用解密凭据直连。验证上游确实可用。
2. **gateway** — POST 本地网关 `/v1/chat/completions`，用 system api_key。验证鉴权/路由/插件链路。

双轮都 200 才算成功；任一失败 → 升级 backoff。

### 3.3 Backoff 7 级（与原文"5s/30s/60s/300s/1h/2h"完全对应，最长 1 天由 attempt=7 触发 24h 间隔 + paused 标志）

| attempt | 间隔 | 累计 |
|---|---|---|
| 1 | 5s | 5s |
| 2 | 30s | 35s |
| 3 | 60s | 1m35s |
| 4 | 5m | 6m35s |
| 5 | 1h | 1h6m35s |
| 6 | 2h | 3h6m35s |
| 7+ | 24h | —（paused） |

### 3.4 状态机

`node_probe_state (credential_id, raw_model_name)`：consecutive_failures / next_retry_at / paused / in_flight_until。

### 3.5 成功重置

双轮都通过 → `consecutive_failures=0`、清 paused、next_retry_at = now() + 24h。

## 4. 系统健康自检（§C）规则

### 4.1 检测窗口

最近 30s `request_logs_hot` 的 `success` 列聚合（SQL 函数 `system_health_status(30)`）。

### 4.2 判定

| 条件 | status | 颜色 |
|---|---|---|
| 0 请求 | `suspect` | ⚪ 灰 |
| 成功率 ≥ 80% | `ok` | 🟢 绿 |
| 成功率 < 80% | `degraded` | 🔴 红 |

### 4.3 API

`GET /api/health/system` 返回：

```json
{
  "status": "ok",
  "success_rate": 0.93,
  "sample_count": 1240,
  "failure_count": 87,
  "last_check_at": "2026-07-14T12:34:56Z"
}
```

503 仅在 worker 未配置时。

### 4.4 GDRT H 角标

`web/src/components/SystemHealthBadge.vue` 轮询 10s 一次，渲染在 `App.vue` 中 `SystemStatusIndicator` 旁。Tooltip 显示具体成功率与样本数。

## 5. 调用端 IP 链 + 发起环节标志

### 5.1 `request_logs` 4 列（migration 341）

| 列 | 类型 | 含义 |
|---|---|---|
| `client_ip` | INET | 单值（X-Real-IP > XFF[0] > RemoteAddr） |
| `client_forwarded_for` | TEXT (≤1024B) | 完整 XFF 链 |
| `origin_stage` | VARCHAR(32) | self_check \| node_probe \| system_health \| business + 旧 probe_* |
| `origin_actor` | VARCHAR(64) | credential-selfcheck-worker \| node-probe-worker \| system-health-worker \| manual:<id> |

`origin_stage` 枚举与 DB CHECK 同步（additive — 旧 probe_direct / probe_v2 / model_probe / passive_probe / manual 仍合法）。

### 5.2 中间件

`middleware/origin_mw.go`：

- 在 auth_mw 之后、logging_mw 之前挂载
- 仅当 ctx 含 `auth.owner_user` sentinel（即静态全局 key 已通过）时信任 `X-LLM-Origin-*` 入站头
- 业务客户端伪造的 `X-LLM-Origin-Stage=manual` 一律被剥为 `business`
- IP 链在 1024B 处截断

### 5.3 Worker 出栈头

3 个新 worker 在出站 HTTP 请求上带：

- `X-LLM-Origin-Stage: <self_check|node_probe|system_health>`
- `X-LLM-Origin-Actor: <worker-name>`
- `X-Real-IP: $LLM_GATEWAY_EGRESS_IP`
- `X-Forwarded-For: $LLM_GATEWAY_EGRESS_FORWARDED_FOR`

## 6. 频度收敛（解决"每分钟 2 次以上"问题）

| Worker | 旧 | 新 | 状态 |
|---|---|---|---|
| `bg/self_check_worker.go`（featured） | 1min tick × 3 model | 关（默认） | env false 可回滚 |
| `bg/credential_probe_v2.go` | 1h cycle | 关（默认） | env false 可回滚 |
| `bg/model_probe.go` | 5min cycle | 关（默认） | env false 可回滚 |
| `bg/model_probe_suspicious.go` | 2min | 关（默认） | env false 可回滚 |
| `bg/passive_probe_listener.go` | 30s poll | 关（默认） | env false 可回滚 |
| `bg/active_probe_worker.go` | 5s/30s/2m/5m/15m | 关（默认） | env false 可回滚 |
| `bg/credential_selfcheck.go` | — | 5min tick，每 tick 1 凭据 | 默认开 |
| `bg/node_probe.go` | — | 错误触发 7 级 backoff | 默认开 |
| `bg/system_health.go` | — | 30s tick | 默认开 |

**收敛后预期**：无错误时 1 分钟探针数 = 0（凭据自检 24h 漂移；节点探测错误触发；系统健康只读 metrics 不发请求）。

## 7. 环境变量

| 变量 | 默认 | 含义 |
|---|---|---|
| `LLM_GATEWAY_USE_NEW_PROBE_MODE` | `true` | `false` 时所有旧 worker 仍启动（回滚） |
| `LLM_GATEWAY_EGRESS_IP` | 空 | worker 自身出栈源 IP |
| `LLM_GATEWAY_EGRESS_FORWARDED_FOR` | 空 | worker 自身在 XFF 链中的位置 |
| `LLM_GATEWAY_SELF_CHECK_API_KEY` | 空 | 自检 system api_key |
| `LLM_GATEWAY_SELF_CHECK_BASE_URL` | `https://llm.kxpms.cn/v1` | 自检目标 baseURL |
| `LLM_GATEWAY_NODE_PROBE_BASE_URL` | `https://llm.kxpms.cn/v1` | 节点探测目标 baseURL |

## 8. 部署步骤

1. 应用 migration 341
2. 运行 `go build ./... && go test ./...` 全绿
3. 重启 llm-gateway-go（154 / kaixuan-* 都要重启拾取新代码）
4. 监控 `request_logs_hot` 中 `origin_stage='self_check' | 'node_probe' | 'business'` 行
5. 监控 `node_probe_runs` 与 `node_probe_state` 表，确认 backoff 阶梯生效
6. 252 / 154 人工 `UPDATE credential_probe_configs SET enabled=FALSE WHERE enabled=TRUE`（如需）

## 9. 验收

- 252 重启后 1 分钟内 `request_logs_hot` 写入行均为 `origin_stage='business'`，无 `origin_stage IS NULL` 业务行
- 无错误时 1 分钟探针数 = 0
- 故意让一个凭据连续失败 → 节点探测在 5s 后启动 → 7 次后 paused
- GDRT H 角标实时反映 30s 成功率
- `go build ./...` + `go test ./...` 全绿
