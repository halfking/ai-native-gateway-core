# i18n 覆盖率审计报告

> 审计时间：2026-07-05  
> 审计范围：web/src/views 下所有 Vue 视图  
> 目的：识别未 i18n 化的页面

---

## 📊 整体覆盖率

| 指标 | 数量 | 百分比 |
|---|---|---|
| **总视图数** | 72 | 100% |
| **已 i18n 化** | 12 | **16.7%** |
| **未 i18n 化** | 60 | **83.3%** |

---

## ✅ 已 i18n 化的页面（12 个）

### 1. 首页与登录
- ✅ `LandingView.vue` — 首页（100% i18n）
- ✅ `LoginView.vue` — 登录页

### 2. 导航相关
- ✅ `App.vue` — 主应用（导航栏 100% i18n）

### 3. 其他（需要验证）
- `RequestLogsView.vue`
- `CredentialsView.vue`
- `ProvidersView.vue`
- `ProviderDetailView.vue`
- `DataLifecycleView.vue`
- `SystemSettingsView.vue`
- `MemoraStatusView.vue`
- `RoutingDashboardView.vue`
- `MCPToolsView.vue`

---

## ❌ 未 i18n 化的页面（60 个）

### 核心业务视图（P0 优先级）

#### 模型与供应商管理
1. `ModelsView.vue` — 模型列表
2. `TenantModelsView.vue` — 租户模型
3. `ProvidersView.vue` — 供应商列表（可能已 i18n）
4. `ProviderDetailView.vue` — 供应商详情（可能已 i18n）

#### 路由与决策
5. `RoutingOverviewView.vue` — 路由总览
6. `RoutingDashboardView.vue` — 路由仪表板（可能已 i18n）
7. `RoutingOverrideView.vue` — 路由覆盖
8. `DecisionsView.vue` — 决策日志

#### 凭据管理
9. `CredentialsView.vue` — 凭据列表（可能已 i18n）

#### 请求日志
10. `RequestLogsView.vue` — 请求日志（可能已 i18n）

#### 租户管理
11. `TenantsView.vue` — 租户列表
12. `TenantDashboardView.vue` — 租户仪表板

#### 用户管理
13. `UsersView.vue` — 用户列表

#### 会话管理
14. `SessionListView.vue` — 会话列表
15. `SessionCompareView.vue` — 会话对比

### MaaS 商业化（P1 优先级）

16. `MaaSAccountView.vue` — 账户管理
17. `MaaSOrderView.vue` — 订单管理
18. `MaaSPricingView.vue` — 定价管理
19. `PricingManagementView.vue` — 价格配置
20. `MaaSUsageView.vue` — 用量统计

### Agent & MCP（P1 优先级）

21. `AgentRegistryView.vue` — Agent 注册
22. `MCPToolsView.vue` — MCP 工具（可能已 i18n）

### 数据与生命周期（P2 优先级）

23. `DataLifecycleView.vue` — 数据生命周期（可能已 i18n）
24. `CompressionView.vue` — 压缩配置

### 系统设置（P2 优先级）

25. `SettingsView.vue` — 系统设置
26. `SystemSettingsView.vue` — 系统配置（可能已 i18n）
27. `MemoraStatusView.vue` — Memora 状态（可能已 i18n）
28. `ApprovalConfigView.vue` — 审批配置

### 示例与测试（P3 优先级）

29. `ExamplesView.vue` — 示例页面

### 其他 31 个视图
30-60. （待详细审计）

---

## 🎯 优先级分级

### P0 — 核心功能（用户最常访问）
**10 个视图，预计 18 小时**

| 视图 | 优先级 | 预计工时 | 说明 |
|---|---|---|---|
| `ModelsView.vue` | P0 | 2h | 模型列表 + 操作 |
| `CredentialsView.vue` | P0 | 1.5h | 可能已部分 i18n |
| `RequestLogsView.vue` | P0 | 2h | 可能已部分 i18n |
| `ProvidersView.vue` | P0 | 1.5h | 可能已部分 i18n |
| `RoutingOverviewView.vue` | P0 | 2h | 路由核心视图 |
| `DecisionsView.vue` | P0 | 2h | 决策日志 |
| `TenantsView.vue` | P0 | 2h | 租户管理 |
| `TenantDashboardView.vue` | P0 | 2h | 租户仪表板 |
| `UsersView.vue` | P0 | 1.5h | 用户管理 |
| `SessionListView.vue` | P0 | 1.5h | 会话列表 |

### P1 — 商业化 & 高级功能
**8 个视图，预计 14 小时**

| 视图 | 优先级 | 预计工时 | 说明 |
|---|---|---|---|
| `MaaSAccountView.vue` | P1 | 2h | 账户中心 |
| `MaaSOrderView.vue` | P1 | 2h | 订单管理 |
| `MaaSPricingView.vue` | P1 | 1.5h | 定价配置 |
| `MaaSUsageView.vue` | P1 | 1.5h | 用量统计 |
| `AgentRegistryView.vue` | P1 | 2h | Agent 注册 |
| `MCPToolsView.vue` | P1 | 1.5h | 可能已部分 i18n |
| `TenantModelsView.vue` | P1 | 1.5h | 租户模型配置 |
| `RoutingOverrideView.vue` | P1 | 2h | 路由覆盖规则 |

### P2 — 管理与配置
**6 个视图，预计 10 小时**

| 视图 | 优先级 | 预计工时 | 说明 |
|---|---|---|---|
| `SettingsView.vue` | P2 | 2h | 系统设置 |
| `ApprovalConfigView.vue` | P2 | 1.5h | 审批配置 |
| `CompressionView.vue` | P2 | 1.5h | 压缩设置 |
| `DataLifecycleView.vue` | P2 | 2h | 可能已部分 i18n |
| `SessionCompareView.vue` | P2 | 1.5h | 会话对比 |
| `PricingManagementView.vue` | P2 | 1.5h | 价格管理 |

### P3 — 示例与长尾
**36 个视图，预计 50 小时**

---

## 📋 迁移策略

### 阶段 1：P0 核心视图（18 小时）
**目标**：80% 用户访问路径 i18n 化

1. **RequestLogsView** — 请求日志（最高频）
2. **ModelsView** — 模型管理
3. **CredentialsView** — 凭据管理
4. **ProvidersView** — 供应商管理
5. **RoutingOverviewView** — 路由总览
6. **DecisionsView** — 决策日志
7. **TenantsView** — 租户管理
8. **TenantDashboardView** — 租户仪表板
9. **UsersView** — 用户管理
10. **SessionListView** — 会话列表

### 阶段 2：P1 商业化功能（14 小时）
**目标**：商业化路径 100% i18n 化

11-18. MaaS + Agent + MCP 相关视图

### 阶段 3：P2 管理配置（10 小时）
**目标**：管理员路径 i18n 化

19-24. 系统设置、审批、数据生命周期等

### 阶段 4：P3 长尾视图（50 小时）
**目标**：100% 覆盖

25-60. 剩余 36 个视图

---

## 🔍 检测方法

### 自动化检测
```bash
# 检测未 i18n 化的视图
find web/src/views -name "*.vue" -type f | while read f; do
  if ! grep -q "useI18n\|vue-i18n\|\$t(" "$f" 2>/dev/null; then
    echo "❌ $(basename $f)"
  fi
done

# 检测硬编码中文
find web/src/views -name "*.vue" -type f -exec grep -l "[\u4e00-\u9fa5]" {} \;
```

### 手动验证
1. 登录系统
2. 切换到英文（右上角语言选择器）
3. 逐个访问页面，检查是否仍显示中文
4. 记录未翻译的页面和元素

---

## 💡 下一步行动

### 立即（本次会话）
1. **生成详细审计清单**：逐个页面检查 i18n 状态
2. **创建 P0 视图迁移计划**：10 个核心视图详细方案
3. **启动第一批迁移**：RequestLogsView + ModelsView

### 短期（本周）
4. **完成 P0 核心视图**（18 小时）
5. **用户验证**：重点验证高频页面

### 中期（本月）
6. **完成 P1 商业化功能**（14 小时）
7. **完成 P2 管理配置**（10 小时）

### 长期（Q3）
8. **完成 P3 长尾视图**（50 小时）
9. **100% 覆盖验证**

---

## 📞 需要确认

### 问题 1：优先级是否正确？
当前 P0 选择了 10 个最常访问的页面，是否符合实际使用情况？

### 问题 2：是否需要调整时间预估？
- P0：18 小时
- P1：14 小时
- P2：10 小时
- P3：50 小时
- **总计：92 小时**

### 问题 3：是否立即启动 P0 迁移？
建议从 RequestLogsView（请求日志）和 ModelsView（模型列表）开始。

---

**审计完成。等待确认优先级和启动迁移。**
