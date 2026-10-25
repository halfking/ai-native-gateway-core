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
7. **★ 同轮后续（见附录 B）**：上面第 6 条已由本机取证闭合，且**推翻了附录 A 的定性** ——
   目标请求**就是探测请求**（侧表 `request_context_attrs`：
   `origin_stage=node_probe` / `client_ip=127.0.0.1` / `is_probe=t`），
   「未标探测」是**真缺陷**而非正确行为；
8. **★ 真根因**：`domains/streaming/handler.go` 的 `emitTelemetry`（**成功**终态 entry）
   从零构造 entry，**从不调用** `applyOriginMetadata` / `applyAutoRouteFields`；
   而 session v2 镜像**只镜像终态 entry**（`internal/sessionv2mirror/hook.go:82`
   刻意跳过初始 in_progress INSERT）⇒ **成功请求落进会话族时 100% 丢 origin 打标**；
   失败路径（`buildEntry`）反而带全套字段 ⇒ 「失败有标、成功无标」；
9. **★ 已落地修复**（本轮提交）：新增 `applyRequestContextFields` 并挂到
   `emitTelemetry` + `emitClientDisconnectProbe` 两个终态发射点；
   154 实测**漏标 205 行（hot）/ 155 行（分区表）**，全部 `client_ip=127.0.0.1`、全部成功请求。

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

> ## ★★★ 订正横幅（2026-10-23 晚，追加）——**本附录 A.3 的判定已被证伪**
>
> A.1 / A.2 的**观测数据全部正确**（四列全空、对照窗口分布），但
> **A.3 的判定与 A.4 / A.5 的成因解释是错的**。
>
> **错在哪**：A.2 用「**失败样本**」的规律去否定**成功样本**——
> 「真探测行必然带 origin_stage + client_ip=127.0.0.1」这句只在**失败**的
> 探测行上成立。事实是：**成功的探测行不带任何打标**（这正是缺陷本身），
> 所以拿失败样本的形态当「探测的定义」，必然把成功样本判成「不是探测」。
>
> **独立裁决（侧表 `request_context_attrs`）**：
>
> | 源 | 目标请求 `af1eb6d8…` 的判定 |
> |---|---|
> | `request_context_attrs`（侧表） | `origin_stage=node_probe` / `client_ip=127.0.0.1` / `is_probe=t` / `agent_name=go-client` |
> | `session_turns_hot`（会话族） | 四列全空 / success=true |
>
> ⇒ **用户是对的**：它是 `node_probe` 探测请求；「未标探测」是**真缺陷**。
> ⇒ 真根因与修复见 **附录 B**；本附录原文保留供追溯。
>
> ⚠ **A.4 / A.5 的两个结论一并作废**：「45.1% 的行四列全空」既不是
> 「claude-code 压缩重建设计使然」，也不是「go-client 间歇性丢失」——
> 而是**所有成功请求都这样**（成功路径从不应用 origin 元数据）。
> 「unknown 客户端桶」的观感来源成立，但归因与处置都变了。

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

> ★ **订正（追加，见附录 B.3.1）**：这句**观测**是对的（29 确实是业务键），
> 但**推论**错了 —— `session_turns.credential_id` 记的是「这次请求实际用了哪个
> **候选凭据**」，**不是「谁发起了这次请求」**。该请求真正的发起方是
> `api_keys.id=175`（`owner_user=self-check-worker`、`is_system=true`、
> `key_tier=system`、`remark=Auto-generated system key for self-check`）。
> ⇒ ★ **「凭据是业务 key ⇒ 不是探测」这条推理链从根上不成立**（凭据 ≠ 发起方）。

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

---

# 附录 B — ★★★ 真根因：成功终态路径丢 origin 元数据（2026-10-23 晚，追加）

> 本附录是**定论**：用户报的那条请求**确实是探测请求**，「没打标」是**真缺陷**，
> 根因已定位到代码行、已修复、已加回归门。
> 取证全程只读，**对生产的写入 = 0**。

## B.1 现象：一句话

> **成功的请求落进会话族时 100% 丢 origin 打标；失败的请求反而全套齐全。**

154 `session_turns_hot` 实测（同一张表、同一时刻）：

| origin_stage | 行数 | success |
|---|---|---|
| 非空 | 3546 | **100% = false** |
| 空 | 2798 | **100% = true** |

`session_turns` 分区表（2026-10-23）**同构**：非空 1061 行全 false / 空 959 行全 true。

★ 这个「成功⇄失败」的反相关就是指纹：**不是字段级丢值，是写入路径差异**。
（旁证：每个分组里 `null_stage = null_ip = null_all4` **完全相等** ⇒ 四列整包有/整包无。）

## B.2 代码级根因：三段链条

会话族（`session_turns_hot` / `session_turns`）的**唯一**写入来源是
`internal/sessionv2mirror` 镜像，而镜像的**唯一**输入是 `h.requestLogHook(entry)`。

**链条 1 —— 镜像只吃终态 entry，初始 INSERT 被刻意跳过**

`internal/sessionv2mirror/hook.go:77-84`：

```go
// _request_logs emits an in_progress INSERT before upstream execution.
// V2 turns are idempotent on request_id and cannot be updated after the
// first insert, so mirroring that placeholder would permanently record
// success=false/status_code=500 and drop the later success enrichment.
// Only terminal entries are valid shadow-write inputs.
if !entry.Success && !isTerminalFailure(entry) {
    return
}
```

⇒ **初始 in_progress INSERT 带的元数据，永远到不了 session_turns。**

**链条 2 —— 「请求上下文派生字段」只在两处被应用**

| 应用点 | 场景 | 带的字段 |
|---|---|---|
| `domains/streaming/handler.go:7815` `recordInitialRequestLog` | 初始 INSERT（**被镜像跳过**） | `applyOriginMetadata` ✅ |
| `domains/streaming/request_log_pipeline.go:1140/1182` `buildEntry` | 失败 / 限流终态 | `applyAutoRouteFields` + `applyOriginMetadata` ✅ |
| **`domains/streaming/handler.go:6055` `emitTelemetry`** | **成功终态** | **❌ 从不调用** |

⇒ `emitTelemetry` 的终态 entry 是**从零构造**的（`handler.go:6285`），
只有它自己算出来的字段（模型 / token / 时延 / 状态码）；
**凡是从 `RequestLogContext` 派生的字段全部丢失**：
`origin_stage` / `origin_actor` / `client_ip` / `client_forwarded_for` /
`client_endpoint` / `client_request_id` / `is_auto_request` / `task_type` /
`task_type_chosen` / `work_type`。

**链条 3 —— 五个镜像输入点，2 个漏补**

| # | 位置 | 场景 | 终态？ | 补 origin？ |
|---|---|---|---|---|
| 1 | `handler.go:6941` | **成功终态** | ✓ | **✗ 缺陷** |
| 2 | `handler.go:7278` | 客户端断连探针 | ✓ | **✗ 缺陷** |
| 3 | `handler.go:7803` | 初始 INSERT | ✗（镜像跳过） | ✓ |
| 4 | `request_log_pipeline.go:1248` | 失败终态 | ✓ | ✓ |
| 5 | `request_log_pipeline.go:1293` | 限流终态 | ✓ | ✓ |

## B.3 ★ 独立裁决：目标请求就是探测（侧表）

侧表 `request_context_attrs` 在**初始 INSERT** 时写入（`BuildContextAttrsEntry`，
走带元数据的那条路）⇒ 它保留了真相：

```
request_id     = af1eb6d83ef39610cc4f8be4e181b5a8
origin_stage   = node_probe        ← 探测 stage
client_ip      = 127.0.0.1         ← 本机回环
is_probe       = t
agent_name     = go-client
```

⇒ **同一个请求：侧表打标了，会话族没打标** ⇒ 两条真值管道分叉，
**实时拓扑读的是分叉的那条**。

### B.3.1 ★★★ 发起方 ≠ 被试凭据 —— 上一轮误判的**机制根源**

上一轮（附录 A）的推理链是：

> 「`credential_id=29` 是 `is_system=false` 的业务键、`owner_user` 空 ⇒
> ⇒ 它不是系统凭据 ⇒ ⇒ 探测必须持系统凭据 ⇒ ⇒ 所以它不是探测请求」

⇒ ★ **这条链从根上不成立**：**`session_turns.credential_id` 记的是
「这次请求实际被路由选中的**候选凭据**」，不是「谁发起了这次请求」。**

目标请求的两个身份（154 实测，`api_keys` 两行并列）：

| 键 | 在这次请求里的身份 | 证据 |
|---|---|---|
| `id=175` | **发起方**（探测 worker 自己的调用键） | `owner_user=self-check-worker` / `is_system=true` / `key_tier=system` / `key_prefix=sk-selfche****` / `remark=Auto-generated system key for self-check` |
| `id=29` | **被试候选凭据**（被路由选中去试的那把） | `owner_user` 空 / `is_system=false` / `remark=admin manual creation` |

⇒ 侧表 `request_context_attrs` 同时记着两个身份：
`api_key_id=175` + `owner_user=self-check-worker`（**发起方**），
而会话族只记 `credential_id=29`（**被试凭据**）—— ⇒ ★ **两表看的是两个不同的键**，
这正是「凭据是业务 key ⇒ 不是探测」能错得这么彻底的结构原因。

★ **量级佐证**（侧表，`owner_user=self-check-worker` 的行）：

| owner_user | origin_stage | is_probe | 行数 |
|---|---|---|---|
| self-check-worker | node_probe | t | **17,545** |
| self-check-worker | self_check | t | **633** |

⇒ 探测流量的真实规模远大于会话族里「看得见」的那点 ——
因为**成功的那些在会话族里丢了标**，落进 unknown / 业务桶。

⇒ ★★ **通用式**：★ **同一张表里若有多个「身份列」（发起方 vs 被选中方、
租户 vs 资源属主、发起者 vs 被作用对象），**取错列就会得出**结构性错误**的结论**
⇒ 判据：**先问「这一列在语义上是谁」**，再拿它做身份推理；★ **同名不同义**
（都叫 key / id / owner）是这类系统的经典陷阱 ⇒ ★ 交叉核验手段 =
**另一张表里记着另一个身份**（这里：侧表记发起方、会话族记被试方）⇒ ★ **两表不一致时，
先怀疑「它们看的是不同的东西」，而不是「其中一张表错了」**。

★ 另两组同源对照（同一 `credential_id` + 同一 `model` + 同一 `request_preview`）：

| (cred, model, prompt) | 失败侧（有 node_probe 标） | 成功侧（**无任何标**） |
|---|---|---|
| 29 / nemotron-3-ultra-550b-a55b / "user: ping" | 186 行 | **50 行** |
| 29 / nemotron-3.5-content-safety / "user: ping" | 176 行 | **78 行** |
| 21 / minimax-m3.1-flash / "user: ping" | 93 行 | **58 行** |

⇒ 同一个 worker、同一凭据、同一模型、同一 prompt：
**失败那次打标，成功那次不打标。**

## B.4 修复

新增 `domains/streaming/request_log_pipeline.go::applyRequestContextFields`
（只补空位 / first-write-wins），把成功终态与失败终态对齐：

- `applyAutoRouteFields` → `is_auto_request` / `task_type` / `task_type_chosen` / `work_type`
- `applyOriginMetadata` → `origin_stage` / `origin_actor` / `client_ip` / `client_forwarded_for`
  （ctx 权威 + DB 系统键兜底）
- `client_endpoint` / `client_request_id` ← `logCtx`

挂载点（**恰好是两个漏补的终态发射点**）：

- `domains/streaming/handler.go` `emitTelemetry`（成功终态）
- `domains/streaming/handler.go` `emitClientDisconnectProbe`（断连探针终态）

## B.5 回归门（3 层）+ 变异验证

新文件 `domains/streaming/request_context_fields_test.go`：

| 层 | 门 | 性质 |
|---|---|---|
| L1 | `TestApplyRequestContextFields{FillsOriginFromRequestContext,FillsAutoRouteFields,FillsEndpointAndClientRequestID,KeepsExistingOrigin,NilSafe}` | 行为门 |
| L2 | `TestSuccessTerminalEntryCarriesProbeOrigin` | **行为门（端到端）**：走真实 `emitTelemetry`，断言产出 entry 带 `origin_stage=node_probe` |
| L3 | `TestEveryMirrorInputPathAppliesRequestContextFields` | **结构门（AST）**：枚举本包全部 `requestLogHook(` 输入点，断言每个都（传递地）经过 `applyOriginMetadata` **且** `applyAutoRouteFields` |

★ **变异探针 3 臂全红**（逐个撤销 ⇒ 对应门变红 ⇒ 再按 md5 逐字还原）：

| 变异 | 撤销内容 | 变红的门 |
|---|---|---|
| A | `emitTelemetry` 的 `applyRequestContextFields` | L2 **+ L3** |
| B | `emitClientDisconnectProbe` 的 `applyRequestContextFields` | L3 |
| C | helper 内的 `applyAutoRouteFields` | L1 **+ L3** |

★ 全量回归：`domains/streaming` / `domains/session/...` / `internal/sessionv2mirror` /
`domains/stats` / `admin` / `middleware` / `internal/probekind` / `bg`
—— **10 包全绿（exit 0）**。

## B.6 影响面量化（154 生产）

用侧表做权威、把会话族里 `origin_stage IS NULL` 的行对回侧表：

| 表 | 侧表判定 | is_probe | 行数 | client_ip |
|---|---|---|---|---|
| `session_turns_hot` | business | f | 1884 | 外部 IP（真业务流量） |
| `session_turns_hot` | **node_probe** | **t** | **194** | 127.0.0.1 |
| `session_turns_hot` | **self_check** | **t** | **11** | 127.0.0.1 |
| `session_turns`（10-23 分区） | business | f | 1820 | — |
| `session_turns`（10-23 分区） | **node_probe** | **t** | **151** | — |
| `session_turns`（10-23 分区） | **self_check** | **t** | **4** | — |

⇒ **漏标 205 行（hot）/ 155 行（当月分区表）**，全部 `client_ip=127.0.0.1`、全部成功请求、
时间跨度 14:10→22:49 贯穿全天、`agent_name` 全是 `go-client`（探测 worker 的 UA）、
模型即探测目标集（minimax-m3.1-flash / nemotron-3.5-content-safety /
nemotron-3-ultra-550b-a55b / deepseek-v4-flash / glm-5.x …）。

> ★ **数字会随时间漂移（热表是滚动窗口）**：复核时点实测 business **2060** /
> node_probe **198** / self_check **11**（⇒ 漏标 **209** 行），相对上表约 1 小时的
> 新增量。★ **口径与结论不随漂移改变**（漏标 ⇔ 成功 ⇔ loopback 三个条件始终绑定）。

### B.6.1 ★ 为什么 hot 表 205 行、当月分区表只有 155 行？（表面差约 15 倍，**不是口径矛盾**）

这两组数**不矛盾**，它们覆盖的是**两段不重叠的时段**（154 实测）：

| 表 | 行数 | ts 跨度 |
|---|---|---|
| `session_turns_hot`（滚动窗口） | 5502 | **14:09:22 → 23:23:39** |
| `session_turns`（partition_date=2026-10-23） | 4909 | **08:00:02 → 14:09:14** |

⇒ ★ **hot 里 ts ≤ 分区表 max(ts) 的行 = 0 行**（实测）⇒ 两表**首尾相接、零重叠**：
热表装「最近 8~9 小时」，分区表装「更早的那段」⇒ **两个数字加总才是当日全量**。

★ 且**两段的比例一致**，说明漏标是**均匀发生的系统性问题**，不是某时段的异常：

| 时段 | 总行 | origin 空行 | 漏标（探测） | 漏标/总行 | 漏标/空行 |
|---|---|---|---|---|---|
| 分区表 08:00–14:09 | 4909 | 1975 | 155 | **3.2%** | 7.9% |
| hot 表 14:09–23:23 | 5502 | 2269 | 209 | **3.8%** | 9.2% |

⇒ 修复后这些行的 `origin_stage` 会变成 `node_probe` / `self_check`：
**实时拓扑不再把它们算成业务流量；stats 侧的「总请求数」也不再把它们当业务流量**
（stats 侧靠 `origin_actor` 白名单识别探测，而成功路径此前把 `origin_actor` 也丢了）。

## B.7 诚实边界

1. **修复只作用于新写入的行**：会话族的 turn 是幂等写入、**不可更新** ⇒
   154 上已有的 205 / 155 行**仍是历史行**，本轮**没有回填**（回填是另一次写操作，需属主拍板）。
2. **本轮未部署**：代码已修 + 门已立，但**没有发版**，154 上当前行为未变。
3. **客户端维度会连带变化**：修复后成功请求也会带 `client_ip` ⇒
   实时拓扑的 unknown 客户端桶会**显著变小**（这是修正，不是副作用）。
4. **结构性附带收益**：镜像自己的「探针合成会话排除闸门」
   （`IsProbeSyntheticSession` → `syntheticKindOf` 读 `origin_stage`/`origin_actor`）
   也依赖同一批字段 ⇒ 修复后，**无会话头的成功探测**会重新被正确排除
   （此前它们因 kind 退化成 `anon` 而被错误镜像进来）。154 当前无此类流量，
   该项**结构成立、未在生产观测到**。
5. **未触碰**：v1 `_request_logs` 族（已退役）、`bg/active_probe_emitter` 的直写路径
   （它自己设 `origin_stage`/`origin_actor`，不受影响）。
6. ★★ **附带撞见的独立缺陷（本轮只观察、只记录，未修）**：**会话族的分区表普遍落后**。
   154 实测（23:24 时点）：

   | 族 | 分区表 max(ts) | hot 表 max(ts) | 落后 |
   |---|---|---|---|
   | `session_turns` | **14:09:14** | 23:24:32 | **≈ 9h15m** |
   | `session_turn_details` | **15:11:22** | 23:24:32 | ≈ 8h13m |
   | `session_bodies` | **NULL（当天分区无行）** | 23:24:32 | **整天为 0** |

   ⇒ ★ 三个族**各自落后且各不相同** ⇒ 不是「一次统一停摆」，而是逐族 promote 各走各的
   （或某些族当天从未成功 promote）。⇒ ★ 与仓内已登记的 §143.96
   「hot 继续累积、★ 父表继续不收冷行」「`session_bodies_hot` 同款病灶未修」**吻合**
   ⇒ **不在本轮修复范围**（属主在 §143.96 那边有台账），此处只登记读数以便解释 B.6.1。
   ⇒ ★ **对本轮结论的影响：无**（B.6.1 已证明两段比例一致，漏标是均匀的系统现象），
   但**读会话族的历史数据时必须先看分区表落后多少**，否则会把「时段差」误读成「口径差」。

## B.8 通用式（本轮又踩到的一次量具自伤）

★ **拿有偏样本定义真值**：A.2 的对照窗口只采样了**失败**的探测行，
于是把「失败探测行带 origin_stage」当成「探测的定义」，
再用它去否定**成功**的那条请求 ⇒ 必然得出「它不是探测」的错误结论。
⇒ **对照窗口必须覆盖被判别对象的全部形态分支（成功/失败各一组）**，
否则对照组本身就是有偏的。

★ **「整包有 / 整包无」是路径指纹**：四个字段同时空或同时有 ⇒
指向「两条写入路径」，而不是「四个字段各自坏了」；
这比逐字段排查快一个数量级。

★ **「一个请求算不算 X」若有 N 处判定，N 处都要覆盖到**：
本轮先做了 N=4 处的口径收敛（附录 A 主体），
但**漏了第 5 处** —— 不是「判定清单」而是「**谁在什么时候写这批字段**」；
判据是「**终态写出的行**才进最终事实源」，于是「初始行带字段、终态行不带」就足以
制造 100% 的漏标，且**任何按清单比对的口径门都发现不了它**。

---

# 附录 C — ★★★ 154 发布与线上验证（2026-10-24 08:22–09:0x，只读取证 + 一次授权发布）

> 本附录记录：修复如何被发到 154、途中撞上的**与本缺陷无关的既有阻塞**、
> 以及**线上行为验证**的实测读数。
> ⇒ 结论：**修复已在生产生效**，观测到部署后新写入的成功请求第一次带上了 origin 打标。

## C.1 发布路径：为什么必须走干净 worktree

★ `scripts/deploy-seamless.sh` 是**就地构建**（`go build ./cmd/gateway` + 前端 build，
不走 `git archive`）⇒ **脏工作树会被直接发上生产**。

- 主工作区当时有并行会话的 14 个在途文件（含 `admin/settings.go` 的功能改动）；
  按「不把别人未提交的半成品塞进生产」的纪律，**本轮不碰**。
- ⇒ 新建**兄弟** worktree `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/lgw-deploy-154`
  （detached @ `origin/main` = `e316234d6`）：
  兄弟路径保证 `scripts/deploy-lib` 的相对符号链接仍解析到仓外 SSOT
  （`../../../deploy-lib` ⇒ `/Users/xutaohuang/workspace/ai-native-tools/deploy-lib`）。
- ★ 唯一的功能性脏改动（`admin/settings.go`：tenant-scoped 键的 rollback 改走租户端点）
  **改前改后都是 400，只差错误文案** ⇒ 干净部署**无功能回退**。

## C.2 ★ 首次尝试被「迁移 checksum 对账闸门」拒绝（安全中止，线上零影响）

- 断点：`[6.5/9] 切换前 pending 迁移 + db-changelog` ⇒ **符号链接未切换、未重启**
  ⇒ 154 全程跑旧版 `2825-8fbabe33` ⇒ **线上零影响**。
- 报错：`已应用迁移 checksum 不匹配，拒绝继续：430:430_sessions_v2_schema.sql`。
- 闸门语义（`scripts/deploy-lib/db-changelog.sh`）：对**每个已应用迁移
  （≥ `DB_LEDGER_RECONCILE_FROM`，默认 412）**比对台账 checksum 与本地文件 sha256，
  不符即 `return 1`（**fail-closed**）。
- ⇒ 离线逐字复刻该判定，154 上 399 条已应用迁移（≥412）的全量结果：

| 类别 | 条数 |
|---|---|
| checksum 一致 | 291 |
| **checksum 不匹配** | **75** |
| **文件名不匹配** | **12** |
| **台账缺 checksum / 无本地文件** | **21** |

- ⇒ ★ **这不是本轮改动引起的**：与探测打标无关的**既有阻塞**；且仓内审计早已登记
  （`R89-CT` 定级 **P1**；`R75`/`R78` 给出「收窄阈值」**P3** 处置）。

## C.3 ★ 处置（一次台账修正 + 一个官方阈值）

- 拦路的最后一条是 **`999|test_notice_capture`** —— 2026-08-13 直接对生产跑测试迁移
  留下的残渣，**仓内根本没有这个文件**（`find sql -name "999*"` 为空）；
  仓内审计文档早就把它标成「生产库里的一条测试行」，并在 `max(version::int)`
  查询里**手工排除**。
- ⇒ 备份后删除两行（**可原样插回**，备份逐字见下）：

| 表 | 备份内容 |
|---|---|
| `schema_migrations` | `999|test_notice_capture|2026-08-13 01:05:27.334725+08` |
| `llm_gateway_migration_checksums` | `999|999_test_notice_capture.sql|69393f8ac7f8243148885be469de129c0937f958db93e9379031d45579267011|2026-08-13 01:05:27.334725+08` |

- ⇒ 带 **`DB_LEDGER_RECONCILE_FROM=902`** 重跑（**官方旋钮**：
  `docs/db-changelog.md:8511` + playbook §76 登记）：
  - ★ 该阈值**同时**门控「已应用迁移的 checksum 校验」与「**pending 迁移的投递**」
    （`db-changelog.sh:278`）⇒ **不能抬到 1000**：那会让待投递的 **902 静默不投递**
    （台账停在 901、仓里却多一条迁移）⇒ 选 **902**：既放行校验，又保证 902 被投递。
  - 清理后「已应用且 ≥902」的版本数 = **0** ⇒ 校验集合为空 ⇒ 闸门通过。
- ⚠ 注意：154 与 252 **共用同一个 PG**（172.16.2.210）⇒ 台账修正是**共享**的。

## C.4 部署结果

| 项 | 读数 |
|---|---|
| 版本 | `version=2826-e316234d seq=2826`，`current → releases/2826-e316234d`，`verified=true` |
| 蓝绿 | old=`2825-8fbabe33` → new=`2826-e316234d`，`active_port=8781`，切换 13s |
| handoff | 12871ms（> 2s ⇒ 保留旧实例用于回滚；旧实例随后 drain 停止 ⇒ **无双实例写库**） |
| DB | 就绪 6s（`background-tasks=200`） |
| 凭据解密冒烟 | `providers=14,18,13092 creds=17 failed=0` |
| 迁移 | **902 `stats_event_dedup_pkey_include` 已落地** |
| 公网 | `https://llm.kxpms.cn/api/system/version` ⇒ `build_seq=2826` / `git_sha=e316234d` |
| 回滚 | `2825-8fbabe33` 仍在盘上 ⇒ `bash scripts/deploy-seamless.sh rollback 154` |

## C.5 ★★★ 线上行为验证（本轮的重点）

部署后（`ts ≥ 2026-10-24 08:49:23`）写入 `session_turns_hot` 的新行：

| success | origin_stage 为空？ | 行数 |
|---|---|---|
| f | 否（非空） | 11 |
| t | **否（非空）** | **8** |

⇒ ★★ **修复前的「零交叉格」被打破**：出现了 `success=t AND origin_stage NOT NULL`
这一格 —— 修复前它**恒为空**（100% 的成功请求不带打标）。

**决定性样本**（一次成功的探测请求）：

```
request_id = 8706d6e39432fa4692a7cdf12c446dea
ts         = 2026-10-24 08:50:18
origin_stage = node_probe      origin_actor = node-probe-worker
client_ip  = 127.0.0.1         agent_name   = go-client
model      = minimax-text-01   status_code  = 200
request_preview = "user: ping"
```

⇒ 这正是**修复前必然丢打标**的那一类行（成功 + 探测 + "user: ping"）；
⇒ 用户最初报的那条 `af1eb6d83ef39610cc4f8be4e181b5a8` 属于同一形态。

⇒ ★ **同时**：部署后的业务成功行也带上了 `origin_stage='business'` + **真实外部
`client_ip`（211.94.141.54）** ⇒ 实时拓扑的客户端维度不再把它们丢进 **unknown 桶**
⇒ 即 B.6 里那「45.1% 行缺 client_ip」的**修正**（不是副作用）。

⇒ **pill 的判定入口**：`extractProbeInfo`（`admin/probe_request_info.go:67`）四条路径里，
**路径 2（origin_stage）与路径 4（origin_actor）** 现在对**成功请求**也命中；
而实时拓扑的 tile 由 `cmd/gateway/main.go:2811` 的 `SetOnRequestLogEmitted` 钩子
喂入 ⇒ 喂进去的正是**终态 entry** ⇒ 终态 entry 现在带标 ⇒ **pill 由「否」变「是」**。

## C.6 诚实边界

1. ★ **历史行不回填**：修复只作用于**新写入**的行；会话族 turn 幂等、不可更新
   ⇒ 154 上此前那 200 余行漏标**仍是历史行**（回填是另一次写操作，需属主拍板）。
2. ★ **阈值收窄的代价**（P3，仓内已登记）：`DB_LEDGER_RECONCILE_FROM=902`
   ⇒ **75 条历史漂移长期不再被检出**。正解是把「应用后被改写」的迁移在**文件头**
   标 `SUPERSEDED`/`DEPRECATED`（工具内置的一等公民机制，`db-changelog.sh:275`
   会跳过它们），或跑 migration ledger repair —— ⚠ **但补录前必须逐条确认 DB 形态**
   （R89-CT 明确警告：补录 ＝ 把改动固化为「正确」）。⇒ **待属主裁决**。
3. **台账写入**：删除了 154/252 共用库里的 2 行 `999` 残留（备份逐字留存，可原样插回）。
4. **Redis live-store 在 154 上为空** ⇒ 未能从 Redis 侧直接读 tile；
   pill 的判定链（终态 entry → `extractProbeInfo` → 路径 2/4）已由代码与 DB 读数双向坐实。

---

# 附录 D — ★★★ 线上验证收尾与★一处口径订正（2026-10-24 15:20–17:50）

> 本附录做三件事：① 补上 C.6 第 4 条留的缺口（**实时拓扑 pill 的端到端观测**）；
> ② **订正附录 B 的一处口径**（「100% 漏标」不准确）；③ 给出修复的**双口径验收**。
> ⇒ 结论：**修复在生产生效，两类流量（业务 + 探测）的 origin 族漏标率均为 0**。

## D.1 ★★★ 实时拓扑 SSE 端到端观测（补上 C.6 第 4 条的缺口）

C.6 第 4 条留的缺口是「pill 由代码路径 + DB 读数双向坐实，但没拿到实际 tile 样本」。
本轮补上了：直连 `GET /api/admin/live-stream`（admin JWT）抓 SSE，落盘 260 个事件。

| 读数 | 值 |
|---|---|
| 抓到的 SSE 事件 | 260（类型：`request` 79 / `incident_update` 107 / `child_request` 24 / `node_update` 15 / `queue_snapshot` 12 / `idle_marker` 11 / `request_lifecycle` 10 / `initial_data` 1 / `snapshot_refresh` 1） |
| 顶层 `request` tile 中 `is_probe=true` | **49**（状态分布：`failure` 39 / `in_progress` 6 / **`success` 4**） |
| 泳道聚合（`delta.changed_lanes.<维度>[].requests[]`）里的 distinct 探测 id | 4004（状态：`failure` 3445 / **`success` 555** / `in_progress` 4） |

★ **决定性样本（成功 + 探测 = 修复前必丢标的那一类）**：

```
request_id    = probe-direct-c50-mkimi-k3-a5-ok-1792826461234326302
ts            = 2026-10-24T07:21:02Z     status = success
is_probe      = true      probe_origin = direct      probe_attempt = 5
model         = kimi-k3   credential_id = 50（商汤-3）

request_id    = probe-direct-c19-mnvidia_ising-calibration-1.5-31b-a2-ok-1792826463266168796
ts            = 2026-10-24T07:21:04Z     status = success
is_probe      = true      probe_origin = direct      probe_attempt = 2
model         = nvidia/ising-calibration-1.5-31b   credential_id = 19（NVIDIA NIM）
```

⇒ **修复前「成功 ⇒ 必无标」⇒ pill 必为「否」；现在 pill 实测为「是」。**

### D.1.1 ★ 一次「量具」失误（值得记）

第一版抓取脚本在**顶层**找 `is_probe`，得到 **0 命中**。
⇒ 第一嫌疑不是「没有探测」，而是**查错了层级**：
`is_probe` 位于 **① 顶层 `request` 事件的 `request` 对象内**（`LiveRequest` 结构，json tag `is_probe,omitempty`），
以及 **② `delta.changed_lanes.<维度>[].requests[]` 的泳道条目内**；
**不是**在 SSE 事件的外层信封上。
⇒ ★ 与本轮前几轮同族：**0 命中的第一嫌疑是量具坏了**（这次是"层级"坏了，不是"数据"坏了）。

## D.2 ★★ 订正：附录 B 的「100% 漏标」不准确

附录 B.2 写的是「**成功请求落进会话族时 100% 丢 origin 打标**」。
本轮用**同一时间窗内并存的两组**成功探测行做了判别，**该说法需订正为「不是 100%，但缺口是系统性的」**。

### D.2.1 判别读数（`session_turns` 分区 `2026-10-23`，探测且成功的行）

| 组 | 行数 | `origin_actor` | `origin_stage` | `client_ip` | 终态字段（latency/tokens/cost/error_kind） | ts 跨度 |
|---|---|---|---|---|---|---|
| A：带标 | **55** | 有（`node-probe-worker` 50 / `credential-selfcheck-worker` 5） | 有 | **55/55 有** | 全有 | 10-24 02:32:47 → 07:56:45 |
| B：漏标 | **408** | **全空** | **空** | **0/408 有** | 全有 | 10-23 08:04:06 → **10-24 06:41:46** |

★★ **两组 ts 区间重叠**（10-24 02:32–06:41 同时存在带标与漏标行）
⇒ **不是「某个时点上了线/没上线」的时间切换**，而是**两条并存的行为分支**。
⇒ 且两组的**终态字段完全一样**（都是终态行，不是"只写了初始行"）
⇒ ⇒ **两组 entry 的唯一差别就是整个 origin 族**（`origin_stage` / `origin_actor` / `client_ip`）。

### D.2.2 订正后的准确表述

> **修复前**：会话族里**成功**请求的 origin 族**整体丢失**——
> 业务流量丢 `origin_stage='business'`（10-24 pre-fix：业务成功行 **122/122 全丢**），
> 探测流量丢 `origin_stage='node_probe'`（同窗 463 条探测成功行里 **408 条丢**）。
> 之所以不是 100%，是因为存在**恰好在终态发射之前已被填过 origin 族**的样本（55 条）。
> ⇒ **订正**：不是「100% 丢」，是「**系统性地丢，少数样本被别的路径偶然填上**」。

⇒ ★ **但这不改变根因与修法**：既然 408 条与 55 条的**唯一差异就是 origin 族**，
那么「成功终态路径不把 ctx 的 origin 族搬进 entry」就是**共同成因**；
修法（`applyRequestContextFields` 挂在 `emitTelemetry`）**同时覆盖两组**。

### D.2.3 ★★ 一个此前被低估的后果：`client_ip` 一并丢失

B 组 408 条的 `client_ip` 是 **0/408 非空**（A 组 55/55 非空）。
⇒ ⇒ **丢的不是一个字段，是整族**（`origin_stage` / `origin_actor` / `client_ip` 同生共死）。
⇒ ⇒ 这直接解释了实时拓扑的**客户端维度**为何把这些行推进 **unknown 桶**：
它们不是"没有客户端信息"，而是**客户端信息在落库时被丢掉了**。

## D.3 ★ 修复验收（双口径，读数取自两处且互相独立）

### D.3.1 月分区表 `session_turns`（`partition_date='2026-10-24'`，切点 = 部署时刻 08:49:23）

| 口径 | 窗口 | 行数 | **漏标（origin_stage 为空）** |
|---|---|---|---|
| 业务成功行 | pre-fix | 122 | **122（100% 漏）** |
| 业务成功行 | **post-fix** | 106 | **0** |
| 探测成功行 | pre-fix | 7 | 0（属 §D.2 的 A 组路径） |
| 探测成功行 | **post-fix** | 15 | **0** |

### D.3.2 hot 表 `session_turns_hot`（独立读数）

| 窗口 | success | origin_stage 为空 |
|---|---|---|
| post-fix | f | 0 |
| post-fix | **t** | **0**（修复前该格恒为 290 行） |

post-fix 成功行的 `origin_stage` 分布 = `business` **236** / `node_probe` **108** / `self_check` **16**
⇒ **合计 360 条成功行，全部带标，零遗漏。**

## D.4 ★★ 修复首发时点（用二进制符号判定，不靠目录名）

`deploy-seamless.sh` 是**就地构建** ⇒ release 目录名里的 sha **不保证等于**二进制内容。
⇒ 用 `strings <binary> | grep applyRequestContextFields` 逐个 release 判定：

| release | 构建时间 | `applyRequestContextFields` |
|---|---|---|
| `2817-a55a4571` | 10-23 05:42 | **0** |
| `2819-412f7f95` | 10-23 17:59 | **0** |
| `2818-64592772` | 10-24 06:37 | **0** |
| `2825-8fbabe33` | 10-24 07:29 | **0** |
| **`2826-e316234d`** | **10-24 08:49** | **1** ← **修复首发** |
| `2840-3e47b1fe`（当前 `current`） | 10-24 15:42 | 1 |

⇒ ★ **2826 是第一个含该修复的 release** ⇒ 修复的**首次上线时刻 = 2026-10-24 08:49**，
与部署记录一致；此后并行会话又连续发布了 2827–2840，**修复在当前生效版本中仍在**。

### D.4.1 ★ 顺带把「根因叙述」也用源码证伪过一次（防自己写错）

> 复核 `412f7f95`（2819）的 `domains/streaming/handler.go`：
> `emitTelemetry` 起于 **6055** 行，其函数体内**没有** `applyOriginMetadata`；
> 仓内唯一带该调用的成功相关入口是 `recordInitialRequestLog`（**7683** 行起，7798/7815 调 `applyAutoRouteFields`/`applyOriginMetadata`）。
> ⇒ **根因叙述成立**：2819 的成功终态路径确实不搬 origin 族。
> ⇒ ★★ 同时也说明：**§D.2 的 A 组（55 条带标）不可能是 `emitTelemetry` 写的**
> ⇒ 它们来自**另一条**填充 origin 族的路径（**具体填充点未定位，见 D.5**）。

## D.5 ★ 未决点（诚实边界）

1. ★★ **A 组 55 条的 origin 族填充点未定位**：它们有 `origin_actor`（`node-probe-worker` /
   `credential-selfcheck-worker`）⇒ entry 在终态发射前已被填过 origin 族，
   而 2819 的 `emitTelemetry` 不做这件事 ⇒ 存在**第三条**填充路径。
   ⇒ 登记为**待查**。⚠ **不影响验收**：修复后两类都带标（漏标率 0，双口径验证）。
2. ★ **历史行不回填**：修复只作用于**新写入**的行；会话族 turn 幂等、不可更新
   ⇒ 用户最初报的那条 `af1eb6d83ef39610cc4f8be4e181b5a8`（ts **2026-10-23 14:37:14**，
   在 `session_turns` 分区 `2026-10-23`）**至今仍无标**：
   `success=t` / `origin_stage` 空 / `client_ip` 空 / `agent_name=go-client` / 200；
   而**侧表同一条一直是对的**（`is_probe=t` / `origin_stage=node_probe` / `client_ip=127.0.0.1` /
   `owner_user=self-check-worker` / `api_key_id=175` / ts 14:36:56）。
   ⇒ **回填 = 生产写，待属主拍板**。
3. ★ **A 组与 B 组的 ts 区间重叠**这个事实**本身**说明「漏标」不是纯时间函数；
   若将来要进一步收敛，需要按 `origin_actor` 有无（而不是按时间）继续二分。
4. **阈值收窄的代价**（同 C.6 第 2 条）：`DB_LEDGER_RECONCILE_FROM=902` ⇒ 75 条历史漂移不再被检出；
   正解（`SUPERSEDED`/`DEPRECATED` 标注或 ledger repair）**待属主裁决**。
5. **会话族分区表普遍落后**（§143.96 台账内）只记录未修；本轮观测到 hot 表在观测期间
   发生了 promote（同一批行从 hot 移到分区表）⇒ **跨表比较必须用固定分区表口径**，
   否则会在 promote 窗口里读到自相矛盾的数（本次已踩，见 D.2 的窗口选取）。

## D.6 ★ 追查「第三条 origin 填充路径」的结果：5 个候选被证伪，待查点收窄（2026-10-24 17:50–18:40）

D.5 第 1 条登记的未决点，本轮继续追查。**结论：不是「第三条路径」，而是两张表的写入器本就分家**；
那 55 条带标行的产生机制**仍未定位**，但**候选已从 5 个收敛到 1 个**，且**不影响验收**。

### D.6.1 ★★ 结构性发现：侧表与会话族是**两个不同的写入器**（这才是「两表分叉」的根）

| 目标表 | 写入器 | 读来源 | 搬运方向 |
|---|---|---|---|
| **侧表** `request_context_attrs` | `domains/streaming/context_attrs.go:138`（`fillFromRequestLogContext`，写 **`telemetryv1.ContextAttrsEntry`**） | `RequestLogContext.OriginStage`（HTTP 入口由 `origin_mw` 写进 ctx，再由 `request_log_pipeline.go:1765` 回写进 ctx） | **ctx → ctx** ⇒ **永远对** |
| **会话族** `session_turns` | `internal/sessionv2mirror`（`s1a_fields.go:153-154` 忠实拷贝 `entry.OriginStage`/`entry.OriginActor`） | `telemetry.RequestLogEntry` | **ctx → entry ⇒ entry → 镜像** ⇒ 断在第一段 |

⇒ ★★ **`context_attrs.go:138` 不是「第三条路径」**：它写的是**侧表**的 entry 类型
（`ContextAttrsEntry`），与会话族镜像消费的 `telemetry.RequestLogEntry` **不是同一个对象**。
⇒ ★★ 由此得到「两表分叉」的**结构性解释**：
**侧表走 ctx、永远正确；会话族走 entry、修复前在成功终态上断链** ⇒ 同一请求两表打标可以不一致。

### D.6.2 ★ 5 个候选的证伪过程（逐条附判据与读数）

| # | 候选假设 | 判据 | 读数 | 结论 |
|---|---|---|---|---|
| 1 | 「存在第三条写 entry 的 origin 的函数」 | 全仓枚举 `entry.OriginStage =` / `entry.OriginActor =` | 写入点只有 3 处：`request_log_pipeline.go:1800/1802`（**`applyOriginMetadata` 内部**）、`request_log_pipeline.go:553`（`applyAutoRouteFields` 填 `OriginActor`）、`telemetry/client.go:2640/2646`（`ApplyOriginFromContext`，**在 `applyOriginMetadata` 内**）⇒ **全部收敛到 `applyOriginMetadata` 一个函数** | **证伪**（不是三个写入器，是一个） |
| 2 | 「带标从某个时刻起（网关换版）」 | 两组 ts 区间 | 带标 02:32–07:56，漏标 10-23 08:04–**10-24 06:41** ⇒ **区间重叠**（02:32–06:41 两组并存） | **证伪**（不是时间切换） |
| 3 | 「初始 INSERT 填的 origin 被终态 first-write-wins 保留」 | 读 `recordInitialRequestLog` 代码 | 它构造的是**独立的 entry 对象**，经 `h.requestLogHook(reqLog)`（`handler.go:7835`）发出；而镜像 hook **跳过 in_progress 初始 entry**（`hook.go:82`）⇒ 初始 entry **不进会话族**，也不可能被终态复用 | **证伪** |
| 4 | 「同一请求先失败后重试成功」 | 侧表 `is_retry` / `attempt_no` | 两组 **全部** `is_retry=false`、`attempt_no` 为空 ⇒ 无重试痕迹 | **证伪** |
| 5 | 「某个版本的成功终态路径本来就带 origin」 | 逐版本读 `emitTelemetry` 函数体 | `2817(a55a4571)`：`emitTelemetry` 6045..7061，**体内无** origin/auto-route 调用；`2819(412f7f95)`：6055 行起，**体内无**；`2826(e316234d)`：体内有 `applyRequestContextFields`（**本轮修复**）⇒ 55 条横跨 4 个在线版本，其中 2 个可读版本**都不带** origin | **证伪**（不靠某个版本的代码差异） |

⇒ ★★ **由 #1 推出的硬约束**：`applyOriginMetadata` 是**唯一**写 `entry.OriginStage`/`entry.OriginActor` 的函数
⇒ ⇒ **那 55 条的 entry 必然经过 `applyOriginMetadata`**；
而 `2817`/`2819` 的**终态成功路径**不调它、`初始 INSERT**又被镜像跳过**
⇒ ⇒ **它必然走过一次「失败/限流终态」（`buildEntry`，`request_log_pipeline.go:1182` 有该调用）**，
随后同一行被成功终态 UPDATE 覆盖成 `success=true`，而 origin 族被 COALESCE 保留。

### D.6.3 ★ 收窄后的待查点（一句话）

> **哪些请求会在生命周期内先出现一次「失败/限流终态」、随后又出现一次「成功终态 UPDATE」**
> （候选：上游重试跨过失败判定 / 限流后放行 / 流式中断后的补写）？
> ★ `session_turn_details` **无判别力**（两组都有留痕：带标组 10/10、漏标组 368/368；
> `node_switch_count` 与 `rate_limit_status` 两组皆 0）⇒ 需要更细的写入时序证据。

⇒ ⚠ **该待查点不影响验收**：修复后（post-fix）两类都带标，**漏标率 0**（D.3 双口径读数）。
⇒ ⚠ 且**这 55 条本身不是缺陷**（它们带标 = 行为正确）⇒ 真正的缺陷面是那 **408 条丢标行**。

### D.6.4 ★ 顺带记录的三个「量具/口径」教训

1. ★★ **`git rev-parse --verify "<短 sha>^{commit}"` 对**不在本仓**的 sha 会**静默失败**（返回空）
   ⇒ `2818-64592772` / `2825-8fbabe33` **不在本仓**（疑似被 force-push / rebase 掉的历史状态）
   ⇒ ★ 脚本里必须**显式判空并报错**，不能把「解析失败」当成「没有差异」。
2. ★★ **`session_turns` 的 `partition_date` ≠ 事件日**：分区 `2026-10-23` 里装着 **10-23 08:00 → 10-24 09:36** 的行
   ⇒ 用 `partition_date` 当时间维度会得出「10-23 有 55 条 10-24 的行」这种自相矛盾读数
   ⇒ ★ 时间维度一律用 `ts`，`partition_date` 只用来圈定**稳定窗口**。
3. ★★ **`session_turn_details` 有 61 列**，其中**没有** `request_status` / `stream_interrupted`
   ⇒ 查它之前先读 `information_schema.columns`（本轮连踩两次列名不存在）。
