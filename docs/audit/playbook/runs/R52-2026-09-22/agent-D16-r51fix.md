# D16 R51 修复波复核子代理报告（窗口：119981c02..5dc4e9f3b）

> 主代理复核结论（2026-09-22）：#1 成立已修（R52-F3：CachedPlatformString + 锁外读 + settingsPut 失效接线）；#2 裁决为修（R52-F9：CorrectEstimatedUsage 补门控）；#3 裁决为修（R52-F23：trace FlushToPG 停写期丢弃+删 Redis key）；#4 已修（R52-F15：RegionStatsReport 并入 overlay）；#5 已补 2 例测试（R52-F17）；#6/#7 维持现状留档（本轮文档登记）；#8 计数口径勘误随轮文档。

复核对象：R51 修复波 8 个提交（655ca58b2 / 42038e1dc / 7d2ea8dd6 / 2fa1942ad / 0c2153721 / 28928f0aa / 03b439798 / b4e353459+993a41a1b），逐提交读 diff + 关联代码现状 + 测试断言抽查。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2（修复引入） | F2 的 defaultBannedRegionsOverlay() 是无缓存 settings DB 读，且调用点在 selectNodeExcluding 的 selectionMu 临界区内——每次节点选择持锁做一次 PG 查询；DB 挂起时所有出口选择串行阻塞最长 5s/次 | proxy/manager.go:354-365、proxy/types.go:244-256、settings/helpers.go:49-51、settings/store_db.go:32-40 | 改 CachedPlatformString 或锁外快照 |
| 2 | P2/P3（覆盖面缺口） | F3 门控漏网写点：CorrectEstimatedUsage 的 UPDATE request_logs_hot（usage_source='corrected'）无 logsWrite 门控——提交声称"六个包裹点"实为 4 个 if logsWrite 块；停写开启后仍可改写停写前存量行 | domains/hooks/observability/telemetry/client.go:1836-1869；门控点 1180/1706/1949/2321 | 主代理裁决补门控或登记豁免 |
| 3 | P3（同族第二漏网） | RedisRecorder.FlushToPG 的 UPDATE request_logs_hot SET trace_events 无门控；停写后父行不存在 → ErrTraceParentNotFound 且 Redis trace 永久保留每次重试 | internal/trace/trace.go:461-495 | 登记或随 #2 一并处理 |
| 4 | P3 | F2 overlay 未接入 RegionStatsReport：admin 地区分布 Banned 计数不叠加平台默认禁区，与选择行为口径分叉（纯展示面） | proxy/manager.go:1990 | 补 overlay 参数 |
| 5 | P3（测试断言与声明不符） | F13 worker panic 测试仅覆盖 4/6（缺 apihub syncRecovered、credential_autoheal cycleRecovered）；F13 收口是"循环内逐 tick recover"，与 R50 runRecovered（整 loop 守护+退避重启）同目的不同机制，轮文档"照 runRecovered 模式"表述不精确但行为正确 | bg/worker_panic_recover_test.go:14-64 | 补 2 个对称用例；文档措辞勘误 |
| 6 | P3（设计确认） | F10 "input==0 即补"确实覆盖上游真实返回 0（全 cache-hit 响应）；缓解：仅改客户端可见 body，内部 usage_ledger 计费用原值；非零永不覆盖 | domains/streaming/messages.go:1340-1372 | 维持现状留档 |
| 7 | P3（理论边界） | F12 两处理论边界（当前 wire 形态不触发）：a) locateKeyedValue 首个 value 字节匹配的 reasoning key 绑定假设；b) probeReasoningRaw SSE 多帧 buffer 解析静默 no-op——现调用方均单帧/纯 JSON | internal/paramledger/ledger.go:231-302 | 登记已知边界 |
| 8 | P3（提交信息失真） | 计数类声明与代码不符：F3"六个包裹点"实为 4 个；F13 模式名同 #5 | 见 #2/#5 证据 | 轮文档勘误 |

## 二、核实为健康的面

- F1 时序根修闭合：responses/chat/anthropic 各路径 redact(restore(body)) 均先于定长（executor_chat.go:1566-1570、:1732-1734、executor.go:175-178、executor_anthropic.go:295/316）；真实 http.Server 回归测试修复前可 FAIL。
- F12 结构感知还原正确：span 限定 reasoning 对象原始字节；转义形态字节层不可能匹配；SSE 帧前缀保留；四形态测试在位。
- F2 词组匹配正确：Phrases 相邻匹配（Hong.Kong/hong_kong/多空格）；HK 只加词组不动 Tokens；选择路径全入口汇于 selectNodeExcluding:381；双测试在位。
- F32 列值对齐亲核：37 列/36 值/实参逐一对应；占位符守卫测试解析源码本身。
- F3 门控键一致同默认 true；usage_ledger 在门外（设计意图）；4 件门控测试在位。
- F6 三处守卫对齐且无第四处（admin/models.go:37 与 govern 脚本为操作员显式写不属复活面）；正则钉桩在位。
- F7 与 R50 F14 同构：role 头先、pin 头后；空 tierPlan 返回 nil 有前置；pin 胜者必在池内。
- 735 迁移逐字一致（索引表达式与 DedupCanonicalNameSQL run-collapse 臂字节级相同）；五点同步齐；TestStartupFilesHaveNoDuplicates 钉死双注册复发面。
- F20 DO UPDATE 安全：冲突目标与父表唯一约束精确对应，无 42P10；RETURNING 计数保循环终止；回填幂等。
- F11 退避重置正确（真 min 封顶、健康判定 ≥3 tick、restarts 只增）。
- F19 每表保底正确（tableRanBatch 门控、错误/空批 break、cycleCtx 优先）。
- F18 幽灵表三张全仓确无建表/写入。
- P3 其余：F8 冲突目标与 schema 精确对应；F16 列号 1-based 全表核对一致；F21 singleflight fail-open 已注释声明；F22 fail-safe；F14 container 进标准扩展往返；F23 归一化与请求侧口径一致；F17 cache-latest 固定 tag 成立。

## 三、未覆盖项与原因

- go build/test 实跑（只读约束）——主代理已补跑全绿。
- web 总览页 Vue 侧 3s 重试/空态逻辑依赖 npm 环境，采信提交内"31/31"声明为线索。
- F5 ursm 脚本三路径演练需真库——仅核对 0c2153721 diff 结构。
- admin ingest 门控测试 pgxmock 断言细节未逐行推演。
