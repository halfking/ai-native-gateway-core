# 实时请求流缓存超时后延：审计修复记录

**日期**：2026-07-11  
**范围**：管理端实时请求流 `GET /api/admin/live-stream` 的 snapshot baseline、Redis 空读保护、SSE 订阅生命周期与部署配置。

## 背景

实时请求流通过 Redis 构建每个 tenant/global scope 的 snapshot，并用内存中的
baseline 生成 SSE delta。此前修复已避免 Redis 返回空 snapshot 时覆盖 baseline，
但审计确认仍有几个会导致“泳道清空后重现”或部署行为不一致的缺口。

## 审计发现与修复

### 1. 活跃 SSE scope 被 TTL 回收

**问题**：某 tenant 的 dashboard 保持连接但暂时没有该 tenant 的新请求时，原逻辑
只会在 `computeScopeDelta` 被调用时更新 `lastAccessed`。清理器仍可能在 TTL 后删除
其 baseline，下一条请求只能走全量 delta。

**修复**：清理器从 Hub 已有的 `clients` 注册表派生活跃 scope 集合。活跃 SSE client
对应的 scope 不参与 eviction；没有订阅且超过 TTL 的 scope 仍被回收。没有增加额外的
Redis 心跳或并行订阅状态容器。

### 2. default tenant 产生两套 baseline

**问题**：Redis 将空 tenant 规范化为 `default`，但内存 cache 曾分别以 `""` 和
`"default"` 为 key，导致同一 Redis scope 出现两套 delta baseline。

**修复**：引入包内 `liveStreamScope`，统一生成：

- tenant scope：`scope:tenant:<normalizeLiveStreamTenant(tenantID)>`
- global scope：`scope:super`

该 helper 同时决定 Redis Snapshot 参数、内存 cache key 和活跃订阅 key。

### 3. TTL 与 cleanup interval 配置不联动

**问题**：只设置 `LLM_GATEWAY_LIVE_STREAM_CACHED_TTL` 时，`main.go` 会保留旧的
`10m` cleanup interval，而不是让 interval 跟随新的 TTL。

**修复**：`liveStreamCachedDurationsFromEnv` 先解析 TTL，再以该 TTL 作为 cleanup
变量的 fallback。显式设置 `LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL` 仍会
独立覆盖。

### 4. duration 解析重复

**问题**：`main.go` 内 session-audit 与 live-stream 分别重复解析正数 duration。

**修复**：抽取同包 `positiveDurationEnv`，统一处理缺失、格式错误、零值、负值和
结构化 warning。未复用 `main_pipeline.go` 的 `envDuration`，因为后者的既有语义允许
非正 duration，不能用于本配置。

### 5. 安装器与开发配置来源不同步

**问题**：安装器使用 `installer/cmd/llm-gw-installer/embeddata/env.template`，之前只
更新了 `installer/templates/env.template`，一键安装看不到新配置。

**修复**：同步两个安装器模板、`.env.example` 和 `docs/CONFIGURATION_GUIDE.md`。

### 6. 指标含义不准确

**问题**：旧 `cached_snapshot_hits/misses` 只代表 baseline 是否存在，不能表示 Redis
cache 命中。

**修复**：改为：

- `cached_snapshot_baseline_present`
- `cached_snapshot_baseline_absent`
- `cached_snapshot_empty_skips`
- `cached_snapshot_evictions`
- `cached_snapshot_entries`
- `active_scope_subscriptions`

## 配置

```bash
# Go duration 格式，例如 30s、10m、1h。
LLM_GATEWAY_LIVE_STREAM_CACHED_TTL=10m

# 可选；未设置时跟随 TTL。
LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL=10m
```

## 回归覆盖

新增测试覆盖：

1. 真实 Redis 链路：写入请求 A → 建立 baseline → 删除 tenant main queue 模拟空读 →
   确认 baseline 保留 → 写入请求 B 后正常演进。
2. `""` 与 `"default"` 共用同一个 normalized scope cache key。
3. 真实 `Run()` cleanup ticker：已订阅 scope 不会被回收；注销后会在后续 tick 回收。
4. duration env：缺失、有效、零、负数、非法值、TTL-only 和显式 cleanup 覆盖。

## 验证命令

```bash
gofmt -w admin/live_stream_sse.go admin/live_stream_redis_store_test.go admin/live_stream_sse_audit_test.go cmd/gateway/main.go cmd/gateway/live_stream_config_test.go
go test ./admin/... -count=1
go test ./cmd/gateway/... -count=1
go vet ./admin/... ./cmd/gateway/...
go build ./...
```

## 工程卫生

移除了误提交的 session 专属 `.zcode/plans/plan-sess_*.md`，并新增
`.zcode/plans/` ignore 规则。稳定的设计与审计信息保留在本文件，而不是会话过程产物中。
