# D04（多层队列/限流并发）+D08（供应商错误链与凭据服务质量）子代理报告（窗口 643735a28..876302d5e）

> 只读审计，主代理已亲读复核。复核结论见轮文档 §一。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P1 | scan_scheduler 预算豁免把真失败一并豁免：Go ≥1.23 下 http.Client.Timeout 满足 errors.Is(err, context.DeadlineExceeded)（主代理 go1.27 实测 true），扫描器 30s Client.Timeout 命中豁免分支 → recordScanFailure 不执行 → 3 次自动禁用对挂起供应商永不触发；且串行 sweep 每挂起模板烧 30s，2 个即饿死本轮 | bg/scan_scheduler.go:370-381；provider_scanner.go:51,108-133；scan_scheduler.go:455-463 | 收紧为仅 ctx.Err() != nil（**已修 R36**） |
| 2 | P2 | durable_contract.go "UNUSED/zero production callers" 标注事实错误：DurableRequested/ClientSignalRequested/clientSignalKindAllowed/DurableRequestSnapshotV1 均有生产调用（handler.go:3116、responses.go:486、handler.go:5137-5138/5173/5204、durable_wiring.go:150-174、durable_runner.go:59）；main.go:2809-2820 已接线 durableStore —— 照注释清理会误删活特性 | domains/streaming/durable_contract.go:1-2 | 修正标注（**已修 R36**） |
| 3 | P3 | action_bridge.go "zero production callers" 字面不实（main_dispatch.go:39 类型引用+测试存在），实质主张（构造链生产未接线）成立 | domains/streaming/action_bridge.go:1-2 | 措辞修正（**已修 R36**） |
| 4 | P3 | R34 breaker 两修缺极值/行为钉桩：exponentialCooling 无直接单测（cycle=63/float 饱和/v<=0）；换族无条件重置无混族断言 | domains/credential/breaker.go:536-553,396-403 | 补钉桩（登记） |
| 5 | P3 | UpdateFromProbe 补 batchWriter 的不对称与效力边界：probe 路径有 nil 防护（manager.go:611）但成功/失败路径裸调（:252,:506）；recover_at/last_error COALESCE 永不清空；表零读者（R34 遗留#1） | manager.go:252,506,606-618；batch_writer.go:191-200 | 与 GetState L3 兜底一并决策 |
| 6 | P3 | buildOutboundProvenance 零测试覆盖（256 帽/128KB fallback/merge 不覆写/window_source 构成） | request_log_pipeline.go:1388-1452 | 补钉桩（登记） |
| 7 | P3 | backfill 截断 WARN 的 deferred 计数虚高（len-written 把后续 stale-skip 也计入）+缺 trigger 字段 | bg/model_availability_backfill.go:220-232 | 口径修正（登记） |
| 8 | P3 | provenance 载荷队列内存上界：4096 条 ×128KB 理论 ~512MB（实际仅压缩请求携带，条目本就带 body）——留档基线 | telemetry/client.go:465-467,757-777 | 留档 |
| 9 | P3 | 换族无条件重置 coolingCycle 对 flapping 坏 key 退避变弱（Auth cycle4 穿插 Timeout → 归零）；alternating exponential 族旧代码同样重置，非新回归 | breaker.go:393-403 | 主代理接受现状 |
| 10 | P3 | materialized_view_refresher.go:282 distLock.Acquire 裸用 sweep ctx（同 P2-8 形态）；scan_scheduler 头部无 leader 选举注明（env 双开即双实例重复扫） | materialized_view_refresher.go:282；scan_scheduler.go:8-19,67-68 | 下轮派发（登记） |

## 二、核实为健康的面
- exponentialCooling 两调用点已替换、无残留裸 math.Pow；float 域+先钳后转，负冷却不可能
- UpdateFromProbe Add 在 per-key 锁内同步执行、探测失败同样落账、跨批到达序=Add 序
- distlock 两路径均有界（follower 3s handshake + leader 3s 独立超时）
- attempt_gate_context.go UNUSED 标注准确（零外部引用）
- 流式 handler 并发面：injectFollowUpRequest 捕获不可变字符串；attemptHasClientSemanticOutput 走 gate.mu；responses_bridge 终态闩与 18933bc6c 契约一致
- D08 两去向不变量：Zhipu 流中仅改 Resumable；三个新正则语义窄化、把误归 Transient 的超长体改判 ContextLength（减少熔断画像污染）
- propagateIsAutoRequestToEntry never-overwrite + 4 钉桩；EMA 不再清零；SetDistLock 契约修正
