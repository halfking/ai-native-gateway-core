# 并发安全与资源管理审计摘要（来自 agent_15ad6055）

**整体并发安全等级**: **B+** (良好，但存在若干需要修复的问题)

## P1 问题（应尽快修复）

### P1-1: rows.Close() 未使用 defer ⚠️ **高风险**
- **位置**: 15+ 文件（bg/auto_route_affinity_worker.go 等）
- **问题**: 数据库连接可能泄漏
- **影响**: 生产环境长时间运行可能耗尽数据库连接
- **修复**: 所有 `rows.Close()` 改为 `defer rows.Close()`

### P1-2: proxy.Manager 缺少并发写保护
- **位置**: `proxy/manager.go:516-546`
- **问题**: `updateNodeInCache` 修改 slice 时未加锁
- **影响**: 健康检查并发时可能读到不一致状态
- **修复**: 添加 per-subscription 的 RWMutex

### P1-5: HTTP Transport 缺少关键超时配置
- **位置**: `proxy/transport.go:95-112`
- **问题**: 缺少 `ResponseHeaderTimeout` 和 `IdleConnTimeout`
- **影响**: 慢速上游可能导致请求永久挂起
- **修复**: 添加 30s ResponseHeaderTimeout 和 90s IdleConnTimeout

## P2 问题（建议修复）

1. **P2-1**: Timer 泄漏风险 - 部分 ticker 未 defer Stop()
2. **P2-2**: sync.Map 无界增长 - 缺少 LRU 或过期清理
3. **P2-4**: HTTP Transport 连接池配置 - 建议添加 MaxIdleConns 等限制
4. **P2-5**: 后台 worker panic 传播 - goroutine 缺少 top-level recover
5. **P2-6**: slice append 无预分配 - 性能优化
6. **P2-8**: HTTP Client 超时配置 - 已正确设置 ✅

## 优秀实践

- ✅ AttemptCommitGate 双锁设计正确
- ✅ SerializedStreamWriter 短写检测（2026-08-28 修复）
- ✅ ConnectionRegistry 超时机制使用 buffered channel 防止泄漏
- ✅ 资源上限控制到位（buffer 64KB, cache 4096 条目）
- ✅ 25 个测试文件使用 race detector

## 统计

| 优先级 | 问题数 | 关键修复 |
|--------|--------|---------|
| P0     | 0      | -       |
| P1     | 5      | 3个必须修复 |
| P2     | 8      | 建议修复 |
| P3     | 1      | 低优先级 |

## 快速修复代码

### 修复 P1-1 (rows.Close defer)
```go
rows, err := db.Query(...)
if err != nil {
    return err
}
defer rows.Close()  // ✅ 确保释放

for rows.Next() {
    // ...
}
return rows.Err()
```

### 修复 P1-5 (HTTP 超时)
```go
return &http.Transport{
    Proxy:                 http.ProxyURL(u),
    MaxIdleConns:          100,
    MaxIdleConnsPerHost:   10,
    IdleConnTimeout:       90 * time.Second,      // ✅ 新增
    TLSHandshakeTimeout:   10 * time.Second,
    ResponseHeaderTimeout: 30 * time.Second,      // ✅ 新增
    ExpectContinueTimeout: 1 * time.Second,
}
```

---

详细报告：`/Users/xutaohuang/.zcode/cli/agents/sess_15d75c70-8085-4139-9ad4-d71fff7995b2/agent_15ad6055-c0e5-40c6-8bcc-a44cfba98f8d/output.txt`
