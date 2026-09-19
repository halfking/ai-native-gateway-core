# 2026-09-19 Sticky 会话负载均衡优化

## 背景与目标

用户需求（2026-09-19）：

1. **会话保持**：一个会话在没有严重错误时应保持在一个节点请求交互；
2. **新会话负载均衡选择**：综合每个节点的处理历史、最近请求时间、并发情况、
   用量余额选择节点，按并发容量拉平各节点请求量，避免上游并发封禁；
3. **节点 sticky 会话计数**：记录 sticky 在每个节点上的会话数，5 分钟滑窗
   （超窗丢弃、窗内全部计数；会话是否完成不可靠判定，以窗口为语义）。

### 审计发现的现状缺口

- `StickyCache.RecordFailure / RecordFailureMultiLevel`（"10s 内 2 连败剥离"）
  **生产零调用**——死代码。sticky 实际只被 TTL、TTFB 失活（>5min 无数据）、
  管理员清理三种方式打断。
- 失败 failover 成功后 `recordStickySuccess` **无条件重写绑定**：一次瞬时错误
  （超时/429）就把会话搬去新节点，破坏 prompt-cache 亲和与"无严重错误保持
  在一个节点"的目标。
- `calculateLoadScore` 没有任何"节点 sticky 会话数 / 最近请求时间 / 余额"
  信号（并发与历史成功率已有：LiveLoad in-flight + quality 分）。

## 方案

### A. 每节点 sticky 会话 5 分钟滑窗（新信号）

- **Redis**（`domains/ursm/v2/cache/sticky_load.go`）：每凭据一个 ZSET
  `ursm:v2:stickyload:{credentialID}`，member=会话标识（L1 sticky key），
  score=最近观察 unix 秒。写路径裁剪→ZADD→EXPIRE；读路径裁剪→ZCARD→
  ZRANGE(-1,-1)=最近活跃。蓝绿双活共享 Redis，计数即跨实例真值。
- **进程内**（`domains/streaming/executors/sticky_load.go`）：本实例内存镜像
  （Redis 不可用兜底）+ activity 时间戳（最近任意成功请求）+ 跨实例快照缓存
  （refresh TTL 3s 节流 + 单飞，热路径永不等待 Redis）。
- 观察时机 = sticky 绑定写入（`recordStickySuccess` 成功路径）：会话持续使用
  持续刷新，空闲超窗自然跌出。会话迁移后旧节点计数最多残留一个窗口（规格容忍）。

### B. 评分接入（`router_scoring.go` calculateLoadScore）

三个新惩罚项（越低越好，P2C 取 min）：

| 维度 | 归一 | 默认权重 | env |
|---|---|---|---|
| sticky 会话数 | 会话数 / 并发容量，夹紧 0..1 | 0.15 | `LLM_GATEWAY_ROUTING_W_STICKY` |
| 最近请求时间 | 距今 30s 线性衰减 1→0，无信号=0 | 0.05 | `LLM_GATEWAY_ROUTING_W_RECENCY` |
| 余额（仅 PAYG） | 低于 $5 水位线性加重，≤0=1 | 0.05 | `LLM_GATEWAY_ROUTING_W_BALANCE` |

- sticky/recency 仅在 `Router.StickyLoad` 接线后非零（未接线时 composite 与
  历史公式逐字节一致，存量测试/旧部署零影响）；
- 容量未知用保守默认（`LLM_GATEWAY_STICKYLOAD_DEFAULT_CAPACITY`=8）；
- 水位线 `LLM_GATEWAY_ROUTING_BALANCE_WATERMARK`（默认 5）；
- recency 地平线 `LLM_GATEWAY_ROUTING_RECENCY_HORIZON_SECONDS`（默认 30）；
- 滑窗 `LLM_GATEWAY_STICKYLOAD_WINDOW_SECONDS`（默认 300）。

`planCandidates` 在可用性过滤后对参与评分的凭据触发一次 `Refresh`（节流）。

### C. 会话保持：瞬时保持 / 严重迁移（executor_dispatch.go + executor.go）

- `dispatchForward` 失败分支记录：sticky 钉扎凭据被尝试且失败时的最终错误
  类型（`dctx.stickyFailed / stickyFailKind`；pipeline 对同一请求的 attempt
  串行，无需加锁）。
- 成功路径 `stickyPreserveBinding(dctx, servedCred)` 决策矩阵：

| 情形 | 结果 |
|---|---|
| 服务节点 = sticky 节点 | 正常刷新绑定 |
| sticky 节点被尝试、失败为**瞬时**（超时/网络/429/过载等非 fatal） | **保持原绑定**（不重写；会话下次仍回原节点，保住 prompt-cache） |
| sticky 节点被尝试、失败为**严重**（`errorsx.IsCredentialFatal`：auth/quota 系列） | 迁移（绑定重写到实际服务节点） |
| sticky 节点未被尝试（被可用性过滤=冷却/熔断） | 迁移 |

- 持续瞬时失败的兜底：节点健康滑窗（FpSlot NodeState 3 连败 300s 冷却 /
  URSM fail_streak）把 sticky 节点过滤出候选 → 走"未被尝试 → 迁移"分支。
  会话不会无限烧首跳。

### D. 观察钩子

- 绑定写入（L1）→ `ObserveSession`（内存同步 + Redis 异步 best-effort 50ms）；
- 每次成功 → `ObserveActivity`（仅内存，recency 信号）；
- ratelimit gate 关闭时全部跳过（与 sticky 本体同 gate）。

## 接线

`cmd/gateway/main.go`：`NewStickyLoadTracker` + （有 Redis 时）
`SetStore(ursmcache.NewStickyLoadStore(...))` → `router.StickyLoad`。
无 Redis 部署（252 形态）退化为纯本实例内存窗口。

## 明确不做

- 不复活 `RecordFailure`"2 连败剥离"作为通用打断：冷却/熔断 + 上述决策矩阵
  已覆盖，且显式避免"瞬时错误打断会话"。
- 不做会话完成判定：以 5 分钟滑窗为语义（用户规格认可）。
- 不跨凭据删除旧计数：迁移会话在旧节点的计数等窗自然跌出。
- balance 惩罚不适用于订阅/免费（billing round 1）凭据。

## 测试

- `domains/ursm/v2/cache/sticky_load_test.go`：ZSET 计数/去重/窗口裁剪/TTL。
- `domains/streaming/executors/sticky_load_test.go`：内存滑窗、activity、
  Redis 快照合并、refresh 节流（快照覆盖内存，不相加）。
- `router_stickyload_scoring_test.go`：三惩罚项归一矩阵 + 零观察时 composite
  与未接线逐字节一致 + 满载节点分数更高。
- `sticky_preserve_test.go`：决策矩阵 + 绑定保持/迁移 + 观察钩子归属。
