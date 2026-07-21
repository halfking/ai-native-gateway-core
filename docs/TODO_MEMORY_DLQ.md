# Memory Service DLQ 实施 TODO

> 创建时间: 2026-07-21
> 关联: `cmd/gateway/main.go:921` `cmd/gateway/main.go:1551`

## 1. 当前状态

Memora 服务（context-compression oracle）的 DLQ（Dead Letter Queue）、
Client()、Sink()、SetDLQ()、SetFallbackCache() 方法**尚未实现**，
main.go 中 TODO 注释占位。

当前实现：仅启用了 Reader() 和 Writer()，
DLQ 相关代码被注释/跳过。

## 2. 待完成的具体工作

### 2.1 实施 memorySvc 接口扩展

```go
// internal/memory/service.go (或类似位置)
type Service interface {
    Reader() Reader
    Writer() Writer
    // 新增：
    DLQ() DLQ
    Client() Client
    Sink() Sink
    SetDLQ(dlq DLQ)
    SetFallbackCache(cache Cache)
}

type DLQ interface {
    Push(ctx context.Context, item FailedItem) error
    Pop(ctx context.Context) (FailedItem, error)
    Size() int
}

type FailedItem struct {
    Request    *Request
    Error      string
    Timestamp  time.Time
    RetryCount int
}
```

### 2.2 持久化层

DLQ 可以基于 Redis 或 DB：
- Redis（推荐）：性能高，自动 TTL
- DB：审计能力强，但需要 schema

### 2.3 Wire 到 main.go

```go
// cmd/gateway/main.go
if memorySvc != nil {
    if dlq := memorySvc.DLQ(); dlq != nil {
        routingExec.DLQ = dlq
        adminHandler.SetDLQ(dlq)
        slog.Info("memora DLQ wired")
    }
    if cache := memorySvc.FallbackCache(); cache != nil {
        routingExec.FallbackCache = cache
        slog.Info("memora FallbackCache wired")
    }
}
```

## 3. 验收标准

- [ ] memorySvc 接口扩展完成
- [ ] Redis-based DLQ 实现
- [ ] main.go wire DLQ
- [ ] 单元测试：DLQ push/pop/超时
- [ ] 245/154 环境端到端：模拟 memora 故障 → 落入 DLQ → 重试成功

## 4. 优先级

**Medium** — DLQ 是容错能力，影响 Memora 服务故障时的请求可靠性。
需要 1-2 个独立迭代完成。

## 5. 依赖

依赖 Memora 团队提供 DLQ 接口规范（如果 Memora 是外部服务）。
如果是内部实现，可由 gateway 团队直接开发。
