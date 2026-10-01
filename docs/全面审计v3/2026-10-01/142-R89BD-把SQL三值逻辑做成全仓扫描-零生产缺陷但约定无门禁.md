# 142 号｜R89-BD：把 §36（SQL 三值逻辑）做成全仓扫描 —— **零生产缺陷**，本仓已有一套近乎统一却无门禁的 NULL 守卫约定

- 日期：2026-10-01
- 轮次：R89-BD
- 起因：141 号我自己**两次**踩 SQL 三值逻辑的坑（`<>` 吃掉 62% 的 NULL 行），
  于是把这条教训做成**全仓机械扫描**——本仓有多少查询在**可空列**上做排除式比较且没有 NULL 守卫。
- **结论先行（这是一个否定结果，且我认为它比大多数肯定结果更有交付价值）**：
  1. **扫描覆盖面**：321 处 SQL 比较站点（非测试代码），
     抽出 **40+ 个不同列名**，逐列与 `information_schema.columns.is_nullable` 交叉核对。
  2. **对可空列的排除式比较**共筛出 **5 处「疑似未加守卫」候选**，**逐处读原文后全部证伪**：
     守卫要么在同一行的前半句，要么**就在上一行**（我的行级 grep 看不见）。
     ⇒ **本仓生产代码在这条陷阱上零缺陷。**
  3. **但约定完全没有门禁**：这套「排除类比较必须带 `IS NOT NULL AND`」的写法
     **不在任何文档里、不由任何测试强制**。下一个少写半句的人，会得到一条**静默少算**的查询。
     **建议加一条守卫测试**（机械可做，成本极低）——登记为待裁决，不擅自动手。
  4. **⚠️ 真正咬人的不是本仓代码，是审计者自己写的临时查询**（141 号两次都出在我自己手上）。
     ⇒ §36 的现实形态是「**审计脚本比生产代码更危险**」。

---

## 一、扫描方法（可复现）

### 步骤 1：抽出所有「排除式比较」站点

```bash
grep -rn --include=*.go \
  -E "\b[a-z_]+\.[a-z_]+ +(<>|!=) +('[^']*'|\$[0-9])|\b[a-z_]+ +(<>|!=) +'[^']*'" . \
  | grep -v "_test.go"
# ⇒ 321 处
```
（含 `=` vs 常量的比较，因为 `col = 'x'` 遇 NULL 同样不匹配。）

### 步骤 2：抽出被比较的列名并去重 → 40+ 列

`status` / `tenant_id` / `client_model` / `raw_model_name` / `end_user_id` / `owner_user` /
`application_code` / `billing_mode` / `unavailable_reason` / `compression_strategy` / `error_kind` …

### 步骤 3：逐列与真实可空性交叉核对

```sql
SELECT count(*) FILTER (WHERE is_nullable='YES')||'/'||count(*)
FROM information_schema.columns
WHERE table_schema='public' AND column_name='<col>';
```
**这一步是本扫描的关键**：
只有**可空列**上的 `<>` 才可能吃掉 NULL。
实测大量列是 **NOT NULL**（`model_aliases.status`、`providers.egress_profile/base_url`、
`self_check_runs.status`、`stats_*.traffic_class`…）⇒ **它们的 `<>` 天然安全，直接排除。**

### ⚠️ 扫描的已知偏差（诚实声明）

我的 grep 是**行级**的，**看不到上一行的守卫** ⇒
**本次扫描会「多报」候选（偏向发现），不会「漏报」候选。**
⇒ 5 处候选全部人工读原文后证伪 ⇒ **结论方向是保守的**（真实未守卫数 ≤ 5，实测为 0）。

## 二、5 处候选逐个证伪

| # | 位置 | 列（可空性） | 看着像的问题 | 实际 | 真库验算 |
|---|---|---|---|---|---|
| 1 | `admin/models.go:806-812` | `models_canonical.family`（5/5 表可空） | `mc.family <> ''` 无守卫 | ✅ **同一 WHERE 已有 `mc.family IS NOT NULL`** | NULL 行 **0/960** |
| 2 | `domains/credentialstate/popularity_tracker.go:86-87` | `request_logs_hot.client_model`（40/41 可空） | `client_model != ''` 无守卫 | ✅ **守卫在上一行** `AND client_model IS NOT NULL` | — |
| 3 | `admin/model_name_mapping.go:401-403` | `provider_models.standardized_name`（6/7 可空） | 两处 `!=` 无守卫 | ✅ **上一行 `IS NOT NULL`**；且 `!= pm.raw_model_name` 在其后才评估，NULL 安全 | — |
| 4 | `credentialhealth/checker.go:557/596` | `cmb/mo.unavailable_reason`（8/8 可空） | `<> 'model_probe_broken'` 无守卫 | ⚠️ 确实无守卫，**但被下游守卫完全覆盖**（见下） | `available=FALSE AND reason IS NULL` = **0 行** |
| 5 | `admin/pricing.go:904/911` | `model_offers.billing_mode`（19/19 可空） | `!= 'free'` 无守卫 | ✅ 无 NULL 行，**无影响** | NULL = **0/1994** |
| 6 | `admin/routing.go:4760` | `providers.catalog_code`（2/2 可空） | `<> ''` 无守卫 | ✅ **语义本就要求排除**（无 catalog_code 的 provider 不登记） | NULL = **21 行**，被排除是设计意图 |

### 候选 4 值得单独说（它是真的没有守卫，但确实无害）

`credentialhealth/checker.go:548-560`（恢复 UPDATE）：
```sql
WHERE ...
  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'   -- ✅ NULL 安全
  AND cmb.unavailable_reason <> 'model_probe_broken'           -- ❌ 不 NULL 安全
  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
  AND COALESCE(cmb.unavailable_recover_at,
               cmb.unavailable_at + INTERVAL '30 seconds') IS NOT NULL
  AND COALESCE(cmb.unavailable_recover_at,
               cmb.unavailable_at + INTERVAL '30 seconds') < now()
```
第 2 个条件会把 `unavailable_reason IS NULL` 的行（= 已健康）全部排除。
**但第 4/5 个条件对同一批行也返回 NULL**（`unavailable_recover_at` 与 `unavailable_at` 都为 NULL
⇒ `COALESCE(NULL, NULL + interval)` = NULL ⇒ `NULL < now()` = NULL ⇒ 不满足）
⇒ **第二道门把第一道门没挡住的部分也挡住了。**
真库确认：`model_offers` 里 `available=FALSE AND unavailable_reason IS NULL` = **0 行**。

⚠️ **诚实定性**：**当前无影响**，但**这是「靠下游守卫偶然兜住」的写法**，
一旦有人日后把恢复条件放宽（例如允许 `unavailable_at IS NULL` 的行也参与恢复），
**第一道门的漏洞立刻显形**。⇒ 登记为 **P3（一词级加固，不改行为）**。

## 三、真正的发现：约定存在，但**无文档、无线禁**

本仓事实上遵守着一条相当严格的约定：

> **对可空列做「排除某类值」的比较时，必须写成 `IS NOT NULL AND col <> 'x'`。**

抽样统计（`client_model` 的 9 处站点）：
`admin/top_problems.go:240`、`admin/routing.go:2693`、`admin/logs.go:1261`、
`admin/work_types.go:294/340/375/444`、`admin/analytics.go:156/220/251`、
`admin/auto_route.go:538/713/764`、`bg/mv_consistency.go:235/290`
—— **14 处全部带守卫**，包括 `is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> ''`
这种把两个可空列一起守住的写法。**这不是巧合，是有人在踩过坑之后形成的习惯。**

**但**：
- **没有任何文档写着这条约定**（`AGENTS.md` / `docs/audit/playbook/conventions.md` 里，
  §36 是 141 号本轮新加的，**描述的是踩坑而不是本仓约定**）；
- **没有任何测试强制它** ⇒ 一次无关的重构/格式化/新人提交，就可能悄悄去掉半句。

**建议（待裁决 58，不擅自动手）**：加一条**机械守卫测试**——
遍历非测试 `.go` 里的 SQL 字符串，对**可空列**上的 `<>` / `!=` / `= 'literal'`
断言**同一语句内出现 `IS NOT NULL` 或 `COALESCE(...)` 兜底**。
- 成本：一条测试，无迁移、无运行时影响；
- 收益：把一条**存在于事实中但不存在于文档中**的约定，变成会报警的门禁；
- ⚠️ **边界**：它只能做**行级/语句级**启发式检查，**必然有假阳性**
  （跨行守卫、`COALESCE` 在 SELECT 而非 WHERE 等）⇒ **应当报告而非 fail**，
  或维护一份**已确认豁免清单**，**不要**做成会卡住 CI 的硬门。

## 四、⚠️ 本报告的方法论收获：§36 的真实形态是「**审计脚本比生产代码更危险**」

| | 本仓生产代码 | 我在 141 号写的临时查询 |
|---|---|---|
| `<>` 是否带守卫 | **几乎 100% 带** | **两次都没带** |
| 结果 | 零缺陷 | 一度算出「**63.4% 的流量是探针**」（真值 1.28%，**错 50 倍**） |

**⇒ 纪律升级（§36 增补）**：
> **临时审计查询的所有 `WHERE` 条件，默认假设「可空」，直到核对过 `information_schema`。**
> **一条「跳过 NULL 行」的过滤，在生产代码里通常有人替你补上守卫，
> 在你自己临时敲的 SQL 里则毫无保护——而后者恰恰是要用来给生产代码定性的那一条。**
>
> **配套**：
> ① 报「占比」前先打一次 `count(*) FILTER (WHERE col IS NULL)`；
> ② 排除类条件统一写 `IS DISTINCT FROM`（它对 NULL 返回 TRUE，语义明确）；
> ③ **用带 NULL 的查询去给「NULL 相关的缺陷」定性，是自指的** ——
>    141 号的教训：`134 号 §「统计必须用含 hot 的视图」` 是同一族的另一面，
>    **都是「连测量工具本身都要先验证它没被待验证的问题污染」。**

**同族**：§27 / §28 / §32 / §33 / §34 / §35 —— 「数字本身干净，但尺子/前提可能不是这一把」。

---

## 五、诚实边界

- **未改动任何生产代码或数据库结构**（全部只读查询）。
- **扫描是行级 grep**，会多报候选、不会漏报候选（方向保守），
  **但也不排除在跨行/拼接/模板化 SQL 中存在我未覆盖的形态** ——
  ⚠️ **本报告的结论是「在可核对的站点上零缺陷」，不是「全仓数学证明」。**
- **未对 `<>` 的参数化比较（`col <> $1`）逐个核对**（抽到但未展开）。
- **候选 4 的「无害」依赖当前下游守卫与真库 0 行两个前提**，任一变化结论就要重评。
- **P3 加固建议（`cmb.unavailable_reason` / `mo.unavailable_reason` 补 `IS NOT NULL`）
  与守卫测试建议都属待裁决，本代理不擅自动手。**
