# R93 · `?model=` 仍 500@30s（2842 上线后）：真因在**第二条语句**（api_key enrich），不在 R92 修的列表查询

> 适用：`llm-gateway-go`，生产 154（252 = PG 宿主）。
> 轮次标签：**2026-10-27**；实测时钟：**2026-10-24**（本系列统一口径，见 `LESSONS.md` §360）。
> ⚠ **本轮是「R92 的验收被证伪 → 重新归因 → 修法已收敛」的轮次**：
> R92 的修法**确实生效**（列表查询 17,501 ms → **342 ms**），但端点**仍 500@30s**，
> 因为 500 来自**另一条语句**。

---

## 0. 一句话结论

★★★ **R92 的验收被证伪，但 R92 的修法没有白做**：

| 事实 | 证据 |
|---|---|
| 端点仍 500@30s | 154 直连 REPEAT=3，build **2842**（R92 已上线） |
| ⇒ **但 500 来自哪条语句，先要问清楚** | 生产 journal：`admin handleTurnsSessions enrich meta failed` → `iterate api_key enrich failed: … 57014` ⇒ **不是列表查询** |
| R92 修的**列表查询已修好** | 生产同快照实测：`?model=` 列表查询 **342 ms**（修前 **17,501 ms**） |
| 真正的瓶颈 | **api_key enrich 查询**：视图与 history source **把同一批 85,712 行扫了两遍**再 1:1 join ⇒ **13,066 ms**（实测） |
| 修法已收敛并实测 | **K4 形态**（每 source「聚合取 max(turn_no) + 回定位」）⇒ 同批次 **1,339 ms**（**9.8×**） |
| 口径等价已证明 | 三批次 **双向 EXCEPT ALL**：B1 `only_new=0 / only_old=0 / 21=21`、B2 `0/0/6=6`、B3 `0/0/0=0` |

---

## 1. 症状（154 直连，build 2842 = R92 已上线）

```
query                                                         codes   times(s)
?limit=20                                                 200 200 200  0.817 0.056 0.063
?limit=20&api_key_id=105                                  200 200 200  13.930 0.226 0.345
?limit=20&model=deepseek-ai%2Fdeepseek-coder-6.7b-instruct  500 500 500  31.505 30.559 30.387
?limit=20&provider=14                                     200 200 200  0.423 0.126 0.088
?limit=20&status_code=500                                 200 200 200  14.810 0.292 0.283
```

★ **测量的第一课**：探针脚本用**应用指纹**（`/api/system/version`）选端口 ⇒
发版后旧实例已 drain（8781 无响应）⇒ **命中的是新实例 8782（2842）**。
⇒ 「还是 500」这个读数**可信**（不是打到了旧实例）。

★ **报错来自哪条语句**（生产 journal，`llm-gateway-go-canary@8782.service`）：

```
WARN admin handleTurnsSessions enrich meta failed
     err=iterate api_key enrich failed: ERROR: canceling statement due to statement timeout (SQLSTATE 57014)
```

⇒ ★★ **失败点在 `enrichTurnsSessionMeta`（第二个查询），不在列表查询** ——
★ 这就是 R92 归因错的分水岭：**R90/R92 一直在修「列表查询」，而 500 由「enrich 查询」造成**。

---

## 2. 归因（252 生产只读实测）

### 2.1 R92 的修法确实生效（列表查询）

用**从代码里原样生成**的端点 SQL（临时 Go 测试打印 `turnsSessionsListSQL()` +
`turnsSessionsFinalizeWhere()` 的真实产物，不手抄）在生产跑：

| 查询 | 实测 |
|---|---|
| 列表查询（`?model=` 稀有值，V2 形态 + 905 的索引） | **342.431 ms**（修前 **17,501 ms**） |
| 列表查询（无过滤） | **124.571 ms** |

⇒ ★★ **R92 的修复是真的**（形态 + 索引两条腿都到位，索引 `indisvalid=t`，905 记账 sha 逐字一致）。

### 2.2 enrich 查询的真实解剖

```sql
SELECT DISTINCT ON (t.session_id) t.session_id, rl.api_key_id, …
FROM public.session_turns_with_current_month t                    -- 视图
JOIN (hot ∪ 全部历史, 按 session 谓词过滤, 113 列投影) rl ON rl.request_id = t.request_id
LEFT JOIN public.api_keys ak ON ak.id = rl.api_key_id
WHERE t.session_id = ANY($1) AND rl.api_key_id IS NOT NULL
ORDER BY t.session_id, t.turn_no DESC
```

★ **`public.session_turns_with_current_month` 的名字骗人**：实测它的父表腿是
`FROM session_turns`（**全部分区**，不是「当月」）⇒ 视图 = `hot ∪ 全部历史`。

⇒ ★★★ **于是同一批行被扫了两遍**：
- 视图侧：`Index Only Scan` on 2026_09 ⇒ **Heap Fetches 86,507**
- history source 侧：同样 **86,507 行**
- 然后两者按 `request_id` **1:1 join** ⇒ ★ **join 本身是冗余的**（同一行、同一列）

### 2.3 为什么只有 `?model=` 慢（批次差异，不是「形状」差异）

`?model=` 命中的 21 个会话是**探针会话**，它们在 2026_09 有 **85,712 轮**：

| 会话批次 | 2026_09 轮数 | 2026_10 | hot | 旧 enrich 实测 |
|---|---:|---:|---:|---:|
| `?model=`（探针） | **85,712** | 0 | 0 | **13,066 ms** |
| 无过滤（新鲜） | 0 | 21×1 | — | **23 ms** |
| 当前月多轮 | 0 | 5,364 等 | — | **115 ms** |

⇒ ★★ **同一个 enrich 查询，差 570×** ⇒ **不是形状问题，是「这批会话有多少轮」**。

### 2.4 排除项（都实测过）

| 假设 | 实测 | 结论 |
|---|---|---|
| 统计信息陈旧 | 统计新鲜（last_analyze 2026-10-19 / last_autoanalyze 2026-10-24） | ✗ 排除 |
| 缺 `session_id` 前导索引 | 2026_09 **有** `session_turns_2026_09_heap_session_id_turn_no_idx`（`(session_id, turn_no DESC)`，`indisvalid=t`） | ✗ 排除 |
| PG17 **index skip scan** 退化 | `SET enable_indexskipscan=off` 后**计划完全不变** | ✗ 排除 |
| 113 列投影太宽 | C 臂（窄投影 request_id+api_key_id）= **21,765 ms** vs 原样 **20,274 ms** | ✗ 排除 |
| `session_turn_details` 的 LEFT JOIN | 同上（C 臂同时去掉）**无改善** | ✗ 排除 |

⇒ ★★ **真正的成本 = 把 85,712 行 × 宽投影物化两遍**，与索引、统计、skip scan、投影宽度都无关。

---

## 3. 修法（K4 形态）与生产实测

### 3.1 形态

```
每个 source（hot 与父表各一份）：
  (SELECT session_id, max(turn_no) AS turn_no
     FROM <source> t
    WHERE t.session_id = ANY($1) AND <CASE api_key 规范化> IS NOT NULL
    GROUP BY session_id) l
  JOIN <source> t ON t.session_id = l.session_id AND t.turn_no = l.turn_no
两 source 用 UNION ALL 合并 → 外层 DISTINCT ON (session_id) ORDER BY session_id, turn_no DESC
```

★ **为什么是「聚合 + 回定位」而不是 `DISTINCT ON`**：
`DISTINCT ON` 写在**分区父表**上时，规划器给的是 **Merge Append ⇒ 全量 86,507 行**再 Sort+Unique
（实测 **9,900 ms**）⇒ ★ **PG 不会把 DISTINCT ON 下推到分区** ⇒ 裁剪不生效。
而 `GROUP BY session_id` + `max(turn_no)` 走 **index-only scan**（不回表）⇒ 只读索引页。

### 3.2 生产实测（三批次，含口径对拍）

| 批次 | 旧查询 | **K4 新形态** | 倍数 | 对拍（双向 EXCEPT ALL） |
|---|---:|---:|---:|---|
| B1 新鲜会话（21 行） | 23.2 ms | **96.0 ms** | 0.24×（慢，但绝对值 <100 ms） | `only_new=0 / only_old=0 / 21=21` ✓ |
| B2 当前月多轮（6 行） | 115.0 ms | **54.1 ms** | 2.1× ✓ | `only_new=0 / only_old=0 / 6=6` ✓ |
| B3 探针 85,712 轮（21 会话） | **13,066 ms** | **1,339 ms** | **9.8×** ✓ | `only_new=0 / only_old=0 / 0=0` ✓ |

⇒ ★★ **口径等价已用两批【非空】数据证明**（B1 21=21、B2 6=6，only_new=only_old=0）。
⇒ ★ **B3 两侧皆空**（这批探针会话的 `api_key_id` 规范化后为 NULL ⇒ 旧口径也填不出 api_key）
⇒ ★ **空集自证不是证据**（`LESSONS.md` §354.2）⇒ **等价性由 B1/B2 承担**。

### 3.3 其余候选形态的实测（避免重复劳动）

| 形态 | 实测 | 判定 |
|---|---:|---|
| 原样（视图 + 113 列 history join） | 20,274 ms | 基线 |
| C：窄投影（无 details JOIN） | 21,765 ms | ✗ 无效 |
| F1：+ request_id 推进（原投影） | 11,495 ms | ✗ 仍慢（规划器先按 session_id 取 85,712 行） |
| F2：+ request_id 推进（窄投影） | 4,789 ms | ✗ 仍慢 |
| F3/F4：**只**按 request_id 集合过滤 | 3,975 / 3,927 ms | △ 能过 30 s 但 >2 s |
| H1：`DISTINCT ON` 写在分区父表上 | 9,900 ms | ✗ Merge Append 全量 |
| H2：LATERAL + `LIMIT 1`（每会话） | 451 ms（B3）/ **4,453 ms（B2，反超）** | ✗ B2 退化 |
| K3：`DISTINCT ON` 但漏了 `IS NOT NULL` 过滤 | 3,376 ms | ✗ **口径不同**（B3 `only_new=20`） |
| ★ **K4：聚合 `max(turn_no)` + 回定位** | **1,339 ms（B3）/ 54 ms（B2）/ 96 ms（B1）** | ★ **采纳** |

---

## 4. 口径等价（不靠「形状像」）

★ 旧口径 = 「**视图里**、且 **`api_key_id` 规范化后非空**的轮次中，`turn_no` 最大的那一轮」的 api_key。
★ 新口径 = 「**hot ∪ 全部历史**里、且 `api_key_id` 规范化后非空的轮次中，`turn_no` 最大的那一轮」的 api_key。

★ 两者**相同**的三条理由：
1. 视图 = `hot ∪ 全部历史`（父表腿是全部分区）⇒ **候选轮次集合相同**；
2. 视图的 hot 腿 anti-join 只删「hot 与父表同时存在」的重复行 ⇒ 同一 `request_id` 的 api_key 相同 ⇒ **取值相同**；
3. 新形态在**每个 source 内**都带 `IS NOT NULL` 过滤 ⇒ 与旧口径的过滤位置等价（K3 漏掉它 ⇒ `only_new=20` ⇒ **这条是实测抓到的**）。

★ 生产**双向 EXCEPT ALL** 三批次实测见 §3.2 ⇒ `only_new=0 / only_old=0`（B1/B2 非空）。

---

## 5. 门禁（待随代码落地）

| 门 | 断言 |
|---|---|
| `admin/turns_sessions_enrich_latest_test.go`（新） | ① 形态门：enrich 查询**不得**再出现 `session_turns_with_current_month` 视图 + 全量 history join；② 每个 source 内必须带 `max(turn_no)` 聚合 + `IS NOT NULL` 过滤；③ **真库等价门**：同快照下新旧两形态逐行比对（`EXCEPT ALL` 双向），**且要求两侧行数 > 0**（空集 ⇒ 显式 SKIP 并写明理由，不冒充「通过」） |
| 变异 | 至少 3 臂：① 去掉 `IS NOT NULL`（⇒ 口径漂移，门必须红）；② 去掉 `max(turn_no)` 聚合（⇒ 物化全量，门必须红）；③ 调用点回退到旧形态（⇒ 形态门红） |

---

## 6. 发版与生产验收

**待回填**（R93 代码落地后按 §3.2 的同一套读数补齐）：
- 发版序号 / 版本身份 / 迁移水位
- `?model=` 的 codes + times（目标：**200 且 <2 s**）
- provider / status_code / api_key_id **不退化**
- 端点总耗时（列表 + enrich + handoff 三腿）

---

## 7. 诚实边界

1. ★ **R92 的验收未达成**（`?model=` 仍 500@30s）⇒ R92 审计档 §6 已回填「未达成 + 新根因」。
2. ★ **R93 的代码尚未落地**（本档记录的是**归因 + 修法实测**，不是已发版的结论）⇒ §5/§6 留空待回填。
3. ★ **B1 批次新形态比旧形态慢 4×**（96 ms vs 23 ms）⇒ 绝对值 <100 ms，
   但**不是全面更快** ⇒ 「新形态一律更快」这句话**不成立**，已如实记录。
4. ★ B3 的 1,339 ms 仍非毫秒级（86,507 个索引元组的 index-only 扫描 I/O）⇒
   **没有**把它做到 <100 ms ⇒ 更彻底的修法（逐分区显式裁剪）**本轮未做**。
5. ★ 本轮**对生产的写入 = 0**（全部只读 + EXPLAIN ANALYZE 的 SELECT）。
6. ★ ★★ **R90/R92/R93 三轮都在修「列表查询」，而 500 一直来自「enrich 查询」** ——
   ⇒ 这说明**「按端点归因」不等于「按 SQL 语句归因」**：端点内部有多条语句，
   **必须先从生产日志确认是哪一条失败**，否则会连续修错对象。

---

## 8. 通用式（并入 `LESSONS.md` §361）

1. ★★★★ **「端点超时」不等于「你盯着的那条 SQL 慢」** —— 端点内部有多条语句；
   ★ **先从生产日志确认是哪一条失败**（本轮：journal 里的 `enrich meta failed`），
   否则会像 R90/R92 那样**连修两轮对象**。
2. ★★★★ **视图的名字可能骗人**：`session_turns_with_current_month` 的父表腿实测是
   `FROM session_turns`（**全部分区**）⇒ ★ **读视图定义要看 `pg_get_viewdef` 的真实文本**，
   不要信名字。
3. ★★★★ **「同一行被扫两遍再 1:1 join」是纯浪费**：视图 + history source 两边都物化同一批行
   ⇒ join 冗余 ⇒ ★ **把「每行取最新」下推到每个 source**（聚合 + 回定位）才能真正裁剪。
4. ★★★ **分区父表上的 `DISTINCT ON` 不会下推到分区** ⇒ Merge Append 全量 ⇒
   ★ **用 `GROUP BY + max()`（index-only）代替**。
5. ★★★ **口径等价必须用【非空】批次证明**：本轮 K3 形态在探针批次返回 20 行而旧口径 0 行
   ⇒ 若只测空批次就会漏掉 ⇒ ★ **对拍要覆盖「有返回」与「无返回」两类批次**。

---

## 9. 相关文件

- R92（本轮的上一轮）：`docs/audit/2026-10-27-R92-model过滤仍500-稀有度与驱动方向.md`
- R91（部署工具链）：`docs/audit/2026-10-27-R91-发版2841被远端锁挡住-SSH传输层四层缺陷.md`
- 代码：`admin/turns_sessions.go`（`enrichTurnsSessionMeta`）、`db/request_logs_view_schema.go`
- 生产取证脚本（本轮临时，用完即删）：`/tmp/kx-prod-step1.sql`、`/tmp/kx-prod-step2.sql`、
  `/tmp/kx-prod-ab.sql`、`/tmp/kx-prod-ab2.sql`、`/tmp/kx-prod-f.sql`、`/tmp/kx-prod-g.sql`、
  `/tmp/kx-prod-h.sql`、`/tmp/kx-prod-i.sql`、`/tmp/kx-prod-j.sql`、`/tmp/kx-prod-k.sql`、
  `/tmp/kx-prod-l.sql`、`/tmp/kx-prod-m.sql`
