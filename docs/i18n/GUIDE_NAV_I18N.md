# i18n 优化实施指南 — 阶段 1.2 导航栏

> 当前进度：25% (7/16 任务完成)  
> 本文档：导航栏 i18n 迁移详细步骤

---

## 当前状态

### ✅ 已完成
- 首页 100% i18n 化（8 种语言）
- 44 模块 locale 结构已就位
- 构建验证通过

### 🚧 当前任务
**阶段 1.2：导航栏 i18n**（预估 3 小时）

---

## 步骤 1：为 NavItem 添加 labelKey 字段

### 1.1 修改类型定义

**文件**: `web/src/config/appNav.ts`

**修改**:
```typescript
// 在 line 3-20 的 NavItem 类型定义中添加 labelKey
export type NavItem = {
  path: string
  label: string           // 保留作为 fallback
  labelKey?: string       // 新增：i18n 键名
  icon: string
  super?: boolean
  platformOps?: boolean
  tenantOnly?: boolean
  hideForTenant?: boolean
  exact?: boolean
}
```

### 1.2 为所有导航项添加 labelKey

**修改 NAV_PRIMARY_ITEMS**（line 29-31）:
```typescript
export const NAV_PRIMARY_ITEMS: NavItem[] = [
  { 
    path: '/', 
    label: '总览',              // 保留中文 fallback
    labelKey: 'nav.item.overview',  // 新增
    icon: '📊', 
    platformOps: true 
  },
]
```

**修改 NAV_GROUPS**（line 33-100）:

为每个 item 添加 `labelKey` 和每个 group 添加 `labelKey`：

```typescript
export const NAV_GROUPS: NavGroup[] = [
  {
    id: 'tenant-portal',
    label: '我的服务',
    labelKey: 'nav.group.tenantPortal',  // 新增
    items: [
      { 
        path: '/tenant/models', 
        label: '标准模型', 
        labelKey: 'nav.item.tenantModels',  // 新增
        icon: '🤖', 
        tenantOnly: true 
      },
      { 
        path: '/tenant/account', 
        label: '我的账户', 
        labelKey: 'nav.item.tenantAccount',
        icon: '💰', 
        tenantOnly: true 
      },
      // ... 其他 item 类似添加
    ],
  },
  // ... 其他 group 类似添加
]
```

**完整的 labelKey 映射表**（共 30+ 个）:

| 原 label | labelKey |
|---|---|
| 总览 | `nav.item.overview` |
| 我的服务 | `nav.group.tenantPortal` |
| 标准模型 | `nav.item.tenantModels` |
| 我的账户 | `nav.item.tenantAccount` |
| 套餐与充值 | `nav.item.tenantPricing` |
| 我的消耗 | `nav.item.tenantUsage` |
| 模型与路由 | `nav.group.modelsRouting` |
| 模型与目录 | `nav.item.models` |
| 路由全景 | `nav.item.routingOverview` |
| 凭据监控 | `nav.item.credentialMonitor` |
| 探测健康度 | `nav.item.probeHealth` |
| 供应商 | `nav.item.providers` |
| 成本价格 | `nav.item.pricing` |
| 定价管理 | `nav.item.modelPricing` |
| 免费资源 | `nav.item.freePool` |
| 租户用户 | `nav.group.tenantUsers` |
| 租户管理 | `nav.item.tenants` |
| 用户管理 | `nav.item.users` |
| API 密钥 | `nav.item.keys` |
| 密钥申请 | `nav.item.keyApplications` |
| 审计日志 | `nav.item.auditLogs` |
| 请求与会话 | `nav.group.requestsSessions` |
| 请求日志 | `nav.item.requestLogs` |
| 会话列表 | `nav.item.sessions` |
| 会话对比 | `nav.item.sessionCompare` |
| 压缩概览 | `nav.item.compression` |
| 会话上下文 | `nav.item.sessionContext` |
| 数据运维 | `nav.group.dataOps` |
| 系统设置 | `nav.item.settings` |
| 数据生命周期 | `nav.item.dataLifecycle` |
| 格式异常监控 | `nav.item.formatAnomalies` |
| 模块管理 | `nav.item.modules` |
| Agent Registry | `nav.item.agents` |
| 接入指南 | `nav.group.guide` |
| 接入示例 | `nav.item.examples` |
| 对话 | `nav.group.chat` / `nav.item.chat` |

---

## 步骤 2：修改 App.vue 使用 t()

**文件**: `web/src/App.vue`

### 2.1 导入 useI18n

在 `<script setup>` 顶部添加：
```typescript
import { useI18n } from 'vue-i18n'
const { t } = useI18n()
```

### 2.2 修改侧边栏渲染逻辑

查找渲染导航项的部分（大约 line 170-220），将：
```vue
<!-- 旧代码 -->
<span class="nav-item-label">{{ item.label }}</span>
```

改为：
```vue
<!-- 新代码 -->
<span class="nav-item-label">{{ item.labelKey ? t(item.labelKey) : item.label }}</span>
```

### 2.3 修改 group 标题渲染

查找渲染 group 标题的部分，将：
```vue
<!-- 旧代码 -->
<h3 class="group-title">{{ group.label }}</h3>
```

改为：
```vue
<!-- 新代码 -->
<h3 class="group-title">{{ group.labelKey ? t(group.labelKey) : group.label }}</h3>
```

---

## 步骤 3：更新 locale 文件

### 3.1 中文 (zh-CN/nav.ts)

**文件**: `web/src/locales/zh-CN/nav.ts`

**内容**（已存在参考项目，需要补齐当前项目的键）:
```typescript
export default {
  collapseSidebar: '折叠侧边栏',
  expandSidebar: '展开侧边栏',
  
  group: {
    tenantPortal: '我的服务',
    modelsRouting: '模型与路由',
    tenantUsers: '租户用户',
    requestsSessions: '请求与会话',
    dataOps: '数据运维',
    guide: '接入指南',
    chat: '对话',
  },
  
  item: {
    overview: '总览',
    tenantModels: '标准模型',
    tenantAccount: '我的账户',
    tenantPricing: '套餐与充值',
    tenantUsage: '我的消耗',
    models: '模型与目录',
    routingOverview: '路由全景',
    credentialMonitor: '凭据监控',
    probeHealth: '探测健康度',
    providers: '供应商',
    pricing: '成本价格',
    modelPricing: '定价管理',
    freePool: '免费资源',
    tenants: '租户管理',
    users: '用户管理',
    keys: 'API 密钥',
    keyApplications: '密钥申请',
    auditLogs: '审计日志',
    requestLogs: '请求日志',
    sessions: '会话列表',
    sessionCompare: '会话对比',
    compression: '压缩概览',
    sessionContext: '会话上下文',
    settings: '系统设置',
    dataLifecycle: '数据生命周期',
    formatAnomalies: '格式异常监控',
    modules: '模块管理',
    agents: 'Agent Registry',
    examples: '接入示例',
    chat: '对话',
  },
}
```

### 3.2 英文 (en-US/nav.ts)

**文件**: `web/src/locales/en-US/nav.ts`

```typescript
export default {
  collapseSidebar: 'Collapse Sidebar',
  expandSidebar: 'Expand Sidebar',
  
  group: {
    tenantPortal: 'My Services',
    modelsRouting: 'Models & Routing',
    tenantUsers: 'Tenants & Users',
    requestsSessions: 'Requests & Sessions',
    dataOps: 'Data Operations',
    guide: 'Integration Guide',
    chat: 'Chat',
  },
  
  item: {
    overview: 'Overview',
    tenantModels: 'Standard Models',
    tenantAccount: 'My Account',
    tenantPricing: 'Plans & Top-up',
    tenantUsage: 'My Usage',
    models: 'Models & Catalog',
    routingOverview: 'Routing Overview',
    credentialMonitor: 'Credential Monitor',
    probeHealth: 'Probe Health',
    providers: 'Providers',
    pricing: 'Cost Pricing',
    modelPricing: 'Pricing Management',
    freePool: 'Free Resources',
    tenants: 'Tenant Management',
    users: 'User Management',
    keys: 'API Keys',
    keyApplications: 'Key Applications',
    auditLogs: 'Audit Logs',
    requestLogs: 'Request Logs',
    sessions: 'Sessions',
    sessionCompare: 'Session Compare',
    compression: 'Compression Overview',
    sessionContext: 'Session Context',
    settings: 'System Settings',
    dataLifecycle: 'Data Lifecycle',
    formatAnomalies: 'Format Anomalies',
    modules: 'Module Management',
    agents: 'Agent Registry',
    examples: 'Examples',
    chat: 'Chat',
  },
}
```

### 3.3 其他 6 种语言

使用相同的结构，翻译对应语言。可以使用 LLM 批量翻译（参考首页翻译的方法）。

---

## 步骤 4：验证

### 4.1 本地开发测试

```bash
cd ~/workspace/official-deploy/services/llm-gateway-go/web
npm run dev
# 访问 http://localhost:5780
# 登录后，依次切换 8 种语言
# 检查侧边栏导航项是否正确显示对应语言
```

### 4.2 构建验证

```bash
npm run build
# 应无错误
```

### 4.3 检查清单

- [ ] 所有导航项标签随语言切换而改变
- [ ] 无 `[vue-i18n] Not found 'nav.xxx'` 警告
- [ ] 中文用户看到中文导航
- [ ] 英文用户看到英文导航
- [ ] 其他 6 种语言用户看到对应语言

---

## 步骤 5：提交

```bash
cd ~/workspace/official-deploy/services/llm-gateway-go
git add web/src/config/appNav.ts web/src/App.vue web/src/locales/*/nav.ts
git commit -m "feat(i18n): 导航栏完全 i18n 化

- appNav.ts: 为所有 NavItem/NavGroup 添加 labelKey 字段
- App.vue: 渲染时优先使用 t(labelKey) || label
- locales/*/nav.ts: 添加 30+ 个导航项翻译（8 种语言）

覆盖范围：
- 7 个导航组（tenant-portal, models-routing, tenant-users, ...）
- 30+ 个导航项（overview, models, providers, ...）
- fallback 机制：labelKey 不存在时显示 label（中文）

阶段 1.2 完成。"
```

---

## 预期工作量

- 修改 appNav.ts 类型和数据：**1 小时**
- 修改 App.vue 渲染逻辑：**30 分钟**
- 创建 8 × nav.ts 文件：**1 小时**（使用 LLM 批量翻译）
- 测试验证：**30 分钟**

**总计**: 3 小时

---

## 下一步

完成导航栏 i18n 后，继续：
- **阶段 1.3**：登录流程 i18n（2 小时）
- **阶段 1.4**：修复 parity.test.ts（1 小时）

**阶段 1 完成后**，即可部署到 184 进行全面测试。
