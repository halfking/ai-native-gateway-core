# 252 SQL 日志审计轮（2026-10-20，§144）

> 会话：ZCode（本仓 llm-gateway-go-4 checkout）。
> 目标（用户指令）：对 252 docker 中 pg-252-pg17 的全部 SQL 日志审计，找出慢 SQL 与
> 错误 SQL，分析并修正。
> 上轮：2026-10-19（外部杀查询/CMI 队列事件轮 + smm 守卫舰队补齐）。本轮窗口从
> 上轮覆盖边界（10-19 06:04）之后起算。
> 本轮**有一个代码修复落地并已预应用生产**（迁移 882），与上轮「无代码改动」不同。

## 一、窗口与方法（起点重算）

- **日志源**：podman 容器完整 ID `dc5e5427…` 的 `HostConfig.LogConfig.Path`
  （上轮量具坑：`podman ps` 短 ID ≠ 目录 64 位长 ID）。本轮在位文件：
  `ctr.log-20261020`（10-19 03:22 → 10-20 03:46，835MB）+ `ctr.log`
  （03:46 → 06:04，845MB）。
- **窗口**：10-19 06:04 → 10-20 06:04（24.0h 整），服务器侧 awk 切片 + gzip -1
  = 33MB 拉回本地。
- **方法**：沿用上轮修复后的 perl 逐行状态机（`/tmp/252-sql-audit-1019/parse.pl`
  → `/tmp/252-sql-audit-1020/`，WIN_START 改 2026-10-19 06:04）。三项上轮修复
  （慢查行内联 `statement:`、带前缀空 STATEMENT 并入、线性引号归一化）全部有效：
  孤儿 STATEMENT **0 条**、`<EMPTY>` 伪影 0 条。
- **产物**：慢查 7,035 条 / ≥1s 累计 **39,826s**；错误 **3,121 条**
  （ERROR 2,175 + FATAL 946）。
- **量具坑（本轮新增两条）**：
  1. 本会话 shell 的裸 `grep -c` 间歇性**零输出**（连 `0` 都不打）——家族统计与
     零判定一律改用 `/usr/bin/grep` 后稳定；同族：zsh 里裸 `===word===` 会被当
     命令展开（`== not found`），echo 参数必须加引号。
  2. `podman exec -i psql` 的 `\i` 解析的是**容器内**路径——宿主 `/tmp` 的文件
     必须 `podman cp` 进容器才能 `\i`（本轮首次应用因此失败一次，无副作用）。

## 二、主发现与主修复：`recorded_cost_is_negative` 重扫浪费（R51 挂账项落地）

### 2.1 现状实测（窗口 24h）

`bg/routing_health_checks.go` 的 `recorded_cost_is_negative` 检查（~8-9 分钟一次，
≈168 次/天）在本窗口：≥1s 慢查 **158 次 / 2,010s**（mean 12.7s，max 29.6s），
较 R51 轮（154/1,529s，彼时已 +30%）**连续第二轮 +31%**——触发上轮预设的
「再增即立项」线；另有 **34 次/天被 statement_timeout 击杀**：那些 tick 里该检查
在健康报告里是 error，不是「无负成本」。

### 2.2 为什么是纯浪费（三层证据，全部本轮实测）

1. **源已冻结**：`storage.request_logs_write_enabled=false`（2026-10-13 起），
   `public._request_logs` 最新行停在 10-13（R51 已证，本轮复核仍成立）。
2. **全表负成本行 = 0**：252 生产 `count(*) WHERE cost_usd < 0`（**不限 30 天窗**）
   = **0**。代码注释里 10-06 时代的 1,628 条负成本行已随退役分区物理消失。
   即：检查现在每次顺序扫 ~43 万行 × 5 分区，只为返回空集。
3. **写路径有守卫**：负成本落 nil（"cannot compute"）而非负数——新负成本行
   结构上不应再出现。检查保留作 canary（覆盖 import/回填路径），但应该是
   sub-ms 的探针，不该是每 8 分钟一次的全表聚合。

### 2.3 修复：迁移 882（R51 §3.5 方案 (a) 落地）

- **形态**：父表级部分索引
  `CREATE INDEX idx_request_logs_cost_negative ON public._request_logs
  (ts, credential_id, outbound_model) INCLUDE (cost_usd, cache_read_tokens,
  prompt_tokens) WHERE cost_usd < 0`。
  键序：ts 首位服务 30 天范围谓词；INCLUDE 四列让聚合 index-only。
- **为什么建父表而非 532 式逐分区循环**：父表索引被 `CREATE TABLE … PARTITION OF`
  自动继承（877 台账订正实测口径）；生产 5 分区 relam=2（heap）逐分区实测 +
  fresh-install 路径（01-schema:1929 ensure 不带 USING columnar）全 heap，
  不存在列存拒绝面。零条目索引、构建 **1.83s**（生产实测）。
- **五点同步 + 通道腿全齐**：canonical/embeddata 双副本、main.go go:embed + map、
  dbinit StartupFiles、TSV 第 276 位、`apply-db-revision-sequence.sh` files 数组、
  `.down.sql`、契约门 ×4、台账行 + 指纹行。
- **生产验证（252，06:35 应用）**：
  - BEFORE：月分区 Parallel Seq Scan（cost 谓词挂 Filter）；
  - AFTER：**全分区 Index Only Scan**（子索引自动命名，877 口径）；
  - 真实检查语句 **43.276ms**（原 mean 12.7s / max 29.6s，≈290×）；
    34 次/天的超时击杀随量级消失。
  - 台账双行：`schema_migrations` 882 + `llm_gateway_migration_checksums`
    （sha `5b53bdd1…` 与文件现场一致）。应用时因外层事务与文件内 BEGIN 嵌套
    出现一次无害 WARNING（内层 COMMIT 先行提交，台账两行在后续隐式事务落库，
    均成功）——如实记录。
- **变异验证**：谓词改坏→字节一致门红；键序改坏（两份同步）→形态门红；
  embeddata 注入漂移→字节一致门红；还原全绿。形状门（非字节门）单独验证有牙。
- **附带回红修复**：881 的通道腿缺口（并行会话只做了 installer 五点、漏通道
  files 数组）——对账门本轮开工时实测红
  （`881 exists but is missing from the channel files=(...) array`），补登后
  **回绿**。生产 881 已于 10-20 由部署链 runner 应用（schema_migrations 与
  指纹表均有行），本次补的是升级通道形态本身。

## 三、错误 SQL 审计（窗口 3,121 条）

### 3.1 已关闭的上轮遗留（三项）

| 项 | 结论 |
|---|---|
| smm 守卫恒零终验（上轮遗留 #1） | **PASS**：24h 全窗 `ALTER TABLE daily_kline` **0 条**（errors+slow 双侧验证）。舰队补齐生效，销案 |
| 873-876 二进制部署（上轮遗留 #3） | **已部署**：154 = `2744-99aca741`（10-19）、245 = `2751-2776e55c`（10-19），两 sha 均在 HEAD 祖先链上。`AND AND`（f05058f7c）与本窗口错误表零条互证 |
| ursm-snapshot-health 量具超时（`WITH win` 的 15 次 timeout） | 并行会话 §143.14 **已于 10-20 晨修复并部署**：STMT_TIMEOUT 180s→600s + 反引号假错修复；252 `/opt/scripts/` 现场核验 `STMT_TIMEOUT:=600s` 在位。本轮不重复改 |

### 3.2 pocket `scheduled_tasks` $4 双型错——修复在仓、容器未部署（第 N+3 次复登）

`inconsistent types deduced for parameter $4`（integer versus bigint）**288 条**
（≈12 条/天，与上轮持平）。语句确认为
`UPDATE scheduled_tasks SET … next_run_at = $4 … WHERE id = $5`（claim 续租）。
**本轮新证据**：修复 `fa20e648`（2026-09-14）**在 openpocket 仓 HEAD 祖先链上**
（`backend/internal/scheduledtask/store.go` 的 `$4::bigint` 消歧在位），但
`opencode-pocket-pocketd-server-opp` 容器仍在跑修复前代码。部署属 openpocket
项目侧，本仓不代部署。挂账第 N+3 次：**每天 288 次失败的 claim 续租在持续累积**。

### 3.3 新登记（单发/低频，均非生产代码缺陷）

- **中文文本灌库 ×6**（21:33-21:44）：`invalid byte sequence for encoding "UTF8":
  0xe7 0x9a 0x3a`——0xe7 0x9a 是「的」的首字节：又有会话把中文叙述文本发进了
  数据库连接（与上轮 markdown 灌库同族）。编码错误连语句都无法落日志。
  修不在本仓；建议按客户端溯源（21:33/21:44 两个时刻的活跃 agent）。
- **`postgres` 密码失败 ×3**（12:26-16:45）：无语句可落；某工具/会话在用错口令
  连 postgres 账号。
- **`only heap AM is supported` ×3**：bloat 巡检脚本对
  `routing_decision_log_2026_09` 调 `pgstattuple_approx`——该函数只支持 heap，
  该分区非 heap。脚本侧应按 relam 分流（登记给 §97-104 线程的脚本维护者）。
- **V359 转型炸 ×2**（21:46）：`WHERE version::int BETWEEN 825 AND 870` ——
  schema_migrations 的 version 是 text 且有 V 前缀回溯行，`::int` 直接炸。
  已知坑（须 `^[0-9]+$` 过滤），某个 agent 会话现场踩中。
- **引用已删视图 ×6**：`request_logs_bodies` ×4（05:26 含一次 `[REDACTED]`
  内容普查）+ `request_logs` ×2（03:51）——owner 10-16 删视图层后，存储普查/
  验证脚本没跟着改（与上轮 §3.4 同族，宿主仍未定位）。**读法切 `_request_logs`
  / `request_logs_bodies_*` 物理族**。
- **05:2x-05:5x 诊断单发一批**（leaf_idx / btree / GROUP BY / ORDER BY position
  等约 10 条）：形态为 AI 会话现场探库的手滑（与本轮开工时间 06:0x 紧邻，
  高置信是并行审计会话），非生产缺陷，不计族。
- 维持上轮登记：外部探错库（llm_usage_stats / llm_optimization_* /
  cl.task_type，~10 条/天）、smm 自身缺陷（round/参数类型/integer out of range）、
  DSM 8+1、端口探测噪音。

### 3.4 取消与击杀的形态变化（对比上轮事件窗）

- `canceling statement due to user request` 1,492（上轮 2,865 且集中于事件窗）；
  本轮 23 时单小时峰 194（夜间批量窗）。
- `statement timeout` 229 **全天连续**（3-31/h）：主体是 `WITH latest`
  （usage_facts 对账，58 次）、抽屉 COUNT DISTINCT（42 次）、`WITH neg`（34 次，
  已修）、`WITH win`（15 次，§143.14 已修）。前两者为已知族在负载下的常态溢出，
  继续观察。
- **外部杀查询（parallel worker terminating）192 次，全天连续 2-35/h**
  （上轮 552 次且集中于事件窗）——外部终止者已从「事件中的清障动作」变成
  **常驻背景行为**。本仓零 `pg_terminate_backend` 调用点（R51 已普查），来源
  仍在仓外（与 10-18 早晨的杀查询同源）。**登记：需要 owner 指认是谁在杀**。

## 四、慢 SQL 审计（窗口 ≥1s 共 7,035 条 / 39,826s）

总密度 ~200-250 条/h（上轮事件窗 3,089/h 已过）；**22-02 时夜峰 430-565 条/h**，
构成 = `WITH matched`（夜间网关批量流量 213/720s）+ routing_analytics_7d /
session_client_stats 两个大 REFRESH + `WITH win`，与已知夜批形态一致，非新事件。

### 4.1 家族表（与上轮对齐；「趋势」均指 24h 对 24h）

| 族 | 本轮 | 上轮 | 定性 |
|---|---|---|---|
| `WITH neg`（负成本扫描） | 158 / 2,010s + 34 timeout | 154 / 1,529s | **已修（882）**：连续第二轮 +31% 触发立项线，R51 方案 (a) 落地 |
| `WITH win`（ursm 快照健康） | 12 / 2,195s，max 319s | 12 / 1,357s，max 113s | 频率持平、单条 +62%：7 天基线全表聚合随表长（26.3M 行 / 9 分区 / 8GB）增长。**量具超时半边已由 §143.14 修复**；「每日基线物化表」结构改动维持属主拍板（脚本头注释已写明）。若保留期 14d 落满（~16GB），单条将逼近 300s+ |
| `daily_kline` 读（`SELECT source, MAX(date)…` 等） | 209 / 2,214s，max 50s | 88 / 1,062s | **2×**。⚠ 更正一个口径：该表**不在 llm_gateway 库**（本轮实测 `relation "daily_kline" does not exist`，与 AGENTS 既有登记一致）——慢查日志是实例级、语句打在同实例另一个库。仓外系统，纪律 #32 不动；2,214s/天 的实例级代价登记给 owner 转 smm 侧 |
| `WITH latest_bucket`（CMI 刷新读） | 456 / 2,164s，max 23.8s | 585 / 3,524s | **−40%**：872 根修持续兑现，安静时点 0.19s 口径维持 |
| `WITH matched`（候选查询） | 765 / 2,116s | 1,792 / 6,021s | 事件受害回落后基线健康（−64%） |
| `WITH targets`（summaries 48h 回填） | 153 / 2,435s | 184 / 2,675s | **回落**，上轮「再增即立项」不触发，继续观察 |
| `WITH pick`（integrity_verify 排队计划） | 134 / 866s，max 27.8s | 201 / 1,144s | **回落**（未解决 high 事件 857 条是扫描面），维持登记 |
| REFRESH `routing_analytics_7d` | 60 / 3,144s，mean 52s | 51 / 4,489s，mean 88s | 均值改善（事件污染消退）；7d 窗裁决维持挂 11-02 |
| REFRESH `session_client_stats` | 31 / 2,110s，max 176s | 38 / 1,624s | **单条变慢**（mean 68s），登记观察：若下轮 mean 再升，立项分片 |
| `INSERT INTO provider_metrics_minute` | 335 / 1,102s | 653 / 2,465s | 事件受害回落，基线 |
| `SELECT c.id FROM credentials -- v1 臂`（selfcheck 拣选） | 246 / 682s，max 5s | 小额 | 放大至 known 量级：LEFT JOIN LATERAL hot 臂在白天的常态成本，夜间基线健康，登记 |
| promote 族（turns/bodies/wal/memora） | ~146 次 / ~1,450s | 事件窗放大 | 基线持平（max 47s 在 06:51） |
| bloat/尺寸普查（pgstattuple/pgstatindex/relsize 族） | ~280 次 / ~1,600s | 95 / 364s | 普查脚本加频（§97-104 线程 + 独立普查），其中 `only heap AM` ×3 见 §3.3 |
| `-- ping` 心跳 | 106 / 264s | 持平 | 正常 |

### 4.2 阴性结论（本轮查过、不成立）

- `AND AND` ×0、`//` 草稿 ×0——上轮两大新签名随 f05058f7c / 3be79b5df 部署消失，
  互证 154=2744/245=2751 已含修复。
- 慢查总量 39,826s 对上轮 67,729s：无新事件级爆发；夜峰是已知夜批 + 大 REFRESH
  的叠加，不是退化。

## 五、改动清单

| # | 位置 | 性质 |
|---|---|---|
| 1 | `sql/migrations/startup/882_request_logs_cost_negative_partial_idx.sql` + `.down.sql` + 契约门测试 | **新增迁移 882**（本仓代码） |
| 2 | `installer/`（embeddata ×2、main.go embed+map、dbinit runner StartupFiles、TSV 第 276 位） | 882 五点同步 |
| 3 | `scripts/apply-db-revision-sequence.sh` files 数组 | **补登 881（回红）+ 登记 882** |
| 4 | `docs/db-changelog.md` | 882 台账行（applied+verified） |
| 5 | 252 生产 llm_gateway 库 | 882 预应用（索引 1.83s + schema_migrations/checksums 双行；三实例共用库立即受益） |
| 6 | `docs/audit/2026-10-20-252-sql-log-audit-round.md` | 本文档 |

无其它生产写操作。pocket / daily_kline / 外部杀查询均属仓外，只登记不代修。

## 六、门与验证记录

- 通道对账门 `apply-db-revision-sequence_test.sh`：开工时红（881 缺通道腿）→
  补登后 **rc=0**。
- installer 全量 `go test ./...`：**13 包 ok**（含 lock guard 门——882 初版缺
  `lock_timeout` 被它当场抓住，补 `SET LOCAL lock_timeout='60s' +
  statement_timeout='10min'`（878 同款）后转绿；这条门是本轮实际拦下缺陷的门）。
- 882 契约门 ×4 PASS + 变异三连（谓词/键序/embeddata 漂移）全红→还原绿。
- `internal/sqlguard` 台账门（RegistryStaysContinuous / ShaIsActuallyTheFile）PASS。
- gofmt：main.go（含并行会话遗留的 873-881 map 错位一并整块对齐）、runner.go、
  契约门测试全干净；`go build ./...`（installer 模块）OK。
- 生产验证：EXPLAIN 前后对比（Parallel Seq Scan → Index Only Scan）+ 真实语句
  43.276ms + 双台账行核对 + 子索引自动生成且被计划器选用。
- 解析器：上轮三项修复在本窗口全部复效（孤儿 0 / 伪影 0）。

## 七、遗留与下一轮

1. **882 部署链收尾**：预应用已生效；下次 154/245 部署后核对 runner 幂等重放
   （IF NOT EXISTS 应为 no-op，不应有第二次 1.83s 级构建）。
2. **pocket $4**（288/天）：openpocket 侧安排 pocketd 容器重新构建/部署
   （修复已在仓）。挂账第 N+3 次。
3. **外部杀查询常驻化**（192 次/天，2-35/h）：需要 owner 指认来源
   （10-18 事件中同一行为的高置信同源）。仓内零调用点，R51 口径维持。
4. **`WITH win` 结构性成本**：每日基线物化表（需属主拍板，§143.14 已留注释）；
   保留期若落到 14d 满额，单条将逼近 300s+，届时 cron 重叠风险回归。
5. **`session_client_stats` REFRESH 单条变慢**（mean 68s）：下轮再升立项分片。
6. **`daily_kline` 2×**（实例级 2,214s/天）：转 smm 侧（该库非本仓管辖）。
7. **普查/验证脚本改读物理族**：`request_logs` / `request_logs_bodies` 已删视图
   的引用方（`src_rows_1d` 特征 + 05:26 `[REDACTED]` 普查）仍未定位宿主。
8. **台账行滞后**：879/881 的 pending-deploy 行落后于生产实况（10-20 已应用）——
   本轮不改他会话的行（属越权），登记提醒各作者回填。
9. **量具坑入册**：裸 grep 间歇零输出（用 /usr/bin/grep）；podman `\i` 容器内
   路径；zsh `===word===` 展开坑；管道后 `$?` 取尾命令（本轮自我纠正一次）。
