# Community Mode 完整实施 TODO

> 创建时间: 2026-07-21
> 关联: `cmd/gateway/main.go:362` `licensing/community_mode.go`

## 1. 当前状态

Community mode marker file 检测已经实现 (`licensing.IsCommunityMode()`)，
启动时会输出告警日志，但**租户数量限制尚未在 middleware 层强制执行**。

当前实现的限制：

```go
// cmd/gateway/main.go:362
if licensing.IsCommunityMode() {
    slog.Warn("running in community mode",
        "max_tenants", licensing.MaxCommunityTenants,
        "features", "basic_api_only",
        "restrictions", licensing.GetCommunityModeRestrictions()["restrictions"])
    // 2026-07-21: 社区模式的租户数量限制通过 admin API 主动检查
    // （创建租户时校验），不在 middleware 层强制
}
```

## 2. 为什么不在 middleware 层强制？

社区模式限制 = **租户总数 ≤ 2**。要在 middleware 层强制，意味着：

| 问题 | 难度 |
|------|------|
| 每次请求都查 DB 数租户数 → 性能影响 | 高 |
| 缓存租户数 → 一致性问题 | 中 |
| 区分"读租户"vs"创建租户" → middleware 需要业务上下文 | 高 |
| 处理 race condition（两个请求并发创建） → 需要 DB 唯一约束配合 | 中 |

**当前推荐方案**：在 admin 创建租户 API 内做前置校验（handler-layer check），
而不是 middleware 全局拦截。这样：

- 性能零影响（创建租户是低频操作）
- 中间件保持简单（不耦合业务规则）
- 配合 DB 唯一约束防止 race condition

## 3. 待完成的具体工作

### 3.1 创建租户 API 增加前置校验

```go
// domains/admin/tenants.go 或 cmd/gateway/main.go
func handleCreateTenant(db *pgxpool.Pool, req CreateTenantReq) error {
    if licensing.IsCommunityMode() {
        var count int
        if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM tenants").Scan(&count); err != nil {
            return fmt.Errorf("count tenants: %w", err)
        }
        if count >= licensing.MaxCommunityTenants {
            return echo.NewHTTPError(http.StatusForbidden,
                "community_mode: tenant limit reached (max "+
                strconv.Itoa(licensing.MaxCommunityTenants)+"). Upgrade license to add more.")
        }
    }
    // ... existing logic
}
```

### 3.2 添加单元测试

```go
func TestCreateTenant_CommunityModeRejects(t *testing.T) {
    // 模拟 community mode + 已达租户上限 → 应该返回 403
}

func TestCreateTenant_CommunityModeAllowsBelow(t *testing.T) {
    // 模拟 community mode + 租户数 < 上限 → 应该成功
}

func TestCreateTenant_LicensedModeIgnoresLimit(t *testing.T) {
    // 模拟非 community mode → 应该不检查租户数
}
```

### 3.3 在 API 文档中注明

`docs/api/admin/tenants.md` 添加：

```markdown
### 社区模式限制

当 gateway 运行在 community mode（license 缺失/过期/未激活）时，
`POST /api/admin/tenants` 限制创建租户数量 ≤ 2。

超过限制返回 403 Forbidden:
```json
{
  "error": "community_mode_tenant_limit",
  "message": "Community mode allows max 2 tenants. Activate a license to remove this limit."
}
```
```

### 3.4 Ops 仪表盘告警

`/api/admin/ops/overview` 已经返回 `licenseModeRestricted` 等字段。
需要新增：
- `licenseMode: "community"` 标识当前是 community mode
- `communityTenantsUsed: N` 已用租户数
- `communityTenantsMax: 2` 上限
- 当 `communityTenantsUsed >= communityTenantsMax` 时仪表盘显示警告横幅

## 4. 验收标准

- [ ] 创建租户 API 在 community mode 下拒绝超额请求
- [ ] 单元测试覆盖 3 个核心场景
- [ ] API 文档更新
- [ ] Ops 仪表盘告警集成
- [ ] 245 / 154 环境端到端验证：
  - 模拟 community mode 创建第 3 个租户 → 403
  - 激活 license → 限制解除

## 5. 关联 Issue

无 — 待创建。

## 6. 优先级

**Low** — 商业版部署（licensed mode）不受影响，社区模式是 trial/expired 场景，
社区用户少且可控。V1.0 可暂缓。
