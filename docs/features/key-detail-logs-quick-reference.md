# 密钥详情页请求日志 - 快速参考

## 功能位置
**路径**: `/keys/:id` → 页面底部"最近请求日志"区域

## 界面元素

### 区域标题
```
最近请求日志                           [查看全部]
```

### 日志条目格式
```
[状态标签] model-name                     12:30:45
150ms  100 + 50 tokens  $0.001000
```

## 状态标签说明

| 状态 | 颜色 | 说明 |
|------|------|------|
| 成功 | 绿色 | 请求成功完成 |
| 失败原因 | 红色 | 请求失败，显示错误类型 |
| 进行中 | 黄色 | 请求正在处理中 |

## 交互操作

### 1. 点击日志条目
- **操作**: 点击任意日志卡片
- **结果**: 在右侧打开 `RequestLogDrawer`，显示完整的请求详情

### 2. 查看全部日志
- **操作**: 点击右上角"查看全部"按钮
- **结果**: 跳转到 `/request-logs?api_key_id=:id`，显示该密钥的所有日志

## 数据说明

### 显示的信息
1. **状态**: 请求的执行状态
2. **模型**: 优先显示 `canonical_model`，回退到 `client_model`
3. **时间**: 请求发起时间
4. **延迟**: 请求耗时（仅成功请求显示）
5. **Tokens**: prompt tokens + completion tokens（仅成功请求显示）
6. **费用**: 请求费用，USD（仅成功请求显示）

### 数据范围
- 最多显示最近 10 条请求
- 按时间倒序排列（最新的在前）

## 特殊状态

### 加载中
```
加载中…
```

### 空状态
```
暂无请求记录
```

### 错误状态
```
[错误信息]
```

## 技术细节

### API 调用
```typescript
GET /api/logs?api_key_id=123&page=1&page_size=10
```

### 响应结构
```typescript
{
  items: RequestLogRow[],
  total: number,
  page: number,
  page_size: number
}
```

### 关键字段映射

| 显示项 | 数据源字段 |
|--------|-----------|
| 模型名 | `canonical_model` \|\| `client_model` |
| 时间 | `ts` |
| 状态 | `success`, `error_kind` |
| 延迟 | `latency_ms` |
| Prompt Tokens | `prompt_tokens` |
| Completion Tokens | `completion_tokens` |
| 费用 | `cost_usd` |

## 常见问题

### Q: 为什么看不到请求日志？
A: 可能的原因：
1. 该密钥尚未发起任何请求
2. 日志正在加载中
3. 网络错误或权限问题

### Q: 日志显示"进行中"很久没更新？
A: 可能的原因：
1. 长时间运行的流式请求
2. 请求卡住或超时
3. 刷新页面查看最新状态

### Q: 能否看到 10 条以上的日志？
A: 点击"查看全部"按钮，跳转到完整的请求日志列表页。

### Q: 日志能否实时更新？
A: 当前版本不支持自动刷新，需要手动刷新页面。

## 代码示例

### 手动触发日志加载
```typescript
await loadRecentLogs()
```

### 打开日志详情
```typescript
openLogDetail('request-id-123')
```

### 跳转到完整列表
```typescript
viewAllLogs() // 等同于 router.push(`/request-logs?api_key_id=${keyId}`)
```

## 相关组件

- `RequestLogDrawer`: 请求详情侧边栏
- `RequestLogsView`: 完整的请求日志列表页

## 相关文档

- [功能文档](./key-detail-request-logs.md)
- [实现总结](./key-detail-request-logs-summary.md)
- [变更日志](./CHANGELOG-key-detail-logs.md)
