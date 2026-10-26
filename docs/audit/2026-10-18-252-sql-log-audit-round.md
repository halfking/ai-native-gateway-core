# 252 SQL 日志审计轮（2026-10-18）

> 会话：ZCode root（本仓 llm-gateway-go-4 checkout）。
> 目标（用户指令）：对 252 docker 中 pg-252-pg17（llm-gateway 项目数据库集群）的全部
> SQL 日志审计，找出慢 SQL 与错误 SQL，分析并修正。
> 上轮：2026-10-17（r1034，UNION 洪水修复 24748e233）。本轮窗口从上轮覆盖边界之后起算。

## 一、窗口与方法（起点重算）

- **日志源**：podman `ctr.log`（`/var/lib/containers/storage/overlay-containers/dc5e…/userdata/`），
  PG17 `log_min_duration_statement=1s`、`log_statement=none`、`log_line_prefix=%m [%p] `。
- **窗口**：`ctr.log-20261018`（10-17 03:44 → 10-18 03:20，286MB）+ `ctr.log`（10-18 03:20 → 06:02，386MB）。
  更早的 `ctr.log-20261017.gz` / `20261016.gz` 已被上轮及更早轮覆盖，未重读。
- **方法**：perl 逐行状态机（剥离 podman `TS stderr F` 包装；`%m [%p] LEVEL:` 行开新记录，
  无前缀行并入当前语句体；`STATEMENT:` 行只在错误记录采纳）。产物：错误家族表（118 族）、
  慢查家族表（1172 族）、逐小时直方图四张。
- **量具坑（沿用 r1034）**：语句体行无 pid 前缀、podman 对超长行做 `stderr P/F` 分片、
  并发 pid 块交错 ⇒ 单词 grep 会漏/重，家族化必须合并语句体后按规范化文本聚合。
  本轮新增：`<EMPTY>` 慢查 350 条 = 语句体被交错行切断的解析伪影 + 被取消查询的空文本，
  不是独立查询族。

## 二、错误 SQL 审计（窗口 46,061 条 ERROR/FATAL）

### 2.1 UNION 洪水收口确认（上轮修复的端点重算）

| 小时 | 条数 |
|---|---|
| 10-17 05 | 11,434 |
| 10-17 06 | 28,226（峰值） |
| 10-17 07 | 98 |
| 10-17 08 | 720（残尾，最后一条 **08:30:29**） |
| 10-17 09 起 | 恒 0 |

`UNION types character varying and bigint cannot be matched at character 766` 共 40,160 条，
**10-17 08:30 后窗口内零复发** ⇒ r1034（24748e233，245=2687/154=2688）的「洪水归零」在本轮
更精确的端点下成立：残尾比上轮记录多烧了约 2.5 小时（部署滚动期），08:30 全灭。

同窗口伴随族（洪水连带，随洪水一起消失）：`canceling statement due to user request` 2,543 条
（05:39→06:08 集中）、`connection to client lost` 1,270 条（同窗口）、
`inconsistent types deduced for parameter $4 at character 135` 294 条中的洪水窗口部分
（见 2.3，该族另有持续成分）。

### 2.2 「同属」裸中文语法错 ×11（10-18 02:57-02:58）——已由仓库修复闭环

`ERROR: syntax error at or near "同属" at character 2993`，STATEMENT 是
`SELECT p.id, COALESCE(p.code,'') … FROM providers p …` 的大查询。取证（保留换行逐行定位 +
行首十六进制 `090909 2f2f`）：**第 45 行行首是 C 风格注释 `//` 而非 SQL 注释 `--`**，
`//` 后的中文文本被当作 SQL 解析，解析器在 `error_rate_24h 同属` 两个连续标识符处报错。

**这不是注入、不是新缺陷**：该草稿即 `admin/providers.go` getProvider 改版 SQL，
缺陷进过仓库、由 `e8f186916`（fix(providers): 注释漏写 -- 让 getProvider SQL 语法错）修复；
`provider/sql_comment_syntax_test.go` 专项门在场且本轮实测绿。02:57 的 11 连错是该缺陷代码
真库门/部署期的历史排放。**无残留动作。**

附带同族一次性项（人工/诊断 SQL，登记不修）：`ardized_name` 截断标识符 ×3（06:26，
EXPLAIN 诊断文本被截 7851 字符）；`left(jsonb,integer) does not exist` ×3（07:43，人工查
session_bodies_hot 的 jsonb 列）；`z.gates_per_task` GROUP BY ×2、`column "id"` ×2、
`runtime_type` ×2（acc/orchestrator 诊断脚本自身 SQL bug）；`role "acc_super" does not exist` ×2
（02:22 外部连接用错角色）。

### 2.3 pocket `scheduled_tasks` $4 双型错——仍在烧（owner 挂账第 N+1 次复登）

`inconsistent types deduced for parameter $4 at character 135`，语句
`UPDATE scheduled_tasks SET last_run_at=$1, … next_run_at=$4, lease_until=0, enabled=CASE WHEN $4=0 …`
：$4 同时被推断为 timestamptz 与整数。**本窗口 294 条（≈11 条/小时），与 10-16 轮 ~287/天
速率一致**；pocketd 容器虽在 10-17 00:47 重启，错误持续到 10-18 06:03 ⇒ 修复 `fa20e648`
**至今未上线**（维持「树有 WIP 不代部署」裁决，仅更新证据）。每次失败都是一次
claim 续租失败，pocket 任务调度功能性受损，建议 owner 尽快部署。

### 2.4 凌晨错误回升（10-18 02-06 时 600-850 条/小时）——已知族，非新缺陷

构成：`canceling statement due to user request` ~1,200 + `connection to client lost` ~800 +
`terminating background worker "parallel worker"` ~450 + `statement timeout` ~290。
与 R31 归因的 **smm nightly-backtest 经网关大 payload 断连** 特征完全一致，属已知夜间族。
同窗口 `could not attach to dynamic shared area` 40 条 / `could not map DSM segment` 19 条
= 并行查询基础设施已知噪音（R2x 登记）。

### 2.5 外部客户端探错库（登记，修不在本仓）

08:08 与 19:52 两批：`INSERT INTO llm_optimization_reports …` / `SELECT … FROM llm_usage_stats …`
/ `prompt_analysis JOIN chat_logs …` —— 某个「LLM 优化器」工具的表在 llm_gateway 库不存在，
**它拿着本库 DSN 在找自己的表**（打错库）。另有 smm 自身缺陷：`could not determine data type
of parameter $2`（fundamentals 查询，6 条）与 `integer out of range`（limit_up_temp INSERT，3 条）。
均登记给 owner；前者建议排查哪个 agent 配置指向了 llm_gateway DSN。

## 三、慢 SQL 审计（窗口 ≥1s）

### 3.1 主修：autoroute 刷新语句（`refreshIndexSQL`）——本仓第一名，已修（迁移 872）

- **量测**：`pg_stat_statements`：**calls=4383 / mean 2310.8ms / max 25,229ms / total≈2.8h**；
  日志窗口内 ≥1s 慢查 **488 次**（均值 4.2s / 最长 26s），**全天平坦分布** ⇒ 内在慢，
  非夜间负载连带。
- **归因**（EXPLAIN ANALYZE，BUFFERS）：89% 时间在两个月分区 423k 行的列存扫描
  （2026_09 707ms / 2026_10 730ms），HashAggregate 仅 ~120ms。关键异常：
  **2026_10（121k 行）吃 16,308 buffer hits，2026_09（301k 行）只要 660** ——
  主文件仅 970 页却 16k 次命中 ⇒ 海量微小 stripe 反复解压。
- **病根**：本族是 5 分钟增量小批量写（lifecycle 登记「凭据模型健康度索引（5min rollup）」），
  citus columnar 每事务落独立 stripe ⇒ 增量写把列存打碎。2026_09 是一次性批量转入所以健康。
  同型先例：806（bodies）/ 812（model_probe_runs）/ 813+836（supplier_errors）。
- **治理面三缺一（为什么只剩存量没人转）**：`columnar_insert_only_parents()` 活库与三基线
  均已是 `{routing_decision_log}`（CMI 已摘）；`ensure_credential_model_index_partition`（694）
  建的就是 heap 分区；`columnar_healthcheck()` 活库已把本族列入 should_be_heap 并**正在报
  三行 non-compliant**。与 836 对 813 的判定同构：检测面在说话，函数面已正确，存量没转。
  842 索引救不了（其注释自证：GROUP BY 无法松扫描，PG17 无解）。
- **修复**：迁移 **872**（DETACH→bak 改名→同边界重建 heap 叶→回拷→守恒校验→DROP bak，
  空分区走 812 空壳通道+TOCTOU 收口；`lock_timeout=2s` + `statement_timeout=10min`；
  落地自证三断言：healthcheck 仍认识本族、白名单不含本族、零非 heap 分区）。
  **252 预应用实录**（md5 与仓库副本一致后执行）：301,255 + 121,609 行搬运守恒
  （10 月分区比审计时点计数多 22 行 = 转换窗口内并发 promote 落叶，校验「新 ≥ bak」容纳）、
  空分区 2026_11 重建、自证过、COMMIT。
- **验收**：`columnar_healthcheck()` 三行全部 **compliant=t**；单次 EXPLAIN 3.17s → 1.08s。
  ⚠ **诚实口径**：预应用后稳态单跑实测 1.45s / 3.29s / 7.9s（剧烈方差）——测量窗口恰逢
  **主机 load 40-47**（smm 晨间回测 + promote + REFRESH 并发，07:05 实拍），单查询 A/B 在
  此负载下不可信。结构性的 stripe 病（16,308 hits 的 25 倍放大）已根除、健康门转绿是硬证据；
  **真实收益以 pg_stat_statements 均值后续回落为准**（登记下轮首查）。
- 六点同步：canonical/embeddata 副本 SHA 一致、main.go go:embed+map、runner.go StartupFiles、
  TSV（-update 再生）、通道 `files=()`、`.down.sql`（836 式验证式 no-op）——installer 13 包全绿、
  通道门 rc=0、sqlguard 双门 PASS、本机真库 up×2+down+up 四连跑通过。

### 3.2 次修：871 通道腿+台账双缺口（并行轮交付的登记事故，本轮补齐）

`apply-db-revision-sequence_test.sh` 实测红：**871 文件与 installer 腿在，但通道 `files=()`
缺登记**（845-851 八连红同型）且 **db-changelog 无 871 行**。按 870 条目样式补通道登记 +
台账行（sha `f9f98c58…` 现算）。通道门转绿。**教训重申：迁移交付是六点同步，缺一道就是
「installer 记账、任何环境都不执行」的静默缺口。**

### 3.3 已定性健康 / 已知 / 连带（登记不改码）

| 族 | 窗口量测 | 定性 |
|---|---|---|
| 候选查询 `WITH matched AS MATERIALIZED`（provider/client.go） | ≥1s 1105 次/2945s，max 27s；pg_stat_statements **calls=9295 / mean 138ms** | **健康**。≥1s 尾部集中在 02-06 时（116-161 次/h vs 白天 ~25）= 夜间负载连带；r0926 优化（61ms 暖目标）成立 |
| `<EMPTY>` | 350 次/4024s | 解析伪影（交错切断）+ 被取消查询，非独立族 |
| 健康面 COUNT 面板（`session_turns JOIN _request_logs`） | 165 次/3530s，max 60s | 已知族（纪律 78；parallel worker 连坐主体），noise 非故障 |
| REFRESH `routing_analytics_7d` | 57 次/5583s，max 163s | 已知族，7d 窗裁决挂 11-02 复核 |
| REFRESH `session_client_stats` / `session_task_stats` | 43+43 次，max 172s/145s | 已知 matview 族（R12/R27） |
| CMI hot 写入 `INSERT INTO credential_model_index_hot` | 210 次/625s，全天分布 | 负载连带（pg_stat_activity 实拍 WALWrite 等待） |
| `WITH targets`（session_summaries 48h 回填） | 167 次/2504s，max 113s | admin 面读，量级中等，登记 |
| `WITH neg`（负值成本扫描 30d） | 117 次/1148s | bg 对账读，登记 |
| `WITH win`（3h 窗口序列） | 17 次/2213s，max 174s | 低频，登记 |
| `WITH deduped`（usage_ledger 月度对账） | 8 次/1136s，max 299s | 870 覆盖索引已建（10-17 applied+verified），本窗口大部分在 870 生效前；后续窗口复核 |
| promote / TTL DELETE / provider_metrics_minute 等已知族 | 若干 | 全部 ≤ 既有登记水位 |
| smm `daily_kline` ALTER | **145 次/1846s，max 55s** | 见 3.4，已修 |

### 3.4 修正（仓外）：smm `daily_kline` ALTER 洪水守卫（252 宿主机）

- **量测**：145 次 ≥1s（均值 12.7s / max 55s）；审计期间**现场活捉一条正在跑 27s**，
  守卫部署前最后一条 **54.7s**（07:11:40）。R27 以来第三次升级（87→190→369/天 → 本窗口 145）。
- **根因**：`/opt/kaixuan/smm/smm/src/data/pg_store.py` `_init_tables()` 每次实例化都跑
  `ALTER TABLE daily_kline ADD COLUMN IF NOT EXISTS adjust …`；PgStore 随 cron（*/5、*/10 多条目）
  反复实例化 ⇒ 每天 ≤288 次 ALTER，**即便无事可做也拿 ACCESS EXCLUSIVE 排队**。
- **修复**：information_schema 列存在性守卫（普通读，无锁），列真正缺失（全新部署首启）
  才走原 ALTER；`[TAG:DAILY_KLINE_ADJUST_GUARD]` 标记。备份
  `pg_store.py.bak_20261018`；宿主→容器挂载确认（`/opt/kaixuan/smm → /app/data`）后用
  **容器内解释器**实跑 `_init_tables()` ×2 通过。
  ★ **部署期间实测踩到第二副本**：cron 存在两种 workdir 形态——`-w /app/data/smm`
  （导入挂载盘代码，已守卫）与 `-w /app/data`（**导入镜像内置 `/app/src/data/pg_store.py`**，
  仍是无守卫旧版——07:17 的 ALTER 即来自它）。diff 证实镜像副本与宿主副本除守卫外零漂移，
  已 `docker cp` 同步补丁（容器内备份 `.bak_20261018`）并再次实跑通过。
  ⚠ 两处均为非 git 原位改：**容器 recreate（compose up 重建）会丢镜像层补丁**、
  smm 重发布可能覆盖宿主文件——登记给 owner 把守卫带回 smm 源仓并随镜像重建。
- **待验证**：部署于 07:14；07:11:40 之后至收尾时无新 ALTER 行（下个 cron 周期后应恒零，
  下一窗口首查项）。

### 3.5 主机负载（量测口径警示）

审计时段（07:05）**load average 47.11**，pg_stat_activity 同时可见：REFRESH routing_analytics_7d
（1m35s）、健康 COUNT 面板 ×3、promote ×2、daily_kline ALTER（27s）、daily_kline INSERT
WALWrite 等待。本窗口全部慢查/取消/超时的公共放大器就是它（smm 夜间回测 + 日常 rollup）。
单查询时序类结论（包括 3.1 的 after 测量）都必须以此为噪声底解读。

## 四、改动清单

| # | 文件 | 性质 |
|---|---|---|
| 1 | `sql/migrations/startup/872_credential_model_index_partitions_heap.{sql,.down.sql}` | 新迁移（CMI 月分区 columnar→heap，自证门内置） |
| 2 | `installer/cmd/llm-gw-installer/main.go`、`internal/dbinit/runner.go`、`embeddata/startup/872_*` | installer 五点同步 |
| 3 | `sql/schema/installed_startup_migrations.tsv` | -update 再生 |
| 4 | `scripts/apply-db-revision-sequence.sh` | 871 + 872 通道登记（871 为补齐并行轮缺口） |
| 5 | `docs/db-changelog.md` | 871、872 台账行（sha 现算） |
| 6 | 252:`/opt/kaixuan/smm/smm/src/data/pg_store.py` | smm ALTER 守卫（仓外，备份在案） |
| 7 | 252 llm_gateway 库 | 872 预应用（9+10 月分区转堆，守恒校验过） |

## 五、门与验证记录

- `bash scripts/apply-db-revision-sequence_test.sh`：补登前 **红**（871 两问题）→ 补登后 rc=0。
- `cd installer && go test ./...`：lock-guard 门先红（872 缺 lock_timeout，按 §44 补
  `SET LOCAL lock_timeout='2s'`）→ 13 包全绿。
- `CGO_ENABLED=0 go test ./internal/sqlguard -run 'TestMigrationRegistry|TestRegistrySha'`：PASS。
- 本机 llm_gateway 真库：872 up×2（幂等）+ down（验证式 no-op）+ up 全过。
- 252 预应用：md5 一致 → 执行（守恒 + 自证过）→ healthcheck 三行 compliant=t。
- smm 守卫：py_compile 过 → 容器内 `_init_tables()` ×2 过。
- 变异口径说明：本轮的「变异验证」由三道真实红→绿承担（通道门 871 红→绿、lock-guard 门
  872 红→绿、872 内置自证在 252 真库执行）；UNION 洪水归零以上轮部署为变异体自然成立。

## 六、遗留与下一轮

1. **872 真实收益复核**（下轮首查）：`pg_stat_statements` 中 refreshIndexSQL 的 mean_exec_time
   应从 2310ms 显著回落；smm ALTER 族应恒零。注意 870 台账同款验收口径教训（父表 0 bytes
   是正常渲染；本迁移验收看 `columnar_healthcheck()` 与 relam，不看尺寸）。
2. **pocket $4 双型错**（≈294/天）：owner 部署 fa20e648，本轮第 N 次复登。
3. **llm_optimization_* 探错库工具**：排查哪个 agent/工具配置指向了 llm_gateway DSN。
4. **smm 源仓回灌**：pg_store.py 守卫是宿主原位改（非 git），重发布会被覆盖；
   owner 应把守卫带回 smm 上游仓库。
5. **主机 load 40-47**（晨间）：smm nightly-backtest + rollup 叠加，慢查公共放大器；
   R31/R32 已登记的宿主内存/负载治理仍是正路。
6. `WITH targets` / `WITH win` / `WITH neg` 三族量级中等（合计 ~25 min/窗口），未达修线，
   下轮如再现抬头再立项。
7. 870 台账注记的「对账外层 Sort external merge 34MB temp」次级问题未处理（维持登记）。
