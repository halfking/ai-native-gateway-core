# 212 号 | R89-DZ — 一份脚本声称做三件事，**只做了第一件**，并把列存改回行存后打印 `done`

> **变更面**：① `sql/fixes/normalize-columnar-historical.sql`（**中止** + 修两处可确定的错误）；
> ② `sql/fixes/fix-request-wal-hot-primary-key.sql`（按名字查 ⇒ no-op；改为只报告真形状 + 真门禁）；
> ③ **210 号文档加更正附录**（我的 F2 依据被削弱，结论方向不变）。
> **零 Go 生产代码改动。**
> 触发方式：210 号登记的顺位第 2 条 —— 这三个脚本当时只做了普查，**没有逐行语义审查**。

---

## 一、🔴 F1：一份「声称做三件事、只做一件、还报告成功」的脚本

`sql/fixes/normalize-columnar-historical.sql`。头部 `:6-8` 写：

> This script rebuilds candidate_failure_logs and the request_logs partitions as
> **rowstore tables**, **applies lower() to the model name columns**, and
> **converts them back to columnar**.

结尾 `:119` 写 `=== columnar historical lowercase rebuild: done ===`。

### ① 声称「applies lower()」—— `lower()` **从未出现在任何写路径上**

全文件 `lower(` 的全部出现位置：

| 行 | 性质 |
|---|---|
| `:7` `:21` `:41` `:75` | **注释** |
| `:44` `:78` | **`\echo` 标签** |
| `:70` `:116` `:117` | **两段验证 SELECT**（`WHERE … <> lower(…)`，只是**用来数**的） |

真正的复制是：

- `:55-60` `INSERT INTO candidate_failure_logs SELECT id, ts, credential_id, … raw_model_name, …` —— **逐值原样**；
- `:103` `INSERT INTO %I SELECT * FROM %I` —— **原样**。

⇒ **花掉「重写整张大关系、阻塞写入」的代价，数据一个字节都没变。**
连它自己的验证都会报出非 0 的 mixed 行数 —— 而那只是个 `SELECT`，**不会中止**（`:34` 的
`ON_ERROR_STOP` 只管**错误**，不管「结果不符合预期」）。

### ② 声称「converts them back to columnar」—— **该步骤根本不存在**

全文件 `SET ACCESS METHOD` / `USING columnar` **零命中**。脚本只把 columnar 分区
重建成 **heap**，然后打印 `done`。

⇒ **跑完它，`request_logs` 的历史分区就从 columnar 变成 heap**，而这与本仓
「所有的大数据表是以 hot+分区（columnar）表完成」的架构要求**直接冲突**
（体积与查询性能显著劣化），并且 `promote_request_logs_hot_to_partition_interval_integer`
会继续往这些分区灌数据。

⚠️ 脚本自己其实**知道**这个风险——头部 `:16-18` 写着
「For `request_logs` (partitioned parent), the partition layout changes … and would
need to be **hand-coordinated with the partition manager**」—— 但它既没协调，也没做回转。

### ③ `ATTACH PARTITION` 的边界参数是垃圾

`:97-102` 把**同一个** `pg_get_expr(c.relpartbound, c.oid)` 同时当作 `FROM` 和 `TO`：

```sql
EXECUTE format('ALTER TABLE %I ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)',
               'request_logs', part.part_name,
               (SELECT pg_get_expr(...) WHERE c.relname = part.part_name || '_col_old'),
               (SELECT pg_get_expr(...) WHERE c.relname = part.part_name || '_col_old'));
```

而 `pg_get_expr(relpartbound, …)` 返回的是**整条** `FOR VALUES FROM (…) TO (…)` 子句，
不是某一个端点 ⇒ 拼出来是
`FOR VALUES FROM ('FOR VALUES FROM (…) TO (…)') TO ('FOR VALUES FROM (…) TO (…)')`。

### 212 号的选择：**在动数据之前中止**

修好这三件事里的两件（`lower()`、边界）不难，但**第 3 步「转回 columnar」无法在本机
实现并验证**（无 PG、无 `citus_columnar`）。

⇒ 一份会**静默把列存改回行存、还自称 done** 的脚本，**中止比运行安全**。
⇒ 加了 **preflight `\quit`**：默认直接退出并说明原因；必须显式
`psql -v r89_allow_incomplete_columnar=1` 才放行，且在文件头部写清放行前要先做完 A/B/C 三项。

### 顺带修掉的两处（保留在文件里，供后续修好时使用）

- 写路径**真正**应用 `lower()`；
- 两段验证从「打印一个数」改成**会中止的门禁**（§130：列不出失败分支的验证不是验证）；
- 补 `n.nspname='public'`（`:89` 原来只按 `relname` 匹配父表，与 210 号在
  `fix-request-logs-bodies-reattach-partitions.sql` 记下的**同一形态**）。

⚠️ **一处我自己写错又被自己抓到的**：我第一版把 `INSERT` 的列清单**手写**了一份
（`id, ts, tenant_id, … status_code, … canonical_model, … created_at`）。
回查 `01-schema.sql:7191` 发现 `request_logs` 实际有 **40+ 列**，
**没有 `status_code`、没有 `created_at`**，也**没有 `canonical_model`**（是 `canonical_id bigint`）
⇒ 我那份清单**几乎逐列不符**。
⇒ 改为**从 `pg_attribute` 现取**（按 `attnum` 顺序，只对已确认的 `client_model` /
`outbound_model` 套 `lower()`）。
**写死列名在这类「同族多副本 + 各自漂移」的仓库里必然过期**，而过期的列清单是
**运行期才报错**，报错形态是「列不存在」，读者会去查分区、查 AM，而**真实原因只是清单抄错了**。

---

## 二、🔴 F2：按**名字**检查主键 ⇒ 在任何与基线一致的库里都是 **no-op**

`sql/fixes/fix-request-wal-hot-primary-key.sql` 头部声称：

> 修复 request_wal_hot 和 request_logs_hot 表**缺少主键**的问题 /
> 影响：**所有请求日志写入失败**

而仓库权威基线 `01-schema.sql:21827,21907` 写着：

```sql
ADD CONSTRAINT request_logs_hot_pkey PRIMARY KEY (request_id);
ADD CONSTRAINT request_wal_hot_pkey  PRIMARY KEY (request_id);
```

⇒ **两张表本来就有主键**，只是建在 `request_id` **单列**上。

原脚本的存在性检查是 `WHERE conname = 'request_wal_hot_pkey' AND conrelid = …`
⇒ **名字恰好命中** ⇒ 打印「✓ 主键已存在，跳过创建」⇒ **整份脚本什么都不做**，
而头部宣称它修了一个「所有写入失败」的问题。

⚠️ **更值得警惕的是它的失效方向**：检查问的是「**有没有那个名字**」，
而不是「**有没有那个形状的主键**」。一旦主键存在但**名字不同**（被某个迁移重命名过），
它会**再加一个主键** ⇒ PG 直接报 `multiple primary keys for table`。
这与 210 号 R89-DV 的「查错了系统目录」、playbook §129 的
「问『有没有任意一个』还是『有没有那一个』」是同一族。

### 改法

1. 本脚本**不再创建任何主键/索引**，只把两张表主键的**真实形状**（按 `pg_constraint`
   的实际列清单）**报出来**；
2. §3 的「验证」改成**真门禁**：问「`(request_id, <时间列>)` 上到底有没有唯一索引」
   —— 没有就 `RAISE EXCEPTION`，并指明该跑同目录的
   `fix-request-logs-hot-unique-constraint.sql`；
3. 补 `\set ON_ERROR_STOP on`（原来没有），所以末尾的「修复完成！」横幅
   原来**在失败后照样会打印**；
4. 删掉「✓ 主键已创建」这类**报告一件没有发生的事**的输出。

⇒ §4 那段「自插 + `ON CONFLICT` + 同表 `DELETE` 清理」的端到端测试**本来就写得不错**，
但因为前面全是 no-op，它**从未真正证明过任何事**；212 号之后它是**真门禁**。

### 这一改还顺带消掉了一个「两份脚本互相看不见」的问题

同目录两份脚本都声称修 `request_logs_hot` 的唯一性：

| 脚本 | 做法 | 判据 |
|---|---|---|
| `fix-request-logs-hot-unique-constraint.sql`（210 号） | 加 **UNIQUE INDEX** `(request_id, ts)` | 查 `pg_index`（212 已修） |
| `fix-request-wal-hot-primary-key.sql`（本轮） | 加 **PRIMARY KEY** `(request_id, ts)` | 查 `conname` |

⇒ 两者**互相看不见**：一个建的是唯一索引、另一个建的是主键，判据查的目录还不同。
若都"修好了"，同一张热表上会出现**两份同列的唯一索引**。现在第二个脚本**只报告并指向第一个**。

---

## 三、🟡 对 210 号 F2 的**更正**（自我推翻）

212 号发现 `01-schema.sql` 里还有一条我 210 号**没查**的记录：两张 hot 表都有
`PRIMARY KEY (request_id)`。

⇒ 210 号写的「`01-schema.sql` 里 `request_logs_hot` **没有任何唯一索引**」**不准确** ——
它**有**一个唯一对象（主键），只是**不是索引、也不是这两列**。

**但 210 号的核心结论不变**：`ON CONFLICT (request_id, ts)` 要求**恰好**建在
`(request_id, ts)` 上的唯一索引/约束；只有 `(request_id)` 的主键**不满足**该推断。
⇒ 210 号改用 `pg_index` + 列清单恰好相等，方向是对的。

被削弱的是那句「热表丢了唯一索引、父表有」：真实含义是**两张表的唯一性口径本来就不同**
（`request_logs` 允许同一 `request_id` 多行；`request_logs_hot` 不允许）。

**教训**：「在某处 grep 不到 X」⇒「不存在 X」。我搜的是 `UNIQUE` / `UNIQUE INDEX` /
`idx_*_request_id_ts_unique` 这几个**词形**，而 `PRIMARY KEY (request_id)` **一个词都不沾**
⇒ **判据只认自己会写的那个词形**，对另一种表达同一约束的对象完全失明。
这与 205 号「引错权威来源」、210 号 F1「正则漏了表别名」同族。
（更正全文已作为附录写进 210 号文档。）

---

## 四、`fix-missing-request-wal-hot.sql`：本轮**未逐行审完**，如实登记

210 号顺位第 2 条里还剩它。已知它 `:100` 有
`RAISE WARNING 'request_wal 父表不存在，无法创建视图'` 后继续
—— 这是**已知类别**（非致命 ⇒ 对自动化不可见）的又一实例，但**本轮没有读完、没有定级**。
⇒ 登记为**未完成项**，不在本轮冒充「已审」。**顺位第 1 条**。

---

## 五、诚实边界

- **未起真进程、未执行任何 SQL、未连 PG/Redis/Docker。** 本轮改的**两个** `.sql`
  **全部未经真库验证**：
  - `normalize-columnar-historical.sql` 的 `relpartbound` 文本拆分、`pg_attribute` 列清单生成、
    `c.conkey = ARRAY[…]::smallint[]` 形状比对，**都只做了静态审读**；
  - `fix-request-wal-hot-primary-key.sql` 的列清单比对同理。
  ⇒ 首次执行前**必须**先在测试库单独跑。
- 本轮**没有改任何 Go 代码**，也**没有改任何门**；`sql/schema` 的门在本轮只用于确认
  它仍然绿。
- `normalize-columnar-historical.sql` 的 **preflight 默认中止** ⇒ 它的功能被**主动关闭**。
  这是有意的：它当前的终态是「静默把列存改回行存并报告 done」。
  ⇒ 恢复功能需要按文件头部 A/B/C 三项做完并在真库验证，**不应由审计代理单方面放行**。
- 债务 `DEBT(R47)` 仍为 **20 条**，本轮未增减。
- 本机 `core.hooksPath` 未设置 ⇒ 推送**未经 pre-push 门**；**CI 未运行**；
  本机**无 `bash`**（211 号已登记）。
- **未裁决项**未变：待裁决 **83**（老 `-cn` 名是否继续可解析，本轮 F2 使其更紧迫）、
  **82**、**81**、**79**、**80**。

---

## 六、可重放证据

```powershell
# F1① lower() 从未出现在写路径上
Select-String -Path sql/fixes/normalize-columnar-historical.sql -Pattern "lower\("
#   ⇒ 全部落在 注释 / \echo / 两段验证 SELECT

# F1② 「转回 columnar」这一步不存在
Select-String -Path sql/fixes/normalize-columnar-historical.sql -Pattern "access\s+method|using\s+columnar"
#   ⇒ 零命中

# F1③ ATTACH 边界取的是整条子句、且 FROM/TO 用了同一个值
#   旧 :97-102（已改）

# F2 主键本来就存在
Select-String -Path sql/schema/01-schema.sql -Pattern "request_logs_hot_pkey|request_wal_hot_pkey" -Context 0,2

# F2 我手写列清单的错误（212 号自抓）
#   01-schema.sql:7191 CREATE TABLE public.request_logs (  → 40+ 列，无 status_code / created_at / canonical_model

# 门仍然绿
cd sql/schema; go test -run TestHandRunSql -count=1 .
```

---

## 七、playbook 新增

### §139 「在某处 grep 不到 X」**推不出**「不存在 X」

212 号查 `request_logs_hot` 的唯一性时，搜的是 `UNIQUE` / `UNIQUE INDEX` /
`idx_*_request_id_ts_unique` 这几个**词形**，而该表其实有
`PRIMARY KEY (request_id)` —— **一个词都不沾** ⇒ 210 号据此写了「没有任何唯一索引」。

⇒ **通用形态**：同一个数据库概念有**多种表达**（约束 vs 索引 vs 域 vs 触发器 vs 视图 vs 物化视图），
而**文本搜索只认你会写的那一种**。
⇒ **判别方法**：下「不存在」这个结论之前，先确认**枚举过这类对象在 PG 里的全部载体**
  （至少 `pg_constraint.contype IN ('p','u','f','c')` + `pg_index.indisunique`），
  而不是只 grep 一个词形。
⇒ 与 §129、§120、210 号 F1（正则漏表别名）同族：**判据只覆盖了对象的一种写法**。

### §140 一份脚本**声称做三件事、只做一件、还报告成功**，比三件都做错更坏

`normalize-columnar-historical.sql` 的三步里：第 1 步 ✓、第 2 步（`lower()`）从未实现、
第 3 步（转回 columnar）**根本不存在**——而结尾打印 `done`。
⇒ 它**不是"做错"，是"没做还说自己做了"**。这类脚本比明显写错的更危险：
  运维看到 `done` 就认为存储布局已按预期处理。
⇒ **判别方法**：对每个交付脚本，把头部/结尾的**每一个动词**列出来，
  逐个在代码里找到它的**实现**。找不到的每一个，都是一条独立缺陷。
⇒ **且它自己的注释里往往已经写着风险**（本脚本 `:16-18` 就写了
  「partition layout changes … would need to be hand-coordinated with the partition manager」），
  ⇒ 复述 §131：**在同一个文件里找「它自己怎么说这件事有风险」**。

### §141 改动**会改变对外行为或数据形态**时，审计代理的默认动作是**让问题可见**，不是**替人做决定**

第 3 步「转回 columnar」在无 PG 的环境里无法实现并验证。212 号因此**中止整份脚本**
（preflight `\quit`），而不是「修好一半然后放行」。

⇒ **判别方法**：当修复的前置条件**在本机不可验证**时，优先级排序是
  **① 让危险动作无法发生** > ② 修好能修的 > ③ 放行。
⇒ 中止必须**可显式放行**（`-v <flag>`），并把「放行前要先做完什么」写成清单，
  否则中止就变成了永久拆除能力。
⇒ 与 210 号 F3 的「不擅自改 `deprecated`→`active`」是同一条纪律的两次应用：
  **改语义 = 产品裁决；改不上去 = 门禁 + 登记。**
