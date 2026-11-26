# 数据权限审计与实施方案
**审计日期**: 2026-11-26  
**审计范围**: 全量 Admin API 端点数据权限控制

## 执行摘要

### 当前状态
- **角色体系**: 已完成基础角色治理（super_admin, tenant_admin, user）
- **端点总数**: 1177 个 handler 方法
- **权限包装器**: 112 个 super_admin 专属，241 个通用 admin，10 个 tenant_admin 专属
- **租户隔离**: 76 处使用 `EffectiveTenantID`，55 处使用 `EffectiveTenantIDAll`
- **用户隔离**: 仅 4 个文件有 user_id 过滤

### 核心问题
1. **通用 admin() 包装器过于宽松**: 241 个端点允许任意 admin 访问，但内部缺少数据级租户过滤
2. **用户级数据隔离缺失**: 普通用户（role=user）几乎无法使用系统，因为没有"只看自己数据"的端点
3. **租户隔离不一致**: 有些端点有 `EffectiveTenantID` 过滤，有些没有，缺乏系统性规则

### 实施优先级
**P0 - 立即修复（安全漏洞）**:
- 写操作端点的租户隔离（27 DELETE + 276 UPDATE + 109 INSERT）
- 敏感数据读取端点（审计日志、凭据、密钥）

**P1 - 本轮完成（功能完整性）**:
- 所有列表/统计端点的租户过滤
- 用户级端点实现（session、usage stats、personal keys）

**P2 - 后续优化**:
- 细粒度权限（如"只读分析师"角色）
- 数据导出的租户水印

---

## 一、权限模型定义

### 1.1 角色定义

```go
// 角色闭集（已实现，见 admin/users_role_rules_test.go）
const (
    RoleSuperAdmin   = "super_admin"   // 平台管理员，只能在 default 租户
    RoleTenantAdmin  = "tenant_admin"  // 租户管理员，可在任意租户
    RoleUser         = "user"          // 普通用户
    RoleChannel      = "channel"       // 渠道商（W4.3）
    RoleAdminKey     = "admin_key"     // 遗留全局 Key（已弃用）
)
```

### 1.2 数据作用域分类

| 数据类型 | super_admin | tenant_admin | user | 示例 |
|---------|-------------|--------------|------|------|
| **系统级** | 读写 | 禁止 | 禁止 | 供应商配置、系统设置、所有租户列表 |
| **租户级** | 全部读写 | 本租户读写 | 禁止 | API Keys、凭据、租户统计、模型配置 |
| **用户级** | 全部读 | 本租户读 | 本人读写 | 会话历史、个人 usage、密码修改 |

### 1.3 权限判定辅助函数

```go
// 已实现（admin/context.go）
func IsSuperAdminOrLegacy(r *http.Request) bool
func IsTenantAdmin(r *http.Request) bool
func EffectiveTenantID(r *http.Request) string      // tenant_admin → 自己 tenant_id; super_admin → "default"
func EffectiveTenantIDAll(r *http.Request) string   // tenant_admin → 自己 tenant_id; super_admin → ""（查全部）

// 需新增
func EffectiveUserID(r *http.Request) int           // user → 自己 user_id; admin → 0（查全部）
func RequireUserOrHigher(w http.ResponseWriter, r *http.Request) bool // 拒绝未认证
func GetAuthUserID(r *http.Request) int             // 返回 AuthContext.UserID
```

---

## 二、端点分类审计

### 2.1 系统级端点（仅 super_admin）

#### ✅ 已正确保护
```
/api/admin/tenants                     superAdmin ✓
/api/admin/providers                   superAdmin ✓
/api/admin/providers/*                 superAdmin ✓
/api/admin/vendor-nodes                superAdmin ✓
/api/admin/swim-lanes                  superAdmin ✓
/api/admin/emergency-repair            superAdmin ✓
/api/routing/emergency-repair          superAdmin ✓
/api/admin/data-lifecycle/*/archive*   superAdmin ✓
/api/admin/extension-points            superAdmin ✓
```

#### ⚠️ 需复核
```
/api/admin/audit-logs                  superAdmin ✓ 但需加 tenant_id 过滤选项
/api/system/settings/*                 需确认是否全是 superAdmin
/api/admin/mobile-node/*               需确认数据作用域
```

### 2.2 租户级端点（需租户隔离）

#### ✅ 已正确实现租户隔离
```
# 以下端点已使用 EffectiveTenantIDAll 或 EffectiveTenantID
/api/admin/stats/v2/summary            ✓ (已在 L129 实施租户过滤)
/api/admin/pi-analysis/*               ✓ (已在 L129 实施租户过滤)
/api/admin/keys                        ✓ (listKeys 已过滤)
/api/admin/credentials                 ✓ (需复核)
```

#### ❌ 缺少租户隔离（P0 修复）
```
# 以下端点使用 admin() 包装器但内部未过滤 tenant_id
/api/sessions                          ❌ 需加 tenant_id WHERE 子句
/api/sessions/{id}                     ❌ 需校验归属
/api/admin/session-analytics           ❌ 需租户过滤
/api/admin/logs                        ❌ 需租户过滤
/api/admin/probe-dashboard             ❌ 需租户过滤（探针可能是全局的，需明确）
/api/admin/usage/*                     ❌ 需租户过滤
/api/routing/resolve                   ❌ 需租户过滤（tenant 参数）
/api/routing/overview                  ❌ 需租户过滤
/api/routing/recent-model-failures     ❌ 需租户过滤
```

#### 🔍 需人工判定作用域
```
/api/admin/compression/*               系统级？租户级？
/api/admin/data-lifecycle/stats        系统级？
/api/admin/model-integrity             租户级？（已有 modelIntegrityTenantAdmin 包装器）
/api/admin/format-anomalies            租户级？（已有 formatAnomalyTenantAdmin 包装器）
```

### 2.3 用户级端点（普通用户可访问）

#### ✅ 已实现
```
/api/auth/me                           ✓ 返回当前用户信息
/api/auth/change-password              ✓ 修改自己密码
/api/auth/logout                       ✓
```

#### ❌ 缺失（P1 实施）
```
/api/sessions/my                       待实现：只返回自己的会话
/api/usage/my                          待实现：只返回自己的用量
/api/keys/my                           待实现：用户个人 Key（需明确产品需求）
/api/sessions/{id}                     需加权限：user 只能看自己的，tenant_admin 看本租户，super_admin 看全部
```

---

## 三、SQL 查询模式审计

### 3.1 租户隔离模式

#### ✅ 正确模式
```sql
-- Pattern A: 显式过滤
SELECT * FROM sessions 
WHERE tenant_id = $1  -- $1 = EffectiveTenantIDAll(r)
AND ...

-- Pattern B: super_admin 看全部
tenantFilter := EffectiveTenantIDAll(r)
if tenantFilter != "" {
    query += " AND tenant_id = $n"
    args = append(args, tenantFilter)
}
```

#### ❌ 错误模式（发现 N 处）
```sql
-- 直接查询，无 tenant_id 过滤
SELECT * FROM sessions WHERE id = $1

-- 只在前端过滤（不可信）
SELECT * FROM sessions  -- 返回全部，前端再过滤
```

### 3.2 用户隔离模式

#### ✅ 正确模式
```sql
-- user 只看自己
userID := GetAuthUserID(r)
if !IsSuperAdminOrLegacy(r) && !IsTenantAdmin(r) {
    query += " AND user_id = $n"
    args = append(args, userID)
}
```

#### ❌ 当前状态
- **仅 4 个文件** 有 `user_id` 过滤逻辑
- 大部分端点根本不支持 `role=user` 访问

---

## 四、实施方案

### 4.1 Phase 1: 建立权限审计基础设施（1-2 天）

#### 任务 1.1: 增强权限辅助函数
```go
// admin/context.go

// EffectiveUserID 返回用户级查询的 user_id 过滤值：
//   - role=user: 返回自己的 user_id（强制限制）
//   - role>=tenant_admin: 返回 0（表示可查本租户所有用户）
//   - super_admin: 返回 0（表示可查全平台）
func EffectiveUserID(r *http.Request) int {
    auth := GetAuthContext(r)
    if auth == nil {
        return 0
    }
    if auth.Role == RoleUser {
        return auth.UserID
    }
    return 0 // admin 角色可看全部
}

// RequireUserOrHigher 确保请求至少是普通用户
func RequireUserOrHigher(w http.ResponseWriter, r *http.Request) bool {
    auth := GetAuthContext(r)
    if auth == nil || auth.UserID == 0 {
        writeError(w, http.StatusUnauthorized, "authentication required")
        return true
    }
    return false
}

// CanAccessTenant 判断是否可访问指定租户的数据
func CanAccessTenant(r *http.Request, tenantID string) bool {
    auth := GetAuthContext(r)
    if auth == nil {
        return false
    }
    if IsSuperAdminOrLegacy(r) {
        return true
    }
    return auth.TenantID == tenantID
}

// CanAccessUserData 判断是否可访问指定用户的数据
func CanAccessUserData(r *http.Request, targetUserID int, targetTenantID string) bool {
    auth := GetAuthContext(r)
    if auth == nil {
        return false
    }
    if IsSuperAdminOrLegacy(r) {
        return true
    }
    if IsTenantAdmin(r) && auth.TenantID == targetTenantID {
        return true
    }
    if auth.Role == RoleUser && auth.UserID == targetUserID {
        return true
    }
    return false
}
```

#### 任务 1.2: 建立审计门测试框架
```go
// admin/permission_audit_test.go

// 为每个端点建立权限矩阵测试
func TestEndpointPermissionMatrix(t *testing.T) {
    endpoints := []struct{
        path string
        method string
        dataScope string // "system" | "tenant" | "user"
        allowRoles []string
    }{
        {"/api/admin/tenants", "GET", "system", []string{"super_admin"}},
        {"/api/sessions", "GET", "tenant", []string{"super_admin", "tenant_admin"}},
        {"/api/sessions/my", "GET", "user", []string{"super_admin", "tenant_admin", "user"}},
        // ... 完整列表
    }
    
    for _, ep := range endpoints {
        t.Run(ep.path, func(t *testing.T) {
            // 测试每个角色的访问权限
            testRoleAccess(t, ep)
            // 测试租户隔离
            if ep.dataScope == "tenant" || ep.dataScope == "user" {
                testTenantIsolation(t, ep)
            }
            // 测试用户隔离
            if ep.dataScope == "user" {
                testUserIsolation(t, ep)
            }
        })
    }
}
```

### 4.2 Phase 2: 修复租户级端点（3-5 天）

#### 优先级排序
1. **写操作**: DELETE/UPDATE/INSERT（防数据破坏）
2. **敏感数据**: keys, credentials, audit logs
3. **高频端点**: sessions, analytics, usage

#### 修复模板
```go
// 示例：修复 /api/sessions
func (h *Handler) handleSessions(w http.ResponseWriter, r *http.Request) {
    // 1. 权限检查（如需限制角色）
    if RequireSuperAdminForWrite(w, r) {
        return
    }
    
    // 2. 获取租户过滤条件
    tenantFilter := EffectiveTenantIDAll(r)
    
    // 3. 构造查询
    query := `SELECT id, user_id, tenant_id, title, created_at 
              FROM sessions 
              WHERE deleted_at IS NULL`
    args := []any{}
    
    if tenantFilter != "" {
        query += " AND tenant_id = $1"
        args = append(args, tenantFilter)
    }
    
    // 4. 执行查询...
}
```

#### 批量修复清单（按文件）
```
admin/sessions.go          - handleSessions, handleSessionDetail
admin/session_analytics.go - handleSessionAnalytics*
admin/logs.go              - handleLogs*
admin/usage.go             - handleUsage*
admin/keys.go              - listKeys（复核）, createKey, deleteKey
admin/routing.go           - handleRoutingResolve, handleRoutingOverview
admin/probe_dashboard.go   - 判定数据作用域后修复
```

### 4.3 Phase 3: 实现用户级端点（2-3 天）

#### 新增用户端点
```go
// admin/user_sessions.go
func (h *Handler) handleMySessions(w http.ResponseWriter, r *http.Request) {
    if RequireUserOrHigher(w, r) {
        return
    }
    
    userID := GetAuthUserID(r)
    tenantID := GetTenantID(r)
    
    query := `SELECT id, title, created_at, updated_at 
              FROM sessions 
              WHERE user_id = $1 
              AND tenant_id = $2 
              AND deleted_at IS NULL
              ORDER BY updated_at DESC 
              LIMIT 50`
    
    rows, err := h.db.Query(r.Context(), query, userID, tenantID)
    // ...
}

// 路由注册
mux.HandleFunc("/api/sessions/my", admin(h.handleMySessions))
```

#### 修改现有端点支持用户访问
```go
// admin/sessions.go - 修改 handleSessionDetail
func (h *Handler) handleSessionDetail(w http.ResponseWriter, r *http.Request, sessionID int) {
    // 1. 查询会话归属
    var session struct {
        ID       int
        UserID   int
        TenantID string
    }
    err := h.db.QueryRow(r.Context(), 
        `SELECT id, user_id, tenant_id FROM sessions WHERE id = $1`, 
        sessionID).Scan(&session.ID, &session.UserID, &session.TenantID)
    
    // 2. 权限判定
    if !CanAccessUserData(r, session.UserID, session.TenantID) {
        writeError(w, http.StatusForbidden, "insufficient permissions")
        return
    }
    
    // 3. 返回数据...
}
```

### 4.4 Phase 4: 测试覆盖（2-3 天）

#### 测试策略
1. **单元测试**: 每个新增/修改的 handler 都用 pgxmock 钉住 SQL
2. **集成测试**: 用真实 DB 测试三种角色的数据隔离
3. **回归测试**: 确保现有功能不受影响

#### 测试用例模板
```go
func TestHandleSessions_TenantIsolation(t *testing.T) {
    // super_admin 看全部
    testWithRole(t, "super_admin", "default", expectAllTenants)
    
    // tenant_admin 只看自己租户
    testWithRole(t, "tenant_admin", "acme", expectOnlyAcme)
    
    // user 无权访问列表
    testWithRole(t, "user", "acme", expectForbidden)
}

func TestHandleSessionDetail_UserIsolation(t *testing.T) {
    // user 可以看自己的会话
    testUserAccess(t, userID: 42, sessionUserID: 42, expectSuccess)
    
    // user 不能看别人的会话
    testUserAccess(t, userID: 42, sessionUserID: 99, expectForbidden)
    
    // tenant_admin 可以看本租户所有用户
    testTenantAdminAccess(t, tenantID: "acme", sessionTenantID: "acme", expectSuccess)
}
```

### 4.5 Phase 5: 文档与监控（1 天）

#### 文档产出
1. **权限矩阵表**: 所有端点的角色-数据作用域映射
2. **API 文档更新**: 标注每个端点的权限要求
3. **迁移指南**: 给现有 API 使用方的兼容性说明

#### 监控指标
```go
// 添加权限拒绝监控
metrics.IncCounter("admin.permission.denied", 
    map[string]string{
        "endpoint": r.URL.Path,
        "role": auth.Role,
        "reason": "tenant_isolation",
    })
```

---

## 五、风险与缓解

### 5.1 兼容性风险
**风险**: 修复后，某些端点会拒绝 tenant_admin 访问之前能访问的数据  
**缓解**: 
- 先在日志模式运行（记录但不拒绝）
- 生产环境分阶段开启
- 提供临时 bypass 开关（环境变量 `DATA_PERMISSION_ENFORCE=log|enforce`）

### 5.2 性能风险
**风险**: 增加 WHERE 子句可能影响查询性能  
**缓解**: 
- 确保 `tenant_id` 和 `user_id` 列有索引
- 复合索引：`(tenant_id, created_at)`, `(user_id, tenant_id)`

### 5.3 测试覆盖风险
**风险**: 1177 个 handler，全覆盖需大量时间  
**缓解**: 
- 优先级分层：P0 写操作 → P1 敏感读 → P2 一般读
- 自动生成测试骨架
- 使用 Maestro 流程测试端到端场景

---

## 六、执行时间表

| Phase | 任务 | 工作量 | 负责人 | 开始日期 | 结束日期 |
|-------|------|--------|--------|----------|----------|
| 1 | 权限基础设施 | 2 天 | TBD | 2026-11-27 | 2026-11-28 |
| 2 | 租户级端点修复 | 5 天 | TBD | 2026-11-29 | 2026-12-05 |
| 3 | 用户级端点实现 | 3 天 | TBD | 2026-12-06 | 2026-12-10 |
| 4 | 测试覆盖 | 3 天 | TBD | 2026-12-11 | 2026-12-13 |
| 5 | 文档与监控 | 1 天 | TBD | 2026-12-16 | 2026-12-16 |
| **总计** | | **14 天** | | | |

---

## 七、验收标准

### 7.1 功能验收
- [ ] 所有写操作端点有租户隔离
- [ ] 所有敏感数据读端点有租户隔离
- [ ] 至少 5 个用户级端点可供 role=user 使用
- [ ] 所有修改的端点有单元测试覆盖

### 7.2 安全验收
- [ ] Penetration test: tenant_admin 无法访问其他租户数据
- [ ] Penetration test: user 无法访问其他用户数据
- [ ] Audit log 记录所有权限拒绝事件
- [ ] 无 SQL 注入风险（参数化查询）

### 7.3 性能验收
- [ ] 新增 WHERE 子句不导致 P95 延迟增加 >10%
- [ ] 相关表已添加必要索引
- [ ] 慢查询日志无新增 N+1 查询

---

## 八、后续优化方向

1. **基于资源的访问控制（RBAC 2.0）**
   - 引入 `permissions` 表
   - 支持细粒度权限（如"只读分析师"）

2. **行级安全（Row-Level Security）**
   - 在 PostgreSQL 层面启用 RLS
   - 应用层设置 `SET LOCAL` 会话变量

3. **审计日志增强**
   - 记录所有数据访问（包括成功的）
   - 支持"谁在何时访问了哪条数据"追溯

4. **数据导出控制**
   - 导出的文件带租户水印
   - 限制导出数量（防批量下载）

---

## 附录 A: 端点完整清单

（见后续生成的 `endpoint-permission-matrix.csv`）

## 附录 B: 测试用例生成器

（见 `scripts/generate-permission-tests.sh`）
