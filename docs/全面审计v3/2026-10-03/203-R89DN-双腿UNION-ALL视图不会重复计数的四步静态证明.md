# 203 号 · R89-DN —— 先验 202 号登记的**假阻塞**：`UNION ALL` 双腿视图**不会重复计数**（四步静态证明），并附一份**已知不完整**的还债三角化

> 结论先行：202 号把 21 条 `DEBT(R47)` 债登记为「需真库验证成本口径」。本轮先验这个前提本身 ——
> **结论是那个前提过强**。`request_logs_with_current_month` 是 `hot UNION ALL mother` 且**无 `EXCEPT`**，
> 所以「同一行会不会同时在两腿」是个真问题；而答案是**不会**，且**四步全部静态可证**。
> ⇒ **假阻塞解除**。本轮**零代码改动**（纯核实与登记）。

---

## 〇、起手

202 号 §五 登记：

> **P2，登记不修**。理由：本轮无 PG，无法验证双腿化的成本口径；先例 `976b871ce` 是拿真库 30 天窗口核对的。

这条理由**本身是对的纪律**（不盲改生产 SQL），但它把「需要真库」当成了**前置条件**。本轮要问的是：**真的需要吗？**

因为 `sql/objects/views/request_logs_with_current_month.sql` 的定义是

```sql
FROM public.request_logs_hot
UNION ALL
SELECT … FROM public.request_logs;
```

**没有 `EXCEPT`、没有去重。** ⇒ 如果同一行能同时存在于两腿，视图就会**双倍计数**，
而把 21 条读面转过去就等于**静默放大每一个数字**。那不是 P2，那是 P1。
⇒ 所以这个问题**必须先答**，它决定后面 21 条能不能动。

---

## 一、四步静态证明：一行 `request_logs` 在任一提交快照上**只属于一条腿**

### 步骤 1：母表的行**只**由 promote 写入

| 检查面 | 结果 |
|---|---|
| Go 生产代码 | `grep '(INSERT INTO\|UPDATE\|DELETE FROM)\s+(public\.)?request_logs(\s\|$\|_)' --include=*.go`：命中里**唯一**的非测试、非 `_hot`/`_bodies`/`_default` 站点是 `bg/lite_retention_worker.go:107` `DELETE FROM request_logs WHERE rowid IN (…)` —— `rowid` + 子查询是 **SQLite** 形态（objective 的「本地简化模式(sqlite+memory+files)」），正是 `partguard` 明确排除的那 7 处之一（`TestSQLiteExclusionIsLoadBearing` 实测「SQLite 侧同名表写操作 7 处已按路径排除」） |
| `.sql` 迁移 | 每个 `INSERT INTO public.request_logs` 命中都是**同一个 promote 函数体在迁移链上的版本**（602 → 688 → 695 → 697 → 698 → 739）+ `01-schema.sql`/baseline 的同一份 dump + 一个 `sql/migrations/test/` 的 **test fixture**。**无任何回填绕过 promote。** |

⚠️ `db/db.go:5848` 的注释也写着「request_logs only receives promoted rows」——但按 198 号起的规矩，**注释不是契约**，所以上表是实测而非引用。

### 步骤 2：promote 是**单条数据修改 CTE**，删与插原子

`sql/objects/functions/promote_request_logs_hot_to_partition_interval_integer.sql:93-181`：

```sql
WITH batch AS (
    SELECT id, ts FROM public.request_logs_hot
    WHERE ts < statement_timestamp() - p_retention
    ORDER BY ts, id LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED
),
moved_rows AS (
    DELETE FROM public.request_logs_hot
    WHERE id IN (SELECT id FROM batch)
    RETURNING  …120 列…
)
INSERT INTO public.request_logs ( …120 列… )
SELECT      …120 列… FROM moved_rows;
```

⇒ 一个语句里 `DELETE … RETURNING` 直接喂给 `INSERT`。在 READ COMMITTED 下，并发读要么看到事务前（行只在 hot）、要么看到事务后（行只在 mother），**不会同时看到两处**。

### 步骤 3：`INSERT` **没有 `ON CONFLICT`** ⇒ 失败是 fail-safe，不是 fail-duplicate

`:156-181` 是纯 `INSERT … SELECT … FROM moved_rows`，无冲突目标。

⇒ 若 `INSERT` 因**任何**原因失败（无对应分区、唯一键冲突、类型错），**整条数据修改 CTE 中止**，`DELETE` 一并回滚 ⇒ **行留在 hot**，下轮重试。
⇒ 既**不丢**也不**重**。这是本条证明里最关键的一环：若当初写成 `ON CONFLICT DO NOTHING`，就会变成「删了却没插进去」的**静默丢数据**。

### 步骤 4：695 的「self-heal demote」**不搬行**

`:54-67` 的 695 自愈段是

```sql
UPDATE public.request_logs_hot h SET is_final_success = FALSE
 WHERE h.is_final_success AND h.ts < … AND EXISTS ( … FROM ONLY <mother partition> x … )
```

它是**改标志位**，不是把母表行搬进 hot ⇒ **不能造出重复**。

### ⇒ 结论

**任一提交快照上，一个 `request_logs` 行恰好属于两条腿之一。`UNION ALL` 不重复计数。**

---

## 二、附带发现：2026-08-25 那个事故修复是**承重**的，而且它的失效形态比重复计数更糟

`:77-92` 的注释记录了旧实现：

> 旧实现**先**从 `request_logs_hot` `DELETE`，**再**单独（且在异常子块之外）`INSERT INTO request_logs SELECT * FROM _promote_hot_batch`。两表 schema 漂移（hot 多 `caller_id`/`session_correlation_id`/`status_code`，父表多 14 个 legacy 列）让位置化 `SELECT *` **每批都失败**；而 **DELETE 已经在异常块之外执行** ⇒ **每批失败都被静默丢弃**，而 `RAISE WARNING` **还宣称 "rows preserved in hot table"**。

⇒ 旧实现的失效形态是**静默丢数据 + 错误消息撒谎**（比「重复计数」严重一级）。
⇒ 现在的形态是「单条 CTE + 无 `ON CONFLICT`」⇒ **失败可观测且无损**。
⇒ **`976b871ce` 说的「两表 cost 口径经核对一致（同窗口均 150.15 美元）」因此有了机制解释**：不是因为两个查询碰巧一致，而是因为视图的两腿在任一快照上互斥。

---

## 三、还债三角化：**已知不完整**，只够当排序用

既然假阻塞解除，那 21 条能不能还？**判据不是「能不能」，而是「这条查询在视图上还快不快」。**
`UNION ALL` 要合并两腿 ⇒ **有 `ts` 谓词才有下推**（`976b871ce` 自述「两腿同吃 ts 谓词下推，1.7s 剪枝收益保留」）。

我按 15 个 Go 侧 `DEBT(R47)` 文件做了文件级三角化：

| 判定 | 文件数 | 裸读数 |
|---|---|---|
| 文件内**可见** `ts` 谓词 | 1（`discovery/discovery.go`） | 1 |
| 文件内**不可见** `ts` 谓词 | 13 | 17 |
| 我的正则数到 0 裸读 | 1（`admin/memora_handlers.go`） | 0 |
| **合计** | 15 | **19** |

### ⚠️ 这份数据有两处已知缺陷，**不可当作判定用**

1. **文件级文本 grep 看不见动态拼装的谓词。**
   `admin/memora_handlers.go:615-616` 的 SQL 是拼接的：
   ```go
   (SELECT client_model FROM request_logs `+where+` ORDER BY ts DESC LIMIT 1)
   FROM request_logs `+where+`
   ```
   谓词在 `where` 变量里，不在字面量里。
2. **它数到的裸读少于那道门。** `memora_handlers.go` 在 `DEBT` 名单里，我的正则却数到 0 —— 因为后面跟的是反引号而不是别名/`WHERE`/`;`/`)`。⇒ **覆盖面 < `sqlreadguard` 的判据。**
3. 反过来 `discovery/discovery.go` 那一处的谓词也是 `fmt.Sprintf` 模板里的 `AND rl.ts > now() - interval '%d hours'`（`:1168`）—— 同样是**字符串命中**，不是真的「有可用下推的谓词」。

⇒ 所以表格里那列**只能读作「文本里看不看得见谓词」**，不是「有没有谓词」。
⇒ **它唯一的用途是排序**：把「看得见谓词、改造面最小」的排在前面。**判定必须逐条打开那个字面量看。**

---

## 四、还债的安全判据（给下一个动手的人，逐条都要过）

1. **该查询的 SQL 必须是字面量**（或至少 `where` 子句是常量），否则改完无法确认谓词下推 —— `memora_handlers.go:615` 就是反例。
2. **必须带 `ts` 谓词**，否则视图会全表扫两腿 ⇒ 用 1.7s 换 8h 完整性是亏的。
3. **`ORDER BY … LIMIT n` 跨 `UNION ALL` 需要合并排序**：`ORDER BY ts DESC LIMIT 1` 这类会从两腿各取再归并，可能比裸读更慢。`memora_handlers.go:615` 正是这个形态。
4. **改完跑三道门**：`sqlreadguard`（白名单条目会被自清洁门要求删除）、`partguard`（不涉及，它是父表**写**门）、`rowsguard`（若循环形态变了）。
5. **每还一条，`debtBaseline` 不需要动**（202 号的设计：移除自动接受）；自清洁门会要求从白名单删条目。

---

## 五、证伪与未settle 的边界

| # | 事项 | 处置 |
|---|---|---|
| ❌ | 撤回「202 号的『需真库验证成本口径』是必要前置」 | 撤回。四步静态证明覆盖任何窗口，且解释了 `976b871ce` 那个 150.15 美元**为什么**成立 |
| ❌ | 撤回「`memora_handlers.go` 已经是双腿视图，不需要还债」 | 撤回。`:522` 已是双腿，但 `:615-616` 仍是裸读 —— **同文件内形态不一致** |
| ❌ | 撤回「三角化显示 13 个文件无 `ts` 谓词」 | 撤回。文件级 grep 看不见拼接谓词（`memora_handlers.go` / `discovery.go` 都是反例）⇒ 只能读作「不可见」 |
| **未settle** | **语义重复**：两条**不同 id** 的行若共享同一 `request_id` / `client_request_id` / `parent_request_id`，落在不同腿时视图仍会出现「同一逻辑请求两条」。这与上面的「同 id 双计」是**两回事**，**静态不可判定**。`request_checksum` 列存在，但我未追它在去重中的角色 | **登记为待核实**，不宣称视图「完全安全」 |
| **未settle** | promote 之外是否还有**运行时**路径（PL/pgSQL 函数、installer 内嵌迁移）往母表灌行 | 本轮只扫了 `.sql` 与 `.go`；未逐个打开 `installer/cmd/llm-gw-installer/embeddata/` 下的每一份 |

---

## 六、验证命令与结果（可重放）

```powershell
# 1) 视图定义（无 EXCEPT / 无去重）
Get-Content sql/objects/views/request_logs_with_current_month.sql
#   FROM public.request_logs_hot UNION ALL SELECT … FROM public.request_logs;

# 2) promote 函数体：单条数据修改 CTE + 无 ON CONFLICT
#   promote_request_logs_hot_to_partition_interval_integer.sql
#     :93   WITH batch AS ( … FOR UPDATE SKIP LOCKED )
#     :102  DELETE FROM public.request_logs_hot … RETURNING …
#     :130  INSERT INTO public.request_logs ( … )
#     :181  FROM moved_rows;      ← 同一语句，原子
#     :77-92  2026-08-25 事故注释（旧实现的静默丢数据形态）

# 3) 695 self-heal 是改标志位，不是搬行
#   同文件 :54-67  UPDATE public.request_logs_hot h SET is_final_success = FALSE …

# 4) 母表直写面普查
#   Go：唯一非测试命中是 bg/lite_retention_worker.go:107（SQLite 形态，partguard 已排除）
#   SQL：全部是同一个 promote 函数体的迁移版本 + baseline dump + test fixture

# 5) 守卫全量（= make guards）
go test ./internal/rowsguard/ ./internal/errdiscard/ ./internal/dbrows/ ./internal/jsoncol/ \
        ./internal/paramguard/ ./internal/sqlguard/ ./internal/sqlreadguard/ \
        ./internal/metricguard/ ./internal/partguard/ ./internal/routeguard/ -count=1 -timeout=120s
#   ok ×10, 0 FAIL
```

**诚实边界（逐条）**

- **本轮零代码改动**（只读核实 + 登记）。`git status` 除文档外无变更。
- **未起真进程、未连 PG、未执行任何 SQL**。§一 的四步是**静态阅读**结论，不是运行时观测。
- 三角化脚本是**一次性外部脚本**（跑在 `D:\temp`），已删除，**不在仓库内**；它的两处缺陷写在 §三，请勿引用其数值当作判定。
- `go test ./...` 全量未跑；跑的是全部 10 个守卫包。
- 本机 `core.hooksPath` 未设置 ⇒ 本会话历次推送都未经 pre-push 门（沿用 199-202 号登记）。

---

## 七、下一轮顺位

1. **还 `DEBT(R47)`**：按 §四 的五步判据，从**字面量 + 有 `ts` 谓词**的读面开始（如 `admin/probe_history.go`、`admin/session_sanitize_matches.go` 这类单查询、单别名、谓词可见的），每还一条跑三道门。
2. **待核实：语义重复**（同 `request_id`/`client_request_id` 的两行跨腿）—— 需要真库或请求链路证据。
3. **待核实：installer 内嵌迁移**是否往母表灌行。
4. 待裁决 **82**（已扩大一条）→ **81** → **79** → **80**。

---

## 八、playbook 新增

### §107 「需要真库验证」是**结论**，不是**前置条件** —— 先问这个验证在证明什么

202 号把 21 条债登记为「需真库验证成本口径」。本轮回头验那个前提，发现它**过强**：

- `976b871ce` 的实测是**一次窗口的数字**（150.15 美元两表一致）；
- 而**机制**（单条数据修改 CTE + 无 `ON CONFLICT` + 母表只由 promote 收行）**在任何窗口都成立**，并且**解释了那个数字为什么成立**。
- ⇒ 机制证明比单窗口数字**更强**，不是更弱。

**How to apply**
- 登记「需真库/需外部证据」时，**先问一句：这个验证在证明什么？**
  - 若它证明的是**机制**（会不会双计、会不会丢、顺序对不对）⇒ 先尝试**静态**证明，成本低一个数量级。
  - 若它证明的是**量级/性能/分布**（多快、多少行、什么占比）⇒ 真库不可替代，登记成立。
- ⇒ **别把「我这边没有真库」直接写成前置条件** —— 那只是「我还没试过能不能静态证」。本轮就是在没真库的情况下把一条 P2 降级成了「假阻塞」。

### §108 判断「视图/合并是否会重复计数」要从**写入侧的不变式**入手，不是从读取侧数一遍

- 读侧看到的 `UNION ALL` 只是**表象**；决定会不会双计的是**写入侧有没有「同一条逻辑记录同时存在于两个源」的可能**。
- 四步顺序（可复用）：① 目标表的**全部**写入路径是否只有一条？② 那条路径的删与插是否**原子**？③ 冲突时是 **fail-safe（回滚）还是 fail-duplicate/fail-lose**？④ 有没有「反向搬迁」路径？
- ⚠️ 第 ③ 步最容易漏：`ON CONFLICT DO NOTHING` 与**裸 INSERT** 在同一 CTE 里，差别是**静默丢数据**与**安全重试**。
