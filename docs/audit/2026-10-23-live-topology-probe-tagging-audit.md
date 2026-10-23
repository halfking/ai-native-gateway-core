# 2026-10-23 — 实时拓扑探测打标四处源真值漂移审计 + 口径收敛

> 会话：`mvs_64ddb526ee11405aae54d67e7c4d6161`
> 触发：用户报 154 生产上一条 32-hex 请求 `af1eb6d83ef39610cc4f8be4e181b5a8`
> "应被识别为探测但实时拓扑 pill = 否"。
> 审计阶段只钉形态；**属主拍板后**（选"修 B' + 收敛成单一真值源"）本轮
> 已**落地修复**。

## TL;DR

1. 仓内**有 4 处**独立代码决定"一个请求算不算探测"——历史上没有同源；
2. `admin/probe_request_info.go:69-73` 自陈"必须同源"是**不实**的；
3. dashboard 端多出的 5 个 alias 值是**死防御代码**（被中间件闸门挡掉），
   不是用户报的"漏标"形态根因；
4. **新增发现**：`stats/board 排除` 与 dashboard **真分歧** —— `self-check-worker`
   写出的行 dashboard 认 probe、stats 不认 ⇒ 板上"总请求数"会**持续虚高**；
5. 本轮在 4 处各加**对齐门**（DB CHECK + 侧表 + dashboard + stats），
   把当前形态钉成可执行的回归保护；
6. 真根因待用户提供 154 PG 上 `af1eb6d83ef39610cc4f8be4e181b5a8`
   的 `origin_stage` / `task_type` / `quality_flags` / `origin_actor` 4 列数据。

## 三处源真值（审计前）

| 位置 | 函数 | 文件:行 | 探测集 |
|---|---|---|---|
| **DB CHECK 闸门** | `isValidOriginStage` | `middleware/origin_mw.go:371-379` | `self_check, node_probe, system_health, business, probe_direct, probe_v2, model_probe, passive_probe, manual` |
| **侧表 `is_probe` 列** | `isProbeOriginStage` | `domains/streaming/context_attrs.go:151-158` | `self_check, node_probe, system_health, probe_direct, probe_v2, model_probe, passive_probe, manual` |
| **实时拓扑 dashboard pill** | `extractProbeInfo` | `admin/probe_request_info.go:74-100` | 上面 8 个 **+ 5 个 alias**：`direct, gateway, scheduled, probe_gateway, probe_scheduled` |
| **stats/board 排除** | `IsProbeTraffic` | `domains/stats/minute_entry.go:36-51` | 三条独立路径：`quality_flags ⊇ ['probe']` / `task_type='probe_triggered'` / **`origin_actor ∈ probeGatewayActors`**（**故意不读 origin_stage**） |

## ★★★ 第 4 处的 actor 白名单与 dashboard 真分歧

`domains/stats/minute_entry.go:19-24` 定义：

```go
var probeGatewayActors = []string{
    "node-probe-worker",
    "active-probe-worker",
    "probe-service",
    "credential-selfcheck-worker",
}
```

而 `middleware/origin_mw.go:400-406` 的 `systemOwnerFallbackStage` 映射 5 个 owner：

```go
"self-check-worker":           "self_check",
"credential-selfcheck-worker": "self_check",
"model-quality-worker":        "self_check",
"node-probe-worker":           "node_probe",
"system-health-worker":        "system_health",
```

**差集**（owner 映射到 self_check 但 actor 不在 stats 白名单里）：

- `self-check-worker`
- `model-quality-worker`

### ★★ 已实测确认：这两个 actor **真的在写**（不是理论上的）

grep 全仓 `X-LLM-Origin-Stage` / `X-LLM-Origin-Actor` 写入点：

| 写入点 | stage | actor |
|---|---|---|
| `bg/self_check_worker.go:640-641` | `self_check` | `self-check-worker` |
| `bg/self_check_worker.go:756-757` | `self_check` | `self-check-worker` |
| `domains/modelquality/invoker.go:281-282` | `self_check` | `model-quality-worker` |

⇒ **B' 是活的缺陷**，不是假设。

### ★★ 且它们的 request_id 形态与用户报的完全一致

`bg/self_check_worker.go:631` 是 `http.NewRequestWithContext(ctx, "POST", w.baseURL+"/chat/completions", ...)`
—— **走 HTTP 进网关** ⇒ 拿的是 `middleware/requestid_mw.go:83-90` 的 `generateRequestID()`
⇒ **32-hex**，与用户报的 `af1eb6d83ef39610cc4f8be4e181b5a8` **同形态**。

### 后果（以 `self-check-worker` 写出的行为例）

| 表面 | 函数 | 判定 |
|---|---|---|
| 实时拓扑 dashboard pill | `extractProbeInfo` 看 `origin_stage="self_check"` | ✅ IsProbe=true |
| 侧表 `is_probe` | `isProbeOriginStage("self_check")` | ✅ true |
| **stats/board 排除** | `IsProbeTraffic` 看 actor，不在 list | ❌ false ⇒ **板上"总请求数"会累计这条** |
| **stats/board 排除（SQL 镜像）** | `bg.ProbeTrafficExclusionPredicateView` | ❌ 同上（自检/打分 SQL 同样不认 self-check-worker） |

⇒ 自检 + 模型质量 worker 写出的行**会持续被 board 当作业务流量计入**。
这是板上数字虚高的真根因 —— 与用户报的"实时拓扑 pill = 否"是**同源**缺陷
的不同表现。

> 形态 B 的真实定义被这发现扩展了：不仅是"Dashboard 没识别"，
> 更是"4 处对什么算探测的口径不一致"。

## ★ 顺带排查并**排除**的两个疑点（阴性结论也要记）

### 排除 1：`globalAuthStageActorPairs` 缺 `system_health` 条目

`middleware/origin_mw.go:147-157` 的配对表只有 `self_check` 与 `node_probe`
两个 stage。若 `system-health-worker` 用**静态全局 key**（owner =
`"global-auth-passed"`）且声明 `X-LLM-Origin-Stage: system_health`，
R64 配对闸门（`origin_mw.go:343-351`）会因 `allowed, ok := globalAuthStageActorPairs["system_health"]`
取不到而**降级为 business** ⇒ 漏标。

**⇒ 但这是防御性缺口，不是活的**：全仓 grep `X-LLM-Origin-Stage` 写入点，
**没有任何代码写 `system_health`**（`bg/system_health.go` 是**只读扫描器**，
扫 `_request_logs_hot` 算成功率，**不发网关请求**）⇒ 该 stage 目前只存在于
映射表 / 白名单 / 注释里。

### 排除 2：失败/限流终态行不应用 origin 兜底

`applyOriginMetadata` 的 docstring 说 "failure/rate_limited 终态行从不应用"
（2026-09-25 前）。**已核实 2026-09-25 的修复覆盖了它**：
`domains/streaming/request_log_pipeline.go:1182` 的 `BuildFailureEntry` 路径
也调用了 `applyOriginMetadata(reqLog, c.KeyInfo, originCtx)`；
且 `handler.go:2429` 的 `logCtx.SetKey(ki)` 在鉴权成功后立即执行，早于
`no_candidate`（`handler.go:3806`）与限流退出 ⇒ KeyInfo 到位。

**结构差集**（dashboard 独有、侧表不认、DB CHECK 不认）：
- `direct` / `gateway` / `scheduled`（裸值）
- `probe_gateway` / `probe_scheduled`

## 关键发现：5 个 alias 走不通

`middleware/origin_mw.go:354-359` 的写入闸门：

```go
if claimedStage != "" {
    if isValidOriginStage(claimedStage) {
        stage = claimedStage
    }
}
```

任何 worker 试图塞 `X-LLM-Origin-Stage: direct` / `gateway` / `scheduled` /
`probe_gateway` / `probe_scheduled`：

1. `isValidOriginStage` 返 false
2. stage 保持默认 `"business"`
3. DB 落 `origin_stage='business'`
4. dashboard `extractProbeInfo` 看到 `business`，**不会**走那 5 个 alias 分支

⇒ `admin/probe_request_info.go:84-99` 的 5 个 alias 分支是**永远走不到
的死防御代码**。`af1eb6d83ef39610cc4f8be4e181b5a8` 走的也不是这条路。

## 用户报的 32-hex 请求的可能形态（4 选 1）

| 形态 | 判定字段形态 | 真实问题 | 修法 |
|---|---|---|---|
| **A. 5 个 alias 之一** | `origin_stage` ∈ {direct, gateway, scheduled, probe_gateway, probe_scheduled} | 不可能（中间件闸门挡掉） | 不适用 |
| **A'. `system_health` 配对缺失** | `origin_stage='system_health'` 且 owner="global-auth-passed" | 不可能（全仓无 `system_health` 写入点；`bg/system_health.go` 是只读扫描器） | 不适用 |
| **B. DB 系统 key worker 被剥头** | `origin_stage='business'` 或 NULL，但 worker 实际是自检 / 探针 | **真问题**：worker owner 不在 `trustedOriginOwners` 或 `globalAuthStageActorPairs` 未登记配对 | 走 `applyOriginMetadata` 兜底（2026-09-25 已加）；主路径已覆盖，剩边缘路径待验 |
| **B'（已实测为活）**. **自检/打分 worker 写出的行被 board 当作业务** | `origin_stage='self_check'` 且 `origin_actor ∈ {self-check-worker, model-quality-worker}` | **真问题**：stats/board 排除不认这俩 actor，但 dashboard 认 ⇒ 板上"总请求数"虚高 | 加 `self-check-worker` / `model-quality-worker` 进 `probeGatewayActors`，并同步 SQL 镜像 |
| **C. 早期 row 没救** | `origin_stage IS NULL` 且无 task_type / quality_flags | **真问题**：R64 之前的 row 写库时 `applyOriginMetadata` 还没加 | 单条 row 无法回填（缺标志位），批量 SQL 走 `auto_decision->>'probe_attempt'` 或 `quality_flags @> ARRAY['probe']` 推断 |
| **D. 根本不是探测** | 全空，业务请求被用户误判为探测 | **非问题**：dashboard pill = 否 是正确行为 | 不修 |

判型只需要 4 列：

```sql
SELECT origin_stage, origin_actor, task_type, quality_flags
FROM _request_logs
WHERE request_id = 'af1eb6d83ef39610cc4f8be4e181b5a8'
   OR client_request_id = 'af1eb6d83ef39610cc4f8be4e181b5a8';
```

## ★★★ 落地：口径收敛成单一真值源（属主拍板后已实施）

**新包 `internal/probekind`**（stdlib-only，不依赖 telemetry / bg ⇒ 无循环）：

| 导出 | 用途 |
|---|---|
| `ProbeStages` | 规范探测 origin_stage 集合（8 个） |
| `Writers` | actor → {Stage, ViaHTTP} 规范表（7 个写入者） |
| `ProbeActors` | **由 Writers 派生**的规范 actor 集合（7 个） |
| `HTTPStageActors()` | stage→actor 反向索引（middleware 配对表的唯一来源） |
| `IsProbeStage` / `IsProbeActor` / `IsProbeTaskType` / `IsProbeQualityFlags` | 4 条基础判定 |
| `IsProbeMarkers(taskType, flags, actor)` | stats 端 SQL 可表达的三条标记 |
| `IsProbe(stage, taskType, flags, actor)` | 完整判定（能读 origin_stage 的表面用） |
| `ValidStages()` / `IsValidStage()` | DB CHECK 合法集合（探测 ∪ business） |
| `SQLActorList()` | 把 actor 集渲染成 SQL `IN` 列表 |

**四处改造**：

| 位置 | 改前 | 改后 |
|---|---|---|
| `middleware/origin_mw.go` `isValidOriginStage` | 手写 9 值 switch | → `probekind.IsValidStage` |
| `middleware/origin_mw.go` `globalAuthStageActorPairs` | 手写 2 stage × 5 actor | → 由 `probekind.HTTPStageActors()` 派生 |
| `middleware/origin_mw.go` `systemOwnerFallbackStage` | **手写 5 actor**（与配对表同源却各写一份） | → 由配对表**反向派生**（只留 trusted owner）+ `system-health-worker` 例外 |
| `domains/streaming/context_attrs.go` `isProbeOriginStage` | 手写 8 值 switch | → `probekind.IsProbeStage` |
| `admin/probe_request_info.go` `extractProbeInfo` | 手写 13 值 switch（含 5 个死 alias） | → `probekind` + **删 5 个死 alias** + **补 origin_actor 兜底路径** |
| `domains/stats/minute_entry.go` `probeGatewayActors` | 手写 4 actor | → `probekind.ProbeActors`（7 个，**B' 修复**） |
| `domains/stats/minute_entry.go` `IsProbeTraffic` | 三条手写判定 | → `probekind.IsProbeMarkers` |
| `bg/probe_policy.go` `ProbeTrafficExclusionPredicateView` | `const` + 手写 4 actor | → `var` + `probekind.SQLActorList()` 渲染 |

### ★ B' 修复（板上"总请求数"虚高）

actor 集从 4 → 7，补进 3 个：
- `self-check-worker`（bg/self_check_worker.go:641,757 — 生产在用）
- `model-quality-worker`（domains/modelquality/invoker.go:282 — 生产在用）
- `mock-probe-client`（internal/mockprobe/client.go:129 — cmd/gateway/main_helpers.go:339 在用）

Go 侧与 SQL 侧同时生效（SQL 列表从 probekind 渲染）。

### ★ dashboard 补 origin_actor 兜底路径（治"漏标"）

`extractProbeInfo` 新增路径 4：`origin_actor ∈ ProbeActors` ⇒ 判为探测。
此前 dashboard 只看 stage ⇒ **stage 一丢就漏标**（用户报的现象）；
而 stats 端早就看 actor 了 ⇒ 两端口径不一致。现在两侧口径一致。

### ★ 5 个死 alias 的处置

`direct` / `gateway` / `scheduled` / `probe_gateway` / `probe_scheduled`
作为 **origin_stage** 写不进 DB（中间件闸门 + migration 341 CHECK 都不认）
⇒ 从 dashboard 的 switch 里**删除**。
⚠ `probe_gateway` / `probe_scheduled` 作为 **task_type_chosen** 仍合法
（bg/active_probe_emitter.go:373 写 `"probe_" + origin`）⇒ 那条路径未受影响。

### 结构不变量（收敛后）

```
ProbeStages ⊆ DB CHECK (migration 341)      ← probekind 自检门对拍
ProbeStages = 侧表 is_probe 判定集             ← 同一份
ProbeActors ⊇ 所有真实写入者的 actor          ← probekind 自检门扫全仓
ProbeActors = stats Go 侧 = stats SQL 侧      ← 同一份渲染
fallback 表 = 配对表的反向（只留 trusted）      ← middleware 门
```

## 验证（变异探针 3 臂全红 + 全量测试）

| 变异 | 期望红的门 | 实际 |
|---|---|---|
| **A**：从 `Writers` 删 `self-check-worker` | probekind 自检门 + stats B' 门 | ✅ **3 个门同时红**（`TestProbeActorsCoverEveryRealWriter` / `TestIsProbeCoversStageAndMarkers` / `TestIsProbeTraffic_SelfCheckWorkerIsProbe`） |
| **B**：从 `ProbeStages` 删 `manual` | migration 341 对拍门 | ✅ `TestProbeStagesMatchMigrationCheck` 红（"被 migration 341 的 CHECK 接受，但不在 ProbeStages 里"） |
| **C**：删 dashboard 的 actor 兜底路径 | admin 一致性门 | ✅ `TestExtractProbeInfo_MatchesProbekindOnEveryActor` 红（7 个 actor 逐个报） |

**全量测试**：`admin` / `domains/streaming` / `middleware` / `domains/stats` /
`internal/probekind` / `bg` 六包 **全部 ok（exit 0）**；
`go build ./...` 与 `go vet` 六包 **exit 0**。

## ★ 量具失效记录（本轮踩到两次，量具都瞎了）

1. **`TestProbeActorSetMatchesSQLPredicate`（既有的门）**
   原来靠"grep bg 源码里的 `'<actor>'` 字面量"判定 Go/SQL 一致。
   SQL 改成从 probekind 渲染后，字面量不再出现在源码 ⇒ **门永远红**。
   ⇒ 门改成两段：① 断言 bg 谓词确实引用 `probekind.SQLActorList()`
   ② 断言渲染结果与 Go 侧逐个对得上。
   **教训**：门的内容跟着口径一起变，量具不跟着变就会给出假红。

2. **`TestProbeActorsCoverEveryRealWriter`（本轮新写的门）**
   第一版只认**字面量**参数，于是漏掉两个**真实**写入点：
   - `X-LLM-Origin-Actor", OriginActorMockProbeClient`（常量引用）
   - `OriginActor: strPtrTelemetry(actor)`（变量引用，actor 来自上文赋值）
   ⇒ 门报"凭想象登记" ⇒ **读数错误，不是数据错误**。
   改法：对每处写入同时解两种形态（字面量直取；标识符回溯同文件赋值）。

3. 附带修掉一处**断言方向错误**：`TestSystemOwnerFallbackStageMatchesPairingTable`
   原写成"配对表 ⊆ fallback"，但 fallback 只给 trusted owner 建 ⇒
   方向应是"fallback ⊆ 配对表"（`probe-service` / `mock-probe-client`
   走 HTTP 头声明，不需要 fallback）。

## 判型（用户报的那条 32-hex 请求）

修法已落地，**但用户报的那一条具体形态仍未定位** —— 需要 154 PG 上 4 列：

```sql
SELECT origin_stage, origin_actor, task_type, quality_flags
FROM _request_logs
WHERE request_id = 'af1eb6d83ef39610cc4f8be4e181b5a8'
   OR client_request_id = 'af1eb6d83ef39610cc4f8be4e181b5a8';
```

**⇒ 注意**：本次修复后，**带 `origin_actor` 的探测行不再漏标**
（dashboard 有了 actor 兜底路径）。若这条请求的 4 列里有任一命中
`probekind.ProbeActors` / `ProbeStages`，则它**现在应当已被打标**；
若仍显示"否"，说明它的 4 列全空 ⇒ 形态 D（根本不是探测），
或者它是 C 形态的早期 row（origin_stage 丢失且无 actor）。

## 落地后行为变化的预期（需属主知悉）

1. **板上「总请求数」会下降** —— 自检/打分/mock 探针的行不再被当业务计入。
   ⇒ 这是修正，不是故障；但数字会变，需要提前打招呼。
2. **实时拓扑 pill 会多认一批行** —— origin_stage 丢失但 actor 在位的
   探测行，现在会显示为探测（此前漏标）。
3. **dashboard 不再认 5 个死 alias 作为 origin_stage** —— 它们本来
   就写不进 DB ⇒ 线上无可观测影响。

## 关联文档

- AGENTS.md 交接：本会话承接 R60~R66 之后，无新引入的 handoff
- 仓内既有 `docs/audit/2026-10-18-live-topology-three-lane-and-data-switches.md`
  涉及"实时拓扑三泳道 + 数据开关"，是上层 UI 视角；本报告是底层数据源视角

---

# 附录 A — 154 生产取证（2026-10-23 17:50–18:10，只读）

> 全部为只读 SELECT / pgrep / journalctl；**对生产的写入 = 0**。
> 连接方式：`ssh 154` → DSN 取自 `/etc/llm-gateway-go/env` 的
> `LLM_GATEWAY_DATABASE_URL`（PG 在 172.16.2.210:5432）。
> ⚠ 量具失效 4 次（进程换 PID / 时区 / journal 不记 request_id / 日志文件已被轮转）
> —— 每次都靠**对照窗口**兜住。

## A.1 目标请求的定性

```sql
SELECT ... FROM session_turns_hot WHERE request_id='af1eb6d83ef39610cc4f8be4e181b5a8';
```

| 字段 | 值 |
|---|---|
| origin_stage / origin_actor / client_endpoint / client_ip | **四个全 NULL** |
| task_type / task_type_chosen / quality_flags | NULL |
| is_auto_request | false |
| success / status_code / quality | true / 200 / verified |
| session_id / turn_no | gw_0b3527ac-a69d-4b22-84a8-f256407e4928 / **1** |
| model / canonical_model | nemotron-3-ultra-550b-a55b |
| agent_name / agent_type / source_kind | **go-client** / api / live |
| credential_id | **29** |
| request_preview / response_preview | **user: ping** / **assistant: pong** |

**凭据归属**：`api_keys.id=29` ⇒ `owner_user` 空、`is_system=false`、`status=active`
⇒ **普通业务凭据，不是系统 key**。

## A.2 ★ 对照窗口：真探测行长什么样

`session_turns_hot` 里 `request_preview ILIKE '%ping%'` 的行（154 实况）：

| origin_stage | origin_actor | client_ip | 行数 |
|---|---|---|---|
| node_probe | probe-service | 127.0.0.1 | 3122 |
| node_probe | node-probe-worker | 127.0.0.1 | 919 |
| self_check | credential-selfcheck-worker | 127.0.0.1 | 7 |
| business | (空) | 146.19.248.250 等外部 IP | 584 |
| **(null)** | **(null)** | **(NULL)** | **3812（45.1%）** |

★ **真探测行的 `request_preview` 也是 "user: ping"** ⇒ 用户"它像探测"的直觉**有依据**；
但真探测行**必然**带 `origin_stage`（node_probe/self_check）+ `client_ip=127.0.0.1` +
`origin_actor`（probe-service / node-probe-worker / credential-selfcheck-worker）。

⇒ **目标请求与真探测行的形态差异 = 四列全空 + 外部业务凭据 + 非 loopback**。

## A.3 判定

| 判定 | 依据 |
|---|---|
| **它不是网关的探测请求** | 凭据是普通业务 key（is_system=false, owner 空）；无 origin 声明；无 task_type/quality_flags；非 loopback IP |
| **它"没被打标"是正确行为** | 系统按"探测需显式声明/系统凭据"判定 ⇒ 不标 |
| ★ **但它暴露了一个独立的、更大的问题** | **3812/8444 = 45.1% 的行四列全空** |

## A.4 ★ 45.1% 四列全空的成因分解

| agent_name | submit_mode | 四列全空 | 四列齐全 | 判定 |
|---|---|---|---|---|
| claude-code | inferred_compressed | **1542** | 33 | **设计使然**：压缩重建轮次（turn_no 高达 5291 的长会话），无原始 HTTP 上下文 |
| codex | full | **766** | 0 | 同类重建路径（待进一步确认） |
| (空) | full | 1135 | 0 | 同上 |
| **go-client** | full | **301** | **4239** | ★ **异常**：同类客户端 6.6% 间歇性丢失 HTTP 元数据 |

★ 判别「投影丢字段」vs「本来就无」：
- 这 3812 行中，**0 行**能在 `session_turns` 分区表找到同 request_id 带元数据的行
- **0 行**与带元数据的行 request_id 重号
⇒ **不是投影丢字段 ⇒ 它们从未有过 HTTP 元数据**。

## A.5 ★★ 真正的"漏掉"观感来源

实时拓扑的**客户端维度**按 `client_ip` / `agent_name` 聚类 ⇒
**45.1% 的行没有 client_ip** ⇒ 这些请求在图上落进 **unknown 客户端桶**，
看起来就像"一批请求没信息 / 没打标"。

⇒ 用户观感中的"探测请求打标有漏掉"，**实际是两件事被混在一起**：
1. 目标请求本身是业务流量，未标探测 = **正确**；
2. 大量请求（尤其 claude-code 压缩重建轮次）**没有 client_ip / origin_stage**
   ⇒ 实时拓扑上聚成 unknown 桶 ⇒ **观感上的"漏掉"**。

## A.6 ★ B' 修复在 154 上当前看不到效果（诚实边界）

154 实况：`origin_actor='self-check-worker'` **0 行**、`'model-quality-worker'` **0 行**；
`pgrep self_check|modelquality` **无进程** ⇒ **这两个 worker 在 154 上没有流量**
⇒ B' 修复（actor 集 4→7）在 154 的板上数字上**当前不可见**。

⇒ 修复本身仍然正确且必要：
- 它让 4 处口径真正同源（结构性）；
- 这两个 worker 在 245 / 252 或未来部署中运行时即生效；
- 若将来它们在 154 启用，修复即刻生效。

⚠ **本轮没有验证「修完后板上总请求数下降」** —— 因为触发该修复的流量在 154 上不存在。
