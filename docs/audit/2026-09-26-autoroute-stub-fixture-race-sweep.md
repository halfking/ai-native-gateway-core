# autoroute 测试地基轮：共享 fixture 桩 -race 并发排查（2026-09-26）

## 0. 对象与结论

- 对象：autoroute 包内全部测试桩（stub）与其共享 fixture 指针/切片的并发安全性。上一轮仅修 stubClassifier（Classify 浅拷贝）；本轮扫尾"其余"。
- **结论：新增一处实锤 race——stubIndex.Recommend 返回共享 fixture 底层数组，经 tier policy 路径被原地写（V1/V2 均可达）。已按 stubClassifier 同款修法收口（返回拷贝）+ 回归测试钉死。其余桩全部安全。**
- 已有 `TestDecider_ConcurrentDecide_ThreadSafeRanking` 虽共享同一 stubIndex，但未装配 WorkTypeRouteStore，tier 写点不可达——**存量 `-race` 绿对该 hazard 是假阴性**。

## 1. 生产侧原地写点清单（Decide 候选路径全量）

| 写点 | 行为 | 判定 |
|---|---|---|
| `work_type_route_store.go` applyTierPolicyWithRoutes | `scored[i].Breakdown.RouteTier = ...` **原地写** | 唯一活动写点；V1 decision.go:614 与 V2 decision_v2.go:297 均调用 |
| `work_type_route_store.go` ApplyBoost | 原地乘 Composite + 写 RouteBoostApplied + **原地 sort 输入切片** | **生产死代码**（全仓仅测试调用）；注释明说 in-place 是其契约；若未来接入 Decide 路径需重审 |
| promoteCanonical / promoteFirstPresent / applyOptimizerRanking / toOptimizerCandidates / FilterBanned / PromotePins | 全部 `make` 新切片，copy-on-write | 安全 |
| rankingOptimizer / trackingOptimizer / confidenceAdjustingOptimizer | 互斥锁计数器、新建返回 | 安全 |
| 真实 `Index.Recommend` | 每次 `make` 新池（index.go:186 起） | 生产无别名，风险纯由桩引入 |

## 2. 桩面全量清单

| 桩 | 共享面 | 判定 |
|---|---|---|
| stubClassifier | `*Classification` fixture | 上一轮已修（浅拷贝） |
| stubIndex | `[]ScoredCandidate` fixture | **本轮修复**：Recommend 返回 `make`+`copy` 拷贝 |
| v2TestClassifier | 无（每次 `&Classification{...}` 新建） | 安全 |
| stubScanRow / stubIntentStore | 值接收者/空结构 | 安全 |
| shadowTestClassifier / blockingShadowClassifier / countingClassifier | 无共享 fixture 写 | 安全 |
| failingPutStore / failingOptimizer 等 | 互斥锁保护 | 安全 |
| newV2Index（真 *Index 包 fixtures） | entries 只读共享，Recommend 拷出 | 安全 |

## 3. 证明（可复跑）

```bash
# 修复前：race 翻红，栈顶正落预测路径
go test -race -run TestDecider_ConcurrentSharedStubIndex_TierStore ./autoroute/
#   → Read/Write race at work_type_route_store.go applyTierPolicyWithRoutes
#     ← decision.go:614 ← 20 goroutine 共享 stubIndex
# 修复后：目标测试 + 全包 -race 双绿
go test -race ./autoroute/   # ok 5.25s
```

- 回归测试：`autoroute/decision_stub_fixture_race_test.go`（20 goroutine × 50 Decide，共享单 stubIndex + WorkTypeRouteStore primary/secondary 两档，secondary 必被滤除、primary 元素必被原地写 RouteTier——无拷贝必翻红）。
- 修复：`decision_test.go` stubIndex.Recommend 返回拷贝（对齐 stubClassifier 的桩边界所有权契约）。

## 4. 方法论沉淀

1. **桩 fixture 所有权契约**：桩方法返回指针/切片时必须交拷贝——只要生产被调方存在任何原地写（哪怕一处），共享 fixture 即 race。逐桩判定"被调方是否原地写"，而非"测试是否并发"。
2. **并发测试必须装配完整的原地写路径段**：共享桩的并发测试若没接到唯一写点（本例：缺 WorkTypeRouteStore），-race 绿是假阴性。写点清单先于测试设计。
3. **写点判定以"传入切片元素赋值/sort 输入"为准**：`make` 新切片一律安全；`scored[i].x =` 与 `sort.Slice(scored)` 即原地写。

## 5. 遗留与挂账

- ApplyBoost：生产死代码但 in-place 契约活跃；未来若接入 Decide 路径，需先按第 4 节方法论重审（挂账观察项，不阻塞）。
- owner 决策两项维持待办不变：① deploy-154.sh/deploy-seamless.sh 探针 120s 是否上调 600 对齐 245（d5eeb71eb 先例，注意 official-deploy 克隆 13 份脚本副本陷阱）；② 并行双 checkout 版本戳互覆盖根治（09-25 seq 2250 撞号实证）。
