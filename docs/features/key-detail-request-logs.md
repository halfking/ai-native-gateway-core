# 密钥详情页 - 请求日志功能

## 概述

在密钥详情页（`KeyDetailView`）中添加了"最近请求日志"区域，展示该密钥最近的 10 条请求记录。

## 功能特性

### 1. 日志列表展示

- 显示最近 10 条请求记录
- 每条记录包含：
  - **状态标识**：成功（绿色）、失败（红色）、进行中（黄色）
  - **模型名称**：优先显示 `canonical_model`，回退到 `client_model`
  - **时间戳**：请求时间
  - **延迟**：请求耗时（毫秒）
  - **Token 使用**：prompt tokens + completion tokens
  - **费用**：请求费用（美元）

### 2. 交互功能

- **点击日志条目**：打开 `RequestLogDrawer` 侧边栏，显示请求的详细信息
- **查看全部按钮**：跳转到请求日志列表页，自动过滤当前密钥的所有请求

### 3. 状态展示

- **加载中**：显示加载提示
- **错误状态**：显示错误信息
- **空状态**：显示"暂无请求记录"提示

## 技术实现

### 数据获取

```typescript
// 在页面加载时获取最近 10 条日志
async function loadRecentLogs() {
  const response = await getRequestLogs({
    api_key_id: keyId.value,
    page: 1,
    page_size: 10,
  })
  recentLogs.value = response.items
}
```

### 组件结构

```
KeyDetailView
├── 密钥信息卡片
├── 使用统计
├── 使用趋势图表
├── 模型使用详情表格
└── 最近请求日志 (新增)
    ├── 标题 + "查看全部"按钮
    └── 日志列表
        └── RequestLogDrawer (点击打开)
```

### 样式设计

- 每条日志记录采用卡片式设计
- 鼠标悬停时高亮显示
- 状态标签使用颜色编码
- 响应式布局，适配不同屏幕尺寸

## 国际化

支持中文和英文：

### 中文 (zh-CN)
```typescript
logs: {
  title: '最近请求日志',
  viewAll: '查看全部',
  empty: '暂无请求记录',
  success: '成功',
  pending: '进行中',
}
```

### 英文 (en-US)
```typescript
logs: {
  title: 'Recent Request Logs',
  viewAll: 'View All',
  empty: 'No request records',
  success: 'Success',
  pending: 'Pending',
}
```

## 测试

测试文件：`web/src/views/__tests__/KeyDetailView.test.ts`

测试覆盖：
- ✅ 页面加载时自动获取日志
- ✅ 空状态显示
- ✅ 错误状态处理

## 相关文件

### 修改的文件
- `web/src/views/KeyDetailView.vue` - 主要功能实现
- `web/src/locales/zh-CN/keys.ts` - 中文翻译
- `web/src/locales/en-US/keys.ts` - 英文翻译

### 新增的文件
- `web/src/views/__tests__/KeyDetailView.test.ts` - 单元测试

### 依赖的组件
- `RequestLogDrawer` - 请求详情侧边栏（已存在）
- `getRequestLogs` - API 接口（已存在）

## 使用示例

1. 用户访问密钥详情页 `/keys/:id`
2. 页面自动加载密钥信息、统计数据和最近 10 条请求日志
3. 用户可以：
   - 浏览最近的请求记录
   - 点击任意记录查看详细信息
   - 点击"查看全部"跳转到完整的日志列表

## 未来改进

- [ ] 添加日志刷新按钮
- [ ] 支持日志实时更新（WebSocket）
- [ ] 添加简单的筛选功能（成功/失败）
- [ ] 支持导出最近日志
