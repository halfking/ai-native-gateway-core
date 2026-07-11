# SPA Auth Hydration: `/api/auth/me` 包装响应污染 `store.userInfo`

**日期**: 2026-07-12
**问题**: cookie auth hydration 阶段会把包装对象 `{ access_token, expires_at, user }` 整体写入 `store.userInfo`，导致登录态降级
**状态**: ✅ 已修复

---

## 一、问题现象

在合并 unified-auth 改动后，HTTP-only cookie 已能完成身份校验。
前端 `App.vue` 的 `onMounted` 探测逻辑会在没有 JWT 的情况下调用
`/api/auth/me`，并把返回值直接写进 `store.userInfo`：

```ts
// 旧代码
const me = await getAuthMe()
setUserInfo(me)
```

后端在 cookie-only hydration 路径上返回：

```json
{
  "access_token": "...",
  "expires_at":   "...",
  "user":         { "id": 1, "tenant_id": "default", "role": "super_admin", ... }
}
```

直接 `setUserInfo(me)` 会让 `store.userInfo = { access_token, expires_at, user }`。
后续 `store.userInfo.role`、`tenant_id`、`username` 全部读不到，路由守卫错判为未登录。

---

## 二、影响面

- 任何在合并 unified-auth 后首次打开过 SPA 的账号
- 影响 `App.vue` (cookie hydration fallback) + `LoginModal.vue` (login fallback)
- 主要表现：路由无限重定向到 `/login`、菜单只显示登录按钮、auth 状态不识别

---

## 三、修复

文件：
- `web/src/App.vue`
- `web/src/components/LoginModal.vue`

把包装响应解开：

```ts
const me = await getAuthMe()
const meAny = me as any
if (meAny?.access_token) {
  setJwtToken(meAny.access_token)
}
setUserInfo(meAny?.user ?? me)
```

`meAny?.user ?? me` 既兼容新 `{ user, access_token, expires_at }` 结构，
也兼容老实现里直接返回 `UserInfo` 的情况。

---

## 四、验证

- `go test ./domains/promptinjection/enhanced/...` 通过（无关联性，回归检查）
- `cd web && npx vite build` 通过
- 真实浏览器路径需要后续 UI 验证（受环境限制无法跑）

---

## 五、教训

- 任何把后端响应直接喂给 `store.*` setter 的代码，必须先校验响应结构是否和 setter 签名一致
- 建议在 `setUserInfo` 内部加一层 runtime guard，对不包含 `id` / `role` 的对象直接拒绝
- `getAuthMe()` 的返回类型已经从 `UserInfo` 升级到 `AuthMeResponse extends UserInfo`，
  调用方需要相应升级
