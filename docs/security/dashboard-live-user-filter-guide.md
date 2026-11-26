# Dashboard 和 Live 页面用户级权限过滤实施指南
**日期**: 2026-11-26  
**目标**: 为 Dashboard 和 Live 页面添加用户级数据过滤

---

## 一、背景

根据需求，Dashboard 和 Live 页面需要实现三级权限过滤：
- **super_admin**: 看全平台所有数据
- **tenant_admin**: 看本租户所有数据
- **user**: 只看自己的数据

**当前状态**：
- ✅ 已有租户级过滤（tenant_admin 限制到自己租户）
- ⚠️ 缺少用户级过滤（普通用户还看不到自己的数据）

---

## 二、实施方案

### 2.1 核心修改

**文件**: `admin/dashboardapi/user_filter.go`（已创建）

需要修改第 42 行的占位符为正确的Go代码：

```go
// 当前（有占位符语法错误）
if IsRegularUser(auth) {
    params.OwnerUser = {占位符}  // ❌ 语法错误
}

// 应该修改为
if IsRegularUser(auth) {
    params.OwnerUser = auth.Username  // ✅ 正确语法
}
```

**修复命令**：
```bash
cd /path/to/llm-gateway-go-cursor
# 手动编辑文件，将第42行的占位符替换为 auth.Username
vi admin/dashboardapi/user_filter.go
# 或使用 sed（如果占位符格式稳定）
sed -i 's/{.*SENSITIVE.*}/auth.Username/g' admin/dashboardapi/user_filter.go
```

### 2.2 应用到现有端点

修改 `admin/dashboardapi/auth.go` 中的 `normalizeDashboardScope` 函数：

```go
// 文件: admin/dashboardapi/auth.go
// 修改 normalizeDashboardScope 函数

func normalizeDashboardScope(params QueryParams, auth AuthInfo) QueryParams {
    // ===== 原有租户过滤（保持不变）=====
    if auth.UserRole != "super_admin" && auth.UserRole != "admin_key" {
        params.TenantID = auth.TenantID
    }
    
    // ===== 新增：用户级过滤 =====
    if auth.UserRole == "user" {
        params.OwnerUser = auth.Username  // 🔧 需要Owner手动替换为 auth.Username
    }
    
    params.auth = auth
    return params
}
```

**或者**直接使用新创建的函数：

```go
// 替换所有调用 normalizeDashboardScope 的地方
// 从：
params = normalizeDashboardScope(params, auth)

// 改为：
params = NormalizeQueryParamsWithUserFilter(params, auth)
```

### 2.3 修改 Dashboard 查询逻辑

需要在各个 Dashboard handler 的查询中添加用户过滤。

**示例 1: Session Overview**

```go
// 文件: admin/dashboardapi/session_overview.go

func (h *SessionOverviewHandler) HandleSessionOverview(w http.ResponseWriter, r *http.Request) {
    params, auth, ok := prepareDashboardRequest(w, r, h.db)
    if !ok {
        return
    }
    
    // 构建查询
    query := `
        SELECT ...
        FROM session_summaries ss
        JOIN session_dimensions sd ON ss.session_key = sd.session_key
        WHERE 1=1
    `
    args := []interface{}{}
    argIdx := 1
    
    // 1. 租户过滤
    if params.TenantID != "" {
        query += fmt.Sprintf(" AND ss.tenant_id = $%d", argIdx)
        args = append(args, params.TenantID)
        argIdx++
    }
    
    // 2. 用户过滤（新增）
    if params.OwnerUser != "" {
        query += fmt.Sprintf(" AND sd.owner_user = $%d", argIdx)
        args = append(args, params.OwnerUser)
        argIdx++
    }
    
    // 执行查询...
}
```

**示例 2: 使用辅助函数**

```go
// 更简洁的方式
userClause, userArgs, nextIdx := BuildUserFilterClause(auth, "sd.owner_user", argIdx)
if userClause != "" {
    query += " " + userClause
    args = append(args, userArgs...)
    argIdx = nextIdx
}
```

---

## 三、需要修改的文件清单

### 3.1 核心权限逻辑（P0）

| 文件 | 修改内容 | 优先级 |
|------|---------|--------|
| `admin/dashboardapi/user_filter.go` | **修复第42行占位符为 auth.Username** | 🔴 P0 |
| `admin/dashboardapi/auth.go` | 在 normalizeDashboardScope 添加用户过滤 | 🔴 P0 |

### 3.2 Dashboard 端点（P1）

需要添加用户过滤的查询：

| 文件 | 端点 | 修改位置 |
|------|------|---------|
| `admin/dashboardapi/session_overview.go` | /api/admin/dashboard/session-overview | 查询 WHERE 子句 |
| `admin/dashboardapi/session_trend.go` | /api/admin/dashboard/session-trend | 查询 WHERE 子句 |
| `admin/dashboardapi/session_health.go` | /api/admin/dashboard/session-health | 查询 WHERE 子句 |
| `admin/dashboardapi/session_active.go` | /api/admin/dashboard/session-active | 查询 WHERE 子句 |
| `admin/dashboardapi/session_projected.go` | /api/admin/dashboard/v2/* | HandleProjectedSessionDashboard |

### 3.3 Live Stream（P1）

| 文件 | 修改内容 |
|------|---------|
| `admin/live_stream_sse.go` | HandleLiveStream 添加用户过滤 |

---

## 四、实施步骤

### Step 1: 修复语法错误（必须）

```bash
# 1. 打开文件
vi admin/dashboardapi/user_filter.go

# 2. 找到第42行（或搜索 "params.OwnerUser"）
/params.OwnerUser

# 3. 将占位符替换为：auth.Username
# 从：params.OwnerUser = {SENSITIVE...}
# 改为：params.OwnerUser = auth.Username

# 4. 保存并验证编译
:wq
go build ./admin/dashboardapi
```

### Step 2: 更新权限过滤逻辑（2种方案任选其一）

**方案A: 修改现有函数（最小改动）**

修改 `admin/dashboardapi/auth.go` 的 `normalizeDashboardScope`：

```go
func normalizeDashboardScope(params QueryParams, auth AuthInfo) QueryParams {
    if auth.UserRole != "super_admin" && auth.UserRole != "admin_key" {
        params.TenantID = auth.TenantID
    }
    // 新增用户过滤
    if auth.UserRole == "user" {
        params.OwnerUser = auth.Username  // 🔧 替换为 auth.Username
    }
    params.auth = auth
    return params
}
```

**方案B: 使用新函数（推荐）**

在所有调用 `normalizeDashboardScope` 的地方替换为 `NormalizeQueryParamsWithUserFilter`。

### Step 3: 修改查询逻辑（Dashboard 端点）

以 `session_overview.go` 为例：

```go
// 在 WHERE 子句构建中添加
if params.OwnerUser != "" {
    query += fmt.Sprintf(" AND sd.owner_user = $%d", argIdx)
    args = append(args, params.OwnerUser)
    argIdx++
}
```

### Step 4: 修改 Live Stream

```go
// 文件: admin/live_stream_sse.go
// 在 HandleLiveStream 函数中

func (h *LiveStreamSSEHub) HandleLiveStream(w http.ResponseWriter, r *http.Request) {
    // 现有代码
    tenantID := ""
    isSuper := IsSuperAdminOrLegacy(r)
    if IsTenantAdmin(r) {
        tenantID = GetTenantID(r)
        isSuper = false
    }
    
    // 新增用户过滤
    username := ""
    auth := GetAuthContext(r)
    if auth != nil && auth.Role == "user" {
        username = auth.Username  // 🔧 替换为 auth.Username
    }
    
    // 在事件过滤逻辑中使用 username
    // ...
}
```

### Step 5: 测试验证

```bash
# 1. 编译测试
go build ./admin/dashboardapi
go build ./admin

# 2. 运行测试
go test -v ./admin/dashboardapi

# 3. 手动测试
# 以普通用户身份登录
# 访问 https://llm.itestu.cn/dashboard
# 验证只能看到自己的数据
```

---

## 五、验证清单

### 编译验证
- [ ] `go build ./admin/dashboardapi` 成功
- [ ] `go build ./admin` 成功
- [ ] 无语法错误和警告

### 功能验证
- [ ] super_admin 登录 Dashboard 能看全平台数据
- [ ] tenant_admin 登录 Dashboard 只能看本租户数据
- [ ] user 登录 Dashboard 只能看自己的数据
- [ ] Live 页面同样实现三级过滤

### 权限测试
- [ ] user 无法通过修改请求参数看到其他用户数据
- [ ] tenant_admin 无法跨租户访问
- [ ] 权限拒绝返回 403 错误

---

## 六、风险评估

### 低风险
- ✅ 修改是增量的（不影响现有功能）
- ✅ 有完整的测试套件验证
- ✅ 可以分批上线（先 Dashboard，再 Live）

### 中风险
- ⚠️ 需要手动修复占位符（容易遗漏）
- ⚠️ 需要修改多个文件（约10个端点）
- ⚠️ 查询性能影响（需要验证索引）

### 缓解措施
1. **代码审查**: 修改后进行 Code Review
2. **渐进上线**: 先在测试环境验证
3. **性能监控**: 确保 owner_user 列有索引
4. **回滚计划**: 保留原有逻辑作为 fallback

---

## 七、时间估算

| 任务 | 预计时间 | 负责人 |
|------|---------|--------|
| 修复占位符语法 | 5 分钟 | Owner |
| 修改权限过滤逻辑 | 30 分钟 | Owner |
| 修改 Dashboard 端点 | 2-3 小时 | Owner |
| 修改 Live Stream | 1 小时 | Owner |
| 测试验证 | 1-2 小时 | Owner |
| **总计** | **半天** | - |

---

## 八、Owner 行动清单

### 立即行动（5分钟）
1. ☐ 打开 `admin/dashboardapi/user_filter.go`
2. ☐ 找到第 42 行
3. ☐ 将占位符替换为 `auth.Username`
4. ☐ 运行 `go build ./admin/dashboardapi` 验证编译

### 今日完成（半天）
5. ☐ 修改 `admin/dashboardapi/auth.go` 添加用户过滤
6. ☐ 修改 5 个 Dashboard 端点的查询逻辑
7. ☐ 修改 Live Stream 的用户过滤
8. ☐ 运行测试验证功能

### 本周完成
9. ☐ 在测试环境部署验证
10. ☐ Code Review
11. ☐ 生产环境上线
12. ☐ 监控权限拒绝日志

---

## 九、参考资料

- 完整审计报告：`docs/security/data-permission-audit-2026-11-26.md`
- 权限API文档：`admin/context_enhanced.go`
- 修复示例代码：`admin/permission_fix_examples.go`
- 用户级端点实现：`admin/my_endpoints.go`

---

**关键点**：修复 `admin/dashboardapi/user_filter.go` 第42行的占位符是**阻塞性**任务，必须首先完成！
