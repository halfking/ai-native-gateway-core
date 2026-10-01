# 182 号 · R89-CR —— 41 个迁移编号在 `startup/` 与 `domain/` 之间完全撞车；`domain/` 全目录 0 条进台账

> **日期**：2026-10-01
> **轮次**：R89-CR（第 82 轮，审计第 182 号）
> **类型**：把 181 号的**一个**根因**类化 + 改写归因**（归因被本轮证据修正）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：181 号（版本号撞车让系统监测审计写入从未成功过）

---

## 〇、起手

181 号的结论是：`system_probe_runs` 的 rich 写入方因 `total_tokens` 列不存在而**必然失败**，
根因是「`domain/346` 的版本号被 `startup/346` 占用」。

**本轮先做 181 号自己留下的建议盘点（startup 与 domain 的编号重叠范围到底多大），
结果推翻了我自己 181 号的归因。**

---

## 一、F1：编号撞车规模 —— **41 个**，不是「344–355 一段」

```
domain/  编号数 50      startup/  编号数 457
交集（comm -12）41 个：
032 033 034 035  328 329 330 331 332 333 334 335 336 337 338 339
341 342 343 344 345 346 347 348 349 350 351 353 354 355 356 357 358
359 360 361 362 363  612 613 640
```

**⇒ 328–363 是一段连续 30+ 号的系统性撞车，另有 032–035 与 612/613/640。**

| 编号 | `domain/` | `startup/` |
|---|---|---|
| 344 | `344_system_probe_runs.sql` | `344_usage_ledger_hot_independence.sql` |
| 346 | `346_system_probe_run_tokens.sql` | `346_routing_decision_log_hot_independence.sql` |
| 343 | `343_credential_probe_queue.sql` | `343_fix_routing_decision_log_columnar.sql` |
| 336 | `336_deduplicate_provider_models.sql` | `336_promote_default_to_partition_functions.sql.bak.skip` |

---

## 二、F2：🔴 台账实证 —— **`domain/` 目录 0 条进台账**

```sql
-- 344–347 的台账记录
 344 | 344_usage_ledger_hot_independence.sql
 345 | 345_request_wal_hot_independence.sql
 346 | 346_routing_decision_log_hot_independence.sql
 347 | 347_credential_model_index_hot_independence.sql
   ← 全部是 startup 侧文件名，domain 侧一个都没有

-- domain 目录特征文件名在台账里的命中
SELECT … WHERE description LIKE '%system_probe_run%' OR '%credential_probe_queue%' …
→ 4 条，但逐条看全是：
   488 | credential_probe_queue_startup_backfill      ← startup
   489 | credential_probe_queue_runtime_columns       ← startup
   490 | credential_probe_queue_runtime_columns       ← startup
   644 | rollup: … self_check_runs_selection_strategy_check …  ← startup
```

**⚠️ 陷阱记录**：`032`–`035` 在台账里**完全没有行** ⇒
**「台账里有这个 version」对 032–035 是假的**，
**只有把 `version` 与 `description` 一起看才能区分两个目录**（这正是 181 号 §73 第 3 条的价值兑现）。

---

## 三、🔴 F3：但撞车**不是** 181 号的根因 —— 181 号的归因本轮被改写

**这一步差点又发一个方向相反的结论，所以写清楚推理。**

181 号说：`total_tokens` 由 `domain/346` 添加，因编号被占而从未执行 ⇒ 缺列。

**本轮的两条反证：**

**反证 A —— 表本身存在，且不是 `domain/343/344` 建的。**
真库 `credential_probe_queue` 与 `system_probe_runs` **都存在**，
若 `domain/343/344` 真被跳过，这两张表不该存在。
⇒ **建表走的是另一条路径**（`sql/objects/tables/` 规范层 + baseline schema，
`system_probe_runs` 的表注释白纸黑字写着「**344:** 系统监测模块审计表」——
**编号对得上，但目录归属从表本身分辨不出来**）。

**反证 B（决定性）—— SSOT 里本来就没有 `total_tokens`。**

```bash
# 规范 schema（sql/objects/tables/system_probe_runs.sql，27 列）
attempt automaticity created_at credential_id dns_ms err_code err_detail finished_at
http_status id latency_ms max_attempts provider_id raw_model recent_request_at
recent_request_id request_body_preview request_url response_body_preview skip_reason
source started_at status task_id task_type tls_ms worker_id
                                    ↑ 有 dns_ms / tls_ms，没有 total_tokens

# audit.go 的 INSERT 列清单 vs SSOT 求差集
comm -23 audit.go列 SSOT列
→ total_tokens          ← 唯一一个
```

**⇒ `total_tokens` 在真库和 SSOT 里都不存在。**
**⇒ 真正的根因不是「迁移被编号挤掉了」，而是：**
**`audit.go` 写的那一列，从一开始就没有进入过项目的规范 schema。**
**⇒ 181 号的归因必须改写**（§16：否定结论分轮登记，不回改历史报告正文）：

| | 说法 |
|---|---|
| ❌ 181 号 | 「`domain/346` 因版本号被占而未执行，导致缺列」 |
| ✅ **182 号** | 「**`audit.go` 引用了一个规范 schema 里不存在的列 `total_tokens`；`domain/346` 那条迁移即使执行了，也只是给一个从不存在于 SSOT 的列打补丁**。编号撞车是**真实的独立问题**，但它不是本缺陷的根因。」 |

**⚠️ 两条结论并存，不互相抵消**：编号撞车（41 个）**本身仍是真问题**（§四），
只是它**不解释** 181 号那个症状。

---

## 四、编号撞车本身：为什么它仍然是真问题

| 影响 | 说明 |
|---|---|
| **可发现性** | 41 个撞号**在单文件阅读层面完全不可见**；只有把两个目录的文件名 + 台账做差集才看得见 |
| **`domain/` 无投递通道** | `grep -rn "migrations/domain" --include=*.go` **零命中** ⇒ 没有执行器扫这个目录 |
| **危险度分级** | `.skip`（180 号）**人眼可见**；**编号撞车文件完全正常，只有对齐台账才看得见**（§73 第 3 条表） |
| **实际影响** | ⚠️ **本轮未证实** `domain/` 里任何一个文件是「本该跑却没跑」的。`domain/343/344` 建的表**在真库存在** ⇒ **至少这两条的效果已通过别的路径达成** |

**⇒ 诚实边界：41 个撞号是「结构缺陷」而非「已证实的运行时缺陷」。**
**⇒ 定级 P3**（机制成立 ✅ / 规模 ✅ / 实际后果 **❌ 未证实**）。

---

## 五、本轮做过的排除（防止下轮重查）

**`db/` 包 79 个 Go 内联迁移 `ensure*()` —— 零死函数。**
逐个数调用次数（`db/` + `cmd/gateway/` 两个目录定向扫描）：

```
计数分布：2 次×3、3 次×36、4 次×11、5 次×13、6 次×3、7 次×4、8 次×2、10 次×3、12/13/14/41 各 1
计数 ≤1 的：0 个
```

**⇒ 79 个 `ensure*` 全部有调用点，最低的 2 次 = 定义 + 1 次真实调用。**
⇒ **与 173 号（`KeyInfo.IsInternal` 零写入点）不同形态：这批是接线的**（§66）。

**⇒ 这一条同时说明 `db/db.go` 里的 Go 内联迁移（`ensureReportSnapshots` 等，
注释写「原文件死放 migrations/ 顶层无投递通道」）是本仓**已知的既定补偿机制**，
**不是本轮新发现** ⇒ 按 §41 桶②「明确声明 = 不是缺陷」处理，仅登记不追。

---

## 六、playbook §74 新增

> **§74 撞车类归因要问一句「即使被挤掉的那个跑了，问题还在吗」—— 否则你会把一个独立问题当成根因**

**由来**（R89-CR / 182 号）：181 号把 `total_tokens` 缺列归因于「`domain/346` 被 `startup/346` 占用」。
本轮查 SSOT 才发现：**`total_tokens` 在规范 schema 里本来就不存在**
⇒ **那条迁移即使执行，也只是给一个从不在 SSOT 的列打补丁 ⇒ 问题依旧。**

**⇒ 落地三条：**
1. **「A 挤掉了 B」成立之前，先问「B 跑了能不能解决症状」** ——
   最省事的验法是**去 SSOT（`sql/objects/tables/`）查那一列在不在**：
   **在 ⇒ 撞车是根因；不在 ⇒ 撞车只是并行的独立问题**；
2. **⚠️ 台账类证据必须带 `description`** ——
   本轮 `032`–`035` 在台账里**完全没有行**，而 `344`–`347` 有行但**描述指向另一目录**；
   ⇒ **只查 `version` 存在性会把「另一目录的同号迁移」误当成「这个迁移跑过」**（§73 第 3 条的第二次兑现）；
3. **⚠️ 「表存在」不能证明「建表迁移跑过」** ——
   本仓真库的表来自 `sql/objects/tables/` 规范层 / baseline schema，
   **而 `system_probe_runs` 的表注释写着「344:」**
   ⇒ **注释里的编号是真的，但目录归属从表本身分辨不出来**
   ⇒ **看到「注释说是 344」不能推断「跑的是 domain/344 还是 startup/344」**（§45 同族）。

**⇒ 与 181 号 §73 的关系**：
§73 第 3 条给了「三方对齐（文件名+version+description）」的**方法**；
**本条给了它的反面 —— 对齐之后还要再问一句「撞车到底解释不解释症状」。**
**否则会产出一份「归因清晰、方向错误」的报告，而它比没有报告更贵**（因为它看起来已经查清了）。

**同族**：§16（否定结论分轮登记）/ §41（注释三桶）/ §45（命中 ≠ 引用）/
§66（守卫存在 ≠ 可达）/ §72（死代码看服务对象）/ §73（三种失效形态）。
