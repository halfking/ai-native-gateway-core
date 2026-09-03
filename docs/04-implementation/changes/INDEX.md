# 04-implementation/changes · 索引

> 最后更新：2026-09-04

## 2026-09

- [2026-09-04 · v6 调度装配与观察索引正确性](2026-09-04-v6-dispatch-composition-and-index-correctness.md) — 修正 Pipeline 启动前依赖装配顺序（RetryScheduler / QueueMirror / QueueBackend / GovernorBackend / snapshot observer / capacity-aware sort），移除 sticky 路由的 credential ID=0 哨兵，修复 DimensionIndex `MaxKeys` 孤立引用，并让节点归属只在 Tier-2 credential hand-off 成功后登记；恢复 autoroute treatment 归因实现，修正 Prometheus 告警契约；全仓测试、核心包 `-race` 与构建均通过。

## 2026-08

- [2026-08-26 · v6 请求类型落库（request_class/due_at，migration 610）](2026-08-26-v6-request-class-persistence.md) — request_logs 全链路持久化（INSERT $101/$102 + UPDATE $98/$99 + upsert 防回退 + admin 过滤/展示 + fsstore 同构镜像）；顺带恢复 d2cbaf88b 剥离的 POST /providers/{id}/models 405 修复、修复 fsstore GetRequest 索引分词/时区 bug、隔离四个腐烂 admin 测试恢复包可编译；离线 SQL 契约测试钉死占位符对齐
- [2026-08-26 · V6-W1.7 双后端队列（内存|Redis）集群准入](2026-08-26-v6-w1-7-dual-backend-queue.md) — U1~U7 落地：QueueBackend 接口 + local pass-through（等价门禁零测试修改）+ Redis 实现（Lua 原子准入 / 心跳 stale 30s 崩溃自愈 / due ZSET / fail-open 降级）+ 管线五类站点接入 + 组合根 `LLM_GATEWAY_DISPATCH_QUEUE_BACKEND=auto|local|redis` + 指标告警；双实例 miniredis 集成测试验证跨实例 cap/释放再准入/断连 fail-open
- [2026-08-26 · V6-W1.6 IR 请求类型 + 执行轨迹队列 + 调度解耦](2026-08-26-v6-w1-6-ir-class-journal-planner.md) — T1~T7 全部落地：ir.RequestClass/DueAt（零泄漏）、AttemptJournal（容量 128，6 站点统一 recordDecision，**轨迹附属请求自身**——范围修正：不进分维索引，读面 JournalSnapshot()）、分维索引仅增 Class + `/api/admin/dispatch/request-dimensions/{id}` 归属查询、planner 纯决策层（等价门禁：dispatch 全量 -race 零测试修改通过）、100 限额收敛 AttemptBudgetLeft；冻结契约 fixture 回归通过
- [2026-08-26 · V6-W1.5 调度执行器闭环](2026-08-26-v6-dispatch-executor-loop.md) — G-Ⅰ~G-Ⅵ：执行器 CPU 自适应、定时请求（X-Gw-Due-At + 到期堆）、DispatchNotice think 通知、回队打标 LastFailover、DimensionIndex 分维队列、换模型/容量等待信号
- [2026-08-25 · pms-redis 大数据审计 + 综合优化方案（ICR-245-A5）](ICR-20260825-redis-audit-optimization.md) — 子代理 A/B/C/D 合并报告，P0×5 + P1×5 + P2×7；本批已落地 P0-2/P0-4/P2-1 + 子代理压缩修复，build_seq 1736 部署验证通过
- [2026-08-24 · V6-W0 (结构 / 门禁) 波次激活](2026-08-24-v6-w0-wave-activation.md) — 基线盘点完成，9 个必做子任务分配建议分支，代码拆分在独立 worktree 推进

