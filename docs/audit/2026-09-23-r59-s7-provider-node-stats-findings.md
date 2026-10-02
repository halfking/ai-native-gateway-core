# R59 48h 审计轮 · S7 组发现报告（D08 供应商错误 / D09 节点状态 / D10 统计）

- 审计基点：main @ 9f7b0ea3f（本地 = origin/main，零未提交源码改动）
- 审计人：S7 子代理（只读 + 报告文件），2026-09-23
- 焦点 commits：9f3e89cae（survival 终态落库）、47980a138（740 view 链 client_ip）、1cd15c6a7（B7 三层接线）、2e6d55c11（ProbeSync/ProbeConfirm）、dccf93e51（maas 倍率 + B8 对账）、6827fce80（selfcheck 谓词 + promote 超时）

## 必跑项结果

| 项 | 结果 |
|---|---|
| `go build ./...` | ✅ exit 0 |
| `go test ./credentialhealth/...` | ✅ ok 0.677s |
| `go test ./admin/...` | ✅ ok（admin 67.5s + dashboardapi/dashboarddegrade/distlock 全 ok） |
| `go test ./db/ -run TestViewV2ProjectionContractSync` | ✅ PASS（离线契约钉桩） |
| `go test ./db/ -run TestRequestLogsViewV2EnsureMatchesMigration` | ⏭️ SKIP：需 `LLM_GATEWAY_TEST_PG_DSN`（live ensure↔迁移逐字节等价测试本机无 DB，记录跳过） |

---

## 一、发现清单

| # | 严重度 | 发现 | 证据（file:line） | 建议 |
|---|---|---|---|---|
| S7-1 | **P1** | **survival 终态 detail code 只接了 1/3 协议面**：`survivalTerminalError` 的模式匹配（落 `failure_detail_code=gateway_survival_<action>`）只存在于 `serveWithExecutor`（OpenAI /v1/chat/completions）。anthropic `/v1/messages`（MessagesHandler）与 `/v1/responses`（ResponsesHandler）各自直调 `runSurvivalCoordinator` 后走自己的通用错误块：对 `*survivalTerminalError` 的 `execErr.(*executors.ExecuteError)` 断言必失败 → errCode 恒为 `provider_error`，survival 决策只以自由文本（"request survival ended: …"）进 error_message，`gateway_survival_*` 结构化码丢失（9f3e89cae 修复5 声明未覆盖这两面）。不产生第二错误帧（`if usedSurvival { return }` 先于任何渲染），纯观测缺口 | domains/streaming/handler.go:4784-4816（唯一匹配点）；domains/streaming/messages.go:753,764-777；domains/streaming/responses.go:743,763-782；domains/streaming/survival_wiring.go:263-307 | messages.go/responses.go 错误块头部加同款 `if ste, ok := execErr.(*survivalTerminalError); ok` 分支（复用 detailCode()/failAndMark 语义）；补两协议的 request_logs 落库钉桩测试 |
| S7-2 | **P2** | **B7「内存/Redis 层」折叠读侧丢 client_ip**：`dimPieKey` 仍只有旧 `virtual_ip→virtual_ips`，缺 `client_ip→client_ips`。链路实证：`AddOnRequestLogPersisted(statsBoardCache.Record)`（cmd/gateway/main.go:4923）→ `FromTelemetryEntry` 产出 client_ip 维度（domains/stats/minute_entry.go:146）→ writer 落 Redis delta（domains/stats/boardcache/writer.go:67）→ `foldScope` 折叠时 `dimPieKey` 查不到 `client_ip` 直接 `continue` 丢弃（domains/stats/boardcache/fold.go:99-101、fields.go:73-81）。Redis 板缓存模式（生产默认）下 client_ips 饼图停在 baseline 快照，只在门控 rebuild（worker.go:50-61，QPS 空闲门控）时才刷新；分钟表路径（admin/dashboard_board_queries.go:208-213）与 fallback 路径正确。boardcache 测试零 client_ip 覆盖 | domains/stats/boardcache/fields.go:73-81；domains/stats/boardcache/fold.go:95-101；domains/stats/minute_entry.go:140-146；domains/stats/boardcache/writer.go:67；cmd/gateway/main.go:4918-4926 | dimPieKey 补 `"client_ip": "client_ips"`（virtual_ip 旧键可留作历史 delta 兼容）；补 boardcache fold 层 client_ip 折叠钉桩测试 |
| S7-3 | **P2** | **生产复验自动化未落仓库、工作区绑错仓库副本**：`automation-1c111234` 实体只存在于本机 `~/.zcode/v2/tasks-index.sqlite`（automation-1c111234-49b2-4680-ac0e-3c9f6078943a，cron `52 20 24,25 9 *`，enabled=1，run_count=0，prompt 全文在 DB prompt 列），仓库 scripts/tests 无任何落盘。可重放性=仅本机单副本，重装/换机即失。且 workspace_path 绑定 `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`（旧副本 @44823d798），非本轮基点 llm-gateway-go-2 @9f7b0ea3f——对纯 ssh 只读探测影响小，但其引用的 docs/design 判定口径在旧副本可能滞后。口径内容本身如实：R3 机制三件套（洪水线=0 + 每 resume_blocked 事件落库/retryable/单帧 + 同夜 A/B 方向一致）✓、R4 时间前缀过滤强制（严禁裸 grep -c，`{"time":"2026-09-24T` 前缀/journalctl --since）✓、R5 保留期判定线=日均 ≤20MB/h ✓、t0_arrived_at NULL→bodies.ts join ✓、R6 shadow 结构性局限判定口径 ✓、journald ~36h 保留期告示 ✓ | ~/.zcode/v2/tasks-index.sqlite automations 表（本机）；docs/audit/2026-09-23-154-double-frame-critical-audit.md:65；docs/design/resume-blocked-long-stream-recovery.md:168；仓库内无 `52 20 24,25`/1c111234 脚本 | 将 prompt 固化为 `scripts/production-reverify/` 下可执行脚本（bash + psql）入仓；或至少把 prompt 全文归档进 docs/audit；9/24 前把 automation workspace 改绑 llm-gateway-go-2 |
| S7-4 | **P3** | **D09「状态更新单模块」未达成**：写 credentials/node_probe_state/model_probe_state 的文件达 ~38 个、跨 ≥4 层（盘点见 §三）。收敛机制在位（ProbeSync/ProbeConfirm 双层信号量、node_probe_write_through 统一健康面恢复 SQL、manual/admin_protected/balance_floor 豁免谓词跨写点一致），但 executor 热路径仍 2 处直写 node_probe_state、admin ~10 文件直写状态列、bg 16+ worker 各自 UPDATE | domains/streaming/executors/executor.go:2731,2827；bg/node_probe.go（主写点，50 处引用）；admin/credential_monitor.go:842-903,1142-1203 等；domains/credential/writer.go:66-90 | 中期：把 admin 手工态与 bg 恢复态统一走 domains/credential Writer + bg/node_probe 两个入口；给 executor 直写点挂统一 writer 接口。本轮不阻塞 |
| S7-5 | **P3** | **maas SQL 估算与 Go 计费的既存残差（文档化，非缺陷）**：SQL 每档保留 `CEIL(base×disc)` 粒度档，Go 用浮点费率——非整有效费率时每桶可差 ≤1 credit。两侧注释已如实声明（dccf93e51 有意保留） | maas/credits.go:57-61；maas/credits_sql.go:14-22 | 维持现状；若未来要求估算=计费逐行相等，需 SQL 侧去 CEIL（会破坏 credit 整数粒度，不建议） |
| S7-6 | **P3** | live 契约测试（ensure↔710+734+738+740 逐字节等价 + down 链）依赖 `LLM_GATEWAY_TEST_PG_DSN`，本机无 DB 跳过——R58 硬约束「视图链改动必须同步自愈体+契约测试」的 live 臂本轮无法本地复跑（离线臂绿） | db/view_schema_v2_contract_test.go:199-202 | 在带 DB 的 252/154 环境补跑一次 live 契约测试作为 740 的部署前门 |

无 P0 发现。

---

## 二、D08 供应商错误落库与凭据详情呈现 · 清单现状盘点

结论：**三件套闭环在位**（写入 → 详情 API → UI → 服务质量），仅协议面 survival 码缺口见 S7-1。

### (a) 单独错误表 / 凭据维错误集合 ✅
- 写入唯一入口：`CandidateFailureWriter.persistSupplierError` → `supplier_errors_hot`（V371；19 列含 supplier/error_type/stage/error_code/http_status/is_retryable/latency_ms 低基数契约；自由文本过 `errorsx.SanitizeErrorText`）——domains/streaming/executors/supplier_error_logger.go:31-114。
- RLS：FORCE RLS + 事务级 `set_config('app.bypass_rls','true',true)` 写旁路（is_local 随提交失效）——同文件 ：45-50,120-138。
- 读端唯一事实源：`supplier_errors_unified`（hot ∪ columnar 历史，security_invoker，deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql:219-239）+ `supplier_error_stats` 预聚合表。
- 预聚合：bg/supplier_error_stats_aggregator.go——minute/hour/day 三级、watermark 幂等重算、窗口钳 48h、聚合事务内 RLS 旁路（头部注释 ：1-33）。
- 辅助表：candidate_failure_logs_hot（含 upstream_response_preview，622 补 aggregation_id 幂等聚合水位）、provider_error_details（435）、provider_error_aggregator_state（622）。

### (b) 凭据详情 API / UI 可见 ✅
- API：`GET /api/vendors/credentials/{id}/error-detail?hours=1|24|168`（admin/vendor_credential_error_handlers.go:118-180）返回 credential 元数据（availability_state/state_reason_code/circuit_state/consecutive_failures/balance 等 17 字段）+ `error_summary`（按 error_kind 折叠，含 retryable_count/stage_counts/distinct_status_codes，:229-284）+ `recent_failures`（supplier_errors_unified LEFT JOIN candidate_failure_logs 回补 upstream_response_preview，LIMIT 10，:306-366，读侧二次脱敏）+ `quality_scores_7d`。
- 读 RLS 旁路：只读事务 GUC 定式（:32-58）。
- UI：web/src/views/provider-detail/ErrorDetailTab.vue——error_summary 表（:145-155，retryable_count/count 比率 + stage_counts 徽标）、recent_failures 表（:170-）、quality_scores_7d 表（:205-207）；类型契约 web/src/api/vendor-credential-error.ts 全字段对齐。

### (c) 服务质量评估 ✅
- provider_profile_daily（availability/stability/total score）→ 详情 API quality_scores_7d；attempt 级质量：domains/providerprofile/attempt_quality.go（AttemptFacts → ErrorKinds 聚合）；趋势 API `/api/errors/trend` 读 supplier_error_stats（admin/errors_trend.go）。

---

## 三、D09 节点状态统一 · 现状盘点

目标口径：「全局节点状态统一，状态更新在一个模块，由它响应多处的成功/失败反馈」。**现状=部分统一**：反馈收集面已收敛，状态写面仍分散（S7-4）。

### 收敛面（✅ 达成的部分）
1. **反馈 → 状态的单入口链（热路径）**：executor OnSuccess/OnError → HealthTracker（domains/streaming/executors/health_tracker.go）→ credentialhealth 三件套：
   - Recorder（Redis 滑窗，有界 32 slot drop-on-full 写闸，:48-77）
   - Checker（markDegraded → UPDATE credentials，candidate cache 失效钩子；credentialhealth/checker.go:531,628）
   - Tuner（并发额度升降）
   - DBProber（30s 近窗成功即豁免降级，防 client disconnect 误杀）
2. **ProbeSync/ProbeConfirm（2e6d55c11，验证在位）**：双层信号量 per-cred ≤2 → 全局 8 的获取顺序对调防饿死；ProbeConfirm 纳入共享闸 + 容量饥饿 fail-open（不降级）+ `llmgw_node_probe_confirm_slot_starved_total` 计数，ctx 取消维持 fail-closed（bg/node_probe.go + bg/metrics.go）。
3. **健康面恢复 SQL 统一**：bg/node_probe_write_through.go——healthyBindingSQL + healthyCredentialSQL 单一实现，manual%/admin_protected 豁免 + balance_floor 豁免（2026-09-13 防抖谓词），探测与业务成功同走 `syncHealthyNodeSurfaces`。
4. **6827fce80 验证在位**：`probeTrafficExclusionPredicate` 全部 6 个拼接点均带 AND（bg/credential_selfcheck.go:515,569；bg/model_probe.go:405,875；bg/model_tier.go:155；View 变体 WHERE 首位免 AND：bg/passive_probe_listener.go:124）；promote 批次 `SET LOCAL statement_timeout='60s'`（bg/partition_manager.go:1124；1271 处另一批 10min）。
5. **node_probe_state 视图族**：读面统一经视图（admin/probe_dashboard.go / credential_monitor_heatmap.go / routing.go 等均读视图族），bg/probe_policy.go、probe_necessity.go 统一探测必要性判定。

### 写状态模块全清单（未统一的部分）
| 层 | 模块 | 写的状态面 |
|---|---|---|
| bg 探测/恢复 | node_probe.go、model_probe.go、model_probe_reconcile_healthy.go、credential_probe_v2.go、credential_cycler.go、credential_recovery.go、credential_autoheal.go、broken_probe_reviver.go、probe_service.go、periodic_quota_probe.go、balance_quota_probe.go、default_probe_picker.go | node_probe_state / model_probe_state / credentials.availability_state+quota_state / credential_model_bindings.available |
| bg 监控 | candidate_failure_monitor.go:301-374、balance_floor_guard.go:718-1297、bandit_flusher.go、slot_suggester.go | availability_state / quota_state（余额拉闸） |
| 执行热路径 | executors/executor.go:2731（model_not_found 5min 冷却）、:2827（free 档抑制窗）——**绕过 bg 直写 node_probe_state**；executors/health_tracker.go→credentialhealth | node_probe_state / credentials.availability_state |
| domain | domains/credential/writer.go（RestoreOnSuccess=ready + 失败态写入）、breaker.go（内存熔断态）、auto_revoke.go | credentials.availability_state / circuit_state / consecutive_failures |
| admin 手工/运维 | credential_monitor.go、credential_state_handlers.go、node_operations.go、provider_cred_lifecycle.go、provider_offer_force_recover.go、free_pool_extra.go、routing.go、peak_handlers.go、provider_credential*.go、providers.go、diagnostics_*.go、probe_history.go | 手工 pin/解 pin、强制恢复、探针历史 |
| 其他 | discovery/discovery.go:308、modelcatalog/upsert.go、domains/providerprofile/credential_actor.go、domains/modelquality/discovery.go、autoroute | 发现期状态写 |

评估：反馈面单链 ✅、探测并发与恢复 SQL 收敛 ✅；但「状态更新在一个模块」按字面未达成——admin 直写与 executor 直写是两处结构性破口。风险被豁免谓词（manual/admin_protected/balance_floor）跨写点一致性兜住，本轮未见状态互踩实锤。

---

## 四、B7 client_ip 真源三层接线 · 验证

| 层 | 结论 |
|---|---|
| 数据源层（47980a138 / 740） | ✅ 底/中层 regexp 补列 + 顶层 115 列契约重建；`client_ip` 31 处投影；逐 view 独立守卫幂等；pre-341 底表无列跳过；down 迁移双侧在位。repo `sql/migrations/startup/740_*` 与 installer embeddata 逐字节一致（diff IDENTICAL ×2）。自愈体 db/request_logs_view_schema.go 同步：appendSelects `source.client_ip`（:275）、宽度门控 115/113（:249-337,531,568-673）、内层无 v.* 星（:620 注释 + 契约测试 :115-150 断言「frozen 链 lateral 只追加 fp/raw，永不 credits/client_ip」）。离线契约测试绿；live 臂跳过（S7-6） |
| 消费层 SQL（rollup/看板） | ✅ bg/stats_minute_rollup.go `rollupDimQueries` 新增 `{"client_ip", COALESCE(NULLIF(HOST(r.client_ip),''),'__unknown__')}`（:161 附近），virtual_ip 留作遗留对照维度（注释明示）；admin/dashboard_board_queries.go:208-213 `client_ips→client_ip` + classifyClientIPPie（admin/ip_region.go:185-215，内网直显/GeoIP/降级三臂）；dashboard_board_fallback.go:14,103 + emptyBoardPies 键同步；web 8 语言 i18n + board.ts 键位已切（1cd15c6a7 验证） |
| 消费层内存/Redis | ❌ 见 S7-2（boardcache fold 丢 client_ip） |
| 遗留投影点 grep | bg rollup virtual_ip 维度（有意保留对照）、admin/logs.go:47,186 per-row virtual_ip（有意保留假名展示）、boardcache fields.go:75（S7-2 主病灶）。无其它以 virtual_ip 冒充客户端 IP 的消费点 |

---

## 五、D10 统计 · 验证

1. **maas 倍率口径（dccf93e51）✅**：抽 4 个倍率点全一致——rateIn/rateOut/rateCacheIn/rateCacheOut 四档均已把 `mult` 移出每档 CEIL（maas/credits_sql.go:26-29），倍率并入总额单次除法 `CEIL(Σ×mult/1e6)`（credits_sql.go:49）≡ Go `int64(math.Ceil(numer * rateMultiplier / 1_000_000.0))`（maas/credits.go:93）。commit 例证 10.4×1.5: 40→39 方向正确。残差见 S7-5（文档化 ≤1 credit/桶）。
2. **B8 对账清理/窗口收敛 ✅**：`pruneOldFindings` DELETE run_at < now()-2×window（bg/ledger_reconciliation.go:166-179，每次 RunOnce 调用 ：142）；`effectiveWindow` clamp 到 lifecycle.hot_retention_hours（:152-160）。
3. **rollup 与 740 兼容性 ✅**：rollup 主表+维度表均扫 `request_logs_with_current_month`；credits 表达式 `RequestLogCreditsSQL("r",true)` 引用 `r.credits_rate_multiplier`（738 列）+ 本轮新增 client_ip 维度——两列均在 740 后 115 列契约内（迁移末列名单 `…,credits_rate_multiplier,client_ip`）且自愈体同源，无缺列风险。其余 view 大户（mv_consistency / probe_policy / candidate_failure_monitor / analytics_materialized 等 20+ 消费文件）所用列均在 115 列契约内，未见 740 删改列的消费者。

---

## 六、下一轮入口建议

1. S7-1：messages/responses 两协议补 survivalTerminalError 分支 + 落库钉桩（最小 diff，两处错误块各 ~15 行）。
2. S7-2：dimPieKey 一行 + fold 钉桩测试；随下次部署自然生效（Redis delta 无需回填——baseline rebuild 会重新拉平）。
3. S7-3：9/24 20:52 automation 首跑前，把脚本落仓 + workspace 改绑 llm-gateway-go-2。
4. 带 DB 环境补跑 `TestRequestLogsViewV2EnsureMatchesMigration`（S7-6）。
