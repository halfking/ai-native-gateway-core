# 208 号 · R89-DS —— 一份要**在生产上手跑**的清理脚本，三道守卫全是坏的：字典序冒充时间、唯一无门禁的 TRUNCATE、统计估算当事实

> 变更面：`sql/fixes/2026-10-02-db-storage-reclaim.sql`（三处门禁 + 头部更正）、`sql/schema/storage_reclaim_guard_test.go`（**新门**，含 5 条负控）。
> **零 Go 生产代码改动。**
> 触发方式：207 号推送时并发会话并进来一批我没审过的存储侧产物，其中这份落在「hot+分区存储」这个我一直在审的范围里。

---

## 〇、起手：合并带进来的东西，也在我的审阅范围内

207 号提交前 `git fetch` 查到 `origin/main` 已被另一会话推进 2 个提交（`474b40a68` + 合并 `4a535b9bd`，「252/34 双实例 PG17 审计交付」），并进 67 个新文件。我 merge 后复验守卫仍绿才推送。

那批产物里有 `sql/fixes/2026-10-02-db-storage-reclaim.sql`。`sql/fixes/` 的性质先确认清楚：

- 全仓对它的引用**只在文档/手册里**（`docs/03-design/…`、`docs/archive/process/…`），**没有任何自动执行路径**；
- 脚本头写明用法是 `psql -d llm_gateway -f …`；
- 验证记录写的是「dry-run 通过 + 真实执行包在 `BEGIN … ROLLBACK` 里演练通过」⇒ **它尚未被真正破坏性地执行过**。

⇒ **这是修它的最佳时机**：门禁的代价此刻还只是「一次代码评审」，执行之后就变成「一次数据事故」。

---

## 一、🔴 F1：`c.relname < 'usage_facts_default'` 是**空操作**，会删掉脚本自己说「保留」的那张分区

脚本 `:161`（改前）用它表达「只碰过去的日期分区」：

```sql
AND c.reltuples = 0
AND c.relname < 'usage_facts_default'   -- 只碰过去的日期分区
```

而脚本 `:20` 写着：

> `usage_facts_20261003` 虽为 0 行但是**预建的未来分区**，保留。

**这两个说法互相矛盾，而矛盾的原因是一个纯算术事实**：

| 名字 | 第 13 个字符 | 与 `usage_facts_default` 比 |
|---|---|---|
| `usage_facts_20260926` … `usage_facts_20261003` | `'2'` = 0x32 | **在前** |
| `usage_facts_default` | `'d'` = 0x64 | — |

⇒ `usage_facts_20261003 < 'usage_facts_default'` 恒为 **TRUE**（本机 ordinal 比对实测 `-50`；C 与 en_US 两种 collation 下数字都排在字母前，故 PG 里同样成立）。

⇒ 那条守卫**什么都不排除**。而它唯一还起作用的保护是 `c.reltuples = 0` —— 恰好是下一条要说的不可靠字段。**两张独立失效的守卫叠在一起，看起来像一个门禁。**

⚠️ 同一个 bug 还在**第 1 步的 dry-run 报表**里（`:116` 同款谓词）⇒ **报表会把 20261003 标成 `DROP`**，与头部注释直接矛盾。也就是说：**操作人看 dry-run 报表也无法发现这件事**，因为报表和执行体犯的是同一个错。

## 二、🔴 F2：`TRUNCATE usage_facts_default` 是**唯一没有非空门禁**的破坏性动作

改前的三个破坏性动作：

| 动作 | 非空门禁 | 依赖 |
|---|---|---|
| 2b `DROP` 过去空分区 | ✅ `c.reltuples = 0` | 不可靠（F3） |
| 2c `DROP bak_*` | ✅ `approx_rows > 0` + `force_nonempty` | 不可靠（F3） |
| **2a `TRUNCATE usage_facts_default`** | ❌ **无** | —— |

而 `usage_facts_default` 恰恰是**兜底分区** = 「时间越界写入的落点」。它最可能积累真实行：**任何超出已建分区范围的时间戳都会落在这里**。脚本头那句「实测 0 行 / 534 MB 全是死索引页」是**192.168.31.34 在 2026-10-02 那一刻的测量**，不是对 252 / 本机 / 任何未来时刻的保证——而脚本的用法就是「在别的库上跑」。

## 三、🔴 F3：`reltuples = 0` 是**统计估算**，脚本自己已经认定它不可信

- `pg_class.reltuples` 是规划器的**估算**：PG14+ 从未 `ANALYZE`/`VACUUM` 过的表是 **-1**（不是 0）；老版本是 0。
- 更危险的一种：表在**空**的时候被 `ANALYZE` 过、之后又写入了行，在下一次 `ANALYZE` 之前它**仍然报 0** ⇒ `reltuples = 0` 会**放行一次有数据的删表**。

⚠️ 而这份脚本自己在文件末尾写着：

> 审计发现 **625 张表**自 `stats_reset` 起从未 `ANALYZE`……

⇒ **它已经认定该字段不可信，却拿它当删表门禁。** 这是自相矛盾，而且是**同一个文件内**的自相矛盾。

---

## 四、改法：门禁一律取**事实**，不取形状

| 原来（形状/估算） | 改后（事实） |
|---|---|
| `c.relname < 'usage_facts_default'`（名字排序） | `substring(c.relname from '^usage_facts_(\d{8})$')::date < current_date`（**分区名里的真实日期**） |
| `c.reltuples = 0` / `approx_rows > 0`（统计估算） | `public.storage_reclaim_table_is_empty(relname)` = `EXISTS (SELECT 1 … LIMIT 1)` 真探针 |
| 2a 无门禁 | 与 `bak_*` **共用同一个 `force_nonempty` 闸**，非空即 SKIP + WARNING |

新增辅助函数（`CREATE OR REPLACE`，幂等；已在头部登记它会在库里留下一个函数）：

```sql
CREATE OR REPLACE FUNCTION public.storage_reclaim_table_is_empty(p_relname text)
RETURNS boolean LANGUAGE plpgsql AS $probe$
DECLARE v_oid regclass; v_has_row boolean;
BEGIN
  v_oid := to_regclass(format('public.%I', p_relname));
  IF v_oid IS NULL THEN RETURN true; END IF;   -- 幂等：已不存在视为空
  EXECUTE format('SELECT EXISTS (SELECT 1 FROM public.%I LIMIT 1)', p_relname) INTO v_has_row;
  RETURN NOT v_has_row;
END $probe$;
```

⚠️ **报表与执行体必须同源**（`:116` 的 CASE 与 `:161` 的 `FOR … WHERE` 一起改）。否则 dry-run 报表会与真实执行不一致——而 dry-run 报表正是操作人唯一的审查依据。

⚠️ 顺带修一个**我自己写出来**的坑：报表的 `CASE` 里我第一版直接写 `substring(...)::date < current_date`，而该清单用 `LIKE 'usage_facts%'` 会把**名字不是日期格式**的分区一起捞出来 ⇒ `::date` 直接抛 `invalid input syntax for type date`，**整个报表查不出来**。已加 `IS NOT NULL` 前置。

头部那句「保守档 ≈ 596 MB **零数据风险**」也已更正为证伪说明——它错在守卫上而不是数据上。

---

## 四之二、🔴 F4（顺带撞上）：唯一会去核对 `sql/objects/` 的门，在本机**从未真正运行过**

跑 `go test ./sql/schema/` 时冒出一处失败：

```
--- FAIL: TestReconcileToolSupportsThirdRepresentation (0.18s)
    objects_registry_test.go:140: reconcile tool failed: exit status 9009
```

先排除是不是我引入的：该测试只读 `sql/schema/01-schema.sql`（自比对）与 `sql/objects/`，**不碰本轮任何文件**（本轮动的是 `sql/fixes/…` 与一个新增测试文件）。**逻辑上不可能由我引起。**

根因在 `objects_registry_test.go:126`（改前）：

```go
if _, err := exec.LookPath("python3"); err != nil {
    t.Skip("python3 不可用；跳过（本跳过不构成该工具可用的证据）")
}
```

⚠️ **这是「存在性」判据，不是「可用性」判据** —— 而它在 Windows 上恰好最不可靠：系统自带的 **App Execution Alias 桩** `python3.exe` **在磁盘上真实存在**（本机 `Get-Command python3` 成功）⇒ `LookPath` 成功 ⇒ **不走跳过分支** ⇒ 随后 `exec.Command` 以 **9009**（命令未真正安装）失败。

⇒ **作者亲手写下的优雅降级被完全击穿**：
- 真没装 Python（Linux 常见）⇒ **SKIP**，门静默不跑；
- 装了别名桩（Windows 常见）⇒ **FAIL**，且报错形态是「工具坏了」，而真相是「**工具根本没跑起来**」。

**两种形态都不对**，而且第二种会把人引向错误的方向（去查 `baseline-reconcile.py`）。

**改法**：换成**活性**探针 —— 判据必须与被测量对象同源，我们要的是「这个工具**能执行**」，不是「有个叫 python3 的文件存在」：

```go
if err := exec.Command("python3", "-c", "pass").Run(); err != nil {
    t.Skipf("python3 解析到的东西不可执行（%v）；跳过。⚠️ 这**不是**「三方对账无漂移」" +
        "的证据 —— 本门在本机从未真正运行过。…", err)
}
```

⇒ 改后本机走 **SKIP**，且跳过理由**大声、准确、可操作**。

### 4.2.1 这条为什么重要：它接上 205 号

205 号的发现是：`sql/objects/views/request_logs_with_current_month.sql`（SSOT）**没有任何门**把它钉到运行时视图定义上（`db/view_schema_v2_contract_test.go` 绑的是 composer ↔ migration 734）。

⇒ 全仓**唯一**会去核对 `sql/objects/` 与 baseline 漂移的机制，就是这个 python reconcile 工具。
⇒ 而它在本机**从来没有跑过** ⇒ **`sql/objects/` 与运行时定义的漂移，在开发机上始终未被验证过**（CI 上是否跑过**未验证**，本轮未联网核对 CI 日志）。

⚠️ 这**不推翻** 205 号的结论（那结论是静态可证的：`sql/objects/**` 与那对之间确实没有门），但它**加重**了 205 号的后果评估——**兜底机制存在却没在跑**。

---


同一条哲学：**仪器自己的失效模式才是危险的部分**。按文本结构断言「哪些守卫在里面」。

1. **覆盖下限**：认出 `-- ---- 2x.` 分节 **≥ 3**、DROP **≥ 2**、TRUNCATE **≥ 1** —— 抽取器坏掉时下面会全绿。
2. **每个含破坏性动作的分节必须自带真探针**（守卫与动作**同节**，不许跨节背书）。
3. 活跃行里**禁止** `relname <` / `relname >` 这类排序比较。
4. 活跃行里**禁止** `reltuples = 0` 当判据。
5. 必须存在 `::date < current_date` 形式的真实日期判据。
6. **注释先剥再判**（`audit_script_test.go:135-137` 已记同一类坑）——这份脚本的头部注释**逐条描述了这三个 bug**，原始文本搜索会被自己的说明满足。

### 5.1 ⚠️ 本门自己踩了两次「误报正确代码」，都做成负控钉住

| 负控 | 抓的是什么 |
|---|---|
| 1 | 未加门禁的节必须被报成**无门禁** |
| 2 | 字典序守卫 + `reltuples = 0` 必须被检出 |
| 3 | **上一节**的探针不得替本节背书（207 号 §118 的「A 替 B 背书」） |
| 4 | **`CONTINUE` 式合法守卫不得被误报** —— 本门第一版按 `;` 切段，把 2c 的 `IF NOT <探针> … CONTINUE; END IF;` 判成无门禁 ⇒ **误报正确代码** |
| 5 | `<>` / `<=` 不得被当成排序守卫 —— 本门第二版用 `strings.Contains(l, "relname <")`，把脚本里合法且必要的 `AND c.relname <> 'usage_facts_default'` 误报了一次；修法还踩了 **RE2 无前瞻** ⇒ 分支顺序必须把双字符形式放前面 |

⇒ 负控 4 与 5 是**本门给自己造的**（不是脚本的缺陷）。把它们留在门里，是因为**一道会误报正确代码的门比没有门更坏**（199 号 §96 / 200 号 §101）。

**文件级负控**（在真实文件上，绿是改完才拿到的）：把 2b 的真实日期守卫退回成 `c.relname < 'usage_facts_default'` ⇒ 🔴 红「活跃行里仍有 1 处用 `relname <` / `relname >` 做分区名比较」+ 字典序解释；改回即绿。

---

## 六、验证

```
$ go test ./sql/schema/
ok  github.com/kaixuan/llm-gateway-go/sql/schema
```

主断言 PASS + 5 条负控 PASS；10 个已登记守卫 `ok ×10 / 0 FAIL`；gofmt/vet 干净。

⚠️ **未连 PG**：本轮全部结论是**静态阅读 + 纯文本判据**。PL/pgSQL 探针函数、`substring(...)::date`、两处 `storage_reclaim_table_is_empty` 调用**都没有在真库上跑过**。
---

## 七、证伪与未 settle

**证伪 3 条**

1. 撤回「脚本头写的『保留 usage_facts_20261003』是可以信赖的」——**它与同一份脚本的执行谓词直接矛盾**，且矛盾方（谓词）是错的那一方。作者的**意图**是对的，**实现**是空的。
2. 撤回「`reltuples = 0` 在这张表上恰好也是对的」——「恰好」不可依赖：它依赖「这张表从未被 ANALYZE 过又写入过行」这一未验证前提，而脚本自己说全库有 625 张表处于该状态。
3. 撤回（自证）「先写的 `NOT …is_empty` 极性是对的」——`storage_reclaim_table_is_empty` 返回 true 表示空 ⇒ DROP 条件应是**正**用；我第一版写成 `AND NOT …` ⇒ 会把**非空**分区判成可删。已在提交前自查改正。

**未 settle（如实登记）**

- ⚠️ **CI 上 `TestReconcileToolSupportsThirdRepresentation` 是否真的跑过，本轮未核实**（未联网查 CI 日志）。若 ubuntu runner 上 python3 正常则它一直在跑；若也在某种跳过态里，则 `sql/objects/` 的漂移**在任何环境都未被验证**。这是 F4 的延伸，值得单独查一次。
- ⚠️ **未在真库上验证**：探针函数能否在目标库创建、`substring(...)::date` 对该库分区命名是否成立、`current_date` 与库时区是否一致。本轮**只做了静态修正**，上线前建议先在 34 或本机 dry-run 一遍。
- ⚠️ **`sql/fixes/` 下另外 15 个脚本本轮一个都没审**。它们同为手跑产物、无自动门；`sql/audit/` 下 6 个采集脚本同理。⇒ **登记为下一轮顺位，不冒充已覆盖。**
- 脚本的 `[可选 2]`（改 `node_probe_runs` / `request_logs_2026_09` 的 autovacuum 参数）与 `[可选 3]`（删 `request_state_transitions_pkey`）**本轮未审**——它们是注释里的建议，不在执行路径上。
- `db/request_logs_view_padded_columns.go:48-49` 文档漂移仍未修（207 号已登记，刻意）。
- 债务仍剩 **20** 条。`go test ./...` 全量未跑；CI 未运行；本机 `core.hooksPath` 未设置 ⇒ 本次推送未经 pre-push 门。

---

## 八、下一轮顺位

1. **审 `sql/fixes/` 剩下的 15 个手跑脚本 + `sql/audit/` 的 6 个采集脚本**——它们是本仓**唯一一类既无自动执行路径、又无任何门**的产物，形态与本轮这份完全一样。
2. 把本门泛化成**族级**：`sql/**/*.sql` 里所有含 `DROP TABLE|DROP INDEX|TRUNCATE` 的文件都必须给出「空表/影响面」门禁，并自报覆盖面。
3. 修 `db/request_logs_view_padded_columns.go:48-49`。
4. 继续还债（`admin/quality_correlations.go` / `admin/provider_models.go` / `admin/session_sanitize_matches.go`）。

---

## 九、playbook 新增

### §120 用**名字的形状**（字典序）冒充**时间/身份**先后

`relname < 'xxx_default'` 想表达「早于今天」，但字典序里数字排在字母**前** ⇒ 谓词恒为真。
同类高发：`created_at::text < now()::text`（字符串比时间）、`id < 9999999999` 冒充「老数据」、`v1` < `v2` 冒充「版本序」。

⇒ **判别方法**：任何「用标识符的字符串形式做大小比较」的判据，先问「这个序与我要的序**同向**吗」。
⇒ **修法**：把隐含的量**解析出来**再比（`substring(relname from '…(\d{8})$')::date < current_date`），或直接用**真实来源**（分区边界 `pg_get_expr(relpartbound)`、时间列）。
⇒ **审计任何守卫时**同时问一句：**如果这个谓词恒为真，测试会红吗？** 恒真的谓词不是门，是装饰——而它常常**看起来**像一道门。

### §121 门禁要取**事实**，不要取**估算**或**形状**

`reltuples` / `reltuples = 0` / `pg_size` / `count(*)` 出现在报表里是**正常**的，出现在**门禁**里就是**把估算当事实**：
- 估算与事实脱钩的典型窗口：**「空时被 ANALYZE 过、之后写入行、下次 ANALYZE 之前」**——恰好是一张刚被清空的表最危险的时刻；
- PG14+ 从未 ANALYZE 的表甚至报 **-1**，方向与直觉相反。

⇒ 删表/清空前必须用 `EXISTS (SELECT 1 … LIMIT 1)` 这类**真探针**。
⇒ ⚠️ **自相矛盾检测法**（本轮最好用的一条）：**在同一个文件里找「它自己怎么说这个字段不可信」**。本脚本末尾写「625 张表从未 ANALYZE」，中段却拿 `reltuples` 当门禁——**两句话互相打脸，比任何外部评审都容易发现**。

### §122 建门时给自己的**误报**留负控

本轮新门**自己**制造了两次误报（按 `;` 切段误伤 `CONTINUE` 式守卫；`strings.Contains(l, "relname <")` 误伤 `<>`），两次都是先跑出来才发现的。

⇒ **建门时预置一条「正确写法不得被误报」的负控**，并把它和「错误写法必须被报出」的负控**并列**。
⇒ 只测「能抓到坏东西」的门，会在收紧过程中不知不觉变成「什么都报」的门，而那比没门更坏。

### §123 「工具可用吗」要用**活性**探针，不能用**存在性**探针

`exec.LookPath("python3")` 判的是「**有个叫 python3 的文件在磁盘上**」，而我们要问的是「**这个工具能执行吗**」。两者在 Windows 上会分叉：**App Execution Alias 桩**满足前者、不满足后者。

⇒ 分叉后的两种失败形态**都错**：
  - 真没装 ⇒ 门 **SKIP** ⇒ 静默不跑（playbook §104 的「非致命即静默」）；
  - 装了桩 ⇒ 门 **FAIL**，且报错形态是「工具坏了」⇒ **把人引向错误的排查方向**（去查那个根本没被执行的 `.py`）。

⇒ **How to apply**：任何 `Skip` 前置条件都要问「这个条件保证的是**我需要的那件事**吗」。
  - 需要「能跑」⇒ 用 `exec.Command(x, "-c", "pass").Run()`；
  - 需要「有文件」⇒ 用 `os.Stat`。
  - 二者不要混用，**尤其不要用一个更弱的条件去守一个更强的结论**。
⇒ 顺带一条审计判据：**看到一个门被 `Skip` 掉，就去查它在本机到底跳过没有**。本轮正是靠「这个测试为什么红」才发现「它其实一直没跑」——**红与跳过都可能意味着门没运行，但只有后者是沉默的**。
