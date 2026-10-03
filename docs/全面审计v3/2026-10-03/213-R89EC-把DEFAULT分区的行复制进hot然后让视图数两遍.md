# 213 号 | R89-EC — 把 DEFAULT 分区的行**复制**进 hot，然后让视图**数两遍**

> **变更面**：`sql/fixes/fix-missing-request-wal-hot.sql`（7 条，含 1 条 P1）。
> **零 Go 生产代码改动。**
> 触发方式：212 号登记的顺位第 1 条 —— 家族里最后一个没逐行审过的脚本。

---

## 一、🔴 F1：视图重复计数，而且**必然触发**

原第 4 节（`:146-148`）：

```sql
INSERT INTO request_wal_hot
SELECT * FROM request_wal_default
ON CONFLICT (request_id, created_at) DO NOTHING;
```

它只**拷贝**、**不从 `_default` 删除**。而：

```
sql/migrations/startup/332_request_wal_default_partition.sql:31
  CREATE TABLE public.request_wal_default PARTITION OF public.request_wal DEFAULT;
```

⇒ `_default` **就是 `request_wal` 的 DEFAULT 分区**
⇒ `SELECT * FROM request_wal` **本来就包含**这批行
⇒ 视图是 `request_wal_hot UNION ALL request_wal` ⇒ **这批行各出现两次**。

**为什么说它必然触发而不是罕见分支**：332 是 **startup 迁移**，任何正常库里 `_default` 都存在
⇒ 该分支**每次跑都会走**。

**为什么更糟一层**：`_default` 是给 `promote_request_wal_default_batch(interval, integer)`
用的**溢写暂存桶**（该函数把这些行 `INSERT` 进正式月分区后 `DELETE` 掉）。
本脚本把行复制进 hot 却把原件留在 `_default` ⇒ 原件稍后还会被**提升**进月分区
⇒ **同一批行最终在 hot 和月分区里各存一份 ⇒ 永久重复**，且不随 hot 保留期过期而消失。

⇒ **改法**：第 4 节**整节移除**。这些行已经通过 `request_wal`（含 DEFAULT 分区）
出现在视图里，不需要也不应该再拷一份。若确实要把它们**移走**（而非复制），
那是 `promote_request_wal_default_batch` 的职责。

---

## 二、另外 6 条

| # | 位置 | 问题 | 定级 |
|---|---|---|---|
| ② | 全文 | **没有** `\set ON_ERROR_STOP on` ⇒ 中途失败 psql 打印错误后**继续往下跑**，末尾照样打印「修复完成！」 | 🔴 |
| ③ | `:117-120`、`:147` | 视图与迁移都用 `SELECT *`。`UNION ALL` 两侧靠**位置**对齐 | 🟡 |
| ④ | `:48` | `WITH (fillfactor=90)` 漏了权威表上的 **5 个 autovacuum 参数** | 🟡 |
| ⑤ | `:99-102` | `RAISE WARNING … RETURN;` 之后**仍执行** `CREATE VIEW` ⇒ 报更难懂的 `relation does not exist` | 🟡 |
| ⑥ | `:15-19, :60-64, :93-97, :140` | `pg_class` / `information_schema` 判据**没限定 `nspname`** | 🟡 |
| ⑦ | `:177` | `hot_columns_count < 15` 是**魔数**，且该判据无 schema 限定 | 🟡 |

### ③ 为什么值得单独说

权威定义（`01-schema.sql` 里的 `request_wal_with_current_month`）**本来就是逐列枚举的**：

```sql
CREATE VIEW public.request_wal_with_current_month AS
SELECT request_wal_hot.request_id, …, request_wal_hot.compression_meta
FROM public.request_wal_hot
UNION ALL
SELECT request_wal.request_id, …, request_wal.compression_meta
FROM public.request_wal;
```

**显式枚举正是因为 `UNION ALL` 靠位置对齐**。本脚本用的 `SELECT *` 是那个脆弱写法。

⚠️ 诚实说明：本机实测两张表**各 17 列、列名与顺序完全一致**，所以 `SELECT *` **目前能跑**。
⇒ 这里是**消除脆弱性**，不是修一个当前正在发生的错。

### ④ 为什么在 hot 表上尤其要紧

`request_wal_hot` 的存在意义就是**高频写入 + 8h 保留 + 批量提升**。权威表上的
`autovacuum_vacuum_scale_factor=0.05 / vacuum_threshold=10 / analyze_scale_factor=0.02 /
analyze_threshold=50` 正是为这种写入模式调的。一张新建的 hot 表丢掉这五个参数，
在没有其它策略兜底时会**按 PG 默认（0.2 / 50）**回收 ⇒ 膨胀快得多。

⚠️ 但 `CREATE TABLE IF NOT EXISTS` 对**已存在**的表**不会改**这些参数
⇒ 本条只对本脚本**真的创建**该表的情况生效。这点已写进代码注释。

### ⑦ 判据为什么从「魔数」改成「与基线逐列比对」

`hot_columns_count < 15` 有三个问题：魔数无出处、**列数相等但名字/顺序不同**对
`ON CONFLICT (request_id, created_at)` 和视图的**位置对齐**同样致命、且无 schema 限定。
⇒ 改为把列名集合与权威基线**逐列比对**，缺/多都点名。

---

## 三、**没有**发现的问题（如实登记，避免下轮重复审）

213 号逐列核对了本脚本的两份 `CREATE TABLE`：

- `request_wal_hot`：**17 列**，与 `01-schema.sql` **完全一致、顺序也一致** ✓
- `request_wal_bodies`：**4 列**，完全一致 ✓

⇒ **这两份列清单本身是正确的**，本轮**没有**改动它们。
（对照 212 号：我在 `normalize-columnar-historical.sql` 里**手写**列清单是错的，
而这里没有 —— 因为这两份恰好与基线同步。⇒ **同一仓里两种结果，说明「手写清单」这件事
本身就是风险点**，而不是某一份具体清单有错。）

⚠️ 但要注意一个**同族不一致**（212 号已记过，这里再确认一次）：
本脚本 `:47` 建的是 `PRIMARY KEY (request_id, created_at)`，
而 `01-schema.sql:21907` 里 `request_wal_hot_pkey` 是 `PRIMARY KEY (request_id)`。
⇒ **同表同名主键，两处定义不同**。若本脚本在一张缺表的库上创建了表，
那库的主键形状就与基线不同 ⇒ 又是一次「脚本与基线漂移」。

---

## 四、诚实边界

- **未起真进程、未执行任何 SQL、未连 PG/Redis/Docker。** 本轮改的这个 `.sql`
  **未经真库验证**：新增的列集合比对（`unnest` + `string_to_array`）、
  `schemaname` 判据、显式列清单视图，**都只做了静态审读**。首次执行前**必须**在测试库单独跑。
- F1 的因果链是**静态坐实**的（`:31` 的 `PARTITION OF … DEFAULT` + 视图定义 + 拷贝语句），
  **不需要真库**就能确定；但「这批行到底有多少」**未取数**，不做规模估计。
- 本轮**没有改任何 Go 代码、也没有改任何门**；`sql/schema` 的门只用于确认仍绿。
- `sql/fixes/` 家族至此 **11 份全部逐行审过**
  （208: storage-reclaim；209: bodies-partition-v2；210: cn-rename / hot-unique-constraint /
  bodies-reattach；211: audit×2；212: normalize-columnar / wal-hot-pk；213: wal-hot-missing）。
  ⚠️ **"审过"不等于"修过"**：`2026-09-20-canonical-dedup-cleanup.sql` 经复核**判定干净**、
  `2026-09-21-unmapped-cn-rename.sql` 的路由语义**待裁决 83**、
  `normalize-columnar-historical.sql` **功能被主动关闭**。
- `sql/audit/` 7 份：A1/A2 已修（211），A3 根因已修（211 的 runner 完整性校验），
  A4（`collect:302` 用 `reltuples=0` 当「空」）与 A5（四种 LIMIT / 两种 TOAST 口径 /
  三种 `seq_scan` 阈值）**仍未处理**。
- 债务 `DEBT(R47)` 仍为 **20 条**，本轮未增减。
- 本机 `core.hooksPath` 未设置 ⇒ 推送**未经 pre-push 门**；**CI 未运行**；本机**无 `bash`**。
- **未裁决项**未变：待裁决 **83**、**82**、**81**、**79**、**80**。

---

## 五、可重放证据

```powershell
# F1 的因果链（三处都必须成立）
Select-String -Path sql/migrations/startup/332_request_wal_default_partition.sql -Pattern "PARTITION OF"
Select-String -Path sql/schema/01-schema.sql -Pattern "CREATE VIEW public.request_wal_with_current_month" -Context 0,4
git grep -n "INSERT INTO request_wal_hot" -- sql/fixes/    # 213 号后：可执行语句已为 0，只剩注释

# ③ 权威定义是显式列清单
Select-String -Path sql/schema/01-schema.sql -Pattern "request_wal_with_current_month" -Context 0,2

# ④ 权威表的 autovacuum 参数
Select-String -Path sql/schema/01-schema.sql -Pattern "autovacuum_vacuum_scale_factor='0.05'"

# 门仍然绿
cd sql/schema; go test -run TestHandRunSql -count=1 .
```

---

## 六、playbook 新增

### §142 「**复制**」和「**移动**」在有 `UNION ALL` 的视图上是两件完全不同的事

`fix-missing-request-wal-hot.sql` 把 `_default` 的行 `INSERT … SELECT` 到 hot，
**没有 `DELETE` 源行**。而 `_default` 是 `request_wal` 的 DEFAULT 分区、视图又是
`hot UNION ALL request_wal` ⇒ **每行被数两遍**；再叠加「原件稍后被 promote 提升进月分区」
⇒ **永久重复**，且不随 hot 的 8h 保留期消失。

⇒ **判别方法**：写「迁移/搬迁」之前先问一句「**源还在不在？**」。
  - 源**保留** + 目标也在读路径上 ⇒ 这不是迁移，是**复制**，会产生重复计数；
  - 源**会被下游继续处理**（提升/清理） ⇒ 复制出来的副本**不会**跟着消失。
⇒ 副本只有在**目标独占该数据**、且**源从所有读路径上消失**时才安全。
⇒ **分区表特有的放大**：`DEFAULT` 分区的行**本来就随父表出现在任何父表查询里**，
  所以「把它搬进 hot」永远是在父子两条读路径上各放一份。

### §143 手写列清单：同一仓里两份脚本、两种结果 —— 说明**做法**本身是风险点

212 号我在 `normalize-columnar-historical.sql` 里手写的列清单**几乎逐列不符**
（`request_logs` 实际 40+ 列，没有 `status_code`/`created_at`/`canonical_model`）；
213 号同一动作在 `fix-missing-request-wal-hot.sql` 上**恰好正确**（17 列、顺序也一致）。

⇒ 同一件事、同一轮、两个结果 ⇒ 区别不在**哪份清单有错**，而在
**「清单是不是从权威来源派生」**。
⇒ **修法**：用 `pg_attribute` / `information_schema` 现取，或直接引用仓库里已有的
  权威定义；**不要把列名当字面量抄进脚本**。
⇒ 过期的列清单是**运行期才报错**，且报错形态是「列不存在」，
  读者会去查分区、查 AM、查迁移 —— **真实原因只是清单抄错了**。
⇒ 与 §139（grep 不到 ≠ 不存在）同源：**手写的字面量只覆盖它写下来的那一刻。**

### §144 `CREATE TABLE IF NOT EXISTS` **不会**改已存在表的存储参数

补 `autovacuum_*` 之类参数时必须写明生效边界：`IF NOT EXISTS` 让语句在表已存在时
**整体跳过**，包括 `WITH (...)` 子句。
⇒ **判别方法**：给「确保表存在」的脚本补参数时，先确认这次参数是**新建时给的**
  还是**对既有表的变更**；后者需要 `ALTER TABLE … SET (…)`，不是 `CREATE TABLE IF NOT EXISTS`。
⇒ 本仓的 hot 表正是最吃这套参数的（高频写入 + 短保留）⇒ 漏掉的后果是膨胀，
  **而且没有任何报错**。
