# 220 号（R89-EK）：清完 DB 也可能仍然不可路由——`health_status='warning'` 被 `is_routable` 拦住时，`unavailable_reason` 返回 NULL

> 结论先行：
> 1. **🟠 P2（待裁决 86）**：`v_routable_credential_models` 的 `is_routable` 十二项 AND 合取里，第 11 项是 `COALESCE(c.health_status,'unknown') = ANY(ARRAY['healthy','unknown'])`；而 `unavailable_reason` 的 CASE **只覆盖 `unreachable`（且带 1 小时时间窗）**。合法取值 **`warning`** 落在排除集里却**没有任何 CASE 分支** ⇒ 凭据被合取拦住、原因却是 **NULL**（CASE 落到 `ELSE NULL`）。
> 2. **`warning` 确实会被写进去**，不是纸面取值：`admin/provider_cred_lifecycle.go:349`，运维点「健康检查」时 chat 探针非 200 即写 `warning`。
> 3. **与 219 号的待裁决 85 是同一条运维链上的两处断点**（不是同一条）：85 说的是「批量修复只清 DB 侧、不清进程/Redis 侧」；本条说的是「**即使 DB 侧全清了，`health_status` 这一项仍没被清**（`handleRoutingBlockedFix` 不重置它）」。修一处不能修另一处。
> 4. 新增门 `internal/routeguard/reason_coverage_test.go`（在**已登记**的包里，无需改 `GUARD_PACKAGES`），把「合取里每一项取值域型闸在 CASE 里都有分支」钉成可执行登记表。**判据迭代了两版**，第一版把 8 个历史迁移判红。

---

## 一、起手：接自己上一轮登记的「待查」

219 号在 §3.3 明确留了一条**没有下结论**的待查项：

> `handleEmergencyRepair` / `handleForceRecover` 都**不重置 `node_probe_state`**，而 `v_routable_credential_models` 的合取里有一项 `NOT EXISTS (... nps.last_direct_ok=false AND nps.next_retry_at>now())` ⇒ **清了 credentials 也可能仍不可路由**。本轮**没有**追到底。

本轮把它追到底的做法是：**不再逐个入口核对，而是先把「什么才叫可路由」定义清楚**——逐字读 `sql/objects/views/v_routable_credential_models.sql`，把 `is_routable` 的合取项全部列出来，再拿它去对照每个恢复入口。

## 二、`is_routable` 的十二项合取（`:17-19` 逐字读过）

| # | 合取项 | 归类 |
|---|---|---|
| 1 | `p.enabled` | 布尔 |
| 2 | `COALESCE(p.manual_disabled,false) = false` | 布尔 |
| 3 | `c.status = 'active'` | **取值域** |
| 4 | `c.lifecycle_status = 'active'` | **取值域** |
| 5 | `COALESCE(c.manual_disabled,false) = false` | 布尔 |
| 6 | `c.availability_state = 'ready'` | **取值域** |
| 7 | `c.quota_state <> ALL(ARRAY['permanently_exhausted','balance_exhausted','periodic_exhausted'])` | **取值域（排除式）** |
| 8 | `pm.available = true` | 布尔 |
| 9 | `cmb.available = true` | 布尔 |
| 10 | `cmb.unavailable_reason IS DISTINCT FROM 'manual'` | 布尔 |
| 11 | `COALESCE(c.health_status,'unknown') = ANY(ARRAY['healthy','unknown'])` | **取值域** |
| 12 | `NOT EXISTS(SELECT 1 FROM node_probe_state nps WHERE … nps.last_direct_ok=false AND nps.next_retry_at>now())` | 子查询 |

（`internal/routeguard/routeguard.go:7-10` 的注释里早写过「单一 AND 合取……合取项越加越容易一项出问题、全凭据连坐」，与这里的 12 项吻合。）

## 三、🟠 P2：`health_status` 这一项，拦了但说不清

### 3.1 缺口本体

```
合取第 11 项：COALESCE(c.health_status,'unknown') = ANY(ARRAY['healthy','unknown'])
CASE（:32）： WHEN ((c.health_status = 'unreachable') AND (c.health_checked_at > now()-'01:00:00'))
                   THEN 'recent_probe_unreachable'
CASE（:39）： ELSE NULL
```

`credentials.health_status` 的 CHECK 约束允许四值（`deploy/sql/schemas/baseline/01-schema.sql:6882`）：
`unknown | healthy | warning | unreachable`。

- 落在合取排除集里的有两个：**`warning`** 与 **`unreachable`**。
- CASE 只处理了 `unreachable`，且**带一小时时间窗**。

⇒ **两重缺口**：
- **(a)** `health_status='warning'` ⇒ 被合取拦住，`unavailable_reason = NULL`。
- **(b)** `health_status='unreachable'` 但 `health_checked_at` **早于一小时** ⇒ 同样被合取拦住、原因同样 **NULL**（合取这一项**没有**时间窗，CASE 有）。

### 3.2 `warning` 确实会被写进去（不是纸面取值）

`admin/provider_cred_lifecycle.go` 的 `doHealthCheck`（`:196`，被 `:187` 与 `:399` 两个 HTTP 入口调用）：

```
:226  decrypt 失败            → healthStatus = "unreachable"
:238  拉模型列表失败           → healthStatus = "unreachable"
:291  没返回任何模型           → healthStatus = "unreachable"
:297  模型列表正常             → healthStatus = "healthy"
:344  chat 探针 200            → healthStatus = "healthy"
:349  chat 探针非 200          → healthStatus = "warning"   ← 缺口 (a) 的生产者
```

⚠️ `apihub.HealthState`（`apihub/types.go:47-50`）的值域是 `healthy|degraded|down|unknown`，**与 `credentials.health_status` 的四值完全不同** ⇒ 同名不同源，跨包 grep 这个字段名极易误并（子代理也独立发现了 `CircuitState` 的同类问题）。本轮所有引用都以 `credentials` 的 CHECK 约束为准。

### 3.3 与待裁决 85 的关系（不要合并叙述）

| | 待裁决 85（219 号） | 本条（220 号） |
|---|---|---|
| 说的是 | 批量修复只清 DB 侧，**进程/Redis 侧五层没清** | 即使 DB 侧全清，**第 11 项 `health_status` 仍没被清** |
| 证据 | `diagnostics_routing.go:254` 缺 `resetInMemoryNodeState` | 该入口的 `UPDATE credentials` 只设 `availability_state`/`circuit_state`/`consecutive_failures`，**不碰 `health_status`** |
| 修一处能修另一处吗 | **不能** | **不能** |

⇒ 同一块 SQL 上一处缺口 + 同一段代码上另一处缺口。**运维点一次「按供应商批量修复」，可能两道都没跨过去。**

### 3.4 为什么本轮**不改视图**

三个理由，缺一不可：

1. **无真库**：`CREATE OR REPLACE VIEW` 的实际行为（列序/类型/是否报错）**本机无法验证**（无 PG，见 §六）。改一个在 dispatch 候选查询里被读的视图而不可验证，风险不对等。
2. **该视图在 18 个文件里被（重）定义**（见 §五），改一处会立刻产生版本分叉，必须同时处理「规范文件 + 新迁移 + 3 份 schema dump」。
3. **迁移 checksum 是已知雷区**：184 号已记录「73 条 >412 的迁移全部会被部署脚本硬拒、checksum 对账在真库上 0 通过」⇒ 新增迁移的写法必须先确认口径，属独立一轮的工作。

⇒ 登记为**待裁决 86**，并把「怎么改才对」写清（不代拍）：
- **最小且不扩大影响面**：在 CASE 里加**无时间窗**的分支
  `WHEN c.health_status NOT IN ('healthy','unknown') THEN 'health_' || c.health_status`
  —— 一条同时补掉 (a) 与 (b)，且不改 `is_routable`（**不动路由判定本身**，只补「为什么」）。
- **更强的版本**：把合取第 11 项也改掉（例如允许 `warning` 参与路由，只在 `unreachable` 时拦截）——**这会改变流量行为，属产品裁决，不代拍**。

## 四、新门：`internal/routeguard/reason_coverage_test.go`

放在**已登记**的 `routeguard` 包里（它本来就管这个视图），所以**不需要改 `GUARD_PACKAGES`**（也不需要动 `sqlguard` 的 `mustHave`）——这是 211 号那条纪律的直接受益。

**两条测试，分工明确**：

| 测试 | 覆盖面 | 断言 |
|---|---|---|
| `TestRoutableReasonColumnSurvivesEveryRedefinition` | 扫描面内**真定义**该视图的 15 个文件 | 重定义时 `unavailable_reason` 这一列不能被弄丢（丢列 = 「为什么不可路由」这个信息整体消失） |
| `TestRoutableReasonCoversEveryExcludedValue` | **只**规范对象文件 | 登记表里 3 道取值域型闸：列必须在合取里、每条覆盖必须有证据子串、每个 Gap 的字面量**必须不出现**、GapWhy 必填 |

**「Gap 的字面量必须不出现」是这道门能成为钉桩的关键方向**：有人把视图修好（给 `warning` 加了分支）却没更新登记表 ⇒ 门转红 ⇒ 强制同步，否则下一个接手的人会以为缺口还在。

### 4.1 判据迭代两版（都写进注释）

**第一版**：把「值级覆盖」套在**全部 18 个**扫描面文件上 ⇒ **把 8 个历史迁移判红**（`131` / `327` / `328` / `332` / `334`…）。原因：它们是**各时期的视图快照**——写法没有 `c.` 前缀、且早于 `health_status` 闸被引入。
⇒ **形态不同是正常的，判红它们只会制造噪声，而噪声会让人习惯性忽略这道门**（与「一道会误报正确代码的门比没有门更坏」同源）。
⇒ **拆成两条**：**列的存在性**对全部文件断言（真实且危险的失效），**值的覆盖度**只对规范文件断言。

**第二版（解析器形态假设）**：`caseSection` 最初锚在 `"END) AS unavailable_reason"`，实测**在 6/18 个文件上失效**——`460_v_routable_credential_models_periodic_exhausted.sql:79` 写的是 `END AS unavailable_reason`（**少一个右括号**，规范文件有、迁移文件没有）。
⇒ 锚得太死 ⇒ 把「排版不同」误判成「视图形态变了」⇒ 报错变噪声。
⇒ 改成锚 `AS unavailable_reason` 再往回找最近的 `CASE`。

**第三处修正（发现精度）**：扫描面 18 个里有 **3 个只提及不定义**该视图（`334_cmb_billing_align_credential_plan.sql`、`manual/20260719_add_volcano_glm52.sql`、`startup/672_local_first_title_summary_routing.sql`）——`RoutingViewFiles` 是按「含 `is_routable` 字样」选文件的，**注释里提一句也算**。加了 `definesRoutableView` 精确判定，并把「只提及」的那几个**显式打日志**出来。
⚠️ 其中 `installer/cmd/llm-gw-installer/embeddata/startup/672_local_first_title_summary_routing.sql` 我一度怀疑是「installer 会真执行、却不受门保护的迁移」，**核实后证伪**（它 `:10` 只在注释里提 `is_routable`，不重定义视图）。
⚠️ 刻意**不改** `RoutingViewFiles`：它同时被 R87-f 的禁用列守卫使用，收紧它的扫描面等于**削弱一道既有门**的覆盖面。

### 4.2 负控 2 个

| 编号 | 变异 | 实测 |
|---|---|---|
| NC-A | 在规范视图的 CASE 里加一条 `WHEN c.health_status = 'warning' THEN 'probe_warning'`（模拟有人修好视图但没更新登记表） | ✅ 红：「视图被修了而登记表没跟上。请把 "warning" 从 Gaps 移到 CoveredBy…」 |
| NC-B | 把登记表里 `c.health_status` 的覆盖证据子串改错（模拟分支被删/证据过期） | ✅ 红：「CASE 段里找不到证据 …（请改证据，不要改声明）」 |

两个负控都逐个精确回退，`git diff --stat` 复核 + 复跑绿。

### 4.3 预算

13 个守卫包全量（与 `make guards` 同参数 `-count=1 -timeout=120s`）：**全 ok**，`routeguard` **1.434s**，最慢 `sql/schema` 31.515s，墙钟 **34s**。新门本身 0.001s 量级（无 I/O 之外的开销）。

## 五、⚠️ 覆盖边界（如实登记，本轮**无结论**）

| 集合 | 数量 | 状态 |
|---|---|---|
| 扫描面内（`RoutingViewFiles` = 规范文件 + `sql/migrations` 下含 `is_routable` 的 `.sql`） | 18 | 其中**真定义** 15、只提及 3 |
| 全仓含 `is_routable` 的 `.sql` | 27 | 差集 9 个在扫描面外 |
| **含该视图定义但不在扫描面内** | **3 份 schema dump**：`deploy/sql/schemas/baseline/01-schema.sql`、`installer/cmd/llm-gw-installer/embeddata/01-schema.sql`、`sql/schema/01-schema.sql`（各 `:19108`，同一份 dump 的副本） | **是否与规范文件漂移，本轮没有结论**（没查有没有别的门在间接比对，也没查部署时以哪份为准）⇒ 登记为**已知边界**，既不写成「已覆盖」也不写成缺陷 |

## 六、诚实边界

- **未执行任何 SQL、未连 PG/Redis/Docker、未起真进程**：本条结论是**逐字读视图定义 + 逐跳读写入方**得出的静态结论。`CREATE OR REPLACE VIEW` 的实际行为、以及「改完是否真的能被路由」**都未在真库验证**。
- **`warning` 值的生产可达性已证**（`:349` 的赋值语句 + 它的两个 HTTP 调用方），但**没有真库取数**证明生产里确实出现过 `health_status='warning'` 的行 ⇒ 「已发生 / 未发生」**不判定**。
- **没有追的**：`doHealthCheck` 的探测是否真在生产被点过（无日志可读）；`admin/diagnostics_credential.go:105/:201-204` 那条诊断文案对 `warning` 的展示（未核）。
- **3 份 schema dump 的漂移问题未查**（§五）。
- **本机无 `bash`** ⇒ `scripts/checks/guards-sync.sh` 至今**未执行过**；本轮未改 `GUARD_PACKAGES`，该脚本的判定不受影响，但**仍未实证**。
- **`go test ./...` 全量未跑**；`core.hooksPath` 未设 ⇒ 未经 pre-push 门；**CI 从未运行**。
- 子代理本轮未再启用（上一轮那份枚举已足够，其未复核部分**本轮未据此下任何结论**）。

## 七、待裁决清单更新

- **86（新增，P2）**：`is_routable` 第 11 项排除 `health_status='warning'`，而 `unavailable_reason` 的 CASE 无对应分支 ⇒ 被拦住时原因 NULL；且 `unreachable` 的分支带 1 小时时间窗而合取不带，超时后同样无原因。
  - **建议的最小修法（不代拍）**：加一条**无时间窗**的 `WHEN c.health_status NOT IN ('healthy','unknown') THEN 'health_' || c.health_status`——只补「为什么」，**不动路由判定**。
  - **前置**：先定「改哪份定义 + 新迁移 checksum 口径 + 3 份 schema dump 是否同步」（184 号雷区）。
- **85 附带一条待查**（219 号登记，本轮仍未追）：`handleEmergencyRepair` / `handleForceRecover` 不重置 `node_probe_state` ⇒ 合取第 12 项未清。本轮只把合取列全了，**未逐个入口重新核对第 12 项**。
- 84 / 83 / 82 / 81 / 80 / 79 均未动。

## 八、playbook §165–§167

**§165 判定与「为什么」是同一个表达式的两半 —— 少一半就是「拦了但说不清」。**
证据（220 号）：`is_routable` 十二项 AND 合取（`:17-19`）与 `unavailable_reason` 的 CASE（`:20-40`）描述的是**同一个准入判定**；CASE 少覆盖一个取值（`warning`）就产生「凭据不可路由、原因 NULL」。
⇒ 新增/修改任何准入闸时，**必须同时问「它的排除值在这个判定里有没有对应的原因分支」**；两半的覆盖度要能被机械比对。
⇒ **最危险的形态是「原因分支带了额外条件」**（本例的 1 小时时间窗）：合取无条件、CASE 有条件 ⇒ 条件外的行**永远说不清原因**，而且**看不出是缺口**——它看起来只是「老数据」。

**§166 判据套在「历史快照」上会制造噪声，而噪声会让人习惯性忽略这道门。**
证据（220 号）：第一版把值级断言套在全部 18 个重定义文件上，把 8 个历史迁移判红——它们是各时期的视图快照，形态不同是**正常的**。
⇒ **把「结构不变量」与「现行形态属性」分开断言**：不变量（如「重定义不能丢列」）对全部版本断言；形态属性只对权威源断言。
⇒ 判据一旦开始误报，**先怀疑覆盖面口径，再怀疑代码**——与 §151「先问它会不会把缺陷判成通过」是成对的另一问：**它会不会把正确判成错误**。

**§167 按「含某字样」选文件会把注释算成定义；两个数字相等也可能是两个不同的集合。**
证据（220 号）：`RoutingViewFiles` 返回 18，全仓含 `is_routable` 的也是 18，但**集合不同**——扫描面内混进 3 个只提及不定义的文件，同时 3 份真含定义的 schema dump 在扫描面外。
⇒ 「数量对上了」不等于「集合对上了」；凡是用「含某关键字」做发现的判据，都要再问一句「**它是不是真的做了那件事**」，并在日志里把「提及但没定义」的**显式打出来**（而不是静默滤掉）。
