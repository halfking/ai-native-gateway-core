# R76 轮：`/api/admin/compression/sessions` 生产恒 500 的根因与修复（106s → 0.36s，逐字等价）

> 标记：**P11-R76**（承接 R75 的 P11-R75 命名标记）
> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`
> 日期：2026-10-24 05:50 ~ 07:00
> ★ 对生产的写入：**0 次落库写**；本轮只做了一次**应用发布**（无 schema 变更、无 pending 迁移）。

---

## §0 一句话结论

★ **D4 后置基线里那条「落前 200 / 落后 500 @10.0s」的端点，生产真实成本是
**65.8 ~ 105.9 秒**（D4 看到的 10.0s 只是 handler ctx 的截止线，不是真实成本）。
根因不是「窗口太宽」也不是「缺索引」，而是：**CTE 把 bodies 的 jsonb 载荷
（`outbound_body` / `request_body`）搬过了整个 24h 窗口**，
而生产 `session_bodies_hot` **9,499 行占 1,058 MB**（单行载荷 ~110KB）。
⇒ ⇒ 改成「载荷只在分页之后取」后，生产实测 **0.36s**，且**冻结窗口逐行等价**（差异 0 行）。

---

## §1 现象与「为什么会误判」

D4（`/tmp/kx-api-full-after.tsv`，2026-10-24 05:26~05:56）里：
`/api/admin/compression/sessions` = **500**，min/med/max = 10,177 / 11,668 / 11,989 ms。
落前（build 2813，2026-10-22）同端点是 **200 但已耗时 11.8~13.8s**。

⇒ ⇒ **两个误判点**：
① 「10.0s」是 `admin/compression_sessions.go` 的 `context.WithTimeout(..., 10*time.Second)`
**掐断的**，不是查询耗时；
② 「落前 200」是**侥幸**（查询恰好在 10s 内跑完），不是「快」。

⇒ ⇒ **通用式：看到「耗时 ≈ ctx 超时上限」时，读数是 ctx 的产物，必须去库里量真实成本。**

---

## §2 真实成本（生产 252 共享库，EXPLAIN ANALYZE 实测）

用仓内 `dbpkg.SessionFamilyTurnsSourceSQL()` / `SessionFamilyBodiesSourceSQL()`
**真实产出的**源形状拼出主查询（`go run` 打印 ⇒ 临时工具已删）：

| 形态 | Execution Time | Buffers hit |
|---|---|---|
| **旧形状**（CTE 带载荷） | **105,934 ms** | 307,841（≈2.4GB） |
| 旧形状（普通执行，非 EXPLAIN） | 15.6 ~ 65.8 s（随负载波动） | — |
| count 查询（LEFT JOIN + IS NOT NULL） | 922 ms | 263,002 |
| count 查询（去掉 bodies JOIN） | **48 ms** | 4,495 |
| **新形状**（标量子查询取载荷） | **359 ms** | 197,207 |

★ 口径说明：新形状**另一次** EXPLAIN ANALYZE 实测 **322 ms** ⇒ 区间 **322~359 ms**
（随共享库负载波动）⇒ 加速比 **295~329×**。仓内代码注释与形状门统一引用 **359 ms**
（权威值 = 与冻结窗口等价对拍同一轮的那次读数），区间在此登记备查。

计划形状（关键几行）：

```
->  Nested Loop (actual rows=17219 loops=1)        ← turns 侧：17,219 行，很便宜
     ->  Append (actual rows=1 loops=17219)        ← bodies 侧：**每个 turn 行都探一次**
          ->  Index Scan using idx_session_bodies_hot_request ...
               Filter: (outbound_body IS NOT NULL)
          ->  Index Scan using session_bodies_2026_07_request_id_idx ...
          ->  Seq Scan on session_bodies_2026_08 ...   ← 2026_08 分区被 **逐行 seq scan**
```

⇒ ⇒ 成本来自两处：
① **bodies 源 = hot ∪ parent（6 个分区）** ⇒ 每个 turn 行要探 **6 次**（103k 次索引探测）；
② **载荷被物化到整个窗口** ⇒ 17,219 行 × ~110KB ⇒ 约 1.9GB 的 TOAST 解冻。

---

## §3 试过的三条路（两条被实测否掉）

| 形态 | 思路 | 实测 | 判决 |
|---|---|---|---|
| **NEW_MAIN** | bodies 侧改 EXISTS 半连接 + CTE 不带载荷，但载荷用 **LATERAL 在外层**取 | 6,090 ms（Buffers 643,283） | ✘ **比旧形状的 count 还贵** |
| **VARIANT_C** | 再加「按腿对齐」（turn 与 body 同 partition_date ⇒ JOIN 带 `partition_date` 相等 ⇒ 分区裁剪） | 7,966 ms | ✘ **更慢** |
| **VARIANT_D / 最终形态** | 载荷用**标量子查询**（带 LIMIT 1 ⇒ 不可被 pull-up 展开）放在**分页之后** | **359 ms** | ✔ **采纳** |

⇒ ⇒★ **两条被否掉的路，各自有可复用的教训**：

- **EXISTS 半连接没有救到「每会话一次载荷解冻」**：NEW_MAIN 把载荷从 CTE 挪到了 LATERAL，
  但 LATERAL 挂在 `s`（**全部会话**）的外侧 ⇒ 14,434 个会话就解冻 14,434 次
  ⇒ ⇒ **通用式：把昂贵计算「挪出 CTE」不等于「缩小它的求值范围」——
  真正的量级是「页行数」还是「会话数」。**
- **「按腿对齐」在实测里反而更慢**（7.97s vs 6.09s）⇒ ★★ **前提核验是对的
  （实测 hot 6,138 对 / parent 11,053 对，partition_date **100% 对齐**、跨腿交叉 **0**），
  但「前提成立」≠「优化有效」** ⇒ 这条路**没做**（省了迁移与索引的风险），
  记在这里以免下轮重复。

⇒ ⇒⇒ ★★ **本轮最可复用的通用式**：
**「贵的东西要放在最后」+「最后」必须用「求值范围」来定义，不是用「代码位置」来定义。**
⇒ ⇒ 载荷取值的求值范围 = **页行数**（50）而不是 **会话数**（14,434）⇒ 差 288 倍。

---

## §4 语义等价性（冻结窗口，逐行对拍）

★ 窗口上界用 `now()` 时对拍会**假阳性**：两次查询之间有新 turn 落库 ⇒ 页边界位移
⇒ 实测抓到 1 行差异（old 独有 / new 独有各 1 行，`last_ts` 相差 13 分钟）。

⇒ ⇒ 改用**冻结窗口** `'2026-10-24 06:00:00+08'` 重测：

| 判据 | 结果 |
|---|---|
| old 独有行数 | **0** |
| new 独有行数 | **0** |
| 行数 | 50 = 50 |
| count（会话总数） | **14,487 = 14,487** |

⇒ ⇒ **逐字等价**，且「冻结窗口」这件事本身就是一条通用式（见 LESSONS §307）。

---

## §5 代码改动与「既有门不用改」

### 5.1 改动（`admin/compression_sessions.go`）

1. `whereClause` **不再**带 `rb.outbound_body IS NOT NULL`；
2. bodies 侧改 **EXISTS 半连接**（只问「有没有」，不搬载荷）；
3. CTE `window_turns` **只留 6 个标量列**；
4. 载荷改用**标量子查询**（`LIMIT 1`）在**分页之后**取；
5. SQL 产出抽成 `compressionSessionsSQL(...)`（**门要断言真实产物，不能断言源码字面量**）。

### 5.2 ★ 既有门 `admin/s3_wave4_bodies_sources_test.go` **一行未改即通过**

它的判据（读门得到）：双源会话族化 + 「不得再出现 `LEFT JOIN LATERAL`」
+ 「每会话最新轮必须 `DISTINCT ON` 单趟取」+ 「数组长度必须先 `jsonb_typeof` 判形」。

⇒ ⇒ ★ 我第一版用 `LEFT JOIN LATERAL` ⇒ **被这道既有门打红** ⇒
⇒ **改用标量子查询**（带 `LIMIT 1`，语义等价且不可被 pull-up 展开）
⇒ ⇒ 既有门**原样通过**，我自己的新门也绿。
⇒ ⇒ ★ **通用式：门红了先读门在守什么** —— 这次是「既有的门比我的第一版更懂性能」。

### 5.3 新门 `admin/compression_sessions_shape_test.go`（4 条）

| 判据 | 钉住什么 |
|---|---|
| ① CTE 投影不得含任何 `rb.` 列 | 载荷不得被物化到窗口 |
| ② 载荷取值位置 **必须晚于** `LIMIT $` | 求值范围 = 页行数，不是会话数 |
| ③ bodies 侧必须是 EXISTS、不得有 bodies 的 LEFT JOIN、半连接里必须保留 `outbound_body IS NOT NULL` | 口径不漂 |
| ④ 必须保留 `jsonb_typeof` + `jsonb_array_length` | 2605 缺陷（JSON 字面量 null → 22023） |

★ 变异验证：**M1**（载荷塞回 CTE）红、**M2**（载荷取值挪到 LIMIT 前）红、
**M3**（EXISTS 换回 LEFT JOIN）红 ⇒ 每臂 md5 逐字还原后复跑绿。

---

## §6 发布（★ 一条重要的「不要顺手带上」纪律）

发版前先算「部署通道的 pending 迁移扫描器会投递什么」：

```
★ 生产未应用且仓内有的 startup 迁移 = 1 ⇒ 902_stats_event_dedup_pkey_include.sql
```

⇒ ⇒ **902 是并行会话的在途迁移**（其工作区副本的 sha256 与 origin/main 的不同）
⇒ ⇒ **从 origin/main 发版会把一个未授权、且还在变的迁移带进生产。**

⇒ ⇒ ⇒ **本轮改为：基于上一次发布的提交 `412f7f95` + 本轮修复** 建发布树
⇒ ⇒ ⇒ 该树的 **pending 集 = 空**，且**不含**并行会话未发布的 §143.103 代码（+485 行）
⇒ ⇒ ⇒ **零 schema 变更、零未授权迁移、零他人未发布代码。**

⇒ ★ 附带结论：**「发版前先算 pending 集」应该是发布流程的固定一步**，
否则一次普通发版就会把在途迁移带上线。

### §6.1 ★★★ 本轮发版引入的一个真缺陷：**build_seq 倒退**（2819 → 2818）

发版树 = `412f7f95` + R76 修复，而 **`412f7f95` 里的 `version.json` 是陈旧的**
（`build_seq = 2817`；`git_sha` 还写着更早的 `a55a4571`）
⇒ ⇒ 部署时按「committed build_seq + 1」烧号 ⇒ ★ **本轮发出来的是 2818**，
而**上一版 active 是 2819** ⇒ ⇒ ⇒ **生产版本号倒退了**。

生产实测（154）：

| release | git_sha | build_seq | active 时刻 |
|---|---|---|---|
| 2817-a55a4571 | a55a4571 | 2817 | 落前 |
| **2819-412f7f95** | 412f7f95 | **2819** | R75 落 D2 修复后（05:26 基线在此） |
| **2818-64592772** | 64592772 | **2818** | ★ **R76（本轮）—— 序号倒退了** |

⇒ ⇒ ★★ **通用式（新增 LESSONS §311）**：**发版基线要同时满足两个条件**：
① **pending 迁移集为空**（否则把在途迁移带上线）；② **基线的 `version.json` 的
`build_seq` ≥ 当前 active**（否则**把版本号打回去**）⇒ ★ ① 只管「不动 schema」，
② 只管「不动版本号」——**两个是不同的不变量，不能用一条判据替代**。
⇒ ⇒ ★ **修法**：发版基线取 **origin/main**（其 `version.json` = 2823 ⇒ 下一版 2824 ⇒ 单调），
并**显式排除**未授权的 902 ⇒ 本轮为规避 902 而退到 `412f7f95`，**代价就是这条序号倒退**
⇒ ⇒ ★ **两个约束会互相顶**：⇒ 「不要带未授权迁移」与「基线要新」**不可兼得**时，
必须**显式记录代价**（本轮已记录），而不是假装没有代价。

⇒ ⇒ ★★★ **但本次发版【没有回退任何运行时代码】** —— 这点必须说清，否则会被误读成「生产回滚」：
★ **两个 release 目录的文件清单逐字相同**（`diff` 只差 2 个 macOS `.DS_Store`）⇒
⇒ 2818 与 2819 的**运行时差异只有本轮 R76 的那一处改动**；
★ 且 `git diff 412f7f95..origin/main -- bg/materialized_view_refresher.go` **为空**
⇒ ⇒ **R75-D2 的物化视图刷新器修复没有丢**（它本来就在 `412f7f95` 里）。
★ ⇒ **真正的落后在别处**：`412f7f95..origin/main` 之间有 **27 个 .go 文件 / +4,649 行**
（其中**非测试**的运行时改动 ≈ 270 行：`admin/settings.go`、`db/db_omnifree.go`、
`domains/hooks/compression/probe_session.go`（§143.103 探测短路，**opt-in 默认关** ⇒ 行为不变）、
`domains/streaming/{handler.go,request_log_pipeline.go}`）
⇒ ⇒ ★ **⇒ 生产跑的二进制比 origin/main 落后 ≈270 行运行时代码**
（★ 这一条**在 2819 时就已存在**，不是本轮造成）⇒ **与「双写退役」主线的
`domains/streaming/request_log_pipeline.go`（+76 行）相关** ⇒ ★ **建议下一版从 origin/main 发**，
把这条落后补掉（前提：902 的归属先裁决）。

### §6.2 ★★★ 263 端点回归重扫：**目标端点达成，但「min 退化 >1s 的端点数 = 0」这条判据本身是脆的**

重扫口径与 05:26 基线**完全同源**：同一份扫描脚本 `kx-api-sweep2.sh`、**同一份 263 路由集**
`/tmp/kx-routes-263.txt`、`REPEAT=3`、同一台 154（底噪 `readyz` min **19 ms**，load 0.51 ⇒ 底噪很低）。

| 指标 | 基线 05:26（2819） | 重扫 06:54（2818+R76） | 变化 |
|---|---|---|---|
| 2xx | 133 | **136** | **+3** |
| 5xx | 7 | **5** | **−2** |
| 超时（code 全 0） | 4 | **3** | **−1** |
| min > 2s | 15 | 16 | +1 |
| min > 30s | 5 | 5 | ±0 |
| 任一轮 > 2s | 26 | **23** | **−3** |

状态码变化（**3 条，全部是修复方向**）：

| 端点 | 变化 |
|---|---|
| `/api/admin/compression/sessions` | **500 → 200**（min 10,177 → **425 ms**，改善 **9,752 ms**） |
| `/api/admin/dashboard/session-health` | **500 → 200** |
| `/api/admin/pending-responses` | **超时 → 200** |

★ **初判「4 个端点 min 退化 > 1s」⇒ 判回归不通过**：
`turns/sessions/filter-options`(+3,709) / `data-lifecycle/stats`(+2,167) / `sessions`(+1,538) /
`dashboard/session-overview`(+1,100)。

⇒ ⇒ ★★★ **但定点复测（REPEAT=7，看全部样本）证明这 4 条是【量具噪声】，不是产品回归**：

| 端点 | 基线 2819（min/med/max） | 重扫 2818（min/med/max） | ★ 定点复测 REPEAT=7（min/med/max，样本） |
|---|---|---|---|
| `turns/sessions/filter-options` | 2,285 / 10,196 / 11,915 | 5,994 / 10,530 / 10,927 | **2,678 / 3,084 / 6,194**（样本 2678,2728,2873,3084,3425,3474,6194） |
| `dashboard/session-overview` | 2,486 / 2,731 / 16,370 | 3,586 / 4,562 / 10,514 | **2,623 / 3,002 / 3,571** |
| `sessions` | 1,660 / — / — | 3,198 / — / — | **1,294 / 1,338 / 1,507** |
| `data-lifecycle/stats` | 15,979 / — / — | 18,146 / — / — | **7,797 / 10,795 / 26,441**（样本 7797,8389,10269,10795,15732,16540,26441）⇒ **min 比基线还快** |
| ★（对照）`compression/sessions` | 10,177 / — / — | 425 / — / — | **501 / 621 / 2,246**（样本 501,509,558,621,690,832,2246）⇒ ★ **稳定在亚秒** |

⇒ ⇒ ★★ **三个时间点（10-22 落前 / 05:26 落后 / 06:54 重扫）放在一起看更清楚**：
`filter-options` 的 **min 在 12,091 → 2,285 → 5,994 之间乱跳**，而 **median 稳定在 ~10,200~10,500**
⇒ ⇒ **min 对这类端点是一个【脆统计量】**（一次随机慢跑就能把它拉高 3 倍）
⇒ ⇒ ★★ **通用式（新增 LESSONS §310）**：★ **「min 退化 > 1s 的端点数 = 0」这个判据只对
【低方差】端点有效**；⇒ 对高方差端点必须用**定点复测（更多重复 + 看样本分布）+ 中位数**复核，
否则会把**噪声**写成**回归**（本轮差点把 4 条噪声当成回归上报）。
⇒ ⇒ ★ **顺带暴露扫描脚本本身的一个口径缺陷**：`curl -m 30` 超时时 `-w '%{http_code}'` 已输出 `000`，
再叠加 `|| echo "000"` ⇒ **实际落盘是 `000000`（6 个 0）** ⇒ 对比脚本必须先把
「全 0 状态码」归一为超时，否则会把它当成一种独立的码。

⇒ ⇒ ★★ **结论（诚实边界）**：★ **本轮改动的目标端点达成且有 3 条额外修复**；
★ **不能宣称「其余端点全部无回归」** —— 只能说「**没有发现由本轮改动引入的回归**」
（依据：① 两个 release 目录文件清单逐字相同 ⇒ 运行时差异只有本轮那一处改动；
② 4 条初判退化经定点复测均为噪声；③ 该端点集在 10-22 与 05:26 两轮之间同样有
同量级的 min 抖动）。★ **真缺口**：**判据（min）与被测对象（高方差端点）不匹配** ——
⇒ 该判据的下一版应改成「**中位数 + 样本分布**」或「**min 退化 > 1s 且复测仍退化**」。


---

## §7 下一轮靶子（D4 榜单剩余 + 本轮 agent 的只读分析）

| 端点 | 实测 min | 归因 | 建议修法 | 风险 |
|---|---|---|---|---|
| `data-lifecycle/degradation/status` | 30s | **不是 SQL**：Redis 全 keyspace SCAN ×4 + 备份 gzip **解压两遍**；且**无 ctx 超时** | 4 次 SCAN 合并成 1 次；删掉重复解压；加 10s ctx | 低（纯 Go、零口径） |
| `db-status` | 21.2s | 同上（同一 `GetTTLStats`） | 同上 | 低 |
| `pending-responses/stats` | 5.6s | `SafeHGetAll` 把**正文**也拉回本地；cutoff 硬编码 **+100 年** | 改 `HMGET` 三字段 | 低 |
| `turns/sessions` | 30s（超时） | **无 ctx 超时** + 逐行相关子查询（其注释依赖的 `idx_sessions_session_id` **已被 876 删除**）+ 2 个 LATERAL 在排序前 | 先加 ctx；再两段式取 MAX(partition_date) | 中 |
| `sessions/inspector-stats` | 1.75s | `session_summaries`+`session_dim` 全量 JOIN，**无时间窗** | 加窗 + 覆盖索引 | 中（口径需裁决） |
| `dashboard/session-overview` | 2.5s | **9 条并行全扫** `session_summaries`（`first_request_at` **无索引**） | 建 `first_request_at` 索引（同时惠及 #5、#7、#10） | 低（纯新增索引） |
| `turns/sessions/filter-options` | 2.3s | 查询写 `ss.first_request_at`，但现存索引是 `last_request_at` ⇒ 用不上 | 加一条**冗余谓词** `AND ss.last_request_at >= $window`（输出逐字不变） | 低 |
| `data-lifecycle/{stats,metrics,partitions}` | 16s / 1.5s / 1.6s | 口径要求**全量历史**（`COUNT(*)`、每分区行数） | ⇒ **必然触碰可观测数字**（估算 vs 精确） | ⇒ **属主决策**，不宜由性能轮夹带 |
| ★ **`compression/stats`（同族，R76 末尾新发现）** | **10.003s**（**200**，不是 500） | ★ **ctx 掐断的假成功**：handler ctx = 10s，4 条查询串行共用；第 2 条（`estimated-orig`，要读 `request_body` 载荷做 SUM）超预算 ⇒ ctx 过期 ⇒ 后续查询**静默失败**（`slog.Warn` 后继续）⇒ **返回 200 但响应体残缺** | 见 §7.1 | **中**（口径要一起定） |

### §7.1 ★ `compression/stats` 的两个真缺陷（生产实测，非推演）

**缺陷①：200 = 假成功，响应体静默残缺。**
生产实测（2026-10-24 06:46，154 active 8781）：

```
http=200 time=10.003s
  compressed_total        17007
  compression_rate        1
  hourly_series           list(len=0)          ← ★ 空数组
  strategy_distribution   {...}                ← 3 项
  total_outbound_tokens   286833350
  total_requests          17007
  ★ 缺失：estimated_original_tokens / summary_mode_rows / token_band_{below,preliminary,forced}
```

⇒ `hourly_series` **长度 0**、三个 token_band 计数**整段缺失**、`estimated_original_tokens`
**缺失** ⇒ ★ **端点返回 200，但数据是残缺的，且调用方无从分辨「真的是 0」还是「超时了」**
⇒ ⇒ **通用式：把「超时」降级成「静默吞错」的 handler，即使状态码是 200，返回体也是错的。**

**★★ 缺陷②（原写「`compression_rate` 恒等于 1（口径错）」—— ★★ 该定性已被 R77 轮的生产实测【推翻并订正】，见下）。**

`admin/compression_stats.go:173-175`：

```go
result.TotalRequests += cnt
if withOutbound > 0 {
    result.CompressedTotal += cnt      // ★ 加的是【整组 cnt】，不是 withOutbound
}
```

⇒ ⇒ ★★ **R77 轮实测（生产 252，冻结窗口 2026-10-23 07:00 → 2026-10-24 07:00）**：

| 探针 | 读数 | 结论 |
|---|---|---|
| 窗口内 turns 行数 | **16,878** | — |
| LEFT JOIN 后的行数 | **16,878** | ⇒ **放大 = 0** |
| 窗口内 distinct request_id | **16,878** | ⇒ **bodies 侧 request_id 唯一（当前）** |
| 其中 `outbound_body IS NOT NULL` | **16,878（= 全部）** | ⇒ **`withOutbound == cnt`** |
| 生产实测响应 | `total_requests=17007`、`compressed_total=17007`、`compression_rate=1` | ⇒ ⇒ |

⇒ ⇒ ★★★ **⇒ 订正：「`compression_rate` 恒等于 1」当前【是准确的】，不是缺陷**
（窗口内 **100%** 的请求都带 outbound body ⇒ 压缩率真的是 1.0）
⇒ ⇒ ★ **原判是【误报】**：我把「代码把一个计数**布尔化**」当成了「口径已错」，
而**没有先量该计数当前的实际值** ⇒ ★⇒ **通用式（LESSONS §312）：
★ 「计数被当布尔用」是【潜在】缺陷，不是【当前】缺陷** ⇒
**判定「口径错了」之前必须先量「当前数据下这个口径算出来的值是什么」** ——
本例中 `withOutbound == cnt` ⇒ 两段代码**逐字等价** ⇒ 线上行为**完全正确**。

⇒ ⇒ ★ **但缺陷本身（潜在风险）仍然成立，只是今天不发作**：
① **布尔化丢信息**：一个策略组里**只要有 1 行**带 outbound body，**整组**都被算成「已压缩」
⇒ ⇒ **当组内出现混合（部分行有、部分没有）时，`compression_rate` 会被高估**。
★ 通用式：★ **「`if count > 0 { total += groupSize }`」是【把一个计数的信息降维成布尔】** ——
它只在「全有或全无」时与正确口径等价 ⇒ **判据必须写成 `total += count`**。
② **LEFT JOIN 的行放大风险当前为 0，但「bodies 侧 request_id 唯一」不是被强制的约束**
（bodies 源 = hot ∪ 6 个分区 ⇒ 同一个 request_id 落在 hot 与分区里就会放大）⇒
⇒ 一旦放大，`COUNT(*) AS cnt` 与 `COUNT(rb.outbound_body)` 会**同时**被放大 ⇒
`total_requests` 与 `compressed_total` 一起错 ⇒ ⇒ **半连接（EXISTS）天然免疫**。

⇒ ⇒ ⇒ ★★ **下一轮（R77）第一靶子定为 `compression/stats`**：
① 把 3 条 bodies LEFT JOIN 改成 EXISTS 半连接（同 R76 的手法）⇒ **顺带免疫行放大**；
② 载荷聚合（`estimated-orig`）**单独给 ctx**（不要与其它 3 条共用一个 10s）
⇒ ★ **R77 实测：它单独就要 34.3 s**（窗口内 `request_body` 共 **1,278 MB**、`_gw_body_summary`
envelope **0 行** ⇒ 每次都要整包 detoast）⇒ ⇒ **单条查询就超预算 3.4×**；
③ **ctx 超时必须显式报错**（不能 200 + 残缺体）；
④ `compressed_total += cnt` ⇒ `+= withOutbound`（★ **当前是等价的（0 差异）⇒ 属主确认的是
「未来是否会改变面板数字」，而不是「今天数是否错」**）。

⇒ ⇒ ★★ **一条索引解四个端点**：`session_summaries(first_request_at)`
（`session_summaries` 829,341 行 / 826 MB / **未分区**；现只有
`idx_session_summaries_last_request_recent` 与 `idx_session_summaries_tenant_time`）
⇒ ⇒ 但**建索引 = 生产写**（且 startup 迁移在事务里 ⇒ 不能 CONCURRENTLY ⇒ 建索引期间阻塞写）
⇒ ⇒ **需属主授权后再做**。

---

## §8 通用式（本轮新增，进 LESSONS §307~§309）

1. ★★ **看到「耗时 ≈ ctx 上限」时，那不是查询耗时** —— 去库里量真实成本；
   否则会把「被掐断」误判成「刚好够快」。
2. ★★ **把昂贵计算挪出 CTE ≠ 缩小它的求值范围** —— 量级是「页行数」还是「会话数」，
   差 288 倍。
3. ★★ **「前提核验成立」≠「优化有效」** —— 「按腿对齐」的前提实测 100% 成立，
   但优化本身更慢 ⇒ 前提与效果要分别验证。
4. ★★★ **发版前必须先算「pending 迁移集」** —— 否则一次普通发版会把并行会话的在途迁移
   带上线（本轮实测：唯一 pending = 902，且它的工作区副本与 origin/main 的 sha256 不同）。
5. ★★ **冻结窗口才能做等价对拍** —— `now()` 会让两次查询之间的新写入造成假阳性差异。
6. ★★★ **「min 退化 > 1s 的端点数 = 0」这条判据只对【低方差】端点有效** ——
   本轮初判 4 条退化，定点复测（REPEAT=7）全部证伪为噪声（`filter-options` 的 min 在
   12,091 / 2,285 / 5,994 之间乱跳而 median 稳定在 ~10,300）⇒ **判据与被测对象的方差
   不匹配时，判据本身是坏的** ⇒ 必须用「中位数 + 样本分布」或「复测仍退化」复核。
7. ★★★ **发版基线要同时满足两个不变量**：① **pending 迁移集为空**（不动 schema）；
   ② **基线 `version.json` 的 `build_seq` ≥ 当前 active**（不动版本号）——
   本轮为规避未授权的 902 而退到旧基线 ⇒ 代价是 **build_seq 2819 → 2818 倒退**；
   ⇒ **两个约束会互相顶，代价必须显式记录，不能假装没有代价**。
