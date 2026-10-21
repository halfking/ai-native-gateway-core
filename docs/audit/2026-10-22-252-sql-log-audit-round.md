# 2026-10-22 252 PG SQL 日志审计轮（§145）：一个发布序列缺口、一个 Sprintf 损坏、一个退役残留探针

> 轮次：§145（上一轮 §144 = 2026-10-20）。窗口：2026-10-20 23:47 → 10-22 05:47
> （ctrlog 快照 `ctrlog-20261021.log` 92MB + `ctrlog-20261022.log` 41MB；
> 10-21 18:47 有一次 conmon 1GB 悬崖，已被 `pg17-ctrlog-snapshot` 界标保全，零丢失）。
> 通道：`/var/log/pg17-ctrlog-archive/`（cron `:47` 小时级外抄）。
> **对生产的写入 = 两处，均有守卫与事后验证**：① 部署 `pg17-disk-watch.sh`（巡检脚本，非数据）；
> ② 定向预应用迁移 891（详见 §三，带 lock_timeout、幂等、双台账）。

## 零. 方法

- 本地 Python 解析器复用 `scripts/audit/parse_pg_kills.py` 的记录重建语义
  （容器前缀剥离 → PG 记录 → 多行 STATEMENT 折叠），新增全量指纹分组
  （`/tmp/pg252-audit-1022/analyze.py`，慢查按语句指纹、ERROR 按（错误头×语句指纹）、
  kills 按 kind×语句头）。
- **量具坑（本轮自踩并绕过）**：ctr.log 含二进制字节 ⇒ 裸 `grep` 中途转 binary 模式、
  只输出 `Binary file matches` ⇒ 管道下游 uniq 出**假频次**（55 vs 真值 1167）。
  一律 `grep -a` 或 Python 文本模式（`errors="replace"`）。
- 审计会话自污染登记：10-21 07 时 `pg_sleep` ×544 = 某审计会话的
  pg_stat_activity 2s 采样器跑了一小时（每次 sleep 都 >1s 被记成慢查）；
  10-22 04 时段一批 user_request 取消 = 并行审计会话自己掐自己的长查询。
  两簇均为一次性，不计缺陷。

## 一. 缺陷 #1（D1）：disk-watch 每 10 分钟点查已退役的 `_request_logs_hot` —— 已修并部署

**签名**：`ERROR: relation "public._request_logs_hot" does not exist` +
`SELECT pg_total_relation_size('public._request_logs_hot')/1024/1024`，
10-21 34 次 + 10-22（半天）36 次 ≈ 144 条/天（cron `*/10`）。

**根因**：`/opt/scripts/pg17-disk-watch.sh:206` 的 hot 表体积探针监视的对象是 v1 族
hot 表；迁移 890（10-21 17:41 应用到 252）把整个 `_request_logs*` 族真删后，
探针每轮 42P01。**连锁损害**：`req_logs_hot_size` 恒空 ⇒ awk 阈值比较恒 0 ⇒
「hot 表异常膨胀」告警**静默失效**（错误噪声 + 告警变盲，双损失）。

**修复**（仓内 `scripts/252-monitor/pg17-disk-watch.sh`，已部署 252）：
- 监视对象换成语义继任者 **`session_turns_hot`**（现行主写入 hot 面，含 JSONB
  preview/search_text，TOAST 风险同族；实测现值 71MB，阈值沿用 2GB/3GB）。
- env 名 `HOT_REQUEST_LOGS_*` → `HOT_SESSION_TURNS_*`，旧名保留一层回退。
- alert/log 消息字段同步改名（`turns_hot=`）。
- 部署后实测：手动跑一轮 `turns_hot=71MB`、`gauge=0/9`（9 次查询全成功）；
  06:50 / 07:00 两个 cron 周期 **42P01 新增 = 0**。
- 台账 `scripts/252-monitor/deployed-252.tsv` 行 54 更新（双侧 md5 `d864e296…`，in-sync）。
- 排除面：`pg17-drop-old-columnar-partitions.sh` / `pg17-emergency-cleanup.sh` /
  `pg17-proactive-empty-table-cleanup.sh` 里的 v1 表名都是**目录查询的 IN 列表死项**
  （`pg_inherits` 名字比对，缺表只是永不命中），不产生 42P01，不动。

## 二. 缺陷 #2（D2）：discovery stale 下架 SQL 被 fmt.Sprintf 的 `%` 损坏 —— 已修（仓内，随下次发布上线）

**签名**：`ERROR: syntax error at or near "(" at character 2700~6891`（多种偏移），
语句：`UPDATE model_offers SET available=FALSE, unavailable_reason='auto_discovery_expired' …`。
10-21 100 条（17:36 起）+ 10-22 半天 111 条。

**时间线钉死**：252 网关二进制 mtime **2026-10-21 17:34:33**；首错 **17:36:27**（部署后 2 分钟）；
10-20 全天 0 条。引入提交 = `5f64031cb`（10-20 14:33，「890 全仓读面切会话族」，
把 `db.SessionFamilyTurnsSourceSQL()` 拼进了这条 UPDATE）。

**根因（生产日志原文自证）**：共享投影 helper 的输出里带字面 `%`
（`LIKE 'sys:%'`），而这条 UPDATE 用 `fmt.Sprintf(模板+helper拼接, placeholders, graceHours)`
组装 ⇒ Go fmt 把投影里的 `%` 当成**未知动词**：

```
(CASE WHEN t.session_id LIKE 'sys:%!'(int=24) THEN NULL ELSE …  ← %' 吃掉了 graceHours(int 24)
AND rl.ts > now() - interval '%!d(MISSING) hours'               ← %d 无参可吃
```

⇒ PG 在 `'sys:%!'` 后的 `(` 处必然语法错（偏移随 NOT IN 列表长度漂移，解释了多变体）；
同一条语句里投影出现两次（hot 腿 + parent 腿），第一次吃参、第二次 MISSING。
同函数里 cmb UPDATE 的 `'manual%%'` 转义**是对的**——890 换源把投影拼进格式串时才引入。
**后果**：stale 模型自动下架功能自部署起全灭（每次 `continue`，只记 Debug 日志），
外加每小时成批 42P01 噪声。

**修复**：
- `discovery/discovery.go`：SQL 组装抽成纯函数 `staleExpiryOffersSQL(placeholders, graceHours)`，
  两个注入值**字符串拼接**（`strconv.Itoa`），不过 Sprintf；注释写明约束。
- `cmd/compression-bench/main.go:242`：同型缺陷（AST 扫描抓到的第二处），limitClause 改拼接。
- **静态门** `scripts/ursmcheck/sqlfmt_sessionfamily_gate_test.go`（数据流版）：
  helper 调用出现在 `fmt.Sprintf` **格式串**（第一实参）⇒ 红；且做**污染集传播**
  （函数体直接/间接含 helper 调用 ⇒ 该函数进污染集，其返回值进格式串同样红）
  —— 变异 M1 证明「只认直接 helper 调用」存在盲区（修复把 SQL 搬进纯函数后，
  把纯函数返回值再包进 Sprintf 的形态门是瞎的），已按包作用域数据流闭合。
  - 自证 1：夹具（直接形态）必须命中；自证 2：夹具（包装形态，M1 盲区）必须命中；
    自证 3：仓内 helper 调用点 >0（防「对空气扫描」）。
  - 变异 M1（包装形态）/ M2（原始事故形态）全红，还原 sha256 逐字一致。
  - 门自身两次自踩：夹具第一版把反引号放进双引号字符串内部 ⇒ 拼接根本不存在 ⇒
    阳性对照红（好事，暴露夹具错）；污染集按函数名跨包传播 ⇒ vendor 同名函数误染
    ⇒ 改为**按包目录作用域** + 跳过 `vendor/`。
- **形态测试** `discovery/stale_expiry_sql_shape_test.go`：钉 `LIKE 'sys:%'` 逐字幸存、
  interval 字面量完整、占位符真注入、证据源是会话族两腿、无 `%!`/`MISSING` 损坏标记。
- 既有 AST 门 `TestExpireStaleModelsConsultsTheGate` 同步改钉
  （UPDATE 字面量搬家后，钉「调用纯函数 + 字面量仍在纯函数体内 + 护栏仍在前」）。
- **部署边界（诚实声明）**：修复在仓内（本轮提交+推送），**252 生产网关未重发**——
  现役版本 `b23a24c1`（10-21）落后 origin/main **89 提交**，重发 = 替整条发布列车做主；
  下次发布列车自然携带（噪声 ~100 条/天、功能停摆持续到那时）。

## 三. 缺陷 #3（D3）：负成本检查换源后配套索引缺席 —— 891 已定向预应用 252（生产写，已验证）

**签名**：`WITH neg AS (…★890 换成会话族…)` 被 `statement_timeout` 击杀：
10-21 17:30 起 36 次 + 10-22 半天 38 次 ≈ 2 次/小时（检查每 8-9 分钟一跑，多数被杀）。
**10-21 17:00 前为 0 条** —— 与 D2 同一分二进制（17:34 上线）相关。

**根因**：`recorded_cost_is_negative` 健康检查的源随 890 迁到会话族
（窄读 `session_turns_hot ∪ session_turns`，30 天窗）；配套的部分索引在**迁移 891**
（并行会话 10-20 已写好、登记齐全、db-changelog 状态 "pending deploy"）。
252 的 10-21 部署只应用了 **890**（17:41 记账），891 没赶上发布包 ⇒
查询在 252 上全扫 30 天会话族（BEFORE EXPLAIN：6 个面 Seq Scan，仅 09 月分区沾旧索引）
⇒ 30s 角色级超时下反复被杀 ⇒ **负成本检查实质失明**。
**这正是 882 修复的回归**：882 的索引随 v1 表消亡（索引活不过它的表），
891 就是它的接棒者，只是没上生产。

**修复（生产写，按 §144 的 882 预应用先例）**：
1. 本地跑 891 契约门（索引形态/谓词/键序/INCLUDE/lock_timeout/双副本逐字）全绿。
2. BEFORE 取证（只读 EXPLAIN，见上）。
3. `psql -f` 应用（迁移自带 `lock_timeout=60s` + `statement_timeout=10min`），
   `CREATE INDEX` ×2 一次通过。
4. 记账：`schema_migrations` 插入 version=891 行（与 890 行同形）。
5. **AFTER 实测：EXPLAIN ANALYZE 16.457ms**（此前 >30s 必被杀），
   全部 Index Only Scan、Heap Fetches 0；空分区 Seq Scan 0 行。
6. 仓内 `docs/db-changelog.md` 891 行状态补「252 已定向预应用」（见提交；
   注意该文件含并行会话未提交 WIP，本次只提交本行 hunk）。

## 四. 已知家族对账（无新缺陷，量级与既登记一致）

| 家族 | 本窗口读数 | 状态 |
|---|---|---|
| ursm 巡检 `WITH win`（P11） | 23 次/总 6191s，max 566s（10-21）；892+巡检改造 pending deploy | 既登记，待发布 |
| rollup `WITH latest`（P1 拆事务） | statement_timeout 45 次/2 天 | 既登记（§143.27/28/31） |
| matview REFRESH（B1） | routing_analytics_7d 67 次 / session_client_stats 33 次… | 既登记 |
| CMI rollup `latest_bucket` | 448 次慢查 | 既登记（§143.19） |
| pocket `$4 inconsistent types` | 284+72 次/2 天 | 已知（fa20e648 在仓未更，N+3），待 owner |
| SPA 路由 `WITH policy` user_request 取消 | 825+224 次/2 天，日间分布（人浏览时段） | 客户端切页 abort，非缺陷；单次 61ms 级，浪费可忽略 |
| `session_owners` / `llm_usage_stats` / `schema_migrations` 缺表探针 | 22+6+3 次，簇状 | **外部产品/其它库**的探针连错库，非本仓缺陷 |
| apihub `INSERT INTO assets` >1s | 105 次/2 天（74MB 表、无触发器） | 宿主 I/O 争用同根（§143.31 现状），登记 |
| 外部 admin 杀查询（§144 遗留 192/天） | **2 次/2 天** | **自行收敛**，销案 |
| FATAL `connection to client lost` ×749 / 并行 worker 陪葬 ×292 | — | 前者是客户端断连；后者是被杀查询的伴生噪声，非独立 |

**阴性结论**：本窗口**没有**新的 42P01/42703/语法错误家族未覆盖；慢查 top 榜
与 §143.29 的物理读排名一致（巡检、REFRESH、rollup、候选查询）。

## 五. 量具与教训

1. **「索引比二进制晚到」是发布序列的缺口族**：D3 的两个 half（查询换源、索引落地）
   分属两次交付，二进制先上、索引没赶上包 ⇒ 回归。§144 的 882→890→891 链条
   在仓内是自洽的，缺的只是「发布时点对齐」。
2. **SQL 组装 helper 的输出含 `%` ⇒ 永不过 Sprintf**（静态门已钉全仓）。
3. **裸 grep 在 conmon 日志上会中途转 binary 假读数**（`-a` 强制文本）。
4. 门自身的阳性对照夹具也会写错（反引号放进字符串内部）——
   **阳性对照红要先查夹具是不是真的含靶模式**，再怀疑扫描器。
5. 部署 sha 与 origin/main 差 89 提交时，单缺陷修复**不该**顺带重发网关
   （等于替发布列车做主）；脚本级修复（巡检）可以直接部署。

## 六. 产物清单

| 类型 | 文件 |
|---|---|
| 修复 | `discovery/discovery.go`（staleExpiryOffersSQL 抽取+拼接）、`cmd/compression-bench/main.go` |
| 门 | `scripts/ursmcheck/sqlfmt_sessionfamily_gate_test.go`（新，数据流版+三自证）、`discovery/stale_expiry_sql_shape_test.go`（新）、`discovery/stale_expiry_gate_test.go`（改钉）、`scripts/ursmcheck/disk_watch_alert_dedupe_test.go`（fake 分支跟探针） |
| 巡检 | `scripts/252-monitor/pg17-disk-watch.sh`（已部署 252，md5 `d864e296…`）、`README.md`、`deployed-252.tsv` |
| 生产写 | 891 预应用 252（双索引 + schema_migrations 记账）、disk-watch 部署 |
| 验证 | 变异 M1/M2 红+还原；891 契约门绿；discovery 包回归绿；ursmcheck 全包绿（除已修的 drift 行） |
