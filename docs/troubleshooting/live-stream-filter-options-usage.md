# 实时请求流筛选选项诊断工具使用说明

## 问题描述

在 https://llmgo.kxpms.cn/dashboard 的实时请求流页面，点击"筛选"区域的"模型"、"供应商"、"原厂"按钮后，弹窗中显示的可选项数量很少。

## 快速诊断

### 方法一：运行自动诊断脚本（推荐）

在服务器上运行诊断脚本：

```bash
cd /path/to/llm-gateway-go
./scripts/debug-live-stream-filters.sh
```

脚本会自动检查：
- Redis 实时流数据是否存在
- 各维度泳道的数据量
- 请求详情的字段完整性
- 数据库记录的字段缺失率
- 可选项的唯一值数量

根据输出的诊断结果和建议进行修复。

### 方法二：手动检查

#### 1. 检查 Redis 数据

```bash
redis-cli

# 检查主队列
ZCARD llmgw:live:main

# 列出所有维度队列
KEYS llmgw:live:dim:*

# 检查某个维度队列
ZCARD llmgw:live:dim:vendor:openai
ZCARD llmgw:live:dim:provider:openai-official

# 查看请求详情
GET llmgw:live:req:<request_id>
```

**预期结果**：
- `llmgw:live:main` 应该有 50-200 条记录
- 每个维度应该有 5-20 个泳道
- 每个泳道应该有 5-20 条记录
- 请求详情应该包含 `model`、`model_category`、`provider_code` 字段

#### 2. 检查数据库记录

```sql
-- 检查最近请求的字段完整性
SELECT 
  request_id,
  model,
  canonical_name,
  model_category,
  provider_code,
  created_at
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 20;

-- 统计字段缺失率
SELECT 
  COUNT(*) as total,
  COUNT(CASE WHEN model_category IS NULL OR model_category = '' THEN 1 END) as missing_category,
  COUNT(CASE WHEN provider_code IS NULL OR provider_code = '' THEN 1 END) as missing_provider,
  ROUND(100.0 * COUNT(CASE WHEN model_category IS NULL OR model_category = '' THEN 1 END) / COUNT(*), 2) as category_missing_percent,
  ROUND(100.0 * COUNT(CASE WHEN provider_code IS NULL OR provider_code = '' THEN 1 END) / COUNT(*), 2) as provider_missing_percent
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour';
```

**预期结果**：
- `model_category` 缺失率应该 < 5%
- `provider_code` 缺失率应该 < 5%

#### 3. 检查后端日志

```bash
# 查找字段缺失警告
tail -n 1000 /path/to/gateway.log | grep "missing model_category\|missing provider_code"

# 如果警告很多，说明问题在数据源
```

#### 4. 检查浏览器前端

打开 https://llmgo.kxpms.cn/dashboard，按 F12 打开开发者工具：

1. **Network 标签**：
   - 找到 `live-stream` 的 EventSource 连接
   - 查看接收到的 SSE 消息
   - 检查 `delta.changed_lanes` 中的数据

2. **Console 标签**：
   ```javascript
   // 加载诊断脚本
   fetch('/debug-filters.js').then(r => r.text()).then(eval);
   
   // 运行诊断
   window.debugLiveStreamFilters();
   ```

## 常见问题和解决方案

### 问题 1：Redis 队列为空

**症状**：`ZCARD llmgw:live:main` 返回 0

**原因**：
- Redis 未启动或连接失败
- Live stream 异步记录器未启动
- SSE hub 未正确初始化

**解决方案**：
```bash
# 1. 检查 Redis 连接
redis-cli ping

# 2. 检查服务日志
grep "live stream" /path/to/gateway.log | tail -50

# 3. 重启网关服务
systemctl restart llm-gateway
```

### 问题 2：字段缺失率高

**症状**：`model_category` 或 `provider_code` 缺失率 > 10%

**原因**：
- 路由层未正确设置 `provider_code`
- 模型目录查询未正确设置 `model_category`
- 数据库迁移不完整

**解决方案**：
1. 检查路由代码，确保设置了 provider_code
2. 检查模型目录查询逻辑
3. 运行数据库补丁脚本（如果有）

### 问题 3：数据量过少

**症状**：各维度的唯一值数量 < 3

**原因**：
- 系统刚启动，数据积累不足
- 生产环境请求量低
- 只有少量模型/供应商在使用

**解决方案**：
- 等待系统积累更多请求（通常 10-30 分钟）
- 发起一些测试请求
- 确认这是正常的业务状态

### 问题 4：前端显示不全

**症状**：后端数据正常，但前端筛选选项少

**原因**：
- 前端缓存问题
- SSE 连接断开
- 前端合并逻辑错误

**解决方案**：
```javascript
// 在浏览器控制台执行
// 1. 清空前端状态
localStorage.clear();
location.reload();

// 2. 强制刷新 SSE 连接
// 点击实时流的"暂停"再点击"恢复"

// 3. 检查前端数据
window.debugLiveStreamFilters();
```

## 预期的正常状态

修复后，筛选弹窗应该显示：

- **模型**：10-50+ 个选项（取决于使用的模型种类）
- **供应商**：5-20+ 个选项（取决于配置的供应商数量）
- **原厂**：5-10+ 个选项（openai, anthropic, google, 等）
- **客户端**：1-10+ 个选项（取决于接入的客户端类型）

如果某个筛选项只有 1-2 个选项，可能是：
1. 业务确实只使用了这么少的类型（正常）
2. 数据积累不足（等待或发起测试请求）
3. 字段缺失（需要修复数据源）

## 相关文档

- 详细诊断文档：`docs/troubleshooting/live-stream-filter-options-diagnostic.md`
- 实时流架构：`docs/architecture/live-stream.md`（如果存在）
- Redis 数据结构：`admin/live_stream_redis_store.go` 注释

## 联系支持

如果按照以上步骤仍无法解决问题，请提供以下信息：

1. 诊断脚本的完整输出
2. 后端日志的相关片段
3. 浏览器控制台的 `debugLiveStreamFilters()` 输出
4. 问题发生的时间和频率
