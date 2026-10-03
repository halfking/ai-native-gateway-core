# WP-3 轮32修复独立复审 子代理报告（窗口 a0da9066d..HEAD）

复审对象：0bda13c41 / 830f2f221 / 41af920d5 / 7d55159b4 / fe40c0802 / 91466ce63 及同文件叠加终态；另核查 R32 §四#2/#3 settle 可测两开放项在 HEAD 的现状。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1（候选）**：0bda13c41 的 P2-C 钉测自身是 flaky/racy 的，「CI 承重」承诺有洞 | **(a) 真数据竞争**：T2/T3 在续约循环运行中途裸写 `g.renewErr`（无锁），而 `fakeLeaseGovernor.Renew` 在 mutex 临界区外读它——实跑 `go test ./domains/dispatch/ -run TestLastArmed -race -count=1` 复现 FAIL+DATA RACE（写：lease_renewer_lastarmed_test.go:149/174；读：lease_renewer_test.go:53,56）。**(b) TOCTOU 假红**：不带 -race 的 count=3 实跑复现 T1 `:130 abort must cancel the forward context`——abort 闭包先 `close(aborted)` 后 `cancel()`，测试观察到 aborted 关闭后立刻断言 `fwdCtx.Err()!= nil`，cancel 尚未生效即误报。**(c) 锚读竞态**（分析性）：替身 Renew 在返回前就发 renewedCh 信号，而 loop 在 Renew 返回后才读 `lastArmed = cf.nowClock()`（lease_renewer.go:93→96）——waitForCall 返回后测试的 Advance 可落在锚读之前被锚吞掉。`make test-race-core`（Makefile:34,70 含 ./domains/dispatch）会周期性被 (a) 打红 | domains/dispatch/lease_renewer_lastarmed_test.go:62,64-68,130,149,174 + lease_renewer_test.go:42-57 + Makefile:34,70 | 修测试替身不改生产码：renewErr 读写纳入 g.mu；abort 闭包 cancel 先于 close；锚读握手（锚读计数）——**本轮已按此根修并变异复验 M1/M2/M3 仍承重** |
| 2 | P3 | 7d55159b4 翻转口径后，同文件两处旧注释与行为相反（测试 doc 仍写「cp 过来 → this test goes red」、包头 §"FALSE INVARIANT" 仍论证「Syncing=看起来修好了其实更错」——新口径恰把「副本出现」合法化为收敛态）；另 sortedKeys 死帮手无调用方 | sql/schema/baseline_drift_test.go:11-31、:98-105、:249 | 本轮已订正注释 + 删死帮手 |
| 3 | P3 | R32 轮文档 §四#2/#3 与「下一轮入口」指引已过时：f609ecab1 已落地 sweep 失败计数器+两条告警收口两开放项，但未回写轮文档 | docs/24h审计第三十二轮-20261002.md:59-60,91-92 vs f609ecab1 | 本轮已补收口回写 |
| 4 | Low/登记性 | P2-D「恒真收窄」的前提是 partition_date ≈ 写入日——但它是 `DEFAULT CURRENT_DATE` 的普通列而非 ts 生成列，恒真仅在「ts 不超前入分区时刻一天以上、读写同 tz」下成立；注释断言「恒成立」比实际前提强 | bg/credential_selfcheck.go:315-320 vs 430/526 迁移 | 可接受现状；登记为已知假设 |
| 5 | 备注 | 任务简报把 request_logs_stop_write_classification_test.go 列为 830f2f221 改动文件之一，实际该提交只改 2 文件；该文件无标签且不引用 v1DirectTables，自洽 | git show 830f2f221 --stat | 无需处置 |

## 二、核实为健康的面

**0bda13c41 P2-A（value 传播链与泄漏面）**
- 传播链闭合：forwarder.go:541-543 → forwardFunc(:569) → dispatchForward（executor_dispatch.go:694）→ upstreamContext（executor_chat.go:2728-2737）。WithoutCancel 保留 value；中间包装均为 WithValue 类不丢 value；非 dispatch 路径取 nil 维持旧行为。
- 覆盖完整性：三个协议执行器（chat:754 / anthropic:1015 / ollama:1036）都走 upstreamContext；桥接/其余 WithoutCancel 点不承载上游调用，无漏网。
- abortAwareContext 无永久泄漏：watcher 只等 abort.Done()/base.Done()，base 是 2h WithTimeout；三处调用方均 defer/显式 cancel；defer cancelDetachAbort() 不会截断仍在跑的 detached 流。
- 实跑：`go test ./domains/streaming/executors/ -run 'TestUpstreamContext|TestResponsesStreamBridge' -race -count=1` 绿。

**830f2f221（integration 标签树）**：共享文件无标签；对 `!integration` 文件全部顶级符号逐一交叉核对无残留无标签引用。实跑 `go vet -tags=integration ./admin/` rc=0、TestIntegrationTaggedTreeCompiles 绿。两树确已同源。

**41af920d5**：P2-B 边界成立（frameUsage nil usage 经 omitempty 不出现；畸形载荷/`[DONE]`/截断流都落零值帧；下游 audit 仅 >0 记账，零值帧不杀流）。P2-D/fe40c0802 叠加自洽（形状门变强）。P2-E 517/527 双文件双注册+次序三点全覆盖。P2-F runner 注释如实。

**7d55159b4 牙齿**：对象须在 canonical；供给迁移须在启动集且正文含 needle（8/10 高度特异）；改名→ReadFile 错→红。实跑 TestDerivedBaselineLag|TestBaselineGenerator 绿。

**91466ce63 注释一致性**：settleInterval=5m/settleBatchSize=500 与三处订正一致；retention 头注释与 pkey 一致；索引审计 6/11 与文档自表吻合。

**R32 §四#2/#3 settle 可观测**：**已不再开放**——f609ecab1 落地 `llmgw_autoroute_settle_sweep_failures_total{reason∈error,panic}` + AutoRouteSettleSweepFailing/AutoRouteSettleStalled 进 rules + promtool 场景 7-12。

## 三、未覆盖项与原因

1. promtool 规则单测未实跑（f609ecab1 场景 7-12）：不在本包清单，文件级核对替代。
2. T3 锚读竞态未确定性复现（分析性）；已复现的是 -race 数据竞争与 T1 TOCTOU。
3. P2-D 未真库 EXPLAIN 复验（本地无 252 生产库）。
4. -tags=integration 只 vet 了 ./admin/（涉事包），370 包全树未重跑。
5. 窗口内 R31/R32-D03 线提交（38497afed 等）不在本包范围。
