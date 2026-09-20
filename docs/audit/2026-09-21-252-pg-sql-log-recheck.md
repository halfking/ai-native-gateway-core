# 252 PG SQL 日志审计复核轮（2026-09-21）

> 审计对象：pg-252-pg17（115.29.212.252 podman，PG 17.10 + Citus 13.3）实例级 SQL 日志与 pg_stat_statements（stats_reset 2026-09-11，覆盖 10 天）。
> 本轮 = R46 §六#2 的既定复核：用 R46 改版后的新日志策略（log_statement=none + 慢查询 1s + 错误全录，窗口 ~6.3h）打捞慢 SQL 与错误 SQL 并修复。
> 范围含同集群其他库（26 库共享实例），跨库发现登记不越界修复。
> 上游输入：docs/audit/2026-09-20-252-pg-sql-log-audit.md（R46）；日志快照 /tmp/pg17_snap_0921.log（本机+252 各一份，13MB/24.7 万行，**不入库**）。

## §〇、取证底座

1. 日志策略在位复核 ✓：log_statement=none、log_min_duration_statement=1s、log_min_error_statement=error（容器 Up 3 天，auto.conf 未丢）。实测写入速率远低于 R46 估计：6.3h 窗口仅 13MB（≈580B/s），**100MB 轮转窗口实际 >10 天**，取证压力大幅缓解。
2. 快照窗口 2026-09-20 23:46 → 09-21 06:03 CST：慢查询（>1s）2,913 条 / 104 指纹；ERROR 2,699 条（去重后 10 指纹）；FATAL 1,202 条。
3. 日志前缀无库名（%m [%p]），跨库归属按 SQL 内容辨认（smm daily_kline、pocket scheduled_tasks 可识别）。
4. **角色级配置实锤**：`llm_gateway` 角色 rolconfig = `statement_timeout=30s + idle_in_transaction_session_timeout=60s` —— 生产一切"贴 30s 线被杀"的真正来源（R46 只写了"贴线"，本轮找到设定位置）。超级用户会话不受限（本轮审计查询 statement_timeout=0）。
5. **网关连接全量 SimpleProtocol 实锤**：db/db.go:65 `DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol` —— 所有参数被客户端内联为无型别字面量后整句发送（生产日志 STATEMENT 中可见值已内联）。任何 `$n + INTERVAL 'lit'` 形态都是生产炸弹（见 F1）。

## §一、发现与处置

| # | 级别 | 发现 | 处置 | 证据 |
|---|------|------|------|------|
| F1 | **P1** | **feature_stats 特征分布聚合在生产每轮必炸**：`ts < $1 + INTERVAL '1 day'` 在 SimpleProtocol 下 $1 被内联为无型别字面量，PG17 把 `'…' + INTERVAL` 解析为 **interval+interval**（本机 PG17 复现实锤），报 `invalid input syntax for type interval: "2026-09-20 00:00:00Z"`。快照窗口 14 次（≈每 27min 一轮全败），worker 首特征 detected_language 即失败→整批分布聚合中断，**feature_distribution_stats/dedup_stats 自 R43 部署起新日期断更** | `bg/feature_stats_worker.go` 三处（144/183/191）改 `ts < $1::timestamptz + INTERVAL '1 day'`——显式 cast 两种协议模式均收敛 timestamptz+interval（本机验证两种写法语义）；全仓扫描确认无其他 `$n + INTERVAL` 形态 | 生产 STMT 原文（值内联）；本机 PG17 字面量语义复现；R43 diff（fde958650）定位引入点 |
| F2 | **P1** | **routing_analytics_7d REFRESH ≈50% 周期被角色级 30s 击杀**：matview 单次物化 ~17M 行、均值 6.2s、重周期 >30s；6.3h 窗口 38 次 timeout / 35 次慢完成。陈旧度契约（15min）破洞，admin 端点退化基础视图重查询。routing_audit_summary_7d 同型（8 timeout / 60 慢完成）。客户端 ctx 本就 5min（RefreshTimeout），卡点纯在角色级 statement_timeout=30s | `bg/materialized_view_refresher.go` refreshView 重构：两条路径统一**固定连接** + `SET statement_timeout='180s'` → REFRESH → defer `RESET statement_timeout`（WithoutCancel 保证归还池时干净）；advisory-lock 路径语义不变（本来就要钉连接） | 角色 rolconfig；快照 timeout 归因（38/35）；refresher 代码 ctx=5min vs 语句=30s 的错配 |
| F3 | **P2** | **request_logs 分区侧缺 credential+model 表达式索引（hot/冷双侧不对齐，R46 F2 同款"父表索引不级联存量分区"病）**：provider-model 抽屉轮询查询 hot 分支走 `idx_request_logs_hot_credential_model_ts`，但父表+全部存量月分区无任何 credential_id 索引，窗口跨过 promote 边界即 Parallel Seq Scan。**pg_stat_statements 10 天：184,620 次 / 均值 168ms / max 13.5s / 累计 8.6h＝全库总耗时第一名**；慢日志 6.3h 窗 717 次（sum 1207s）；72h 窗 EXPLAIN ANALYZE 实测 105s。同索引顺带覆盖按 credential_id+ts 过滤当月分区的候选失败关联查询两族（n=119 均值 1.8s / n=82 均值 2.1s） | **迁移 728**（startup，727 三段式同款）：逐存量分区 \gexec CONCURRENTLY 建 `(credential_id, (lower(COALESCE(outbound_model, client_model))), ts DESC)` + 父表 ONLY 壳 + 幂等 ATTACH 守卫；hot 表索引属 sql/objects 通道不重复建；revision-sequence 通道登记。**252 存量真库实跑：4 分区叶+父壳 5 索引全 valid**；EXPLAIN A/B：分区分支 Parallel Seq Scan(cost 101449)→**Index Scan(cost 35)**，总 cost 160385→70659，实测 >30s（105s 峰）→暖缓存 10.9s | 728 实跑输出；EXPLAIN 前后对比；pss/慢日志三源交叉 |
| F4 | **P2** | **self_check_runs 写入 23514 违反 CHECK**：`self_check_runs_error_type_check` 停在 09-03 的旧版 http_* 清单（缺 `concurrent` 等 canonical errorsx 类别），运行时二进制写新类别→每条自检失败记录被拒。644 的 §C DO 块幂等守卫本可重建，但 revision-sequence 台账按文件名记账（644=2026-09-03 已账），**文件内容后续加固无重放通道**——台账 stuck | 252 真库手工重放 644 §C DO 块（幂等守卫判定非 canonical→重建，NOT VALID 不阻存量行）→ CHECK 已含全量 canonical 清单。**登记机制债**：内容型加固依赖同名文件重放时，apply 脚本需内容指纹或版本后缀 | DETAIL Failing row（error_type='concurrent'）；重放前后 pg_get_constraintdef 对比；schema_migrations 台账记录 |
| F5 | **P1(ops)** | **R46 F1（super_admin GUC current_role 保留字）修复已合入 main 未部署**：`syntax error at or near "current_role" at character 15` 快照窗口 **755 次**（≈2 次/min，ApprovalTimeoutWorker 60s 节奏，R41 起从未成功）。审批超时清扫/跨租户审批读取在生产持续全败 | 代码修复已在 main（2aff71420 set_config 形态）；**行动项=252 网关二进制重新部署**（随维护窗口执行，本审计轮不代执行）。部署后 gateway 日志 "approval timeout sweep failed" 应绝迹 | 快照 ERROR 原文 755 次；main 已含修复的代码比对 |
| F6 | P2（跨库，登记不修） | pocket 库 `scheduled_tasks UPDATE $4 inconsistent types deduced` 75 次/6.3h（R46 F5 原样持续，≈288 次/天必失败） | 维持移交 opencode-pocket | STMT 归属指纹不变 |
| F7 | P3（外部脚本，移交） | `SELECT ?, count(*) FROM model_capability_profiles UNION ALL … model_capability_profile_audit … model_substitution_overrides` 00:02 单次报 42P01 表不存在——三表属 domain 迁移 342，252 未应用；**全仓零 Go 引用**→探针来自仓库外巡检脚本 | 登记移交：脚本 owner 需容错缺表，或 252 随 R48 部署补 342 | to_regclass 双 NULL；grep 全仓无引用 |
| F8 | P3 登记 | **会话写链 user-cancel 族**（锁排队代价，非扫描慢）：MAX(turn_no) 取号 181 次 cancel + advisory lock 111 次（pss 1.48M 次调用均值 13ms、max 3.8s）+ session_turns_hot/session_bodies INSERT ~74 次。同族 gw_task_id 查询 pss 均值 1.1s×7,880 次——纯 EXPLAIN cost 仅 58（索引齐备），生产慢=锁等待非计划劣化 | 维持现状登记（S4 停写前 claim 路径整改时一并看，R46 F8 顺延） | cancel 归因表；gw_task_id EXPLAIN（cost 57.94） |
| F9 | P3 登记 | **路由候选查询 user-cancel 650 次/6.3h＝最大单一 cancel 源**（≈2.5k 次/天，WITH policy…routable CTE）；request-path 深因属 R47 §五#1 评分热路径专项 | 顺延该专项 | cancel 归因第一名 |
| F10 | P3 登记 | 其余慢/超时榜：provider_error_agg_src 临时表 pss 2,706 次/均值 5.67s/累计 4.26h + 17 次 30s 击杀（candidate_failure_logs_unified columnar 按 aggregation_id 增量扫描，columnar 行取回天然慢）；supplier_error_stats WITH base 1.69h；credential_model_index_hot DELETE+INSERT 周期 3.17h+2.74h（by-design 刷新环）；claim UPDATE is_final_success pss 80,234+38,526 次/均值 90-167ms/max 4.9s + 307 cancel；session_aggregate_outbox 轮询 40ms×416,859 次=4.62h；auto_route_selections_all 聚合 137 次/均值 8.7s（R48 新查询）；`could not attach to dynamic shared area` ×6（并行 worker DSM，观察项）；parallel worker FATAL 177（REFRESH/大查询取消的连带）；smm 库口令认证失败 76 次（跨系统） | 各自带数据登记，专项化另行立项 | pss 总耗时榜（§后附）+ 慢日志/timeout 归因 |

## §二、R46 §六#2 交付复核（727 回落判定）

| 项 | R46 前 | 本轮证据 | 判定 |
|---|---|---|---|
| stage_events 保留清理 DELETE | 532 批/9 天，均值 10.5s，撞 30s | 慢日志 6.3h 窗 **0 条**（>1s 全消失）；pss 累计均值 9.8s 为 09-11 起含修复前 8 天的混合值 | **已回落 ✓** |
| 会话存在性 EXISTS（session_turns 分支） | P50 687ms / max 29.8s，反复取消 | pss 快路径条目 50,870 次/均值 **1.3ms**；次条目 60,830 次/54.8ms；cancel 归因仅 21 次 | **已回落 ✓** |
| cancel 风暴总量 | 估 ~7k/天 | 1,682 次/6.3h ≈ 6.4k/天——总量未降但**构成已换血**：原 F2/F3 两源退出榜面（EXISTS 21、stage_events 0），新榜首=路由候选 650/claim 307/会话写链 ~370 | 构成性回落 ✓，总量由 F8/F9 接管 |

## §三、改动清单

1. `bg/feature_stats_worker.go`：三处 `$1 + INTERVAL '1 day'` → `$1::timestamptz + INTERVAL '1 day'`（SimpleProtocol 字面量内联根修）+ 注释载明根因。
2. `bg/materialized_view_refresher.go`：mvRefreshStatementTimeout=180s 常量 + refreshView 统一钉连接/SET→REFRESH→RESET（WithoutCancel 收尾）；两条锁路径语义不变。
3. `sql/migrations/startup/728_sql_audit_request_logs_credential_model_index.sql`（+down）：request_logs credential+model 表达式索引三段式。
4. `scripts/apply-db-revision-sequence.sh`：728 登记。
5. 252 生产已实改（同 726/727"实跑后随二进制 no-op"路径）：**728 五索引全 valid**；**644 §C 重放→canonical CHECK**。
6. 本轮未动 252 网关二进制（F5 的部署属维护窗口动作，见 §五）。

## §四、测试

- `go build ./...` 过；`go vet ./bg/` 净；`go test ./bg/ -count=1` 全绿（7.2s，含 feature_stats/refresher 既有钉桩）。
- `bash scripts/apply-db-revision-sequence_test.sh` 契约 PASS（728 登记后）。
- 252 真库：728 实跑（5 CREATE INDEX + DO 全成，indisvalid 6/6 含既有 hot）；EXPLAIN A/B（101449→35 / 160385→70659）；实测抽屉查询 >30s→10.9s（暖缓存）；644 §C 重放后 constraintdef 含全量 canonical 清单。
- 本机 PG17：`'…Z' + INTERVAL '1 day'` 报 invalid input syntax（interval+interval 误解析复现）vs `'…Z'::timestamptz + INTERVAL` 正常——F1 根因与修法语义双验证。
- 明确未覆盖：F1/F2 的生产行为验证需二进制部署后观察（feature_stats 当日聚合产出恢复、REFRESH timeout 归零）；F3 全量收益看下轮 pss 增量与慢日志回落。

## §五、风险

1. **F5 部署行为翻转面**（R46 §四#1 原样有效）：252 二进制一更新，current_role 修复、F1/F2 随包生效；审批超时清扫将首次真正清算存量 pending（approval_queue 状态迁移量首日观察，量大受 30s 批约束需手动分批）。
2. F2 抬升 REFRESH 语句上限到 180s：重周期 REFRESH 占锁时间变长（advisory/redis 双锁已防叠加）；若单次 REFRESH 仍 >RefreshInterval，需回到 F7 增量物化专项（R46 F7 顺延）。
3. 728 索引维护成本：request_logs 月分区写入路径（INSERT 密集）每分区多一份索引写放大（与 hot 侧既有同构索引对齐，属"该有的索引"，非新增冗余）；252 实测建索引期间写入无阻塞（CONCURRENTLY）。
4. F4 的 NOT VALID CHECK：存量旧行未回验（by design）；若后续做 VALIDATE 需先清洗 http_* 旧值。
5. 审计侧自伤记录：本轮一次 EXPLAIN ANALYZE（72h 窗）在 252 实跑 105s 只读查询（superuser 会话无 statement_timeout）——审计查询今后一律先纯 EXPLAIN、执行必带超时上限。

## §六、handoff 更新

- 记忆库：llm-gateway-go-audit-cycle-progress 追加本轮；pg-252-sql-log-audit-facts 增补（角色级 30s、SimpleProtocol $n+INTERVAL 炸弹、644 台账 stuck、728 索引对齐、慢日志新基线写入速率）。
- 原始物证：/tmp/pg17_snap_0921.log（252 与本机各一份）+ 本机 /tmp/top_slow_sql.txt、/tmp/parse2_out.txt——均不入库（沿用 R46 先例）。

## §七、下一轮提示词（建议）

> 以本文 §五 + R47 §五 + R46 §五顺延为起点，审计入口 docs/audit/playbook/orchestrator-prompt.md。优先级：
> 1) **部署复核**：252 网关二进制随维护窗口更新后，验证 current_role 错误绝迹、feature_stats 聚合恢复产出（feature_distribution_stats 当日行出现）、REFRESH timeout 归零、728 索引被 pss 增量确认命中（抽屉查询均值 168ms 应显著回落）；
> 2) **session_turns 分支长窗专项（729 候选）**：抽屉查询 72h 窗实测残余 10.9s 在视图 session_turns 分支（158k 行/72h 堆过滤，视图冻结不可改投影）；候选=对 session_turns 分区建与视图投影逐字一致的表达式索引 `(CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END, lower(model), ts DESC)`，727 三段式同款，先 EXPLAIN A/B 定案；
> 3) **provider_error_agg_src 专项**（F10）：columnar 按 aggregation_id 增量扫描 4.26h/10 天 + 17 次 30s 击杀，评估 ts 下界联合过滤或 hot 表预聚合；
> 4) R47 §五 全单顺延（评分热路径/白名单清偿/存储演进二批/pricing_plans EXPLAIN）。
> 纪律沿用 R46 §六⑥⑦ + 本轮新增：⑧ 审计执行类查询必须带 statement_timeout 上限（§五#5）；⑨ 内容型迁移加固重放需内容指纹/版本后缀（F4 机制债，随下轮脚本改造收口）。
