# R77 轮：`/api/admin/compression/stats` 的「200 = 假成功」与载荷聚合归因（106s→不可得 ⇒ 显式标注 + 必需查询 1.9× 提速）

> 标记：**P11-R77**（承接 R75 的 P11-R75、R76 的 P11-R76 命名标记）
> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`
> 日期：2026-10-24 07:05 ~ 08:10
> ★ 对生产的写入：**0 次落库写**；本轮只做了一次**应用发布**（无 schema 变更、无 pending 迁移）。

---

## §0 一句话结论

★ 端点返回 **200 @10.003s**，但**响应体是残缺的**：`hourly_series` **长度 0**、
`estimated_original_tokens` / `summary_mode_rows` / `token_band_*` **整段缺失**。
⇒ ⇒ **真因不是「慢」，是「一条昂贵且可选的查询吃光了整个端点的 ctx 预算，
把后面的必需查询全部饿死，而它们的失败被 `slog.Warn` 静默吞掉」**。

⇒ ⇒★ **而且那条昂贵查询的成本是【固有的】**：它要读窗口内**全部** `request_body`
载荷（24h 窗口 **1,278 MB**），**单独就要 34.3 秒** ⇒ 超端点 10s 预算 **3.4×**
⇒ ⇒ ★ **它不能靠「摘要化」解决**（CO-5 digest 模式**被刻意关闭**，
理由是它会**销毁唯一的完整审计副本** —— 见 §6）。

⇒ ⇒★ **⇒ 本轮的正确修法不是「让它快」，而是**：
① 把三条必需查询改快（**1.9×**）；② 把昂贵可选的那条**排到最后**并给**独立预算上限**；
③ **拿不到就显式说拿不到**（`degraded` / `degraded_reason`，仓内既有约定）。

---

## §1 现象：200 ≠ 有数据

生产实测（154 active 8781，build 2818）：

```
/api/admin/compression/stats   http=200  time=10.003s
  compressed_total        17007
  compression_rate        1
  hourly_series           list(len=0)          ← ★ 空数组
  strategy_distribution   {'none': 14132, 'sliding_window_count': 1696, 'sliding_window_token_absolute': 1179}
  total_outbound_tokens   286833350
  total_requests          17007
  ★ 缺失：estimated_original_tokens / summary_mode_rows / token_band_{below,preliminary,forced}
```

⇒ ★ **前端类型里 `hourly_series` 是【必填】**（`web/src/api/compression.ts` 的
`CompressionStats`）⇒ ⇒ **图表恒空** ⇒ ⇒ **这才是用户可见的损害**。
⇒ ★ 而 `token_band_*` / `estimated_original_tokens` 是 `omitempty` 的可选字段
⇒ 它们的消失在响应里**没有任何痕迹** ⇒ ⇒ **调用方无从分辨「真的是 0」与「超时了」**。

---

## §2 归因：4 条查询的成本分解（生产 252 共享库，EXPLAIN ANALYZE 实测）

| 查询 | 作用 | 实测 | 判决 |
|---|---|---|---|
| Q1 `agg` | 策略分布 + token 汇总 | **860 ms**（3 轮交错中位） | 快 |
| Q2 `estimated-orig` | 原始 token 估计（**读全部载荷**） | **34,334 ms** | ★ **超预算 3.4×** |
| Q3 `band` | token_band 计数 | **41~55 ms** | 快 |
| Q4 `bucket` | 小时桶（`hourly_series`） | **501 ms** | 快 |

⇒ ⇒ ★ **端点 ctx = 10s，4 条串行共用** ⇒ Q2 一条就把它吃光 ⇒
⇒ **Q3、Q4 根本没跑完** ⇒ ⇒ **Q4 的 `hourly_series` 恒空** ⇒
⇒ ⇒ ★ **「端点慢」的真正形态不是「慢」，是「**前面的步骤把后面的步骤饿死，
而饿死被静默吞掉**」**。

### §2.1 Q2 的成本为什么是固有的

| 探针 | 读数 | 含义 |
|---|---|---|
| 窗口内 turns 行数 | 16,878 | — |
| `request_body` **存储字节**（`pg_column_size`，**不 detoast**） | **1,278 MB** | 压缩后仍 1.28 GB |
| 只读 bodies 载荷（`length(request_body::text)`） | **34,334 ms** | ★ **detoast + jsonb→text 重新序列化** |
| **1 小时窗口**的同一条查询 | **938 ms** | ⇒ ★ **随窗口线性标度** |
| `_gw_body_summary` 信封行数 | **0 / 16,878** | ⇒ ★ **摘要模式在生产完全没生效** |

⇒ ⇒★ **⇒ 「让 Q2 变便宜」的三条路**：
① **摘要化** ⇒ **已被否决**（§6）；② **换便宜口径**（`pg_column_size`）
⇒ ★ **数值含义会变**（压缩后体积 ≠ 文本长度）⇒ **需属主裁决**；
③ **后台预计算** ⇒ 架构 + schema 变更 ⇒ **需属主裁决**。
⇒ ⇒★ **⇒ 本轮两条都不擅自做**，端点改为「拿不到就显式说拿不到」。

---

## §3 三条候选形态的实测（★ 全部在生产上量过）

★ 测量方法：**同会话交错 A/B、3 轮**（共享库负载波动可达 8×，单次读数不可信 —— §3.1）。

| 形态 | R1 | R2 | R3 | 中位 |
|---|---|---|---|---|
| **OLD** agg（bodies LEFT JOIN） | 1087 | 860 | 812 | **860 ms** |
| **NEW** agg（EXISTS 半连接，**只求值一次**） | 447 | 464 | 469 | **464 ms** |
| PURE-turns（**完全不接 bodies**） | 149 | 151 | 149 | 150 ms |
| **OLD** bucket | 501 | 501 | 503 | **501 ms** |
| **NEW** bucket | 201 | 346 | 199 | **201 ms** |

⇒ ⇒★ **⇒ 收益拆解**：bodies 腿的成本 **OLD = 860−150 = 710 ms**、
**NEW = 464−150 = 314 ms** ⇒ **减半**；端点必需三条查询合计
**1,402 ms → 720 ms（1.95×）**。

### §3.1 ★ 一条被否掉的形态 + 一条量具教训

- ✘ **「预聚合 bodies 后 hash 半连接」**（`LEFT JOIN (SELECT DISTINCT request_id …)`）
  ⇒ **3,251 ms** ⇒ ★ **比现状还慢** ⇒ 否掉。
- ★★ **量具教训（第三次踩同一族）**：★ 我第一次量「Go 产物 agg」得到 **3,425 ms**，
  而同一时刻手建的同形状只有 **416 ms** ⇒ 差 **8×** ⇒
  ⇒ **同会话交错复测 ⇒ 两个形状同价**（R1 949/774、R2 506/507 ms）
  ⇒ ⇒ ★★ **⇒ 单次读数在共享库上可以差 8×** ⇒ ⇒ **「旧形状 1,355ms → 新形状 416ms（3.3×）」
  这个我先前写下的数字是【单次读数】⇒ 真正的倍数是 **1.85×**（3 轮交错中位）**
  ⇒ ⇒ ★ **本节的所有数字都用 3 轮交错中位**（LESSONS §313）。

### §3.2 ★★ EXISTS **求值次数**是关键（不是「有没有用 EXISTS」）

| 形态 | EXISTS 求值次数 | 实测 |
|---|---|---|
| 旧形状（LEFT JOIN） | — | 860 ms |
| EXISTS 写在 `FILTER` + `CASE` 各一遍 | **2 次** | **762 ms** |
| EXISTS 写在**子查询投影**里，外层引用该列 | **1 次** | **464 ms** |

⇒ ⇒★ **⇒ 「用 EXISTS」本身不够；「EXISTS 只求值一次」才是那条不变量**
⇒ ⇒★ 这也是**最容易回退**的一处 ⇒ ⇒ 必须有门（§5.2 判据①）。

---

## §4 语义等价性（冻结窗口，逐行对拍）

★ 冻结窗口 `'2026-10-23 07:00:00+08' → '2026-10-24 07:00:00+08'`（生产 252）：

| 对拍 | 结果 |
|---|---|
| bucket：OLD vs NEW | **28 行逐字相同** ✅ |
| agg：OLD vs NEW | **数值逐行相同**（`none 14094/14094/120965592/120965592` 等 3 行全等）✅ |
| 差异 | **仅列别名**（我手建版叫 `tok`，产物叫 `total_tok_after`）⇒ **不是数据差异** |

### §4.1 ★★ 一个必须一起读的量具事故

★ 我第一版对拍脚本用 `psql \o` 把结果写到 `/tmp/eq-*.txt` ⇒ **那是【容器内】路径**
⇒ 我在**宿主机**上 diff 两个**不存在**的文件 ⇒ `diff` 对两个空流返回 0
⇒ ⇒ **输出「✅ 逐行相同」的假阳性** ⇒ ⇒ ★ 是「文件缺失」这条自检把它抓出来的
⇒ ⇒ ★★ **通用式：「diff 返回 0」不等于「两者相同」—— 必须同时断言【两边都非空】**
（LESSONS §314）。

### §4.2 ★★★ 顺带把上一轮的「误报」也用数据钉死了

R76 轮我写过一条：「`compression_rate` 恒等于 1 ⇒ **口径错** ⇒ 该指标无信息量」。
★ **R77 轮的生产实测把它推翻了**：

| 探针 | 读数 |
|---|---|
| 窗口内 turns 行数 | 16,878 |
| LEFT JOIN 后的行数 | 16,878 ⇒ **放大 = 0** |
| 窗口内 distinct request_id | 16,878 ⇒ bodies 侧 request_id **当前唯一** |
| 其中 `outbound_body IS NOT NULL` | **16,878（= 全部）** |
| ⇒ ⇒ | **`withOutbound == cnt`** ⇒ ⇒ **两段代码逐字等价 ⇒ 线上行为正确** |

⇒ ⇒★ **⇒ 已订正并落仓**（提交 `8ccc3ca81`）：「计数被当布尔用」是**潜在**缺陷
（组内出现混合时 `compression_rate` 会被高估），**不是当前缺陷** ⇒ LESSONS §312。

---

## §5 代码改造与门

### §5.1 改造（`admin/compression_stats.go`）

1. **bodies 腿 → EXISTS 半连接**（`agg` 与 `bucket`），EXISTS **只求值一次**
   （算在子查询的投影里，外层聚合引用 `rl.has_outbound`）；
2. **窗口谓词下推**进子查询 ⇒ EXISTS 只对窗口内的行求值；
3. **步骤化**：4 条查询变成 `[]compressionStatsStep`（**顺序是数据**），
   昂贵且可选的那条**排到最后**并带**独立预算上限**（4s）；
4. **每步独立 ctx**：预算 = min(该步上限, 端点剩余时间) ⇒ ★ **一条慢步骤吃不掉后面的**；
5. **显式降级**：`degraded`（**恒发**，无 `omitempty`）+ `degraded_reason`
   ⇒ 沿用仓内既有约定（`admin/dashboard_board_aux.go`）；
6. `CompressedTotal += withOutbound`（★ 当前逐字等价，**结构性**修正）。

★ **诚实边界**：★ **Q2 的 SQL 一字未改** —— 它的成本是固有的（§2.1），
改它只能动口径或动存储 ⇒ **两条都需属主裁决** ⇒ ⇒ 本轮只改它的**位置与预算**。

### §5.2 门 `admin/compression_stats_shape_test.go`（5 条，全部断言**真实产物**）

| 判据 | 钉住什么 |
|---|---|
| ① bodies 必须 EXISTS、不得 LEFT JOIN；半连接必须保留 `rb.outbound_body IS NOT NULL` | 口径不漂 + 免疫行放大 |
| ①' **就地阳性对照**：`estOrig` **必须**保留 LEFT JOIN（它要读正文长度） | ⇒ 证明判据不是「一律禁 LEFT JOIN」 |
| ② **EXISTS 只许求值一次** + 必须落在 `AS has_outbound` 投影里 + 外层引用 `rl.has_outbound` | ★ 本轮最易回退的一处 |
| ②' **就地阳性对照**：同一把尺子必须能把「两次 EXISTS」数成 2 | ⇒ 证明判据不是恒绿 |
| ③ 昂贵可选的那一步**必须排最后** + **必须有独立且小于总预算的上限**；其余步骤**不得**有上限 | 预算纪律 |
| ④ 慢步骤**不得**让后面的步骤「看起来成功但没有数据」（后面必须被显式记为 `skipped`） | 预算纪律（行为级） |
| ⑤ 任何一步不可得 ⇒ `degraded` + 原因**点名该步**；全部成功 ⇒ **不得**报降级 | 记账（+ 反向自检） |

★ **既有门 `admin/body_envelope_test.go` 的 `TestCompressionStatsEstimatedOrigSQL`**
⇒ 它守的是「摘要信封适配不能丢」⇒ **意图仍成立**，只是取 SQL 的入口从包级 `var`
变成了构造器 ⇒ ⇒ **判据改成断言真实产物**（`compressionStatsSQL("", "").estOrig`）
⇒ ⇒ **门比原来更强**（原来断言包级变量，现在断言**真正发出去的 SQL**）。

### §5.3 变异验证（4 臂全红 + md5 逐字还原）

| 变异 | 目标门 | 结果 |
|---|---|---|
| M1 bodies 腿回退成 LEFT JOIN | 判据① | ✅ 红 |
| M2 EXISTS 求值两次 | 判据② | ✅ 红 |
| M3 昂贵那一步挪到最前 | 判据③ | ✅ 红（`序列=[estimated-orig agg band bucket]`） |
| M4 去掉昂贵那一步的预算上限 | 判据③ | ✅ 红 |

★ ★ **还原阶段还逮到一次事故**：`git checkout -- admin/compression_stats.go`
**把【未提交的】R77 改动一起回退了**（我的改动还没提交）⇒
⇒ 是**还原后的残留自检**（build failed）抓到的 ⇒ 从文件级备份逐字节恢复（md5 一致）⇒
⇒ ⇒ ★★ **通用式：变异还原必须用【文件级备份 + md5】，不能用 `git checkout --`**
（`git checkout --` 恢复的是 **HEAD**，不是你的工作区！）⇒ LESSONS §315。

---

## §6 ★★ 「让它更快」这条路在**存储层**被封死了（必须带着这条否决理由）

`domains/hooks/observability/telemetry/body_summary.go` 的文件头注释（逐字）：

> CO-5 … briefly replaced bodies persisted to `_request_logs_bodies_hot` with digest
> envelopes. **That write mode is disabled**; the envelope implementation remains only
> for compatibility tests and admin readers of rows written while it was active.
> … new request logs always retain full bodies. The former platform flag is ignored
> because **persisted false values survived the default change and continued
> destroying the only complete audit copy of large requests**.

⇒ ⇒ ★★ **⇒ 「开启摘要模式让 Q2 变便宜」是一条【已被否决过】的路**，
否决理由是**审计完整性**（它会销毁大请求的唯一完整审计副本）
⇒ ⇒ ★ **⇒ 下一轮不要把它当成「没试过的优化」再提一次**
⇒ ⇒ ⇒ **要提，必须连同这条否决理由一起提。**

---

## §7 发布（★ 一条工装缺陷的发现）

★ **发布前的 pending 集**：发布树基线 = `412f7f95`（**不含 902**）
⇒ ⇒ ★ **pending 集 = 空** ⇒ ★ **发布树的迁移文件集不决定 pending**
（生产 `schema_migrations` 决定）⇒ ⇒ **零 schema 变更**。

★ **build_seq**：R76 轮出现的 **2819 → 2818 倒退**，本轮查清了根因：

- `scripts/bump-version.sh` 的规则：`seq = max(version.json 的 build_seq, **机器级 floor**) + 1`；
- 机器级 floor 在 `$HOME/.llm-gateway-go/build_seq.floor`（**跨 checkout 共享**，锁内读改写）；
- ★ 实测该文件内容 = **2818**、mtime = **2026-10-24 06:34**（= R76 那次发布）
  ⇒ ⇒ **R75 那次发布（2819）没有更新这个 floor**
  ⇒ ⇒ ⇒ ★★ **⇒ floor 是【per-HOME】的 ⇒ 并行会话在另一个 HOME / 另一台机器上发布时，
  本机 floor 兜不住它的 seq ⇒ seq 会倒退 / 撞号**
  ⇒ ⇒ ★ **⇒ 这是一条【发布工装缺陷】，不是我的操作失误**
  （R76 轮我把它记成「基线太旧」，只说对了一半）。

⇒ ⇒★ **本轮的处置**：发布树 `version.json` 用 **origin/main 的 2823**
⇒ 部署自动 +1 ⇒ **published 2824** ⇒ ⇒ **单调（2819 → 2824）**、且 floor 被推进到 2824
⇒ ⇒ 且 **不与未来的 main 基线发布撞号**（main 的 version.json 同时被推进到 2824 ⇒ 下一版 2825）。
⇒ ⇒ ★ **下一轮工装修复项**：★ **floor 应以【发布目标】为准，而不是【发布者本机】** ——
发布前先读目标机上已发布的最大 seq，取 `max(本机 floor, version.json, 目标机最大 seq) + 1`。

---

## §8 下一轮靶子（D4 榜单剩余，本轮只动了 compression/stats）

| 端点 | 实测 min | 归因 | 建议修法 | 风险 |
|---|---|---|---|---|
| `data-lifecycle/degradation/status` | 30s（超时） | **不是 SQL**：Redis 全 keyspace SCAN ×4 + 备份 gzip **解压两遍**；且**无 ctx 超时** | 4 次 SCAN 合并成 1 次；删掉重复解压；加 10s ctx | 低（纯 Go、零口径） |
| `db-status` | 19.6s | 同上（同一 `GetTTLStats`） | 同上 | 低 |
| `turns/sessions` | 30s（超时） | **无 ctx 超时** + 逐行相关子查询（其注释依赖的 `idx_sessions_session_id` **已被 876 删除**）+ 排序前 2 个 LATERAL | 先加 ctx；再两段式取 MAX(partition_date) | 中 |
| `pending-responses/stats` | 4.3s | `SafeHGetAll` 把**正文**也拉回本地；cutoff 硬编码 **+100 年** | 改 `HMGET` 三字段 | 低 |
| `sessions/inspector-stats` | 2.4s | `session_summaries`+`session_dim` 全量 JOIN，**无时间窗** | 加窗 + 覆盖索引 | 中（口径需裁决） |
| `dashboard/session-overview` | 2.6s | **9 条并行全扫** `session_summaries`（`first_request_at` **无索引**） | 建 `first_request_at` 索引（同时惠及 #5、#7、#10） | 低（纯新增索引） |
| `turns/sessions/filter-options` | 2.7s | 查询写 `ss.first_request_at`，但现存索引是 `last_request_at` ⇒ 用不上 | 加一条**冗余谓词** `AND ss.last_request_at >= $window`（输出逐字不变） | 低 |
| `data-lifecycle/{stats,metrics,partitions}` | 7.8s+ | 口径要求**全量历史** | ⇒ **必然触碰可观测数字**（估算 vs 精确） | ⇒ **属主决策** |

⇒ ⇒ ★★ **一条索引解三个端点**：`session_summaries(first_request_at)`
（`session_summaries` 829,341 行 / 826 MB / **未分区**）
⇒ ★ startup 迁移在事务里 ⇒ **不能 CONCURRENTLY** ⇒ 建索引期间阻塞写 ⇒ **需属主授权**。

---

## §9 通用式（本轮新增，进 LESSONS §313~§315）

1. ★★★ **共享库上的单次读数可以差 8×** ⇒ **必须同会话交错 A/B + 多轮中位**；
   ⇒ ★ **「先测出来的倍数」本身也要用同一把尺子复核**（本轮 3.3× → 真值 1.85×）。
2. ★★★ **「diff 返回 0」≠「两者相同」** ⇒ 必须同时断言**两边都非空**
   （本轮 `psql \o` 写的是**容器内**路径 ⇒ diff 两个不存在的文件 ⇒ 假阳性「逐行相同」）。
3. ★★ **「用 EXISTS」不够，「EXISTS 只求值一次」才是那条不变量**
   ⇒ 成本随求值次数线性增长 ⇒ **必须落在子查询投影里、只算一次**。
4. ★★★ **变异还原必须用【文件级备份 + md5】，不能用 `git checkout --`**
   （后者恢复的是 **HEAD**，不是你的工作区 ⇒ 未提交的改动会被一起抹掉）
   ⇒ ★ **还原后的残留自检是最后一道网**（本轮它抓到了一次真实事故）。
5. ★★ **「计数被当布尔用」是【潜在】缺陷，不是【当前】缺陷**
   ⇒ 判定「口径错了」之前必须先量「当前数据下这个口径算出来的值是什么」。
6. ★★ **一条昂贵且可选的查询，会把后面的必需查询饿死，而饿死被静默吞掉**
   ⇒ 修法：**顺序 + 独立预算 + 显式标注**（三件一起，缺一件都还会复发）。
