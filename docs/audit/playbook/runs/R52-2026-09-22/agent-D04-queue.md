# D04 多层队列/限流并发/负载均衡 子代理报告（窗口：119981c02..5dc4e9f3b）

> 主代理复核结论（2026-09-22）：#1 维持 R51-F15 登记顺延（reqprobe 免费重试限流计量，机制既有扩展面）；#2 与 D14-#9 同根成立已修（R52-F5：processOneRecovered recover 分支删 running[key]，行为钉桩 TestActiveProbeWorker_PanicReleasesRunningDedupKey）；#3 确认仍在、维持 R51 登记下轮（BaseWorker 自愈重构）；#4 已补 2 例（R52-F17）+ active_probe 行为断言；#5 登记顺延（InvalidateLearned 时顺带 learnSF.Forget 可选）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P1（存量，R51-F15 确认未修） | reqprobe 两次免费重试不过 Governor：probeRetry（参数剔除/模式回退各至多一次）在凭据 attempt 内 attempt-- 免费重发，加 ctxLenRecoveryRetry 免费重试，一次准入最多 4 次上游 HTTP 调用；Governor Acquire 仅准入时一次，RPM/TPM/并发占用按 1 次计入——限流计量失真、雪崩防护预算被绕过 | domains/streaming/executors/executor_chat.go:462-466,508-516,1041,1069；domains/dispatch/forwarder.go:379 | 维持登记；修复方向为重试计入 retry budget 或按次补记 Governor 用量 |
| 2 | P2（本轮新增，F13 收口副作用） | active_probe 的 processOneRecovered 吞 panic 后 running[key] 永久残留：清理仅在 markSuccess/markFailedFinal 正常路径，panic 后既不 re-enqueue 也不删 dedup 键——后续 Submit 全部被静默丢弃，该凭据+模型探测链死亡直至重启 | bg/active_probe_worker.go:257-266、284-289、138-144、405-409/444-448 | recover 分支补 delete(running,key) + 钉桩 |
| 3 | P2（R51 已登记下轮） | BaseWorker recover 后永久退出不重启：recover 仅记日志，started 永久 true，Start 幂等守卫使后续 Start 返回 false；进程不退出 systemd 不会拉起。影响 storage_retention/vacuum/balance_floor_guard/provider_error_aggregator 等全部 BaseWorker 系 | bg/base_worker.go:104-108、88-91 | 维持 R51 登记"BaseWorker 自愈重构"；短期加 restarts 计数 |
| 4 | P3 | worker_panic_recover_test.go 仅覆盖 4/6（缺 apihub syncRecovered、credential_autoheal cycleRecovered）；用例只断言 panic 不逃逸，未断言循环存活与状态清理 | bg/worker_panic_recover_test.go:14-64 | 补 2 例 + 行为断言 |
| 5 | P3（观察项） | reqprobe singleflight 残留小口子：InvalidateLearned 与 in-flight 回源并发时旧规则可回写、最长 60s 生效（pre-existing）；回源失败/空结果缓存 60s（fail-open 既有语义）；首调用方 Background ctx 无取消毒化问题 | internal/reqprobe/coordinator.go:158-164,171-178；reqprobe_integration.go:46 | 可选 InvalidateLearned 顺带 learnSF.Forget |

## 二、核实为健康的面

- F11 node_probe 退避重置并发安全且 8min 超顶已修：纯函数 + loop 局部变量无竞态；健康判定 ran ≥3×30s tick；7 个边界用例钉桩。
- F21 singleflight 主体正确：key 与缓存一致；到期并发合并；learned map 全持 mu；共享 slice 只读消费。
- F7 pin 置顶不会错误迁移 sticky 会话：只改 tierFailoverModels 头部；orderedAutoFallbackModels 剔除 ==chosen 项；仅当前模型穷尽后使用；修复前缺陷（旧胜者占梯子头）确已消除。
- 窗口内无新增绕过 Governor 的供应商出口点：apihub_watcher 控制面、reqprobe/telemetry 观测、executor.go 仅 UA 字符串；forwarder panic recover 带 gov.Release 兜底不泄露。
- F19 每表保底双实例收敛：advisory lock + DO UPDATE 幂等，无重复计数。

## 三、未覆盖项与原因

- 未实跑 go test ./bg/ -race（只读）——主代理已补跑全绿。
- bg 剩余约 50 处裸 go 与 BaseWorker 自愈重构维持 R51 登记。
- credential_selfcheck/active_probe 双实例重复探测（进程内 dedup 无分布式锁）——窗口未改动，备注存疑。
- loopOnce 全文逐行复核基于结构确认，未逐行读 drainDue 内部。
