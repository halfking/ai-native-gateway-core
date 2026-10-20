# R80 轮：形态门补 `customer_id` 契约 ＋ **canonical 视图列集单调性**两条实测发现

> 日期：2026-10-21
> 会话：`mvs_eb0f78d138144e2ab538833ce12ffdfc`
> 起点：并行会话（`mvs_770f7896d54b4b62be91ec830b6d48c5`）R77 轮订正带进来的需求
> **对生产的写入 = 0**（全程只读 + 一次性库 `itgate_r80_shape`）
> 仓库状态：远端 `origin/main` = `611fd46e1`（本轮基于它 rebase）

---

## 0. 一句话结论

形态门 `TestFreshInstallRuntimeShape_EnsureChain` 的 ④ 段**契约集合从 2 扩到 3**
（加 `customer_id`），判据形态**不变**（**集合投影**，不是绝对列数 pin）；
并补一道**静态 pin 门**堵住「集合被静默削弱」这条路。
⇒ **变异 M1/M2/M3 三条全红**（形态层红 / 源码层红 / 削弱层红），还原按被改路径 `shasum -a 256` 逐字一致。

★ 顺带撞出**两条此前未被记录的机制**（见 §5）：canonical 视图的列集在既有库上**单调不减**，
且 ensure 链**只判族形态、不判列形状** ⇒ **列形状的收敛只由迁移链负责**。

---

## 1. 需求与背景

并行会话 R77 轮把 (B) 定性为**混合门**，并给形态门线留了一条订正：

- R77 轮实测：canonical 视图 `request_logs_with_current_month` **118 列**，
  `customer_id` / `request_class` / `due_at` **3/3 在位**。
- ⇒ 只差 `customer_id` 没被形态门钉住。
- ★ 平行会话同时订正了我此前建议的「钉 118 列」：**绝对数 pin 必然腐烂**
  （R74 轮实测：hot 表列数 104 → 106 → 108 三次）。

**本轮采用的口径 = 零语义风险的改瞄**：
**「视图必须存在且必须投影这几列」** ⇒ 契约集合从 2 扩到 3，**不钉列数**。

---

## 2. 改动

### 2.1 契约集合提升为具名变量（`db/db_fresh_install_runtime_shape_test.go`）

```go
var canonicalMustProjectColumns = []string{
	"request_class",
	"due_at",
	"customer_id",
}
```

④ 段的判据从「手写 IN 列表 + 写字面量阈值」改成
「`column_name::text = ANY($1::text[])` + 阈值 = `len(集合)`」：

- ★ 阈值不再有第二处手写数字 ⇒ **集合与阈值不可能不同步**；
- ★ 列名从 SQL 里搬进 Go 切片 ⇒ 静态 pin 门可以直接读它（不必解析 SQL 文本）。

### 2.2 新增静态 pin 门 `TestCanonicalMustProjectColumnsIsPinned`

★ **这道门防的是「静默削弱」，它是被实测逼出来的**（§4 的 M1 第一版）：
形态门断言的是「**集合里的列都在**」⇒ 有人把某一列从集合里删掉（并把阈值改回集合大小），
形态门**照样绿** ⇒ ⇒ 削弱不会自己暴露。
⇒ ⇒ 所以集合的**内容**另有一道静态门钉住：删一个元素 ⇒ 静态门红 ⇒ **削弱变成响亮的失败**。

★ 该门**不需要 DSN**（纯静态）⇒ 任何环境下都会真跑，不会被 SKIP 读成绿。

### 2.3 未改动的东西（刻意）

- **未改** `db/request_logs_view_schema.go`（canonical 视图的渲染器）—— 零 schema 变更。
- **未改** 任何迁移文件（canonical 与 embeddata 两份都未动）。
- **未改** 生产/既有库的形态（只有一次性库被 ALTER 过，且已还原）。

---

## 3. 验证读数

| 项 | 读数 |
|---|---|
| `go vet ./db/` | 干净 |
| 形态门（产品真实形态库 `itgate_r80_shape`） | **PASS**（118 列 / 3 列在位） |
| 静态 pin 门（无 DSN） | **PASS** |
| `TestMigration890ProjectionMatchesComposer` | **PASS** |
| `TestMigration890ViewBodyMatchesComposer` | **PASS** |
| 库侧实测：canonical 视图列数 | **118**（与 R77 轮一致） |
| 库侧实测：三列在位 | **3/3**（`customer_id` / `due_at` / `request_class`） |

★ 一次性库 `itgate_r80_shape` 是 R80 轮之前就建好的「产品真实形态库」，本轮沿用未重建。

---

## 4. 变异证据（三条全红）

### M1 —— 形态层：把 `customer_id` **真的从视图里拿掉** ⇒ 形态门红

★ **第一版 M1 是无效变异**：只把契约集合里的 `customer_id` 删掉、阈值改回 2 ⇒ **门照样绿**
（这正是「集合可被静默削弱」的实证，见 §2.2）。
⇒ ⇒ **改正**：变异必须**改形态**，不是改断言。

真实变异（对视图本身动刀）：

```sql
ALTER VIEW public.request_logs_with_current_month
  RENAME COLUMN customer_id TO customer_id_dropped;
```

⇒ 形态门红，且报的是契约原文：

```
db_fresh_install_runtime_shape_test.go:288: canonical 视图 request_logs_with_current_month
只投影了 2/3 个必投影列（request_class / due_at / customer_id）⇒ 分类信息与客户维度的读路径断了
（R69 轮实测过前两列的载体契约；R77 轮实测三列 3/3 在位）
```

★ 还原：`RENAME COLUMN ... TO customer_id` ⇒ 门恢复 **PASS**（A/B 往返闭合）。

### M2 —— 源码层：改 `canonicalSessionOnlyDDL` 的渲染 ⇒ 静态契约门红

删掉 `canonicalColumnOrderV2` 里的 `"customer_id"` **与** `projectionExprsV2` 里的
`"t.customer_id"`（两处必须同删，否则 `init()` 期的长度自校验先 panic）：

```
migration_890_v1_family_drop_test.go:147: 890's $proj$ block and db.canonicalSessionOnlyDDL have drifted.
    Consequence: the view the migration installs and the view the gateway self-heals on boot
    are NOT the same — a boot-time self-heal would swap one body for the other, ...
```

⇒ ★★ **源码层已有锁**（`TestMigration890ProjectionMatchesComposer` /
`TestMigration890ViewBodyMatchesComposer`），本轮**不需要新加**。
⇒ ★ **本轮补的是「运行时形态层」**（库里那个视图到底有没有这几列）—— 两层互补。

### M3 —— 削弱层：把 `customer_id` 从契约集合里删掉 ⇒ **静态 pin 门红**（形态门仍绿）

| 门 | 读数 |
|---|---|
| 静态 pin 门 | **RED**：`canonical 必投影列集合的大小变了：现在 2 列（[request_class due_at]），期望 3 列（[request_class due_at customer_id]）` |
| 形态门 | **GREEN**（★ 这正是被 pin 堵住的那条路） |

⇒ ★★ **M3 是本轮最有价值的一条**：它把「形态门可以被无声削弱」这个**事实**，
变成了**一道会响的门**。

### 还原校验（按**被改的那个路径**）

| 路径 | 还原后 `shasum -a 256` | 与备份一致 |
|---|---|---|
| `db/db_fresh_install_runtime_shape_test.go` | `b9b27c29…6a17609e` | ✔ |
| `db/request_logs_view_schema.go` | `aaa71510…51b52c6f` | ✔ |
| `sql/migrations/startup/890_drop_request_logs_v1_family.sql` | `39eb2781…7555591` | ✔ |

★ 教训沿用 R65 轮的纪律：**还原校验必须校验「被改的那个路径」**，
读同一个错路径会报出假读数。

---

## 5. ★★ 两条此前未被记录的机制（本轮的意外收获）

### 5.1 `CREATE OR REPLACE VIEW` **不能删列** ⇒ canonical 视图的列集**单调不减**

实测（R80 轮，PG 17）：

```
ERROR:  cannot drop columns from view
CONTEXT:  SQL statement: ...
```

★ **来源**：本轮第一版变异想把 `customer_id` 从 890 的 `$proj$` 里删掉
（模拟「新装出的视图少这一列」）⇒ **迁移直接失败**，视图纹丝不动（仍 118 列 / 3 列在位）。

⇒ ⇒ ★★★★ **通用式（PG 硬约束）**：
**`CREATE OR REPLACE VIEW` 只能「加列或保持」，不能「减列」**
⇒ ⇒ **一个视图在既有库上的列集是单调不减的**
⇒ ⇒ **从最后一个迁移的 `$proj$` 里删一列，永远不会让视图少这一列**——它只会让那个迁移**失败**。
⇒ ⇒ ★★ **推论（视图列集的 SSOT 是「链上所有视图体的并集」，不是最后一个）**：
`448 / 459 / 532 / 575 / 610 / 696 / 700 / 710 / 734 / 738 / 740 / 815 / 816 / 817 / 824 / 890`
这一串迁移里**任何一次**加过的列都会**留在**既有库的视图上。
⇒ ⇒ ★★ **要真正删掉视图的一列，只能 `DROP VIEW` + `CREATE VIEW`**（本轮**未做**，
属破坏性动作 ⇒ 属主决定）。

★ 对本轮的意义：它解释了**为什么第一版 M1（改断言）无效、而改形态有效**——
**「视图少一列」这个状态在既有库上几乎不可达** ⇒ 要验门，就只能直接对视图动刀（M1 的做法）。

### 5.2 ensure 链的守卫**只判族形态、不判列形状** ⇒ **列形状的收敛只由迁移链负责**

`db/request_logs_view_schema.go` 的 ensure 守卫（自愈重建的触发条件）：

```go
if canonicalExists && bodyIsSessionOnly && !bodyHasV1Arm {
    return nil          // ← 已是会话族两臂形态 ⇒ 直接返回，永不重建
}
```

★ 它判的三件事：**视图存在 / 是 session-only 两臂 / 没有 v1 臂** ⇒ **完全不判列形状**。

⇒ ⇒ ★★★★ **实测证据**：本轮 M2 把 Go 渲染器改成 117 列后跑形态门 ⇒ **视图仍是 118 列**、
门**照样 PASS** ⇒ ⇒ **ensure 链根本没重建过它**。
⇒ ⇒ ★★ **推论**：
**canonical 视图一旦装成「会话族两臂」形态，ensure 链就永远不会重建它**
⇒ ⇒ ⇒ **列形状的收敛 100% 由迁移链负责** ⇒ ⇒ **迁移链少投影一列 ⇒ 运行时不会有任何东西替它补上**
⇒ ⇒ ★★ **所以本轮这道门（量运行时形态）不可省**：它是「迁移链有没有把列投影对」的唯一运行时证据。

★ ★ **两条合起来的净结论**：
**「迁移链写错列」在既有库上 ⇒ 视图停在旧形状（读者可能仍拿到列）；**
**「迁移链写错列」在新装库上 ⇒ 视图按链装出来的形状成形 ⇒ 读方直接 42703。**
⇒ ⇒ **新装路径才是列形状的暴露面** ⇒ ⇒ **形态门必须在新装形态库上量**（本轮沿用产品真实形态库）。

---

## 6. 诚实边界

- ★ 本轮**没有**重建一次性库 ⇒ 形态门的读数来自 R80 轮之前建好的「产品真实形态库」
  （R74/R77 轮那一套），**不是**本轮现建的新装库。
- ★ 本轮**没有**验证「真·全新安装」路径（那是 installer 的迁移链，分钟级）⇒
  §5.2 的「新装路径才是暴露面」是**机制推论**，**未**在本轮实测。
- ★ M1 用的是 `ALTER VIEW … RENAME COLUMN`（改列名）而非删列 ⇒
  它证明的是「**门能发现列不在**」，不是「门能发现列被删」（后者在既有库上不可达，见 §5.1）。
- ★ 本轮**没有**碰并行会话的任何文件；`db/` 包里并行会话的未提交文件
  （`db/scratch_db_test.go` / `db/db_package_isolation_gate_test.go` 等）本轮**一个都没碰**
  ⇒ ★ 提醒：`db/db_package_isolation_gate_test.go` 当前有语法错误（`t.Helper.TODO()`）
  ⇒ **共享工作树上 `./db/` 包级 vet 会红**（不是本轮改动）。
- ★★ **靶子自证（本条由并行会话 R78 轮通报补入）**：**跑一次 `./db/` 包会把 890 已退役的
  v1 族 `_request_logs` 按新名复活，并 CASCADE 带走 canonical 视图**
  ⇒ ⇒ **任何以「canonical 视图形态」为判据的读数，靶子必须自证「没被 db 包碰过」**
  ⇒ ⇒ 本轮形态门读的是**一次性库 `itgate_r80_shape`**（本轮**没有**跑过 `./db/` 包）⇒ 靶子成立。
  ⇒ ⇒ ⚠ **并行 R79 轮建议**把 `TestDualReadValidator_Summarize_RealDB` 纳入 v1 族判据 ——
  该门已是「退役态契约的真库门」（真库上证明 `Summarize` 不 42P01 且如实报 `v1_family_retired`）
  ⇒ ⇒ **与本轮判据同族**；但它落在 `cmd/gateway/dual_read_*`（**归并行会话 D3 范围**）
  ⇒ ⇒ **本轮未改**，仅登记为「v1 族判据」的可选增强项。

---

## 7. 与规划文档的对应

- 本轮对应规划文档 `docs/96小时内修订审计-20261007-1350.md` 的 **形态门线**（§19.20.17 / §19.20.24）。
- ★ 本轮**没有**推进的属主决策项（仍待拍板，见 §19.20.16 末尾）：
  ① 既有库 9 个孤儿要不要清理；② `573`/`603` 两条未登记失败怎么处理；
  ③ `727/728/729/744` 是否登记进注册链；④ db/ 两条 v1 时代夹具（A′/B′/C′）；
  ⑤ `GATEWAY_CANONICAL_TEST_LOCAL_ONLY` 未授权时 `t.Fatal` ⇒ 改 `SKIP`；
  ⑥ 613/628 两处 JOIN 结构要不要一并纳入 `TestMigration890ViewBodyMatchesComposer`；
  ⑦ `run-integration-gate.sh` 是否切事实源。
</content>
</invoke>
