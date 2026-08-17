# 2026-07-23: Redis 缓存 TTL 精细化分层

## 背景

之前的 `348,148` 个 Redis keys 中，318,931 (91.7%) 来自 session 业务，平均存活 7 天但很多从未被访问。
本次优化按业务生命周期的精细化分层 TTL，减少不必要的内存占用。

## 改动

### Live Stream 缓存（最核心改动）

| Key 类型 | 之前 | 现在 | 业务依据 |
|---------|------|------|---------|
| 请求详情 (`live:req:*`) | 2h | **4h** | 一般请求 4h 内完成 |
| 泳道维度队列 (`live:dim:*`) | 2h | **24h** | 泳道存在不超过 1 天 |
| 泳道活跃度 (`live:activity:*`) | 2h | **1h** | 1h 无变化应清除 |
| 主队列 (`live:main`) | 2h | **2h** | 保持 |
| 租户集合 (`live:tenants`) | 2h | **2h** | 保持 |

### Session 缓存

| Key 类型 | 之前 | 现在 | 业务依据 |
|---------|------|------|---------|
| session hash | 7d | **3d** | OpenCode/Cursor 编辑场景 3d 足够 |
| session:key 映射 | 7d | **3d** | 与 session 同步 |
| session_pref | 7d | **3d** | 与 session 同步 |
| session:apiKey:active | 7d | **3d** | 与 session 同步 |
| title cache | 7d | **3d** | 与 session 同步 |

### Stats 缓存

| Key 类型 | 之前 | 现在 | 业务依据 |
|---------|------|------|---------|
| stats baseline | 7d | **1d** | 1d 覆盖 6 次 4h 重建周期 |
| stats board | 7d | **1d** | 同上 |
| stats baseline meta | 永不过期 | **1d** | 与 baseline 同步 |
| stats board meta | 永不过期 | **1d** | 与 board 同步 |
| stats dirty set | 6h | **30min** | 重建触发窗口 30min 足够 |
| stats rebuild lock | 30min | **30min** | 已合理 |
| stats rebuild:last | 永不过期 | **7d** | 防止永不过期 key |

### 异步响应

| Key 类型 | 之前 | 现在 | 业务依据 |
|---------|------|------|---------|
| pending response | 7d | **1h** | LLM 响应客户端重试窗口 |
| pending response index | 7d | **1h** | 与 entry 同步 |

## 验证

- ✅ 编译通过
- ✅ 老 keys 通过 SSH 在 154 服务器上批量缩短 TTL
- ✅ 删除了 19,601 个永不过期的泄漏 keys  
- ✅ 已部署到 245（最终验证环境）

## 后续

- 监控:`db0: keys_with_ttl=-1` 告警阈值设为 100
- 监控:`db0: used_memory > 600MB` 告警
- 长期:考虑 Redis Cluster 拆分 hot/cold 数据
