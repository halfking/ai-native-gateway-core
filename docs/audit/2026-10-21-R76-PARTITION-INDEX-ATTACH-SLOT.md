# R76 轮 —— 「attach 槽位冲突」根因实测闭合 ＋ 三份迁移守卫重写

> 日期：2026-10-21｜树：`367c71e54` 起｜容器：`llm-gateway-pg`
> 库：`itgate_46941_612` / `itgate_77236_10923`（官方门禁保留的失败态，取证用）
> ★ **对生产零 schema 变更、零数据变更**（只改三份迁移的**守卫**，DDL 语义不变）
> ★ 官方门禁未登记的「第二遍失败」：**5 条 → 2 条**

---

## 1. 头条：根因不是「命名漂移」，而是**守卫按名字判挂载**

### 1.1 前情（R73/R74/R75 的悬念）

- R73 真跑官方 runner ⇒ `exit=2`，未登记的「第二遍失败」**5 条**：
  `573` / `603` / `877` / `887` / `888`。
- 三条 attach 失败的报错**逐字相同**：
  `cannot attach index "…" as a partition of index "…"`。
- R74 查生产 252 ⇒ 父索引 `idx_session_turns_search_trgm` 的 6 个成员**全是自动名**
  （`_search_text_idx` × 5 + `_default_search_text_idx`），**无一个 `_search_trgm`**。
- R75 读 877 原文 ⇒ 定位到「第 4 段守卫按名字硬拼」⇒ 建议改命名。

### 1.2 ★★★★★ R76 的订正：**「命名漂移」这个表述本身要改**

R74/R75 的建议是「把 877 的命名改成与生产一致的 `_search_text_idx`」。
**R76 实测证明这条建议无效**：

- 生产上的 `_search_text_idx` **不是谁「该用的名字」**，而是 **PG 自动生成的**（见 §2 第 3 条）；
- ⇒ 就算把 877 改成 `_search_text_idx`，在「该分区已有自动名成员」的形态下
  **仍然会建出同义重复索引**、**仍然撞同一个槽位** ⇒ **问题原地不动**。

⇒ ★★ **正解是让守卫承认「该分区在父索引下已经有成员」**，而不是让迁移去猜 PG 的命名。
⇒ ⇒ R74/R75 的原文**保留**，本节即为就地订正。

---

## 2. PG 的硬约束 —— **实测**，不是推理

一次性 PG 17（容器 `llm-gateway-pg`）三条实测：

### ① ★★ 一个分区在同一个父索引下只能有一个成员索引

```
ERROR:  cannot attach index "st_1_trgm_a2" as a partition of index "st_trgm_a"
DETAIL: Another index is already attached for partition "st_1".
```

⇒ ★★ **与成员定义是否同义无关** —— 挂一个**定义完全相同**的成员，同样被拒。
⇒ ★★ 这是本轮一切推理的**唯一前提**，也是 DETAIL 行直接写明的。

### ② ★ 一个分区可以同时挂在**多个不同的父索引**下

```
 parent   |   member
-----------+-------------
 st_trgm_a | st_1_trgm_a
 st_trgm_b | st_1_trgm_b
```

⇒ ★ 约束是「**每个父索引 × 每个分区 = 至多一个成员**」，不是「每个分区至多一个成员索引」。

### ③ ★★★ PG 会**自动**给分区建匹配索引，并**自动命名**

新分区（`CREATE TABLE … PARTITION OF`）继承父分区索引时，PG 自动产出：

```
   member    |   parent
-------------+-----------
 st_1_trgm_a | st_trgm_a
 st_2_t_idx  | st_trgm_a      ← 自动命名：<partition>_<column>_idx
 st_2_t_idx1 | st_trgm_b
```

⇒ ★★★ **「自动命名」只发生在父分区索引已存在之后新增的分区上** ——
这正是**生产形态**的由来（生产的 6 个成员全是自动名）。

### ④ ★ DETACH → RE-ATTACH **不会**改名（一个被证伪的假说）

R76 中途曾假设「DETACH→RE-ATTACH 会把子索引改名成自动名」⇒ 实测**证伪**：
PG 复用既有的等价索引、**保留原名**（`st_1_trgm_a` 挂回去后仍叫 `st_1_trgm_a`）。
⇒ ⇒ 孤儿与自动名成员**不是** DETACH 造成的 ⇒ 归因必须落在 §2③ 上。

---

## 3. ★★★★★ 三方事实完全自洽 ⇒ 缺陷本体

| 来源 | 观测 |
|---|---|
| **生产 252**（只读） | 父索引 6 个成员**全是自动名**；`_search_trgm` **一个都没有** ⇒ **877 在生产上从未建出自己命名的子索引** |
| **门禁失败库** `itgate_46941_612` | `session_turns_2026_10_search_text_idx`（自动名）**占着槽位**；`session_turns_2026_10_search_trgm`（877 建的）**是孤儿**（未挂父、同义重复） |
| **877 源码** | 第 1 段按**固定后缀**建子索引（`:120-122`）；第 4 段按**索引名**判「挂没挂」（`:156`） |

⇒ ⇒ **缺陷本体**：三段式的第 1 段与第 4 段都假设「我建的那个索引名就是该分区在父索引下的成员名」，
而真实形态是「**该分区在父索引下的成员名由 PG 自动决定**」⇒
第 1 段建出**同义重复索引**（孤儿）＋ 第 4 段去 ATTACH 时**撞槽位** ⇒ 迁移失败 ⇒ 门禁 abort。

⇒ ★★ **877 头部 `:72-73` 早就写下了正确规格**：
「验收必须按**分区级 + 定义匹配**查 pg_index，**不能按索引名 grep**」
⇒ ⇒ **本轮是让实现服从它自己的规格，不是发明新语义。**

---

## 4. 修法：守卫重写（DDL 语义零变化）

判据统一改成：**「该分区在父索引下有没有成员（名字不限）」**。

```sql
-- 旧（按名字判）⇒ 在真实形态下判成「没挂」⇒ 去 ATTACH ⇒ 撞槽位
AND NOT EXISTS (
  SELECT 1 FROM pg_inherits ci
  WHERE ci.inhparent = 'public.idx_session_turns_search_trgm'::regclass
    AND ci.inhrelid = to_regclass(format('public.%I', part || '_search_trgm'))
)

-- 新（按分区级判）⇒ 已有成员就跳过
AND NOT EXISTS (
  SELECT 1 FROM pg_inherits ci
  JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
  WHERE ci.inhparent = to_regclass('public.idx_session_turns_search_trgm')
    AND mi.indrelid = to_regclass(format('public.%I', part))
)
```

**三份迁移 × 三处**（共 11 处锚点）：

| 迁移 | 第 1 段（不重复建） | 第 4 段（ATTACH 守卫） | 形状校验段 |
|---|---|---|---|
| `877_session_turns_search_text_trgm` | ✔ | ✔ | 无此段（只查 invalid） |
| `887_session_turns_search_text_fts` | ✔ | ✔ | ✔ |
| `888_session_family_join_index_gaps` | ✔ × 2（两个父索引） | ✔ × 2 | ✔ × 2 |

⇒ ★ **第 ③ 处（校验段）不能漏**：守卫放松后，旧的「按名字判未挂载」校验会把
「PG 自动命名的成员已承重」误判成缺陷 ⇒ **反过来误报**。R76 一开始漏了这一处，同批补上。

---

## 5. ★★★★★ 官方门禁：未登记失败 **5 → 2**

`scripts/audit/run-integration-gate.sh` 真跑（`KEEP_GATE_DB=1`）：

| 轮次 | applied | failed | 未登记的「第二遍失败」 |
|---|---|---|---|
| R73（修复前） | 276 | 20 | **5**：`573` / `603` / `877` / `887` / `888` |
| R76（修复后） | **279** | **17** | **2**：`573` / `603` |

⇒ ⇒ **877/887/888 三条 attach 失败全部消失**。
⇒ ★ 剩下 2 条是**另一族机制**（v1 退役顺序 ⇒ `cannot drop view … because other objects depend on it`）⇒ 本轮未动。

---

## 6. ⚠★★ 本轮踩到的最大坑：迁移有**两份副本**

- canonical：`sql/migrations/startup/<file>.sql`
- embeddata：`installer/cmd/llm-gw-installer/embeddata/startup/<file>.sql`
  （全量门 `installer/cmd/llm-gw-installer/migration_embed_sync_gate_test.go` 钉住两份**逐字相同**）

★★ **安装器与官方门禁的 rerun 阶段读的是 embeddata 那份**
（门禁脚本 `SF_DIR="$REPO_ROOT/installer/cmd/llm-gw-installer/embeddata/startup"`）
⇒ ⇒ **只改 canonical ⇒ 门禁 `EXIT=2` 且报错逐字不变**
⇒ ⇒ ★★ **极易误判成「我的修复无效」**（R76 实测踩到：第一次补丁改错地方，门禁复现同一报错）。

⇒ ★ **纪律：改迁移必须两份同改；改完先跑同步门（秒级），再跑官方门禁（分钟级）。**

---

## 7. 门与变异

### 7.1 静态门（`sql/migrations/startup/migration_r76_attach_slot_test.go`，无 build tag）

1. 守卫不得再出现「按名字判挂载」（`ci.inhrelid = to_regclass`）；
2. 必须按分区级判成员（`mi.indrelid =`）；
3. ★ **剥离器自检**（量具有牙）：**断言 SQL 文本前先剥注释** ——
   否则 877 头部那句「不能按索引名 grep」会让门**恒绿**。

### 7.2 真库门（`…_integration_test.go`，`//go:build integration`）

**复现原缺陷形态**：先用一个**普通 `CREATE INDEX`**（不带 `ON ONLY`）在父表上建索引
⇒ PG 自动给每个分区建成员并自动命名 ⇒ **这正是生产 252 的形态** ⇒
跑三份真迁移 ⇒ 断言 **零报错 + 零孤儿 + 每个分区在父索引下确有成员**。

### 7.3 ★★ 变异（门必须证明它有牙）

| 变异 | 内容 | 静态门 | 真库门 |
|---|---|---|---|
| **M1** | 只把第 4 段守卫改回按名字判 | **红** | **绿** |
| **M2** | 第 1 段 + 第 4 段**同时**还原（完全复原缺陷形态） | **红** | **红**（报错逐字复现原始那条，SQLSTATE 55000） |

⇒ ★★ **M1 下真库门仍绿，是机制事实而非门的漏洞**：第 1 段「不重复建」先把重复索引挡住了
⇒ ⇒ **防御是双层的**（跳过建造 + 跳过挂载）⇒ ⇒ 这也说明**两处必须一起改**，只改一处不够。

⇒ ⇒ 变异按**被改路径** `shasum -a 256` 逐字校验还原（canonical + embeddata 两份都验）。

---

## 8. ⚠ 本轮自身的两次量具故障（如实登记）

1. ★ **孤儿统计 SQL 的 `AND`/`OR` 优先级 bug** ⇒ 把「已挂父的成员」误报成孤儿
   ⇒ 修正口径（`OR` 两侧都加括号 + 排除父索引与 hot 独立索引）后重测：
   修正前「11 个孤儿」⇒ **修正后 0 个**。
2. ★★ **M1 下真库门显示 `ok` 实为 SKIP** —— 库名 `itgate_r76_mut1` 不含 `test`
   ⇒ 被门自己的「一次性库」守卫跳过（**0.431s 即疑点**）
   ⇒ 换成合规库名后才发现门没红 ⇒ ⇒ ★ **读门禁输出必须显式看 SKIP 行，不能只看 ok/FAIL**。

---

## 9. 阴性结论与残留（如实登记，不夸大）

- ★ **同款形态的守卫还有 4 份**（`727` / `728` / `729` / `744`，同样「三段式 + 按名字判挂载」）
  ⇒ ★★ **但它们不在注册链**（`installer/internal/dbinit/runner.go` 的 `StartupFiles` 无它们）
  ⇒ ⇒ **安装器与官方门禁都跑不到它们** ⇒ **不影响本轮结论，也不影响门禁**
  ⇒ ⇒ 登记为「同款形态、**未在注册链**」；若将来被登记进链，**必须先做本轮同样的守卫重写**。
- ★ **生产上不需要清理**：生产 252 实测**零 `_search_trgm` 孤儿**（该形态从未在生产出现），
  既有 6 个自动名成员**逐字不变** ⇒ 本补丁在生产上是**空操作**。
- ★ **既有库里可能残留孤儿**（跑过坏重跑的库；门禁留存的 `itgate_46941_612` 实测 9 个）：
  本补丁**只阻断新增**，**不做清理** ⇒ ⇒ **清理是破坏性动作，留待属主决定**。
- ⚠ **`573` / `603` 两条未登记失败仍未处理**（v1 退役顺序，与本轮不同机制）。

---

## 10. 台账与副本纪律（都已过门）

- `docs/db-changelog.md` 补三行登记（新 SHA）+ 标注「原登记行 … 保留」
  ⇒ `scripts/verify-migration-checksums.sh` 实测 `OK: 240 registered migrations verified`
  ⇒ `internal/sqlguard` 两道门（`TestRegistryDivergentShaRowsAreDisambiguated` /
  `TestRegistryShaIsActuallyTheFile`）全绿。
- canonical 与 embeddata 两份**逐字相同**（`cmp` 校验）⇒ `migration_embed_sync_gate_test.go` 绿。

---

## 11. 属主决策项（本轮未擅自拍板）

1. ★ **既有库里的 9 个孤儿要不要清理**（本轮只阻断新增；清理是破坏性动作）。
2. ★ **`573` / `603` 两条未登记失败**怎么处理（v1 退役顺序，与本轮不同机制）。
3. ★ **727/728/729/744 是否登记进注册链**（若登记 ⇒ 必须先做同样的守卫重写）。
4. ★ D2 剩余项：`GATEWAY_CANONICAL_TEST_LOCAL_ONLY` 未授权时 `t.Fatal` ⇒ 改 `SKIP`。
5. ★ 其余沿用：切事实源的前置、A 类剩余、C1、db/ 两条 v1 夹具。
