# v6-W1.7 · 双后端队列（内存 | Redis）集群准入 — QueueBackend

**日期**：2026-08-27
**分支**：main
**证据等级**：`LOCAL_VERIFIED`（`go build ./...` + `go test -race ./domains/dispatch/`（local 等价 + miniredis 双模）全绿 + 冻结契约 fixture 回归；staging 多实例实测未做）
**Commit / Migration / Flag**：
- commit: 本轮提交
- migration: N/A（Redis 键为运行时自建：hash 族 + due ZSET，TTL 自清）
- flag: `LLM_GATEWAY_DISPATCH_QUEUE_BACKEND=auto|local|redis`（默认 auto：有 Redis 用 Redis，无则 local + WARN）

## 场景与需求范围

用户需求（2026-08-27）："没有 redis 时才会回退到本机内存，有 redis 就用 redis——队列实现两套（内存与 Redis）；多台服务器接收请求时用 Redis 协调。"设计定稿：[`docs/架构优化v6/10-dual-backend-queue.md`](../../架构优化v6/10-dual-backend-queue.md)（U1~U7）。

**架构约束（连接亲和）**：QueuedRequest 携带 goroutine/连接/ResultCh 不可序列化——执行永远在持有客户端连接的实例；Redis 后端只管**跨实例的准入/容量/定时可见**，不做跨实例 work-stealing（durable lane 范畴，另行 ADR）。

## 实现（U1 ~ U7）

| 任务 | 变更 |
|---|---|
| U1 接口 + local 等价实现 | `domains/dispatch/queue_backend.go`（新）：`QueueBackend` 接口（`Kind/Open/Close/TryAdmitTotal/TryReserveLane/Release/ParkDue/ClearDue/Heartbeat/Snapshot`）+ `Admission` token + `localQueueBackend` 纯 pass-through（本地原语仍是唯一准入权威——**行为与现状逐位等价**，全量 dispatch 测试零修改通过） |
| U2 Redis 实现 | `domains/dispatch/queue_backend_redis.go`（新）：每容量族一对 hash（`counts`{instance→计数} + `hb`{instance→心跳 ms}，hash-tag 同 slot）；**Lua 原子准入**（TIME 服务端时钟、sum≥cap 拒绝、HINCRBY 自增、顺带清扫 stale 实例）；**崩溃自愈**：实例停跳 30s 后其计数被任何后续操作清出集群和（D2）；释放 floor-at-0 防负；**fail-open**（D1：≤1 次短重试→仍败→按本地口径放行 + `dispatch_queue_backend_degraded`=1，心跳成功自动回切 D5）；due ZSET `…:due`（member=`{instance}\|{request_id}`，score=due ms；park 写 / 到期与终态删 / 心跳顺带清扫 1h 残留 D8） |
| U3 管线接入 | `queue_admission.go`（新）+ pipeline/forwarder 五类站点：Submit 与到期重入先 `TryAdmitTotal` 后本地 CAS（本地拒→补偿释放）；`enqueueModel`/`enqueueModelFromTotal`（背压环内逐次预留/回退）与 `tryEnqueueCred` 预留 `TryReserveLane`（本地拒→释放 token）；释放点与本地深度归还同位（模型道=runModelDrainer 弹出后、凭据道=forwarder loop/drain 接收后）；全部 totalQueue.release 站点 → `releaseTotal`（本地槽+集群 token 同还）；complete() 清 due + 防御性扫尾（take-once 原子 swap 防双放，invariant 2）；parkScheduledRequest 写 due ZSET。token 存 QueuedRequest 三个 `atomic.Pointer[Admission]` 槽（handoff 前写、取得后释放，-race 干净）；`Pipeline.Stop` 关停后端心跳 |
| U4 组合根 | `cmd/gateway/main_dispatch_backend.go`：`resolveQueueBackend`（auto/local/redis 三态 + 回退 WARN）+ `wireDispatchQueueBackend`；main.go 在 SetQueueMirror 后接线（与 governor 组合根同位；governor 组合根本身未动） |
| U5 指标告警 | `dispatch_queue_backend_{admit,release,rejected}_total{backend,lane}`、`_held{backend,lane}`、`_degraded{backend}`；`deploy/prometheus/rules/dispatch-queue-backend.yml`（降级告警 + 拒绝率告警；集群级 held 对账属 F5） |
| U6 观测边界 | mirror 深度键（既有）与 backend 计数并存：前者是队列深度观测、后者是准入占用权威——08 号 §2.1 已加双后端修订注 |
| U7 文档 | 本 changes 文档 + 10 号证据等级 LOCAL_VERIFIED + 08 号 §2.1 修订 |

## 关键正确性决策

1. **降级方向与 Governor 相反、勿统一**（§2.2）：准入/容量 fail-open（Redis 失联→各实例本地口径放行，保可用）；Governor 限流 fail-closed（保供应商）。代码注释与告警文案均钉死该方向。
2. **local 后端 = pass-through 而非复制本地原语**：Try* 恒 true、Release no-op，本地 totalQueue CAS/lane channel 容量继续作为唯一准入权威——等价门禁零修改通过是唯一可行证明。
3. **Redis 准入是本地原语之前的额外语义**：集群 cap 与本地 cap 同时生效（两个都要过）；本地拒绝后补偿释放集群 token（admit/release 严格对称）。
4. **崩溃自愈不用 key-TTL 而用 hash 字段 + 心跳 stale 判定**：count/hb 分离两 hash（同 hash-tag 同 slot，Lua 内原子），dead 实例 ≤30s 被清出集群和；活实例每次 admit/release/心跳都刷新自身 hb，不会误清。
5. **lane 释放点取"道内驻留"语义**（与本地 depth 归还同位），complete() 防御扫尾兜住取消/关停场景；取消发生在 lane 驻留期间时集群 token 提前归还（本地槽仍在）——方向为集群少记（安全侧），TTL/心跳自愈兜底。
6. **due ZSET 仅观测**（E16）：拾取仍由本实例 promoter，Redis 侧 due 不驱动执行。

## 回归证据

- `go build ./...`；`go test -race ./domains/dispatch/` 全绿（含 W1.6 全部测试——**U1 等价门禁：零测试修改通过**）。
- 后端单测（miniredis）：准入到 cap 拒绝、跨实例共享预算、释放对称 + floor-at-0、**崩溃自愈**（stale 心跳 30s 被清扫）、**fail-open**（Redis 关停→放行 + degraded）、due ZSET 写/删。
- 集成测试：`TestPipelineClusterQueueBackendIntegration`（双 pipeline 共享 miniredis：i1 的 q3 持集群唯一 Tier-0 槽 → i2 跨实例被拒（total_queue_full）→ 取消释放 → i2 再准入通过）+ `TestPipelineQueueBackendFailOpen`（Redis 关停后 Submit 正常完成 + degraded）。
- v4 冻结 fixture（`./test/events/contract/`）回归通过；`restart_semantics_v1` 的 `ordinary_dispatch → dropped` 不变（对象仍不可迁移）。

## 负向测试

- cap=3 拒第 4 个；跨实例共享同一预算；释放后立即可再准入。
- 双重 Release 不致负计数（floor-at-0）。
- dead 实例（心跳 stale 60s）的幻影计数被 admit 顺带清扫，不再挤占预算。
- Redis 全程不可达：Submit 正常完成（fail-open）、degraded=1、Snapshot 报 degraded。
- 未知/超 TTL 请求 due ZSET 清扫。

## 回滚动作

- env `LLM_GATEWAY_DISPATCH_QUEUE_BACKEND=local`（或停 Redis 配置）→ 全部集群逻辑变 no-op，行为回到 W1.6 形态。
- 整体 revert 本 commit：无 schema、无契约变更；Redis 键 TTL 2min 自清。

## 遗留（后续波次）

- F5：跨实例观测聚合（ClusterTotal recording rule + 对账告警）、DimensionIndex/journal 投影边界维持"附属请求"口径。
- 集群多实例 staging 实测（本轮 miniredis 双实例模拟已覆盖语义，未做真多机压测）。
- due 堆容量上界（09 号 E13/F8）与 mirror 深度键/Backend 计数的展示层合并（运营面板）。
