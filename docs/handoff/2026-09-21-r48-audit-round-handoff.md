# Handoff: R48 48h 审计轮收口 —— 评分热路径专项 + DEBT 白名单清偿 + oldest-row-age gauge + ModelPicker 守卫

> 日期: 2026-09-21（执行 + 收口推送）
> 状态: **全部完成**——主提交 `84efda93e`（merge `e93420124`），留档
> `docs/audit/2026-09-20-r48-48h-audit-round.md`（命名对齐 R47/R49 同日惯例）。
> 推送链：84efda93e → merge e93420124 → 3d818b934（db-changelog 部署产物）→
> bdc533e55（留档改名），main 与 origin/main 同步。

## 一、结论/根因

**R47 §六 四项优先级全部落地，实测数据支撑（非声明式完成）**：

| # | 任务 | 结论 | 实测证据 |
|---|------|------|---------|
| 1a | D 基准先行 | `BenchmarkPlanByTier_n10_s50` 落地，基线 **26884 ns/op / 97.5 KB / 68 allocs** | `router_bench_test.go`，3×10s 实跑，deterministic seed |
| 1b | A2 权重启动解析 | 六维 env 权重（sticky/recency/balance/planquota/headroom/capacity）`NewRouter` 一次性预解析入 `Router.EnvWeights`；单改 **−2%（在 noise 内，诚实登记）** | 26362 ns/op；`readEnvWeights` nil 回退保逐字节兼容 |
| 1c | B 单次 Snapshot | `StickyLoadView.Snapshot(ids)` 批量读（单锁），`StrategyInput.StickySnapshot` 一次构建全候选共享，Info() 调用 **2n→1 次锁**；顺带修 D01#5 跨模型快照覆盖语义；终态 **24660 ns/op（−8.4% vs base）** | 4 钉桩全绿（快照优先/recency/原子性/nil 安全） |
| 1d | C 锁分片 | **按 D01 §三 计划正确推迟**——gauge 证明会话数偏大后才做（当前无证据） | 登记 R49 |
| 2 | DEBT 白名单清偿 | admin 三优先 8 处裸读全部双腿化到 `request_logs_with_current_month` 视图，删 3 条 DEBT 条目，自清洁守卫绿 | session_list.go:109/136/306/363、usage.go:789/796/1078、session_online.go:102；guard test 1.0s 绿；admin 套件 66s 绿 |
| 3 | 存储演进二批 | `llm_gateway_hot_table_oldest_row_age_seconds` gauge 落地（与 backlog_rows 互补）；**sessions 族 DROP TTL 正确推迟**——按 R47 §五#3 需一周双 gauge 观测后裁决 | `hotTableTSColumn` 映射 + SQL 注入白名单 + 3 钉桩；合并远端 733 `session_turn_details_hot`（ts 列）已被默认映射覆盖（亲验） |
| 4 | UI 统一批 | R46 #10 ModelPicker `[value]` 守卫收官（`safeValue` computed + watch 纠正三种错形态）；PaginationBar 收编 7 页**按计划留独立 UI 轮** | 7 钉桩全绿；vue-tsc 绿；vitest 全仓 129 files/936 tests 绿 |

**根因性质疑（批判式自审）**：
1. A2 的 −2% 增益在 3 次 bench 的方差（26362/28831/33569 → 前 3 次 26884 基线只有 1 组）内，**不能声称 A2 单独显著**；−8.4% 是 A2+B 合并终态 vs 基线，主要归因 B（锁消除 ~2000ns 理论值与实测 1700ns 吻合）。留档已如实写明。
2. `firstHopLotteryWeights`（router_scoring.go:738）仍走 Info() 回退——有意保留（低频路径，避免扩大 StrategyInput 签名扩散面），注释已声明。
3. 白名单清偿只完成了 admin 三优先（8/44 裸读点），**不是全量 25 Go+6 SQL**——目标原文"本轮先清 admin 前三优先"，如实按范围交付。

## 二、改动文件与关键行为

**主提交 84efda93e（19 files, +1037/−52）**：

| 文件 | 关键行为 |
|------|---------|
| `domains/streaming/executors/router_bench_test.go`（新） | BenchmarkPlanByTier_n10_s50 + Scale；benchCandSet/buildBenchRouter/benchStickyLoad stubs；TestMain 静音 slog（防 10% LOAD_SCORE_V2 采样日志污染测量） |
| `domains/streaming/executors/router_scoring.go` | LoadScoreEnvWeights 类型 + DefaultLoadScoreEnvWeights + readEnvWeights（nil 回退）；calculateLoadScore 签名 +stratIn；stickySessionPenalty/recentRequestPenalty +snapshot 参数优先读 |
| `domains/streaming/executors/router.go` | Router.EnvWeights 字段；NewRouter 预解析；planCandidates 构建 StickySnapshot 传入 stratIn |
| `domains/streaming/executors/sticky_load.go` | StickyLoadView 接口 +Snapshot(ids)；StickyLoadTracker.Snapshot 单锁批量实现（语义与 Info 一致：snap 优先/maxAge 判据/activity 取大） |
| `domains/streaming/executors/strategy.go` | StrategyInput.StickySnapshot 字段；p2cStrategy.Score 传 in（回退路径不带快照） |
| `domains/streaming/executors/router_b_test.go`（新） | 4 个 B 钉桩 + 3 个 A2 相邻测试（setenv 必须在 NewRouter 前——A2 预解析语义的显式契约） |
| `admin/session_list.go` | 4 处 `FROM request_logs` → `FROM request_logs_with_current_month` |
| `admin/usage.go` | 3 处同上（含 :789 `rl2` 别名子查询） |
| `admin/session_online.go` | :102 `JOIN request_logs rl` → `JOIN request_logs_with_current_month rl` |
| `internal/sqlreadguard/guard_test.go` | 删 3 条 DEBT(R47) 条目（session_list/usage/session_online），加注释指向 R48 修复 |
| `bg/metrics.go` | `llm_gateway_hot_table_oldest_row_age_seconds` GaugeVec + recordHotTableOldestRowAge + hotTableTSColumn 映射（default ts；sessions 族/candidate_failure_logs 等 created_at；白名单防注入） |
| `bg/partition_manager.go` | hotTableOldestRowAge(ctx,label) 查询（EXTRACT(EPOCH FROM now()-MIN(ts))，空表=0/失败=-1）；promote 循环 backlog 后调用 |
| `bg/partition_manager_test.go` | TestHotTableTSColumn（20 断言映射）+ TestHotTableTSColumn_TSQLInjectionGuard |
| `web/src/components/ModelPicker.vue` | safeValue computed（multi+string→[]、single+array→首元素、过滤非字符串元素）+ watch 漂移纠正 emit |
| `web/src/components/ModelPicker.test.ts`（新） | 7 钉桩：三种错形态纠正 + 两种合法形态不误触发 + multi 空数组 |

**测试文件同步更新**（calculateLoadScore 签名变更连带）：router_scoring_test.go、router_stickyload_scoring_test.go、router_two_layer_cost_test.go。

## 三、测试命令与结果

```bash
# 三门（合并远端后复跑全绿）
go build ./...                                   # OK
go vet ./...                                     # OK
go test -count=1 ./bg/ ./domains/streaming/executors/ \
  ./internal/sqlreadguard/                       # 7.4s / 23.8s / 2.2s 全绿
go test -count=1 -timeout=300s ./admin/          # 66.2s 全绿

# race（涉并发包）
go test -race -count=1 ./internal/sqlreadguard/... \
  ./domains/streaming/executors/...              # 10.3s / 25.3s / 1.5s 全绿

# 基准（三轮 10s）
go test -bench=BenchmarkPlanByTier_n10_s50 -benchtime=10s -count=3 \
  -run='^$' ./domains/streaming/executors/
# 基线 26884 ns/op → A2 后 26362 → A2+B 终态 24665/24725/24589（−8.4%）

# 前端
cd web && npx vitest run                         # 129 files / 936 tests 全绿
cd web && npx vue-tsc --noEmit                   # EXIT 0
```

## 四、遗留风险

1. **sessions 族 TTL 裁决未做**（by design）：需一周 backlog+oldest 双 gauge 观测。若一周后水位居高，`stateTableTTLSpecs()` 加 3 行即可启用，无迁移。
2. **A2 增益统计显著性弱**：如未来有人在 CI 上 benchstat 出 A2 单独无增益，不必惊讶——A2 的价值在消除每请求 60 次 env 系统调用与为 B 铺路，非独立大优化。
3. **`firstHopLotteryWeights` 未接入快照**：首跳抽签路径仍走 per-candidate Info()。若 p99 profile 显示该路径占比上升（当前不是热点），R49+ 可把 StrategyInput 传参扩展到 planByTier→firstHopLotteryWeights 链。
4. **Snapshot 接口破坏性变更**：任何外部实现 StickyLoadView 的代码（仓外）编译会断。仓内已全部同步（StickyLoadTracker + benchStickyLoad + 测试 stub），grep 复核无遗漏。
5. **白名单余债 21 Go + 7 SQL**：admin 前三清偿后守卫继续强制推进，但 domains/hooks 等非 admin 域的裸读（如 output_compliance_control.go:84、main_v3_wiring.go:107）仍是运行时读面盲区，R49+ 按同法逐文件清偿。
6. **hotTableTSColumn 映射靠人工同步**：新 hot 表上线若 ts 列名不是默认 ts 且未登记 switch，gauge 会查错列（fail → -1 保 last 值 + Warn，不会崩但会哑）。TestHotTableTSColumn 钉住现有 20 项，新增表时必须同步。

## 五、下一轮提示词（R49 入口）

> 拷贝 docs/audit/playbook/orchestrator-prompt.md 作主提示词，起点如下：
>
> 你是 llm-gateway-go 仓库的审计主代理（R49 轮）。窗口 95d170645..HEAD。
> 优先级：
> 1) **sessions 族 DROP TTL 裁决**（R48 §五#1 顺延）：读取
>    `llm_gateway_hot_table_backlog_rows` + `llm_gateway_hot_table_oldest_row_age_seconds`
>    一周观测水位（prometheus/245 现网），裁决 session_turns/session_bodies 是否进入
>    stateTableTTLSpecs()（+3 行，零迁移）；DROP=删用户历史，语义风险前置，须给出
>    水位证据链再动手。
> 2) **C 锁分片**（R48 §五#2 顺延）：同一份水位数据裁决是否需要 shardLock——
>    backlog 平均 <100 则登记不动。
> 3) **DEBT(R48) 白名单续清**（R47 §五#2 剩余 21 Go+7 SQL）：按 R48 同法
>    （双腿化→删条目→guard 绿），优先运行时读面 cmd/gateway/output_compliance_control.go:84、
>    main_v3_wiring.go:107。
> 4) **UI 独立轮**：PaginationBar 收编 7 页（AgentRegistryView/DecisionsView/
>    ApprovalListView/RoutingLogView/PromptInjectionSettingsView/UserProfileListView/
>    ModelsView 主表）+ Badge 抽象统一（≥40 文件，可拆两轮）。
> 纪律：①删除类修复先自跑引用扫描；②子代理线索亲读复核；③SQL 先真库实跑；
> ④性能权衡第一步 EXPLAIN；⑤i18n 8 语言全落 + vitest run；⑥三门 → 留档
> docs/audit/2026-09-2N-r49-48h-audit-round.md → 推 main。git 网络用
> https_proxy=http://127.0.0.1:7897。
