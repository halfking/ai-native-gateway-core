# domains/session/preprocess

会话优化 v4 / FR-11（R11.1–R11.9）的**唯一会话预处理所有者**：会话语义三级制品
（Raw → Sanitized → Compressed）的类型、复用判定、存储与 Hook 状态机。

本包为 T11 的 **lite 版**：不接生产管线、不 import sanitizer/compressor 实现本体，
生成逻辑通过 `ArtifactBuilder` 接口由集成者注入。

## 结构

| 文件 | 职责 |
|---|---|
| `types.go` | R11.3/R11.4 契约类型（逐字段照抄规格）：ArtifactKind/Status/Flags、SessionRevision、ArtifactMeta/Manifest、MutationKind、TurnArtifactBlock、BodyRefHash、TransformReceipt |
| `deps.go` | R11.5 三层依赖哈希纯函数（版本号显式字符串输入，sha256 汇总） |
| `store.go` | `SessionArtifactStore` 接口（R11.7 签名照抄）+ StoreOptions + 哨兵错误 |
| `redis_store.go` | Redis 实现：tenant-scoped 键 `session:artifact:v1:{tenantHash}:{sessionHash}:{kind}:{variant}`；Lua compare-and-set（revision 三元组比对 + 空值 HDEL 不留残值）；`SET NX PX` 构建租约（可释放）；InvalidateFrom 按依赖图只改 manifest flags/meta.status 不删正文；Raw payload AES-256-GCM envelope（AAD=tenant+session+kind，错误 key/篡改密文解密必败）；Sanitized/Compressed 明文或可选 gzip；TTL 默认 2h 每层独立可配 |
| `lru.go` | L1 进程内 byte-bounded LRU：所有 `[]byte` 深拷贝，更新后重跑 byte-budget 淘汰 |
| `hook.go` | `SessionPreprocessHook`（R11.2 三方法）默认实现 + `ArtifactBuilder` 注入接口 + 复用四条件（ReuseCheck）+ singleflight（vendored `golang.org/x/sync/singleflight`）合并并发构建 |
| `turn_block.go` | TurnArtifactBlock 追加/查询：同轮多次重试只追加 attempt/status 元数据，不复制六类正文 |
| `events.go` | R11.9 观测事件（raw_hit/build、sanitize_hit/build/degraded、compress_hit/build/variant_miss、lease wait、CAS conflict、bytes/tokens saved）——只含 hash 前 8 字节/计数，无正文无敏感映射；经 `EventSink` 接口发出 |

## 复用判定（R11.4，四条件缺一不可）

```
ReadyBit==1 && StaleBit==0 && SourceRevision==当前修订
  && DependencyHash==当前依赖哈希 && ContentHash 校验通过
```

任一不满足即重建；`building` 经 Redis lease（跨进程）+ singleflight（进程内）合并，
lease 等待方轮询 manifest 直到胜者完成，拿到结果不重复消耗。

## 修订状态机（R11.6）

1. `Prepare`：按 manifest + 四条件判定复用或懒生成，产出 **provisional** revision
   （TurnNo+1、HeadRequestID=本次请求、ChainHash=Advance(prev, deltaHash)），**不推进**持久修订；
2. `CommitFirstForward`：仅 `beginUpstreamAttempt` 且 `AttemptNo==1` 时 CAS 提交
   provisional→definitive；重试/换节点复用同一 Prepared bundle；重复提交幂等；
   CAS 冲突 reload 后 **rebase 到新修订之上，绝不覆盖**；
3. `AppendTerminal`：终态落库后追加 assistant delta、推进 chain hash，
   使 Sanitized/Compressed stale（只改 manifest，不删正文）。

mutation 四类型：`append_delta / replace_snapshot / reset / attachment_only`；
snapshot 被当 delta 追加直接拒绝（ErrSnapshotAsDelta）。

## 集成接线点（由集成者完成）

- **ArtifactBuilder**：注入适配器，Raw 侧接 canonicalizer，Sanitized 侧复用
  `security/sanitize`（placeholder/restore/context bridge），Compressed 侧复用
  `domains/hooks/compression` 的 `SessionCompressor.Prepare/CommitFinal` 思路；
- **EventSink**：桥接到 `internal/liveactions` 的 request_id 键控 action 流与 journey 事件
  （本包不 import liveactions/journey，防止环）；
- **挂点**：`Prepare` 在 ChatHandler 会话装载后、压缩 seam 前；`CommitFirstForward` 在
  executor `beginUpstreamAttempt(AttemptNo==1)`；`AppendTerminal` 在终态持久化成功后；
- **kill-switch**：现有 direct path 保留——不装配本 Hook 即回到旧行为；
- 已知前置修复（不在本包内）：sanitize Redis key tenant scope v2、placeholder offset
  HINCRBY 原子化、`session_summaries` tenant scope（R11.8 P0）。

## 测试

`go test -race ./domains/session/preprocess/...`（miniredis 跑 store/lease/CAS；
FastForward 测 TTL；fake clock 测 lease 等待）。覆盖测试方案 G7 的 UT-SA-01..14。
