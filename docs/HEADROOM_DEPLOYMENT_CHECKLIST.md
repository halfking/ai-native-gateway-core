# Headroom 集成部署检查清单

## ✅ 完成状态

所有任务已完成，系统已就绪。

---

## 📋 部署前检查

### 1. 文件验证

```bash
# 验证所有文件已创建
✓ domains/hooks/compression/headroom_compressor.go       (7.5K)
✓ domains/hooks/compression/smart_crusher.go             (3.7K)
✓ domains/hooks/compression/adaptive_sizer.go            (5.2K)
✓ domains/hooks/compression/headroom_compressor_test.go  (5.5K)
✓ docs/HEADROOM_INTEGRATION.md                           (8.7K)
✓ docs/HEADROOM_SUMMARY.md                               (5.3K)
```

### 2. 代码修改验证

```bash
# 验证修改已应用
✓ domains/hooks/compression/compressor.go 
  - ModeHeadroom 枚举已添加
  - String() 方法已更新
  - envMode() 已支持 "6" 和 "headroom"
  - LoadMode() 已支持 "headroom"
  - StrategyHeadroom 常量已添加

✓ domains/hooks/compression/session_compressor.go
  - tryHeadroomCompression() 方法已添加
  - loadHeadroomConfig() 方法已添加
  - HeadroomCompressionResult 类型已定义
  - Phase 4 已集成 Headroom 逻辑

✓ domains/hooks/compression/session_cache.go
  - HeadroomApplied 字段已添加
  - HeadroomRatio 字段已添加
  - TokensSaved 字段已添加

✓ settings/spec_compression.go
  - compression.mode 选项已添加 "headroom"
  - compression.headroom.target_ratio 配置已添加
  - compression.headroom.enable_smart_crusher 配置已添加
  - compression.headroom.enable_adaptive_sizer 配置已添加
```

### 3. 编译验证

```bash
cd /path/to/llm-gateway-go
go build ./domains/hooks/compression
# 输出：无错误 ✓
```

### 4. 测试验证

```bash
go test -v ./domains/hooks/compression -run "TestHeadroom|TestSmartCrusher|TestAdaptiveSizer"

结果：
✓ TestHeadroomCompressor_BasicCompression  - PASS
✓ TestSmartCrusher_RemoveRedundant        - PASS
✓ TestAdaptiveSizer_Resize                - PASS
✓ TestHeadroomCompressor_SessionStitching - PASS
```

---

## 🚀 启用步骤

### 方式 1: 环境变量（测试环境）

```bash
# 设置压缩模式
export LLM_GATEWAY_COMPRESSION_MODE=6  # 或 "headroom"

# 重启服务
systemctl restart llm-gateway-go
```

### 方式 2: 数据库配置（生产环境推荐）

```sql
-- 启用 Headroom 模式
UPDATE settings 
SET value = '"headroom"'
WHERE key = 'compression.mode';

-- 调整目标压缩率（可选）
INSERT INTO settings (key, value, scope, category)
VALUES ('compression.headroom.target_ratio', '0.5', 'platform', 'compression')
ON CONFLICT (key) DO UPDATE SET value = '0.5';

-- 服务会自动热重载配置（HotReload: true）
```

### 方式 3: API 配置（动态调整）

```bash
# 通过管理 API 设置
curl -X POST http://localhost:8080/api/admin/settings \
  -H "Content-Type: application/json" \
  -d '{
    "key": "compression.mode",
    "value": "headroom"
  }'
```

---

## 📊 监控验证

### 1. 查看压缩效果

```sql
-- 查看最近的 Headroom 压缩记录
SELECT 
    id,
    created_at,
    compression_strategy,
    compression_meta->>'hr_ratio' as compression_ratio,
    compression_meta->>'hr_saved' as tokens_saved,
    compression_meta->>'hr_applied' as applied
FROM request_logs
WHERE compression_strategy = 'headroom'
ORDER BY created_at DESC
LIMIT 10;
```

### 2. 统计压缩性能

```sql
-- 统计 Headroom 压缩平均效果
SELECT 
    COUNT(*) as total_requests,
    AVG((compression_meta->>'hr_ratio')::float) as avg_ratio,
    AVG((compression_meta->>'hr_saved')::int) as avg_tokens_saved,
    MIN((compression_meta->>'hr_saved')::int) as min_saved,
    MAX((compression_meta->>'hr_saved')::int) as max_saved
FROM request_logs
WHERE compression_strategy = 'headroom'
  AND created_at > NOW() - INTERVAL '1 hour';
```

### 3. 对比不同模式

```sql
-- 对比各压缩模式效果
SELECT 
    compression_strategy,
    COUNT(*) as requests,
    AVG(CASE 
        WHEN compression_strategy = 'headroom' 
        THEN (compression_meta->>'hr_ratio')::float 
        ELSE NULL 
    END) as avg_ratio
FROM request_logs
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY compression_strategy
ORDER BY requests DESC;
```

---

## 🔍 故障排查

### 问题 1: 编译错误

```bash
# 检查 Go 版本
go version  # 需要 Go 1.19+

# 重新下载依赖
go mod tidy
go mod download
```

### 问题 2: 测试失败

```bash
# 运行详细测试
go test -v -race ./domains/hooks/compression -run TestHeadroom

# 检查测试覆盖率
go test -cover ./domains/hooks/compression
```

### 问题 3: 压缩未生效

```bash
# 检查配置
psql -c "SELECT key, value FROM settings WHERE key LIKE 'compression%';"

# 检查日志
tail -f /var/log/llm-gateway-go/app.log | grep -i headroom

# 验证模式加载
curl http://localhost:8080/api/admin/compression/stats
```

### 问题 4: 性能问题

```bash
# 压缩延迟过高，可以禁用 AdaptiveSizer
UPDATE settings 
SET value = 'false'
WHERE key = 'compression.headroom.enable_adaptive_sizer';

# 或调高目标压缩率减少压缩强度
UPDATE settings 
SET value = '0.7'  -- 从 0.5 提高到 0.7
WHERE key = 'compression.headroom.target_ratio';
```

---

## 📈 性能基准

### 预期指标

| 指标 | 目标值 | 说明 |
|------|--------|------|
| 压缩延迟 | < 5ms | SmartCrusher + AdaptiveSizer |
| 压缩率 | 13-20% | 取决于内容冗余度 |
| 内存开销 | < 1MB | 单次压缩操作 |
| CPU 使用 | < 2% | 正则匹配和字符串处理 |

### 压缩效果示例

```
输入: 150 tokens (包含口语化表达)
  └─> SmartCrusher: 135 tokens (-10%)
      └─> AdaptiveSizer: 120 tokens (-11%)
          = 总计: 120 tokens (-20%)
          
延迟: 3-5ms
```

---

## ✅ 最终确认

在生产环境启用前，请确认：

- [ ] 所有测试通过
- [ ] 编译无错误
- [ ] 在测试环境验证压缩效果
- [ ] 监控指标正常
- [ ] 配置参数符合预期
- [ ] 回滚方案已准备（设置 mode=smart）

---

## 🎯 回滚方案

如遇问题，可快速回滚：

```sql
-- 回滚到 smart 模式
UPDATE settings 
SET value = '"smart"'
WHERE key = 'compression.mode';

-- 服务会自动热重载，无需重启
```

---

## 📚 参考文档

- 完整集成文档: `docs/HEADROOM_INTEGRATION.md`
- 快速总结: `docs/HEADROOM_SUMMARY.md`
- 测试文件: `domains/hooks/compression/headroom_compressor_test.go`

---

**部署准备完成！** 🚀
