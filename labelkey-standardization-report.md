# 时间范围 labelKey 标准化修复报告

## 修复时间
2026-11-19

## 修复目标
统一所有数据统计页面的时间范围选择器 labelKey，使用标准 i18n 键，修复不存在的键引用。

## 标准 i18n 键规范

### 标准键位置：`web/src/locales/zh-CN/common.ts`
```typescript
dateRange: {
  preset: {
    today: '今天',
    yesterday: '昨天',
    last7d: '近 7 天',
    last14d: '近 14 天',
    last30d: '近 30 天',
  }
}
```

### 特殊键位置：`web/src/locales/zh-CN/dashboard.ts`
```typescript
range: {
  last90d: '近 90 天',  // 仅 90 天保持在 dashboard
}
```

## 修复详情

### 修复的文件（6个）

1. **BoardOperationalView.vue**
   - `dashboard.operational.timeRange1d` → `common.dateRange.preset.today`
   - `dashboard.operational.timeRange7d` → `common.dateRange.preset.last7d`
   - `dashboard.operational.timeRange30d` → `common.dateRange.preset.last30d`

2. **ClientAnalyticsView.vue**
   - `sessions.stats.last7Days` → `common.dateRange.preset.last7d`
   - `sessions.stats.last30Days` → `common.dateRange.preset.last30d`

3. **RoutingAuditView.vue**
   - `board.timeRange.presets.today` → `common.dateRange.preset.today`
   - `board.timeRange.presets.last7Days` → `common.dateRange.preset.last7d`
   - `board.timeRange.presets.last30Days` → `common.dateRange.preset.last30d`
   - `board.timeRange.presets.last90Days` → `dashboard.range.last90d`

4. **TaskAnalyticsView.vue**
   - `sessions.stats.last7Days` → `common.dateRange.preset.last7d`
   - `sessions.stats.last30Days` → `common.dateRange.preset.last30d`

5. **TaskProfileView.vue**
   - `board.timeRange.presets.last7Days` → `common.dateRange.preset.last7d`
   - `board.timeRange.presets.last14Days` → `common.dateRange.preset.last14d`
   - `board.timeRange.presets.last30Days` → `common.dateRange.preset.last30d`
   - `board.timeRange.presets.last90Days` → `dashboard.range.last90d`

6. **TenantDetailView.vue**
   - `board.timeRange.presets.today` → `common.dateRange.preset.today`
   - `board.timeRange.presets.last7Days` → `common.dateRange.preset.last7d`
   - `board.timeRange.presets.last30Days` → `common.dateRange.preset.last30d`
   - `board.timeRange.presets.last90Days` → `dashboard.range.last90d`

### 已确认标准的文件（无需修改）

- **AutoTuningView.vue** - 两处时间选择器均已使用标准键
- **UserProfileView.vue** - 使用 `dashboard.range.*`（符合规范）
- **CorrelationsView.vue** - 使用标准 `common.dateRange.preset.*` 键

### 不统一的合理原因（保持现状）

- **DecisionsView** - 使用相对时间描述（"最近1小时"/"最近24小时"）
- **UsageCost** - 使用月份选择器，不是日期范围
- **DashboardView** - 使用 composable 架构，时间选择器在独立组件中

### 并行会话负责（未提交）

- **WorkTypesView.vue** - 时间范围参数化（会话 mvs_aa27b530262e45fdbcc95c81b78bcfcd）
- **RoutingDashboardView** - analytics 统计（同上会话）
- **SessionStatsPanel** - 会话统计面板（同上会话）

## 错误的 labelKey 模式（已全部修复）

### 不存在的键前缀
- ❌ `board.timeRange.presets.*` - 该前缀不存在于任何 i18n 文件
- ❌ `sessions.stats.last*Days` - 该前缀不存在于任何 i18n 文件
- ❌ `dashboard.operational.timeRange*` - 该前缀存在但不应用于时间预设

### 正确的键前缀
- ✅ `common.dateRange.preset.*` - 标准位置，所有通用时间预设
- ✅ `dashboard.range.last90d` - 仅 90 天特殊键

## 验证结果

```bash
# 验证不再有不规范的 labelKey
$ grep -rn "labelKey.*board\.timeRange\|labelKey.*sessions\.stats\|labelKey.*dashboard\.operational\.timeRange" web/src/views/*.vue
# (无输出，表示已全部修复)
```

## 提交信息

- Commit: `13cec5709`
- 分支: `main`
- 已推送: `origin/main`

## 影响

- **前端显示**: 修复前端时间选择器文本显示，避免显示键名而非实际文本
- **国际化**: 确保所有 8 种语言的时间范围预设文本正确显示
- **一致性**: 所有页面统一使用相同的 i18n 键，便于维护

## 后续工作

无。所有应由本会话负责的 labelKey 规范化工作已完成。

## 协作纪律遵守

- ✅ 未修改并行会话负责的文件（WorkTypesView, RoutingDashboardView, SessionStatsPanel）
- ✅ 修复前先 `git pull` 确保代码最新
- ✅ 只提交本会话负责范围的 6 个文件
- ✅ WorkTypesView 的工作区改动保留，留给并行会话处理
