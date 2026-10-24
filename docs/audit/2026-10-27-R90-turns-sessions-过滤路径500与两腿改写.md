# R90 · `turns/sessions` 四条过滤路径的 500@30s：两腿 EXISTS 改写 + 口径等价性证明

> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`（P11 线，段号 `P11-R90`）
> 起点：`origin/main = 6c492bfed`（R89），生产 build **2840**（`2.5.8-3e47b1fe-20261024-2840`）@ 154:8782
> ★ 本轮**只改读端 SQL 形态**，不改任何数据、不改任何列的取值口径；
> ★ 口径等价性**不是靠「形状像」保证的**，而是靠**两条结构前提 + 生产规模逐条比对**保证。

---

## 0. 一句话结论

`GET /api/admin/turns/sessions` 带 `?api_key_id=` / `?model=` / `?provider=` / `?status_code=`
时**生产恒 500@30s**（角色级 `statement_timeout=30s` ⇒ SQLSTATE 57014）⇒
根因是这四条过滤**读的是带反连接的视图**，而 api_key 那条还把 **113 列全量 source
放进关联子查询内部**（外层每行物化一遍）⇒
改成 **hot ∪ 父表两腿 EXISTS**（`db.SessionFamilyTurnsSessionExistsSQL`）⇒
**生产规模逐条比对 41,122 个候选会话、口径不一致 0 条**。

---

## 1. 症状（154 直连实测，build 2840，**修复前**）

```
query                                                          codes   times(s)
?limit=20                                                   200 200  0.332 0.040
?limit=20&api_key_id=105                                    500 500  30.009 30.007
?limit=20&model=deepseek-ai%2Fdeepseek-coder-6.7b-instruct    500 500  30.003 56.647
```

⇒ 不带过滤是 **0.04 s**；带任一过滤 = **500@30 s**（`model` 那条有一次 56.6 s，
★ 即 30 s 是**语句**超时、端点还要再花时间收尾）。

★ 这条端点在 R85 轮就被登记为「带过滤路径的已知边界」：
`admin/r85_enrich_source_pushdown_test.go:152` 原文写着
「`?api_key_id=` 过滤用的 EXISTS 子句 …… 那条是**带过滤**的路径（谓词按外层行相关），
不在本次修复范围 ⇒ 该残留路径**如实登记**在审计档『已知边界』里。」
⇒ ★★ **本轮就是那一项的后续**。

---

## 2. 根因（252 生产只读实测，2026-10-24）

### 2.1 根因①：视图的 hot 腿带反连接

```sql
CREATE VIEW public.session_turns_with_current_month AS
  SELECT … FROM public.session_turns_hot hot
  WHERE NOT EXISTS (SELECT 1 FROM public.session_turns archived
                    WHERE archived.tenant_id = hot.tenant_id
                      AND archived.request_id = hot.request_id)
  UNION ALL
  SELECT … FROM public.session_turns;
```

⇒ 外层**每一行**都要在 hot 上跑一次 Nested Loop Anti Join。
⇒ EXPLAIN ANALYZE：`?model=` 生产形态 **36,415 ms**。

### 2.2 根因②：api_key 那条还 JOIN 了 113 列全量 source

```sql
EXISTS (SELECT 1 FROM public.session_turns_with_current_month ft
        JOIN <db.SessionFamilyTurnsSourceSQL()> rl ON rl.request_id = ft.request_id
        WHERE ft.session_id = s.session_id AND ft.tenant_id = s.tenant_id
          AND rl.api_key_id = $n)
```

`rl` = **hot ∪ 父表全部历史**（≈1.1 M 行 × 113 列）的派生表，而它在**关联子查询内部**
⇒ 外层每一行都把它**物化一遍** ⇒ EXPLAIN ANALYZE **> 90 s 未跑完**（被 30 s 超时杀掉）。
★ 单变量 A/B（252）：谓词推进 source 内部（R85 手法）= **52,854 ms** ⇒ **同样无效**。

---

## 3. 修法

### 3.1 新增单一真值源（`db/request_logs_view_schema.go`）

```go
// SessionFamilyAPIKeyIDExpr —— api_key_id 的唯一规范表达式（text 列 → bigint）
const SessionFamilyAPIKeyIDExpr = "(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END)"

// SessionFamilyTurnsSessionExistsSQL —— 「外层会话 s 是否含满足 colPred 的轮次」
// 的两腿 EXISTS（hot ∪ 父表）
func SessionFamilyTurnsSessionExistsSQL(colPred string) string
```

★ `SessionFamilyAPIKeyIDExpr` 同时**替换掉了投影表里那一项字面量**
⇒ 「展示的 api_key」与「过滤用的 api_key」从此是**同一个常量**。

### 3.2 四条过滤改写（`admin/turns_sessions.go`）

| 过滤 | 改前 | 改后 |
|---|---|---|
| `model` | `EXISTS (… FROM session_turns_with_current_month ft … ft.model = $n)` | `db.SessionFamilyTurnsSessionExistsSQL("t.model = $n")` |
| `provider` | 同上 | `db.SessionFamilyTurnsSessionExistsSQL("t.provider = $n")` |
| `status_code` | 同上 | `db.SessionFamilyTurnsSessionExistsSQL("t.status_code = $n")` |
| `api_key_id` | `EXISTS (视图 ft JOIN 113 列 source rl … rl.api_key_id = $n)` | `db.SessionFamilyTurnsSessionExistsSQL(db.SessionFamilyAPIKeyIDExpr + " = $n")` |

★ **保留的语义**：`api_key_id` 参数非法（`0` / 负数 / 非数字）⇒ **整个过滤子句被跳过**
（等价于不过滤）—— 与改动前逐字一致（门 `TestR90_InvalidAPIKeyIDSkipsFilter` 钉住）。

★ **这不是新发明**：同仓 `admin/turns_filter_options.go` 的 `turnMergedSource`
早在 2026-10-18 就用了同一手法（实测：视图 **44,564 ms** ⇒ 两腿 **2,048 / 2,814 ms**）。
本轮只是把那条**已验证**的形态收敛成**单一真值源**，避免第三次各写一份。

---

## 4. ★★ 口径等价性：靠前提与逐条比对，不靠「形状像」

### 4.1 两条结构前提（252 生产实测，2026-10-24）

| 前提 | 内容 | 实测 |
|---|---|---|
| **(a)** | `request_id → (session_id, tenant_id)` 是**函数依赖**（全库 hot ∪ 父表，**跨租户**） | **0 例外** |
| **(b)** | hot 与父表的**同一 request_id** 在被比较的列上取值一致 | 配对行 **0**（⇒ 视图的 anti-join 剔除 0 行 ⇒ 视图 ≡ hot ∪ 父表） |

★ **(a) 为什么必要**：旧形态用 `rl.request_id = ft.request_id` 关联（**不带 tenant_id**），
新形态按 `(session_id, tenant_id)` 匹配 ⇒ 没有 (a) 就不等价。
★ ★ 而新形态同时**消掉了旧形态的一个潜在跨租户串味**（旧 JOIN 不带 tenant_id）。

★ **(b) 为什么必要**：视图的 anti-join 会把 hot 里的重复行剔掉 ⇒ 若两侧取值不同，
「视图看到的集合」与「两腿看到的集合」就不同。
★ 而 (b) 是**结构性**的：`promote_session_turns_hot_to_partition` 是
「`DELETE FROM session_turns_hot` … `INSERT INTO public.session_turns` …」的**移动**
⇒ 同一 request_id 不会同时存在于两边。

### 4.2 生产规模逐条比对（252，2026-10-24）

旧形态用 **113 列 source**（api_key 族）与 **视图**（model 族），与新形态逐条比对：

| 族 | 样本 | 候选会话数 | **口径不一致** |
|---|---|---|---|
| api_key | `105` | 28,263 | **0** |
| api_key | `143` | 9,692 | **0** |
| api_key | `2` | 845 | **0** |
| model | `abab5.5-chat` | 405 | **0** |
| model | `abab6.5s-chat` | 287 | **0** |
| model | `ai-synthetic-video-detector` | 1,630 | **0** |

⇒ 合计 **41,122 个候选会话**，old/new 判定**逐条相等**，`old_true == new_true`。
★ api_key 族的生产核对用了一个**可证等价**的「最小 source」（只投影
`request_id` + `api_key_id`）：113 列投影里的 `api_key_id` **只**由 `t.api_key_id` 决定
（`db/request_logs_view_schema.go:159`）⇒ 换成最小投影不改变谓词取值，但避免 1.1 M × 113 列的物化。
★ 仓内那道真库门（`admin/turns_sessions_filter_pushdown_test.go`）用的是**真 113 列 source**，
在本地真库上逐条比对了 **648 个候选会话、不一致 0 条**。

---

## 5. 门禁

### 5.1 新增 `admin/turns_sessions_filter_pushdown_test.go`（7 条）

| # | 门 | 钉的是什么 |
|---|---|---|
| 1 | `SessionExistsSQLShape` | helper 渲染形态：两腿、每腿带 session_id+tenant_id、整体带括号、**不得**引用视图 / 113 列 source、**不得**含 `%` |
| 2 | `ExistsShapeDetectorHasTeeth` | 5 份合成坏样本必须被判红 |
| 3 | `FilterUsesDBAPIKeyExpr` | 过滤侧必须用 `db.SessionFamilyAPIKeyIDExpr` |
| 4 | `InvalidAPIKeyIDSkipsFilter` | 非法/零值 `api_key_id` ⇒ 子句被跳过；合法值绑定 `int64` |
| 5 | `FilterBranchHasNoViewOrFullSource` | 源码级：过滤构造块不得引用视图 / 全量 source（★ **剥注释**后判） |
| 6 | ★★ `RealDB_FilterPremises` | **真库**：前提 (a) 与 (b) 必须成立（否则口径等价性不再成立 ⇒ 门红） |
| 7 | ★★ `RealDB_FilterEquivalence` | **真库**：同一 REPEATABLE READ 快照内逐条比对新旧形态；★ **行数守卫**：无候选 ⇒ 显式 SKIP 并写明理由（★ 空集不是「等价通过」） |

### 5.2 新增 `db/request_logs_view_schema_apikey_expr_test.go`（1 条）

`TestR90_APIKeyExprMatchesProjection`：断言**投影里 api_key_id 那一项**
与 `db.SessionFamilyAPIKeyIDExpr` **逐字相同**。
★ 为什么必须住在 db 包：投影表 `projectionExprsV2` **未导出** ⇒ 只有同包测试能读到它。

### 5.3 更新既有门（逐条写清理由）

| 门 | 原判据 | 改后 | 为什么 |
|---|---|---|---|
| `admin/turns_sessions_test.go` `TurnLevelFiltersSession` | `ft.model = $2` + 「EXISTS on 视图」 | `t.model = $2` + **两腿 EXISTS** + **反向断言「不得引用视图」** | 改的是实现形态；反向断言让「视图被引回来」当场红 |
| 同上 `APIKeyAndStatus` | `rl.api_key_id = $2` | CASE 表达式 = `$2` + **反向断言「不得用 113 列 source」** | 同上 |
| 同上 `SQLHasNoFormatArtifacts` | `rl.api_key_id = $2` | CASE 表达式 = `$2` | 绑定位置的判据随谓词形态一起改（`%!` 残渣检测原样保留） |
| `admin/session_view_dependency_risk_test.go` `nativeSessionReaders` | `turns_sessions.go` → `SessionFamilyTurnsSourceSQL` | → `SessionFamilyTurnsForSessionsSQL` | R90 后该文件**不再**引用 113 列 source ⇒ 旧 marker 失效；换成的 marker 是本文件**真正**的 class-A 原生读（enrich 查询，R85 推谓词版） |

### 5.4 ★★ 顺带修掉一处「被掩盖的 class-A 违规」

`nativeSessionReaders` 的判据是**整文件 `strings.Contains`** ⇒ 它只证明
「这个文件**某处**有 class-A 原生读」，**不**证明「这个文件里**每一个**
session-scoped 查询都用了原生读」。

★ 2026-10-24 实测发现：`admin/turns_sessions.go` 的 `loadTurnsForSessions`
（谓词 `t.session_id = ANY($1)` ⇒ **session-scoped** ⇒ class A）
**确实在读 `session_turns_with_current_month` 视图** ⇒ 按审计 §5.5.5 的字面要求，
这是**违规**；它之所以一直没被抓到，是**同一文件里别处**（当时是 api_key 分支的
`SessionFamilyTurnsSourceSQL`）让整文件 `Contains` 成立。
⇒ ★★ 这正是 LESSONS §353.3 点名的反模式（「断言对象必须是可执行体，不是文件里
出现过的文本」）在**活的门**上的实例。

★ **为什么不能简单改掉（豁免理由）**：该查询要的是**去重后**的轮次集合 ⇒
视图的 anti-join 是**承重**的 ⇒ 裸 hot ∪ 父表会把已 promote 的轮次**数两遍**
⇒ 「直接换原生源」会引入重复轮次（那是口径变更，比慢更糟）。
★ 实测该腿 **< 20 ms** ⇒ 不是瓶颈。
⇒ ⇒ 新增 **`nativeSessionReaderBlockExemptions`（逐块豁免 + 理由）** 及其
**可执行断言** `TestSessionScopedBlockExemptionsArePinned`（豁免块必须仍然读视图、
不得改读单腿源）+ 阳性对照 `…TableHasTeeth`。

---

## 6. 诚实边界

1. ★ **本地库比生产小 3 个数量级**（本地 turns 2,128 行 / hot 32 行）⇒
   本地真库门证明的是**谓词等价**（在真实数据上逐条比对），**不**证明生产计划形态。
2. ★ **生产计划形态未在本轮实测**（改写后的两腿 EXISTS 的 EXPLAIN 在本轮没有单独跑）。
   ⇒ 本轮的「更快」结论由**端点级 A/B**给出（见 §8），不是由 EXPLAIN 给出。
3. ★ **前提 (a)/(b) 是数据事实 + 结构性保证的组合**：(b) 有 promote 的 MOVE 语义兜底，
   (a) 只有实测兜底（仓内真库门会持续盯它）。
4. ★ **本轮只动了这四条过滤**。`turns_sessions.go` 里另外两处读视图的地方
   （`loadTurnsForSessions`、`unified_detail` 之外的查询）**未动**，理由见 §5.4。
5. ★ **`api_key_id` 的「非法值 ⇒ 跳过过滤」语义保留** ⇒ 因此 `?api_key_id=0`
   仍然返回**未过滤**的全量列表（这是改动前的行为，不是新引入的）。

---

## 7. 待属主裁决（本轮不涉及，但因本轮证据而更清晰）

- ★ **「恒失败且产出恒空的 12 s 趋势腿」**（`data-lifecycle/stats`）：日志实抓
  `data_lifecycle_stats trend failed; growth_trend 已退化为空 budget=12000000000
  error=timeout: context deadline exceeded` ⇒ 根因未变。
- ★ **会话关闭路径 42703**（`HandleSessionRecycle` / `bg/session_lifecycle_worker.go`）：
  4 个恒 0 字段的真因。
- ★ **`work_mem` 提升**（L1 哈希连接溢写，−34%，口径零变更）、生产 VACUUM、
  `filter-options` 覆盖索引、`session-overview` 缓存或 rollup。

---

## 8. 通用式（并入 `LESSONS.md` §354）

1. ★★★★ **口径等价不能靠「形状像」保证** ⇒ 形状门只能证明「改成了什么形态」，
   **口径要靠「前提 + 逐条比对」** ⇒ 本轮把两条结构前提各写成一条**真库门**。
2. ★★★★ **旧形态带 JOIN 条件缺列（缺 tenant_id）时，新形态可能更正确** ⇒
   改写顺带消掉了一个**潜在跨租户串味** ⇒ 登记为收益，不是「口径不变」的副作用。
3. ★★★★ **整文件 `strings.Contains` 的门是「假阳性的门」**：它只证明「某处有」，
   不证明「处处有」⇒ 本轮实测抓到一处**被掩盖的 class-A 违规**
   ⇒ 修法 = **逐块豁免 + 可执行断言**，不是把文件整体豁免。
4. ★★★ **源码断言必须剥注释**：本轮自己的注释里就写着被禁的视图名
   ⇒ 不剥注释时门**恒红**（LESSONS §353.3 的又一次实例）。
5. ★★★ **阳性对照要先证明「尺子」坏了**：本轮两处变异体/门缺陷
   （括号加回去、注释未剥）都是阳性对照当场逼出来的。
6. ★★ **带过滤的端点超时 ⇒ 先看「过滤谓词所在的那个源」的形状**，
   别只看端点总耗时。
