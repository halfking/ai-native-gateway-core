# 245泳道修复 - 增强日志版本部署报告

**部署时间**: 2026-07-21 00:56
**部署版本**: seq=1252, commit=942d2209
**部署耗时**: 26秒

---

## 🎯 本次部署内容

### 核心修复（已部署）
- **commit ff28c65d**: 按活跃度排序维度队列，修复SCAN随机性问题

### 增强日志（新增）
- **commit 942d2209**: 详细记录每次delta推送的完整信息

---

## 📊 新增日志功能

### 1. Delta推送日志
**位置**: `admin/live_stream_sse.go:logDeltaDetails()`

**记录内容**:
- Scope (tenant/super)
- Tenant ID
- 触发请求ID
- Summary统计 (total, success, failure)
- 每个变化泳道的详细信息:
  - 维度/泳道名称
  - 总数 (stats.total)
  - Tile数量
  - 请求ID窗口 (前2个...后2个)

**日志示例**:
```
INFO live stream delta push
  scope=tenant
  tenant_id=default
  trigger_request=req_abc123
  summary_total=45
  summary_success=42
  summary_failure=3
  changed_lanes_count=3
  changed_details=vendor/minimax: total=12 tiles=20 ids=[req_001,req_002...req_019,req_020] | provider/NVIDIA: total=35 tiles=20 ids=[req_101,req_102...req_119,req_120]
```

### 2. 快照构建日志
**位置**: `admin/live_stream_redis_store_snapshot_fix.go:SnapshotFromDimensionQueues()`

**记录内容**:
- Tenant ID / is_super
- 总请求数
- 扫描的维度队列数
- 时间范围 (第一个和最后一个请求的时间戳)

**日志示例**:
```
INFO snapshot from dimension queues built
  tenant_id=default
  is_super=false
  total_requests=156
  dimension_keys_scanned=48
  first_request_ts=2026-07-21T00:45:00Z
  last_request_ts=2026-07-21T00:56:30Z
```

### 3. 维度队列发现日志
**位置**: `admin/live_stream_redis_store_snapshot_fix.go:discoverDimensionQueues()`

**记录内容**:
- Tenant ID / is_super
- 发现的队列总数
- 前3个队列名称（用于验证排序顺序）

**日志示例**:
```
INFO dimension queues discovered
  tenant_id=default
  is_super=false
  total_keys=48
  first_3_keys=llmgw:live:dimension:vendor:minimax, llmgw:live:dimension:vendor:openai, llmgw:live:dimension:provider:NVIDIA
```

---

## 🔍 如何使用日志进行诊断

### 方法1: 实时监控（推荐）

```bash
# SSH到245服务器
ssh -p 25022 root@8.136.114.245

# 实时查看live stream日志
docker logs llm-gateway-go -f 2>&1 | grep -E 'live stream|dimension queue'
```

### 方法2: 使用监控脚本

```bash
# 运行预配置的监控脚本
/tmp/monitor-245-live-stream.sh
```

### 方法3: 分析历史日志

```bash
# 查看最近30分钟的delta推送
docker logs llm-gateway-go --since 30m 2>&1 | grep "live stream delta push"

# 查看特定泳道的变化
docker logs llm-gateway-go --since 30m 2>&1 | grep "vendor/minimax"

# 统计推送频率
docker logs llm-gateway-go --since 30m 2>&1 | grep "live stream delta push" | wc -l
```

---

## 📋 诊断检查清单

### 检查1: 排序是否生效

```bash
# 查看dimension queues discovered日志
# 检查first_3_keys是否每次都一样
docker logs llm-gateway-go --since 10m 2>&1 | grep "dimension queues discovered"
```

**期望**: first_3_keys在多次查询中保持相同顺序

**异常**: 顺序每次都不同 → 排序未生效或Pipeline失败

### 检查2: 请求ID窗口是否稳定

```bash
# 连续查看同一泳道的delta推送
docker logs llm-gateway-go --since 10m 2>&1 | grep "vendor/minimax"
```

**期望**:
- 新请求到达时，ids后面新增，前面保持不变
- 无新请求时，ids完全不变

**异常**:
- ids前后都在变化 → 窗口"滚动"问题仍存在
- ids突然完全替换 → 可能是SCAN顺序变化

### 检查3: 推送频率是否正常

```bash
# 统计推送频率（每分钟）
docker logs llm-gateway-go --since 5m 2>&1 | \
  grep "live stream delta push" | \
  awk '{print $1" "$2}' | \
  cut -d: -f1-2 | \
  uniq -c
```

**期望**: 与实际流量相符

**异常**: 频率过高 → 可能有不必要的推送

### 检查4: 是否有错误或fallback

```bash
# 查找错误和警告
docker logs llm-gateway-go --since 10m 2>&1 | grep -E 'failed to sort by activity|lexicographic order|pipeline exec failed'
```

**期望**: 无错误

**异常**:
- "failed to sort by activity" → sortKeysByActivity()失败，已fallback到字母排序
- "pipeline exec failed" → Redis Pipeline错误

---

## 🎯 验证目标

### 成功标准

通过分析日志，确认：

1. ✅ **排序稳定**: dimension queues discovered的first_3_keys保持一致
2. ✅ **窗口稳定**: 同一泳道的request IDs只在后端新增，不会前后都变
3. ✅ **无滚动**: 没有出现"移除N个，新增N个"的模式
4. ✅ **无错误**: 没有sort失败或Pipeline错误

### 需要进一步分析的情况

如果发现：
- ❌ first_3_keys顺序仍在变化
- ❌ request IDs窗口仍在"滚动"
- ❌ 频繁出现sort失败

则需要：
1. 检查Redis Pipeline性能
2. 分析sortKeysByActivity()的score计算逻辑
3. 考虑是否需要增加缓存或调整算法

---

## 📝 下一步行动

1. **立即执行**: 运行监控脚本，观察30分钟
2. **记录数据**: 截取关键日志片段，保存到文档
3. **对比分析**: 将连续的delta推送日志放在一起，逐条对比request IDs
4. **验证结论**: 确认修复是否有效，是否还需要进一步优化

---

**监控负责人**: ___________
**监控开始时间**: ___________
**初步结论**: [ ] ✅ 修复有效  [ ] ⚠️ 部分有效  [ ] ❌ 需进一步分析
