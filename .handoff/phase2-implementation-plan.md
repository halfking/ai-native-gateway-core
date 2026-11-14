# Phase 2 实施计划 - Handler Refactoring

**目标**: 将现有 handlers 重构为使用 plugin registry，替代直接数据库访问

**预计时间**: 8-10 小时

## 验收标准

- [ ] 在 `cmd/gateway/main.go` 中创建全局 auth registry
- [ ] 重构 `admin/auth.go` 的 handleLogin 使用 AuthProvider
- [ ] 重构 `admin/users.go` 的用户管理 handlers 使用 UserManager
- [ ] 所有现有测试通过（无回归）
- [ ] API 响应格式保持兼容
- [ ] 代码编译通过

## 实施步骤

### Step 2.1: 创建全局 Registry (1-2小时)

**目标**: 在 main.go 中初始化并注册所有 native providers

**任务**:
1. 在 main.go 中添加 initAuthRegistry 函数
2. 在 main() 中初始化 registry
3. 将 registry 传递给 admin handlers

**修改文件**:
- `cmd/gateway/main.go`
  - 添加 initAuthRegistry() 函数
  - 在数据库初始化后调用
  - 将 registry 传递给 admin.NewHandler()

**代码变更**:
```go
// 在 main.go 添加（约在 line 100 附近，数据库初始化后）
func initAuthRegistry(db *pgxpool.Pool, jwtSecret string) (*authplugin.Registry, error) {
    config := &authplugin.Config{
        DefaultAuthProvider: "native",
        DefaultUserManager: "native",
        DefaultTenantManager: "native",
    }
    
    registry := authplugin.NewRegistry(config)
    
    // Register native providers
    nativeAuth, err := native.NewNativeAuthProvider(db, jwtSecret)
    if err != nil {
        return nil, fmt.Errorf("create native auth provider: %w", err)
    }
    
    nativeUser, err := native.NewNativeUserManager(db)
    if err != nil {
        return nil, fmt.Errorf("create native user manager: %w", err)
    }
    
    nativeTenant, err := native.NewNativeTenantManager(db)
    if err != nil {
        return nil, fmt.Errorf("create native tenant manager: %w", err)
    }
    
    if err := registry.RegisterAuthProvider(nativeAuth); err != nil {
        return nil, err
    }
    if err := registry.RegisterUserManager(nativeUser); err != nil {
        return nil, err
    }
    if err := registry.RegisterTenantManager(nativeTenant); err != nil {
        return nil, err
    }
    
    return registry, nil
}
```

### Step 2.2: 重构 Login Handler (2-3小时)

**目标**: 将 admin/auth.go 的 handleLogin 改为使用 AuthProvider

**修改文件**:
- `admin/auth.go`
  - 修改 Handler struct 添加 registry 字段
  - 重构 handleLogin() 使用 AuthProvider.Authenticate()
  - 保持响应格式兼容

**关键变更**:
1. Handler struct 添加 registry:
```go
type Handler struct {
    db       *pgxpool.Pool
    registry *authplugin.Registry  // 新增
    // ... 其他字段
}
```

2. 重构 handleLogin:
```go
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
    // 1. 解析请求（保持不变）
    var req loginRequest
    // ...
    
    // 2. 获取 AuthProvider
    authProvider, err := h.registry.GetAuthProvider()
    if err != nil {
        writeError(w, http.StatusInternalServerError, "authentication service unavailable")
        return
    }
    
    // 3. 调用 Authenticate
    authReq := &authplugin.AuthRequest{
        Username: req.Username,
        Password: req.Password,
        // ... 其他字段
    }
    
    result, err := authProvider.Authenticate(r.Context(), authReq)
    if err != nil {
        // 错误处理（保持兼容）
        switch err {
        case authplugin.ErrInvalidCredentials:
            writeError(w, http.StatusUnauthorized, "Invalid credentials")
        // ... 其他错误
        }
        return
    }
    
    // 4. 返回响应（保持格式兼容）
    writeJSON(w, http.StatusOK, map[string]any{
        "token": result.Token,
        "expires_at": result.ExpiresAt.Format(time.RFC3339),
        "user": map[string]any{
            "id": result.UserID,
            "username": result.Username,
            // ... 其他字段
        },
    })
}
```

### Step 2.3: 重构 User Handlers (3-4小时)

**目标**: 将 admin/users.go 的所有用户管理 handlers 改为使用 UserManager

**修改文件**:
- `admin/users.go`
  - listUsers() -> UserManager.ListUsers()
  - getUser() -> UserManager.GetUser()
  - createUser() -> UserManager.CreateUser()
  - updateUser() -> UserManager.UpdateUser()
  - deleteUser() -> UserManager.DeleteUser()
  - resetPassword() -> UserManager.ResetPassword()

**示例 - listUsers**:
```go
func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
    userManager, err := h.registry.GetUserManager()
    if err != nil {
        writeError(w, http.StatusInternalServerError, "user service unavailable")
        return
    }
    
    // 解析查询参数
    filter := &authplugin.UserFilter{
        TenantID: r.URL.Query().Get("tenant_id"),
        Role: r.URL.Query().Get("role"),
        // ... 其他过滤条件
    }
    
    users, err := userManager.ListUsers(r.Context(), filter)
    if err != nil {
        writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
    
    // 转换为响应格式（保持兼容）
    writeJSON(w, http.StatusOK, map[string]any{
        "users": users,
        "total": len(users),
    })
}
```

### Step 2.4: 测试和验证 (1-2小时)

**任务**:
1. 运行所有现有测试确保无回归
2. 手动测试关键API端点
3. 验证响应格式兼容性
4. 性能对比（确保无性能下降）

**测试清单**:
- [ ] `go test ./admin/...` 全部通过
- [ ] `go test ./cmd/gateway/...` 全部通过
- [ ] 手动测试 POST /admin/auth/login
- [ ] 手动测试 GET /admin/users
- [ ] 手动测试 POST /admin/users
- [ ] 手动测试 PATCH /admin/users/:id
- [ ] 手动测试 DELETE /admin/users/:id

## 风险和注意事项

### 高风险
1. **API 响应格式变更**: 必须保持完全兼容，否则会破坏前端
2. **错误处理映射**: 需要正确映射 plugin errors 到 HTTP 状态码
3. **性能回归**: 新的抽象层可能影响性能

### 缓解措施
1. 保留原有响应结构，只改变数据来源
2. 创建错误映射函数，统一处理
3. 在关键路径添加性能监控

### 中风险
1. **测试覆盖不足**: 现有测试可能不覆盖所有边界情况
2. **并发问题**: Registry 需要是线程安全的

### 缓解措施
1. 手动测试补充自动化测试
2. Registry 已经实现了线程安全（使用 sync.RWMutex）

## 回滚计划

如果 Phase 2 出现重大问题：
1. Git revert 到 Phase 1 完成的 commit (8e7850b8a)
2. 使用 feature flag 控制新旧代码路径
3. 渐进式迁移：先迁移非关键路径

## 成功指标

- [ ] 所有现有测试通过
- [ ] 新代码编译无错误
- [ ] API 响应格式100%兼容
- [ ] 无性能回归（<5% 延迟增加）
- [ ] 代码审查通过

## 预计时间分配

| 步骤 | 预计时间 | 优先级 |
|------|---------|--------|
| 2.1 创建 Registry | 1-2小时 | P0 |
| 2.2 Login Handler | 2-3小时 | P0 |
| 2.3 User Handlers | 3-4小时 | P0 |
| 2.4 测试验证 | 1-2小时 | P0 |
| **总计** | **8-10小时** | |

## 下一步行动

1. ✅ 创建 Phase 2 实施计划（本文档）
2. ⏭️ 开始 Step 2.1: 在 main.go 中添加 initAuthRegistry
3. ⏭️ 修改 admin.Handler 支持 registry
4. ⏭️ 逐步重构各个 handler

---

**创建时间**: 2026-11-14  
**作者**: ZCode AI Assistant  
**状态**: 待执行
