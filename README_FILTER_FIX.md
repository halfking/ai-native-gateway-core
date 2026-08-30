# 实时请求流筛选选项问题 - 快速诊断和修复指南

## 🎯 快速开始

你报告的问题：在 https://llmgo.kxpms.cn/dashboard 的实时请求流"筛选"中，模型、供应商、原厂的可选项数据太少。

### 立即诊断（3 分钟）

在生产服务器上运行：

```bash
cd /path/to/llm-gateway-go
./scripts/debug-live-stream-filters.sh
```

这个脚本会自动检查所有可能的问题源，并给出诊断结果。

---

## 📊 我已经完成的分析

### 代码审查结论

我审查了完整的数据流程，从后端到前端：

✅ **前端代码正确**：
- `useLiveStreamFilters.ts` 正确从 `lanes.requests` 提取筛选选项
- `useSwimLane.ts` 的 `filterSourceLanes` 已在 2026-08-29 修复（聚合所有维度）
- SSE 数据合并逻辑正确

✅ **后端结构正确**：
- `LiveStreamTile` 包含 `Model`、`Vendor`、`Provider` 字段
- `liveRequestTile()` 函数正确填充这些字段
- Delta 推送包含完整的 tile 数据

⚠️ **最可能的问题**：
- **后端数据源缺失**：`LiveRequest` 的 `ModelCategory` 或 `ProviderCode` 字段为空
- 这会导致 `vendor` 和 `provider` 字段缺失或不准确

---

## 🔍 问题根源分析

### 数据流程图

```
1. 请求处理
   ↓
2. LiveRequest { Model, ModelCategory, ProviderCode }  ← 可能在这里缺失
   ↓
3. liveRequestTile() → LiveStreamTile { model, vendor, provider }
   ↓
4. Redis 存储 → SSE 推送
   ↓
5. 前端提取 → 筛选选项
```

### 关键字段映射

| 筛选项 | 前端字段 | 后端来源 | 缺失影响 |
|-------|---------|----------|---------|
| 模型 | `req.model` | `LiveRequest.CanonicalName` 或 `Model` | ❌ 低（通常有值） |
| 原厂 | `req.vendor` | `resolveVendorForRequest()` | ⚠️ 中（依赖 ModelCategory） |
| 供应商 | `req.provider` | `LiveRequest.ProviderCode` | 🔴 高（经常缺失） |

### 后端已有的保护措施

```go
// admin/live_stream_redis_store.go:295-301
if req.ModelCategory == "" {
    slog.Debug("live stream record: missing model_category", ...)
}
if req.ProviderCode == "" {
    slog.Debug("live stream record: missing provider_code", ...)
}
```

后端已经记录了缺失警告，查看日志可以确认问题频率。

### vendor 字段的降级逻辑

```go
// resolveVendorForRequest() 的降级链：
1. ModelCategory (from DB)              ← 首选
2. VendorFromProvider(ProviderCode)    ← 第二选择
3. InferVendorFromModel(Model)         ← 从模型名推断
4. Model 或 CanonicalName              ← 最后兜底
```

即使 `ModelCategory` 缺失，vendor 也应该能通过推断得到值，但可能不够准确。

---

## 🛠️ 诊断工具

我已经创建了完整的诊断工具集：

### 1. 自动诊断脚本（推荐）

**位置**：`scripts/debug-live-stream-filters.sh`

**功能**：
- ✅ 检查 Redis 数据量
- ✅ 检查字段完整性
- ✅ 检查数据库缺失率
- ✅ 统计唯一值数量
- ✅ 给出修复建议

**运行**：
```bash
./scripts/debug-live-stream-filters.sh
```

### 2. 浏览器诊断脚本

**位置**：`web/debug-filters.js`

**使用**：
```javascript
// 在浏览器控制台
fetch('/debug-filters.js').then(r => r.text()).then(eval);
window.debugLiveStreamFilters();
```

### 3. 完整文档

- 📋 **分析报告**：`docs/troubleshooting/LIVE_STREAM_FILTER_ANALYSIS.md`
- 📖 **详细诊断**：`docs/troubleshooting/live-stream-filter-options-diagnostic.md`
- 📘 **使用说明**：`docs/troubleshooting/live-stream-filter-options-usage.md`

---

## 🎯 推荐的修复步骤

### 步骤 1：运行诊断脚本（5 分钟）

```bash
cd /path/to/llm-gateway-go
./scripts/debug-live-stream-filters.sh
```

查看输出，重点关注：
- ❌ **Redis 主队列为空** → SSE 推送未工作
- ❌ **字段缺失率 > 10%** → 数据源问题
- ⚠️ **唯一值数量 < 3** → 数据量不足

### 步骤 2：查看后端日志（2 分钟）

```bash
# 查找字段缺失警告
tail -n 1000 /path/to/gateway.log | grep "missing model_category\|missing provider_code" | wc -l
```

如果数量很多（> 100），说明问题在数据源。

### 步骤 3：根据诊断结果修复

#### 场景 A：字段缺失率高（最可能）

**症状**：
- `model_category` 缺失率 > 10%
- `provider_code` 缺失率 > 10%
- 大量 "missing model_category" 日志

**修复**：
1. 检查路由层是否设置 `ProviderCode`
2. 检查模型目录查询是否返回 `ModelCategory`
3. 确认数据库 `request_logs` 表的字段是否正确填充

#### 场景 B：Redis 队列为空

**症状**：
- `ZCARD llmgw:live:main` 返回 0
- 前端显示"暂无请求数据"

**修复**：
1. 检查 Redis 连接
2. 检查 SSE hub 是否启动
3. 重启网关服务

#### 场景 C：数据量过少（可能正常）

**症状**：
- Redis 有数据，但唯一值很少
- 只有 1-2 个模型/供应商

**修复**：
- 这可能是正常的业务状态（刚启动或低流量）
- 等待 30 分钟积累数据
- 或者发起一些测试请求

### 步骤 4：验证修复（5 分钟）

1. 重新运行诊断脚本
2. 在浏览器打开实时流页面
3. 点击筛选按钮，检查选项数量
4. 切换不同维度，确认筛选选项一致

---

## 📈 预期结果

修复后的正常状态：

| 筛选项 | 预期数量 | 说明 |
|-------|---------|------|
| 模型 | 10-50+ | 取决于使用的模型种类 |
| 供应商 | 5-20+ | 取决于配置的供应商数量 |
| 原厂 | 5-10+ | openai, anthropic, google 等 |
| 客户端 | 1-10+ | 取决于接入的客户端类型 |

**如果某项只有 1-2 个选项**：
- ✅ 可能是正常的（业务确实只用这么多）
- ⚠️ 可能是数据积累不足（等待或测试）
- ❌ 可能是字段缺失（需要修复）

---

## 🚨 常见问题速查

### Q1：刚修复完，但前端还是显示少？

**A**：清空浏览器缓存
```javascript
// 浏览器控制台
localStorage.clear();
location.reload();
```

### Q2：某个维度下筛选选项为空，切换维度就有？

**A**：Queue 维度的特殊处理未生效，检查 `useSwimLane.ts:36-49` 的代码是否已部署。

### Q3：Redis 有数据，但前端显示"暂无请求数据"？

**A**：SSE 连接可能断开
- 检查 Network 标签中的 EventSource 连接
- 点击"暂停"再点击"恢复"重连

### Q4：数据库显示字段完整，但 Redis 缺失？

**A**：异步记录器可能有延迟或失败
- 检查后端日志中的 "live stream record failed" 错误
- 确认 Redis 连接池没有耗尽

---

## 📞 需要更多帮助？

如果按照以上步骤仍无法解决，请提供：

1. ✅ 诊断脚本的完整输出
2. ✅ 后端日志的相关片段（最近 100 行）
3. ✅ 浏览器 `debugLiveStreamFilters()` 的输出
4. ✅ 问题发生的时间和环境（测试/生产）

---

## 📚 相关文件清单

已创建的文件：

```
llm-gateway-go/
├── scripts/
│   └── debug-live-stream-filters.sh        # 自动诊断脚本
├── web/
│   └── debug-filters.js                    # 前端诊断脚本
└── docs/troubleshooting/
    ├── LIVE_STREAM_FILTER_ANALYSIS.md      # 完整分析报告
    ├── live-stream-filter-options-diagnostic.md  # 详细诊断文档
    └── live-stream-filter-options-usage.md       # 使用说明
```

所有文件都已就绪，可以立即使用！

---

## ✅ 下一步行动

1. **立即执行**：运行 `./scripts/debug-live-stream-filters.sh`
2. **根据输出**：按照诊断结果和建议修复
3. **验证修复**：重新检查筛选选项数量
4. **持续监控**：添加告警规则（见分析报告）

祝顺利解决问题！🎉
