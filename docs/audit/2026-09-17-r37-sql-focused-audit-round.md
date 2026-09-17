# R37 — SQL 全量专项审计轮（2026-09-17）

- 性质：用户指定专项（"对项目中所有的sql进行审计，优化性能解决风险"），非 48h 滚动窗口轮。全仓 SQL 面（Go 内嵌查询 / sql/objects DDL / 迁移通道 / plpgsql）按性能+风险双轴横扫
- 起点（重算）：R36 §四遗留为底本；起点核对时发现并行会话已推进 main（R35/R35-gap/R36 三轮 + 717 列型对齐迁移），先行快进合并；**收尾时并行会话再推 19 提交（R36 补位轮：probemode 单一事实源/716 down+全文守卫/717 首版重写/冗余清理批），按点位对账合并**——采其 probemode 机制与 716 down 全文守卫版，撤并本轮重叠的 Reviver main 侧门控/守卫字面切源/716 down 手补三处；其 717 重写缺视图依赖处理（本轮真库实证的硬阻断），717 采本轮版本
- 方法：主代理 + 6 路只读子代理并行（A:R36遗留核验 / B:admin查询 / C:运行时热路径 / D:DDL对象 / E:迁移安全 / F:动态SQL注入横扫），每条 P0/P1 线索经主代理亲读复核后才动手；关键性能结论一律本机真库 EXPLAIN/pg_index 实证
- 与并行轮对账：R36 已闭环 R34 遗留 #2（717 迁移落地但**未在存量库执行过**，本轮真库首跑连抓三个缺陷，见 §一 P1-1）/ #3 / #4 / #6；R34 #1 credential_state_log 维持 R35 结案

## 一、本批修复

### P1

| # | 发现 | 证据 | 处置 |
|---|------|------|------|
| P1-1 | **fix_sql 二阶存储型 SQL 注入**：billing_mismatch 检查把 `provider_models.raw_model_name`（上游 /models 目录可影响）与 plan_type 裸拼进 fix_sql（`fmt.Sprintf("…'%s'…'%s'…")`），落库 routing_health_checks 后 `ExecuteFix` 直接 `db.Exec(fix_sql 自由文本)`——管理员点"一键修复"即执行被污染 SQL（错误信息还回显 SQL 全文） | bg/routing_health_checks.go:74 + admin/health_check_handlers.go:130-140（子代理 F1 线索，主代理亲读全链确认） | 执行端改 `cannedFix(entityType)` 分发参数化罐头语句（entity_id 为唯一实参；billing_mismatch 修复时联表重读 CHECK 约束保护的 plan_type；canonical_id_null 罐头保留 693 解绑守卫），fix_sql 降级为展示文本 + `pgQuoteLiteral` 转义；无罐头的 entity_type 一律 400 不执行。钉桩：pgxmock 严格期望证明注入 payload 不可达执行面（admin/health_check_fix_test.go ×4 + bg 形状钉桩 ×1） |
| P1-2 | **探测权威源切换未完段（R36 遗留#3/#5 收口）**：①BrokenProbeReviver 无门控每 30min 把冻结旧表 broken_confirmed 翻回 recovering——同时击穿凭据恢复守卫+清空路由排除；②credential_recovery / credentialhealth.checker 两处"全模型 broken 才阻止"守卫仍读冻结 model_probe_state，新系统证实坏死的模型以冻结 healthy 形态漏过守卫随凭据翻回 ready 入池；③路由候选排除 NOT EXISTS broken_confirmed 同样直读死表；④defaultAsyncExitSuspicious 新模式下照写死表 | bg/broken_probe_reviver.go:67-72、cmd/gateway/main.go:3731、bg/credential_recovery.go:594-612、credentialhealth/checker.go:636-677、provider/client.go:1586/2368（Lane A 亲核的是**修复前状态**） | **同点位对账**：本轮开工后并行 R36 补位轮已独立修复全部四点（internal/probemode 单一事实源：worker 内 no-op 门控 + GuardStateTable 模式感知选表 + brokenPairExcludeSQL helper + Enabled() 早退）——首轮文档曾误记"checker/client 为补位轮漏面"，自审计核对 origin/main 后更正：**四点全部为其覆盖，本轮该点位贡献 = 发现（Lane A 取证）+ 合并对账 + 四包源形状钉桩改钉 probemode 机制**（后台字段：其实现已含各自行为测试） |

### P2

| # | 发现 | 处置 |
|---|------|------|
| P2-1 | **717 迁移真库首跑即崩（三个缺陷，R36 只登记未实跑）**：①`customer_id ~ '正则'` 无条件假定 text——本机该列已对齐为 bigint，42883 中止序列；②protocol_conversion 的 `IN ('true',…)` 同病；③request_logs_hot 列被 4 个静态视图依赖（with_current_month 族 + routing_analytics_source），ALTER TYPE 42883 "cannot alter type of a column used by a view" | 717 重写为单 DO 块：pg_get_viewdef 捕获 4 视图现体 → DROP → 按 information_schema 逐列守卫 ALTER（仅漂移列执行）→ 按捕获体重建。单语句原子，对漂移库（252/baseline）与部分对齐库（本机）行为一致可重入。**本机实测：9 项列型漂移 → 0**，视图全部重建有效 |
| P2-2 | **716 down 半回滚**（R36 遗留#4 半边）：DROP 7 对象仅重建 3，v_model_priority_details / v_model_availability_timeline / get_model_state_summary 回滚后 42P01，probe-health 三端点 500 | 补齐三体（逐字取自 313d1ebc8^ 的 db.go 内嵌 SQL）；与 R36 补位轮对账采其版本（另含 ensure↔迁移全文等价守卫，为本轮所无）；installer embeddata 副本同步 |
| P2-3 | **全库冗余索引约 45 对（写放大 + vacuum 拖累）**：子代理 D 报"129 对"，主代理本机 pg_index 逐对按函数等价口径复核收敛为 45（唯一约束影蔽 / 同构重复对 / 方向影蔽 / partial 影蔽三档），**排除** 8 个 db.go ensure 创建者（drop 会被复活，登记遗留）与叶子分区 attached 归属不明的局部对 | 迁移 **718_drop_redundant_indexes_and_add_ttl_indexes**：45 个函数等价冗余索引 DROP（分区父表 plain、其余 CONCURRENTLY），含 718 首跑后对账补漏的 3 个。**本机实测：索引总数 2276→2227（自审计时点值；活库 ensure/分区轮转会自然增删，非稳定终值）**。自审计修正：B2 段原有 `idx_request_logs_parent_ts` drop **已撤回**——该名由 db.go ensure（013 镜像）持有，与 V369/202609_01 通道的 idx_request_logs_parent_request_id 构成所有权冲突对（本机两父索引各挂 5 个 attached 叶子），drop 被复活且扰动 ATTACH 拓扑，归入遗留 ensure 根治范畴 |
| P2-4 | **三表缺索引致 Seq Scan 热路径**（EXPLAIN 实证）：①session_aggregate_outbox claim 查询（OR 双分支+ORDER BY）在 36.5 万 done 行存量上每 tick 全表扫；②request_envelope / sticky_sessions 零二级索引，cleaner 的 expires_at DELETE 全表扫 | 718 同批补 3 索引：`idx_session_aggregate_outbox_claimable (next_retry_at) WHERE status IN ('pending','claimed')`（EXPLAIN 实证 Index Scan 命中）+ 两表 `expires_at` btree；reaper 加 done 行 7d 保留期增量清理（每 tick 一批 LIMIT 5000，事务内同款 super_admin GUC；钉桩防退化回无界 DELETE） |
| P2-5 | **db/migrations/ 孤儿通道撞号异文件**：014/352-365 与 startup 正典同号异内容（353_goal_loop_detection ≠ 正典 353_request_logs_bodies_hot_independence 等），无执行器接线但 test-phase3-integration.sh 直接 apply 014 | 目录加 README 冻结护栏（禁止新增/禁止手工执行/正典通道指引）；清理列遗留 |

### P3

- admin/logs 列表与 top-models 两端点 from/to 无跨度上界（from=1970 → 分区母表全表 COUNT/SUM）：`clampQueryWindow` 钳 366d（与 usage 面口径一致），钉桩
- session_aggregate_outbox reaper tick 循环 idle 早退改 break 以便尾随 trim（行为等价增强）
- db/migrations 目录 README（见 P2-5）

## 二、核实为健康的关键面（子代理亲读/真库对账 + 主代理抽核）

- **动态 SQL 注入面整体干净**（Lane F 全横扫）：77 处 fmt.Sprintf+SQL 命中逐条判定——~60 处纯 `$%d` 占位、~8 处 int 型 LIMIT/OFFSET、其余内部常量；ORDER BY 用户输入 3 处全部白名单闸（allowedSort/ValidateOrderByColumn/switch 枚举）；plpgsql 26 处 EXECUTE 全部 %I/%L；分区名正则白名单+quoteIdent；freeresource 租户 GUC 白名单转义。唯 P1-1 一处漏网（已修）
- **admin 查询面**：ORDER BY 闸覆盖良好；热点聚合（heatmap/usage/usage_enhanced）均有强制时间窗与跨度上限；真 N+1 未发现（turns enrich 批量 ANY($1)）；无 WHERE 的 UPDATE/DELETE 未发现
- **迁移通道主干**：函数 clobber 守卫（intentional_function_chains）设计良好；sequence 通道 autocommit+幂等；installer 五点同步 714-718 逐字节一致；down 抽查（除 716 已修）语义清晰；危险操作扫描未见无界 DELETE/TRUNCATE
- **DDLL 函数/触发器/视图**：0 个 SECURITY DEFINER（无提权面）；热写入表零触发器；无 >2 层视图链；无视图内裸 SELECT *
- **运行时热路径**（Lane C 健康面）：session v2 热写 advisory lock 全 64 位无桶撞、ON CONFLICT 全 COALESCE 单调守卫、outbox claim/requeue/markDead 守卫在位、stats_event_inbox claim 命中 partial 索引、dispatch 主链路零同步 SQL

## 三、测试与验证（真实实测）

```
go build ./...                                                        # clean
go vet ./admin/ ./bg/ ./provider/ ./credentialhealth/ ./cmd/gateway/ ./domains/session/v2/  # clean
go test ./admin/ -count=1                                             # ok 358s（含 4 个新钉桩）
go test ./bg/ ./provider/ ./credentialhealth/ ./cmd/gateway/ ./domains/session/v2/ -count=1  # 全绿
go test ./bg/ -race -count=1                                          # （见 §五 追加）
gofmt -l（本轮触碰 18 文件）                                           # 清零
installer 模块 go build ./... + go test ./cmd/llm-gw-installer/ ./internal/dbinit/ 全绿
bash scripts/apply-db-revision-sequence_test.sh                       # passed（718 登记）
具名钉桩（红绿）：
  TestExecuteFix_BillingMismatchRunsOnlyCannedStatement   PASS（pgxmock 严格期望：注入 payload 不可达执行）
  TestExecuteFix_UnsupportedEntityTypeRejectedWithoutExec PASS
  TestCannedFix_CanonicalIDNullKeepsAdminUnbindGuard      PASS
  TestBillingMismatchFixSQLEscapesRawModelName            PASS
  TestCredentialRecoveryGuardReadsCompatView / TestRecoverExpiredGuardReadsCompatView / TestRoutingExclusionReadsCompatView / TestBrokenProbeReviverGatedByUseNewProbeMode / TestAsyncExitSuspiciousDBGatedByLegacyMode / TestLegacyProbeModeSemantics  PASS
  TestOutboxDoneTrimIsBatchedAndBounded                   PASS
  TestClampQueryWindowCapsSpan                            PASS
真库实测（本机 llm_gateway@127.0.0.1:5432）：
  apply-db-revision-sequence.sh 全序列跑通（717+718 落账）
  717：hot↔母表列型漂移 9 → 0；4 个依赖视图重建且 SELECT 冒烟通过
  718：45 个 drop 全部落库；claim EXPLAIN 由 Seq Scan → Index Scan using idx_session_aggregate_outbox_claimable（时点复核）
  718 自审计（本节追加）：索引总数时点读数 2276→2227（活库 ensure/分区轮转自然增删）；46 名被 drop 索引复活扫描发现 idx_request_logs_parent_ts 被 db.go ensure 复活 → 718 B2 撤回该行（所有权冲突对，见 P2-3），其余 45 名无一复活
  outbox trimDoneRows 真库实证：scratch 库 + TestReaper_TrimsOldDoneRowsOnly（-tags integration，8d done 删 / 1h done 留 / 8d pending 留）PASS——此前仅有源码形状钉桩，本用例补齐真实执行面
```

## 四、遗留登记（本轮新登记，均经亲读确认）

1. **P2 | RLS owner 绕过是架构级缺口**：全库 510 表 owner=llm_gateway（应用连接角色）而仅 9 表 FORCE ROW LEVEL SECURITY——~40 张租户隔离表的 RLS 对应用自身空操作；attachments 更是 policy 存在但 RLS 未 ENABLE（策略 inert）。**不可盲修**：单加 ENABLE 对 owner 连接是 no-op；FORCE 需全部查询路径先设 app.current_tenant GUC（现仅 freeresource/RLS helper 等少数路径）。需产品级设计：应用改非 owner 角色 + 按表分批 FORCE + 维护面 bypassrls 角色。另:声明式 objects/ 与活库 policy/ENABLE 清单有漂移（本机 383 policies vs 声明 122 文件）
2. **P2 | ensure 创建的 8 个约束影蔽冗余索引**（applications/center_commands/licenses/offline_activation_requests/releases/instance_status_reports/keyless_providers/free_resource_catalog）：须改 db.go ensure 删除冗余 CREATE 才能根治（718 drop 会被复活），同批可把 tools_usage_stats_hot 三对 ASC/DESC 近重复一并定夺（混合排序语义需确认）。**自审计追加同族项**：request_logs 父表 `idx_request_logs_parent_ts`（db.go:1220 ensure + 013）↔ `idx_request_logs_parent_request_id`（V369/202609_01 通道）为**跨通道所有权冲突对**（本机两父索引各挂 5 个 attached 叶子，ATTACH 脑裂态），根治须先统一通道归属再清理，不可单侧 drop
3. **P2 | 713 ALTER TYPE 全分区重写的生产前置**：session_turns numeric(12,6)→(14,8) 级联全部月分区 ACCESS EXCLUSIVE，本机 3 万行秒级但 252 体量未验证——252 执行前须查分区行数/尺寸并低峰
4. **P2 | V 系列交付通道缺口复发面**：deploy/sql/migrations 36 文件仅 V371 接进 sequence 通道；V355 回填"分批"是假的（DO 块不可 COMMIT）；新 V 文件必须按 695-705 定式登记
5. **P3 批**：708 回填单事务注释纠偏 + DROP sessions.last_full_* 三列的生产前 NULL 审计前置；717 down 未审视；request_logs 月度叶子局部重复索引清理（attached 归属需逐叶确认，父表 drop 已级联清一份）；request_logs.client_model 五重叠索引收敛（btree/hash/lower/text_pattern/gin trgm 各有用途面，删减需查询面确认）；api_key_model_cost 触发器链死亡确认（函数在、无挂载，需查 Go 直写后删或挂）；15 个可空列 FK 语义确认；approval_requests 死序列清理；0731 audit-log ILIKE 无索引；data-lifecycle stats 全表 COUNT 改 pg_class 估算；turns/sessions 默认无窗 + 4 路 ILIKE 全扫；routing decisions since_minutes 封顶；audit-log 解析失败静默丢弃
6. **运维事实 | 本机 PG 对 DROP INDEX CONCURRENTLY 不稳定**：本轮两次 postmaster 崩溃入恢复（约 60-90s 自愈），均为 CONCURRENTLY 索引操作触发；本机索引操作建议 plain DROP + 低峰。252 上 718 建议维持 CONCURRENTLY（标准形态）但安排可回退窗口

## 五、自审计轮（用户指令"逐条核查、勿以声明为完成"，2026-09-17 收尾批）

对首轮全部声称逐条实证复核，**三处发现并修正**：

| # | 声称 | 实证结果 | 修正 |
|---|------|---------|------|
| 1 | "718 被 drop 索引名全部消失" | **不成立**：46 名复活扫描发现 `idx_request_logs_parent_ts` 被 db.go:1220 ensure 复活（B2 追加段未重跑创建者核查，违反本轮自定的排除规则）；进一步取证发现该名与 V369/202609_01 的 idx_request_logs_parent_request_id 是跨通道所有权冲突对（两父索引各挂 5 attached 叶子），此前 plain drop 还引发一轮叶子索引重建 churn | 718 B2 撤回该行（注释载明原因），embeddata 重同步；归入遗留 #2 ensure 根治范畴；其余 45 名复扫无一复活 |
| 2 | "credentialhealth/checker 切源为本轮贡献（补位轮漏面）" | **失实**：补位轮 checker.go 已用 `probemode.GuardStateTable()` 覆盖（当时用字面量 grep 计数误判） | P1-2 行更正：该点位四点全为补位轮实现，本轮贡献=Lane A 取证+对账+钉桩改钉其机制 |
| 3 | "outbox trimDoneRows 已验证" | **不完整**：此前仅有源码形状钉桩，真实执行面零覆盖 | 新增 `TestReaper_TrimsOldDoneRowsOnly` 真集成测试（-tags integration + TEST_AUDIT_ISOLATED_DB_URL → scratch 库），实跑 PASS（8d done 删/1h done 留/8d pending 留） |

**复核为实的声称**（抽样重验）：fix_sql 全仓执行面扫描无第二执行点、web 前端零依赖；合并后 ./db/ 包 + 迁移守卫测试补跑全绿（首轮漏跑，属门禁覆盖不全）；gofmt 16 触碰文件重核清零；installer 五点同步 717/718 cmp 逐字节一致。

## 六、测试与验证（追加）

- go test ./bg/ -race -count=1：ok（后台批完成）
- go test ./db/ ./sql/migrations/startup/ -count=1：ok（自审计补跑，含补位轮 716 全文等价守卫 + baseline 列型守卫）
- go test -tags integration ./domains/session/v2/ -run TestReaper_TrimsOldDoneRowsOnly：PASS（scratch 库真库实证，用后即删）

## 七、下一轮提示词（建议）

> 以 docs/audit/2026-09-17-r37-sql-focused-audit-round.md §四 遗留为起点：首位 **RLS owner 绕过架构设计**（#1，应用角色拆分 + FORCE 分批 + bypassrls 维护角色，动代码前先盘点全部查询路径的 GUC 设置面）；其次 ensure 冗余索引根治（#2，db.go ensure 删 CREATE + 三份 baseline 对齐，**注意 idx_request_logs_parent_ts ↔ idx_request_logs_parent_request_id 是跨通道所有权冲突对，须先统一归属再清理，本机两父索引各挂 5 个 attached 叶子**）与 **252 部署前置**（R36 遗留#2 + 本轮 #3：717 已真库实证可跑，252 执行前仍须 information_schema 复核 + 713/718 低峰评估）。新增迁移前 git fetch 核对远端编号（当前已至 718，五点同步 + apply-db-revision-sequence_test.sh 双门禁必跑）。**本轮教训入 playbook：新迁移必须至少在一个存量真库实跑后再定稿（717 即例）；追加进迁移文件的每一行都要重新过创建者核查（718 B2 parent_ts 即反例）；对并行轮的"覆盖面"断言必须以 origin/main 代码为准而非字面量 grep（checker 误判即例）**。审计入口：docs/audit/playbook/orchestrator-prompt.md（下一轮 R38）。
