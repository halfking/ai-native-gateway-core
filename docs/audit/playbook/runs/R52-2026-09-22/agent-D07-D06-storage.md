# D07（hot+columnar 分区）+ D06（双存储模式）复合子代理报告（窗口：119981c02..5dc4e9f3b）

> 主代理复核结论（2026-09-22）：#1 成立已修（R52-F4：updateModel status 分支 23505→409 + 其余分支错误浮出）；#2 成立、本轮采窄修（R52-F8：lite 装配注册 storage 族 specs + settings.Init(nil) 仅接 env backend；"文档改口"备选未用）；#3 接受为已知边界留档 + 注释位次已修（R52-P3）；#4 登记顺延（promote 循环单测）；#5 登记下轮顺手删（session_last_requests 死映射）；#6/#7 登记备查（lite journal 重复轮幽灵轮 / F8 换冲突目标后的 PK 硬失败边）。

已读 conventions.md / D07 / D06 全文；窗口内相关改动面：735 迁移族、bg/partition_manager.go（28928f0aa）、733 promote DO UPDATE（0c2153721）、storage/sqlite/turns_store.go（03b439798）、S4 停写门控（7d2ea8dd6）、settings/store_db.go（03b439798）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | 735 表达式唯一索引 × admin updateModel 复活路径 23505 被静默吞：部分索引只约束 status='active' 行，"disabled A + active B 折叠同名"可合法共存；管理员把 A 改回 active → 23505 被 //nolint:errcheck 丢弃，HTTP 仍 200——行实际未启用（空壳成功响应）。735 只给 createModel 加了 23505→409，没给 re-enable 路径加守卫 | admin/models.go:614-616；sql/migrations/startup/735_*.sql:43-47；modelname/canonical_dedup.go:26-41 | status 分支 pgErr 判别 23505→409；或 re-enable 前跑 DedupCanonicalNameSQL 预检 |
| 2 | P2 | S4 停写门控在 lite 部署形态结构上不可能生效：settings registry DB backend + PlatformSpecs 注册只在 dbConn.Enabled() 分支，lite 跳过 PG 初始化 → Global.Spec(key)==nil → GetPlatformBool 恒回落 true。而方案文档明写 lite"同门控"；R51 双模式测试是手工注册 registry 的，掩盖了装配缺口 | cmd/gateway/main.go:1339-1347；cmd/gateway/lite_telemetry_sink.go:128；settings/helpers.go:83-101；cmd/gateway/storage_mode_init.go:9-11 | lite 装配注册 storage specs（env backend）或文档改口；补 lite 装配门控行为测试 |
| 3 | P3 | F19 每表保底使"每周期 ≤100 批"从硬顶变软顶（最坏 119 批）；墙钟仍被 promoteCycleTimeout=5min 兜住；残余时间型饥饿路径（前序大表按时间耗尽 5min 时晚位表保底 1 批也拿不到）——F19 只治批数型饥饿；注释"promoteSpecs 第 10 位"实为第 11 位 | bg/partition_manager.go:51-64、1050-1056、1146-1149 | 观察两 gauge 验证晚位表排水；修注释位次 |
| 4 | P3 | F19 新语义无钉桩测试：全仓无测试引用 promoteCycleMaxBatches/tableRanBatch，预算语义全靠人肉保证 | bg/partition_manager.go:1050-1163 | 补 promote 循环单测 |
| 5 | P3 | F18 幽灵表清理不彻底：session_last_requests 同样不出现在 promoteSpecs，是同类死映射条目 | bg/metrics.go:255-265；bg/partition_manager.go:969-999 | 下轮删条目或注明身份 |
| 6 | P3 | F8 修好"永久失败"但保留 Lite journal 重试重复轮副作用：写点顺序 bodies→turn meta→details→markJournaled，失败重试以新轮号再写一轮 → session_logs_view 出现特征全 NULL 的"幽灵轮"（注释已自认非原子） | cmd/gateway/lite_telemetry_sink.go:155-217；storage/sqlite/schema.go:84-96 | 登记顺延 |
| 7 | P3 | F8 换冲突目标后的新硬失败边：ON CONFLICT (tenant_id,request_id) 不再覆盖 PK (tenant_id,session_id,turn_no)——同 (session,turn) 不同 request_id 时（仅在外部清空 bodies 目录+重启后轮号回绕可触发）撞 PK 进 failPermanent。可接受边 | storage/sqlite/turns_store.go:40-61；schema.go:75-78；lite_telemetry_sink.go:244-266 | 登记备查 |

## 二、核实为健康的面

- 735 与分区四不变量：models_canonical 普通表（写只在 hot 不适用）；表达式索引无时区依赖；733 promote/ensure 全部 UTC date 派生分区键，473 族干净。
- 735 迁移本体与五点同步齐（fail-closed 守卫指路 cleanup、IF NOT EXISTS 幂等、down 手术性、C1 表达式契约钉死、C4 embeddata 字节一致）；TestStartupFilesHaveNoDuplicates 钉死 733/734 双注册复发面。
- 733 promote DO UPDATE（F20）冲突目标逐字对应、v_set 排除 id/partition_date、advisory xact lock + ensure 后插 + anti-join 候选 + RETURNING 计数；理论残边（同 session/turn 两个 request_id）在 Full 流不可达（turn_no advisory lock 下 MAX+1 分配）。
- F18 幽灵表三标签无残留引用。
- S4 门控 Full 侧（F3）：事务级单次读门 + insert/insert-claim/update/update-claim 四门；usage_ledger、api_keys 计数、outbox 在门外照常；admin ingest 同门 + F32 修复；双侧占位符守卫就位；`CorrectEstimatedUsage`/trace flush 等旁路 UPDATE 在停写期匹配 0 行（主代理注：本轮已裁决补门控 R52-F9/F23）。
- 恢复无数据缺口假设：两 mode 均无"重开回填"路径；dual_read 7 天零漂移仅为 ops 前提无代码强依赖。
- D06 #4：PartitionManager 仍在 `!bgDataPlaneOnly && dbConn.Enabled()` 内启动，lite 不启动不变量未破。
- F22：5s 超时 fail-open 到声明默认值，方向与既有设计一致。

## 三、未覆盖项与原因

- 远端迁移编号冲突核对需 git fetch（主代理收尾时按纪律补）——主代理已核：735 为当前最新编号无冲突。
- installer 独立 module 三门由其自身测试声明，本机未复跑（本轮无 installer 改动）。
- 被删 gauge 标签的仓外 Grafana/告警引用属 deploy 仓资产。
- 252 共享库 735 实应用属真机部署动作。
- 735 表达式索引 Planner 可用性（EXPLAIN）未验——性能面非不变量。
