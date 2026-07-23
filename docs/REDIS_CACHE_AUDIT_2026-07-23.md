# Redis 缓存梳理分析报告

**日期**: 2026-07-23
**Redis 实例**: 172.16.2.210:6389 (内网，db0=328k, db1=19k, db9=24)
**总占用**: ~489MB / 14.84GB 系统内存 (3.2%)

## 业务模块与 Redis 缓存对应表

### A. 实时流/统计 (~3.5k keys, 0.5%)
| Prefix | Keys | 必要性 | TTL | 替代方案 |
|--------|------|--------|-----|---------|
| `llmgw:live:main` | ~200 | 必需 | 2h | 实时流主队列 |
| `llmgw:live:dim:{vendor\|provider\|model}:*` | ~30 | 必需 | 2h | 实时流泳道 |
| `llmgw:live:tenant:*:dim:*` | ~30 | 必需 | 2h | 租户维度 |
| `llmgw:live:req:*` | ~600 | 必需 | 2h | 请求详情 |
| `llmgw:live:activity:*` | ~50 | 必需 | 4h | 活跃度 |
| `llmgw:stats:delta:tenant:*` | ~1000 | ⚠️ 过多 | 30d | **可优化** |
| `llmgw:stats:delta:global:*` | ~1000 | ⚠️ 过多 | 30d | **可优化** |
| `llmgw:stats:baseline:tenant:*` | ~50 | 必需 | 1d | 基线数据 |
| `llmgw:stats:rebuild:last:*` | ~5 | 必需 | 永久 | 重建锁 |

### B. Session 管理 (~319k keys, 91.7%) ⚠️ 最大
| Prefix | Keys | 必要性 | TTL | 替代方案 |
|--------|------|--------|-----|---------|
| `session:gw_*` | ~236k | ⚠️ **过多** | 7d (有 bug 时永不过期) | **必须优化** |
| `session:key:*` | ~54k | 必需 | 7d | 客户端 session_key → sessionID 映射 |
| `session_pref:*` | ~28k | 必需 | 7d | 会话偏好（凭证+模型） |
| `session:apiKey:*:active` | ~100 | 必需 | 7d | 活跃 API key 集合 |
| `session:stopped:{tenant}` | ~50 | 必需 | 24h | 已停止会话索引 |

### C. 异步响应缓存 (~25k keys, 7.3%)
| Prefix | Keys | 必要性 | TTL | 替代方案 |
|--------|------|--------|-----|---------|
| `pending_response:{sid}:{rid}` | ~12k | 必需 | 7d | 异步响应缓存 |
| `pending_response:index:{sid}` | ~12k | 必需 | 7d | 索引 ZSET |
| `pending_response:{sid}:*` (其他) | ~1k | 必需 | 7d | 关联数据 |

### D. 凭证指纹/槽位 (~200 keys, 0.05%)
| Prefix | Keys | 必要性 | TTL | 替代方案 |
|--------|------|--------|-----|---------|
| `llmgw:tenant:*:sess_cred_fp:*` | ~80 | 必需 | 7d | 凭证指纹 |
| `llmgw:tenant:*:cred_fp_slot:*` | ~10 | 必需 | 7d | 槽位快照 |
| `llmgw:avail:*` | ~150 | 必需 | 30min | 可用性指标 |
| `llmgw:avail:*` (其他) | ~20 | 必需 | 30min | 备份可用性 |

### E. 会话 V2/历史/审计 (~200 keys, 0.05%)
| Prefix | Keys | 必要性 | TTL | 替代方案 |
|--------|------|--------|-----|---------|
| `session:gw_*:cred_rotations` | ~20 | 必需 | 7d | 凭证轮换历史 |
| `llmgw:live:tenant:*:req:*` | ~600 | 必需 | 2h | 租户请求详情 |
| `llmgw:live:activity:tenant:*:*` | ~10 | 必需 | 4h | 租户活跃度 |

## 必要性评估

### ✅ 真正必需的缓存（不可删除）
1. **实时流数据**（llmgw:live:*）：3.5k keys，0.5% 空间，但**用户实时看到**
2. **Session 核心**（session:gw_*, session:key:*, session_pref:*）：319k keys，91.7% 空间，**核心业务**

### ⚠️ 需要优化的缓存
1. **session:gw_*（236k keys）**
   - 业务上：7天累计创建了 23.6 万个 session，平均 ~33,700/天
   - 大部分是 OpenCode 编辑器/Cursor 等客户端频繁创建会话导致
   - **建议**：缩短 TTL 到 1-3 天 + 添加定期清理

2. **llmgw:stats:delta:***（~2000 keys, 30天 TTL）**
   - 业务上：用于增量统计
   - 30天太长，**建议缩短到 1-3 天**

3. **pending_response:***（~25k keys, 7天 TTL）**
   - 业务上：异步响应缓存
   - **建议**：响应完成后立即删除（而非等 7 天）

### ❌ 可以彻底删除的缓存
1. **session:stopped:{tenant}** 中已停止的会话索引：停止即过期（24h）
2. **重复的 session:apiKey:*:active 集合**：可考虑用其他方式代替

## 优化优先级

### 优先级 P0（立即）
1. 修复 HSet 隐式清除 TTL 的 bug（已完成）
2. 清理已存在的 20k 个无 TTL 泄漏 keys（已完成）
3. 部署修复到生产

### 优先级 P1（1周内）
1. 缩短 session TTL 从 7天到 1-3天
2. 缩短 stats delta TTL 从 30天到 3天
3. pending_response 响应完成后立即删除
4. 添加定期清理 job 防止泄漏

### 优先级 P2（长期）
1. 减少 Session 重复创建（前端复用 session_id）
2. 添加 Prometheus 监控：Redis 内存、key 数量、TTL 泄漏
3. 考虑 Redis Cluster 拆分 session / live_stream / stats

