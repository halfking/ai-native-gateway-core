# Body Size 优化方案与监控 - 2026-07-25

## 背景

在 2026-07-24 修复客户端断开日志后，发现 RequestBody 64KB 限制对长对话/文档分析场景不够，2026-07-25 已将限制提升到 512KB。本文档记录后续优化方向和监控方法。

---

## 已完成的优化

### 1. RequestBody 大小限制: 64KB → 512KB ✅

**提交**: 60e3fd9c  
**文件**: `domains/streaming/handler.go`

**修改**:
```go
maxSize := 512 * 1024 // 512KB，覆盖长对话/文档分析/工具调用场景
bodyText := string(logCtx.Body)
if len(bodyText) > maxSize {
    bodyText = bodyText[:maxSize] + "...[truncated,original=" + strconv.Itoa(len(logCtx.Body)) + "bytes]"
}
```

**覆盖场景**:
- ✅ 短对话 (1-5 轮): 2-10KB
- ✅ 中等对话 (10-20 轮): 10-50KB
- ✅ 长对话 (50+ 轮): 100-500KB
- ✅ 大文档分析 (100KB 文档): 150-300KB
- ⚠️ 工具调用 (200KB-1MB): 部分覆盖
- ❌ 包含 base64 图片 (500KB-2MB): 仍会截断

### 2. 测试用例覆盖

**文件**: `domains/streaming/handler_disconnect_probe_test.go`

新增 `TestBuildClientDisconnectProbeEntry_LargeBody`:
- Case A: 400KB 请求体 → 完整保留 ✓
- Case B: 800KB 请求体 → 截断 + 标记原始字节数 ✓

所有 5 个测试全部通过。

---

## 待优化项

### 3. ResponseBody 大小限制 (待评估)

**当前状态**: ResponseBody 没有大小限制，可能导致:
- 完整流式响应被完整存储（可能数 MB）
- 数据库压力较大
- 长时间响应（o1-preview, DeepSeek-R1）可能产生 100KB-1MB 响应

**建议**: 同样设置 512KB 限制，但需要确认这是否影响分析。

### 4. 数据库性能监控

需要持续监控:
- `request_logs` 表大小增长
- 截断请求占比
- 查询性能影响（jsonb 查询）

---

## 监控工具

### 1. Body Size 分布监控脚本

**文件**: `scripts/monitor-body-size.sh`

**使用方法**:
```bash
# 查看最近 24 小时
./scripts/monitor-body-size.sh 24

# 查看最近一周
./scripts/monitor-body-size.sh 168
```

**输出内容**:
- RequestBody/ResponseBody 大小分布（按区间）
- 截断请求数（带 original=N 标记）
- 截断请求的详细列表

### 2. 关键 SQL 查询

```sql
-- 1. 请求体截断统计
SELECT COUNT(*) AS truncated_count
FROM request_logs
WHERE request_body LIKE '%...[truncated%'
  AND created_at > now() - interval '1 hour';

-- 2. 大请求体占比 (> 256KB)
SELECT
    COUNT(*) FILTER (WHERE octet_length(request_body) > 256*1024) AS large_count,
    COUNT(*) AS total_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE octet_length(request_body) > 256*1024) / COUNT(*), 2) AS large_percent
FROM request_logs
WHERE created_at > now() - interval '1 hour'
  AND request_body IS NOT NULL;

-- 3. 平均 body 大小
SELECT
    ROUND(AVG(octet_length(request_body))/1024.0, 1) AS avg_request_kb,
    ROUND(AVG(octet_length(response_body))/1024.0, 1) AS avg_response_kb
FROM request_logs
WHERE created_at > now() - interval '1 hour';

-- 4. 客户端取消事件的 body 详情
SELECT
    request_id,
    api_key_id,
    latency_ms,
    octet_length(request_body)/1024 AS body_kb,
    SUBSTRING(request_body, 1, 200) AS preview_start,
    created_at
FROM request_logs
WHERE origin_stage LIKE 'probe-client_cancel-%'
  AND created_at > now() - interval '1 day'
ORDER BY created_at DESC
LIMIT 20;
```

---

## 调整建议触发条件

| 指标 | 当前值 | 触发调整阈值 | 建议动作 |
|------|--------|-------------|---------|
| RequestBody 截断率 | - | > 5% | 提升到 1MB |
| RequestBody 截断率 | - | > 10% | 提升到 2MB 或实现动态配置 |
| ResponseBody > 1MB | - | > 5% | 给 ResponseBody 加 512KB 限制 |
| 表大小增长 | - | > 10GB/周 | 评估归档策略 |
| 查询 P95 延迟 | - | > 100ms | 检查 jsonb 查询性能 |

---

## 后续优化方向

### 短期（1-2 周）

1. **生产数据收集**: 运行监控脚本 1 周，收集实际大小分布
2. **截断率评估**: 根据实际数据决定是否需要进一步提升限制
3. **ResponseBody 限制**: 如果发现响应体普遍 > 1MB，给响应体加同样的 512KB 限制

### 中期（1 个月）

1. **动态配置**: 实现 `system_settings.request_log_body_max_kb` 可配置
2. **超大请求单独存储**: 超过 512KB 的请求体存储到外部系统（S3/OSS）
3. **归档策略**: 7 天后的 body 数据归档到冷存储

### 长期（3 个月）

1. **请求体压缩**: 超过 128KB 的请求体使用 zstd 压缩
2. **选择性记录**: 只在失败/取消/超时的情况下记录完整 body，成功请求只记录预览
3. **PII 脱敏**: 在记录前对请求体中的敏感信息脱敏

---

## 当前状态

✅ **512KB RequestBody 限制已部署**  
✅ **测试覆盖完整**  
⏳ **监控脚本待运行验证**  
⏳ **生产数据待收集**

**下一步**: 运行监控脚本，收集 24-48 小时数据，评估是否需要进一步调整。