# D02 协议适配与双向解析 子代理报告（窗口：876302d5e..67f78247c，聚焦 59eb7fa06 + 67f78247c）

> R37 主代理注：窗口内实际含 5 个提交（另含 deploy-local 两修 + R36 审计批 ad68c91ac + 一次 merge）；按派发指令仅深审 59eb7fa06 与 67f78247c。窗口内新增/改动测试已实际运行验证全绿（本机 go test -count=1）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3（P2 若复活） | **同型 typed-nil 隐患未修副本**：domains/routing/sticky.go 的 SetRedisStore 与被修的 executors 版逐字同构，无 typed-nil 归一化；生产零引用（仅 sticky_redis_test.go 使用），属死代码孪生副本 | domains/routing/sticky.go:76-80；读点 :191/:314/:477 | 删除死副本，或同步归一化 + 注释指向 59eb7fa06 |
| 2 | P3（现网不可达的脆弱耦合） | **intentStore typed-nil 通道**：main.go:1232-1234 具体指针注入接口成 typed-nil；SessionIntentCache.SetRedisStore 无归一化，redisFallback 守卫同型失效；当前被 fpSlotRedis!=nil 门挡住 | cmd/gateway/main.go:4919-4921、:1232-1234、:886-888；autoroute/session_intent_cache.go:129-134、:163 | 同款反射归一化加进 SetRedisStore |
| 3 | P3（flaky 观察） | **E2E 用例 a 读窗口偏紧**：deadline retryInterval*15=1.2s，慢 CI + -race 下可能误报；Go 侧三用例均未钉 think 节奏间隔（cadence 只在 Python 场景脚本断言） | survival_no_nodes_e2e_test.go:189、:223-225；scenario_no_nodes_survival.py:8,408 | 读循环改轮询至终帧或余量放大；节奏可加一例钉桩 |
| 4 | 备忘（非缺陷） | 终态判定顺序：Exhausted()（attempt_limit_exceeded）先于 retry-limit（retry_limit_exceeded）检查；用例 c 能钉 retry_limit_exceeded 依赖 stub 不消耗 UpstreamAttemptBudget 这一隐含前提 | executors/attempt_budget.go:59-61; survival_coordinator.go:661-691; survival_no_nodes_e2e_test.go:309-316 | 记入域文档 §历史回归点：换真消耗预算的 stub 时用例 c 需同步预期 reason |

## 二、核实为健康的面

- sticky.go typed-nil 修复本体完备（归一化一次、三读点快照判空全覆盖、装配点双保险、回归钉两路径）。
- liveactions typed-nil 修复完备（NewEmitter 唯一构造口、write() 守卫经归一化恢复、回归钉桩在位）。
- internal/trace.NewRedisRecorder 收具体指针非接口，typed-nil 机制不适用，5 处方法均有 nil 守卫——健康。
- 413 映射链路端到端：三协议入口双 guard 齐备、code 全下划线；上游 413→KindContextLength→FailTerminal；流式未提交走 R36 one-shot compress-retry；默认 2M 预算下 32MiB 分支数学不可达；双钉在位。
- kind 注入边界：空候选集落 no_candidate_outcomes、空 kind 必 FailClosed、kinds 全为常量无大小写风险、按次聚合不跨 attempt 累积；无 history 兼容版保持 generic。
- reason 消费面不被新串破坏：wait 决策恒走重试分支；新串只进 think 注释/日志/metrics 标签/durable 透传；生产代码对 wait_recovery_window 无精确匹配消费者；metrics 测试已同步钉新标签。
- E2E 三用例钉桩真实且走真实 ChatHandler+SurvivalCoordinator 代码路径；断连经 select 即时生效；预算上界由 res.Attempts-1>=maxRetries + NightMaxRetries 同值保证确定性。

## 三、未覆盖项与原因

- 窗口内 ad68c91ac（R36 审计批）触及的同域文件未逐条深审（派发指令限定两提交）。
- scenario_no_nodes_survival.py 端到端验证需真机网关，仅静态核对 think 正则与 reason 归一化。
- 仓外 Grafana/告警规则若硬编码 wait_recovery_window 串会多出新标签值，需运维面自证。
- 59eb7fa06 声明的 252 部署验证属运维证据，只读审计无法复核。
