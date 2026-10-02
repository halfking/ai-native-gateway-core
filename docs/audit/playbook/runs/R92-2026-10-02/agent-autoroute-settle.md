# R92 域B 报告：auto-route settle worker 族与 resolve 真库门（第三十二轮）

审计人：分域子代理 B（只读）。HEAD=8f79ad402。手段：git show 对账、go build/vet/test、promtool（docker）、两棵历史树实跑、scratch-tree 变异、本机真库只读。

## 一、逐提交判定

| 提交 | 判定 | 证据 |
|---|---|---|
| e5e997feb §9.35 | 成立 | reason 闭集 5 值（:82-88）、pessimistic 初始化 + live 分支显式清 Stale（:145-148）、42P01→absent（:115）；3 挂载点 admin/auto_route.go:746/1175/1270；TestOutcomeSource* 全绿 |
| 0ad75a8ff §9.40 | 成立（附 P3-1 注释错） | 真库 EXPLAIN：710 视图 v1 臂展开 7 叶子（hot/2026_07..11/default）与自述逐字一致；缺口结论被 TestAutoRouteSettleWorkerSourceConstraintIsDocumented 钉住 |
| a06dd860e §9.41 | 成立 | 真库复现旧表达式对 routing_attempts 报 non-array 错；341,660 行 100% object、0 array；新守卫（worker:520-533）实跑 500 行 clean |
| e154135fa §9.42 | 成立 | 三态在 worker:680-690，retryScore 缺省 autoroute/affinity.go:422；CHECK 约束引用 db/db.go:7764 逐字属实；三道门全绿 |
| 075760768 §9.43 | 成立（含 P2-2 风险） | 默认 v1（settleSourceFor(true) 断言）；三腿同源；门读失败第三态落 v1 且静默 |
| dc01a69c5 §9.44 | 成立 | 会话族分支真执行（M2 变异：仅硬编码会话键列→一致性集成测试红 42703）；cohort 三指标三告警+rule_tests；真库 cohort v1=1699/session=0（结构性零确认） |
| a7cf114ca | 成立 | 门真过 handler（删 filter 重赋值→156 处 INVARIANT BROKEN）；缺 env 是 skip 但 CI 注入 env 统计（run-integration-gate.sh:587,597-599）；本机 960/960、0 foreign 复现 |
| a0da9066d settle 腿 | 成立 | family 用批首 src（worker:621）；中性计数移 writeReward 成功后；logSettleSourceSwitch 死代码删除留碑 |
| 32aa86eeb §9.49 | 成立（归属自白实跑验证准确） | git archive 两棵树实跑 9 道 admin 器眼测试：e154135fa 9/9 绿；075760768 恰 4 红（与「4/5 是 §9.43 引入」精确吻合）；第 5 个两树均绿（与 816 归因一致） |
| 4897c28f0 §9.53 | 成立 | s1a_fields.go:64-99 引用不悬空；区间内恰 9 个路由组映射 |

## 二、发现清单

**P2-1 settle 停滞零告警面**：worker:276-280 出错仅 slog.Warn；rules 全目录无 settled_total/settle_lag 消费；三条 settle 告警全以 `increase(source_total[...])>0` 为前提；停滞>8h 行被 promote 出 hot、hot-only 写使 selection 永久 unsettled（reward NULL）。复现路径：任何使 settlePendingSQL 整查询报错的回归（§9.41 即实例）。

**P2-2 门读失败静默翻回 v1**：settings/helpers.go:91-94 EffectiveValue 出错静默 fallback=true → auto_route_settle_source.go:106-108 落 v1；停写期该翻回零 settle 信号（P2-1 同盲区）。

**P3**：P3-1「每 30 秒/每次 100 行」3 处与 settleInterval=5m/settleBatchSize=500 不符（被文档门间接固化）；P3-2 writeReward 影响行数不检查（先在类）；P3-3 一致性集成测试对「全量硬编码回 v1」天然免疫（M1 实证绿），形状门单点承重；P3-4 每轮双读门切换瞬间短暂异族标注（一轮自愈）；P3-5 CohortEmpty 稀疏流量下活动守卫难满足。

## 三、重点核查与健康的面
- §9.41 fail 方向：毒表达式已除（真库验证），守卫最坏退化为 0 重试而非中止。
- §9.43 分支完备性：门查询失败落 v1 是提交自辩的保守选择（未初始化=今日行为）。
- §9.44 覆盖：M1（表+列全量硬编码）→形状门红/一致性集成绿；M2（仅会话键列）→一致性集成红——分支确被执行。
- cohort 可观测：闭集标签+注册表断言+rule_tests 6 场景 promtool SUCCESS；两条历史缺陷（changes>0 恒触发、缺 on(family) group_left）变异回退均红。
- 汇总：go build 0、vet 0、bg/admin/autoroute/rules 全绿、bg 会话族 3 集成测试对真库 scratch schema 绿。

## 四、不确定项
§9.43/9.44 自述变异未逐条复跑（承重的 M1/M2 与 rule_tests 已复验）；§9.40 方案B 覆盖数字基于 9 月分区取样未复核（方法学自洽已核）；is_auto_request 83%/p95 15.2% 须 252 终验；真库 cohort 现测 1699 与自述 2178 差值=24h 滑窗（结构性结论不受影响）。
