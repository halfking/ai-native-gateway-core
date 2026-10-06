# bodies 读方的取源路径 —— 13 个此刻读的是 v1，13 个无开关可翻

> 生成时间：2026-10-06　起点提交：`8b76814ec`（= `origin/main`）
> 门：`admin/v1_bodies_read_path_test.go`（3 道，常驻）
> ⚠ **本文修正 `2026-10-06-v1-stopwrite-precondition.md` 的第二条结论**

---

## 零、一句话版

`TestV1BodiesReadersAreAssessed` 那道红门说「db 包没有 bodies 源 helper，先补上再改指」。
**这段处置信息已过期两处**；按它做会得到一个假的「已迁移」结论。

实测 27 个 bodies 读方的取源路径：

| 路径 | 数量 | 停写后会怎样 |
|---|---:|---|
| **只经切换层** `db.SessionBodiesSourceSQL()` | **13** | **⚠ 开关未设 ⇒ 此刻读的就是 v1** ⇒ 停写后读到**冻结**的 v1 bodies，不报错 |
| **只字面量**指名 v1 bodies 关系 | **13** | 无开关可翻，**必须改 SQL** |
| 混合（切换层 + 活代码里指名 v1） | 1 | `db/request_logs_view_schema.go`（切换层自己，两臂都在） |

---

## 一、红门给的处置方向，两处都过期了

红门的报错文本（`TestV1BodiesReadersAreAssessed`）写着：

> 处置方向：会话族有 session_bodies 表…但 **db 包没有 bodies 源的 SQL helper** ——
> turns 侧有 `SessionFamilyTurnsSourceSQL` / `SessionFamilyTurnsForSessionSQL`，bodies 侧没有。
> ⇒ **先补那个 helper**，再逐个把 `LEFT JOIN request_logs_bodies*` 改指过去。

| 门说的 | 实际 | 后果 |
|---|---|---|
| bodies 源 helper 不存在，**先补上** | `db.SessionFamilyBodiesSourceSQL()`（会话侧）与 `db.SessionBodiesSourceSQL()`（切换层）**都在**，`db/request_logs_view_schema.go:1028` / `:1081`，§9.230 起就有 | 照着做会去补一个已经存在的东西 |
| 用切换层 `SessionBodiesSourceSQL()` **改指** | 它的**默认支是 v1** | ★ **照着做会得到「接了切换层但仍在读 v1」** |

★ 第二条是真正咬人的那条。补 helper 那条顶多多花时间；
**「改指」那条会产出一个看起来做完了、实际上一步没走的迁移。**

---

## 二、★ 核心事实：开关的默认臂是 v1，而那个键从未被设过

```go
// db/request_logs_view_schema.go:1081
func SessionBodiesSourceSQL() string {
    if settings.GetPlatformBool(SessionBodiesNativeReadSetting, false) {
        return SessionFamilyBodiesSourceSQL()            // 会话侧
    }
    return "request_logs_bodies_with_current_month"      // ★ v1（默认）
}
```

`SessionBodiesNativeReadSetting` = `storage.session_bodies_native_read`。

**本地真库 `settings_kv` 实测**（门 `TestBodiesNativeReadKeyInRealDB` 跑出来的原文）：

| 键 | 在表里？ | 值 |
|---|---|---|
| `storage.session_turns_bodies_enabled` | ✅ 在 | **true** |
| `storage.admin_logs_native_turns_read` | ✅ 在 | **true** |
| `storage.request_logs_write_enabled` | ❌ 不在 | （默认 true） |
| ★ **`storage.session_bodies_native_read`** | ❌ **不在** | — |

★ **这张表比「一个键没设」更有信息**：两个**同类**的读端切换开关
（turns 的、会话日志列表的）**都被设成了 true**，唯独 bodies 读端那个没被设。
⇒ 它不是「没人管过 bodies」，而是**bodies 是唯一漏掉的那一个**。

⇒ `GetPlatformBool(key, false)` 恒取 **false** ⇒ **13 个只经切换层的 bodies 读方
此刻读的就是 v1。**

⚠ 「接了切换层」只说明**有了改指的位置**，不说明**已经改指**。

★ **更正归属**：上面那句「默认臂 = v1 ⇒ 后果不变」**不是本轮的发现**。
既有登记表 `admin/v1_bodies_reader_shapes_test.go` 的 `shapeAlreadySwitched` 档
理由里**逐字写着**：

> §9.230 已迁。bodies 腿经 `db.SessionBodiesSourceSQL()`，**默认臂 = v1** ⇒
> 停写/退役后果不变。

⇒ 仓库早就知道。本轮真正独有的只有两件：

| | 既有登记表 | 本轮 |
|---|---|---|
| 知道「默认臂 = v1」 | ✅（逐字登记） | 重复 |
| 那个键在**真库 `settings_kv` 里的实际值** | ❌ 它只读代码 | ★ **独有** |
| 13 个**没有**开关可翻、必须改 SQL 的字面量读方 | 有（按迁移形状分档） | ★ **本门按「有无开关」重新切一刀，与它交叉核对** |

⇒ 所以本门不摆成第二份真相源，而是**与既有表对质**：
`TestBodiesReadPathAgreesWithTheShapeRegistry` 断言「机械算出只经切换层的
必须全在既有表的 `shapeAlreadySwitched` 里」，反向也把切缝钉死。

---

## 三、三条路径的成员（27 个，与 `v1BodiesReaders` 总体对齐）

### A. 只经切换层（13）—— 翻开关即可，但**现在没翻**

`admin/auto_title_generator.go` · `admin/compression_stats.go` · `admin/logs_summary.go` ·
`admin/memora_handlers.go` · `admin/no_topic_session.go` · `admin/session_compare.go` ·
`admin/session_export.go` · `admin/session_sanitize_matches.go` · ★ `admin/session_title.go` ·
`bg/passive_probe_listener.go` · `domains/sessionforensics/export.go` ·
`domains/sessionsummary/summarizer.go` · `domains/sessionsummary/system_prompt_prefix.go`

★ `admin/session_title.go` 值得单说：它的 `request_logs_bodies_with_current_month`
只出现在**一处 SQL 块注释**里 ——
`LEFT JOIN `+dbpkg.SessionBodiesSourceSQL()+` rb /* request_logs_bodies_with_current_month rb */`
（`admin/session_title.go:192`）。**有人在那里标注了这个调用的默认臂。**
剥掉 SQL 注释后它不指名任何 v1 bodies 关系，所以它是 `switch_only`。

### B. 只字面量（13）—— 无开关可翻，必须改 SQL

`admin/body_resolver.go` · `admin/compression_sessions.go` · `admin/data_lifecycle.go` ·
`admin/data_lifecycle_blobs.go` · `admin/logs.go` · `admin/quality_correlations.go` ·
`admin/session_bodies_batch.go` · `admin/unified_detail.go` ·
`cmd/compression-bench/main.go` · `cmd/scenario_driver/main.go` ·
`cmd/tools/backfill_session_bodies/main.go` · `cmd/tools/validate_sessions_v2/loader.go` ·
`domains/hooks/goal/history_store.go`

### C. 混合（1）

`db/request_logs_view_schema.go` —— 切换层自己；
`return "request_logs_bodies_with_current_month"` 是**活代码**，`SessionFamilyBodiesSourceSQL()` 同文件。

---

## 四、⚠ 对 §9.263 结论的修正

`docs/audit/2026-10-06-v1-stopwrite-precondition.md` 第二节写着：

> ⇒ 每个窗口 bodies 缺口 **⊆** 主表腿缺口；**bodies 独有缺口 = 0**。
> ⇒ **bodies 独有缺口** 0 ⇒ **27 个 bodies 读方与 76 个「必须迁」读方共用同一条前置，
> 不需要再加一道门。**

★ **前半句在数据上成立，后半句在代码路径上不成立。**

| 层面 | 结论 |
|---|---|
| **数据** | bodies 缺口 ⊆ 主表腿缺口（5 个窗口）⇒ 没有独立的**数据**缺口要补 |
| **代码路径** | 13 个读方经切换层而**开关未设** ⇒ 它们**不会自动**跟着走会话侧；另 13 个**无开关可翻** |

⇒ 「共用一条前置」漏了 **P1**。P1 是一条**代码/配置**前置，与数据缺口无关：

> **P1（新增）**：停写前必须让 bodies 读端真正走会话侧 ——
> 13 个 switch-only 读方需要 `storage.session_bodies_native_read=true`
> （或改成直接调 `db.SessionFamilyBodiesSourceSQL()`），
> 13 个 literal-only 读方必须改 SQL。**只做数据回填不做 P1，停写后这 26 个
> 读方读到的是冻结的 v1 或直接报错，而多数调用点写 `COALESCE(rb.request_body,'{}')`
> ⇒ 不报错、正文为空。**

---

## 五、门（5 道，常驻）

| 门 | 断言什么 | 变异检验 |
|---|---|---|
| `TestV1BodiesReadPathsArePinned` | 三条路径**逐个点名**、互斥、穷尽 27；**从源码树重算**后双向比对 | 删一个成员 / 挪一个成员 → 红 |
| `TestBodiesReadPathClassifierSeparatesTheTwoHelpers` | 分类器分得清两个 helper | 退回裸 `Contains` → 红 |
| `TestBodiesNativeReadKeyInRealDB` | 键**能读到**（读不到判红）+ 由值推出结论文字，两个分支都自洽 | —（**有意不断言配置值**：设成 true 是目标，不是缺陷） |
| `TestBodiesReadPathAgreesWithTheShapeRegistry` | 与既有 `v1BodiesReaderShapes` **双向对质**，钉住切缝 | — |
| ★ `TestBodiesSwitchPairIsPinnedAndPaired` | **P1 那一对开关逐个点名**（读端/写端各一、键名正确、两侧源码都带 **false 兜底**）—— 防止「只翻读端」重新变成一个看起来成立的选项 | 删写端 ⇒ 红；写端键换成别的开关 ⇒ 红；兜底 false→true ⇒ 红 |

第 5 道是第七节那个生产实测出来之后才补的（**新门进代码却漏进本表**，
是下一轮现核「门名清单 vs 文档声明」时数出来的：实际 16 道，本节当时仍写 4 道）。

**7 个变异全部按预期变红**（前 4 个来自上面的三档分类，后 3 个来自成对开关那道门）。
★ 变异期间还抓到我两处自己的问题：
一处是**归属错误**——我一度把「默认臂 = v1」写成自己的发现，而既有登记表早已逐字登记；
另一处是**变异脚本自己失败**：删写端那一发按手算行号删，连续两次 AssertionError，
而失败时 `go test` 打的是 **ok**——那看起来就是「删掉写端门仍然绿」。
⇒ 脚本有 stderr 而门那行是绿，就说明没变异；变异要按**内容定位**而不是数行号。

### ⚠ 我自己写了一道重复的门，然后删掉了

第一版里有 `TestBodiesSwitchDefaultArmIsV1`，断言切换层默认臂是 v1。
写完才发现 `admin/session_bodies_source_test.go` 的 **`TestBodiesSwitchDefaultsToV1`
早已断言同一件事**，而且它的注释里带着一条我算不出来的事实：

> ⚠ 默认开启会让生产 2026-09-30 的会话导出正文全变 `{}`（1,113 条 turn，§9.229.2 实测），
> 而接口仍 200。

⇒ 两道同义的门有一个坏形态：**改对的人只需改一道，另一道会红**，
后来的人会误以为判据自相矛盾。已删掉我那道，改为**引用**既有覆盖。

### ⚠ RE2 不支持 lookbehind（同一轮第二个 RE2 坑）

分类器第一版用 `(?<!Family)\b(?:dbpkg\.|db\.)?SessionBodiesSourceSQL\s*\(` 想
「保险地」排除 `SessionFamilyBodiesSourceSQL`，结果
`error parsing regexp: invalid named capture` ⇒ **`init()` 直接 panic，整包 admin 门全崩**。
（同一轮我已经在 RE2 上踩过不支持 lookahead。）

★ 而这个负向断言**本来就不需要**：`SessionBodiesSourceSQL` **不是**
`SessionFamilyBodiesSourceSQL` 的子串 —— 前者 `Session` 之后直接是 `Bodies`，
后者是 `FamilyBodies`。所以裸 `strings.Contains` 本来就分得开。
门里配了对照把这件事钉住。

---

## 六、限度与未验项

- ~~**只读了本地真库的 `settings_kv`**，生产未查~~ ⇒ **已于 2026-10-06 23:55–23:58
  补上生产 252 只读实测，见第七节。** 结论：该键在生产上**整行不存在** ⇒ A 档 13 个
  在生产上**确实读 v1**，P1 在生产侧**未满足**。原句担心的「若生产已设为 true」分支已排除。
  ⚠ 但**补出了更重的一条**：写端 `storage.session_turns_bodies_enabled` 在生产上
  **同样不存在** ⇒ **P1 是「读端 + 写端成对翻」**，不是单键 —— 详见第七节的新发现。
- **「曾经被设过又删掉」仍然查不出，但机制说错了**：原句归因于「`updated_by` /
  `prev_value` 审计字段是空的」，那只是**本地**实况；生产上这些列**存在且在用**
  （14 键有 `prev_value`）。**真正的原因是 `prev_value` 只记「已存在键的改值」，
  删除不留痕** ⇒ 「从未创建过」与「被删过」在两个库上**都没有任何痕迹**。
  （§9.238.3 已记过这张表审计能力名义上存在但字段全空）⇒ 查不出历史。
- **没有改任何读方、没有翻任何开关。**
- `admin/session_title.go` 归为 `switch_only` 依赖「剥掉 SQL 块注释」这一步；
  若有人把那段标注从注释改成活代码，这条会红。

---

## 七、★ 生产 252 实况（2026-10-06 23:55–23:58，**只读 SELECT**）

⚠ 此前本文写「**生产 252 上那个键是什么值未查**」。**目标约束原文是「生产 252 只读，
任何生产变更未经属主批准不执行」——只读查询是允许的**，是我把约束读窄了。现已查过。

查询对象：生产 `pg-252-pg17` 容器（`kx-citus-pg17`）里的 `llm_gateway` 库 `settings_kv`。
`value` 是 **jsonb**（用 `value::text` 取值，`coalesce(value,'<NULL>')` 会因 `<` 非法 json 报错）。
`settings_kv` 共 **42 个键**，其中 `storage.` 前缀**只有 2 个**：

| 键 | 生产值 | `updated_at` |
|---|---|---|
| `storage.request_logs_write_enabled` | **true** | 2026-10-06 04:29 |
| `storage.session_final_full_enabled` | false | 2026-10-02 14:27 |
| ★ **`storage.session_bodies_native_read`** | **整行不存在** | — |
| `sessions_v2.enabled` | true | 2026-07-21 16:50 |
| `sessions_v2.shadow_write` | true | 2026-07-21 16:50 |

### ⇒ P1 的关键前置**已核实**（不是待核实）

`storage.session_bodies_native_read` 在生产上**确定不存在** ⇒
`GetPlatformBool(SessionBodiesNativeReadSetting, false)`（`db/request_logs_view_schema.go:1082`）
恒取 **false** ⇒ **13 个只经切换层的 bodies 读方在生产上此刻读的就是 v1。**
我此前提的「若生产已设为 true 则 P1 在生产侧可能已满足」这个分支**已被排除**。

★ 另两条生产事实，对停写前置直接相关：
- `storage.request_logs_write_enabled = **true**`（今天 04:29 刚更新过）⇒ **v1 仍在生产上写入**。
- `sessions_v2.enabled = true` 且 `shadow_write = true` ⇒ v2 处于**影子写**模式，不是切换。

### ⚠ 更正一：「bodies 是唯一漏掉的那一个」**只在本地库成立**

第二节那张表（同类的 `storage.session_turns_bodies_enabled` / `storage.admin_logs_native_turns_read`
「✅ 在 = true」）是**本地 `llm-gateway-pg`** 的实况。**生产上这两个键也都不存在。**
⇒ 不能拿本地那两行论证「bodies 是被刻意漏掉的唯一一项」。

⚠ 但**不等于生产有第二个缺陷**：`storage.admin_logs_native_turns_read` 默认 false 是
**有意设计**，`admin/logs_turns_source.go:40–55` 写着「订正后的结论」——
原生源不是全量日志视图的等价替代（真库实测 2,321,464 个 request_id 里 641,452 个、
**27.6%** 在原生源查不到：无会话头流量、in_progress 占位、摘要生成器回环），
按 `request_id` 反查或全量时间窗聚合的读**禁止**迁，判据是**谓词形态**，
守卫在 `admin/session_view_dependency_risk_test.go`。**它读 v1 是对的。**

### ★ 新发现：P1 是**一对**开关，不是**一个** —— 只翻读端会读到空正文

`storage.session_turns_bodies_enabled` 控制的是 **writer 侧**（`domains/session/v2/session_writer_v2.go:674`
「S1b 灰度开关①：每轮正文同步进 session_turns」）：

```go
if settings.GetPlatformBool("storage.session_turns_bodies_enabled", false) {
    turnRec.RequestDeltaJSON  = ...   // req.RequestDelta
    turnRec.ResponseDeltaJSON = ...   // req.ResponseBody
}
```

该键在生产上**也不存在** ⇒ 恒取 false ⇒ **`RequestDeltaJSON` / `ResponseDeltaJSON`
在生产上根本没在写进 `session_turns` 行。**

⇒ ★ **只把 `storage.session_bodies_native_read` 翻成 true，读端会切到会话侧，
而会话侧本身没有正文** —— 读到的就是空。
这正是 §9.229.2 记录的「生产 2026-09-30 的会话导出正文全变 `{}`（1,113 条 turn）而接口仍 200」
那个失败形态。⇒ **P1 必须修正为「读端 + 写端成对翻」**，属主拍板时按这一对来定，
不要按单键定。

### ⚠ 更正二：「审计字段是空的、查不出历史」——机制说错了

原文说查不出「是否曾被设过又删掉」，因为 `updated_by` / `prev_value` 字段全空。
**那是本地库的实况**。生产上这些列**存在且在用**：14 个键有非空 `prev_value`，
3 个有 `updated_by`（`session-compression-default-on-245`、`handoff-hot-split-20260818` 等）。

**但结论不变，机制不同**：`prev_value` 只记录**已存在键被改值**时的旧值，
**删除不留痕**。⇒ 无论本地还是生产，「某个键**从未创建过**」与「**被删过**」
在这张表里**都查不出任何痕迹**。要回答这个问题需要 `settings_kv` 的删除审计（当前没有）。

### ⚠ 更正三：一处**未标环境**的注释

`admin/logs_turns_source.go:53` 写着「S4 停写（`storage.request_logs_write_enabled=false`）的
活跃漏写阻断**已解除**」。而 §9.263 在**本地库**实测 S4 退出条件**实质不满足**
（168h 漏写 463/25,367 = 1.82%），且**生产上那个开关此刻是 `true`（还没停写）**。
⇒ 三者**不必然矛盾**（阻断已解除 = 允许停写 ≠ 已停写；本地与生产是两个环境），
但**那句注释没有标明它说的是哪个环境**，后来者极易把它当成本地既定事实。
本轮**不改它**（不在本轮范围，且改动需要知道作者的原始依据），**留给属主**。
