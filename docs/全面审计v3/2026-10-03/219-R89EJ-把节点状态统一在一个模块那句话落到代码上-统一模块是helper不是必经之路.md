# 219 号（R89-EJ）：把 objective 那句「节点状态统一在一个模块」落到代码上量了一次——统一模块是 helper 不是必经之路，而十个恢复入口里有一个完全没走它

> 结论先行：
> 1. **🔴 待裁决 85（P1 按其自身契约）**：`POST /api/admin/diagnostics/routing-blocked/fix`（**按供应商批量修复**）清了 DB 侧 4 张表 + 路由缓存，**没有清进程内熔断器 / fpslot Redis / key 缓存 / key rotator / URSM v2**，却照样返回 HTTP 200 与 `"provider force-recovered"`。而它该调的那个 helper 的注释**原文写明**不调它会「return HTTP 200 while the router still filters the node out」。同族另两个恢复入口都已做对，**它是唯一没跟上的那一个**——而它恰是出事时最该用的那个。
> 2. **🟠 F2（零逻辑可修，已修）**：`internal/routeguard/routeguard.go` 的守卫**理由**写着「`state_sync.go` 只写不读，全仓无恢复路径」——**已被证伪**。`circuit_state` 有一条活的读路径且在 dispatch 热路径上。守卫的**不变式仍然正确**，但它写下的「事实」错了，而错误的理由会让下一个人把守卫当成可删之物。
> 3. **我本轮被推翻三次**，其中一次是被子代理推翻的：我在中途说过「`v_routable_credential_models` 不引用 `circuit_state` ⇒ DB 侧闸清干净也没用」——**这个结论是错的**，见 §四。
> 4. 新增守卫包 `internal/healthstateguard`（0.5~1.2s），把「9 层状态层 × 10 个恢复入口」钉成可执行登记表；**判据迭代了三版，每版都被实测推翻一次**。

---

## 一、起手：先查前轮覆盖，避免把旧结论当新发现（§153）

objective 里有两条与本轮主题相邻的要求，我按纪律先查了它们的前轮状态：

| objective 原文 | 前轮状态 | 本轮动作 |
|---|---|---|
| 「这些错误需要单独记录到一个 log 错误表中…呈现在凭据的详情下」 | **149 号已查透**（链路四维度全过；两处缺口：两套并行错误表、`resolved` 36,401 行全 false 且前端 0 命中），45/122/125 号亦已覆盖 | **不重查**（重查只会重复 149 号的结论） |
| 「全局的节点状态需要统一，状态更新需要在一个模块中进行」 | 86 号确认 `domains/nodestatecache` **整包未接线**（方案文档明文规定的门禁结果，可用代码非坏代码，已登记待裁决） | **换问法**：不查「统一模块在不在」，而查「**实际在改状态的地方有几处、形状是否一致**」 |

⇒ 219 号的切入点是那句 objective 的**后半句**：「状态更新需要在一个模块中进行」。86 号已经回答了「统一模块存不存在」，但没回答「**没有它的时候，状态是谁在改**」。

## 二、把那句 objective 量出来的答案

`credentials` 的健康列族（`deploy/sql/schemas/baseline/01-schema.sql:6817-6884`）：
`circuit_state`、`consecutive_failures`、`cooling_until`、`circuit_opened_at`、`health_status`、`availability_state`、`quota_state`、`state_reason_*`、`*_recover_at`、`probe_consecutive_failures`。

**`circuit_state` 的生产写入方共 6 个文件**（本轮逐个重数，未采信任何记忆）：

| # | 位置 | 写什么 | 触发方 | 走统一模块吗 |
|---|---|---|---|---|
| 1 | `domains/credential/state_sync.go:137` | `'half_open'` | 熔断器回调 | —（它**就是**镜像模块） |
| 2 | `domains/credential/state_sync.go:151-155` | `$1`（closed/open，QUARANTINED→open） | 熔断器回调 | — |
| 3 | `domains/credential/writer.go:87` | `'closed'` | 真实流量成功（`executors/execution_recorder.go:42`） | ❌ |
| 4 | `admin/routing.go:1603`（const `forceEnableCredentialSQL` @1595） | `'closed'` + 6 个状态列全清 | HTTP force-enable | ✅（`:5844`） |
| 5 | `admin/routing.go:1808` | `'closed'` | HTTP emergency-repair | ✅（`:1845`） |
| 6 | `bg/credential_recovery.go:697` | `'closed'` | 后台 ticker | ❌（跨进程，**碰不到进程内熔断器**） |

⇒ **`state_sync.go` 只占 2/6**。它自己把语义写得很诚实（`:30-41`）：异步单 worker、队列满即丢、**不重试**、last-writer-wins、DB 列是「近似信号」。

**所以那句 objective 的准确答案是**：进程/Redis 侧**确实有一个统一模块** `resetInMemoryNodeState`（`admin/routing.go:1974`，一次清三层：进程内熔断器 + fpslot Redis NodeState + 可选 credentialstate 缓存），但它是**按需调用的 helper，不是必经之路**；DB 侧则**根本没有**统一模块，20+ 个文件各写各的（子代理枚举 49 个写入点，见 §附录）。

## 三、🔴 待裁决 85：批量修复入口只清 DB 侧

### 3.1 事实

`admin/diagnostics_routing.go:254 handleRoutingBlockedFix`（`POST /api/admin/diagnostics/routing-blocked/fix`，`admin/handler.go:1465` 注册、`superAdmin(...)` 包装、`ctx` 超时 15s）：

它清 5 层：`credentials`（`:277-293`）、`credential_model_bindings`（`:299-310`）、`model_probe_state`（`:316-330`）、`node_probe_state`（`:336-352`，注释标为「R36 dual-clear contract」）、路由缓存（`:358 InvalidateAllCandidateCache` + `:361 pg_notify('auto_route_refresh')`）。

它**不清**：`resetInMemoryNodeState`（进程内熔断器 + fpslot Redis NodeState + credentialstate 缓存）、`InvalidateCredentialKeyCache`、`ResetKeyRotatorForCredential`、`ClearStateForTenant`（URSM v2）——**对该文件做符号级 grep，4 个符号 0 命中**。

然后 `:365` 返回 `{"triggered": true, "message": "provider force-recovered: credentials / bindings / probe_state reset"}`。

### 3.2 为什么定为 P1（不是 P2）

三条独立理由，任何一条单独成立都不够，三条同时成立才是 P1：

1. **它违反的是这个 helper 自己写下的契约**，不是我的推断。`admin/routing.go:1962-1969`：
   > "Without this, force_enable / clear_circuit **return HTTP 200 while the router still filters the node out** until the in-memory cooling windows expire (breaker cooling / fpslot 300s cooldown / credentialstate Redis TTL up to 5 min)"

   本入口返回的正是 HTTP 200 + 「已修复」语义，而它就是那个 "this"。
2. **它的 WHERE 恰恰专门 targeting 最需要清进程内熔断器的那批凭据**：`:288-291` 的条件是 `circuit_state IN ('open','half_open') OR consecutive_failures > 0 OR availability_state IN (...)`。
3. **同族另两个恢复入口都做对了，而且是审计驱动做对的**：`handleForceRecoverSingle` 的 `:319-322` 注释写着「R21（2026-09-13）audit P2: the comment above claimed to mirror applyForceEnable's recovery chain **but skipped resetInMemoryNodeState** — in-memory circuit cooling / fpslot cooldown kept filtering the node for up to ~5 min after a "successful" recovery click」，`:325` 就是那次修复。`applyForceEnable` 则清**全部 9 层**。

⇒ 批量入口不是「另一种设计」，是**唯一没跟上的那一个**。

### 3.3 机制上为什么「清了 DB 也不够」

子代理找到、并由我独立核实的那条读路径（`provider/client.go`）：

```
:1639  candidateQuerySQL  投影 COALESCE(c.circuit_state,'closed') AS circuit_state
:1906  Scan 进 Candidate.CircuitState
:316   UnavailableReason(): CircuitState=="open" ⇒ reasons += "circuit:open"
:290   IsAvailable() := UnavailableReason()==""
executors/executor_dispatch.go:120/170-171  dispatchCandidateAllowed 逐候选准入（主 dispatch 热路径）
```

⇒ **DB 里的 `circuit_state='open'` 会真实摘掉候选**（唯一例外是 `PinCredentialID` 的可信探测豁免，`:169-171`）。而进程内还有 `executors/executor_dispatch.go:810` 的 `e.Circuit.Allow(...)` 与 fpslot/URSM 的独立闸门。

⚠️ **另一条被登记为「待查」而没有下结论的**：`handleEmergencyRepair` 与 `handleForceRecover` 都**不重置 `node_probe_state`**，而 `v_routable_credential_models` 的合取里有一项
`NOT EXISTS (SELECT 1 FROM node_probe_state nps WHERE nps.credential_id=cmb.credential_id AND nps.raw_model_name=pm.raw_model_name AND nps.last_direct_ok=false AND nps.next_retry_at>now())`
⇒ **清了 credentials 也可能仍不可路由**。本轮**没有**追到底（需要真库与并发时序），明确记为**待查**，不写成缺陷。

### 3.4 为什么不擅自修

修它需要三件本轮**没有**的事：

1. **批量语义要产品定**：`handleRoutingBlockedFix` 是「按供应商批量修复」，与 `applyForceEnable`（「强制启用」，**故意**清 `quota_state='ok'`，因为它就是覆盖）语义不同。批量修复该不该也覆盖配额？本轮**判不了**。
2. **规模与超时**：该 handler 的 `ctx` 只有 15s，而 `resetInMemoryNodeState` 内部要**枚举该凭据的全部绑定模型**（`:1993-1998`）再逐模型打 Redis。批量到供应商级（N 个凭据 × M 个模型）就是 N×M 次 Redis 往返 ⇒ **naive 的平铺修复很可能中途超时，留下半清理状态**——那比现在更糟。
3. **本轮的环境不允许验证**：无 Redis、无真进程（见 §六）。

⇒ 登记为**待裁决 85**，并把三个选项连同各自代价写清：

| 选项 | 做法 | 代价 / 风险 |
|---|---|---|
| **A（推荐）** | 批量入口在 DB 事务后对**受影响凭据**调 `resetInMemoryNodeState`，**分批 + 汇总 outcome**，把「已清理 N/M」写进响应 | 需新增 `RETURNING id` 或前置 SELECT；必须处理超时与部分失败；改动面中等 |
| **B** | **不动代码，让响应说实话**：响应里加 `in_process_state_cleared: false` 与「进程内冷却可能仍持续 ≤5min」的提示 | 成本最低、零行为风险；但运维仍需等窗口 |
| **C** | 只清 `circuit_state/cooling_until` 对应的**进程内熔断器**（`circuitResetter.Reset`），不碰 fpslot/URSM | 折中；覆盖了本入口最可能触发的场景（breaker cooling），但 fpslot 300s 冷却仍在 |

## 四、🟠 F2：一个已登记守卫的「理由」被证伪（已零逻辑订正）

`internal/routeguard/routeguard.go` 的包注释原文：

> `credentials.circuit_state` / `cooling_until` 是**内存熔断器的单向观测镜像**（`state_sync.go` **只写不读，全仓无恢复路径**），而且它今天不在路由视图里。

- **「不在路由视图里」成立**：`sql/objects/views/v_routable_credential_models.sql:17-19` 的 `is_routable` 十二项合取逐字读过，确无 `circuit_state`。
- **「只写不读，全仓无恢复路径」不成立**：上面 §3.3 那条读路径逐跳核实，且子代理独立得出同一结论。
- **它为什么没看见**：`routeguard` 的扫描面**只有 SQL 文件**（`RoutingViewFiles` 只 walk `*.sql`），那条读路径全在 Go 里，**落在守卫视野之外**。

⇒ **守卫的不变式保留**（禁止这两列进合取仍然正确且必须保留），**只订正理由**，并把理由换成可验证的版本：不是「没人读它」，而是「**它的写入是异步可丢、不重试、last-writer-wins 的近似信号**（`state_sync.go:30-41` 自陈），拿近似值做**合取**会把不可靠放大成整行凭据出局」。

**为什么这条值得改而别的注释漂移可以不改**：错的不是某个旁注，而是**这道守卫成立的理由本身**。下一个人读到「只写不读」会合理地推断「这列是惰性的」，进而可能把守卫当多余的删掉——**错误的注释比没有注释更贵**，因为它会主动误导。

⚠️ **我本轮在同一条上自己也错了一次并已订正**：我在中途说过「`v_routable_credential_models` 完全不引用 `circuit_state` ⇒ 把 DB 侧那 5 个闸清干净并不会让路由器重新接纳该凭据」。**这个推论是错的**——`circuit_state` 走的是**另一条查询**（`provider/client.go:1639`）而不是那个视图，我只看了视图就下了全局结论。保留这段订正，不抹掉。

## 五、新守卫 `internal/healthstateguard`：判据迭代了三版

**存在理由**：把「每个恢复入口清了哪些层」变成可执行契约。**9 层**（DB 四张表 + 路由缓存 + key 缓存 + key rotator + 进程内熔断器/fpslot/credentialstate + URSM v2），**逐层必须表态**，未处理的层**必须写理由**。

**发现口径的三版迭代（每版都被实测推翻，全部记录在判据注释里）**：

| 版 | 口径 | 实测结果 |
|---|---|---|
| 1 | 同一行同时有健康列字面量与 `SET` | **漏掉 `handleEmergencyRepair`** —— Go 的 SQL 多行排版，`SET` 与列名不在同一行。而它恰是本轮最该扫出来的函数 |
| 2 | 函数体同时有 `UPDATE ` 与健康列字面量 | 扫出 32 个，其中 **5 个一层都没碰**（`createProvider`/`toggleProvider`/`fetchReorderScope`×2/`handleRoutingCandidateBindingReorder`） |
| 3 | 命中任一层符号 | 扫出 **41 个**，其中 37 个只能盖上「非恢复入口」这种**橡皮图章** ⇒ **登记表一旦掺假就没人信它** |
| **现行** | **名字像恢复入口 ∧ 命中至少一层** | 收敛到 **10 个**，每一个都是人工读过、层覆盖有实质差异 |

**另外两个必须写进判据的机制**：

- **常量内联**：`forceEnableCredentialSQL` 是**文件级 const**，执行它的 `applyForceEnable` 函数体里**根本没有**那几列的字面量（只有 `tx.Exec(ctx, forceEnableCredentialSQL, ...)`）。不内联常量，判据会把**唯一一处「一键清全部健康列」的 SQL** 判成「没写健康列」⇒ 与 §158「下限必须钉在宽集合」同族。
- **锚点取符号不取列名**：列名会出现在只读代码里（`admin/routing.go:3425` 的 `if c.CircuitState == "open"` 是管理端列表渲染），用列名当锚点会把读者算成写者。

**负控 4 个**（全部跑在未修复的登记表上，逐个精确回退）：

| 编号 | 变异 | 实测 |
|---|---|---|
| NC-A | 把 `handleRoutingBlockedFix` 的 `inProcessNodeState` 谎报为 `true` | ✅ 红：「登记说清了…但函数体里 [resetInMemoryNodeState] 一次都没出现」 |
| NC-B | 从 `applyForceEnable` 的 `Done` 里**删掉** `ursmV2` 这一键 | ✅ 红：「这一层没有表态 —— 必须写 true 或 false。**「没表态」会被下一个接手的人读成「已覆盖」**」 |
| NC-C | 把一个入口在登记表里改名（模拟漏登记） | ✅ 红并点名未登记的那一条 |
| NC-D | Makefile 去掉 `./internal/healthstateguard` 登记 | ✅ 211 号那条登记断言红 |

⚠️ **残余盲区如实登记**：一个**名字不含** `recover|repair|reset|restore|enable|blocked` 的恢复入口能逃掉这张表。已写进判据注释，不假装闭合。

**接线**：`Makefile` 补登记（**必须**登记：`guards-sync.sh` 按磁盘 `find internal -name '*guard'` **发现式**枚举，CI `audit-guards-ci.yml:87` 会跑它 ⇒ 漏登记直接让 CI 转红）；`internal/sqlguard/guard_wiring_test.go` 的 `mustHave` 同步补（211 号：登记断言必须放在**已在登记表里**的包）。

**预算实测**（与 `make guards` 同参数 `-count=1 -timeout=120s`）：**全 13 包绿**，新包 **1.187s**，最慢的 `sql/schema` 28.136s，墙钟 **30.1s**。`guards-sync.sh` 的 PowerShell 代理：on_disk `*guard` **9 个全部已登记**、登记的 12 个 `./internal/*` **全部存在**（**这是代理，不是原脚本** —— 本机无 `bash`）。

## 六、诚实边界

- **未起真进程、未连 Redis / PG / Docker**：§3.3 那条读路径是**代码路径静态坐实**（逐跳 `文件:行号`），**没有**真的打一次请求验证「DB open ⇒ 凭据收不到流量」。
- **未追到底的两条**（明确记为待查，不写成结论）：① `handleEmergencyRepair` / `handleForceRecover` 不重置 `node_probe_state` 而 `v_routable` 合取里有 `nps` 一项；② 批量入口修复时 `resetInMemoryNodeState` 的 N×M 规模与 15s 超时的真实冲突程度。
- **子代理的结论我复核了关键链，但没逐条复核全部 49 个写入点**：`circuit_state` 的 6 个写入方、那条读路径、`routeguard` 的错误理由，都是我自己 `git grep` + 读文件核实的；其余（`routing_health_checks` 的 4 个写入点、`credentialhealth`/`balance_floor_guard` 等）**采信未独立复核**。
- **本机无 `bash`** ⇒ `scripts/checks/guards-sync.sh` 至今**未执行过**；本轮用 PowerShell 复刻其正反两向检查作**代理证据**。
- **`go test ./...` 全量未跑**；`core.hooksPath` 未设 ⇒ 推送**未经 pre-push 门**；**CI 从未运行**，本轮所有「CI 会红/会绿」都是读 CI 配置 + 读脚本得出的。
- **工具坑登记一条**：子代理报告 `Get-Content` 在本仓部分含非 ASCII 的文件上**行号与 ripgrep 相差最多 ~100 行**，它因此废弃了自己一批推导结果。**我的行号一律以 `git grep -n` / read 工具为准**（本轮所有引用行号都出自这两个）。
- **登记表里每条「未处理」的理由都是本轮判断，不是产品裁决**；其中 `batchRecoverCredentials` / `handleNodeProbeStateReset` 是否也对外承诺「已恢复」语义，**本轮未核实**，只登记事实。

## 七、待裁决清单更新

- **85（新增，P1 by contract）**：`POST /api/admin/diagnostics/routing-blocked/fix` 未清进程内熔断器 / fpslot / key 缓存 / key rotator / URSM v2 却返回 200 +「已修复」。三个选项见 §3.4，**推荐 A**（分批 + 汇总 outcome），但需先定「批量修复是否覆盖配额」这一语义。
- **85 附带一条待查**：`handleEmergencyRepair` / `handleForceRecover` 不重置 `node_probe_state`，而 `v_routable` 合取含 `nps.last_direct_ok` / `nps.next_retry_at` 一项。
- 84（218 号）、83 / 82 / 81 / 80 / 79（更早）**均未动**，本轮无新证据。

## 八、playbook §161–§164

**§161 「统一模块」可以是一个 helper，而不是必经之路 —— 所以要问的不是「有没有统一模块」，而是「每一个能改状态的入口是否都经过它」。**
证据（219 号）：`admin/routing.go:1974 resetInMemoryNodeState` 一次清进程内熔断器 + fpslot Redis + credentialstate 三层，是货真价实的统一模块；但十个 admin 恢复入口里**有一个完全不调它**（`diagnostics_routing.go:254`），而它**照样返回「已恢复」**。
⇒ 回答「状态更新是否统一在某个模块」这类架构要求，**唯一有效的做法是枚举入口 + 逐个核对**，而不是找到一个统一模块就宣布达标 —— 找到模块只证明了模块存在，没证明它是**必经之路**。
⇒ 配套：**逐层表态**。把「状态」拆成可枚举的层（DB 表 / 缓存 / 进程内 / Redis / 队列），逐层记「清了 / 没清 + 理由」，缺口才会自己显形。

**§162 守卫注释里写下的「事实」会过期，而过期的守卫理由比过期的旁注更贵。**
证据（219 号）：`internal/routeguard/routeguard.go` 写着「`state_sync.go` 只写不读，全仓无恢复路径」——被证伪（`provider/client.go:1639→:1906→:316→IsAvailable→executor_dispatch.go:120` 是活路径）。守卫的**不变式**没问题，**理由**错了。
⇒ 理由错 ⇒ 下一个人会推断「这列惰性」⇒ 可能把守卫当多余的删掉 ⇒ **错误的注释会主动误导，而沉默不会**。
⇒ 判据：**守卫/不变式旁边的事实断言，必须能被独立重放**；只扫 SQL 的守卫无法为 Go 侧的读路径背书（本仓就有一个活守卫因扫描面不含 Go 而看漏了这条链）—— 覆盖面不含某类文件时，要在注释里**明写这个边界**，别让读者以为它管全部。

**§163 发现口径要收敛到「有实质差异的对象」—— 从 41 收到 10 不是放松判据。**
证据（219 号）：判据从「命中任一层符号」扫出 41 个，其中 37 个只能盖上「非恢复入口」这种橡皮图章；一张 41 行、37 行无信息量的登记表**一旦掺假就没人信它**，而没人信的登记表连它唯一的作用（让缺口显形）都失去。
⇒ 判据宁可**窄而准**：名字/结构上可枚举 ∧ 确实碰了目标对象。收敛的标准是「**每一条都能写出一句有实质内容的理由**」。

**§164 「没表态」既不是 `false` 也不是 `true` —— 判据要显式要求表态。**
证据（219 号）：本门要求 9 层逐层写 `true`/`false`，第一版登记表用「缺席即 false」写，**当场被门抓到 5 个条目漏表态**。
⇒ 「缺席 = false」在**有**独立的「必须写理由」约束时是安全的；但只要理由可以缺席，缺席就会被读成「已覆盖」——与 §154（缺失断言比存在断言更容易错）同源。
⇒ 落地：把状态存成**显式取值**（`map[string]bool` 且要求全部键在场），而不是靠缺席表达。

---

## 附录：本轮采信的子代理枚举（**未逐条独立复核**）

子代理给出 `credentials` 行健康列 **49 个写入点**、`routing_health_checks` **4 个**、节点/模型探测表 **10 个**、内存节点状态 **3 类**；以及读面 21 条。
其中我**独立复核**并采信的是：`circuit_state` 的 6 个写入方、§3.3 那条读路径（5 跳）、`routeguard` 的错误理由、`domains/nodestatecache` 包外 0 生产导入（与 86 号一致，**今天仍成立**）。
**未独立复核**的：其余 40+ 个写入点与读面清单 —— 本轮**不据此下任何结论**，登记在此仅供下一轮取用。
