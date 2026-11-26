# Routing-v2 页面数据权限分析与实施方案
**日期**: 2026-11-26  
**页面**: https://llm.itestu.cn/routing-v2  
**目标**: 从用户视角分析关注点，实现三级权限数据过滤

---

## 一、用户角色与关注点分析

### 1.1 Super Admin（系统管理员）

**核心关注点**：全平台路由健康与策略管理

#### 关注的信息
1. **全局路由策略**
   - 路由算法配置（优先级、权重、降级策略）
   - 模型候选池管理
   - 供应商优先级排序

2. **全平台供应商凭据状态**
   - 所有供应商的凭据健康度
   - 配额使用情况（quota_state）
   - 凭据轮换历史
   - 凭据失败率统计

3. **全平台路由统计**
   - 所有租户的路由成功率
   - 模型失败率排行
   - 路由决策分布
   - 异常路由诊断

4. **系统级配置**
   - 路由评分权重（scoring-weights）
   - 特色模型配置（featured-models）
   - 紧急修复操作（emergency-repair）

**数据范围**: 无限制，看全平台

---

### 1.2 Tenant Admin（租户管理员）

**核心关注点**：本租户路由效果与可用模型

#### 关注的信息
1. **租户路由概览**
   - 本租户的路由成功率
   - 本租户最常用的模型
   - 本租户的路由失败原因分析

2. **可用模型列表**
   - 本租户可用的模型清单
   - 每个模型的可用凭据数量
   - 模型响应时间统计
   - 模型成本统计

3. **租户凭据状态**（如果有租户自建凭据）
   - 本租户配置的 custom provider 凭据
   - 本租户凭据的健康度
   - 本租户凭据的用量

4. **路由决策透明度**
   - 为什么选择了某个模型？
   - 为什么某个请求失败了？
   - 路由候选排序逻辑

**数据范围**: 限制到 `tenant_id = 自己租户`

---

### 1.3 User（普通用户）

**核心关注点**：自己请求的路由效果与可用性

#### 关注的信息
1. **个人路由统计**
   - 我的请求成功率
   - 我最常使用的模型
   - 我的请求失败原因

2. **可用模型列表**（只读）
   - 我可以使��哪些模型
   - 每个模型的响应速度
   - 每个模型的成本

3. **路由透明度**（单个请求级别）
   - 我的某个请求为什么被路由到特定模型？
   - 我的请求为什么失败了？
   - 我的请求使用了哪个供应商凭据？

4. **模型推荐**
   - 根据我的使用习惯推荐模型
   - 哪些模型当前可用性最好

**数据范围**: 限制到 `tenant_id = 自己租户 AND user_id = 自己`

---

## 二、Routing-v2 页面核心功能模块

### 2.1 路由概览（Overview）

**API**: `/api/routing/overview`

**展示内容**：
```
┌─────────────────────────────────────┐
│  路由概览 (Routing Overview)        │
├─────────────────────────────────────┤
│ 总请求数:    125,432               │
│ 成功率:      98.5%                 │
│ 平均延迟:    1.2s                  │
│ 失败请求:    1,881                 │
├─────────────────────────────────────┤
│ 热门模型 Top 5:                    │
│  1. gpt-4o (45%)                   │
│  2. claude-3.5-sonnet (28%)        │
│  3. gemini-pro (15%)               │
│  ...                               │
└─────────────────────────────────────┘
```

**权限过滤**：
- super_admin: 全平台数据
- tenant_admin: WHERE tenant_id = $1
- user: WHERE tenant_id = $1 AND user_id = $2

---

### 2.2 路由解析（Resolve）

**API**: `/api/routing/resolve`

**展示内容**：
```
┌─────────────────────────────────────┐
│  模型候选列表 (Model Candidates)    │
├─────────────────────────────────────┤
│ 模型名称        | 可用凭据 | 优先级 │
│ gpt-4o         |    5     |   1    │
│ claude-3-opus  |    3     |   2    │
│ gemini-pro     |    2     |   3    │
│ ...                                 │
├─────────────────────────────────────┤
│ 凭据健康度:                         │
│  ● OpenAI-cred-1    健康           │
│  ● Anthropic-cred-2 健康           │
│  ⚠ Google-cred-1    配额不足       │
└─────────────────────────────────────┘
```

**权限过滤**：
- super_admin: 看所有供应商凭据
- tenant_admin: 看 default 租户凭据 + 本租户 custom provider
- user: 只看可用模型列表，不看凭据详情

---

### 2.3 最近失败（Recent Failures）

**API**: `/api/routing/recent-model-failures`

**展示内容**：
```
┌─────────────────────────────────────┐
│  最近路由失败 (Recent Failures)     │
├─────────────────────────────────────┤
│ 时间  | 模型    | 原因       | 用户  │
│ 10:30 | gpt-4o  | rate_limit | 只看自己的数据 │
│ 10:25 | claude  | 503 error  | bob   │
│ 10:20 | gemini  | timeout    | carol │
└─────────────────────────────────────┘
```

**权限过滤**：
- super_admin: 全平台失败记录
- tenant_admin: WHERE tenant_id = $1
- user: WHERE tenant_id = $1 AND user_id = $2

---

### 2.4 模型树（Model Tree）

**API**: `/api/routing/model-tree`

**展示内容**：
```
┌─────────────────────────────────────┐
│  模型层级 (Model Hierarchy)         │
├─────────────────────────────────────┤
│ ├─ GPT-4 系列                       │
│ │  ├─ gpt-4o (OpenAI)              │
│ │  ├─ gpt-4-turbo (OpenAI)         │
│ │  └─ gpt-4 (Azure, OpenAI)        │
│ ├─ Claude 系列                      │
│ │  ├─ claude-3.5-sonnet            │
│ │  └─ claude-3-opus                │
│ └─ Gemini 系列                      │
│    ├─ gemini-1.5-pro               │
│    └─ gemini-pro                    │
└─────────────────────────────────────┘
```

**权限过滤**：
- 所有角色：只看可用模型（不涉及敏感数据）
- 但需要根据租户的凭据配置过滤

---

### 2.5 凭据状态监控

**API**: `/api/routing/credentials-status`（需新增或现有API）

**展示内容**：
```
┌─────────────────────────────────────┐
│  凭据健康监控 (Credentials Health)  │
├─────────────────────────────────────┤
│ 供应商  | 凭据ID | 状态 | 配额     │
│ OpenAI  | cred-1 | ✓   | 95%      │
│ OpenAI  | cred-2 | ✓   | 60%      │
│ Anthropic| cred-3| ⚠   | quota_exhausted│
│ Google  | cred-4 | ✓   | 80%      │
└─────────────────────────────────────┘
```

**权限过滤**：
- super_admin: 看所有凭据
- tenant_admin: 看 default 租户 + 自己租户的 custom provider
- user: **不可见**（凭据是敏感信息）

---

## 三、数据权限实施方案

### 3.1 核心原则

| 数据类型 | super_admin | tenant_admin | user |
|---------|-------------|--------------|------|
| **路由统计** | 全平台 | 本租户 | 自己 |
| **模型列表** | 全部 | 租户可用 | 租户可用 |
| **凭据详情** | 全部 | 部分可见 | **不可见** |
| **失败日志** | 全平台 | 本租户 | 自己 |
| **策略配置** | 可读写 | **只读** | **不可见** |

### 3.2 实施清单

#### Phase 1: 核心统计端点（P0）

| 端点 | 文件 | 需要添加的过滤 |
|------|------|--------------|
| `/api/routing/overview` | `admin/routing.go` | tenant_id + user_id 过滤 |
| `/api/routing/recent-model-failures` | `admin/routing.go` | tenant_id + user_id 过滤 |
| `/api/routing/resolve` | `admin/routing.go` | 凭据可见性过滤 |

#### Phase 2: 扩展功能端点（P1）

| 端点 | 权限要求 |
|------|---------|
| `/api/routing/model-tree` | 根据租户凭据配置过滤 |
| `/api/routing/featured-models` | 租户可用模型 |
| `/api/routing/policy` | super_admin 专属 |
| `/api/routing/scoring-weights` | super_admin 专属 |

---

## 四、实施示例

### 4.1 路由概览（Overview）

**当前实现**（需要查看实际代码）：
```go
// 文件: admin/routing.go
func (h *Handler) handleRoutingOverview(w http.ResponseWriter, r *http.Request) {
    // ❌ 当前可能没有权限过滤
    query := `
        SELECT 
            COUNT(*) as total_requests,
            SUM(CASE WHEN success THEN 1 ELSE 0 END) as successful_requests,
            AVG(latency_ms) as avg_latency
        FROM request_logs
        WHERE ts >= NOW() - INTERVAL '24 hours'
    `
}
```

**修复后**：
```go
func (h *Handler) handleRoutingOverview(w http.ResponseWriter, r *http.Request) {
    auth := GetAuthContext(r)
    tenantID := GetTenantID(r)
    
    query := `
        SELECT 
            COUNT(*) as total_requests,
            SUM(CASE WHEN success THEN 1 ELSE 0 END) as successful_requests,
            AVG(latency_ms) as avg_latency
        FROM request_logs rl
        WHERE rl.ts >= NOW() - INTERVAL '24 hours'
    `
    args := []interface{}{}
    argIdx := 1
    
    // 1. 租户过滤
    if !IsSuperAdminOrLegacy(r) {
        query += fmt.Sprintf(" AND rl.tenant_id = $%d", argIdx)
        args = append(args, tenantID)
        argIdx++
    }
    
    // 2. 用户过滤
    if auth.Role == RoleUser {
        query += fmt.Sprintf(" AND EXISTS (SELECT 1 FROM sessions s JOIN session_dimensions sd ON s.session_key = sd.session_key WHERE s.session_key = rl.session_key AND sd.owner_user = $%d)", argIdx)
        args = append(args, auth.Username)
        argIdx++
    }
    
    // 执行查询...
}
```

### 4.2 路由解析（Resolve）- 凭据可见性

```go
func (h *Handler) handleRoutingResolve(w http.ResponseWriter, r *http.Request) {
    auth := GetAuthContext(r)
    
    // 查询候选凭据
    credQuery := `
        SELECT 
            c.id,
            c.provider_id,
            c.key_prefix,
            c.quota_state,
            c.health_score
        FROM credentials c
        WHERE c.lifecycle_status = 'active'
    `
    args := []interface{}{}
    argIdx := 1
    
    // 权限过滤
    if IsSuperAdminOrLegacy(r) {
        // super_admin 看所有
    } else if IsTenantAdmin(r) {
        // tenant_admin 看 default + 自己租户
        tenantID := GetTenantID(r)
        credQuery += fmt.Sprintf(" AND (c.tenant_id = 'default' OR c.tenant_id = $%d)", argIdx)
        args = append(args, tenantID)
        argIdx++
    } else {
        // user 不能直接看凭据详情
        // 只返回模型可用性，不返回凭据 ID
        // 修改查询逻辑...
    }
    
    // 执行查询并处理结果
    rows, err := h.db.Query(ctx, credQuery, args...)
    // ...
    
    // 对于 user，过滤敏感字段
    if auth.Role == RoleUser {
        for i := range candidates {
            candidates[i].CredentialID = 0  // 隐藏凭据ID
            candidates[i].KeyPrefix = ""     // 隐藏密钥前缀
        }
    }
}
```

### 4.3 最近失败（Recent Failures）

```go
func (h *Handler) handleRoutingRecentModelFailures(w http.ResponseWriter, r *http.Request) {
    auth := GetAuthContext(r)
    tenantID := GetTenantID(r)
    
    query := `
        SELECT 
            rl.ts,
            rl.client_model,
            rl.error_message,
            sd.owner_user,
            rl.session_key
        FROM request_logs rl
        LEFT JOIN sessions s ON s.session_key = rl.session_key
        LEFT JOIN session_dimensions sd ON sd.session_key = s.session_key
        WHERE rl.success = FALSE
          AND rl.ts >= NOW() - INTERVAL '1 hour'
    `
    args := []interface{}{}
    argIdx := 1
    
    // 租户过滤
    if !IsSuperAdminOrLegacy(r) {
        query += fmt.Sprintf(" AND rl.tenant_id = $%d", argIdx)
        args = append(args, tenantID)
        argIdx++
    }
    
    // 用户过滤
    if auth.Role == RoleUser {
        query += fmt.Sprintf(" AND sd.owner_user = $%d", argIdx)
        args = append(args, auth.Username)
        argIdx++
    }
    
    query += " ORDER BY rl.ts DESC LIMIT 100"
    
    // 执行查询...
}
```

---

## 五、前端展示逻辑

### 5.1 根据角色显示/隐藏功能

```typescript
// 前端代码示例（React/Vue）

function RoutingV2Page({ userRole }) {
  return (
    <div>
      {/* 所有角色都能看 */}
      <RoutingOverview />
      <ModelList />
      
      {/* tenant_admin 和 super_admin 可见 */}
      {(userRole === 'tenant_admin' || userRole === 'super_admin') && (
        <>
          <CredentialsHealthDashboard />
          <RoutingPolicyViewer />
        </>
      )}
      
      {/* 仅 super_admin 可见 */}
      {userRole === 'super_admin' && (
        <>
          <RoutingPolicyEditor />
          <ScoringWeightsConfig />
          <EmergencyRepairPanel />
        </>
      )}
      
      {/* 仅 user 可见 */}
      {userRole === 'user' && (
        <MyRoutingStats />
      )}
    </div>
  )
}
```

### 5.2 数据脱敏展示

```typescript
// 凭据状态展示 - 根据角色脱敏

function CredentialCard({ credential, userRole }) {
  if (userRole === 'user') {
    // 用户不应该看到这个组件
    return null;
  }
  
  return (
    <div>
      <h3>{credential.providerName}</h3>
      
      {/* super_admin 看完整信息 */}
      {userRole === 'super_admin' && (
        <>
          <p>凭据ID: {credential.id}</p>
          <p>密钥前缀: {credential.keyPrefix}</p>
          <p>所属租户: {credential.tenantId}</p>
        </>
      )}
      
      {/* tenant_admin 看部分信息 */}
      {userRole === 'tenant_admin' && (
        <>
          <p>密钥前缀: {credential.keyPrefix}</p>
          {/* 不显示凭据ID和完整租户信息 */}
        </>
      )}
      
      {/* 所有管理员都能看健康状态 */}
      <p>健康状态: {credential.healthScore}</p>
      <p>配额状态: {credential.quotaState}</p>
    </div>
  )
}
```

---

## 六、实施时间表

### Week 1: P0 - 核心统计端点

| 任务 | 预计时间 |
|------|---------|
| 分析现有 routing 端点权限状态 | 2 小时 |
| 修改 handleRoutingOverview 添加过滤 | 2 小时 |
| 修改 handleRoutingRecentModelFailures | 2 小时 |
| 修改 handleRoutingResolve 凭据可见性 | 3 小时 |
| 测试验证 | 2 小时 |
| **小计** | **11 小时（1.5 天）** |

### Week 2: P1 - 扩展功能

| 任务 | 预计时间 |
|------|---------|
| 修改 handleRoutingModelTree | 2 小时 |
| 修改 handleRoutingFeaturedModels | 1 小时 |
| 确认 policy/scoring-weights 权限 | 1 小时 |
| 前端权限适配 | 4 小时 |
| 集成测试 | 3 小时 |
| **小计** | **11 小时（1.5 天）** |

### Week 3: 测试与上线

| 任务 | 预计时间 |
|------|---------|
| 完整回归测试 | 1 天 |
| 性能测试 | 0.5 天 |
| Code Review | 0.5 天 |
| 部署上线 | 0.5 天 |
| **小计** | **2.5 天** |

**总计**: 约 **1 周**（5-6 个工作日）

---

## 七、测试验证清单

### 7.1 功能测试

#### Super Admin
- [ ] 能看到全平台路由统计
- [ ] 能看到所有供应商凭据
- [ ] 能看到所有租户的失败日志
- [ ] 能编辑路由策略
- [ ] 能执行紧急修复

#### Tenant Admin
- [ ] 只能看本租户路由统计
- [ ] 能看到 default 租户 + 自己租户凭据
- [ ] 只能看本租户失败日志
- [ ] 只能查看（不能编辑）路由策略
- [ ] 不能执行紧急修复

#### User
- [ ] 只能看自己的路由统计
- [ ] 能看可用模型列表（无凭据详情）
- [ ] 只能看自己的失败日志
- [ ] 不能看路由策略
- [ ] 不能看凭据信息

### 7.2 安全测试

#### 越权测试
- [ ] user 尝试访问 /api/routing/policy → 403
- [ ] tenant_admin 尝试修改路由策略 → 403
- [ ] user 尝试通过修改参数看其他用户数据 → 403
- [ ] tenant_admin 尝试看其他租户凭据 → 403

#### 数据泄露测试
- [ ] user 不能在响应中看到凭据 ID
- [ ] tenant_admin 不能看到其他租户的配额信息
- [ ] API 响应不包含不该显示的字段

### 7.3 性能测试

- [ ] 添加过滤后查询性能 <500ms
- [ ] 确保 tenant_id, user_id 列有索引
- [ ] 大数据量下（10万+请求）性能稳定

---

## 八、Owner 决策清单

### 必须决策

1. **用户级功能范围**
   - [ ] 普通用户是否需要看 routing-v2 页面？
   - [ ] 如果需要，他们能做什么？
     - [ ] 查看自己的路由统计？
     - [ ] 查看可用模型列表？
     - [ ] 查看自己的失败请求？

2. **凭据可见性**
   - [ ] tenant_admin 能看哪些凭据详情？
     - [ ] 凭据 ID？
     - [ ] 密钥前缀？
     - [ ] 配额状态？
   - [ ] user 是否完全不能看凭据？（推荐：是）

3. **路由策略**
   - [ ] tenant_admin 能看路由策略吗？（推荐：只读）
   - [ ] tenant_admin 能修改租户级路由策略吗？（推荐：否）

### 可延后决策

4. **细粒度权限**
   - [ ] 是否需要"只读分析师"角色？
   - [ ] 是否需要租户级路由策略配置？

5. **数据导出**
   - [ ] 谁可以导出路由数据？
   - [ ] 是否需要水印？

---

## 九、快速开始指南

### Owner 立即可以做的

1. **确认用户需求**（10分钟）
   - 普通用户是否需要访问 routing-v2？
   - 如果不需要，前端直接隐藏该页面入口

2. **查看现有权限实现**（30分钟）
   ```bash
   # 检查 routing 端点的权限包装器
   grep "mux.HandleFunc.*routing" admin/handler.go | grep -E "admin\(|superAdmin\("
   
   # 查看是否已有租户过滤
   grep -n "EffectiveTenantID\|tenant_id" admin/routing.go | head -20
   ```

3. **确定实施优先级**（10分钟）
   - 如果用户量少，可以先不实施用户级过滤
   - 如果只有管理员用，只需确保租户隔离

---

## 十、总结

### 核心要点

1. **Routing-v2 页面主要面向管理员**
   - super_admin: 全平台监控与配置
   - tenant_admin: 本租户路由效果监控
   - user: 个人路由统计（可选）

2. **关键敏感数据：供应商凭据**
   - super_admin: 完全可见
   - tenant_admin: 部分可见
   - user: 完全不可见

3. **实施复杂度：中等**
   - 大部分端点已有 admin() 包装器
   - 需要添加 SQL 级别的租户和用户过滤
   - 凭据可见性需要额外处理

4. **实施时间：1周**
   - P0（核心统计）: 1.5 天
   - P1（扩展功能）: 1.5 天
   - 测试上线: 2.5 天

### 下一步

1. Owner 确认用户需求和权限范围
2. 开发团队按本文档实施
3. 参考 Dashboard/Live 的实施经验
4. 复用已有的权限辅助函数

---

**相关文档**：
- Dashboard/Live 实施指南：`docs/security/dashboard-live-user-filter-guide.md`
- 完整审计报告：`docs/security/data-permission-implementation-complete.md`
- 权限API：`admin/context_enhanced.go`
