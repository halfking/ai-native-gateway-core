# Request-Detail 跨进程共享与多副本可见性策略

**日期**: 2026-08-28  
**作者**: Request-Detail 审计闭环第三轮  
**状态**: Recommendation（待决策）  
**关联文档**: `request-detail-performance-audit-20260828.md`

## 1. 背景

Request-Detail 主流程在请求生命周期内维护两层 **节点本地** 缓存：

| 层 | 介质 | 位置 | 生命周期 |
| ---- | ---- | ---- | ---- |
| L1 Memory | 进程内 `map[string]Meta` | 进程堆 | `PutMeta` → `Clear` |
| L2 File | `{dir}/{request_id}.json` | `LLM_GATEWAY_REQUEST_DETAIL_DIR`（默认 `os.TempDir()/llmgw-request-detail`） | `PutBodies` → `Clear` 或 TTL/LRU 驱逐 |
| L3 DB | `request_logs` / `session_turns` | PostgreSQL（**唯一全局源**） | 持久化后由 `ClearAfterPersist` 触发 L1/L2 清理 |

**当前部署假设**: 单进程或 sticky-session（同一请求的所有读写都路由到同一节点）。

**当前痛点**（来自 `docs/implementation/request-detail-performance-audit-20260828.md` 第 7 节阻塞项）:

> 本地 /tmp 是节点局部；多副本部署下 in-flight detail 不会跨节点可见，需确认业务侧是否依赖跨节点查询。

具体表现:
1. **请求落在节点 A**, telemetry `onEmitted` 在 A 上完成 L1/L2 写入。
2. **用户在节点 B 打开 detail 页**: B 的 L1 miss（无内存条目）、L2 miss（`/tmp` 是 B 自己的 `os.TempDir()`）。
3. **B 走 L3 DB 回退**: `request_logs` 行未必已持久化（DB write 是异步的，且 PG 复制到 reader 可能滞后），导致 admin 看到 "metadata-only fallback" warning 或 404。
4. **同一客户端的不同 admin 用户可能跨副本轮询**, 在 persist 完成前的窗口内会出现间歇性 404。

## 2. 候选方案

### 方案 A — 共享存储卷（Shared Volume）

将所有副本的 `LLM_GATEWAY_REQUEST_DETAIL_DIR` 指向同一网络文件系统（NFS / EFS / Azure Files / CephFS）。

- **优点**:
  - 实现最小改动: 只改环境变量,代码不变。
  - 复用现有 file-backed Store（含 TTL/LRU/锁）。
- **缺点**:
  - **节点本地 inode 限制**: NFS 客户端对每个文件句柄有内核级缓存，且 `flock`/`fcntl` 不跨节点。需要重新设计锁（目前 `Store.mu` 是进程内 sync.RWMutex,跨进程完全失效）。
  - **写入延迟抖动**: NFS 在 1ms ~ 50ms 之间随机抖动；当前 `captureForwarder` 的非阻塞假设仍然成立,但读路径（GetFile）会同步阻塞。
  - **元数据一致性**: rename 在 NFS 上是 "重命名协议" 而非 "本地原子";多个副本同时 Put 同一 request_id 可能产生半发布状态（write-then-rename 的 visibility window 变长）。
  - **运维复杂度**: NFS quota / consistency / stale handle 监控需要单独接入。
- **建议**: **不推荐**。与本仓库 "本地 fast-path" 的架构意图相悖，且把 L2 改造为分布式文件系统背离了 L1 → L2 → L3 的清晰分层。

### 方案 B — Sticky / 一致哈希路由

用请求 ID（或其 hash）作为路由 key，让同一 request_id 的所有读写都打到同一副本。

- **优点**:
  - **零代码改动**: L1/L2 仍然是节点本地,只要路由器保证同 key 同节点。
  - 副本之间完全独立,失败域小。
- **缺点**:
  - **需要稳定的路由层**: 现有 LB（猜测为 nginx / envoy / aliyun SLB）必须支持 consistent hashing by header 或 path param;若用 round-robin 则需要先在 LB 上做改造。
  - **rehash 成本**: 节点扩缩容时,被 rehash 的 request_id 在窗口内仍然会跨节点查询。
  - **跨副本 admin UI 不会消除**: 同一个 admin 实例可能被 LB 路由到不同副本,只在同一副本上的同一 session 内才能保证 sticky。
- **建议**: **作为中期推荐**。前置条件: LB 层改造（k8s Ingress `sessionAffinity: ClientIP` 或 envoy `use_hash_policy`）。落地成本低,且与 "本地 fast-path" 架构兼容。

### 方案 C — Redis-backed 共享层

把 L1 + L2 合并为一个 Redis-backed Store（key=`request_id`，value=JSON payload），副本之间共享。

- **优点**:
  - **真跨副本**: Redis 是 single-writer / multi-reader,无需 sticky 路由。
  - **TTL 统一**: 不再需要每副本独立 TTL/LRU 驱逐。
  - **可观测性**: Redis 自身的 hit/miss/evict 指标可直接对接 Prometheus。
- **缺点**:
  - **重构 Store**: 当前 `Store.mu` / `captureForwarder` / `GetFile` 都假设本地 I/O。需要新增 `RemoteStore` 接口（`Get`/`Put`/`Clear`）并把 redis 客户端注入。
  - **延迟变化**: Redis 一次往返 0.5~2ms,比本地 `/tmp` 慢约一个数量级。但仍然在 admin UI 可接受范围（< 5ms 阈值）。
  - **依赖扩展**: 引入 Redis SPOF。需要集群 + 哨兵或云托管 Redis,与现有 PostgreSQL 主备并列运维。
- **建议**: **作为长期推荐**。是真正解决跨副本可见性的方案。改造应分两步:
  1. 抽象 `Store` 为接口,保留现有 `LocalStore` 与新增 `RedisStore`。
  2. 通过 `LLM_GATEWAY_REQUEST_DETAIL_BACKEND=local|redis` 切换,默认保持 local,新部署可显式开启 redis。

### 方案 D — 缩短 persist 窗口（最简妥协）

不解决跨副本问题,但 **缩短 L3 DB 的可见性延迟**,使 L1/L2 miss 的回退路径更可靠。

具体做法:
- telemetry DB write 的 publish 同步化: terminal status → `INSERT INTO request_logs` 走 synchronous 事务,而不是 async batch。
- admin read 在 L1/L2 miss 时,主动等待 up to 500ms 让 writer 完成 (类似 "read-your-writes")。

- **优点**: 改造点少,与现有持久化路径合并即可。
- **缺点**: 仍然不解决 "请求落在 A 节点而 admin 查 B 节点" 的问题。
- **建议**: **作为短期推荐**。可以马上做,且对其他方案无依赖。

## 3. Recommendation

按时间窗给出三阶段建议:

### 短期（≤ 1 周）

采用 **方案 D** 的 read-your-writes 缓解:
- 在 `Locator.Get` 的 L3 DB 回退处加一个可配置 retry: 当 `omitBody=false` 且 L1/L2 都 miss 时,等 100ms 后重试一次 DB 查询。
- 在 `cmd/gateway/main.go` 增加 `LLM_GATEWAY_REQUEST_DETAIL_DB_RETRY` 环境变量（默认 1 次, 0 禁用）。
- 监控指标: 新增 `requestdetail_locator_db_retry_total`,统计 retry 命中/未命中比例。

### 中期（≤ 1 月）

采用 **方案 B** 的 sticky 路由:
- 在现有 LB (k8s Ingress / envoy) 上开启 `sessionAffinity`。
- 路由 key 优先用 `X-LLM-Gateway-Request-ID` header;若缺失则降级为 round-robin。
- 验证: 在 staging 集群跑 mixed-traffic 测试,确认同 request_id 的读写命中率 ≥ 99%。

### 长期（≥ 1 季度）

评估 **方案 C** 的 Redis Store:
- 把 `Store` 抽象为 `interface { GetMeta, GetFile, Put, Clear }`。
- 新增 `RemoteStore` 实现 (使用现有的 `infra/redis` 客户端)。
- 通过 feature flag 灰度: 5% → 25% → 100%。
- 保留 `LocalStore` 作为单进程/单副本部署的 fast-path 选项。

## 4. 决策待确认

- [ ] 业务侧确认: admin UI 在 persist 完成前的窗口期是否会触发"间歇性 404"工单？
- [ ] 运维侧确认: 是否愿意引入 Redis 依赖 (若有则走方案 C;若无则只走方案 B+D)。
- [ ] LB 侧确认: 当前 LB 是否支持 consistent hashing by header / path param?

## 5. 引用

- `domains/requestdetail/store.go` — LocalStore 实现
- `domains/requestdetail/capture_forwarder.go` — 异步 capture
- `domains/requestdetail/locator.go` — L1 → L2 → L3 回退链
- `cmd/gateway/main.go:2585-2609` — `LLM_GATEWAY_REQUEST_DETAIL_DIR` 配置与启动
- `docs/implementation/request-detail-performance-audit-20260828.md` — 前置审计报告
