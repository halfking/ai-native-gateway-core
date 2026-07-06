# Session 组件

这个目录包含会话相关的 Vue 组件。

## HealthPanel.vue

健康面板组件，用于显示会话健康评分和诊断导航。

### 功能特性

1. **健康评分展示**
   - 显示 0-100 分的健康分数
   - A-F 等级标识（带颜色和图标）
   - 结果分类（completed/error/abandoned/unknown）

2. **扣分明细**
   - 列出所有扣分项及扣分值
   - 显示扣分原因的详细说明
   - 支持点击跳转到相关诊断面板

3. **诊断导航**
   - 高延迟 → 跳转到时间线面板
   - 频繁模型切换 → 跳转到模型切换面板
   - 合规问题 → 跳转到合规面板
   - 错误请求 → 跳转到时间线面板

4. **空数据态处理**
   - 健康分未计算时显示"建设中"提示
   - 加载态骨架屏
   - 无扣分项时显示成功状态

### Props

```typescript
interface Props {
  gwSessionId: string  // 会话 ID
}
```

### Emits

```typescript
{
  jumpTo: [target: string]  // 跳转到指定锚点（如 '#timeline'）
}
```

### 暴露方法

```typescript
{
  reload: () => Promise<void>  // 重新加载健康数据
}
```

### 使用示例

```vue
<template>
  <HealthPanel 
    :gw-session-id="sessionId" 
    @jump-to="handleJumpTo"
    ref="healthPanelRef"
  />
</template>

<script setup>
import { ref } from 'vue'
import HealthPanel from '@/components/session/HealthPanel.vue'

const sessionId = ref('gw_abc123')
const healthPanelRef = ref(null)

const handleJumpTo = (target) => {
  const element = document.querySelector(target)
  if (element) {
    element.scrollIntoView({ behavior: 'smooth' })
  }
}

// 手动重新加载
const reloadHealth = () => {
  healthPanelRef.value?.reload()
}
</script>
```

### API 依赖

组件依赖后端 API：
- `GET /api/admin/sessions/<id>/health` - 获取健康评分详情

响应格式：
```json
{
  "gw_session_id": "gw_abc123",
  "health_score": 72,
  "health_grade": "C",
  "outcome": "completed",
  "outcome_reason": "0 errors across 45 requests",
  "error_rate": 0.0,
  "avg_latency_ms": 1800,
  "computed_at": "2026-07-06T10:30:00Z",
  "penalties": [
    {
      "reason": "high_latency",
      "deduction": -15,
      "detail": "avg_latency_ms=6200 > 5000"
    }
  ]
}
```

### 样式定制

组件使用 Element Plus 的 CSS 变量，可通过覆盖以下变量定制样式：

- `--el-color-primary` - 主色调
- `--el-color-success` - 成功色（等级 A）
- `--el-color-warning` - 警告色（等级 C）
- `--el-color-danger` - 危险色（等级 F）

### 等级映射

| 等级 | 分数区间 | 颜色 | 图标 | 标签 |
|------|---------|------|------|------|
| A | 90-100 | 绿色 | 🟢 | 优秀 |
| B | 75-89 | 蓝色 | 🔵 | 良好 |
| C | 60-74 | 黄色 | 🟡 | 一般 |
| D | 40-59 | 橙色 | 🟠 | 较差 |
| F | 0-39 | 红色 | 🔴 | 异常 |

### 扣分项说明

| 扣分项 | 标签 | 跳转目标 |
|--------|------|---------|
| high_latency | 高延迟 | #timeline |
| frequent_model_switch | 频繁模型切换 | #model-switches |
| compliance_issue | 合规问题 | #compliance |
| per_error | 错误请求 | #timeline |
| error_ended | 错误结束 | #timeline |
| abandoned | 会话放弃 | #timeline |
| prompt_injection | 提示注入 | #compliance |
| pii_detected | PII 检测 | #compliance |
| toxic_output | 毒性输出 | #compliance |
| sensitive | 敏感内容 | #compliance |

### 参考文档

- 产品规划：`docs/session-management-analytics-plan.md` 第 4.3.3 节和 11.3.3 节
- 后端实现：`admin/session_health_api.go`
- 健康计算：`admin/session_health.go`
