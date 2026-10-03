# 252 PG SQL 日志审计第二十三轮（2026-10-04）—— capability 修复部署闭环（源头归因反转）+ session_health_worker 全扫根修（迁移 822）+ 双线 main 分叉对账 + R22 五项遗留复核

以 round22 §八 + round21 §十二为起点（起点重算：本轮开局时本地 main=5461c1676（含 ursm redis 修复，**未推送**），origin/main=316ac33a9（含并行线 46 提交），**两线在 213699cee 分叉**——见 §四.3）。取证窗 = **2026-10-03 06:03 → 10-04 06:02**（ctr.log-20261004 归档 [10-03 03:08→10-04 03:15] + 现行 ctr.log 快照 [03:15→06:02]，与 R22 窗口 [→06:03] 首尾相接）。所有时间戳为 252 CST；快照 06:02，本机/252 时差 <1min（06:01 双端对时）。本轮由每日 06:00 审计 automation（automation-f0a3bf63，第 18 次）触发。

---

## §零、传输层巡检：copytruncate 物化洞再量化 + 归档未压缩观察

- 昨日归档 ctr.log-20261004：表观 = du = **963,012,664 字节**（完全物化，无稀疏性）；NUL 洞剥除后全窗真实内容 **252MB**（两文件合计）——单归档 ~700MB 纯洞字节。R22 §零的处方（podman 原生 `--log-opt max-size`）仍未采纳，运维轨登记维持。
- 反常观察：`/etc/logrotate.d/podman-pg-252` 配置含 `compress`（无 delaycompress），但 03:15 轮转产生的归档 3h 后仍未压缩（963MB 明文）。轮转→压缩链路存在未闭环环节，移交运维轨复核（不排除 gzip 失败被 logrotate 吞掉）。
- 现行 ctr.log：表观 972MB / 真实 9.3MB（2.8h，≈0.92KB/s ≈ 4× 基线，白天膨胀工具+审计会话负载）。

---

## §一、错误家族全景（头锚定 `CST [pid] ERROR:` 二段计数）

| # | 家族 | 计数/24h | 节拍 | 归因 | 处置 |
|---|---|---|---|---|---|
| 1 | `operator does not exist: timestamp with time zone < interval` | **×27,510** | 恒定 1,800/h（精确 30/min、2s 节拍）06:03→**21:21:55 戛止**，22h 起零 | acc_db 域服务（`UPDATE acc_candidate_saga_steps … claimed_at < $1 - $2::interval`，非本仓，全仓 grep 零命中）——**外部已修复/停跑，风暴持续 15.3h 后消失** | R22 催办升级可销案；末次时间戳 21:21:55 已可供对方定位 |
| 2 | `invalid input syntax for type json`（`\x7b22` hex） | **×4,999**（61-402/h 全天不断） | 呈 30min tick 突发簇（2s 节拍爆发数分钟后静默） | **R22 已根修、未部署**：capability_backfill persistRow `[]byte` hex。**本轮源头归因反转：全部来自 154/245**（252-dev 的二进制是 10-01 cc0e977f，早于 10-02 ece68f148 的 worker 接线——它从未跑过该 worker，且实例钥不匹配、凭据全解不开，连探测都发不出） | **本轮闭环**（§四.2/3） |
| 3 | `canceling statement due to user request` | ×2,933 | 白天 100-200/h，夜间趋零 | 应用侧 context 取消（probe cycle、`WITH used` 路由查询等已知容忍族） | 计数登记 |
| 4 | `connection to client lost` | ×1,237 | 06h 204/h → 21h 后个位数，与 #1 衰减曲线吻合 | acc 风暴的长查询拖死客户端连接的伴生信号 | 随 #1 销案 |
| 5 | `canceling statement due to statement timeout` | ×520 | 白天 ~30/h、夜间 2-12/h | probe cycle 目标查询 + project_backfill 已知族（R8 老病维持登记） | 维持 |
| 6 | `inconsistent types deduced for parameter $4` | ×288 | **恒定 12/h**（24h 节奏纹丝不动） | E6 opencode-pocket scheduled_tasks | 催办维持（§五.3） |
| 7 | `duplicate key value violates unique constraint` | ×101 | 06h 集中 | `orchestration_dispatches`/`orchestration_tasks` ——非本仓表（全仓零命中），共享实例外部服务 | 不计入本库对账 |
| 8 | `terminating background worker "parallel worker" due to admin` | ×140 | 白天 | REINDEX CONCURRENTLY/analyze 的并行 worker 树被取消——索引膨胀工具轮自噪音 | 对账剔除 |
| 9 | `deadlock detected` | ×1 | — | R22 的 8 例 MAAS DDL 环未再复发（MAAS 对象已落库） | 已收敛 |
| 10 | 单发长尾 | ~100 | — | 共享实例其他库（memora/redclaw/backends 等）+ 并行会话探针自噪音（`aggregate functions are not allowed in GROUP BY` ×13 = 有人对 ursm_node_snapshot_min 手写 `GROUP BY 1` 含聚合的侦察查询；`probe_reclaim`/`g34-a` uuid 等 = 其他审计会话样本） | 逐条剔除 |

二段计数：本库（llm_gateway）真阳性 = 家族 2（修复中）+ 家族 5/3 的本库子集；家族 1/6/7 为外部库。

---

## §二、慢 SQL 全景（≥1s 共 24,351 条 / 86,310s 语句时 / 24h）

方法增补：在 R22 纪律 56（duration 行+下一非空行配对）之上，本轮补齐**同行语句提取**——R22 的「空语句体」桶实际混入了全部**单行语句**（REINDEX/ANALYZE 一行式、psql 单行查询），配对法对它们取不到后续行。本轮把同行余文一并归族，慢查全景首次完整：

| 家族 | n | sum | max/avg | 判定 |
|---|---|---|---|---|
| **路由刷新 SELECT**（autoroute `refreshIndexSQL`，`WITH latest_bucket`） | ×7,052 | 18,420s | 22.2s / 2.6s | **P2 维持**（R8 基线 61ms；R22 提案待产品拍板；11-02 day-2 DROP 自然缓解） |
| **路由刷新写臂**：`INSERT INTO credential_model_index_hot`（单行） | **×2,486** | **7,174s** | 29.8s / **2.89s** | **P2 证据扩充（新）**：R22 只测到读臂；写臂同病（每次刷新重写候选行），两臂合计 **25,594s ≈ 全库语句时 30%**——7d 回看提案的收益比 R22 估计更大 |
| analyze tick（`analyze_llm_gateway_table_stats('2')`，单行） | ×75 | 9,924s | 475.4s / 132s | D7' 已知（巨 payload 行宽采样贵），未破 10min SET LOCAL 线；R22 后进一步走高的部分与白天 REINDEX 的 IO 争用同窗 |
| stats rollup（dim_minute 族） | ×3,186 | 6,642s | p50 1.5s / p95 4.4s | 常态节拍；max 73.8s 那条是膨胀轮的 `pgstatindex('request_stats_dim_minute_pkey')` 侦察（自噪音，剔除后家族无异常） |
| probe_cycle 目标查询 | ×811 | 3,152s | 9.9s / 3.9s | R8 v_routable_credential_models 双视图 join 老病（R21 §九④），timeout ×520 同源，白天集中 |
| REFRESH MATERIALIZED VIEW（单行） | ×279 | 1,412s | 110.1s / 5.06s | R12-F1 收敛后常态（avg 秒级；110s 单条在 180s pin 线内） |
| **session_health_worker sweep**（`… FROM session_summaries WHERE last_request_at < now()-'1h' AND health_score IS NULL ORDER BY … LIMIT 100`） | ×28 | 211s | 29.4s / 7.5s | **本轮根修（§四.1，迁移 822）**：R22 §五.7「`SELECT session_key, tenant_id` timeout ×10 待归因」即此形状，本轮闭合 |
| cmb_update（探针 revert/mnf_cooling 两个扫描 UPDATE） | ×763 | 1,415s | 14.8s / 1.9s | **非计划病**：EXPLAIN 均为廉价计划（1,935 行表 bitmap/seq scan），耗时来自白天膨胀轮 REINDEX 的锁/IO 争用（夜间同查询归零）；不改代码，随膨胀轮收工自然消失 |
| ursm 快照聚合（`date_trunc('hour', snapshot_ts)`） | ×23 | 263s | 59.6s | 夜间也出现（20:25/01:50），独立于膨胀轮；URSM_SNAPSHOT_RETENTION_DAYS 下调提议（R16）仍待拍板，settings_kv 无键=用默认值 |
| REINDEX INDEX CONCURRENTLY（单行） | ×6 | 1,187s | 565.9s | 索引膨胀工具轮工作窗（06:13-18:19），自噪音；`idx_route_incident_events_incident_created`/`idx_session_summaries_cost` 等重建 |
| promote 族（单行 `SELECT promote_*`） | ~130 | — | — | 正常 tick 节拍 |
| `-- ping`（探针基建，单行） | ×219 | — | — | probe cycle 常态 |

---

## §三、R22 遗留项逐项复核

| R22 登记 | 本轮复核结果 |
|---|---|
| §五.1 路由候选 7d 回看提案待拍板 | 维持待拍板；**证据扩充**：写臂 `INSERT INTO credential_model_index_hot` ×2,486/7,174s（avg 2.89s），两臂合计占语句时 30% |
| §五.2 MAAS DDL 部署纪律 | 死锁 8 例→1 例（未复发），MAAS 对象落库完好；纪律建议维持 |
| §五.3 acc 催办升级 | **风暴已止**：末错 10-03 21:21:55（整 2s 节拍跑了 15.3h/27,510 条后消失）；`connection to client lost` 同步衰减。可销案，末次时间戳移交对方定位根因 |
| §五.4 E6 维持 | 288/24h = 12/h 恒定，节奏不变，催办维持 |
| §五.5 vacuum 脚本「明日 03:15 将第 N 次失败」 | **预言证实**：10-04 03:15:01 `VACUUM FULL columnar_internal.chunk_group FAILED`（VACUUM cannot run inside a transaction block），服务器侧坏版本仍在位（cron `/etc/cron.d/pg17` 周日 03:15），部署缺口移交索引膨胀工具轨（再次） |
| §五.6 ledger 测试债（本机数据态敏感） | 未复核（测试轨，非本库日志面），遗留维持 |
| §五.7 `session_key` timeout ×10 待归因 | **已归因并根修**：session_health_worker sweep（10-02 ece68f148 接线后才有），迁移 822 闭合；maas_credit_consumption_buckets ×1（24h）属预热观察，另有 252-dev 启动时 backfill 90 天超时一次（journal WARN，观察） |
| §八① R21 §十二收口采纳 | 已采纳：三根修 810/811/812 在 applied_at 后持续零复发（本轮全窗 row-too-big/overlap/CTID 生产命中 0） |
| §八② capability 部署复核 | **本轮执行**（§四.2/3） |
| §八④ session_key 归因 | 同 §五.7，闭合 |

---

## §四、本轮修复

### §四.1 迁移 822：session_summaries 健康分待评分部分索引

- **缺陷**：`bg/session_health_worker`（10-02 ece68f148 接入生产，每 60min/实例 tick、批 100 行）的捞取查询在 252 真库 EXPLAIN = **Parallel Seq Scan + Sort**（586,339 行全扫、394,710 行 health_score IS NULL），单次 25.8-29.4s。现有 20 个索引无一覆盖「health_score IS NULL + last_request_at 排序」（多为 tenant 前导列，本查询无 tenant 谓词）。
- **修复**：迁移 822 `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_health_pending ON session_summaries (last_request_at DESC) WHERE health_score IS NULL`（dbinit:no-transaction 标记）；行评分后即离开部分索引，体积随积压收敛。
- **五点同步**：startup 正典 + installer embeddata/main.go/runner.go + apply-db-revision-sequence.sh + installed_startup_migrations.tsv（217 行）。
- **验证**：本机真库（正典通道 apply-db-revision-sequence.sh）CREATE INDEX 成功、EXPLAIN Parallel Seq Scan → **Index Scan**；252 生产预应用同效（indisvalid=t + 计划切换），台账 schema_migrations 记 822（252 现状：max=819，820/821 待网关下次启动随链应用——非缺口，登记）；installer stats_migrations 双向对账测试 PASS。

### §四.2 capability 修复部署——源头归因反转

R22 修复（35a35b063）压在 main 上一天未部署，json_hex 以 ~208/h（5× R22 速率）持续。本轮部署前的归因链：

1. 154/245 在 10-04 01:04-03:07 被并行线重新部署（build_seq 2440/2441，git_sha **ed827caa**，双机二进制 sha 一致）——但 json_hex 在 03:07 后**继续**（06:08/06:40 仍在爆）。
2. 252-dev 自 10-01 05:19 未动：其二进制（cc0e977f）**早于** capability worker 的接线提交（10-02 ece68f148）——252-dev 从未跑过该 worker；且 252-dev 部署脚本生成实例专属加密钥、共享库凭据全解不开（journal 实证 node probe 全部 `decrypt: unknown format`），连探测都发不出，persistRow 无从触发。
3. pg_stat_activity 毫秒级抓捕失败（0/5 次采样命中，语句执行窗 <10ms），但上述二进位证据已足够：**json_hex 全部来自 154/245，ed827caa 不含 35a35b063**（ed827caa 不在 origin/main，属并行线本地分支，其分叉早于 R22 修复落点）。
4. **252-dev 先行换新**（deploy-252-gateway.sh 正典通道）：cc0e977f-2373 → **5461c1676**（含 capability+ursm 修复），VERIFY_COMMIT=5461c1676、healthz ok、systemd active；dev 实例 decrypt_failed 为脚本契约内预期。部署后 252-dev journal 零 capability 持久化错误（对照其修复前不可能产生的差异）。

### §四.3 双线 main 分叉对账 + 生产部署

本地 main（4 个未推送提交：2 个 ursm redis 修复 + 2 文档）与 origin/main（并行线 46 提交：session-migration/RLS/silentcatch 测试套件 + §9.13x 文档）在 213699cee 分叉，且**并行线今晨把不含 capability 修复的 ed827caa 部上了生产**。处置：

1. 两次合并（6082b5275：46 入站提交，唯一冲突=双方各自新增 §9.124 的审计文档，取并集并留双占号标记；25a864398：追平并行线午间续推的 §9.152），合并后 `go build ./...` 干净、bg capability 测试 + sessionv2mirror + installer 对账测试全绿。
2. 推送 origin/main = **25a864398**（union：capability+ursm 修复 ∥ 并行线全部工作 + 迁移 822）。
3. **245 预生产部署**（deploy-245.sh → deploy-seamless 正典编排）：✅ **PASS**——old=2440-ed827caa → new=**2442-25a86439**（active_port=8782，总 101s/切换 11s）；门禁全绿：DB 就绪 1s、admin 密码同步+登录 200、**凭据解密冒烟 providers=18/creds=16/failed=0**。切换前迁移步骤顺带把 252 schema_migrations 水位从 819 推进到 **821**（820=06:50:36、821=06:50:46），与预应用的 822 汇合——252 台账现已齐至 822。
4. **154 生产晋级**（同编排）：✅ **PASS**——old=2441-ed827caa → new=**2443-25a86439**（active_port=8782，总 122s/切换 19s）；门禁同上全绿（解密冒烟 16 creds failed=0、healthz ready:true）。
   - **披露（A7）**：154 的构建窗口（06:52-54）与并行会话在制编辑（bg/metrics.go 06:52:13、bg/partition_manager.go 06:54:12）重叠，二进制经 strings 验证**裹入了其未提交的 D8-d 只读扫描**（`checkDefaultPartitionResidue` + `llm_gateway_partition_default_residue_rows` 指标，§9.152/§9.153 的残留行探测）。该代码为加法式只读（boot+tick 各扫一次 default 分区残留），全部门禁通过、154 journal 零 residue/panic 报错；245 构建早于其编辑时间（06:47-51），未裹入。后续以并行线的正式提交为准对齐，本报告如实记面试图。
5. **部署后判据实测**：`credential_model_capabilities` **2 行 → 97 行、max(last_tested_at)=2026-10-04 06:55:53**（worker 一天半以来首次成功写回，修复端到端生效）；json_hex 末错 **06:40:13**（154/245 修复前最后一簇），此后静默。

---

## §五、登记与移交清单

1. **[P1·已闭环待 R23.5 终验]** capability 修复生产部署完成（§四.3）：三台网关均携修复（154=2443/245=2442 均 25a86439，252-dev=5461c1676）；capability 表 2→97 行实证生效。R24 首查：json_hex 全窗归零 + capability 行数持续增长（612 种子为满水位参考）。
2. **[P2·提案待拍板·证据加重]** 路由候选 7d 回看（两臂合计占语句时 30%）；11-02 day-2 tick DROP credential_model_index_2026_09 自然缓解节点不变。
3. **[外部·催办维持]** E6 pocket $4 12/h 恒定。
4. **[外部·销案]** acc 风暴 10-03 21:21:55 止（15.3h/27,510 条），末次时间戳移交；conn lost 伴生信号同步消失。
5. **[运维轨·再移交]** ① vacuum-bloat 服务器侧坏版本 03:15 第 N 次失败实录（§三）；② logrotate copytruncate 物化洞 +「compress 配置在但归档 3h 未压缩」反常（§零）；③ 252 schema_migrations 水位 819 < 仓库链 822（820/821/822 将随下次网关启动迁移链应用，启动前重启生产网关即可带上）。
6. **[观察]** ursm 快照聚合夜间 59.6s/44.6s（独立于膨胀轮）；cmb_update 白天锁争用（膨胀轮收工后应消失，R24 复核）；并行线新增每小时 URSM 快照健康巡检 cron（10-04 01:11 上线，其工作面勿动）。
7. **[测试债维持]** ledger 真库用例数据态敏感（R22 §五.6）。

---

## §六、新纪律候选

- **57**：慢查「空语句体」桶必须再分离**单行语句**（同行余文）与多行语句——R22 纪律 56 的配对法把单行语句全部漏进空头（本轮 1,312 条空体实为 REINDEX/analyze/INSERT 一行式，其中藏着写臂 ×2,486 的新病）。配对法 + 同行提取两级都要做。
- **58**：错误家族的实例归因不能只看「谁重启了」——必须核对**二进制是否包含引入/修复该行为的提交**（本轮 154/245 凌晨重启曾造成「已修复」假象：06:08/06:40 继续爆错才暴露 ed827caa 缺修复；而 252-dev 因「worker 接线晚于其构建日期 + 凭据解不开」两条独立理由排除嫌疑）。
- **59**：本地 main 与 origin/main 分叉期间**不得从任一单侧部署生产**——先对账合并（union）再部署，否则必然丢另一线的修复（本轮 ed827caa 部署即缺capability 修复的活例）。

## §七、自审计

- A1：acc「已修复」结论只基于日志消失（21:21:55 后零命中）——外部进程停止与修复不可区分，措辞用「风暴已止」而非「已修复」，末次时间戳供对方自查。
- A2：json_hex 源头归因经历一次反转：先按「252-dev 未重启」假设它为主要源头并部署之；部署后 06:40 仍在爆 → 倒查 252-dev 二进制日期（10-01）早于 worker 接线（10-02），**252-dev 根本没有该 worker**——此前一轮的 ×761 计数也必须全部改记到 154/245 头上。反转的触发器是「部署后预期信号未出现」，不是新证据的被动出现。
- A3：pg_stat_activity 抓捕 5 次全空后**没有**把它写成「无法归因」，而是改用二进制考古（构建日期 vs 接线提交）闭环——毫秒级语句活捉失败不等于归因失败。
- A4：合并冲突唯一落点是审计文档双方各自新增 §9.124——保留两节并留双占号标记交文档属主重编号，没有擅自替并行线改号。
- A5：252 预应用 822 后发现台账漏记，补 `schema_migrations` 行——对齐 252 现有记账形态（version text + description），未照搬本机新形态（content_sha256 列 252 还没有）。
- A6：全程未触碰并行会在制对象（膨胀轮 REINDEX、并行线 URSM 巡检 cron、session-migration 工作面）；生产部署严格走 245→154 晋级门禁，未绕过预生产。
- A7：154 构建窗口与并行线 WIP 编辑重叠的事故是**主动 strings 验证**发现的（不假设「工作区=已提交状态」）；验证后做三件事——运行期 journal 复核（零报错）、245 构建时间线甄别（未裹入）、文档如实披露（§四.3.4），不以「门禁全绿」掩盖部署内容与提交标签的偏差。
- A8：json_hex 归零判定只用「修复前最后一簇的 30min 相位窗」作边界（06:40:13 后静默），在 154 首个 worker 相位（~07:10）过后再做最终确认，不用「暂时没出现」充数。

## §八、下一轮提示词（建议）

> 以本文 + R22 §八为起点。优先级：① capability 部署终验（json_hex 应已归零、credential_model_capabilities 落行、max(last_tested_at) 前进；若 245/154 部署有回滚，按 §五.1 处置）；② 822 部署后 session_health_worker 慢查应归零（worker 下一 tick 起索引扫描），顺带核对 252 schema_migrations 水位是否被启动链推进到 822；③ 路由 7d 提案拍板跟踪（两臂 30% 证据在手）；④ cmb_update 是否随膨胀轮收工消失；⑤ ursm 快照聚合夜间慢查归因；⑥ E6 催办维持。纪律沿用 ⑪-56 + 57-59。

## §九、产物与物证

- 代码/迁移：sql/migrations/startup/822_session_summaries_health_pending_index.{sql,down.sql} + installer 五点同步（提交 29723508e）；合并提交 6082b5275/25a864398（origin/main=25a864398）；252-dev 部署 5461c1676（VERIFY_COMMIT 证据在 deploy 日志）。
- 日志物证：252:/tmp/pg252-r23/（ctr-snap-r23.log、ctr.log-20261004-snap-r23.log、errors_r23.tsv 5,000+ 行、slow_r23.tsv 2.4 万行、summary_r23.json、r23_extract.py）。
- 关键实测值：acc 末错 21:21:55；json_hex ×4,999 且 06:40 仍活跃（部署前基线）；EXPLAIN 前后 Parallel Seq Scan → Index Scan（本机+252 双库）；822 台账 applied_at=06:35:20；vacuum-bloat 03:15:01 FAILED ×3 实录；归档表观=du=963,012,664B/真实内容 252MB。
- 本报告：docs/audit/2026-10-04-252-sql-log-audit-round23.md。
