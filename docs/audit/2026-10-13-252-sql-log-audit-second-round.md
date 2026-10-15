# 252 PG SQL 日志审计轮·第二（同日续轮，2026-10-13 下午）

> 会话目标（用户）：对 252 docker 中 pg-252-pg17 的 SQL 日志做全量审计，找出慢 SQL 与错误
> SQL，分析归因并修正发现的问题。
> 窗口：**2026-10-13 06:08 → 16:22 CST（10.2h，本报告主窗口）**；快照实际覆盖
> 03:12 → 16:22（13.2h，`docker logs` 活缓冲，739k 行 / 207MB），与上一轮
> （`2026-10-13-252-sql-log-audit.md`，止于 06:08）**衔接零缺口**。
> 定位：同日续轮。上一轮修复（auto-cool hot 化 514a273e0）已随 **154=2576(9e2d77b9)、
> 245=2601(95640a65)** 上生产（本轮实测 version.json 确认），效果复测直接可判。
> 桥接量具：`pg_stat_statements`（stats_reset=2026-10-10 16:11，72.75h 窗）横跨
> 上轮+本轮，用于族级均值/调用数与慢日志 ≥1s 截断互补。

## 一、量具与提取器（本轮踩坑记录）

1. 提取器三轮演进：v1（`CST [pid] LEVEL:` 头锚定，120 字符签名）→ v3（续行拼接至
   400-2600 字符）。**三个伪影与修正**：
   - 裸 `statement:` 2,546 条 = 全部**多行语句**（duration 行只有语句头，SQL 在续行），
     v1 口径下全部落 OTHER——第一版"session_turns 慢写 250/h"的判断**据此撤回**
     （真实慢写仅 3 条）；
   - 族分类必须 `tolower` 后匹配（`INSERT INTO`/`REFRESH` 大小写曾把 ra7d 全族
     塞进 OTHER，墙钟 4.36h 是被错误充大的）；
   - shell `paste -d"\t"` 在该环境不解释转义 → 墙钟列全 0，改用 awk 重建配对。
2. pss 解码坑：输出列序是 `tot_s | calls | mean_ms`，第一版按 mean 误读
   （"INSERT request_logs_hot mean 105s"实为 105,328 次调用 × mean 43ms）。
3. 客户端归因三连排（本轮新增事实）：
   - 252 本机裸进程网关（PID 508581，:18082，9.5 天）**连的是 memora 库（kxuser），
     不是 llm_gateway 客户端**——上一轮登记项 1 的对象定性据此修正；
   - `ssh -L` 真库门隧道必须指 `172.16.2.210:5432`（PG 不监听 loopback；
     252 本地 15432 被 nginx 占用但那在远端，不冲突）；
   - 裸网关进程的 `POSTGRES_*` env 可作 DSN 来源，但其 DB 不是 llm_gateway。

## 二、错误族二段计数（1,234 条锚定）

| 族 | 条数 | 判定 |
|---|---|---|
| cancel_user + conn_lost | 555 + 367 | 流式客户端取消（与上轮同形态）；速率 ~90/h vs 上轮 ~330/h，**降 3×** |
| inconsistent_types $4 | 123 | E6 pocket scheduled_tasks，12/h 与 R15 登记完全一致——外部，维持移交 |
| stmt_timeout（30s rolconfig 击杀） | 99 | ≥49 条 -A4 窗口伴随 daily_kline 语句文本（smm 外部主题延续）；其余语句锚在窗口外未逐条分解（提取窗过浅，如实记录） |
| bgworker parallel_term | 56 | 领头语句被杀陪葬（症状非病因） |
| **DSM attach 失败** | **13** | **1.3/h vs 上轮 4.2/h，降 3×**——auto-cool hot 化（消并行 worker 需求侧）方向性验证成立，但未归零（backfill 并行来源本轮修复二，见 §四） |
| password_auth_failed / deadlock | 2 / 2 | 零星 |
| AI 探库/语法 singleton | ~14 | employees/llm_usage_stats 幻觉 SQL、GROUP BY position、round(double precision)、`tot` FROM-clause、unsupported protocol——外部指纹延续（R30 同款） |

## 三、慢查族谱（≥1s 共 3,532 条，墙钟 ≈5.7h / 10.2h ≈ 0.56 核，上轮 0.81 核）

| 族 | 条数 | pss 72.75h 口径 | 判定 |
|---|---|---|---|
| WITH_matched（路由候选，provider/client.go D10 修复形态） | 269 | mean **78.8-97.4ms**，max 19.2s | **健康**——慢日志条目是饱和期队尾，**撤销"计划回归"嫌疑**（D10 的 61ms 基线仍成立） |
| **WITH targets（project_backfill worker）** | 65 | **439 次 × mean 23.1s** | **本轮修复一**（§四） |
| count-view（summarystore v1 回退） | 29 | — | **本轮修复二**（§五） |
| **latest_bucket（autoroute 索引刷新）** | 199 | **1,823 次 × mean 2.6s** | 根因证明+修复设计被数据否决，登记专项（§六.1） |
| supplier aggregator（date_bin） | ~90 | 1,765 次 × mean 2.65s ≈ max | 稳定成本未复现（20min 窗 EXPLAIN 亚秒），登记不盲修 |
| MVREFRESH_ra7d | 76 | 469 次 × mean 26.8s | 并行轨主线（较上轮 65.2s 均值回落但仍 3.5h/72h 总量） |
| SMM daily_kline + ALTER | 191+7 | — | 外部第四次登记延续 |
| stats minute 族 | 121 | — | **大幅回落**（墙钟 302s vs 上轮 3.5h/54h 折算——S4 停写后 rollup 行数减少的自然结果） |
| promote 四族 | 161 | — | 合法（30-50s，60s SET LOCAL 内） |
| is_final_success UPDATE（telemetry claim） | 41 | **10,253 次 × mean 918ms，max 918s** | max 15 分钟疑似锁等待，登记 §六.4 |
| MAAS assets | 61 | — | 正常写入 |
| WITH_win（auto-cool 旧视图形态） | 65（281 含 03:12-06:08） | — | 来源排除法+已停止，见 §六.5 |
| probe queue picker | — | **4.83M 次 × mean 3ms** | 调用量级登记（19 次/秒的探针队列轮询，单查不慢） |

## 四、修复一：project_backfill worker 批调优去并行

**根因（252 生产 EXPLAIN 实证）**：`WITH targets` 选择谓词的计划是 **Gather Merge +
2 并行 worker**——每 10 分钟 tick 分配一次 DSM 段。850 索引正常走位（Bitmap 20,133
行），随后对 session_dim_pkey 做 ~1.7 万次 probe（64,786 buffers），行几乎全部双 NULL
（Rows Removed by Filter: 1/probe）——不可解析水库在 48h 窗内每 tick 被整个重探。
pss：439 次 × mean 23.1s（72.75h）≈ 0.9h/天，与 DSM attach 失败时段吻合。
R31"6,700s/天归零"的口径是全表扫描时代（40-117s/次）；23.1s×439 说明**残留成本仍在且
以 DSM/并行税形态存在**。

**修复**：`backfillBatchTuningSQL` 包级常量（statement_timeout 抬高 + 
`max_parallel_workers_per_gather='0'`，均 SET LOCAL），worker 每批事务开头执行；
worker 与真库计划断言共用同一常量（"测的就是跑的"）。

**验证链**：
1. 252 生产双腿计划断言：tuned 后计划零并行节点（Aggregate→Sort→Nested Loop→2
   Index Scan）；不调优时 Gather Merge + Workers Planned: 2（对照腿有牙）；
2. 静态钉：`TestBackfillBatchBudgetIsTransactionLocal` 升级——调用点（backfillOnceBody
   函数体顺序钉）+ 常量内容双向钉死；
3. 真库门：`TestBackfillBatchPlanHasNoParallelWorkers_RealDB`（EXPLAIN 纯计划零执行，
   无 fixture/scratch 需求；对照腿在表过小环境诚实 SKIP 并注明需 252 规模复跑；
   本轮已按其语义在 252 完成人工双腿验证）；
4. **变异验证**：M2 去掉并行 set_config → 常量内容钉红；M3 worker 不再调用 → 调用点
   钉红。均红在正确断言。
5. `go build`/`go vet`/`gofmt` rc=0；`CGO_ENABLED=0 go test ./bg/` 全量 ok（32s）。

## 五、修复二：summarystore v1 回退腿视图→分区父表

**根因**：`v1FallbackCountSQL` 走 `request_logs_with_current_month` 三臂视图，但回退腿
**只在会话族腿返回 0 时执行**——此时本会话在 session_turns(_hot) 无行可反连接，视图的
turns 两臂 + details join 是 leg 1 刚证明为空的纯重复扫描。252 实测：1.0-5.6s × 29
次/10.2h（自动摘要滚动门每轮完成触发），与 promote/饱和时段**无相关**。
与 promote 无相关 ⇒ 锁等待假设不成立；主因判定为**多臂扫描面 × 饱和期的组合成本**
（单跑瞬时 119ms，日志期 1-5.6s），残余不确定性如实登记。

**修复**：`SELECT COUNT(*)::int FROM request_logs WHERE gw_session_id=$1 AND success=TRUE`
（分区父表直读：hot∪各月分区全覆盖，252 实测 legacy 会话 **638==638** 与视图对拍一致，
119ms/638 行全索引探）。

**验证链**：
1. 等价性论证：family=0 时反连接对回退场景恒不命中 ⇒ 视图≡父表（外加 638==638 实测）；
2. 钉子演进：`TestS3Wave2TurnCountSources` 从「必须视图」演进为「必须父表 + 禁视图 +
   禁裸 hot」双向钉死（S3 波 2 的 promote-排空关切由父表天然满足）；
3. **变异验证**：M1 回退视图形态 → 双断言红（应父表/不应视图）；
4. `CGO_ENABLED=0 go test ./internal/summarystore/` ok。

## 六、登记项（不修，移交/待窗口）

1. **★ latest_bucket（autoroute 索引刷新）——根因已证、修复被数据否决**：
   `refreshIndexSQL` 的 latest_bucket CTE 对 `credential_model_index_with_current_month`
   全视图 GROUP BY——252 实测视图形态 425ms（ColumnarScan 40.2 万历史行：
   2026_09=301k + 2026_10=101k）vs hot 形态 **21.5ms（20×）**。**但** hot 只留 <24h
   （2026_10 分区 max(bucket)=当日 08:30，promote 排空很快），**1,068 组合中 340
   （32%）的最新 bucket 只在分区**——hot-only 会把三分之一 autoroute 候选丢出索引，
   语义不可接受。**设计方向**：写入端（refresher writer arm）以
   `(credential_id, raw_model)` 为键 ON CONFLICT 维护 latest-bucket 状态表（或等价的
   upsert 列），读侧 O(1)——留专项轮，勿直接 hot 化。
2. **ra7d REFRESH**：mean 26.8s × 469 次/72h（较上轮 65.2s 回落），并行轨主线，继续移交。
3. **supplier_error_stats_aggregator**：pss mean≈max≈2.65s（1,765 次）稳定成本，
   20min 窗简化 EXPLAIN 亚秒——真实参数形态未复现，登记待专项（先抓真实 $3..$6 窗）。
4. **is_final_success UPDATE**（telemetry/client.go claim 路径）：10,253 次 × mean
   918ms、**max 918s（15 分钟）**——max 疑似行锁等待，量级值得专项看一眼（不加索引，
   先取证 pg_locks）。
5. **WITH_win 旧视图形态 65 条（窗口内，281 含凌晨）——来源未定，已停止**：排除法
   坐实非 154/245（二进制含 514a273e0 且 hot 形态秒级不进慢日志）、非 252 裸网关
   （连 memora 库）；全部裸形态（非 EXPLAIN 包装）、递减至 15h 归零、含 161.3s 完成
   量（执行角色无 30s rolconfig）。最可能：**并行审计会话的真实库对拍/取证负载**
   （如 auto-cool 修复的 EXCEPT 对拍基线腿，postgres 角色）。下轮若复现：轮询
   pg_stat_activity 抓现行。
6. **smm pg_sleep(4.2) 突发**：06:35:44-06:42:59 ×98（≈14/min 重试节律），与同时段
   `pg_advisory_lock('smm_pg_schema_init')` 等待×6、ALTER×7 同窗——smm 应用内
   schema-init 竞争的症状面，外部维持移交（第四次登记的补充证据）。
7. **252 裸进程网关定性修正**：PID 508581（:18082，10-04 构建）连 **memora 库**
   （kxuser@172.16.2.210:5432/memora），**不是 llm_gateway 的过期网关**。上轮"重部署
   或退役"建议的对象应改为 memora 侧维护者裁决；它不含 llm_gateway 安全修复一事
   不再构成 llm_gateway 数据面的暴露（其数据面是 memora）。
8. AI 探库群（~14 singleton）+ password_auth 2 条：外部指纹延续。
9. probe queue picker 调用量级（4.83M/72h ≈ 19/s）：单查 3ms 不慢，纯量级登记。

## 七、DSM 复发复核（上轮登记项 2）

106 条（上轮窗）→ 13 条（本窗），4.2/h → 1.3/h。auto-cool hot 化消掉一个并行需求侧
来源的方向性验证成立；本轮修复一再消一个（backfill 每 10min tick 的 Gather）。
预期下轮继续回落；根治（/dev/shm 扩容）仍需容器重建 = 维护窗口。

## 八、自审计六件套

1. **结论/根因**：10.2h 窗 1,234 错误 + 3,532 慢查全部锚定分族；两个可安全修复项已修
   （backfill 并行税、回退计数形状浪费），两大登记项带完整证据链（latest_bucket 20×
   与 32% 丢弃集、aggregator 稳定成本）；WITH_matched 健康结论撤销回归嫌疑；
   WITH_win 来源排除法+已停止。上轮修复（auto-cool）经本窗验证无复发（生产二进制
   已携带，hot 形态零慢查）；DSM 降 3×。
2. **改动**：`bg/project_backfill_worker.go`（backfillBatchTuningSQL 常量 + 批内执行 +
   头注）、`bg/project_backfill_worker_test.go`（钉子升级：调用点+常量双向）、
   `bg/project_backfill_worker_realdb_test.go`（计划断言门，strings import）、
   `internal/summarystore/store.go`（v1FallbackCountSQL 父表化 + 头注）、
   `internal/summarystore/s3_wave2_turn_count_sources_test.go`（钉子演进双向钉）、
   本报告。
3. **测试**：build/vet/gofmt rc=0；`CGO_ENABLED=0 go test ./bg/ ./internal/summarystore/`
   全量 ok（32s/0.5s）；变异 M1/M2/M3 三方向红在正确断言；252 生产计划双腿断言
   （tuned 零并行 / plain Gather Merge+2）人工完成——realdb 门在其环境中会因
   TEST_DATABASE_URL 缺省而 SKIP（既有门控形态），其首次真库运行可在下次
   scratch 库演练时补跑。
4. **风险**：①backfill 关并行后极端大窗（>48h 回调）单线程扫描变慢——48h 窗契约下
   ~1.7 万行 pkey probe 量级为几十 ms，可接受，且头注已记录动因；②v1 回退父表直读
   在「request_logs 分区索引缺失」的新环境会退化为顺序探（索引由 728 等迁移保证，
   EXPLAIN 实测 5 分区全部走 gw_session_id 索引）；③回退计数残余慢（1-5.6s 期）的
   精确机制（缓冲 vs 锁）未完全隔离——修复缩小 4× 接触面是严格改进，残余如实登记。
5. **handoff**：AGENTS.md 未动（工作区有并行会话 WIP：bg/node_probe.go、URSM lua 族、
   VERSION/version.json——本轮提交只含上述 6 个文件）；记忆
   pg-252-sql-log-audit-facts 增补本轮事实。
6. **下一轮提示词**：①DSM 频次复测（backfill 修复需随下次部署到 154/245 生效——本轮
   修复在 bg worker，252 本机无 llm_gateway 网关，真正受影响的是 154/245 的
   project_backfill tick）；②WITH_win 若复现，pg_stat_activity 抓现行；③latest_bucket
   状态表设计专项（§六.1）；④aggregator 真实参数形态取证；⑤is_final_success max
   918s 锁取证。
