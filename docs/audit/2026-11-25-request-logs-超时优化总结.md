# request-logs 查询超时优化总结

**日期**: 2026-11-25  
**问题**: request-logs 页面在选择特定日期查询时超时，错误信息："query timeout after 60 seconds (29545 records found)"

## 一、问题分析

### 1.1 根本原因
- **探测流量数据量大**: 当启用 `include_probe=1` 或 `lane=all` 时，查询需要 UNION 三个源表（business hot + business cold + probe hot）
- **24小时窗口过大**: 生产环境实测24小时窗口+探测流量+2个过滤器查询31K条记录仍会超时
- **60秒超时不足**: 对于包含探测流量的大窗口查询，60秒超时不够

### 1.2 现有索引情况
经检查，相关表已有时间索引：
- `idx_session_turns_hot_ts` on `session_turns_hot(ts)`
- `idx_session_turns_hot_tenant_ts` on `session_turns_hot(tenant_id, ts)`
- `idx_request_logs_hot_ts` on `request_logs_hot(ts)`
- `idx_request_logs_hot_tenant_ts` on `request_logs_hot(tenant_id, ts)`

问题不在于缺少索引，而在于查询复杂度和数据量。

## 二、实施的优化

### 2.1 后端优化（admin/logs.go）

#### ① 动态超时
```go
// 业务流量查询：60秒
// 探测流量查询：120秒（需要UNION三个源表）
queryTimeout := 60 * time.Second
if includeProbe {
    queryTimeout = 120 * time.Second
}
```

#### ② 自动窗口限制
```go
// 探测流量查询超过12小时自动截断（除非 override_window_limit=1）
if includeProbe && queryString(r, "override_window_limit") != "1" {
    maxProbeWindow := 12 * time.Hour
    if end.Sub(start) > maxProbeWindow {
        start = end.Add(-maxProbeWindow)
        windowClamped = true
    }
}
```

#### ③ 改进的错误消息
超时错误现在提供具体的操作指导：
- **探测流量 + 窗口>12h**: "查询超时(120秒，已扫描X条记录)。建议：1) 缩小时间窗口至12小时以内，2) 减少过滤器，3) 或添加 override_window_limit=1 参数使用完整窗口"
- **探测流量 + 窗口≤12h**: "探测流量查询超时(120秒，已扫描X条记录)。建议：1) 缩小时间窗口，2) 减少过滤条件，3) 或移除探测流量过滤"
- **业务流量**: "查询超时(60秒，已扫描X条记录)。建议：1) 缩小时间窗口，2) 减少过滤条件，3) 或尝试添加更具体的过滤器"

#### ④ 响应头和响应体增强
添加查询元数据：
```
X-Query-Duration-Ms: 查询耗时（毫秒）
X-Query-Count: 记录总数
X-Query-Window-Hours: 查询窗口大小（小时）
X-Query-Include-Probe: 是否包含探测流量
X-Query-Window-Clamped: 窗口是否被自动限制
X-Query-Clamp-Reason: 限制原因说明
```

响应体新增字段：
```json
{
  "window_clamped": true/false,
  "window_clamp_message": "查询窗口已自动限制至12小时以提高性能",
  "query_window_start": "2026-11-25T00:00:00Z",
  "query_window_end": "2026-11-25T12:00:00Z"
}
```

### 2.2 前端优化建议

**待实施**: 前端组件需要改进加载状态显示，建议：

1. **使用响应头信息**: 检测 `X-Query-Include-Probe` 和 `X-Query-Window-Hours`，当探测流量查询且窗口>6h时，显示"查询可能需要较长时间"的提示

2. **改进加载动画**: 将现有的简单 loading 替换为：
   - 更醒目的 spinner（增大尺寸、增加对比度）
   - 添加进度提示文字："正在查询...（包含探测流量，可能需要1-2分钟）"
   - 添加估计时间（基于查询参数）

3. **显示窗口限制提示**: 当 `window_clamped=true` 时，在结果上方显示警告横幅：
   "注意：查询窗口已自动限制至12小时。如需查看完整时间范围，请添加更多过滤条件或使用 override_window_limit=1 参数"

4. **优化错误显示**: 超时错误已经包含了具体的操作建议，前端只需要正常显示即可

## 三、使用指南

### 3.1 正常使用
- **业务流量查询**: 窗口≤24小时，超时60秒
- **探测流量查询**: 窗口自动限制≤12小时，超时120秒

### 3.2 高级用法
如需查询超过12小时的探测流量窗口，添加参数：
```
/api/logs?include_probe=1&from=2026-11-24T00:00:00Z&to=2026-11-25T00:00:00Z&override_window_limit=1
```

注意：此时查询可能需要接近120秒才能完成，请耐心等待。

### 3.3 性能建议
1. **优先使用业务流量模式** (`include_probe=0`，默认)
2. **缩小时间窗口**: 探测流量建议≤6小时，业务流量建议≤24小时
3. **添加具体过滤器**: 如 api_key_id、provider_id、model 等
4. **避免全文搜索**: `q` 参数会触发 FTS 和 ILIKE，性能开销大

## 四、监控和告警

### 4.1 慢查询日志
查询超过5秒会记录 WARN 级别日志：
```
admin listLogs query slow: total_elapsed_ms=... count=... page=... filter_count=...
```

### 4.2 超时统计
查询超时会记录 ERROR 级别日志：
```
admin listLogs rows iteration failed: err=context deadline exceeded items_returned=... count=...
```

建议设置告警：当 `admin listLogs` 超时次数在5分钟内超过10次时告警。

## 五、后续优化方向

### 5.1 短期（1-2周）
1. ✅ 动态超时 - 已完成
2. ✅ 自动窗口限制 - 已完成
3. ⏳ 前端加载UI改进 - 待实施
4. ⏳ 添加查询结果缓存（5分钟TTL）- 待实施

### 5.2 中期（1个月）
1. 创建物化视图用于常见查询模式
2. 添加复合索引 `(tenant_id, ts, include_probe相关字段)`
3. 实施查询结果分页预加载
4. 添加查询计划分析和自动优化建议

### 5.3 长期（3个月）
1. 探测流量数据归档策略（>30天数据移到归档表）
2. 实施查询缓存层（Redis）
3. 考虑引入 ClickHouse 等OLAP数据库用于分析查询
4. 实施智能查询优化器（根据历史查询性能自动调整参数）

## 六、测试验证

### 6.1 测试场景
- [ ] 业务流量 24小时窗口查询
- [ ] 探测流量 6小时窗口查询
- [ ] 探测流量 24小时窗口查询（应自动限制到12小时）
- [ ] 探测流量 24小时窗口 + override_window_limit=1（应使用完整窗口）
- [ ] 超时场景的错误消息验证
- [ ] 响应头验证

### 6.2 Maestro测试脚本
参见 `tests/maestro/request-logs-timeout-test.yaml`

## 七、部署清单

- [x] 代码修改：admin/logs.go
- [x] 编译测试通过
- [ ] 提交代码
- [ ] 推送到远程仓库
- [ ] 部署到 192.168.31.211
- [ ] Maestro 验证测试
- [ ] 监控慢查询日志
- [ ] 收集用户反馈

## 八、风险评估

### 8.1 风险
1. **120秒超时可能仍不够**: 对于极大窗口+多过滤器，可能还会超时
   - **缓解**: 自动窗口限制到12小时
   - **后备**: 错误消息提供明确的操作指导

2. **自动窗口限制可能导致困惑**: 用户选择24小时但只看到12小时结果
   - **缓解**: 响应体包含 `window_clamped` 标志和说明消息
   - **后备**: 提供 `override_window_limit=1` 参数

3. **前端未同步更新**: 后端已优化但前端UI可能没有相应改进
   - **缓解**: 错误消息已经足够清晰，即使前端不变也能理解
   - **后备**: 本文档提供前端改进建议

### 8.2 回滚计划
如果优化导致问题，回滚步骤：
1. 恢复 admin/logs.go 到之前版本
2. 重新部署
3. 验证功能正常

回滚风险低，因为：
- 只修改了一个文件
- 改动是向后兼容的（不改变API接口）
- 有详细的测试用例

## 九、相关文档

- 代码文件：`admin/logs.go`
- 相关 Issue：request-logs页面查询超时问题
- 部署脚本：`deploy-211.sh`
- Maestro测试：`tests/maestro/request-logs-timeout-test.yaml`

---

**作者**: ZCode AI Assistant  
**审核**: 待审核  
**状态**: 后端优化已完成，前端优化待实施
