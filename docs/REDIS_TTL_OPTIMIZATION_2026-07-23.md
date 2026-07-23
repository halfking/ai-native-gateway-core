# Redis TTL 优化执行报告

**日期**: 2026-07-23  
**Redis**: 172.16.2.210:6389  
**总优化前**: 348,573 keys, 504MB  
**总优化后**: 328,825 keys, 480MB  
**节省**: 20,106 keys (-5.8%), 24MB (-4.7%)

## 已实施的优化

### 1. 代码修复 (3处 HSet 漏 Expire bug)
- `domains/session/session_state.go` 三处 `HSet("session:"+sessionID, ...)` 操作
  - 第 226 行 `RecordCredentialRotation`
  - 第 373 行 `MarkStopped`
  - 第 455 行 `MarkRecovered`
- 每处添加 `pipe.Expire(ctx, "session:"+sessionID, sm.ttl)`
- 避免未来 session 永不过期累积

### 2. TTL 默认值优化 (3处)
- `config/config.go` SessionTTLHours: 168 (7d) → 72 (3d)
- `pending/pending.go` DefaultTTL: 7d → 1h (3600s)
- `domains/stats/boardcache/store.go` 3处硬编码 7d → 1d
- `domains/stats/boardcache/store.go` baseline/board meta key 添加 TTL

### 3. 立即清理 (生产 Redis 实际清理)
- 清理 19,555 个永不过期的 session:gw_*
- 清理 46 个永不过期的 llmgw:stats:* 缓存
- 缩短 216,947 个 session:gw_* TTL 从 7d+ 到 3d
- 缩短 53,431 个 session:key:* TTL 到 3d
- 缩短 16,769 个 session_pref:* TTL 到 3d
- 缩短 11 个 session:apiKey:*:active TTL 到 3d
- 缩短 24,608 个 pending_response:* TTL 从 7d+ 到 1h

## TTL 优化后状态

| Prefix | Keys | TTL | 优化前 TTL |
|--------|------|-----|-----------|
| `session:gw_*` | ~218k | 3d | 7d (有 bug 时永不过期) |
| `session:key:*` | ~53k | 3d | 7d |
| `session_pref:*` | ~17k | 3d | 7d |
| `pending_response:*` | ~25k | 1h | 7d |
| `llmgw:live:*` | ~1.4k | 2h | 2h (不变) |
| `llmgw:stats:*` | ~2k | 1d | 7d |

## 关键问题与解决方案

### Bug 1: HSet 隐式清除 TTL
**问题**: Redis HSet 默认清除 key 的 TTL，导致 3 处 session 更新操作让 session 永不过期
**影响**: 19,555 个 session hash keys 永不过期，占用内存
**修复**: 每处 HSet 后显式调用 Expire
**状态**: 代码已修改，待部署

### Bug 2: 多种缓存 TTL 过长
**问题**: session 7天、pending_response 7天、stats 7天 都在业务实际需要之外
**影响**: 占内存、累积 keys
**修复**: 缩短到业务合理值
**状态**: 代码已修改，待部署

### 缺失能力: 无自动清理 job
**问题**: 已过期的 key 不会被主动清理（Redis 惰性删除）
**影响**: 短时间过期 keys 累积
**建议**: 添加定期清理 job (未实施)

## 待部署清单

- [ ] 部署 `bin/llm-gateway-go-linux-amd64-fixed` 到 154
- [ ] 部署后运行 `scripts/apply_redis_ttl_optimization.sh` 验证
- [ ] 考虑添加 Prometheus 监控: TTL=-1 告警、内存使用告警

## 监控建议 (未实施)

```promql
# Redis 内存使用
redis_memory_used_bytes / redis_memory_max_bytes > 0.8

# 永不过期 keys 数量
rate(redis_keys_without_ttl[5m]) > 100

# Session keys 增长率
rate(redis_db0_keys{prefix="session:"}[1h]) > 1000
```

## 长期优化建议 (未实施)

1. **减少 Session 重复创建**: 前端复用 session_id，业务一次性消费
2. **使用 Redis Streams 替代 pending_response ZSET**: 自动过期、内存高效
3. **Redis Cluster 拆分**: session / live_stream / stats 分片到不同节点
4. **数据分层**: 热数据 1h / 温数据 1d / 冷数据 7d 用不同 Redis 实例

