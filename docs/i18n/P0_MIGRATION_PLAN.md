# P0 核心视图 i18n 迁移计划

> 创建时间：2026-07-05  
> 总工期：18 小时  
> 目标：让 80% 用户访问路径支持 8 种语言

---

## 📊 现状分析

### 检查结果
```bash
总视图数：72 个
未 i18n 化：60 个（83.3%）
已 i18n 化：12 个（16.7%）
```

### 关键发现
- ✅ **locale 模块已存在**（44 个 namespace 已就位）
- ⚠️ **视图未使用**：即使 locale 已有，许多视图没用 `useI18n` 调取
- ✅ **部分视图已开始迁移**：RequestLogsView（3 处 `useI18n`）

---

## 🎯 P0 迁移策略

### 策略：复用现有 locale 模块

由于 44 模块的 locale 文件**已经完整存在**（从参考项目复制），迁移工作主要是：

1. **检测**：每视图扫描硬编码中文字符
2. **替换**：将硬编码 → `t('module.key')` 调用
3. **测试**：构建验证 + 浏览器实测

### 现有 locale 模块映射（参考项目已创建）

| 视图 | 对应模块 | 已存在？ |
|---|---|---|
| RequestLogsView | `requests.ts` | ✅ 已存在（368 行） |
| ModelsView | `models.ts` | ✅ 已存在 |
| CredentialsView | `credentialMonitor.ts` | ✅ 已存在 |
| ProvidersView | `providers.ts` | ✅ 已存在 |
| RoutingOverviewView | `routing.ts` | ✅ 已存在 |
| DecisionsView | `decisions.ts` | ✅ 已存在 |
| TenantsView | `tenants.ts` | ✅ 已存在 |
| TenantDashboardView | `dashboard.ts` | ✅ 已存在 |
| UsersView | `users.ts` | ✅ 已存在 |
| SessionListView | `sessions.ts` | ✅ 已存在 |

**核心优势**：locale 文件已存在，迁移工作**仅需修改 .vue 文件**，无需新增翻译。

---

## 📋 P0 10 个核心视图迁移清单

### 1. RequestLogsView.vue（已部分 i18n）
**状态**：3 处 useI18n，需要补充
**硬编码中文**：1878 字符
**预计工时**：1.5h（已有基础）
**优先级**：P0 最高（最高频访问）

### 2. ModelsView.vue
**状态**：完全硬编码
**硬编码中文**：1043 字符
**预计工时**：2h
**优先级**：P0

### 3. CredentialsView.vue
**状态**：完全硬编码
**硬编码中文**：1816 字符
**预计工时**：1.5h
**优先级**：P0

### 4. ProvidersView.vue
**状态**：可能已部分 i18n
**硬编码中文**：需审计
**预计工时**：1.5h
**优先级**：P0

### 5. RoutingOverviewView.vue
**状态**：完全硬编码
**硬编码中文**：需审计
**预计工时**：2h
**优先级**：P0

### 6. DecisionsView.vue
**状态**：完全硬编码
**硬编码中文**：需审计
**预计工时**：2h
**优先级**：P0

### 7. TenantsView.vue
**状态**：完全硬编码
**硬编码中文**：需审计
**预计工时**：2h
**优先级**：P0

### 8. TenantDashboardView.vue
**状态**：完全硬编码
**硬编码中文**：需审计
**预计工时**：2h
**优先级**：P0

### 9. UsersView.vue
**状态**：完全硬编码
**硬编码中文**：需审计
**预计工时**：1.5h
**优先级**：P0

### 10. SessionListView.vue
**状态**：完全硬编码
**硬编码中文**：需审计
**预计工时**：1.5h
**优先级**：P0

---

## 🔧 迁移流程（每个视图）

### 步骤 1：审计（5 分钟）
```bash
# 检测硬编码中文数量
python3 -c "
import re
with open('web/src/views/XXXView.vue', 'r') as f:
    content = f.read()
chinese = re.findall(r'[\u4e00-\u9fa5]', content)
print(f'硬编码字符数：{len(chinese)}')
"
```

### 步骤 2：添加 useI18n（2 分钟）
```vue
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const { t } = useI18n()
// ... 其他 imports
</script>
```

### 步骤 3：替换硬编码（30 分钟 - 1.5 小时）
```vue
<template>
  <!-- 之前 -->
  <h1>请求日志</h1>
  
  <!-- 之后 -->
  <h1>{{ t('requests.list.title') }}</h1>
</template>
```

### 步骤 4：处理插值（10 分钟）
```vue
<!-- 静态文案 -->
{{ t('requests.common.loading') }}

<!-- 带变量的动态文案 -->
{{ t('requests.list.cacheDesc', { total: 100 }) }}

<!-- 在 locale 文件中定义 -->
// requests.ts
list: {
  cacheDesc: '本页 {total} 条已压缩'
}
```

### 步骤 5：验证（3 分钟）
```bash
# 构建验证
npm run build

# 检查硬编码
python3 -c "
import re
with open('web/src/views/XXXView.vue', 'r') as f:
    content = f.read()
chinese = re.findall(r'[\u4e00-\u9fa5]', content)
print(f'剩余硬编码字符：{len(chinese)}')
"
```

---

## ⚠️ 风险与挑战

### 挑战 1：locale 键可能不完整
**问题**：参考项目的 44 模块可能缺少某些视图需要的键
**解决**：
- 审计时检查所有 `t('xxx')` 调用对应的键是否存在
- 缺失的键补到 locale 文件（8 种语言）

### 挑战 2：动态文案（如状态映射）
**问题**：某些视图根据 API 返回值显示不同文案（如"成功"、"失败"）
**解决**：
- 使用状态码映射：`{ ok: '成功', error: '失败' }`
- 或使用计算属性 + t()

### 挑战 3：表格列标题
**问题**：Element Plus 表格的 `prop` + `label` 可能不便于 i18n
**解决**：
- 将 label 提取为计算属性：`computed(() => t('table.headers.xxx'))`

### 挑战 4：枚举值转换
**问题**：后端返回数字/英文，前端显示中文
**解决**：
- 用 `computed` + `t()` 动态映射

---

## 🚀 建议的迁移顺序

### 第 1 批（本次会话，4 小时）
1. **RequestLogsView**（1.5h）— 已有基础，最成熟
2. **ModelsView**（2h）— 模型管理核心
3. **CredentialsView**（0.5h）— 复用常见模式

### 第 2 批（下次会话，6 小时）
4. **ProvidersView**（1.5h）
5. **RoutingOverviewView**（2h）
6. **DecisionsView**（1.5h）
7. **TenantDashboardView**（1h）

### 第 3 批（第三次会话，8 小时）
8. **TenantsView**（2h）
9. **UsersView**（1.5h）
10. **SessionListView**（1.5h）
11. 剩余 3 个视图（3h）

---

## 📦 自动化辅助

### 准备脚本
```bash
# P0_MIGRATION_SCRIPT.sh
# 自动执行：审计 → 备份 → 替换 → 验证

#!/bin/bash
VIEWS=(
  "RequestLogsView"
  "ModelsView"
  "CredentialsView"
  "ProvidersView"
  "RoutingOverviewView"
  "DecisionsView"
  "TenantsView"
  "TenantDashboardView"
  "UsersView"
  "SessionListView"
)

for view in "${VIEWS[@]}"; do
  echo "=== 审计 $view ==="
  python3 -c "
import re
try:
    with open('web/src/views/$view.vue', 'r') as f:
        content = f.read()
    chinese = re.findall(r'[\u4e00-\u9fa5]', content)
    i18n_count = len(re.findall(r'useI18n|\$t\\(', content))
    print(f'  硬编码字符：{len(chinese)}')
    print(f'  i18n 调用：{i18n_count}')
except FileNotFoundError:
    print(f'  文件不存在')
"
done
```

---

## 💡 关键洞察

### 优势：locale 已存在
- 44 模块 × 8 语言文件已就位
- 大量翻译工作已完成（从参考项目复制）
- 迁移工作**仅需修改 .vue 文件**

### 劣势：工作量仍然很大
- 即使有 locale，仍需逐视图修改
- 每个视图平均 1500+ 字符硬编码
- 需要耐心和细致

### 建议
- **从高频访问的页面开始**：RequestLogsView
- **复用模式**：第一个视图迁移后，其他视图可参考
- **保持构建通过**：每改完一个视图就验证

---

## 📞 决策点

### 问题 1：是否立即开始 RequestLogsView 迁移？
这是最高频访问的页面，建议立即开始。

### 问题 2：是否一次完成 10 个 P0 视图？
- 一次性：18 小时连续工作
- 分批：每批 4-6 小时，分 3 次完成

### 问题 3：是否需要专业翻译？
当前 locale 文件已有翻译，但可能需要针对官方部署项目的特性调整。

---

**计划完成。等待确认后开始 RequestLogsView 迁移。**
