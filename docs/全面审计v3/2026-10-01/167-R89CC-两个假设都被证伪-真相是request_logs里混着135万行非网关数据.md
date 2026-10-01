# 167 号 · R89-CC —— 顺着 166 号自己留的判别维度查下去：两个假设都被证伪，真相是 `request_logs` 里混着 **135 万行非网关数据**

> **日期**：2026-10-01
> **轮次**：R89-CC（第 67 轮，审计第 167 号）
> **类型**：假设证伪 ×2 + 一条影响面很广的新缺陷 + playbook §60
> **改动生产代码**：无　**改动数据库**：无（全部只读 SELECT）
> **归属声明（§44③）**：`ps aux` 确认本机**无网关进程** ⇒ 真库由**远端实例**写入。
> **上一轮**：166 号（补上 110 号「真库无法量化」的缺口）

---

## 〇、起手：166 号自己留的那句话

166 号结尾写：

> 判别所需的一个维度（下轮做）：按 `api_key_id` / `tenant` / 端点路径拆这批流量。

166 号给了两个竞争解释：

- **(c)** 某个客户端/压测/爬虫在裸打无状态接口 ⇒ 这类流量本来就不该进 `session_turns` ⇒ 非缺陷。
- **(d)** 会话头识别坏了 ⇒ 本该带 session 的请求被当成无状态 ⇒ 那是真缺口。

**本轮就是去分辨 (c) 与 (d) 的。答案是：两个都不对。**

---

## 一、结论摘要

| # | 结论 | 级别 |
|---|---|---|
| **F1** | 「无 session」行的 `request_wal` 命中率 **6/241,031、5/239,209、0/262,432**；而「有 session」行是 **99.99%** ⇒ 这批**根本没走网关主路径** | 判别证据 |
| **F2** | 这批 **1,357,556 行**（占 9 月 `request_logs` 的 **63.0%**）**100% 是 `request_class='immediate'`**、**只有 28 行有 `api_key_id`**、仅 7.6% 有 `prompt_tokens` ⇒ **不是网关请求，是另一个写入方** | **待裁决 69，P2** |
| **F3** | ⚠️ **行为在 2026-09-25 变过一次**：此前这批**也**被双写进 `session_turns`（09-24 只缺 25 行），09-25 起**几乎全部不再进** | 待裁决 69 的时间边界 |
| **F4** | ⚠️ **影响面警告**：凡是以 `request_logs` 行数作分母的历史统计，9 月口径**可能虚高约 63%** | **范围警告（非新缺陷）** |
| **F5** | ⚠️ **我自己的「09-25 起凭空多出」框架也是错的** —— 这批行从 **09-03 就在**，09-25 变的不是「有没有」，是「占比」 | 自我更正 |

---

## 二、F1：`request_wal` 命中率把两个假设一起排除

`request_wal` 的一个硬事实（166 号已测）：**986,722 行，100% 有 `gw_session_id`**。它是请求主路径的旁证表。

```sql
SELECT r.ts::date, count(*) AS rl_no_sess,
       count(*) FILTER (WHERE w.request_id IS NOT NULL) AS has_wal_row
FROM request_logs r LEFT JOIN request_wal w ON w.request_id = r.request_id
WHERE r.ts >= '2026-09-24' AND r.ts < '2026-09-27'
  AND (r.gw_session_id IS NULL OR r.gw_session_id='') GROUP BY 1;
```

| 日 | 无 session 行 | **有 wal 行** | 无 wal 行 |
|---|---|---|---|
| 09-24 | 241,031 | **6** | 241,025 |
| 09-25 | 239,209 | **5** | 239,204 |
| 09-26 | 262,432 | **0** | 262,432 |

**对照组（同日「有 session」的行）**：

| 日 | 有 session 行 | 有 wal 行 |
|---|---|---|
| 09-24 | 151,842 | **151,829**（99.99%） |
| 09-25 | 131,558 | **131,555**（99.99%） |
| 09-26 | 142,367 | **142,366**（99.99%） |

⇒ **两个假设同时被排除**：

- **(d) 会话头识别坏了** —— 若如此，这些请求会走完整链路、**会有 WAL 行、有 prompt_tokens、有 api_key**。实测**三项全无** ⇒ 不是「解析失败」，是**压根没走那条路**。
- **(c) 客户端/压测裸打无状态接口** —— 若如此，**会有 `api_key_id`**（网关入口必然鉴权）。实测 09-26 这批 **0 行有 api_key_id** ⇒ 不是从网关入口进来的。

---

## 三、F2：这是另一个写入方，不是网关流量

```sql
SELECT count(*), min(ts)::date, max(ts)::date,
       count(*) FILTER (WHERE api_key_id IS NOT NULL) AS with_apikey,
       count(*) FILTER (WHERE prompt_tokens>0)          AS with_prompt_tok,
       count(*) FILTER (WHERE request_class='immediate')AS class_immediate
FROM request_logs WHERE gw_session_id IS NULL OR gw_session_id='';
```

```
 total_no_sess | first_day  |  last_day   | with_apikey | with_prompt_tok | class_immediate
---------------+------------+-------------+-------------+-----------------+-----------------
       1357556 | 2026-09-03 | 2026-10-01  |          28 |          102556 |         1357556
```

| 特征 | 这批 | 同日正常流量 |
|---|---|---|
| `request_class='immediate'` | **1,357,556 / 1,357,556 = 100%** | — |
| 有 `api_key_id` | **28 行**（0.002%） | 99.98% |
| 有 `prompt_tokens>0` | 102,556（7.6%） | **100%** |
| 有 `request_wal` 行 | **≈0** | 99.99% |
| 有 `gw_session_id` | 0 | 100% |

**模型分布也不像真实流量**（09-26，`request_class='immediate'`）：

```
   class   |    model     |  n
-----------+--------------+-------
 immediate | gpt-4        | 11644
 immediate | gpt-4o-mini  | 11214
 immediate | gpt-4o       | 11140
 immediate | qwen-plus    | 10948
 immediate | mixtral-8x7b | 10938
 immediate | o1-preview   | 10918
```

**跨 6 个不同厂商的模型几乎完全均匀（~11k each）**。真实流量会高度集中在少数模型上；这种均匀分布是**生成/合成**的典型形态。

⇒ **定性：这是一条独立写入 `request_logs` 的合成/测试数据流，不经网关主路径。**

**⚠️ 诚实边界**：本轮**没有**查到写入方是谁（无 pid、无来源标记、无 `api_key_id`）。**「是谁写的」需要你或运维提供**（`request_class='immediate'` 这个取值本身的含义也值得核对——它是否本就是某条内部/测试写入路径的枚举值）。

---

## 四、⚠️ F3：09-25 变的是「是否双写」，不是「有没有」

这是 166 号说对了、但**框架错了**的地方。166 号写「09-25 起每天**凭空多出** 24–26 万条」——**不对**：

```sql
-- 逐月占 request_logs 的比例
SELECT date_trunc('month',ts)::date, count(*),
       count(*) FILTER (WHERE gw_session_id IS NULL OR gw_session_id='') AS no_sess,
       round(100.0*count(*) FILTER (WHERE gw_session_id IS NULL OR gw_session_id='')/count(*),1)
FROM request_logs GROUP BY 1;
```

| 月 | `request_logs` 总行 | 无 session | 占比 |
|---|---|---|---|
| 2026-09 | 2,154,225 | 1,356,558 | **63.0%** |
| 2026-10 | 1,536 | 998 | 65.0% |

**这批行从 09-03 就在，占比一直 ~63%。** 166 号之所以看起来像「突变」，是因为 166 号量的是**「缺 `session_turns` 的行」**（09-24 只缺 25 行 → 09-25 缺 143,278 行）。

⇒ **真正的事件是：2026-09-25，`session_turns` 停止镜像这批非网关行。**

| 日 | 无 session 行 | 其中缺 `session_turns` |
|---|---|---|
| 09-24 | 241,031 | **25**（0.01%） |
| 09-25 | 239,209 | **143,278**（60%） |
| 09-26 | 262,432 | **262,432**（100%） |

**⇒ 从行为上看，09-25 那次切换是「对的」**（非网关行本就不该进 `session_turns`）。
**⇒ 但 `request_logs` 自己仍然留着这 135 万行**，且它们占了 9 月的 63%。

---

## 五、⚠️ F4：一条影响面很广的范围警告

**这不是一个新缺陷，但它让我必须回头看自己前面几十轮的数。**

`request_logs` 是本仓最常被拿来当分母的表。**9 月它的 63% 是非网关行。**

⇒ **凡是以 `request_logs` 行数作分母的历史统计，9 月口径可能虚高约 63%。**

**本轮主动回查并需要标注的**（不逐条重算，登记为待复核）：

| 轮次 | 用到的数 | 风险 |
|---|---|---|
| 136 号 | 「会话总数 849,605 / 最近 8h 有行 748」 | 用的是 `session_summaries`，**不受影响** |
| 136 号 | `request_logs` 裸读子查询的覆盖率讨论 | 讨论的是 hot 分区 8h 窗口，**结论不受影响** |
| 153 号 | 丢弃站点普查 | 那是**代码**普查，非真库计数，**不受影响** |
| 158–164 号 | `request_attachments` 10,652 行 | 独立表，**不受影响** |
| **166 号** | 「`request_logs` 非空 `compression_strategy` 48,711」 | ⚠️ **该列 2.26% 的占比可能受污染**，绝对值仍有效 |

**⇒ 结论：本仓已落盘的真库量化里，暂未发现被这条污染**（多数用 `session_turns` / `request_attachments` / `session_summaries` 等独立表）。**登记为一条常驻警告，不做无证据的全面推翻。**

---

## 六、⚠️ F5：我在 166 号的框架也是错的

166 号 §四写「**09-25 起每天凭空多出 24–26 万条无 session 流量**」。

**实测：这批行 09-03 就在，占比一直 63%。** 166 号把「缺 `session_turns` 的行数变化」误读成了「无 session 流量数量变化」。

⇒ **已在 166 号文件加警示框回灌，正文不回改。**
⇒ **教训与 §46/§57 同族但更细一层**：
**我查的列（`missing from session_turns`）和我描述的现象（无 session 流量总量）不是同一个量。**
**列名对了、SQL 没错、结果干净自洽 —— 但那句话描述的是另一件事。**

---

## 七、⚠️ F6：自摆乌龙第 10 次 —— **第一次落在「静默」那一类**

166 号记的第九次是三次响亮 `ERROR`。本轮**五次**，其中**第四次是静默的**：

| # | 我写的 | 真相 | 形态 |
|---|---|---|---|
| 1 | `request_wal.status` 当作 `request_logs` 的列 | 无关联查询 | 响亮 `ERROR` |
| 2 | 同上，`count(status)` | — | 响亮 `ERROR` |
| 3 | 同上，样本查询 | — | 响亮 `ERROR` |
| 4 | **`SELECT status, count(*) … FROM request_logs r LEFT JOIN request_wal w`** | ⚠️ **`status` 未限定表名 ⇒ PG 绑定到 `w.status`（request_wal 的列）** | **🔇 静默** |
| 5 | `GROUP BY coalesce(nullif(gw_session_id,''),'(no_sess)')` | 把有 session 组按**每一个 session_id** 分组 ⇒ 14 万组 ⇒ `count(DISTINCT …)=0` ⇒ 除零 | 响亮 `ERROR` |

**第 4 条我差一步就写进报告。** 我当时看到的结果是：

```
 status |    n    | in_wal
--------+---------+---------
        | 262432  |      0
```

读起来是「**这批旁路流量的 `status` 全部为空**」——一个干净、自洽、**完全可以当结论用**的句子。

**实际上**：`status` 是 `w.status`；这 262,432 行**全部 `w.request_id IS NULL`（JOIN 没命中）** ⇒ 列必然全 NULL。
**「全空」不是事实，是「我没 JOIN 上」的副产品。**

**我为什么停下来**：playbook **§57** 写的「同一批结果里『通过』与『全灭』都要怀疑」，加上**自己写的判别问题**——「旁路流量没有 status」如果成立，那正常流量也该有 status 可供对照，而我从没查过对照组。

⇒ **playbook §60 新增**（见下）。

---

## 八、playbook §60 新增

> **§60 JOIN 里未限定表名的列，会静默绑定到另一张表 —— 若那一侧没命中，NULL 列看起来像一个真实结论**

§47/§48/§50/§57 记的检测器缺陷都是「判据写错 ⇒ 输出错误数字」。本轮第 4 次踩的是**更隐蔽的一层**：

```sql
-- 错：status 未限定，PG 绑定到 w.status（request_wal 的列）
SELECT status, count(*) FROM request_logs r LEFT JOIN request_wal w ON …  -- 返回 262432 行、status 全空
-- 对：加前缀
SELECT r.<col>, count(*) FROM request_logs r LEFT JOIN request_wal w ON …
```

**为什么它比前几次更危险**：前几次的判据写错后，输出的是**一个具体的错值**（`0/80`、61 而不是 28）。
**这一次的输出是「全空」——而「全空」本身就是一个看起来合理的结论形状。**
**它不需要你算错，只需要你没 JOIN 上。**

**⇒ 两条落地规则：**

1. **JOIN 多表时，所有出现在聚合/输出里的列一律加表前缀。**
   本仓尤其危险：`request_wal` 与 `request_logs` **共享列名**（`request_id` / `gw_session_id` / `tenant_id` / `prompt_tokens` / `completion_tokens` / `compression_strategy` / `error` / `status`…）。

2. **「某列全空」这个结论，必须配一个「JOIN 命中率」的分母。**
   本轮如果先查了 `has_wal_row`（= 0/262,432），就立刻知道那不是「status 为空」而是「这批行根本不在另一张表里」。
   **⇒ 判据：看到 `GROUP BY <col>` 输出 `(empty)` 占 100% 时，先问「这一列非空的对照组长什么样」。**

**同族**：§47（`\S`+`.match()` 静默吞行）/ §48（箭头前有没有 `:=`）/ §50（返回值真的存在吗）/ §57（判据自己写错）/ **§59（尺子错的两种形态）**。
**§59 说「尺子错分静默与响亮」，本条说「静默里最危险的那一类：不是算错，是没连上」。**
