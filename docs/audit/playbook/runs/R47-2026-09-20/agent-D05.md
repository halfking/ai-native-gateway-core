# D05 多层队列/限流并发安全 子代理报告（窗口：45412e919..HEAD）

## 一、发现（候选）
| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | LiteRetentionWorker `DELETE FROM request_logs WHERE ts < ?` 无可用索引（仅 (tenant_id,ts)/(session_id,ts) 复合），裸 ts 全表扫描且单语句持 SQLite 写锁；与请求路径写共池，长扫期间并发写等 busy_timeout=5s 后 SQLITE_BUSY | bg/lite_retention_worker.go:74；schema.go:65-66；factory.go:126 | 加裸 ts 索引 + 分批删除（批间查 ctx） |
| 2 | P3 | RunOnce 会话清理 TOCTOU：缝隙(a) 复活会话丢 turns；缝隙(b) 孤儿 turn（session_turns 无 FK）。窗口亚秒×6h 一轮 | bg/lite_retention_worker.go:81-116 | 最后 DELETE 用已收集 id 或单事务 |
| 3 | P3 | StickyLoadTracker.Close() 无生产调用点（Close 契约悬空） | cmd/gateway/main.go:1436-1440 | shutdown 段补 Close |
| 4 | P3 | sweepLoop goroutine 无 panic recover（同文件 Refresh/Observe 都有） | sticky_load.go:108,137-149 | 加顶层 recover |
| 5 | P3 | 与 Trimmer 惯例两处漂移：不过滤 context.Canceled；无"已启动"日志 | bg/lite_retention_worker.go:52-68 vs cache_trimmer.go:68,80 | 对齐 |
| 6 | P3 | F4 注释漂移：探测走 idx_request_logs_hot_request_id，但 718 已 DROP（pkey 影蔽） | bg/auto_route_affinity_worker.go:273-274 | 注释改 pkey(request_id) |

## 二、核实为健康的面
- goroutine 生命周期（trimmerWG 先 Add、Start 阻塞式、Shutdown 有界等）；6h ticker 首轮立即执行；SQLite 共享句柄并发安全（WAL+busy_timeout=5000，MaxConnections 钳池）；删除顺序 turns→sessions 符合依赖；幂等测试在位。
- F4 NOT EXISTS 语义等价（request_id 唯一性 ⇒ 旧 JOIN+IS NULL 放行 == 新 NOT EXISTS 排除；NULL actor 两形态均放行）；行数暴涨受 affinitySweepTO=3min 封顶，distlock TTL 契约未破。
- Bandit 死代码移除无悬挂引用；StickyLoad 接线段（typed-nil 归一、单飞+TTL 节流、Info 陈旧回落）；stickyFailed/stickyFailKind per-request 独享（pipeline 单 owner 串行，注释钉死无需加锁）。

## 三、未覆盖项
真库 EXPLAIN 复测 F4；build/vet/test 实跑；大 backlog 锁持有时长；lite 请求路径写同步/异步归属。
