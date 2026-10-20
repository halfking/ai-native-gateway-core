# R80 轮（续四）—— `session_turns_hot` 的**同源判别**：把「靶子不对」从假红改成诚实 SKIP

> 2026-10-21 · llm-gateway-go · 本会话（R80 续三之后）
> 归属：并行会话 R76 轮把「全新安装形态库缺 `session_turns_hot`」这条线**明确划给我**
> （并给了一条关键启发：「这类『库里有没有某个对象』的断言，极易被『自动命名/自动继承』造成的形态差异骗到 ⇒ 建议按**对象存在性**写，不要按名字」）。
> ⇒ 本档是本轮的**权威规格**；handoff 只放指针。

---

## ① 一句话结论

★★★ **「全新安装库缺 `session_turns_hot`」不是缺陷，是一个**时点/链位**现象** ——
`session_turns_hot` **不由基线建**，而由 **installer 启动序列**的
`session_turns_hot_bootstrap.sql` 建 ⇒ ⇒ **基线形态的库里它本来就不存在**，
**启动序列跑完后才在**（实测形态库：`session_turns_hot` relkind=`r` ✔）。

⇒ ⇒ 而 `db::TestFreshInstallRuntimeShape_EnsureChain` 原来把「缺它」**一律判成红**
⇒ ⇒ **在「压根没跑过安装链」的库上会报一条假红**（「安装链没生效」）
⇒ ⇒ ★★ 本轮用**同源产物**把它分成两支：**真缺陷 ⇒ 红** / **靶子不对 ⇒ 诚实 SKIP**。

---

## ② ★★★★ 机制（三条事实，逐条实测）

### ②.1 基线**不建**它，而且基线里**自己写明了**

`sql/schema/01-schema.sql:30450`（末尾，基线的 migration 说明区）：

```
-- Migration 526 historically owns session_turns_hot, its security-invoker view,
-- advisory-lock key, and promotion function.
-- Fresh installer bootstrap supplies their current final-state form through
-- session_turns_hot_bootstrap.sql.
```

⇒ ⇒ **基线形态库里 `public.session_turns_hot` 不存在，是设计，不是缺陷。**

### ②.2 建它的是 **installer 启动序列**的一个文件

- 文件真身：`installer/cmd/llm-gw-installer/embeddata/startup/session_turns_hot_bootstrap.sql`（**741 行**）
  ⚠ **不在** `sql/migrations/startup/` 下（那儿是 495 个编号迁移），它在 **installer 的 embeddata** 里。
- 登记：`sql/schema/installed_startup_migrations.tsv` 第 27 行（序号 **9**）——
  该清单是 `installer/internal/dbinit/runner.go : Runner.StartupFiles` 的**生成物**，
  `installer/internal/dbinit/startup_manifest_test.go` 逐条比对 ⇒ **手改即测试失败**。
- 该文件建的 4 个关键产物（实测 grep）：

| 产物 | 类型 | 基线里有吗（实测） |
|---|---|---|
| `public.session_turns_hot` | 表 | **0 命中** |
| `public.session_turns_with_current_month` | 视图 | **0 命中** |
| `public.promote_session_turns_hot_to_partition` | 函数 | **0 命中** |
| `public.session_turns_advisory_lock_key` | 函数 | **0 命中** |

⇒ ⇒ ★★ **这 4 个都是「启动序列专属标记」** ⇒ ⇒ 可以当**判别式**用。

### ②.3 ⇒ 与 R64 轮的结论**合流**：这正是「基线必须保留 v1 fallback 体」的原因

- `CREATE VIEW` 会**急切校验**它引用的关系 ⇒ 基线时点 `public.session_turns_hot` 还不存在
  ⇒ ⇒ 基线里**不能**写会话族形态 ⇒ ⇒ 基线**必须保留 v1 fallback 体**（R64 已闭合）。
- ⇒ ⇒ ★★ **两条线是同一件事的两个面**：「基线里没有 hot 表」既是「基线必须写 v1 臂」的**原因**，
  也是「形态门不能说『装完就该有它』」的**原因**。

---

## ③ ★★★★ 缺陷本体：形态门把「靶子不对」报成了「产品缺陷」

`db/db_fresh_install_runtime_shape_test.go`（R74 轮，提交 `ea3263c21`）的 ① 段原文：

```go
if !hotExists {
    t.Errorf("public.session_turns_hot 在「装完」时就不存在 ⇒ " +
        "安装链的 session_turns_hot_bootstrap 没生效（它由 installer 的启动迁移链建，" +
        "ensure 链不建它 ⇒ 这里没人替它兜底）")
}
```

⇒ ⇒ ★★ **问题**：这道门的 oracle 是「**全新安装 + 启动一次**」之后的形态，
而它的 **⓪ 锚点自检**只查 4 张**基线**表（`models_canonical` / `credentials` /
`provider_catalog` / `session_turns`）⇒ ⇒ ⇒ **一个「跑过基线、没跑安装链」的库会被 ⓪ 放行**
⇒ ⇒ ⇒ 然后 ① 在它上面报「安装链没生效」⇒ ⇒ ⇒ **量具失效被报成了产品缺陷。**

★★ **这不是假想**：R74 轮实测的 **R69「手工子集库」**正是这一类
（缺 `credentials` / `session_turns_hot` / `schema_migrations` /
`request_logs_with_current_month`，但**有** `session_turns`）⇒ ⇒ 旧写法在它上面会假红。

⇒ ⇒ ★★★ **而且这两种情况单看形态是分不开的**（「缺表」既可能是「装完却没建出来」，
也可能是「这里根本没装」）⇒ ⇒ ⇒ **判据必须引入一个可判别的同源信号** ——
⇒ ⇒ **这才是本轮真正要补的东西**（不是「再加一条断言」）。

---

## ④ ★★★ 修法：**同源判别**（同文件、另一个产物、且不依赖那张表）

★ **判别式 = `public.promote_session_turns_hot_to_partition` 是否存在。**

★ **为什么选函数、而不是同文件建的视图**：
`session_turns_with_current_month` **依赖** `session_turns_hot`
⇒ ⇒ 缺表时它已被 `CASCADE` 带走 ⇒ ⇒ 拿它当判别式会在「真缺陷」那一支**永远判错**。
⇒ ⇒ 函数是 PL/pgSQL，**不随表被 DROP** ⇒ ⇒ 能在「表没了」的状态下存活并作证。

⇒ ⇒ ★★ 两条分支（实测见 ⑤）：

| 同源函数 | 判定 | 处置 |
|---|---|---|
| **在** | 安装链**跑过**却没建出这张表 | ★ **真缺陷 ⇒ 红**（并把原因写清） |
| **不在** | 这个库**根本没跑过安装链** | ★ **靶子不对 ⇒ 诚实 SKIP**（说清缺哪个、为什么这不是产品缺陷、该用哪种靶子） |

⇒ ⇒ ★★ **零覆盖损失**：旧写法在「真缺陷」那一支也是红 ⇒ ⇒ 新写法只是把
「靶子不对」那一支从**假红**改成**诚实 SKIP** ⇒ ⇒ **不可能产生新红**（静态可判，见 ⑥）。

---

## ⑤ ★★★★ 验收：三态实测 + 负控（M5）

靶子 = `scripts/audit/probe-onetime-db-shape.sh` 建的**一次性新装形态库**
（`itgate_r80d_probe`，跑完即回收）；状态单调退化，只建一次库。
门：`go test ./db/ -run '^TestFreshInstallRuntimeShape_EnsureChain$' -count=1 -v`。

| 态 | 库的状态 | 门的实际输出 | 期望 |
|---|---|---|---|
| **(a)** | 完好 | `--- PASS (0.68s)` | 真跑且绿 ✔ |
| **(b)** | `DROP TABLE session_turns_hot CASCADE`（连带 11 个视图/物化视图），**同源函数仍在** | `--- FAIL` ＋ `db_fresh_install_runtime_shape_test.go:236: public.session_turns_hot 在「装完」时就不存在，**但同一个 bootstrap 文件建的 promote 函数在** ⇒ 安装链**跑过**却没建出这张表 ⇒ 真缺陷` | 真缺陷 ⇒ 红 ✔ |
| **(c)** | 再 `DROP FUNCTION promote_session_turns_hot_to_partition` ⇒ 表与函数**都不在** | `--- SKIP (0.05s)` ＋ `…:240: 目标库不是「全新安装」形态：public.session_turns_hot 缺，**且同一个 bootstrap 文件建的 promote 函数也缺** ⇒ 这个库**没跑过安装链**（基线形态里本来就没有这张表 ⇒ sql/schema/01-schema.sql 末尾有说明）⇒ 本门量的是「全新安装形态」，前提不成立 ⇒ 跳过，避免把**靶子不对**报成产品缺陷。` | 靶子不对 ⇒ 诚实 SKIP ✔ |
| **(M5)** | 负控：把同源判别去掉（一律当真缺陷），库停在 (c) | `--- FAIL` ＋ 红在**真缺陷那条腿**（`:237`） | (c) 必须变红 ⇒ 证明判别式**有牙** ✔ |

⇒ ⇒ ★ 还原 md5 逐字一致：`3f6fbb1ea057c387f5148bb0500c62dc`（还原后复跑回到 (c) 的 SKIP ✔）。
⇒ ⇒ ★ 一次性靶子库跑完即回收（`DROP DATABASE … WITH (FORCE)`）⇒ **零残留**。

★★ **附带读数（值得单列）**：在 (b) 态里，除了形态门那条人话红，还紧跟着一条
`db.Open（= 产品启动一次）失败: rebuild request_logs_with_current_month as session-only two-arm body: ERROR: relation "public.session_turns_hot" does not exist (SQLSTATE 42P01)`
⇒ ⇒ ★ **这正是该门当初被排到 `db.Open` **之前**要治的病**（hot 表缺失时只能报出一句看不懂的启动失败）
⇒ ⇒ ⇒ **本轮把「人话」那条又往前推了一步**：现在它还能区分「装完却没建出来」与「压根没装」。

---

## ⑥ ★★ 诚实边界

- ★ **未跑 `db` 包整包**（该包对共享库有破坏性 ⇒ 不能拿形态库当靶子）。
  ⇒ 但 **改动只在 `if !hotExists { … }` 这一个分支内** ⇒ ⇒ 表**在**时代码路径**逐字相同**
  （(a) 态实测 PASS）⇒ ⇒ ⇒ 且该分支**只可能把 FAIL 变成 SKIP**，**不可能**产生新红
  ⇒ ⇒ ⇒ **静态可判**，不需要整包对拍。
- ★ **gofmt**：本文件（`db/db_fresh_install_runtime_shape_test.go`）在 **HEAD 就是 gofmt 脏的**
  （既存 2 个 hunk，都在 doc 注释的缩进续行上）⇒ ⇒ **本轮刻意不重排**（会在共享文件里造出与本轮无关的大 diff）
  ⇒ ⇒ 实测**我的改动新增 0 个 hunk**（HEAD 版与我的版本 `gofmt -d` 都是同 2 个 hunk、同一位置）。
- ★ 只改了**测试面**（1 个 `_test.go`）⇒ **对生产零影响**。
- ⚠ **本档不改「基线必须保留 v1 fallback 体」那条**（R64 轮已闭合）⇒ ⇒ 本轮只是把它的**机制原因**补全了。

---

## ⑦ ★★★ 通用式（方法论沉淀）

- ⇒ ⇒ ★★★★ **⇒ 通用式（① 「缺对象」不是二值判据 —— 它至少有三种含义，必须逐个可判别）**：
  ★ 「这个对象不在」可能是 ① **该建没建**（真缺陷）② **压根没走到那一步**（时点/链位）
  ③ **压根不是这个流程的产物**（靶子不对）。
  ⇒ ⇒ ★ **前两种单看形态分不开** ⇒ ⇒ ⇒ **必须引入「同源信号」**（同一份脚本/同一个文件建出的**另一个**产物）
  ⇒ ⇒ ★ **判别式必须是「缺了它也不会连带消失」的那个** ⇒ ⇒ **函数 > 视图**（视图会被 CASCADE 带走）。
  ⇒ ★ 判别动作：★ **问「这个对象缺失时，还有哪个同源产物能证明『我确实走到那一步了』」**。
- ⇒ ⇒ ★★★★ **⇒ 通用式（② 形态门的 ⓪ 锚点自检，要钉「这一步的产物」，不是「上一步的产物」）**：
  ★ 本轮实测：⓪ 钉的是 4 张**基线**表 ⇒ ⇒ ⇒ 「跑过基线、没跑安装链」的库会被放行
  ⇒ ⇒ ⇒ 后续对**启动序列产物**的断言就会假红。
  ⇒ ★ 判别动作：★ **⓪ 的每个锚点，问「它是哪一步的产物」** ⇒ ⇒ 断言「B 步的产物」时，
  ⓪ 必须有一个 **B 步的锚点** ⇒ ⇒ ⇒ 否则 ⓪ 与后面的断言**不同层**。
- ⇒ ⇒ ★★★ **⇒ 通用式（③ 「前提不成立 ⇒ 红」与「⇒ SKIP」的二选一，往往说明前提还不可判别）**：
  ★ 二选一之所以难，是因为**两种前提在形态上同形** ⇒ ⇒ ⇒ **正解不是选一边，而是把前提变成可判别的信号**
  ⇒ ⇒ ⇒ 本轮就是：把「靶子对不对」从**形态**升级成**同源产物是否存在** ⇒ ⇒ ⇒ 然后红与跳过可以**同时**成立。
  ⇒ ★ 与仓里已有范式的关系：`admin/session_final_success_readpath_realdb_test.go` 的
  「`!hasTurns ⇒ t.Fatalf`」是**判别职责**（会话读路径本身就该红）；
  本轮是**形态前提**（靶子对不对）⇒ ⇒ **两者不矛盾，区别在「前提是否可判别」**。
- ⇒ ⇒ ★★★ **⇒ 通用式（④ 「同源产物」是分辨「没装」与「装坏了」的通用手法**：
  ★ 适用于任何「由某个脚本/迁移/安装器建出来的一组对象」：
  ★ 挑一个**同源**、**不被 CASCADE 连带**、且**只在那个流程跑过时才存在**的产物当判别式。
  ⇒ ⇒ ★ 判别动作：★ **在那个流程的产物集合里找「依赖关系最浅」的那一个**（函数/序列 > 视图 > 表）。

★ 同族：[[「前提不成立 ⇒ 红」与「⇒ SKIP」的两难]] · [[「按对象存在性」不按名字]] ·
[[量具失效要显式 SKIP 而非假装通过]] · [[「同一件事」要取两处事实源对拍]]。

---

## ⑧ 溯源

- llm-gateway-go §19.20.28 轮（2026-10-21，本会话 R80 续四）。
- 相关既有结论：R64 轮（基线必须保留 v1 fallback 体）｜R74 轮（形态门补自检，提交 `ea3263c21`）｜
  R69 轮（手工子集库，形态不可信）｜并行会话 R76 轮（把这条线划给我 + 「按对象存在性」的启发）。
