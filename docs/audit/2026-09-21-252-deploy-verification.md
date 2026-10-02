# 252 部署验证与 SQL 专项轮（2026-09-21）

> 轮次性质：去编号专项轮（R51/R52 均已被并行轨道的已提交文档预留——due-diligence-tracker / phase4-tx-ordering-rec 的 canonical-name 与 tx-ordering 方向；沿用 recheck 轮撞号去编号先例）。
> 上游输入：docs/audit/2026-09-21-252-pg-sql-log-recheck.md §五+§七 + R47 §五顺延。
> 本轮四项主轴：① 252 网关二进制部署后四项验证；② session_turns 分支长窗专项（729）；③ provider_error_agg_src columnar 扫描专项；④ 644 型台账重放机制改造（纪律⑨收口）。

## §〇、起点与部署事实

1. **起点重算**：HEAD = origin/main = ef694cdfd（fetch 后快进核对，工作区仅 3 个未跟踪 docs 目录）。
2. **部署已发生（本轮开局即核实）**：llm.kxpms.cn /healthz = `2.5.6-a91c0dfe-20260921-2161`；154 主机 systemd `llm-gateway-go-canary@8781` 于 **2026-09-21 10:44:30 CST** 启动（ActiveEnterTimestamp），旧槽 8782 于 10:45:42 被杀（蓝绿轮转残留 failed 状态，by design）。a91c0dfe 包含 recheck 轮 F1（::timestamptz）/F2（REFRESH 钉连接 180s）/F5（set_config 根修）全部修复。
3. 252 PG 访问：ssh 252（root@115.29.212.252:25022）→ podman exec pg-252-pg17。网关生产在 154（47.97.111.154，netbird 172.16.2.209），pg_stat_activity 客户端 241/209/210 三台。

## §一、四项部署验证（全部 PASS）

| # | 验证项 | 结论 | 证据 |
|---|---|---|---|
| V1 | current_role 错误绝迹 | **PASS** | 切流后 gateway 日志 `approval timeout sweep failed` **0 次**（切流前同日志 6 次，末次 2026-09-20T09:59:54，均报 `syntax error at or near "current_role"`；旧节奏 ≈2 次/min）；252 PG 日志切流后 `current_role` 命中 **0** |
| V2 | feature_stats 聚合恢复产出 | **PASS（口径修正见 §八 A5）** | 新二进制首轮 10:45:11 即成功：`dedup_stats` 落 `stat_date=2026-09-21` 行（created_at 10:45:11.529）；11:45:11 第二周期同样 `feature stats computed`；PG 日志切流后 **0 ERROR**（此前每轮必报 `invalid input syntax for type interval`）。`feature_distribution_stats` 无 09-21 行是空集正确语义：数据源 auto_route_selections 当日 UTC 窗 0 行（INSERT..SELECT 空集不落行） |
| V3 | REFRESH timeout 归零 | **PASS** | 切流后 ~1.3h（≥5 个 15min REFRESH 周期）PG 日志 `statement timeout / canceling statement` **0 条**（recheck 轮口径：38 次/6.3h） |
| V4 | 728 索引 pss 命中 | **PASS** | `pg_stat_user_indexes`：`request_logs_2026_09_credential_model_ts` idx_scan **18,439**（15 分钟窗口 +315 ≈ 21 次/min，即抽屉轮询真实命中）；hot 侧同构索引 15.69M。pss 累计均值（163ms/5.9ms/113ms 三形态混合 09-11 起全程）无法干净分离前后窗口，均值回落留待下轮 pss 增量（§八 A2） |

**侧观察（登记不动）**：auto_route_selections 自 09-09 起从 ~11k 行/天塌到近零（09-15:9、09-16/17:120、09-18:3、09-20:8、09-21:0）——月级旧态（生产 auto-route 流量切走），非本轮部署回归；连带效果是 feature_distribution_stats 在无 auto-route 流量的日子不再产行（V2 口径修正的根因）。

## §二、专项：session_turns 分支长窗（迁移 729）

**问题**（recheck §七#2）：728 收口后抽屉查询残余 10.9s（72h 窗）全部落在视图 session_turns 分支——2026_09 分支 cost 60650 / 全查询 71260（85%）：裸 ts 范围扫（`session_turns_2026_09_tenant_id_ts_idx`）+ 逐行 CASE-credential 过滤。视图冻结不可改投影（R37 定案）。

**§七原案否决（真库实测推翻）**：原案三列表达式索引 `(CASE credential, lower(model), ts DESC)` 的中列**不可能被使用**——查询谓词是 `lower(COALESCE(t.model, d.client_model))`（跨 JOIN 的 COALESCE），与 `lower(model)` 不匹配；而中列存在反而挡住第三列 ts 的边界使用。本机 1.2GB 2026_09 分区 A/B 实测：三列版 543ms（bitmap 全前缀扫 + heap 过滤）；两列版 409ms 且被 planner 优先选择。

**定案（迁移 729）**：`(CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END, ts DESC)` 两列——等值前缀 + ts 范围双边界 + ts DESC 保序，与视图投影逐字一致（planner 可复用），727 三段式同款（逐存量分区 CONCURRENTLY + 父表 ONLY 壳 + 幂等 ATTACH 守卫），session_turns_hot 独立表单独建（727 同款）。

**真库 A/B 结果**：

| 环境 | 前 | 后 |
|---|---|---|
| 252 生产（EXPLAIN ANALYZE 72h 窗，credential 35） | 10.9s（暖）/ 105s（冷峰，recheck 轮实测） | **247ms（暖）** / 2.7s（冷，首触新索引）；计划切至 `Parallel Index Scan using session_turns_2026_09_credential_ts`，分支 cost 60650→839，总 cost 71260→7515（-89%） |
| 本机存量库（47.6 万行 2026_09 分区，正例 glm-5.3） | ~10s（cost 78732） | **401ms**，计划走 `ab_b`（后删）→ 729 正式索引同形态 |

残余 200ms 为 detail-JOIN 回表（2.3 万次 × ~8μs，受 COALESCE 谓词不可索引伺服所限），可接受。

**252 实跑事故与教训**：以 `-U llm_gateway` 手工跑 729 时，**角色级 rolconfig statement_timeout=30s 击杀了 09 大分区的 CONCURRENTLY 构建**（07/08 小分区已过），留下 INVALID 残留——若不清理，`IF NOT EXISTS` 会跳过重建成、ATTACH 挂上无效索引。处置：DROP 残留 → `-U postgres` 重跑全文件（7 索引全 valid）。教训入册：**存量库手工应用含 CONCURRENTLY 的迁移必须 superuser 会话（或先核算时长 vs rolconfig 30s）；CONCURRENTLY 被超时击杀必查 pg_index.indisvalid 残留**。

## §三、专项：provider_error_agg_src columnar 扫描（代码根修）

**问题**（recheck F10）：`stageSourceRowsSQL`（pass one 暂存）`FROM candidate_failure_logs_unified WHERE aggregation_id > $1`——视图把水位列合成为 `COALESCE(aggregation_id, -id)`，planner 无法据此剪枝，**每个 tick 对全部 11 个 columnar 月分区做 Seq Scan**（252 EXPLAIN：2026_08 cost 8969 + 2026_09 cost 10713，均 2.5 万行量级）；pss 10 天 2,706 次 / 均值 5.67s / 累计 4.26h + **17 次 30s 击杀**（击杀本身让 tick 失败、聚合中断——现状设计在自己的扫描成本下已经失守）。

**根修（数学论证）**：水位增量行只可能出现在 hot 表——月分区行全部经 promote 进入（live retention 默认 **8h**，688 对齐 Go 调度器；非 628 的 24h），被 promote 时其 aggregation_id 已被健康 tick（10min 节奏）的水位越过。故 pass one 改 `FROM candidate_failure_logs_hot`，与原形态逐字同列（temp 表列形不变，`NULL::text AS source` 占位保留）。pass two（整桶重读，audit F-4 精确性）**仍读 unified 视图**：其 ts 范围谓词可剪枝，语义不变。

**逃逸守卫**：唯一行逃逸通路 = 水位停摆超过 promote retention（停摆期间 promote 走了的行 id > 水位且不在 hot）。新增 stall guard：tick 读 state 行时一并取 `updated_at`（只在水位前进时刷新），停摆 > **7h**（retention 8h − 1h 余量）打 ERROR（可告警），把静默漏算变成显式告警——不回退为全分区扫。

**交付面**：`bg/provider_error_aggregator.go`（SQL + 守卫 + 注释载明论证）；契约测试钉升级（unified 视图引用数 2→1 + `FROM candidate_failure_logs_hot c` 来源钉——防回退）；集成测试 `TestProviderErrorAggregatorRealPG` 在干净 scratch 库（`cfl_behavior_test`，pg_dump 最小 schema 配方见 §九）端到端 PASS（真实 tick：staging→聚合→upsert→水位前进 0.01s，error_groups=4 断言过）。

## §四、专项：644 型台账重放机制（纪律⑨收口）

**机制债**（recheck F4）：`gateway_db_revision_sequences` 按 `sequence:basename` 记账，已应用文件的内容加固（幂等守卫重建、canonical 清单扩充）永无重放通道。

**改造（scripts/apply-db-revision-sequence.sh）**：
1. 台账加 `content_sha256` 列（`ALTER TABLE ... ADD COLUMN IF NOT EXISTS`，向后兼容）；此后每次应用记录当前文件内容 sha256。
2. **自动重放**：台账已记指纹且与文件当前 sha 不同 → 内容在应用后被改过 → 重放（通道契约"每个迁移幂等、重跑安全"兜底）并更新指纹。
3. **legacy 重放清单** `legacy_content_replays`（`basename|sha256` 格式）：指纹通道上线前的遗留记账（sha NULL）且登记在册 → 按当前内容重放一次后转入常规指纹管理。首条登记：**644**（sha `5dc5731f…`，文件自带 definition-aware 守卫，重放对已修复库是幂等 no-op）。
4. **契约测试自清洁三重**：条目格式（`basename|64hex`）／重放目标必须在 files=() 通道内／指纹必须与文件当前内容一致（文件再改动而条目未同步 → 门禁红并直接打印正确 sha——sqlreadguard 白名单同款）。
5. 本机真库实跑：644 遗留重放触发（`already canonical, skipping` 幂等 NOTICE 实证）→ 729 应用 → 台账 95 行（2 行含指纹）。**252 侧未跑全量脚本**：252 台账仅 48 个 marker，全量跑会真应用 ~47 个未登记文件、不可控——留待下次受控 schema 窗口（§八 A1、§九）。

## §五、改动清单

1. `sql/migrations/startup/729_sql_audit_session_turns_credential_ts_index.sql`（+down）：session_turns 分区三段式 `(CASE credential, ts DESC)` 表达式索引 + session_turns_hot 独立索引。
2. `scripts/apply-db-revision-sequence.sh`：729 登记 + content_sha256 台账列 + sha256_file 助手 + 重放决策循环 + legacy_content_replays 清单（644 首条）。
3. `scripts/apply-db-revision-sequence_test.sh`：重放清单三重自清洁断言。
4. `bg/provider_error_aggregator.go`：stageSourceRowsSQL 改 hot-only（注释载明数学论证与 252 证据）+ stall guard（providerErrorAggregatorStallWarnAfter=7h，updated_at 差值）。
5. `bg/provider_error_aggregator_contract_test.go`：视图引用钉 2→1 + hot 来源钉。
6. `admin/credential_monitor.go`：抽屉查询提取为 `slidingWindowQuery()`（防测试-生产 SQL 漂移，featureDistributionQuery 同款）。
7. `admin/sliding_window_realdb_test.go`（新）：抽屉查询 SimpleProtocol 真库语义执行 + 729 计划断言（索引缺席时降级 skip）。
8. 252 生产已实改：729 七索引全 valid（superuser 重跑后）；644 §C CHECK 已于 recheck 轮手工重放（本轮清单登记后，下次脚本跑齐时自动幂等补账）。

## §六、测试

```
go build ./... / go vet ./...                    # 全绿
go test ./bg/ ./admin/ -count=1                  # bg 7.3s / admin 66.0s 全绿
go test -race ./bg/ -count=1                     # 14.3s 全绿
bash scripts/apply-db-revision-sequence_test.sh  # PASS（含新增三重断言）
TEST_DATABASE_URL go test ./bg/ -run RealDB      # F1/F2 真库回归 PASS（既有）
TEST_DATABASE_URL go test ./admin/ -run TestCredentialDrawerWindow_RealDB   # PASS（新，含计划断言）
TEST_PG_CONTRACTS_ISOLATED=1 -tags integration TestProviderErrorAggregatorRealPG  # PASS（新，cfl_behavior_test 端到端）
252 真库：729 七索引 valid；EXPLAIN A/B（60650→839 / 71260→7515）；ANALYZE 10.9s→247ms（暖）
本机真库：729 幂等应用（脚本通道）；EXPLAIN 78732→10616→401ms
纪律⑩ 全仓扫描（范围=全部 Go 源码+sql/ 全目录，命令=grep -rn -E '\$[0-9]+ *\+ *INTERVAL'）：生产面 0 命中；唯一命中为 sql_audit_realdb_test.go 注释内引用形态，非可执行 SQL
```

明确未覆盖：① V1-V4 的长周期复核（本轮窗口仅切流后 ~1.3h；pss 均值回落、provider_error_agg_src 的 5.67s 消失需下轮 pss 增量）；② 252 台账 48→95 补账（受控窗口动作）；③ stall guard 的真实停摆场景无演练（逻辑 review + 契约钉）；④ fresh-DB 全新库路径仍属推理（729 父壳 + PARTITION OF 继承，与 727/728 同款约束）。

## §七、风险

1. **729 索引写放大**：session_turns 月分区 INSERT 路径每分区多一份索引（本机实测 09 分区索引 14MB/1.2GB 表 ≈1.2%）；与 727 effective_session 索引并存，planner 按谓词各取所需（真库验证无互扰）。
2. **252 台账欠账 47 文件**：下次在 252 跑 apply 脚本将真应用全部未登记文件（均幂等，但需受控窗口 + 预期 `content replay (legacy, registered)` 与 `applying` 混合输出）；在那之前 644 的 sha 重放与 729 的 marker 登记在 252 不生效（729 对象已手工建，无功能缺口）。
3. **stall guard 阈值与 retention 耦合**：7h 常量假设 live retention 8h（688 默认）；若 ops 调大 retention，guard 会偏噪（误报方向，安全侧）；调小则漏报窗口收窄方向仍安全（守卫注释已载明，未做成 env 可配——避免为低频告警加配置面）。
4. **hot-only staging 的行为面**：正常态与原实现逐行等价（增量行恒在 hot）；停摆 >8h 时漏算由 guard 告警兜底——这是"每 tick 5.67s + 17 次击杀"换来的取舍，击杀本身就是更频繁的聚合中断。
5. REFRESH 180s 抬升后重周期占锁更长（recheck §五#2 顺延，双锁防叠加已在位，观察项不变）。

## §八、自审计（同日批判式复核）

| # | 初版问题（性质） | 复核结论与处置 |
|---|---|---|
| A1 | **252 台账 48 markers 事实初版未查**——差点直接在 252 跑全量 apply 脚本"顺便交付" | 亲查后发现全量跑 = 真应用 ~47 个未登记文件、不可控；改为 252 只手工实跑 729 单文件（727/728 先例），补账列入下一轮受控窗口（§九#1） |
| A2 | **"728 pss 均值应显著回落"初版直接引用累计均值当证据** | 累计 pss（stats_reset 09-11 起）混合修复前后，不能证明；改用 pg_stat_user_indexes.idx_scan 计数（18,439 且 15min +315）为命中主证，均值回落降级为下轮增量复核项 |
| A3 | **feature_distribution_stats "当日行恢复"表述失真**——初版照抄 recheck §七口径 | 实际：分布行受 auto_route_selections 断流（09-09 起，月级旧态）影响不会每日出现；本轮真证据 = dedup_stats 行恢复（聚合恒产行）+ 分布查询零错误 + 双周期 computed。已按此修正 §一 V2 |
| A4 | **729 首次 252 实跑被角色级 30s 击杀，留下 INVALID 索引**——若不复查，`IF NOT EXISTS` 会永久跳过重建、ATTACH 挂无效索引（新教训） | 亲查 indisvalid 发现残留 → DROP → superuser 重跑 → 7 索引全 valid 复核；教训入册（§二）与记忆库 |
| A5 | **§七原案三列索引未先质疑中列可用性** | 本机 A/B 实证 planner 行为后否决原案改两列——"先 EXPLAIN A/B 定案"纪律的价值实锤（原案若直接上生产，索引照样建、查询照样慢、还多一份写放大） |
| A6 | **llm_gateway_test 库被我 dump 灌入时被残存 citus event trigger 拦截**，留下孤儿 sequence | 已 DROP 还原；改用干净 scratch（cfl_behavior_test）并沉淀最小 schema 配方；event trigger 拦 DDL 现象登记（本机 llm_gateway_test 库债） |
| A7 | **stall guard 若 state 行不存在（首 tick）** | `lastAdvance.IsZero()` 守卫零值不误报；state 缺席视为零水位沿用既有语义 |

复核后维持原结论的项：V1-V4 证据链、hot-only 数学论证（promote 与 aggregator 同进程前提下的逃逸分析）、644 重放幂等性（NOTICE 实证）、729 计划形态双环境一致。

## §九、下一轮提示词（建议）

> 以本文 §七 + R47 §五顺延 + R50 §五为起点，审计入口 docs/audit/playbook/orchestrator-prompt.md。优先级：
> 1) **252 schema 受控补账窗口**：备份后跑 reformed apply 脚本（预期 ~47 个 `applying` + 644 `content replay (legacy, registered)` + 729 marker；之后 48→95 补齐）；窗口内顺带核 pss 增量：抽屉查询均值（168ms 基线）与 provider_error_agg_src（5.67s/2,706 次）回落幅度；
> 2) **R47 §五#1 评分热路径性能专项**（D01 方案：基准先行 → 权重启动期解析 → 批量快照共享）；
> 3) **DEBT(R47) 白名单清偿**（守卫自清洁，session_list.go / usage.go / session_online.go 前三优先）；
> 4) 本机 llm_gateway_test 库治理：残存 citus event trigger 拦 DDL（A6），重建或清理。
> 纪律沿用 recheck ⑧⑨⑩ + 本轮：⑪ PG 语义修复完成态=真库经驱动执行（admin/sliding_window_realdb_test.go 为 admin 面模板）；⑫ 存量库手工应用含 CONCURRENTLY 的迁移必须 superuser 会话 + 事后 indisvalid 复查（§二教训）；⑬ "恢复产出"类验证必须先核对数据源是否仍在产数据（A3：空集不落行是正确语义，不是故障）。
> 附：cfl_behavior_test scratch 库配方——pg_dump --schema-only -t candidate_failure_logs_hot -t provider_error_aggregator_state -t candidate_failure_logs_hot_aggregation_id_seq -t provider_error_details -t candidate_failure_logs -t candidate_failure_logs_unified + get_current_tenant() 函数定义，灌入新建 `*_test` 后缀空库 + INSERT state 单例行。

## §十、handoff 更新

- 记忆库：llm-gateway-go-audit-cycle-progress 追加本轮；pg-252-sql-log-audit-facts 增补（角色级 30s 同击手工 CONCURRENTLY/INVALID 残留、729 定案、aggregator hot-only + 8h retention、252 台账 48 markers 欠账）。
- 原始物证：252 /tmp/729.sql；本机 /tmp/cfl_schema.sql、/tmp/ped_schema.sql、/tmp/cfl_parent_view.sql（scratch 配方，不入库）。
