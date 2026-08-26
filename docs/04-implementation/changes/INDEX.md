# 04-implementation/changes · 索引

> 最后更新：2026-08-27 06:30:00

## 2026-08

- [2026-08-27 · V6-W1.6 IR 请求类型 + 执行轨迹队列 + 调度解耦](2026-08-27-v6-w1-6-ir-class-journal-planner.md) — T1~T7 全部落地：ir.RequestClass/DueAt（零泄漏）、AttemptJournal（容量 128，6 站点统一 recordDecision）、分维索引 Class/Journal 快照复用 + `/api/admin/dispatch/journal/{id}`、planner 纯决策层（等价门禁：dispatch 全量 -race 零测试修改通过）、100 限额收敛 AttemptBudgetLeft；冻结契约 fixture 回归通过
- [2026-08-27 · V6-W1.5 调度执行器闭环](2026-08-27-v6-dispatch-executor-loop.md) — G-Ⅰ~G-Ⅵ：执行器 CPU 自适应、定时请求（X-Gw-Due-At + 到期堆）、DispatchNotice think 通知、回队打标 LastFailover、DimensionIndex 分维队列、换模型/容量等待信号
- [2026-08-25 · pms-redis 大数据审计 + 综合优化方案（ICR-245-A5）](ICR-20260825-redis-audit-optimization.md) — 子代理 A/B/C/D 合并报告，P0×5 + P1×5 + P2×7；本批已落地 P0-2/P0-4/P2-1 + 子代理压缩修复，build_seq 1736 部署验证通过
- [2026-08-24 · V6-W0 (结构 / 门禁) 波次激活](2026-08-24-v6-w0-wave-activation.md) — 基线盘点完成，9 个必做子任务分配建议分支，代码拆分在独立 worktree 推进

