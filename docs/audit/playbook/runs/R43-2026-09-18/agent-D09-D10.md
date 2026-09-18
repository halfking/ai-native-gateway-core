# D09+D10 合并域子代理报告（R43 轮）

**审计窗口**：48h = `643735a28^..HEAD`（917 文件改动面）；**重点（未经审计增量）= `b75c91900..HEAD`**（R40 收尾之后全部提交，含 R42 修复本体）。本报告所有"增量"指后者；48h 窗口内更早改动属 R40/R41/R42 已审计基线，仅作横向对照。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 触发路径 | 建议处置 |
|---|---|---|---|---|---|
| 1 | **P2** | `feature_stats_worker` 同一 worker 内日窗口径分叉：`computeDedupRate` 在 3a9dca46d 改为显式 UTC 半开窗（`ts >= $1 AND ts < $1 + INTERVAL '1 day'`，statDate=UTC 午夜），但同文件的 `computeSingleFeatureDistribution` 仍用 `DATE(ts) = $1`——`DATE()` 按**会话时区**求值，而 db/db.go DSN 未钉扎 TimeZone | bg/feature_stats_worker.go:90（UTC 午夜）、:174/:182（dedup 新窗）、**:141（feature 分布仍 `DATE(ts)`）**、db/db.go:45-53（无 TZ 钉扎） | 部署在 Asia/Shanghai 会话时区的库上每小时 `computeStats` → `feature_distribution_stats.stat_date` 按上海日分组、`dedup_stats.stat_date` 按 UTC 日分组，同名 stat_date 两表描述的底层窗口错位最多 8h；且 `DATE(ts)` 不可 sargable，478 分区父表每次全扫描（7 特征×每小时） | 把 :141 改成与 dedup 相同半开窗（一行改法与 3a9dca46d 完全同构）；或主代理裁决"统一上海日"后两处一起改 |
| 2 | **P2（边界/一致性）** | `auto_route_affinity_worker.aggregate` 的合成轮过滤（R38 收口）只 join `request_logs_hot`，但 hot 表 7 天默认 promote 走人（迁移 341 `p_retention DEFAULT '7 days'`），而聚合窗 `affinityWindow = 14 天`——7~14 天段 `rl.origin_actor` 为 NULL，`COALESCE(...,'')` 使 `goal-%`/loopback 行**穿透过滤被当普通流量**计入 `task_model_affinity` | bg/auto_route_affinity_worker.go:54（14d 窗）、:276-286（LEFT JOIN 仅 hot + NOT LIKE 谓词）、sql/migrations/startup/341_hot_table_independence.sql:209-211 | 每轮 sweep 全窗重算（upsertAggregates sample_count=窗口全量，:323-356），7 天前落入父表的合成轮每轮都漏进聚合。**当前无实伤**：settle worker 给合成轮落 reward=NULL（bg/auto_route_settle_worker.go:74-80），主守卫 `reward IS NOT NULL` 已排除；该 join 本是防"未来某写路径给 goal-* 打真 reward"的纵深防御——但其覆盖面只有窗口前半 | 把 join 改为覆盖全窗的读面（hot+父表 UNION，或 `auto_route_selections_all` 同款 all 视图镜像），或登记 Phase-2 债；同时给 341 promote 与 14d 窗的覆盖关系补一条注释钉扎 |
| 3 | P3 | D10 §3.2 写-only 债复发候选：`dedup_stats`/`feature_distribution_stats`/`feature_quality_metrics` 无任何 worker 外消费方（仅 worker 自身 detectAnomalies 读回打日志）；`GetLatestStats` 零调用方（死代码）；且 `feature_quality_metrics` 全仓**无写入方**（只读不存在的数据）。另：聚合只查 `auto_route_selections` 父表，656 hot 堆（settled 8h 后才 promote）里"今天"的行系统性缺席，且 statDate 只前进不回填，日均被低估 | bg/feature_stats_worker.go:140、:189（父表）、:321-365（无调用方的 GetLatestStats）、:344（读无人写的 feature_quality_metrics）；sql/migrations/startup/656_auto_route_selections_hot.sql:113-160 | 每小时 computeStats 写表无外部读面（ML 训练质量监控用途未接线） | 沿 R30 routing_optimization_metrics 先例：要么挂只读端点，要么登记 write-only 债；顺带删/接 GetLatestStats |
| 4 | P3 | `settings` 新增 `CachedPlatformBool/String/Float` 三读取器 + `cachedEffectiveRaw` 缓存机制**全仓零调用方**（生产与测试均无），仅 helpers.go 注释与 store_db.go 失效接线引用——投机性死代码（反向"写必有读"） | settings/ttl_cache.go:69-149（新家族）、settings/store_db.go:82/:88/:218/:234（失效接线）、settings/helpers.go:19（仅注释引用） | 无（无消费方即无 staleness 风险），纯维护面 | 要么在窗口内本应受益的热路径接上（如有 ≤5s 重载容忍的 GetPlatformBool 调用点），要么注释标注 Phase-2 备用；顺带指出 `entry.registry == Global` 对包变量 Global 的无锁读在测试并发 swap 下理论 data race（生产 init-once 不触发） |
| 5 | P3 | 文档漂移：D09 playbook §R40 回注称豁免分支"绑定/observed/**ladder** 全走 else 分支"，实际 ladder（node_probe_state.consecutive_failures）对 gateway-side 错误**含豁免在内一律冻结**（15m 固定退避）；R42 在代码注释里已写对（"失败 ladder 仍按 gateway-side 冻结"），漂移残留在域文档本身 | bg/node_probe.go:2211-2219 + :2224（CASE WHEN $10 冻结）、:2111-2115（R42 修正注释，与代码一致）；docs/audit/playbook/domains/D09-node-state-selfcheck.md §R40（"含 ladder"表述） | 豁免的单凭据密文损坏凭据永远停在 15m 探测节奏、attempt 不升级（对永久损坏凭据这是保守方向，R42 已注释钉死为可接受） | 轮末回注时把域文档 R40 句改为"绑定/observed 走 else；ladder 按 gateway-side 冻结（R42 钉扎）" |

## 二、核实为健康的面

- **URSM v2 三路失败写门控对称（R42 修复本体，重点复核项 1）**：三个调用点全部收敛为 `ok || ursmFailureWritable(errCode, errDetail)`——tick runOne（bg/node_probe.go:2098）、sync ProbeSync（:1584）、持久队列 probe_service.Run（bg/probe_service.go:423）；谓词本体 `!isGatewaySideProbeError || credentialSpecificDecryptFailure`（:2904-2913）与 updateBindingAvailability 内部门（:2874）、三处 updateObservedState 失败调用点门（sync :1558 / tick :2104 / queue :718）完全同构。成功恒写（恢复信号）三路一致。钉桩 `TestURSMv2FailureWriteCarriesGatewaySideGuard`（bg/node_probe_gateway_side_test.go:207-239）对三个调用点逐个源码断言。R39 教训（legacy/queue 守卫必须成对）本轮**未再犯**。
- **单一写入面无新旁路（重点复核项 2）**：`stateSink` 全仓仅 node_probe.go 的 updateURSMv2ProbeState 一处使用（grep 证实）；窗口内 node_probe_state 写者集合不变（admin/diagnostics、credential_recovery 均未动）；`updateObservedState` 恰 6 个调用点全部成对门控。R40 豁免与 R42 门控叠加语义经亲读核对自洽（豁免放行绑定/观测/URSM，ladder 仍冻结——见发现 #5）。
- **探测暂停语义注释修正与代码一致**：bg 生产代码无任何 `paused = TRUE` 写入（grep），6h 梯顶在 bg/probe_backoff.go:75-87（2026-07-24）；node_probe.go 头注释与 deescalate 注释的改写（含"R42 豁免写与启动清扫不可区分"权衡，:578-590）与实现相符。modelQualityTrigger "attempt>=2 而非 maxAttempts"注释与 tick（:2248）/queue（:490）两处代码一致。
- **MarkTimeout RLS 适配正确**：显式事务 + `SET LOCAL app.current_role='super_admin'`（domains/sessionaudit/approval_manager.go:404-424、:453-459），defer Rollback + 显式 Commit；测试同步更新为 ExpectBegin/SET LOCAL/Commit（approval_manager_test.go:464-479）。60s worker 周期注释同步，无双计面。
- **partition_manager RLS bypass 模式正确**：`runWithBypass` 用事务内 `set_config(..., true)`（事务局部）+ Commit（bg/partition_manager.go:1455-1484），跨租户 TTL 清理不再静默 0 行。
- **runtime_alerts → runtime_alert_events 表切换一致（重点复核项 4）**：`runtime_alert_events` 表真实存在（迁移 403，status CHECK 覆盖查询的三个状态值），center 包 8 处 SQL 全部用 events 表，无任何残留单数表引用（grep 证实，SQL 侧亦无旧表）；消费方 center/admin_api.go:189。此修实为 P1 级救火——原查询自 7c462c1fa 起查不存在的 `runtime_alerts`，该 admin 端点必 42P01。未引入双实例重复计数（读路径，instance_id 过滤）。
- **余额探测失败落账（重点复核项 3 一半）**：credential_probe_v2 失败路径写 `balance_error` 戳且守 `ManualBalanceProtectionPredicate`、故意不 bump checked_at（bg/credential_probe_v2.go:597-614）；floor guard 失败路径同戳同谓词（bg/balance_floor_guard.go:1013-1014）；戳内容不含厂商响应体、契约表 + 接线测试齐备（bg/balance_manual_protection.go:16-46）。manual 戳条件化正确：仅 balance_usd 值真变更才翻 manual（admin/provider_credential.go:636）。
- **settings 失效语义**：Set（platform-only，租户走 SetTenant）/Rollback/Delete 三写路径均先写库后 `InvalidatePlatformValue`，顺序正确；失败结果不缓存（瞬时 DB 错误不钉住 fallback）；registry 指针守卫防测试换 Global 后串味。
- **统计防双计（重点复核项 3 另一半）**：feature_stats 两表 INSERT 全部 `ON CONFLICT DO UPDATE` 全量重算语义，双实例并发写同键收敛同值，无增量累加双计；routingopt 纠正混合是纯函数 + 60s TTL 整体替换（无跨刷新累加），纠正源出错降级为 auto-only 不打断主循环；affinity upsert 的 sample_count=窗口全量（历史双计修复注释在位，:311-322）。
- **rollup 42P10 修复（58384b0d8 / 迁移 726）**：唯一索引恢复 + ctid 去重先于建索引、事务包裹，`migration_726_test.go` C1 把索引元组与 auto_index_refresher 的 ON CONFLICT 元组绑定钉死——D10 聚合管道（credential_model_index rollup）恢复进化。
- **vacuum_worker 减负正确**：VACUUM FULL 分区父表移除（父表无存储，原每周空耗集群 mutex 窗），hot 表改 `VACUUM (ANALYZE)`；`VacuumFullMutex` 仍有 data_lifecycle_storage.go:787 在用，非死代码。
- **admin/routing.go free-pool bootstrap 批量化语义保持**：SELECT 已过滤 status='disabled'，批量 ANY($1) UPDATE 与逐行版行为等价；该直写 credentials 状态面是既有 bootstrap-only 通道，非窗口新增旁路。
- **shadow_actors 注释修正属实**：request_logs_hot 落库面的 X-Gw-Source-Actor 确在 handler 入口 TrimSpace（domains/streaming/handler.go:1727），R39"唯一写入口"说法确为不完整，R42 注释修正成立。

## 三、未覆盖项与原因

- **未跑 go build / go test / -race**：只读审计子代理无写权限，三门验证留主代理（R42 轮文档称其修复批三门全绿，本轮未复跑）。
- **发现 #1/#2 的真库影响量化未做**：需对真库查询（父表中 7-14d 段 origin_actor LIKE 'goal-%' 且带 reward 的行数；非 UTC 会话时区下两 stat 表的窗口错位样本）——本环境无真库凭据。
- **settings `cachedEffectiveRaw` 的 Global 并发 swap race** 未以 `-race` 实测（无消费方，生产 init-once 不触发，仅理论面）。
- **643735a28..b75c91900 段的 node_probe.go(369 行)/probe_service.go(65 行)** 按 R40/R42 已审计基线处理，仅复核其与 R42 门控的叠加语义，未逐行重审（超出"未经审计增量"焦点）。
- **窗口内非本域变更未审**：taskprofile 模块（724/725）、session/v2 outbox reaper、streaming TargetProvider 接线、installer/migrations 五点同步、deploy 脚本族——属 D16/D17/D14 等邻域边界。
- **`GET /api/admin/routing-opt/metrics` 与 /metrics、仪表盘三处口径对账（D10 §3.4 全量）**：本轮仅覆盖窗口内触碰的 dedup/affinity/rollup 面，未重做全指标口径横向对账（无窗口内相关变更触发）。

**主代理复核结论（R43）**：#1 成立 → 已对齐 UTC 半开窗；#2 成立（与 D04#2 合并，含 341 p_retention=7d 澄清）→ 登记遗留；#3/#4 登记（#4 的 RESERVED 注解本轮补）；#5 域文档回注时更正。runtime_alerts→events 修复为窗口内隐性 P1 救火，健康面留档。
