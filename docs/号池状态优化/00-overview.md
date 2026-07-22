# 00 — 概述与决策记录

## 1. 背景

当前生产路由热路径大致为：

1. `provider.Client`：模型解析 + DB 候选 + 30s 内存缓存
2. `streaming/executors.Router`：`credentialstate` 过滤、P2C/Bandit、tier、billing-round、sticky、降级
3. `Executor`：FP slot / concurrency / RPM / circuit、上游调用、失败切换
4. 结果回写分散在 `credentialstate.Manager`、探测 worker、半成品 URSM 与 `routingstate` 影子观测

主要问题：

- 状态读写分散，跨实例难以及时共享完整号池画像
- `credentialstate` 同时承担缓存、DB 批写、探测触发、候选失效，职责过重
- `domains/ursm` 已有四层状态骨架，但缓存 miss **fail-open**，`Enabled()` 语义过宽
- 路由排障缺少统一 debug 链路与分钟级复盘

## 2. 目标

1. 独立号池状态模块（URSM v2），统一管理模型/节点/评分/延迟/成功率/计费/限额/价格/可信度/并发/指纹/多窗口成功率等
2. Redis 作为**运行时权威**状态池；原子更新；多维度快速检索与更新 API
3. 滑动窗口多池共存（1m / 5m / 30m）
4. 每分钟持久化快照到 PostgreSQL，用于复盘与 Redis 恢复回填
5. 丰富请求全链路 debug 日志，尤其路由过滤/评分/切换原因
6. 将原路由内散落的状态管理收敛到统一模块（单一原则、统一接口、闭包装配）

## 3. 非目标（第一阶段）

- 不直接全量切换生产路由
- 不重写 Executor 候选循环与上游协议转换
- 不引入 Redis Cluster / Sentinel
- 不把探测 HTTP 执行器并入状态模块（只接收 `ProbeOutcome`）
- 不强制第一阶段把进程内 concurrency 全量改为 Redis（接口预留）
- 不删除 `provider` 30s 配置候选缓存
- 不在本阶段交付完整 WRR/水位大调度重写

## 4. 成功标准

| 项 | 标准 |
|---|---|
| 权威 | 热路径可用性裁决不读 DB 状态表 |
| 安全 | Redis 读失败 → 候选保护性拒绝 |
| 恢复 | Redis 重启 → ready=0 阻塞 → 预热成功后放行 |
| 控制 | 人工 disable 不可被 request/probe 自动解开 |
| 一致 | 请求结果同步进窗口与 node 摘要；审计异步 |
| 复盘 | 每分钟快照可查，可关联 routing_attempts |
| 发布 | shadow 差异指标齐全；可一键 `mode=off` 回退 |
| 性能 | 中等规模下路由附加开销 P95 ≤ 15ms；状态读 P95 ≤ 5ms |
| 回归 | FP slot / RPM 现有 Lua 语义测试不回归 |

## 5. 决策记录

| ID | 决策 | 选择 | 原因 |
|---|---|---|---|
| D1 | 状态权威 | Redis 运行时权威 | 跨实例即时共享；DB 作配置/审计/快照 |
| D2 | Redis 读失败 | 保护性拒绝 | 避免 fail-open 把故障凭据重新打回上游 |
| D3 | 无 key | 首次请求懒初始化 | 须先过 DB 硬门 + Redis 原子 NX |
| D4 | 粒度 | 五层含资源池 | 健康态与租约分 key，统一观测 |
| D5 | 部署 | 单实例 AOF | 贴合现状；Lua 跨 key 安全 |
| D6 | 丢失恢复 | 阻塞至预热完成 | 空库不可伪装健康 |
| D7 | 规模 | 中等 | pipeline + ZSET + 索引即可 |
| D8 | 并发裁决 | 来源优先 + CAS | manual > probe > request |
| D9 | 人工禁用 | 仅显式 enable | 运维意图不被自动逻辑绕过 |
| D10 | 结果写 | 同步 Redis / 异步审计 | 下一请求立即可见 |
| D11 | 上线 | 影子后灰度 | 可回退 |
| D12 | 架构 | URSM 内演进六组件 | 复用注入点，重做权威语义 |

## 6. 与历史文档关系

| 已有材料 | 关系 |
|---|---|
| `docs/号池优化/*` | 偏调度（WRR/水位）；本设计聚焦状态池权威与接口 |
| `docs/ursm-routing-redesign/*` | 方向一致；本设计修正 fail-open、Enabled、plan 期 acquire 等问题 |
| `docs/2026-07-15-routing-state-probe-capability-unification.md` | Coordinator 影子证据协议可复用并升级 |
| `docs/自检优化/00-实时号池状态与统一自检探测方案.md` | 探测队列独立；本模块只消费探测结果 |

## 7. 风险摘要

| 风险 | 缓解 |
|---|---|
| Redis 成为路由 SPOF | AOF + 恢复演练；紧急人工 `mode=off` |
| 影子双写分叉 | 旧路径不读 URSM；diff 指标；权威前停旧写 |
| 懒初始化并发 | NX + singleflight；配置同步预热优先 |
| 窗口内存 | TTL 略大于窗口；索引驱动扫描，禁止 KEYS |
| 与 fp node 健康重复 | 收敛到 URSM node；fp 包仅保留租约 |
