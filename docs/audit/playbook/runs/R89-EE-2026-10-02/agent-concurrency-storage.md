# 并发/双模式存储/可观测性组子代理报告（窗口 e3406f9e2..77837b013，含 24h 全量）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 | 处置 |
|---|---|---|---|---|
| 1 | P3 | LLM 压缩摘要被 guard 拒绝只打固定一句无 error/candidate；RecoveryCoordinator guard 分支完全无日志静默回退机械路径 | compaction.go:542,627 / recovery_coordinator.go:198 | 本轮已修（三处 Warn 补 error/candidate） |
| 2 | P3 | 输出侧强制 gate 的 block/mask 无专属 Prometheus series（block 仅 slog+审计行） | interceptor.go:244-265 / goal_control.go:577 | 登记下轮（sanitize 包新增 prometheus 依赖边需设计） |
| 3 | P3 | Wrap 锁窗口新增两段 Redis IO（正确性动机成立，锁 refs 计数正确） | smart_sani_guard.go:229-235,334-360 | 保留现状，进观测清单 |
| 4 | P3 | Redis 不可用（lite）时 generation 每请求随机新生成→增量压缩/ResultMemo lineage 全部失效，逐轮全量重算（fail-safe 有意，成本无文档） | smart_sani_guard.go:347-353 / session_compressor.go:361-370 | 登记 docs/storage lite 语义待办 |
| 5 | P3 | chain FailClosed/release 回灌错误只落 writeErr+一处 slog 无计数（本轮新造关键阻断路径） | chain.go:138-141,194-205 | 与 #2 一并下轮补 |

无 P1/P2 候选。

## 二、核实为健康的面
- vendor 升级面：go mod verify 全过；grpc/otelhttp/x-tools/x-text 均间接依赖，仓内零直接调用点（otelhttp wrap.go 删除无受损面）
- bg listener 泄漏双修复收敛到 serveOne defer；测试侧 t.Cleanup(l.Stop)；BaseWorker 握手所有权下沉+SpawnLoopWG 纵深
- FileCache.SetTTL 读侧竞争修复（-race 钉测）+async_writer 节流 Warn
- cache_v2/session_cache 大 diff 为纯字段/序列化扩展（全字段显式写、Invalidate 补删 generation 键、坏 JSON 留痕）
- sessionv2mirror 白名单键与生产端严格匹配（boundedHex 32/64 vs 生成端）
- breaker 同事件时间窗修复（变异 3 处全红）
- lite/full 接缝：lite 补装输出防护（nil detector 门控）；sessionv2mirror 仅 dbConn.Enabled() 注册；autoroute 无 PG 依赖
- 供应商错误链路完整：supplier_errors_hot/candidate_failure_logs_hot 写入 3s 超时；admin VendorRecentFailuresSQL 1ce06c354 修行数扇出+citus XX000 规避+真库门
- budgetCheck fail-closed（e3406f9e2）本体成立
- 429 不计入 breaker 为设计定版（仍进 supplier_errors 事实面）

## 三、未覆盖项与原因
docs/web/守卫包内部未逐行；真库门 SQL 未跑；dispatch 生产代码窗口无 diff；grpc 运行时等价性仅编译级证明；admin session_* 大文件族由前轮收口本轮仅抽样。
