# 181 号 · R89-CQ —— 一次「版本号撞车」让系统监测审计写入**从未成功过**：`system_probe_runs` 的 rich 写入方在真库 0 行

> **日期**：2026-10-01
> **轮次**：R89-CQ（第 81 轮，审计第 181 号）
> **类型**：**数据闭环缺口**（objective 明确要求的「反馈闭环」）+ 死写入方定级（新增 **待裁决 73，P2**）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：180 号（8 个 `promote_*_default_batch` 全族无消费方 + 运维误导）

---

## 〇、起手

180 号在核对「哪些表有 `_default` 分区但没有 promote spec」时，捞到 6 张表。
其中 `system_probe_runs` 立刻跳出来，因为它同时命中**三个**异常：

| 观察 | 数值 |
|---|---|
| `_default` 分区行数 | 1,484 |
| **该表全仓无任何生产 `DELETE` / TTL** | 唯一 DELETE 在 `metrics_collector_test.go` |
| **有 8 个索引，其中 2 个建在 preview 文本列上** | `system_probe_runs_default` 968 kB + 索引 624 kB |

**本轮的问题：那 1,484 行是谁写的，以及为什么另外 4 个文本列全是 NULL？**

---

## 一、F1：先把「谁在写」查清 —— 生产里 **100% 是一个降级写入方**

```sql
SELECT source, task_type, worker_id, count(*), min(started_at)::date, max(started_at)::date
FROM system_probe_runs_default GROUP BY 1,2,3;
```

| source | task_type | worker_id | count | 区间 |
|---|---|---|---|---|
| **`legacy_selfcheck`** | `chat_tool` | `credential-selfcheck-worker` | **1,484（100%）** | 09-03 → 09-30 |

**⚠️ 两种读法，必须分开：**
- **「legacy」**（legacy_selfcheck）字面就在 source 值里 ⇒ **这不是新写的功能，是历史路径**；
- 而 1,484 行里 **`http_status` / `latency_ms` / `dns_ms` 三列的非空计数全是 0**
  ⇒ **没有任何一行是「rich 写入方」写的**。

---

## 二、F2：🔴 两个写入方，逐行读包围函数后确认

### 写入方 A（rich）—— `bg/systemmonitor/audit.go:121-150`

```go
_, err := a.db.Exec(insertCtx, `
    INSERT INTO system_probe_runs (
        task_id, task_type, automaticity, credential_id, provider_id, raw_model,
        source, worker_id, status, attempt, max_attempts,
        http_status, latency_ms,
        total_tokens,                      // ← 第 15 个列名
        request_url, response_body_preview,
        err_code, err_detail, ...
```

**🔴 第 15 个列名 `total_tokens` 在真库表里不存在。**
全表 27 列实为：`id, task_id, task_type, automaticity, credential_id, provider_id, raw_model,
source, worker_id, status, attempt, max_attempts, http_status, latency_ms, **dns_ms, tls_ms**,
request_url, request_body_preview, response_body_preview, err_code, err_detail, skip_reason,
recent_request_id, recent_request_at, started_at, finished_at, created_at`。

⇒ **`total_tokens` 被 `dns_ms` / `tls_ms` 取代了。**
⇒ **这条 INSERT 必然以 `column "total_tokens" does not exist` 失败**（本轮真库实测该报错，见 §四）。

**⇒ 连带发现第二个错位**：`:146` 把 `extrasJSON` 传给了 `response_body_preview`（`$16`），
**而 `request_body_preview` 根本不在列清单里**（`:127` 写了 `request_url, response_body_preview`，
漏了 `request_body_preview`）。⚠️ 但**这一条被上一条掩盖了** —— 前一个错位已让整条 SQL 失败，
**所以「$16 传错值」目前不可观测**（§70 的同族：**修好第一个错位，第二个会立刻显形**）。

### 写入方 B（legacy）—— `bg/credential_selfcheck.go:850-853`

```go
INSERT INTO system_probe_runs (task_id, task_type, automaticity, credential_id,
                                raw_model, source, worker_id, status, started_at, finished_at)
VALUES ($1,...,$10)
```

**只写 10 列** ⇒ 完美解释真库「`request_url` / `err_detail` / 两个 preview **1484/1484 全 NULL**」。

**⇒ 这不是「字段没写」，是「这个写入方压根不写这些字段」。**

---

## 三、🔴 F3：根因 —— **迁移版本号撞车，`domain/346` 永远不会被执行**

`total_tokens` 是由这条迁移加的：

```
sql/migrations/domain/346_system_probe_run_tokens.sql:9
  ALTER TABLE system_probe_runs ADD COLUMN IF NOT EXISTS total_tokens BIGINT NOT NULL DEFAULT 0;
```

**但真库的迁移台账里，`346` 被另一个文件占用了：**

```sql
SELECT version, description FROM public.schema_migrations WHERE version BETWEEN '344' AND '348';

 344 | 344_usage_ledger_hot_independence.sql
 345 | 345_request_wal_hot_independence.sql
 346 | 346_routing_decision_log_hot_independence.sql     ← 占用 346
 347 | 347_credential_model_index_hot_independence.sql
```

**两条同号迁移，文件名完全不同：**

| 目录 | 文件 | 状态 |
|---|---|---|
| `sql/migrations/startup/` | `346_routing_decision_log_hot_independence.sql` | **已执行**（台账有记录） |
| `sql/migrations/domain/` | `346_system_probe_run_tokens.sql` | **从未执行** |

**⇒ 台账以 `version` 为主键语义，`346` 一旦被 startup 那条登记，
domain 那条就永远轮不到。**

**⚠️ 且 `domain/` 目录没有任何执行器扫描**（`grep -rn "migrations/domain" --include=*.go` 零命中），
**只有 `startup/` 在 `db/db.go` 的三个 INSERT 点被写台账** ⇒
**这批 `domain/` 迁移在本环境是「摆在那里的」**（与 180 号的 `.bak.skip` 属**不同**的失效形态，见 §六）。

---

## 四、变异验证：改坏→红、还原→绿

**验证 1（正向坐实）**：在 `BEGIN … ROLLBACK` 事务里跑那条 INSERT（**无副作用**），实测报错：
```
BEGIN
ERROR:  column "total_tokens" of relation "system_probe_runs" does not exist
LINE 1: ..., attempt, max_attempts, http_status, latency_ms, total_toke...
                                                             ^
ROLLBACK
```

**验证 2（等价性反证）**：真库已写 1,484 行，`id` 从 **1 连到 2600** 且**全部 `legacy_selfcheck`**
⇒ 若 `audit.go` 曾成功哪怕一次，该 id 段里就会出现 `source ≠ legacy_selfcheck` 的行、
且 `http_status` 非空计数 > 0。**实测 `http_status` 非空 = 0 / 1484**
⇒ **「rich 写入方从未成功过」不是推测，是 id 段级证据。**

**⇒ 两个方向相互独立且同时指向同一结论**，符合 §33 对双向证据的要求。

**验证 3（决定性：唯一阻断点）**：把同一批列**去掉 `total_tokens`** 后再插：

```
BEGIN
INSERT 0 1
             probe
--------------------------------
 INSERT_WITHOUT_total_tokens=OK
ROLLBACK
```

⇒ **`total_tokens` 缺失是这条 INSERT 的唯一阻断点**，其余列（`dns_ms`/`tls_ms` 不在清单里也没关系）
**全部合法** ⇒ **修法可以精确到「删掉 INSERT 里的那一个列名与占位符」或「补上该列」，不需要动表结构以外的任何东西。**

**⚠️ 顺带一个本轮踩到的类型陷阱（已修正）**：`task_id` 是 **`bigint`** 而不是 `text`
（`audit.go:143` 传的是 `task.ID`）⇒ 第一次用字符串 `'probe-vtest'` 试插时报的是
`invalid input syntax for type bigint`，**那是个与本缺陷无关的错**。
⚠️ **若当时就此收手，就会把「类型错」当成「列缺失」的证据** —— 两者报错行不同、原因不同（§70 同族）。

---

## 五、影响面：四维评估（§37）

| 维度 | 评估 | 证据 |
|---|---|---|
| **会失败吗** | ✅ **会** | `total_tokens` 列不存在 ⇒ INSERT 必报错 |
| **会波及吗** | ✅ **会** | `system_monitor` 的**全部** rich 审计写入；`monitor.go:669 sm.audit.Write` 是系统监测的唯一审计出口 |
| **当事方知道吗** | ❌ **不知道** | `audit.go` 返回的 error 在 `monitor.go:669` 被调用方接收，但真库 1,484 行全是 legacy ⇒ **失败被静默吞掉或该路径没跑** |
| **有痕迹吗** | ⚠️ **部分** | 每次失败会在 PG 日志留 `column "total_tokens" does not exist`；但 `system_probe_runs` 表内**看不出来**（只有 1,484 行 legacy） |

**⇒ 定级 P2**（机制坐实 + 数据侧后果坐实「rich 写入 0 行」，
但**未证实**用户可见的功能退化 —— 管理端 `recent-runs` 读的是 legacy 数据，页面不空）。

**⚠️ 诚实边界（§41 桶②/③ 的应用）**：
`monitor.go:669` 的调用点**存在**、`NewSystemMonitor` **在 `main.go:3507` 真实装配**（活的），
**所以不能断言「monitor 没跑」**；能断言的只是
**「即便跑了，它的审计写入也会因缺列而失败」**。
⚠️ **另需说明**：`main.go:3507` 处于一段条件分支内，**本机无网关进程**（§44③），
**无法判定生产上该分支是否进入** ⇒ **「rich 写入今天是否真的被调用过」仍未证实**，
但「**一旦被调用就必失败**」已完全坐实。

---

## 六、附带澄清：两处「失效迁移」形态**不同**，不可混为一谈

| 形态 | 例子 | 机制 |
|---|---|---|
| **A：被显式否决** | `startup/336_….sql.bak.skip`（180 号） | 文件名带 `.skip` ⇒ **主动标记为不跑** |
| **B（本轮）** | `domain/346_…` | **文件名正常，但版本号被 startup 同号文件占用** ⇒ **没有任何标记会阻止它被"认为"该跑**，只是永远轮不到 |

**⇒ B 比 A 危险**：A 至少在人眼可见（`.skip` 后缀），B **在目录层面完全正常**，
只有把 `version` 与 `description` 一起看台账才会发现对不上。

**⇒ 盘点建议（不擅自动手）**：`domain/` 目录与 `startup/` 存在**编号重叠**，
**至少 344–355 这一段全部重叠**（`startup/350` 甚至有三个同号文件）。
**这类冲突无法靠单文件阅读发现，必须把两个目录的文件名 + 台账做三方差集。**

---

## 七、playbook §73 新增

> **§73 「某列全 NULL」先分「没人写」与「写了但失败」—— 两者的判别数据不在同一张表里**

**由来**（R89-CQ / 181 号）：`system_probe_runs` 的 `request_url` / `err_detail` /
两个 `*_body_preview` **1484/1484 全 NULL**。
第一反应是「字段没人写」—— **只对了一半**：
- `credential_selfcheck.go:850` 的 INSERT **确实不写这些列**（⇒ 这部分成立）；
- 而 `systemmonitor/audit.go:121` **确实要写**（还多写一个 `total_tokens`），
  **但它必然失败**（⇒ 这部分是「写了但没成功」）。

**⇒ 落地三条：**
1. **「列全 NULL」有两种机制，判别数据不在那张表里** ——
   「没人写」要查**各写入方的列清单**（本轮两个写入方一列一列比对才看得出来）；
   「写了但失败」要查**台账 / id 段 / 非空计数**（本轮 `http_status` 非空 = 0 是决定性的）；
   ⚠️ **只查「有没有写入方提到这个列名」会漏掉第二类** ——
   **「提到了」不等于「成功写进去了」**；
2. **⚠️ 一个 SQL 里有多个错位时，第一个错位会掩盖后面所有错位** ——
   本轮 `total_tokens`（不存在）掩盖了 `$16 extrasJSON → response_body_preview` 的传参错位。
   ⇒ **修第一个之前，不要宣称「只错了一处」**（§70 同族：**要问「修好第一个之后，第二个会立刻显形吗」**）；
3. **⚠️ 迁移「未执行」有三种形态，判定方式完全不同**：
   `.skip` 后缀（显式否决）／**版本号被同号文件占用**（本轮，静默失效）／目录不被执行器扫描（本轮 `domain/`）。
   ⇒ **判「某迁移是否执行过」必须三方对齐：文件名 + `schema_migrations.version` + `schema_migrations.description`。**
   ⚠️ **只查 `version` 存在与否会得到「已执行」的错误结论**（本轮 346 就在台账里，只是描述对不上）。

**⇒ 与 180 号 §72 的关系**：§72 说「死代码还要看它服务的对象」；
**本条说「死写入方还要看它是『没被调用』还是『被调用但失败』」——
后者更隐蔽，因为它在代码里看起来是活的、在台账里看起来是跑过的。**

**同族**：§10（查调用方）/ §12（codegraph 死代码不可信）/ §37（四维评估）/
§41（注释/代码都不是契约）/ §45（命中 ≠ 引用）/ §66（守卫存在 ≠ 可达）/
§70（查错出口）/ §72（死代码要看服务对象）/ §33（双向证据）。
