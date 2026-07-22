# 号池状态优化（URSM v2）— 设计索引

> 日期：2026-07-21  
> 状态：设计已确认，待实施计划  
> 范围：将路由运行时状态管理独立为 Redis 权威的号池状态模块

## 文档清单

| 文档 | 内容 |
|---|---|
| [00-overview.md](./00-overview.md) | 背景、目标、约束、非目标、决策记录 |
| [01-architecture.md](./01-architecture.md) | 六组件、五层状态、对外接口、代码对接 |
| [02-redis-protocol.md](./02-redis-protocol.md) | Redis key、Lua CAS、滑动窗口、资源池 key |
| [03-hotpath-and-logging.md](./03-hotpath-and-logging.md) | 请求热路径、过滤评分、结果回写、debug 日志 |
| [04-recovery-persist-rollout.md](./04-recovery-persist-rollout.md) | 恢复预热、分钟快照、灰度回退、迁移、验收 |

## 一句话结论

在 `domains/ursm` 内演进 **URSM v2**：Redis 作为运行时状态权威，PostgreSQL 保存配置硬门、审计与分钟快照；Router/Executor 保留现有骨架，经 `off → shadow → canary → authoritative` 接入；请求结果同步写 Redis，审计异步落库。

## 已锁定决策

1. 状态权威：Redis 运行时权威  
2. Redis 读失败：保护性拒绝候选  
3. 无状态记录：首次请求懒初始化（仅当 DB 配置硬门通过）  
4. 状态粒度：五层（provider / credential / binding / node / resource）  
5. Redis 部署：单实例高持久（Redis 7 + AOF）  
6. 状态丢失恢复：阻塞路由直到预热完成  
7. 规模：中等（约 100–1k credential，1k–10k 节点，2–10 实例）  
8. 并发裁决：来源优先 + CAS（manual > probe > request）  
9. 人工禁用：仅显式 enable 可恢复  
10. 结果写入：同步 Redis，异步审计  
11. 上线：影子对比后灰度  
12. 架构：URSM 内演进 + 六内部组件  

## 实施入口

设计审阅通过后产出分步实施计划；代码默认落在 `domains/ursm/`，文档与 runbook 继续沉淀本目录。
