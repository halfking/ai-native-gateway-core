# healthz 401 错误时间线分析

## 📅 时间线

### 2026-06-28 (约 12 天前)
**提交**: `905f8da7` - feat(llmgw): 28-Jun 综合改动

**后端改动** (NET-007 fix):
```
/healthz           → 匿名访问（基础状态）
/healthz/full      → 需要 ADMIN_API_KEY（运维专用）
/healthz?full=true → 旧格式，需要 ADMIN_API_KEY
```

**关键**: 后端已经将 `/healthz?full=true` 改为需要静态 admin token

**此时前端状态**: 未知（可能还没有 SystemStatusIndicator）

---

### 2026-07-08 (约 2 天前)
**提交**: `d9f4a3c0` - feat(system): add system status indicator + 2-min idle heartbeat

**前端改动**:
- 新增 `SystemStatusIndicator.vue` 组件
- **组件代码**: 调用 `getHealth(true)` → 发送到 `/healthz?full=true`
- **问题**: 前端不知道后端已经在 12 天前改变了认证方式

**此时状态**: 
- 前端: 调用 `/healthz?full=true` 
- 后端: 需要 ADMIN_API_KEY (静态 token)
- 用户 JWT token: 无法访问
- **结果**: 立即产生 401 错误！

---

### 2026-07-10 01:16 (约 2 小时前)
**提交**: `5911f1c9` - fix(frontend): _core.ts 401 + LoginView

**前端改动**:
- 修改 `_core.ts`，让 `/healthz?full=true` 的 401 不触发登录重定向
- 目的: 避免登录循环
- **问题**: 只是掩盖了错误，没有解决根本问题

**此时状态**: 401 错误仍然存在，只是不会触发登录循环了

---

### 2026-07-10 01:55 (现在)
**我们的修复**:

**发现问题**:
1. `/healthz/full` 是运维端点，需要静态 ADMIN_API_KEY
2. 用户 JWT token 无法访问该端点
3. 前端不应该调用运维专用端点

**修复方案**:
- `SystemStatusIndicator.vue`: 改用 `getHealth(false)` → 只调用 `/healthz`
- `system.ts`: 更新端点格式 `/healthz?full=true` → `/healthz/full`

---

## 🔍 为什么 5 小时前是好的？

**答案**: **5 小时前不是好的！**

根据时间线分析：

1. **2026-07-08** (2天前): `SystemStatusIndicator` 被添加时，就已经存在 401 错误
2. **5 小时前的状态**: 你可能：
   - 没有登录，所以没有触发 `SystemStatusIndicator` 的加载
   - 或者没有注意到控制台的 401 错误
   - 或者在 2 小时前的修复 (`5911f1c9`) 之前，401 会触发登录循环，你可能误认为是其他问题

**关键发现**: 这个 401 错误从 `SystemStatusIndicator` 被添加（2天前）就一直存在，只是之前可能被其他症状（登录循环）掩盖了。

---

## 🎯 根本原因

**架构不匹配**:
```
2026-06-28: 后端改为需要 ADMIN_API_KEY
            ↓
            12 天间隔
            ↓
2026-07-08: 前端添加组件，调用旧格式端点
            ↓
            立即产生 401 错误（但可能未被注意）
            ↓
2026-07-10: 修复 _core.ts 避免登录循环
            ↓
            401 错误暴露出来
            ↓
2026-07-10: 我们发现并正确修复
```

---

## 📝 经验教训

1. **前后端 API 变更需要同步更新**
   - 后端在 6月28日改了 API
   - 前端在 7月8日才添加使用该 API 的组件
   - 中间缺少沟通

2. **新功能开发时要检查 API 版本**
   - 添加 `SystemStatusIndicator` 时，应该检查当前 healthz API 的认证方式
   - 不应该假设 API 没有变化

3. **401 错误不一定是权限问题**
   - 有时候是认证方式不匹配
   - 有时候是端点用错了

4. **掩盖错误不如解决错误**
   - `5911f1c9` 提交只是让 401 不触发重定向
   - 没有解决根本问题
   - 我们的修复才是正确的

---

**结论**: 5 小时前**不是好的**，只是错误被其他症状掩盖了。真正的问题从 2 天前就存在。

---

*分析完成时间: 2026-07-10 02:00*
