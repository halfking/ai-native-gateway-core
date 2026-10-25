# R158 · 上游封死的因果链（四层）与「force-recover 假恢复」真缺陷

> **日期**：2026-10-27（轮次标签）　**实测时钟**：2026-10-24/25
> **会话**：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`
> **基线提交**：`4169c492e`（`origin/main`）
> **本轮交付提交**：`ed1fe331b`（工具 + 门）
> ★ **对生产的写入 = 0**（★ 252 / 211 全程**只读**；★ 本地复现走**单事务 + ROLLBACK**，★ 零残留）
> ★ 本轮**只加只读工具与门**，★ **不改任何生产行为**（★ 改行为属生产变更 ⇒ ★ **待属主裁决**）

---

## §0 摘要

本轮起因是原始任务里的「**进行模型的请求与写入测试，积累足够的数据**」这一条 ——
本地网关对 `/v1/chat/completions` 一律回 **503**。

本轮做了三件事：

1. ★★★★★ **把 503 的因果链逐层挖到闭合**，★ 结论是：★ **链断在【上游】，不在本仓**。
   ★ 四层各自有实测证据（§2）。
2. ★★★★ **顺带挖出一个真缺陷**（★ §3）：
   `handleForceRecoverSingle`（★ `admin/diagnostics_credential.go:247-312`）**只重置 `availability` 一族**，
   ★★ **不重置 `quota_state`** ⇒ ★★★ **对配额耗尽的凭据，force-recover 返回 200、文案说「凭据已强制恢复」，
   ★★★ 但它【仍然不可路由】⇒ 「恢复成功」是假的。**
3. ★★★★ **交付一个只读一致性探测器 + 行为门**（★ §5），★ 并在**两个生产实例**上量化了该缺陷的规模（★ §4）。

### 本轮读数一览

| 项 | 读数 |
|---|---|
| 本地 503 复现 | `grok-4.6` ⇒ HTTP 503 / `code=no_candidate` / **0.065 s**（★ 本地即返回，未打上游） |
| 本地 503 复现 | `glm-5.3-flash` ⇒ HTTP 503 / `model_not_found` / **18.55 s**（★ **打到上游**后失败） |
| 本地 503 复现 | `minimax-m2.7` ⇒ HTTP 503 / `model_not_found` / **10.80 s**（★ **打到上游**后失败） |
| 上游错误原文（★ 库内逐字） | `[quota_permanent] upstream 429: {"error":{"message":"Organization balance insufficient","type":"rate_limit_error","code":"budget_exceeded"}}` |
| 写入路径 | ★ **503 失败请求照样落库** ⇒ `session_turns` / `session_turns_hot` 都有 `request_status=failure` 的 `user: ping` 行 |
| 252 量化（★ 只读） | 不可路由绑定 **1461** / 凭据 **42** ⇒ **A 类 462 绑定 / 5 凭据**、B 0、C 377、D 622 |
| 211 量化（★ 只读） | 不可路由绑定 **1462** / 凭据 **39** ⇒ **A 类 483 绑定 / 5 凭据**、B 0、C 343、D 636 |
| 本地探测器（★ 真库） | `probe-reverted-only` / exit 2 / 1 绑定 / 1 凭据（★ cred 2 / binding 106 / `binding_unavailable`） |
| 行为门 | **9 条全绿** |
| 变异验证 | **6 臂全红**（★ 每臂还原后 md5 逐字一致） |
| 回归 | `go build ./...` ✔ · `make guards-shell` ✔ · `make guards-ursmcheck` ✔（★ 见 §7） |

---

## §1 起因与「503」是什么

原始任务要求「使用指定的 key 与地址来操作，需要使用 ping 和指定的提示词进行多轮的测试，积累足够的数据」。
本地网关（`http://127.0.0.1:8782/v1`）在 R157 轮部署成功后对任何模型请求都回 **503**。

★ 注意 R157 轮已经证明过的量具纪律：**「端点 200」只证明进程活着，不证明连得上库**；
本轮 503 **不是**那种事故 ——
★ **库是通的**（★ 写入路径正常落库、★ 诊断接口正常读库），★ **503 是网关自己主动返回的业务错误**。

★ 证据（★ 本轮当场复现，★ `session_turns` / `session_turns_hot` 里能看到这几次请求）：

```
id    agent_name  turn_no  origin_stage  request_preview  request_status
1771  go-client   1       business      user: ping       failure
1770  go-client   1       business      user: ping       failure
1769  go-client   1       business      user: ping       failure
…
972   curl        —        business      user: ping       failure   ← 本轮的 curl 请求
971   curl        —        business      user: ping       failure
970   curl        —        business      user: ping       failure
```

⇒ ★★ **写入路径与失败解耦**：★ 503 的请求**照样落库**、★ `request_status=failure`、
★★ `origin_stage=business`（★ **不是探测** —— 与 154 生产取证的形态**逐字一致**）。

---

## §2 ★★★★★ 因果链四层（★ 每一层各自实测）

★ 方法：**不猜**。★ 每层都问「如果这一层是根因，观测应该长什么样」，★ 然后去取那个观测。

### §2.1 第 1 层：上游（★ 链的终点）

★ **三路本地模型路径逐条被上游封死**：

| 凭据 | provider | 上游报错（★ 逐字） | 性质 |
|---|---|---|---|
| cred 3 | 2454 | `429` / `{"error":{"message":"Organization balance insufficient","type":"rate_limit_error","code":"budget_exceeded"}}` | ★★ **余额耗尽**（★ 不可自行恢复） |
| cred 1 | 2453 | `rpm exhausted` / `quota_exceeded_error` | ★ 限流 |
| cred 2 | 2452 | `inference exceeds tpm/rpm limit` / `rate_limit_error` / `code 429003` | ★ 限流 |

★★ **cred 3 那条是库里的逐字原文**（★ `credentials.state_reason_detail`，★ 本轮当场读出）：

```
[quota_permanent] upstream 429: {"error":{"message":"Organization balance insufficient","type":"rate_limit_error","code":"budget_exceeded"}}: <nil>
```

★★ **重要边界**：★ 「Organization balance insufficient」是**账户余额**问题
⇒ ★★ **本地任何代码改动都不可能把它变成可路由** ⇒ ★★ **「积累足够数据」这个目标本轮【未达成】，
★★ 原因是【上游配额】，★ 不是本轮代码**。

### §2.2 第 2 层：网关的归类（★ 正确，不是误判）

★ 网关把上游 429 **正确归类**成配额类：

```
id  provider_id  availability_state  quota_state                state_reason_code
3   2454         suspended           permanently_exhausted      quota_permanent
```

⇒ ★★ **这一层没有问题**：★ 网关**如实**记录了上游的拒绝。

### §2.3 第 3 层：候选查询（★ 正确排除）

★ `v_routable_credential_models` 把 `quota_*` 档排在 availability 各档**之后**
⇒ ★★ **quota 判定盖过 availability**（★ ★ 这条排序正是 §3 那个缺陷的**根因所在**）。

★ 观测（★ `grok-4.6` ⇒ 走 cred 3 / provider 2454）：

```
HTTP=503  time=0.064654s
{"error":{"code":"no_candidate", …}}
```

⇒ ★★ **0.065 s** ⇒ ★★ **本地即刻返回、根本没打上游** ⇒ ★★ 候选查询**正确地**把它排除了。
⇒ ★★ 这一层**也没有问题**。

### §2.4 第 4 层：决定性证据（★ 确实打到了上游）

★ 只看 §2.3 会误以为「网关从没试过上游」。★ 实测另外两个模型：

```
model=glm-5.3-flash  HTTP=503  time=18.550975s   code=model_not_found
model=minimax-m2.7   HTTP=503  time=10.799369s   code=model_not_found
```

⇒ ★★★★★ **18.55 s 与 10.80 s 是【上游往返】的量级**（★ 本地候选查询只要 0.065 s）
⇒ ★★★ **请求确实打到了上游、★ 上游确实拒绝了它** ⇒ ★★★ **链闭合在【上游】**。

★★ 另有一条来自 R158-A 的**决定性观测**：★ 对 cred 3 做 force-recover（★ 使其恢复可路由）后，
★★ 请求**确实打到了上游**（★ **10.66 s**）⇒ ★★ 上游**仍然失败** ⇒ ★★ 与本层互相印证。

### §2.5 小结（★ 四层逐层排除法）

```
请求 503
  ├─ 第 1 层 上游       ⇒ ★ 上游 429（限流 ×2 + 余额 ×1）     ★★★ 链的终点
  ├─ 第 2 层 网关归类   ⇒ ★ 正确（quota_permanent）            ✔ 无问题
  ├─ 第 3 层 候选查询   ⇒ ★ 正确排除（0.065 s 本地返回）        ✔ 无问题
  └─ 第 4 层 实际请求   ⇒ ★ 10.80~18.55 s ⇒ ★ 打到上游后失败   ★★★ 上游拒绝
```

⇒ ★★★★ **四层里只有第 1 层是根因，★ 第 2/3 层都是【正确行为】**
⇒ ★★★ **把 503 当成本仓缺陷去修，★ 是把【上游配额】伪装成【本地 bug】。**

---

## §3 ★★★★ 真缺陷：`force-recover` 不重置 `quota_state` ⇒「恢复成功」是假的

### §3.1 机制

★ 本仓的凭据有**两个各自独立**的判定来源：

1. `credentials.availability_state`（★ 「凭据能不能用」⇒ ★ UI / 诊断面板读它）
2. `v_routable_credential_models.is_routable`（★ 「这条 (凭据,模型) 能不能进路由候选池」）

★ 而 `handleForceRecoverSingle`（★ `admin/diagnostics_credential.go:247-312`）的写入面是：

| # | 对象 | 写什么 | ★ **碰 `quota_state` 吗** |
|---|---|---|---|
| 1 | `credentials` | `availability_state='ready'` / `availability_recover_at=NULL` / `circuit_state='closed'` / `consecutive_failures=0` / `state_reason_code=NULL` / `state_reason_detail='admin force-recover via diagnostic'` / `state_updated_at=now()` | ★★ **不碰** |
| 2 | `credential_model_bindings` | `available=true` / `unavailable_reason=NULL` / `unavailable_at=NULL` / `unavailable_recover_at=NULL` | ★ 不碰 |
| 3 | `model_probe_state` | `state='healthy_confirmed'` / `consecutive_successes=5` / `next_retry_at=now()+4h`（★ 仅 `recovering`/`unknown`/`suspicious`） | ★ 不碰 |
| 3.5 | `node_probe_state` | `last_direct_ok=TRUE` / `last_gateway_ok=TRUE` / `last_err_*=NULL` / `next_retry_at=now()` / `paused=FALSE`（★ 仅 `last_direct_ok=FALSE` 的行） | ★ 不碰 |
| 4 | — | `invalidateRoutingCaches(...)` + `provider.InvalidateCredentialKeyCache(credID)` | ★ 不碰 |

⇒ ★★★★★ **`quota_state` 不在这张表的任何一行里** ⇒ ★★ **配额耗尽的凭据被 force-recover 后，
★★ `availability_state` 变成 `ready`，★ 但 `quota_state` 仍是 `permanently_exhausted`**
⇒ ★★★ 而路由视图的 CASE 里 `quota_*` 档**排在 availability 各档之后** ⇒ ★★★ **quota 判定盖过 availability**
⇒ ★★★★ **⇒ 后果：★ 接口返回 200、★ 文案说「凭据已强制恢复，credential / binding / probe_state 已重置」，
★★★★ ⇒★ 但它【仍然不可路由】⇒ ★★★★ 「恢复成功」是假的。**

### §3.2 确定性复现（★ 本地单事务 + ROLLBACK，★ 零残留）

```sql
BEGIN;
UPDATE credentials SET availability_state='ready', availability_recover_at=NULL, circuit_state='closed',
  consecutive_failures=0, state_reason_code=NULL,
  state_reason_detail='admin force-recover via diagnostic', state_updated_at=now() WHERE id=3;
select id, availability_state, quota_state from credentials where id=3;
--   ⇒ 3 | ready | permanently_exhausted      ← ★ UI 说「已恢复」，★ quota 没被重置
select v.binding_id, v.raw_model_name, v.is_routable, v.unavailable_reason
  from credentials c join v_routable_credential_models v on v.credential_id=c.id
 where c.id=3 and v.raw_model_name like 'grok-4.6%';
--   ⇒ f | quota_permanently_exhausted        ← ★ 路由仍然不选它
ROLLBACK;
```

⇒ ★★★★★ **确定性复现**（★ 不是概率、不是竞态、★ 是**逐字照抄 handler 的写入面**就能重现）。

### §3.3 附带事实：force-recover **会**清 `node_probe_state`，★ 但**恢复不持久**

★ 第 3.5 步确实清 `node_probe_state` ⇒ ★ 「探针失败类」的凭据 force-recover **当场可路由**。
★★ **但探针 worker 会再标回来** ⇒ ★★ **恢复不持久**（★ 这就是 §5 里 C 类要单独分类的原因）。

★ 路由视图用 `node_probe_state` 的退避窗口判 `node_probe_failed`：
★ `last_direct_ok = false AND next_retry_at > now()` ⇒ ★ 匹配不上 ⇒ ★ `NULL` ⇒ ★ **整行被滤掉**。

---

## §4 ★★★★ 生产量化（★ 只读，★ 两实例）

★ 用本轮新交付的探测器（★ §5）跑：

| 实例 | 不可路由绑定 | 涉及凭据 | **A 凭据级自相矛盾** | B 两答案 | C 探针回标 | D 不是矛盾 |
|---|---|---|---|---|---|---|
| **252** | **1461** | **42** | ★★ **462** | 0 | 377 | 622 |
| **211** | **1462** | **39** | ★★ **483** | 0 | 343 | 636 |

### §4.1 A 类的规模（★ 真正的缺陷面）

| 实例 | A 类绑定 | **A 类涉及凭据** | `quota_state` 分布 |
|---|---|---|---|
| 252 | 462 | ★★ **5** | `permanently_exhausted` **446** + `periodic_exhausted` **16** |
| 211 | 483 | ★★ **5** | `permanently_exhausted` **483** |

★★ **放大效应**：★ **5 个凭据 ⇒ 462 / 483 条绑定被卡**（★ 一凭据多模型绑定 ⇒ 几十倍放大）
⇒ ★★ **UI 上只有 5 个凭据看起来「已恢复但还是不通」，★ 实际影响近 500 条绑定路由**。

### §4.2 ★★ 判据口径的两次修正（★ 重要）

★ **第一版判据太宽**：★ 「`availability='ready'` 但 `is_routable=false`」把**人工禁用 / 生命周期停用 /
provider 禁用**也算成矛盾（★ 252 上 335 + 112 + 75 + 52 + 38 + 10 条）⇒ ★★ **那是【粒度不同】**：
★★ 那些是**绑定级/模型级**的人工判定，★ 与凭据级 `availability_state` **本就不同粒度**
⇒ ★★ **本就该被路由排除** ⇒ ★★ **不是矛盾，是正确行为**。

★ ⇒ ★★★ **判据重写为三个各自语义明确的断言**（★ 单一真值源在 SQL 的 `finding_class`）：

| 类 | 含义 | 处置 |
|---|---|---|
| **A** | ★ 凭据级自相矛盾：`availability='ready'` **但** `quota_state` 落在永久耗尽族 | ★★ **真缺陷**（★「恢复成功」是假的） |
| **B** | ★ 同一信号两个答案：绑定级理由落 `availability_%` 家族，★ 但凭据说 ready | ★★ 凭据级与路由级互相矛盾 |
| **C** | ★ 探针回标：`node_probe_failed` / `binding_unavailable` | ★★ 恢复**不持久**（★ 探针 worker 会再标回） |
| **D** | ★ 其余（★ 人工禁用 / 生命周期停用 / provider 禁用） | ★★ **不是矛盾**（★ 粒度不同） |

★★ **实测两实例的 B 类都是 0** ⇒ ★★ **说明「同一信号两个答案」这条路径在本仓当前形态下没有活跃样本**
（★ 但判据保留，★ 因为它是**唯一能抓到「availability 与绑定级理由互相打架」**的那条）。

---

## §5 ★★★★ 新交付：只读一致性探测器 + 行为门

### §5.1 `scripts/audit/check-credential-routability-consistency.sh`（★ 只读）

| 属性 | 说明 |
|---|---|
| 模式 | ★ `probe [DSN]`（★ 真查库）/ ★ `classify < 清单.tsv`（★ 喂清单，★ **判定逻辑与查库分开** ⇒ ★ **门不需要真库**） |
| 只读 | ★ 一条 `SELECT`，★ 无任何写库语句（★ 门会剥注释后逐字检查） |
| 判定 | ★ 单一真值源在 SQL 的 `finding_class`；★ 分类计数也按**第 10 列**算 ⇒ ★★ **与 SQL 同源** |
| 退出码 | ★ **0 = consistent**、★★ **2 = inconsistent / probe-reverted-only**、★ 3 = 探测失败、★ 64 = 用法错 |
| 不回显 | ★ DSN / 口令**绝不打印**；★ 探测失败只报**目标**（★ `host:port/db`）；★ stderr 经 `scrub` 抹除口令 |
| 输出 | ★ stderr 一段人读摘要 + ★ stdout 一段 TSV（★ `#meta` 键值 + ★ 逐行明细） |

### §5.2 `scripts/ursmcheck/credential_routability_consistency_gate_test.go`（★ 9 条）

★ **行为层为主**（★ §5.1 的 `classify` 模式让门**秒级、无需真库**）：

| # | 门 | 性质 |
|---|---|---|
| 1 | 脚本在位 + `bash -n` 过 | 形状 |
| 2 | ★★ **`PROBE_SQL` 区域禁反引号**（★ bash 双引号串里反引号 = 命令替换 ⇒ ★ SQL 被静默改写） | 形状 |
| 3 | ★ 剥注释后**不得出现写库关键字**（★ `INSERT/UPDATE/DELETE/DROP/TRUNCATE/ALTER/CREATE/GRANT`） | 形状 |
| 4 | 双模式 + 退出码契约在用法里写明 | 形状 |
| 5 | ★ **A 类 ⇒ `inconsistent` + 退出码 2 + 分类计数 2 + 绑定总数 2 + 凭据数 2** | 行为 |
| 6 | ★ **C 类 ⇒ `probe-reverted-only` + 退出码 2 + 分类计数 1** | 行为 |
| 7 | ★ **空清单 ⇒ `consistent` + 退出码 0**（★ **反向对照** ⇒ ★ 「永远红」不算通过） | 行为 |
| 8 | ★★ **阳性对照**：★ 同一批数据换类别 ⇒ ★ **判定词必须跟着变**（★ 且分类计数也必须变） | 行为 |
| 9 | ★ **哨兵口令不出现在 stdout+stderr** | 行为 |

### §5.3 ★★ 变异验证（★ 6 臂全红，★ 每臂 md5 逐字还原）

★ 手法：★ 真改仓内文件 ⇒ ★ 跑门 ⇒ ★ 期望红 ⇒ ★ **仓外备份副本**还原 ⇒ ★ **md5 逐字比对**。

| 臂 | 注入的缺陷 | 门红在哪 |
|---|---|---|
| **M1** | A 类计数判据失效（`/^A-/` ⇒ `/^ZZA-/`） | `ClassifiesAClass` + `VerdictTracksInput` |
| **M2** | 退出码契约失效（★ `inconsistent` 却退 0） | `ClassifiesAClass` + `VerdictTracksInput` |
| **M3** | ★ SQL 区域混入反引号 | `SQLHasNoBacktick` |
| **M4** | ★ 代码区混入 `UPDATE credentials …` | `ScriptHasNoWriteStatements` |
| **M5** | ★ 判定恒为 `inconsistent`（★ 反向对照失效） | `EmptyIsConsistent` |
| **M6** | ★ `classify` 不读 stdin（★ 喂什么都是空清单） | `ClassifiesAClass` + `ClassifiesCClass` + `VerdictTracksInput` |

⇒ ★★★ **6/6 全红** ⇒ ★★ **这道门有牙**；★ 每臂还原后 **md5 逐字一致**（★ `7edd737bec4cc80def88d11d6f4af159`）。

---

## §6 ★★ 本轮自身踩到的 5 个缺陷（★ 量具失效，★ 全部已修）

★★ **本轮最值得记的不是产品缺陷，★ 而是「我写的第一个检查器自己就错了 5 次」** ——
★★ **每一次都是【读数错误】而不是【行为错误】**，★★ 如果没让门真跑一遍，★ 这些都会被当成「事实」。

| # | 缺陷 | 现象 | 修法 |
|---|---|---|---|
| 1 | ★ `classify_file "-"` 把 `-` 当**文件名** | ★ awk 读不到 stdin ⇒ 读到 0 行 | ★ 改走**快照文件** |
| 2 | ★★ **`/dev/stdin` 只能读一次** | ★ 第一个 awk 吃光 stdin ⇒ ★★ 后面的计数全 0 ⇒ ★★ **「绑定总数 2（真）」与「涉及凭据数 0（假）」自相矛盾** | ★★ **先落盘快照，再对文件反复读**（★ 所有计数必须来自**同一份快照**） |
| 3 | ★★ **`awk -F'\t'` 在 BWK awk 不生效** | ★ 字段按整行取 ⇒ 分类计数恒 0 | ★ 改 `BEGIN{FS="\t"}`（★ v1 检查器已用 `FS = "\t"`，★ 安全） |
| 4 | ★★ **顶层 `case` 分支里用 `local`** | ★ bash 报 `local: can only be used in a function` ⇒ ★★ **退出码 141**（★ SIGPIPE）⇒ ★★ **掩盖真因** | ★ 去掉 `local` |
| 5 | ★ SQL 注释区一处**反引号** + 一处 **U+FFFD 乱码** | ★ bash 双引号串里的反引号 = **命令替换** ⇒ ★★ **SQL 被静默改写**，★ 而 psql **照样成功** | ★ 去掉反引号；★ **门钉死「SQL 区域禁反引号」** |

★★★ **通用式沉淀见 `LESSONS.md` §413**（★ 本轮共 7 条）。

---

## §7 ★★ 全量回归读数

| 项 | 结果 |
|---|---|
| `go build ./...` | ✔ exit 0 |
| `make guards-shell` | ✔ 「✓ 无『$变量紧跟非 ASCII 字节』隐患（探针与反向对照均已通过）」 |
| `go test ./scripts/ursmcheck/ -run 'TestCredentialRoutabilityConsistency'` | ✔ **9/9 绿**（3.483 s） |
| `make guards-ursmcheck`（★ 权威慢门，★ `-timeout=900s`，★ 含 `./admin`） | ✔ 见下 |
| 变异验证 | ✔ **6/6 全红 + md5 逐字还原** |

★ 慢门读数（★ 三轮，★ 供下一轮比对）：

| 轮 | ursmcheck | admin | exit |
|---|---|---|---|
| R157-1 | 674.441 s | 168.473 s | 0 |
| R157-2 | 549.989 s | 94.260 s | 0 |
| R157-3 | 532.430 s | 87.160 s | 0 |
| **R158** | ★★ **498.053 s** | ★★ **81.634 s** | ★★ **0** |

⇒ ★★ R158 慢门**比 R157 三轮的最小值还快**（★ 498.053 s vs 532.430 s）
⇒ ★★ **本轮新增的门（9 条、★ 纯进程内合成清单）没有拖慢慢门**（★ 每条约 0.4 s，★ 合计约 3.5 s）。

★ ★★ **顺带一条量具纪律**：★ 本轮 slow gate 的**读数是墙钟**（★ `real 8m19.762s`），
★ 而 §143.19/§143.20 已登记过「**机器负载会污染耗时读数**」⇒ ★★ **这里只做「与同批基准比」的相对判断，
★★ 不把 498 s 当成绝对性能指标**（★ 且本轮机器 load1 曾实测到 31.22）。

---

## §8 ★★★ 属主裁决项（★ 本轮**全部留空未做**）

| 编号 | 事项 | ★ 现状与理由 |
|---|---|---|
| ★★ **R158-1** | ★ **`handleForceRecoverSingle` 不重置 `quota_state`** ⇒ ★ 「恢复成功」是假的 | ★★ 修它会**改生产凭据的恢复行为**（★ 252 有 **462** 条绑定 / 211 有 **483** 条绑定受影响）⇒ ★★ **必须属主授权**。★ 备选修法：★ ① 恢复时同时清 `quota_state`（★ **危险**：★ 配额确实耗尽的凭据会被假恢复）；★ ② **不清 quota**，★ 但**让接口文案与响应体如实反映**「已重置 availability，★ 配额状态未变，★ 仍不可路由」⇒ ★★★ **推荐 ②**（★ 零语义风险、★ 只改文案与响应形状）；★ ③ 让路由视图的 CASE 把 availability 提到 quota **之前**（★ **危险**：★ 会让所有配额耗尽的凭据被假恢复） |
| ★★ **R158-2** | ★ 探测器的**退出码 2** 在 CI 里怎么用 | ★ 本检查器是**诊断器**不是**门**（★ 判据依赖生产数据 ⇒ ★ 把它当 CI 门 ⇒ ★ 任何一次上游配额波动都会让 CI 红）⇒ ★ 建议**只在巡检/看板侧消费** |
| R157 遗留 | ★ D17-A 生产 252/211 应用迁移 909 · D17-B 超时可见 · D17-C 搜索范围收窄 · D17-D 先加观测 · D11 211 台账补录（14 条）· D18 剩余 107 条 · D13/D1/D2/D3/D10 | ★ 全部留空（★ 同前） |
| R157 遗留 | ★ 部署引擎口令「发现源」污染（★ `scripts/deploy-lib` 仓外共享 SSOT）· ★ `ursm_node_snapshot_min_*` 保留期（252 8 天 ≈ 8.1 GB）· ★ `session_bodies` TOAST 95.6% · ★ `usage_ledger_*` 索引占比 68% · ★ matview REFRESH 缩短重算窗口 | ★ 全部留空（★ 同前） |

---

## §9 ★★ 诚实边界（本轮**没有**证明的事）

1. ★★★ **本轮**没有**修改 force-recover 的写入面** ⇒ ★★ **生产行为未变** ⇒ ★★ 缺陷**仍然存在**。
   ★ 本轮只交付了「**能把它量出来的工具**」与「**能防止工具自身退化的门**」。
2. ★★★ **本轮没有在真库上证明「probe 模式在每个实例上都能正确判定」** ——
   ★ 门只用**合成清单**测 `classify` 模式（★ 门不连任何库）；★
   ★ 「probe 模式在真库上正确」这件事，★ 由**§4 的双实例实测**承担（★ 不是由门承担）。
3. ★★ **`probe` 模式在真库上跑出的 A/B/C/D 分类**依赖 `v_routable_credential_models` 的**当前定义** ——
   ★★ **若将来那个视图的 CASE 顺序改了，★ 探测器的 A 类判定语义也会跟着变** ⇒ ★★ **探测器的口径必须随视图一起复核**。
4. ★★ **上游侧的三路封死是「本轮时点」的快照** ⇒ ★★ 配额会恢复 ⇒ ★★ **「503 ⇒ 链在上游」这个结论对本轮时点成立**，
   ★★ **但不等于「以后 503 都是上游问题」** ⇒ ★★ **每轮都要重新取第 4 层那个「秒级 vs 十秒级」的观测**。
5. ★★ **89 条有副作用的 POST 端点仍未验证**（★ 同前轮遗留）。
6. ★★★ **本轮没有重新部署本地网关** ⇒ ★★ **本地跑的二进制仍是 `git_sha=3e214a70`**（★ 与已推送提交**运行时零差异**）
   ⇒ ★★ **本轮的新工具是纯脚本 + 纯 Go 门，★ 不影响网关运行时** ⇒ ★★ 无需重新部署。

---

## §10 复现命令（★ 全部可重跑）

```bash
# ① ★★ 本地真库 probe（★ 只读；★ 口令不进会话）
cd /tmp/kx-wt-r98
PW="$(grep '^LLM_GATEWAY_DATABASE_URL=' /Users/niuer/kaixuan/llm-gateway-go/run/llm-gateway-local-8782.env \
      | sed -E 's#.*//[^:]+:([^@]+)@.*#\1#')"          # ★ 只取口令，★ 不回显
DSN="postgresql://llm_gateway:${PW}@127.0.0.1:54318/llm_gateway?sslmode=disable"
PATH="/Users/niuer/kaixuan/pg18/bin:$PATH" \
  bash scripts/audit/check-credential-routability-consistency.sh probe "$DSN"
# ⇒ probe-reverted-only / exit 2 / 1 绑定 / 1 凭据（A=0 B=0 C=1 D=0）

# ② ★★ 合成清单 classify（★ 门就是这个模式，★ 无需真库）
printf '3\t2454\tready\tpermanently_exhausted\tok\t100\tgrok-4.6\tf\tquota_permanently_exhausted\tA-credential-quota-contradiction\n' \
  | bash scripts/audit/check-credential-routability-consistency.sh classify
# ⇒ inconsistent / exit 2

# ③ ★★ 生产 252（★ 只读）
scp -q scripts/audit/check-credential-routability-consistency.sh 252:/tmp/kx-crc-check.sh
ssh -o ConnectTimeout=20 252 'set -a; . /opt/llm-gateway-go/.env.dev; set +a; export LLM_GATEWAY_DATABASE_URL; \
  bash /tmp/kx-crc-check.sh probe "$LLM_GATEWAY_DATABASE_URL"' | grep '^#meta'

# ④ ★★ 生产 211（★ 只读；★ 211 无 psql ⇒ 在 acc-pg17 容器内跑）
scp -q scripts/audit/check-credential-routability-consistency.sh 211:/tmp/kx-crc-check.sh
ssh -o ConnectTimeout=25 root@192.168.31.211 'DSN=$(docker inspect llm-gateway-211-b --format "{{range .Config.Env}}{{println .}}{{end}}" \
  | grep "^LLM_GATEWAY_DATABASE_URL=" | cut -d= -f2- | sed -E "s#@acc-pg17:5432/#@localhost:5432/#"); \
  docker cp /tmp/kx-crc-check.sh acc-pg17:/tmp/ >/dev/null; \
  docker exec acc-pg17 bash /tmp/kx-crc-check.sh probe "$DSN"'

# ⑤ ★★ 门 + 变异
go test ./scripts/ursmcheck/ -run 'TestCredentialRoutabilityConsistency' -count=1 -v
bash /tmp/kx-mut-r158b/run-mutations.sh    # ★ 6 臂全红 + md5 逐字还原
```
