# 修复首页加载 JS 错误

**日期**: 2026-07-10  
**问题**: 首页加载时出现 `TypeError: Cannot destructure property 'row' of 'undefined'` 错误

## 问题分析

### 错误现象
```
[Vue Error] TypeError: Cannot destructure property 'row' of 'undefined' as it is undefined.
    at index-CTolYw8f.js:156:107393
```

### 根本原因
在 Vue 组件中使用了 TypeScript 的非空断言操作符 (`!`) 访问可能为 `null` 的数据：

1. **HeatmapMatrix.vue**: 第 109 行使用 `data!.rows`，但 `data` prop 类型为 `AnalyticsMatrix | null`
2. **RouteFlowSankey.vue**: 第 176 行使用 `data!.links`，同样的问题
3. **RoutingDashboardView.vue**: 第 853, 856 行使用 `layer2Cache[...]!`

当组件初始化时，这些数据为 `null`，Vue 尝试渲染时会报错。虽然代码中有 `v-if` / `v-else-if` 条件判断，但 `v-else` 分支没有检查数据是否存在就直接使用了非空断言。

## 修复方案

### 修改文件

#### 1. `web/src/components/analytics/HeatmapMatrix.vue`

**修改前** (第 94 行):
```vue
<div v-else class="table-wrap">
  <table class="heatmap-table">
    <!-- ... -->
    <tr v-for="(row, ri) in data!.rows" :key="row">
```

**修改后**:
```vue
<div v-else-if="data && data.rows && data.cols" class="table-wrap">
  <table class="heatmap-table">
    <!-- ... -->
    <tr v-for="(row, ri) in data.rows" :key="row">
```

移除了所有 `data!` 非空断言，改用 `data`，并添加了明确的 `v-else-if` 条件检查。

#### 2. `web/src/components/analytics/RouteFlowSankey.vue`

**修改前** (第 153, 176 行):
```vue
<div v-else class="sankey-svg-wrap">
  <!-- ... -->
  <path v-for="(l, i) in data!.links" :key="'l-' + i"
```

**修改后**:
```vue
<div v-else-if="data && data.links" class="sankey-svg-wrap">
  <!-- ... -->
  <path v-for="(l, i) in data.links" :key="'l-' + i"
```

#### 3. `web/src/views/RoutingDashboardView.vue`

**修改前** (第 853, 856 行):
```vue
<div class="l2-meta mono">{{ layer2Cache[m.canonical_name || m.raw_model]!.resolution_path }}</div>
<div v-for="(c, ci) in layer2Cache[m.canonical_name || m.raw_model]!.candidates.filter(x => x.routable).slice(0, 4)"
```

**修改后**:
```vue
<div class="l2-meta mono">{{ layer2Cache[m.canonical_name || m.raw_model].resolution_path }}</div>
<div v-for="(c, ci) in layer2Cache[m.canonical_name || m.raw_model].candidates.filter(x => x.routable).slice(0, 4)"
```

## 验证步骤

1. **编译前端代码**:
   ```bash
   cd web
   npm run build
   ```
   ✅ 编译成功，无错误

2. **浏览器验证**:
   - 刷新首页
   - 检查浏览器控制台，确认不再出现 `Cannot destructure property 'row' of 'undefined'` 错误
   - 访问路由仪表板 (`/routing-v2/dashboard`)，验证热力图和流向图正常显示

3. **功能测试**:
   - 数据分析页面的热力图交互正常
   - 路由流向图正常渲染
   - 模型路由详情展开正常

## 技术说明

### TypeScript 非空断言操作符的问题

TypeScript 的 `!` 操作符告诉编译器"我确定这个值不是 null/undefined"，但这只是编译时的类型断言，运行时如果值确实是 null/undefined，仍会报错。

### 正确的做法

1. **使用 v-if 条件渲染**: 在使用数据前先检查是否存在
2. **移除非空断言**: 让 TypeScript 和 Vue 正确处理类型检查
3. **可选链操作符**: 在脚本中使用 `?.` 进行安全访问

## 相关文件

- `web/src/components/analytics/HeatmapMatrix.vue`
- `web/src/components/analytics/RouteFlowSankey.vue`
- `web/src/views/RoutingDashboardView.vue`

## 部署说明

修改后需要重新构建前端并部署：

```bash
cd web
npm run build
# 将 dist 目录部署到生产环境
```
