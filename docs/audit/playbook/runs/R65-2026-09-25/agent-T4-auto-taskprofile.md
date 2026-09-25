# T4 auto/taskprofile/testbench 子代理报告（窗口：2026-09-24T05:00..2026-09-25T05:00, HEAD 9635b9b17）

> R65 轮只读子代理原文留档。主代理复核结论见轮文档（#1/#3/#5/#7 复核属实并当轮修复）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P2** | **回路C 修正/分母/回放读冷父表 `auto_route_selections`，遗漏 hot heap（656 架构）**。selections 先进 hot 表，promotion 滞后（settled 8h、unsettled 7 天兜底）期间对父表不可见。`POST /tuning/proposals/generate?days=7` 的 JOIN 与 volume 只扫父表；BacktestThresholdBand 同盲区。对照：annotation 采样与 testbench 候选生成均已用 `_all`——同一份修正数据三个消费者两种口径 | taskprofile/analyzer.go:382、:419、:474、:495；对照 sql/migrations/startup/656_auto_route_selections_hot.sql:113-174、admin/annotation_handler.go:320、cmd/auto-testbench/generate.go:214,:246 | 统一切 `auto_route_selections_all`；与 cmd/tuning-backtest/main.go:140 同步【已修】 |
| 2 | **P3** | **threshold 只降不升 + 全仓无任何 API 回滚路径**。Go 侧 tuning_params 唯一写者是 applyProposalInTx；`thresholdOnlyLowerViolation` 拒绝升档，也无 manual 参数端点。误批 0.70→0.50 后除直接 SQL 外无法升回 | admin/auto_route_tuning.go:504-508、:524-534；taskprofile/analyzer.go:51、:70 | apply 允许精确回退或增 manual 端点；至少 UI 明示【登记】 |
| 3 | **P3** | **keyword_add / weight_adjust 的 apply 无 key 白名单**，与 threshold 分支的 `allowedThresholdKeys` 纪律不对称。keyword_add 可指向任意 key，解析失败回退空数组再写回（参数行损坏）；weight_adjust 同理。运行时有兜底但 DB 行损坏且无 API 修复 | admin/auto_route_tuning.go:558-572、:587-590；对照 :445-448 | 补白名单 + fail-closed【已修】 |
| 4 | P3（待复核） | **闸门 classifier 口径不含 `heuristic_v2`**。任一 V2 子特性 flag 开启时生产走 DecideV2 且给 classifier 追加 `_v2` 后缀；taskprofile 三个闸门 SQL 均只匹配 `'heuristic'`，V2 路径的启发式修正与流量整段漏出闸门。R64 F5 在采样面已白名单化 `llm_v2`，闸门面未同步。待复核各环境 V2 flag 实际取值 | autoroute/feature_flags.go:337-341；autoroute/decision_v2.go:385；taskprofile/analyzer.go:384、:421-422、:476 | 与 F5 同法扩白名单或按部署拍板后钉口径【登记】 |
| 5 | P3 | **`BacktestKeywordHint` 缺 `classifier='heuristic'` 过滤**——同文件其余回放都限 heuristic，唯独 keyword 回放混入 llm/jev 行，证据数字虚高。当前三枚举休眠不可达 | taskprofile/analyzer.go:490-498 | 补同款过滤【已修】 |
| 6 | P3 | **bg/feedback_analyzer 的 weight_adjust 提案零去重且 new 值硬编码**。每次 AnalyzeOnce 对同一 (canonical_id, task_type) 重复插 pending（无 EXISTS 探针、不取 'TUNP' advisory 键）；`"new":20` 不读当前权重，陈旧提案批准后会静默覆写人工调过的值 | bg/feedback_analyzer.go:308-377、:344 | weight 路径补 pending 探针；new 从当前 weights 读出【登记】 |
| 7 | P3 | **rejectProposal 对不存在/非 pending 的 id 也回 200 "rejected"**——不查 RowsAffected，与 approve 的 404/409 语义不对称 | admin/auto_route_tuning.go:397-411 | 检查后回 404/409【已修】 |
| 8 | P3（潜在） | **testbench 与包内回归对 known_failure 语义分叉**：包测试容忍 known_failure 失败，testbench 把它计入 Total/accuracy——未来添加第一条 known_failure 行会打爆随仓 baseline（threshold=1.0）。当前三套件 0 行 | cmd/auto-testbench/metrics.go:81-111；对照 autoroute/auto_matching_suite_test.go:141-189 | 单列不计入门禁指标【登记】 |
| 9 | P3（已记录残留） | **task-profile 写端点后端仍挂普通 admin 中间件，super 门控仅前端**：R64 F16 有意只做前端兜底并留注释，属已接受残留 | taskprofile/handler.go:128-136；admin/handler.go:1395 vs :1363；web/src/router.ts:204-210 | 后端补齐 parity【登记】 |
| 10 | 横切观察 | HEAD 9635b9b17 使 i18n-audit 实测 56 missing、exit 1（归 reportrollup 域，T1#3 同事实） | web/scripts/i18n-audit.mjs 实跑 | 移交【已修】 |

已登记不重复开案：bg keyword 去重 LIKE 未转义（feedback_analyzer.go:255-262，R64 同族预留仍在）；generate 中途失败已插提案不计响应（R64 A1-P3-6）；i18n-audit --json 截断（R64 登记）。

## 二、核实为健康的面

- **Gate 口径**：volume/corrections 均限 classifier='heuristic'，keyword_add 三枚举休眠如实（inferDomainHint 只产三值 + 单测钉桩）。
- **并发与闭环**：generate 竞态由单事务 pg_advisory_xact_lock('TUNP',1) 覆盖探针+插入；AnalyzeOnce TryLock+409 带单测；approve 走 FOR UPDATE+同事务 apply。越界校验：经任何在库路径都无法把阈值调成 0/负数/超大；days 7-90 双端钳制。
- **testbench**：validateBaseline（exit 2）+ CLI 互斥 + --refresh-baseline 可达 + 负值舍入语义化 + 随仓 baseline 与代码实测一致（240 例 GATE: PASS 复现）。
- **分类器链路**：fail-open 完整；R44 置信度门完整；hydrate clamp 与 apply 同契约；Jev 断路器 + 指标族；决策可观测性成立（/api/admin/auto-route/decisions + selections 异步落库）。
- **前端最后一公里**：8 语言键集 diff 全 MATCH；index/nav 注册 ×8；路由 requiresSuper；menu-config 同步。

## 三、未覆盖项与原因

1. 真库端到端演示（禁连库；R64 scratch 验证留档）。
2. AutoTuningView/TaskProfileView 全量逐行 UI 审查（仅核 API 接线/门控/路由/i18n）。
3. cmd/tuning-backtest keyword/weight 回放全量（threshold 分支已亲读并入发现#1）。
4. classifier_llm.go 内部（prompt 构造与响应解析）。
5. 8 语言翻译语义质量（只验键集结构）。
6. V2 flag 各环境实际取值（发现#4 定级前提，待复核）。
