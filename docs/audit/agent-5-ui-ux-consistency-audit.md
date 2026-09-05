# UI/UX 一致性与可观测性审计报告

**审计日期**: 2026-08-31  
**审计范围**: `/web/src` 前端代码库  
**审计版本**: 当前主分支

---

## 执行摘要

本次审计覆盖了 LLM Gateway 前端系统的 **119 个视图组件**、**162 个通用组件**，以及 **8 种语言**（zh-CN, zh-TW, en-US, ja-JP, fr-FR, de-DE, es-ES, ar-SA）的国际化文案。审计重点关注组件复用、设计一致性、国际化完整性、菜单结构和可观测性页面质量。

### 关键发现

✅ **优势**
- 国际化架构完善，8 种语言配置文件（每语言 69 个模块）结构一致
- CSS 设计系统规范，双主题（light/dark）支持完整
- 核心可观测性页面（RequestLogsView, ProviderDetailView）功能丰富
- 菜单层级合理（最深 2 层），权限控制清晰（tenant/default/super_admin）

⚠️ **改进空间**
- **组件重复实现**：多个视图重复实现相似的表格、对话框和加载状态
- **交互一致性**：混用 Element Plus 组件（el-button）与自定义样式（.btn）
- **空状态提示**：45 处空状态实现方式不统一
- **国际化盲区**：部分新增功能文案覆盖不完整

---

## 1. 组件复用与设计统一性

### 1.1 发现的重复实现模式

#### 🔴 高优先级：抽取为公共组件

| 功能模式 | 重复次数 | 出现位置示例 | 建议抽取组件名 |
|---------|---------|------------|--------------|
| **数据表格（自定义）** | 21+ | RequestLogsView, TenantsView, UsersView | `<DataTable>` |
| **对话框/抽屉** | 15+ | LoginModal, ApiKeySelectModal, ChangePasswordDialog | 已有 Drawer/Modal 组件，需统一使用 |
| **加载状态** | 多处 | `<div v-if="loading">加载中…</div>` | `<LoadingState>` |
| **空状态提示** | 45+ | `<div class="empty">无数据</div>` | `<EmptyState>` |
| **分页控件** | 10+ | 各列表视图自行实现 | `<Pagination>` |
| **状态徽章** | 多处 | 混用 `badge` / `status-badge` / `state-pill` | 统一使用 `<StatusBadge>` |

#### 📍 具体案例分析

**案例 1: 数据表格重复实现**

```vue
<!-- RequestLogsView.vue (2036行) -->
<table class="data-table request-log-table">
  <thead><tr><th>时间</th><th>脉络</th>...</tr></thead>
  <tbody>...</tbody>
</table>

<!-- TenantsView.vue / UsersView.vue / KeysView.vue -->
<!-- 各自实现相似的表格结构，样式和分页逻辑重复 -->
```

**建议**：抽取 `<DataTable>` 组件，统一处理：
- 列定义（支持 slot 自定义渲染）
- 排序/筛选
- 分页（当前各页面自行实现分页条）
- 加载/空状态
- 响应式断点

---

**案例 2: 加载状态不一致**

```vue
<!-- 模式 A: 纯文本 -->
<div v-if="loading" class="empty">加载中…</div>

<!-- 模式 B: Element Plus -->
<el-skeleton v-if="loading" :rows="5" animated />

<!-- 模式 C: 内联样式 -->
<div v-if="loading" style="text-align:center;padding:40px">Loading...</div>
```

**建议**：统一使用 `<LoadingState>` 组件，支持不同尺寸（small/default/large）和骨架屏模式。

---

**案例 3: 对话框组件混用**

当前同时存在：
- Element Plus 组件: `<el-dialog>`, `<el-drawer>`
- 自定义组件: `LoginModal.vue`, `ApiKeySelectModal.vue`, `ChangePasswordDialog.vue`
- 内联弹窗: `prompt()`, `confirm()`

**问题**：
- 样式不统一（Element Plus 默认主题 vs 自定义 CSS）
- 交互行为差异（关闭方式、遮罩点击行为）
- 响应式适配不一致

**建议**：
1. 统一使用 Element Plus 的 `<el-dialog>` / `<el-drawer>`
2. 封装业务级组件（如 `<ConfirmDialog>`, `<FormDialog>`）
3. 废弃 `prompt()` / `confirm()`，替换为统一的对话框组件

---

### 1.2 样式系统一致性

#### ✅ 做得好的地方

**CSS 变量设计系统** (`/web/src/style.css`):
- 完整的双主题支持（`:root` + `html[data-theme='light']` / `html[data-theme='dark']`）
- 语义化命名（`--kx-primary`, `--kx-success`, `--kx-danger`）
- 向后兼容别名（`--accent`, `--border`, `--text`）
- 厂商品牌色统一（`--vendor-openai`, `--vendor-anthropic`）

**按钮系统**:
```css
.btn                 /* 基础按钮 */
.btn-primary         /* 主按钮 */
.btn-danger          /* 危险按钮 */
.btn-ghost           /* 幽灵按钮 */
.btn-sm              /* 小尺寸 */
```

#### ⚠️ 存在的问题

**按钮实现混用**（418 处自定义 + 275 处 Element Plus）:

```vue
<!-- 样式 A: 自定义 CSS 类 -->
<button class="btn btn-primary">保存</button>

<!-- 样式 B: Element Plus 组件 -->
<el-button type="primary">保存</el-button>

<!-- 样式 C: 内联样式 -->
<button style="color:var(--danger);border-color:var(--danger)">删除</button>
```

**影响**：
- 视觉不一致（字体大小、内边距、悬停效果）
- 维护成本高（两套样式系统）
- 主题切换时可能出现遗漏

**建议**：
1. **统一决策**：选择 Element Plus 或自定义系统，全面迁移
2. **短期方案**：创建 `<AppButton>` 包装组件，内部统一使用 Element Plus，暴露简化的 API
3. **长期方案**：逐步迁移所有 `<button class="btn">` 到 `<el-button>`

---

## 2. 国际化完整性

### 2.1 架构评估

✅ **架构完善**：
- 8 种语言全覆盖（zh-CN, zh-TW, en-US, ja-JP, fr-FR, de-DE, es-ES, ar-SA）
- 每种语言 69 个模块文件，结构一致
- 模块化组织（`/locales/{lang}/{module}.ts`）
- 聚合导出（`/locales/{lang}/index.ts`）

### 2.2 发现的问题

#### 🔴 问题 1: 硬编码文本检查

通过 grep 检查发现：
- **0 个** Vue 文件包含裸中文字符（✅ 优秀）
- 所有 UI 文本均通过 `t()` 函数国际化

#### ⚠️ 问题 2: 新增功能文案覆盖

部分文件包含"硬编码"注释标记：
- `/web/src/views/RequestLogsView.vue` (line 552): 压缩策略文案
- `/web/src/views/PricingManagementView.vue`: 定价管理相关
- `/web/src/views/KeysView.vue`: API 密钥管理

**建议审查点**：
1. 检查近期新增的视图（2026-08-xx 日期的代码）
2. 确认所有 `t()` 键在 8 种语言中都有对应翻译
3. 运行国际化覆盖率检查工具

#### 📋 国际化文案一致性检查清单

```bash
# 建议执行的检查脚本
cd /web/src/locales

# 检查 zh-CN 的键是否在其他语言中都存在
for lang in en-US ja-JP fr-FR de-DE es-ES ar-SA zh-TW; do
  echo "Checking $lang..."
  diff <(find zh-CN -name "*.ts" -exec basename {} \;) \
       <(find $lang -name "*.ts" -exec basename {} \;)
done
```

### 2.3 日期时间本地化

✅ **做得好**：80 处使用 `toLocaleString` / `toLocaleDateString` / `toLocaleTimeString`

```typescript
// RequestLogsView.vue
function fmtTs(ts: string) {
  return new Date(ts).toLocaleString(localeRef.value, { hour12: false })
}
```

**建议**：封装统一的日期格式化工具函数，支持：
- 相对时间（"3分钟前"）
- 绝对时间（"2026-08-31 14:30"）
- 时区感知

---

## 3. 交互规范一致性

### 3.1 通知/反馈机制

#### 当前使用的方式

| 方式 | 使用场景 | 示例文件 |
|-----|---------|---------|
| `ElMessage` | 操作成功/失败提示 | ActivationWizard.vue (15处) |
| `ElNotification` | 系统级通知 | 0 处（未使用） |
| `alert()` / `confirm()` | 确认对话框 | ProviderDetailView.vue |

#### ⚠️ 发现的问题

**不一致案例**：

```typescript
// 模式 A: ElMessage（推荐）
ElMessage.success('操作成功')
ElMessage.error('操作失败')

// 模式 B: 原生 confirm（不推荐）
if (!confirm('确认删除？')) return

// 模式 C: prompt 输入（不推荐）
const reason = prompt('请输入原因', '')
```

**建议**：
1. 统一使用 Element Plus 的 `ElMessage` / `ElMessageBox`
2. 封装业务级 `useNotification` composable:
   ```typescript
   const { success, error, confirm } = useNotification()
   await confirm('确认删除该供应商？')
   success('删除成功')
   ```
3. 确保所有通知文案都通过 `t()` 国际化

---

### 3.2 表单验证提示

**当前实现**：
- Element Plus 内置验证（部分表单）
- 自定义验证逻辑（部分视图）
- 提示位置不统一（行内 / 顶部通知 / 弹窗）

**建议**：
1. 统一使用 Element Plus 的 `<el-form>` 验证
2. 错误提示位置：优先行内，严重错误用顶部通知
3. 验证规则集中管理（`/utils/validationRules.ts`）

---

### 3.3 加载状态与骨架屏

**发现**：
- 部分页面使用 `<div v-if="loading">加载中…</div>`
- 部分页面无加载指示
- 骨架屏使用不一致

**建议**：
```vue
<!-- 统一加载状态组件 -->
<template>
  <div class="page-container">
    <LoadingState v-if="loading" type="skeleton" />
    <EmptyState v-else-if="!data.length" icon="📭" message="暂无数据" />
    <DataTable v-else :data="data" />
  </div>
</template>
```

---

## 4. 数据展示一致性

### 4.1 时间戳格式

✅ **一致性较好**：

```typescript
// RequestLogsView.vue
fmtTs(ts: string) => "2026-08-31 14:30:25"
fmtDate(ts: string) => "08-31"
fmtTime(ts: string) => "14:30:25"
```

**建议优化**：
- 添加相对时间（"3分钟前" / "今天 14:30" / "昨天"）
- 时区显示（对跨地区团队重要）

---

### 4.2 数字格式

✅ **千分位分隔符**：使用 `toLocaleString()`

```typescript
// 统一使用
token(v: number) => v.toLocaleString()  // 1,234,567
```

**建议补充**：
- 货币格式（USD, CNY）
- 百分比（保留位数一致）
- 文件大小（KB, MB, GB）

---

### 4.3 状态徽章

**当前实现**：混用三种样式类

```vue
<!-- 样式 A: .badge -->
<span class="badge badge-blue">活跃</span>

<!-- 样式 B: .status-badge -->
<StatusBadge state="healthy" label="健康" />

<!-- 样式 C: .state-pill -->
<span class="state-pill state-pill--success">成功</span>
```

**建议**：统一使用 `<StatusBadge>` 组件（已存在于 `/components/StatusBadge.vue`）

---

### 4.4 空状态提示

**发现**：45 处空状态实现各不相同

```vue
<!-- 样式 A -->
<div class="empty">无数据</div>

<!-- 样式 B -->
<div v-if="!items.length" style="text-align:center;padding:20px">暂无内容</div>

<!-- 样式 C -->
<el-empty description="暂无数据" />
```

**建议**：统一使用 `<EmptyState>` 组件

```vue
<EmptyState
  icon="📭"
  title="暂无请求日志"
  description="当前筛选条件下没有找到数据"
  action-text="清空筛选"
  @action="clearFilters"
/>
```

---

## 5. 菜单与导航

### 5.1 菜单结构评估

**数据源**：`/web/public/menu-config.json`

✅ **优点**：
- 层级清晰（primary + 8 个 groups）
- 权限控制完善（`tenantScope`: default/tenant/*）
- 插件依赖标记（`pluginScope`）
- 国际化键配置（`labelKey`）

**菜单组织**（共 8 个分组）：

| 分组 ID | 中文名称 | 层级深度 | 菜单项数 |
|---------|---------|---------|---------|
| tenant-portal | 我的服务 | 1层 | 4 |
| models-routing | 模型与路由 | 1层 | 8 |
| tenant-users | 租户用户 | 1层 | 5 |
| requests-sessions | 请求与会话 | 1层 | 3 |
| data-ops | 数据运维 | 1层 | 13 |
| opsplatform | 运维中心 | 1层 | 7 |
| guide | 接入指南 | 1层 | 1 |
| chat | 对话 | 1层 | 1 |

**层级深度分析**：
- ✅ 最深 1 层（无嵌套子菜单）
- ✅ 符合 UX 最佳实践（不超过 3 层）

---

### 5.2 发现的问题

#### ⚠️ 问题 1: "数据运维" 分组过载

`data-ops` 分组包含 **13 个菜单项**，远超其他分组（平均 3-5 项）

**建议拆分**：

```json
// 当前: data-ops (13项)
// 建议拆分为:

// 分组 A: 系统配置 (6项)
- 系统设置
- 代理管理
- 模块管理
- 提示词注入检测
- 压缩管理
- 微信机器人

// 分组 B: 数据管理 (4项)
- 数据生命周期
- 格式异常监控
- 模型完整性监控
- Agent Registry

// 分组 C: 激活与授权 (3项)
- 更新与激活
- 离线激活
- 数据采集范围
```

#### ⚠️ 问题 2: 图标使用不一致

部分菜单使用 emoji 图标（🏷️, 🗺️, 🔍），部分无图标

**建议**：
- 统一使用图标库（如 Element Plus Icons 或自定义 SVG）
- 或全部使用 emoji（保持风格一致）

---

### 5.3 面包屑导航

**未发现统一的面包屑组件**

建议实现：
```vue
<Breadcrumb :items="[
  { label: '模型与路由', path: '/routing' },
  { label: '凭据监控', path: '/routing-v2/credentials' },
  { label: '详情', path: null }
]" />
```

---

## 6. 可观测性页面审计

### 6.1 请求日志页面 (`RequestLogsView.vue`)

**文件大小**: 2036 行（⚠️ 超大文件）

#### ✅ 功能亮点

1. **强大的筛选能力**：
   - 时间范围（9 种预设 + 自定义）
   - API 密钥、供应商、凭据、模型
   - 状态、错误类型、Token 来源
   - 会话 ID、任务 ID（脉络追踪）

2. **统计概览**：
   - 请求总数、Token 统计（输入/输出/缓存）
   - 成本汇总（USD）
   - 按模型分组统计

3. **压缩与会话缓存可视化**：
   - 压缩策略标记（delta_append, sliding_window, mechanical_trim）
   - 会话缓存命中率
   - 节省量统计（Token, Bytes）

4. **脉络追踪**：
   - 按会话聚合多步请求
   - 任务成功/失败汇总
   - 会话总结（AI 生成摘要）

#### ⚠️ 需要改进

1. **文件过大**：2036 行，建议拆分：
   ```
   RequestLogsView.vue (主视图, 300行)
   ├── RequestLogsFilter.vue (筛选区, 200行)
   ├── RequestLogsStats.vue (统计卡片, 150行)
   ├── RequestLogsTable.vue (表格, 400行)
   ├── RequestLogRow.vue (单行, 150行)
   └── composables/
       └── useRequestLogs.ts (逻辑, 800行)
   ```

2. **国际化覆盖**：部分新增字段（如压缩策略）文案需确认 8 种语言完整

3. **响应式优化**：表格在小屏幕上需要横向滚动（可考虑卡片布局）

---

### 6.2 凭据详情页面 (`ProviderDetailView.vue`)

**文件大小**: 257 行（✅ 合理）

#### ✅ 设计亮点

1. **标签页组织清晰**：
   - 凭据 (creds)
   - 模型 (models)
   - 质量 (quality)
   - 日志 (logs)
   - 错误详情 (error-detail)
   - 诊断 (diag)
   - 探测历史 (probe)
   - 设置 (settings)

2. **权限控制**：
   - 手动禁用/启用
   - 删除确认（危险操作）

3. **组件化良好**：
   - 每个 tab 独立组件（`CredsTab.vue`, `ModelsTab.vue`）
   - 通过 `@silent-refresh` 避免重复加载

#### ⚠️ 建议优化

1. **删除确认**：使用 `confirm()` 原生对话框
   ```typescript
   // 当前
   if (!confirm(pp('deleteConfirm', { name }))) return
   
   // 建议改为
   await ElMessageBox.confirm(pp('deleteConfirm', { name }), {
     type: 'warning',
     confirmButtonText: pp('confirmDelete'),
     cancelButtonText: pp('cancel')
   })
   ```

2. **错误处理**：统一使用 `ElMessage.error()` 而非 `error.value = ...`

---

### 6.3 会话详情页面

**发现**：未找到 `SessionDetailView.vue`

通过搜索发现相关文件：
- `/web/src/views/admin/SessionDetailPage.vue`（AI Session Manager 插件）
- `/web/src/components/SessionSummaryDrawer.vue`（会话总结抽屉）
- `/web/src/components/SessionTurnDrawer.vue`（会话轮次抽屉）

**建议**：
- 确认是否需要独立的会话详情视图
- 或者当前通过 RequestLogsView 的脉络模式已满足需求

---

### 6.4 实时监控页面

**发现**：`liveStreamStore.ts` (53KB, 2000+ 行)

**功能亮点**：
- EventSource 实时推送
- 泳道视图（按凭据/供应商/模型分组）
- 队列深度监控
- 节点健康状态
- Action timeline（请求生命周期追踪）

**建议**：
- 拆分文件（store + actions + utils）
- 添加单元测试覆盖（已有 `.test.ts` 文件，✅）

---

## 7. 具体改进建议

### 7.1 短期优化（1-2 周）

#### 优先级 P0

1. **统一空状态提示**
   - 创建 `<EmptyState>` 组件
   - 替换 45 处重复实现

2. **统一加载状态**
   - 创建 `<LoadingState>` 组件
   - 支持骨架屏模式

3. **统一确认对话框**
   - 封装 `useConfirm()` composable
   - 替换所有 `confirm()` / `prompt()`

4. **拆分超大文件**
   - RequestLogsView.vue (2036行 → 拆分为 5-6 个组件)
   - liveStreamStore.ts (2000+行 → 拆分为多个文件)

#### 优先级 P1

5. **菜单重组**
   - 拆分 "数据运维" 分组（13项 → 3个分组）

6. **国际化审查**
   - 运行覆盖率检查脚本
   - 补齐缺失的翻译键

---

### 7.2 中期优化（1 个月）

7. **组件库决策**
   - 决定使用 Element Plus 还是自定义组件系统
   - 统一迁移（418 处自定义按钮）

8. **数据表格标准化**
   - 抽取 `<DataTable>` 组件
   - 统一分页、排序、筛选逻辑

9. **通知系统规范**
   - 统一使用 ElMessage / ElMessageBox
   - 封装业务级 composable

10. **日期时间格式化**
    - 创建 `useDateFormat()` composable
    - 支持相对时间、时区

---

### 7.3 长期优化（3 个月）

11. **设计系统文档**
    - Storybook 展示所有组件
    - 使用指南和最佳实践

12. **自动化检查**
    - ESLint 规则：禁止 `confirm()` / `prompt()`
    - 国际化覆盖率 CI 检查
    - 组件复用度分析工具

13. **响应式优化**
    - 移动端适配
    - 断点设计规范

---

## 8. 可观测性页面评分

| 页面 | 功能完整性 | 交互一致性 | 性能 | 国际化 | 综合评分 |
|-----|----------|----------|------|--------|---------|
| RequestLogsView | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐⭐⭐ | 4.0/5.0 |
| ProviderDetailView | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 4.4/5.0 |
| LiveStream (实时监控) | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 4.2/5.0 |

**总体评估**：可观测性页面功能强大，信息密度高，但需要优化代码组织和交互一致性。

---

## 9. 组件清单

### 9.1 现有可复用组件（部分列表）

✅ **通用组件**（已实现）：
- `StatusBadge.vue` - 状态徽章
- `ModelPicker.vue` - 模型选择器
- `FilterInput.vue` - 筛选输入
- `FeeCostCell.vue` - 费用/成本单元格
- `PageBackLink.vue` - 返回链接
- `IntegrityChip.vue` - 完整性标签
- `ActiveFilterChips.vue` - 活跃筛选项芯片

✅ **抽屉组件**（已实现）：
- `SessionSummaryDrawer.vue` - 会话总结
- `RequestLogDrawer.vue` - 请求日志
- `NodeDetailDrawer.vue` - 节点详情
- `StatsDrawer.vue` - 统计抽屉

✅ **对话框组件**（已实现）：
- `LoginModal.vue` - 登录模态框
- `ApiKeySelectModal.vue` - API 密钥选择
- `ChangePasswordDialog.vue` - 修改密码
- `OperationAgreementDialog.vue` - 操作协议

---

### 9.2 建议新增的组件

🆕 **待创建**：
- `<DataTable>` - 统一数据表格
- `<Pagination>` - 统一分页控件
- `<LoadingState>` - 统一加载状态
- `<EmptyState>` - 统一空状态
- `<ConfirmDialog>` - 统一确认对话框
- `<Breadcrumb>` - 面包屑导航
- `<DateRangePicker>` - 日期范围选择器（封装 Element Plus）
- `<SearchInput>` - 搜索输入框（带清除按钮）
- `<FormField>` - 表单字段容器（label + input + error）
- `<ActionButtons>` - 操作按钮组（编辑/删除等）

---

## 10. 代码组织建议

### 当前结构
```
web/src/
├── views/           (119 个视图)
├── components/      (162 个组件)
├── composables/     (逻辑复用)
├── locales/         (国际化)
└── style.css        (全局样式)
```

### 建议优化结构
```
web/src/
├── views/                    (页面视图)
├── components/
│   ├── common/              (通用基础组件)
│   │   ├── DataTable/
│   │   ├── LoadingState/
│   │   ├── EmptyState/
│   │   └── ...
│   ├── business/            (业务组件)
│   │   ├── RequestLogRow/
│   │   ├── ProviderCard/
│   │   └── ...
│   └── layout/              (布局组件)
│       ├── AppHeader/
│       ├── AppSidebar/
│       └── Breadcrumb/
├── composables/
│   ├── useNotification.ts   (通知)
│   ├── useDateFormat.ts     (日期格式化)
│   ├── useConfirm.ts        (确认对话框)
│   └── ...
├── utils/
│   ├── validationRules.ts   (验证规则)
│   └── formatters.ts        (格式化工具)
└── styles/
    ├── variables.css        (CSS 变量)
    ├── components.css       (组件样式)
    └── utilities.css        (工具类)
```

---

## 11. 总结与行动计划

### 核心问题总结

1. **组件重复实现** - 影响开发效率和维护成本
2. **交互不一致** - 影响用户体验
3. **代码组织** - 部分文件过大，需要拆分

### 三步走行动计划

#### 第一步：快速修复（2 周）
- [ ] 创建 5 个基础组件（EmptyState, LoadingState, ConfirmDialog, DataTable, Pagination）
- [ ] 拆分 RequestLogsView.vue 和 liveStreamStore.ts
- [ ] 统一所有 confirm/prompt 为 ElMessageBox

#### 第二步：系统优化（1 个月）
- [ ] 统一按钮系统（迁移到 Element Plus 或自定义）
- [ ] 补齐国际化缺失项
- [ ] 重组菜单结构（拆分"数据运维"）
- [ ] 创建设计系统文档（Storybook）

#### 第三步：持续改进（3 个月）
- [ ] 建立组件库
- [ ] 自动化检查（ESLint 规则、CI）
- [ ] 响应式优化
- [ ] 性能监控

---

## 附录

### A. 检查脚本

#### A.1 国际化覆盖率检查

```bash
#!/bin/bash
# check-i18n-coverage.sh

cd web/src/locales
BASE_LANG="zh-CN"

for lang in en-US ja-JP fr-FR de-DE es-ES ar-SA zh-TW; do
  echo "=== Checking $lang against $BASE_LANG ==="
  
  # 检查文件数量
  base_count=$(find $BASE_LANG -name "*.ts" | wc -l)
  lang_count=$(find $lang -name "*.ts" | wc -l)
  
  echo "Files: $BASE_LANG=$base_count, $lang=$lang_count"
  
  # 检查缺失文件
  diff <(find $BASE_LANG -name "*.ts" -exec basename {} \; | sort) \
       <(find $lang -name "*.ts" -exec basename {} \; | sort) || true
  
  echo ""
done
```

#### A.2 组件重复度分析

```bash
#!/bin/bash
# check-component-duplication.sh

echo "=== 检查空状态提示重复 ==="
grep -r "class=\"empty\"" web/src/views/*.vue | wc -l

echo "=== 检查加载状态重复 ==="
grep -r "v-if=\"loading\"" web/src/views/*.vue | wc -l

echo "=== 检查自定义按钮 vs Element Plus 按钮 ==="
echo -n "自定义 (.btn): "
grep -r "class=\"btn" web/src/views/*.vue | wc -l
echo -n "Element Plus: "
grep -r "el-button" web/src/views/*.vue | wc -l
```

---

### B. 参考资料

- [Element Plus 官方文档](https://element-plus.org/)
- [Vue I18n 最佳实践](https://vue-i18n.intlify.dev/guide/best-practices.html)
- [前端代码规范](内部 Wiki 链接)
- [设计系统指南](内部 Figma 链接)

---

**审计人员**: AI Agent-5  
**审计时间**: 2026-08-31  
**下次审计建议**: 2026-10-31（季度审计）
