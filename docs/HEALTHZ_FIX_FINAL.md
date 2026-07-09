# healthz 401 错误 - 最终修复说明

## 问题根源

在调查过程中，我们发现了一个**关键的架构设计**：

### 后端设计（NET-007 fix）

```
/healthz           → 匿名访问（基础状态）✅ 前端可用
/healthz/full      → 需要静态 ADMIN_API_KEY ❌ 前端不可用
/healthz?full=true → 旧端点，需要 ADMIN_API_KEY ❌ 已废弃
```

**重要发现**：`/healthz/full` 需要的是**静态的运维 token**（`LLM_GATEWAY_ADMIN_API_KEY`），而不是用户的 JWT token。这意味着：

1. 前端用户（包括 admin 用户）的 JWT token **无法访问** `/healthz/full`
2. 这个端点是设计给**运维工具**（如监控系统、运维脚本）使用的
3. 前端应该只使用 `/healthz` 基础版本

## 修复策略

### 最初的修复（错误）❌
```typescript
// 尝试 /healthz/full，如果 401 就降级
health.value = await getHealth(true)  // 会返回 401
```
**问题**: 每次都会先尝试 `/healthz/full`，导致控制台出现 401 错误

### 最终修复（正确）✅
```typescript
// 直接使用 /healthz 基础版本
health.value = await getHealth(false)
```
**好处**: 
- 不会产生 401 错误
- 所有用户都能看到基础状态
- 符合后端的设计意图

## 修复的文件

1. **`web/src/api/system.ts`**
   - 更新端点格式：`/healthz?full=true` → `/healthz/full`
   - 保留此修改，虽然前端不使用 full 版本，但 API 应该正确

2. **`web/src/components/SystemStatusIndicator.vue`**
   - ❌ ~~尝试 full + 降级逻辑~~
   - ✅ 直接使用基础 `/healthz`

## 预期效果

### 修复后
- ✅ 控制台无 401 错误
- ✅ 所有用户（admin/普通）看到基础系统状态
- ✅ 基础状态包括：
  - Gateway 版本
  - 状态 (ok/error)
  - 基本健康信息

### 不包括的信息（需要 admin API key）
- 数据库详细信息
- Redis 详细信息  
- 后台任务详细信息

这些详细信息是给运维工具使用的，不是给前端用户的。

## 架构理解

```
┌─────────────────────────────────────────────────┐
│  前端用户（包括 admin）                          │
│  使用: JWT Token                                 │
│  可访问: /healthz (基础状态)                     │
└─────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────┐
│  运维工具（监控系统、脚本）                      │
│  使用: LLM_GATEWAY_ADMIN_API_KEY                │
│  可访问: /healthz/full (详细状态)               │
└─────────────────────────────────────────────────┘
```

## 总结

这次修复让我们更深入理解了系统的安全架构：
- **用户权限**（JWT）≠ **运维权限**（静态 API key）
- 前端不应该尝试访问运维端点
- 基础健康检查已经足够满足前端需求

---

**修复日期**: 2026-07-10 01:55  
**最终状态**: ✅ 正确理解架构，简化为使用基础端点
