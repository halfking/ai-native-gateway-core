# Routing-v2 页面用户级权限过滤 - 实施方案
**日期**: 2026-11-26  
**状态**: Owner已决策，准备实施  

---

## Owner 决策（已确认）

### 决策1: 普通用户访问权限
✅ **是** - 普通用户需要访问 routing-v2 页面，查看个人路由统计

### 决策2: Tenant Admin 凭据信息可见性
✅ **推荐方案** - 只看配额状态和健康分数
- ❌ 不看凭据ID
- ❌ 不看密钥前缀
- ✅ 看配额状态
- ✅ 看健康分数

### 决策3: Tenant Admin 路由策略权限
✅ **只读** - 只能查看，不能修改（推荐）

---

## 实施清单

### Phase 1: 核心统计端点（P0，预计1.5天）

#### 1.1 路由概览 - handleRoutingOverview

**文件**: `admin/routing.go:2145`

**当前状态**: 可能没有用户级过滤

**需要修改**:
```go
func (h *Handler) handleRoutingOverview(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    auth := GetAuthContext(r)
    
    // 构建基础查询
    query := `
        SELECT 
            COUNT(*) as total_requests,
            SUM(CASE WHEN success THEN 1 ELSE 0 END) as successful,
            AVG(latency_ms) as avg_latency
        FROM request_logs_with_current_month rl
        WHERE rl.ts >= NOW() - INTERVAL '24 hours'
    `
    args := []interface{}{}
    argIdx := 1
    
    // 1. 租户过滤
    tenantID := GetTenantID(r)
    if !IsSuperAdminOrLegacy(r) {
        query += fmt.Sprintf(" AND rl.tenant_id = $%d", argIdx)
        args = append(args, tenantID)
        argIdx++
    }
    
    // 2. 用户过滤（新增）
    if auth.Role == RoleUser {
        // 普通用户只看自己的路由统计
        query += fmt.Sprintf(` AND EXISTS (
            SELECT 1 FROM sessions s
            JOIN session_dimensions sd ON s.session_key = sd.session_key
            WHERE s.session_key = rl.session_key
            AND sd.owner_user = $%d
        )`, argIdx)
        args = append(args, auth.Username)
        argIdx++
    }
    
    // 执行查询...
    var stats struct {
        TotalRequests   int     `json:"total_requests"`
        SuccessfulReqs  int     `json:"successful_requests"`
        AvgLatency      float64 `json:"avg_latency_ms"`
    }
    
    err := h.db.QueryRow(ctx, query, args...).Scan(
        &stats.TotalRequests,
        &stats.SuccessfulReqs,
        &stats.AvgLatency,
    )
    if err != nil {
        writeInternalTextErr(w, "failed to query routing overview", err)
        return
    }
    
    writeJSON(w, http.StatusOK, stats)
}
```

#### 1.2 路由解析 - handleRoutingResolve（凭据可见性）

**文件**: `admin/routing.go:276`

**核心修改**: 根据角色脱敏凭据信息

```go
func (h *Handler) handleRoutingResolve(w http.ResponseWriter, r *http.Request) {
    // ... 现有查询逻辑 ...
    
    auth := GetAuthContext(r)
    
    // 查询候选凭据（保持原有逻辑）
    // ... 执行查询获取 candidates ...
    
    // 根据角色脱敏（新增）
    for i := range candidates {
        if auth.Role == RoleUser {
            // 普通用户：完全隐藏凭据详情
            candidates[i].CredentialID = 0
            candidates[i].KeyPrefix = ""
            candidates[i].QuotaState = ""  // 隐藏配额
            candidates[i].HealthScore = 0   // 隐藏健康分数
        } else if auth.Role == RoleTenantAdmin {
            // Tenant Admin：只保留配额和健康分数
            candidates[i].CredentialID = 0  // 隐藏凭据ID
            candidates[i].KeyPrefix = ""     // 隐藏密钥前缀
            // 保留 QuotaState 和 HealthScore
        }
        // Super Admin：看全部（不修改）
    }
    
    writeJSON(w, http.StatusOK, candidates)
}
```

#### 1.3 最近失败 - handleRoutingRecentModelFailures

**文件**: 需要查找或创建

**修改逻辑**:
```go
func (h *Handler) handleRoutingRecentModelFailures(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    auth := GetAuthContext(r)
    tenantID := GetTenantID(r)
    
    query := `
        SELECT 
            rl.ts,
            rl.client_model,
            rl.error_message,
            sd.owner_user,
            rl.session_key
        FROM request_logs_with_current_month rl
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
    
    // 执行查询并返回...
}
```

---

### Phase 2: 扩展功能（P1，预计1.5天）

#### 2.1 模型树 - handleRoutingModelTree

**当前状态**: 可能显示所有模型

**修改**: 根据租户的凭据配置过滤可用模型

```go
func (h *Handler) handleRoutingModelTree(w http.ResponseWriter, r *http.Request) {
    // 只显示租户可用的模型
    tenantID := GetTenantID(r)
    
    // 查询该租户有凭据的模型
    if !IsSuperAdminOrLegacy(r) {
        query := `
            SELECT DISTINCT cm.canonical_model
            FROM credentials c
            JOIN credential_model_bindings cm ON cm.credential_id = c.id
            WHERE c.lifecycle_status = 'active'
              AND (c.tenant_id = 'default' OR c.tenant_id = $1)
        `
        // 执行查询并过滤模型树...
    }
}
```

#### 2.2 特色模型 - handleRoutingFeatured

**修改**: 根据租户可用性过滤

#### 2.3 确认策略端点权限

**验证**: 确保以下端点已用 `superAdmin()` 包装
- `/api/routing/policy` - ✅ 已保护
- `/api/routing/scoring-weights` - ✅ 已保护
- `/api/routing/candidate-binding/` - ✅ 已保护（写操作）

---

## 详细实施步骤

### Step 1: 准备工作（30分钟）

1. **创建测试分支**
   ```bash
   cd /path/to/llm-gateway-go-cursor
   git checkout -b feature/routing-v2-user-filter
   ```

2. **备份关键文件**
   ```bash
   cp admin/routing.go admin/routing.go.bak
   ```

### Step 2: 修改 handleRoutingOverview（2小时）

1. 找到函数定义（约2145行）
2. 添加 `auth := GetAuthContext(r)`
3. 添加租户过滤逻辑
4. 添加用户过滤逻辑（使用 EXISTS 子查询）
5. 测试编译：`go build ./admin`

### Step 3: 修改 handleRoutingResolve（3小时）

1. 找到函数定义（约276行）
2. 在返回结果前添加脱敏逻辑
3. 根据角色隐藏字段：
   - User: 隐藏全部凭据信息
   - Tenant Admin: 只保留 QuotaState 和 HealthScore
   - Super Admin: 不修改
4. 测试编译

### Step 4: 创建或修改 handleRoutingRecentModelFailures（2小时）

1. 查找是否存在该handler
2. 如果不存在，创建新函数
3. 添加完整的权限过滤逻辑
4. 在 handler.go 中注册路由（如需要）

### Step 5: 测试验证（4小时）

#### 5.1 单元测试

创建测试文件 `admin/routing_permissions_test.go`:

```go
package admin

import (
	"net/http/httptest"
	"testing"
)

func TestRoutingOverview_UserFiltering(t *testing.T) {
	tests := []struct {
		name     string
		auth     *AuthContext
		wantUser bool
	}{
		{
			name:     "super_admin sees all",
			auth:     &AuthContext{UserID: 1, Role: RoleSuperAdmin, TenantID: "default"},
			wantUser: false,
		},
		{
			name:     "tenant_admin sees tenant",
			auth:     &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			wantUser: false,
		},
		{
			name:     "user sees only self",
			auth:     &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme", Username: "alice"},
			wantUser: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/routing/overview", nil)
			r = SetAuthContext(r, tt.auth)

			// 验证过滤逻辑...
		})
	}
}

func TestRoutingResolve_CredentialVisibility(t *testing.T) {
	tests := []struct {
		name              string
		auth              *AuthContext
		expectCredentialID bool
		expectKeyPrefix    bool
		expectQuotaState   bool
	}{
		{
			name:              "user sees nothing",
			auth:              &AuthContext{Role: RoleUser},
			expectCredentialID: false,
			expectKeyPrefix:    false,
			expectQuotaState:   false,
		},
		{
			name:              "tenant_admin sees quota only",
			auth:              &AuthContext{Role: RoleTenantAdmin},
			expectCredentialID: false,
			expectKeyPrefix:    false,
			expectQuotaState:   true,
		},
		{
			name:              "super_admin sees all",
			auth:              &AuthContext{Role: RoleSuperAdmin},
			expectCredentialID: true,
			expectKeyPrefix:    true,
			expectQuotaState:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 测试凭据可见性...
		})
	}
}
```

运行测试：
```bash
go test -v ./admin -run TestRoutingOverview
go test -v ./admin -run TestRoutingResolve
```

#### 5.2 集成测试

1. 启动测试环境
2. 以不同角色登录测试
3. 验证数据过滤正确

### Step 6: 前端适配（需前端配合，4小时）

前端需要根据用户角色调整显示：

```typescript
// 示例：前端适配
function RoutingV2Page({ userRole }) {
  return (
    <div>
      {/* 所有角色 */}
      <RoutingOverview />
      
      {/* Tenant Admin 和 Super Admin */}
      {(userRole === 'tenant_admin' || userRole === 'super_admin') && (
        <CredentialHealth 
          showCredentialId={userRole === 'super_admin'}
          showKeyPrefix={userRole === 'super_admin'}
          showQuota={true}
          showHealth={true}
        />
      )}
      
      {/* 仅 Super Admin */}
      {userRole === 'super_admin' && (
        <RoutingPolicyEditor />
      )}
    </div>
  )
}
```

---

## 验证清单

### 功能验证

#### Super Admin
- [ ] 能看全平台路由统计
- [ ] 能看所有凭据详情（ID、密钥前缀、配额、健康分数）
- [ ] 能看所有租户的失败日志
- [ ] 能编辑路由策略
- [ ] 能修改候选绑定

#### Tenant Admin
- [ ] 只能看本租户路由统计
- [ ] 能看凭据的配额状态和健康分数
- [ ] **不能**看凭据ID和密钥前缀
- [ ] 只能看本租户失败日志
- [ ] 只能查看（不能编辑）路由策略
- [ ] **不能**修改候选绑定

#### User
- [ ] 只能看自己的路由统计
- [ ] 能看可用模型列表（无凭据详情）
- [ ] 只能看自己的失败日志
- [ ] **不能**看凭据信息
- [ ] **不能**看路由策略

### 安全验证

#### 越权测试
- [ ] user 尝试访问 /api/routing/policy → 403
- [ ] tenant_admin 尝试修改候选绑定 → 403
- [ ] user 尝试通过修改参数看其他用户数据 → 返回空或403

#### 数据泄露测试
- [ ] user 的 API 响应中不包含任何凭据信息
- [ ] tenant_admin 的 API 响应中不包含凭据ID和密钥前缀
- [ ] 跨租户数据隔离有效

### 性能验证
- [ ] 添加过滤后查询性能 <500ms
- [ ] 确保以下列有索引：
  - `request_logs_with_current_month.tenant_id`
  - `session_dimensions.owner_user`
  - `credentials.tenant_id`

---

## 关键代码位置

| 功能 | 文件 | 行号 | 修改内容 |
|------|------|------|---------|
| 路由概览 | admin/routing.go | ~2145 | 添加用户过滤 |
| 路由解析 | admin/routing.go | ~276 | 凭据脱敏 |
| 最近失败 | admin/routing.go | 待查找 | 添加用户过滤 |
| 模型树 | admin/routing.go | ~2319 | 租户可用模型 |
| 路由策略 | admin/routing.go | ~2591 | 确认已保护 |

---

## 风险评估

### 低风险
- ✅ 修改是增量的
- ✅ 不影响现有功能
- ✅ 可以分批上线

### 中风险
- ⚠️ 需要修改多个端点
- ⚠️ 查询性能影响（需验证索引）
- ⚠️ 前端需要同步修改

### 缓解措施
1. 先在测试环境验证
2. 分批上线（先 Overview，再 Resolve，最后 ModelTree）
3. 监控查询性能
4. 准备回滚计划

---

## 时间估算

| 任务 | 预计时间 |
|------|---------|
| 修改 handleRoutingOverview | 2 小时 |
| 修改 handleRoutingResolve | 3 小时 |
| 修改/创建 handleRoutingRecentModelFailures | 2 小时 |
| 修改 handleRoutingModelTree | 2 小时 |
| 单元测试编写 | 2 小时 |
| 集成测试 | 2 小时 |
| 前端适配 | 4 小时 |
| Code Review + 修复 | 2 小时 |
| **总计** | **19 小时（约2.5天）** |

---

## 下一步行动

### 立即开始（今天）
1. ☐ 创建功能分支
2. ☐ 修改 handleRoutingOverview（最简单的）
3. ☐ 编译测试验证

### 明天
4. ☐ 修改 handleRoutingResolve（凭据脱敏）
5. ☐ 修改 handleRoutingRecentModelFailures
6. ☐ 单元测试

### 后天
7. ☐ 修改 handleRoutingModelTree
8. ☐ 集成测试
9. ☐ Code Review

### 本周内
10. ☐ 前端适配（需前端配合）
11. ☐ 测试环境部署
12. ☐ 生产环境上线

---

## 参考资料

- 完整分析：`docs/security/routing-v2-permission-analysis.md`
- 权限API：`admin/context_enhanced.go`
- 类似实现：`admin/my_endpoints.go`（用户级端点示例）

---

**准备就绪，可以开始实施！**
