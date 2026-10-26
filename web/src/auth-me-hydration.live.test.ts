// auth-me-hydration.live.test.ts — 真实端点上的水合判据（部署后验证）
//
// ★ 本文件是**部署后验证**，不是单元测试：
//   它对**线上**的 GET /api/auth/me 发一次真实请求（只用会话 cookie，
//   不带 Authorization 头 —— 这正是浏览器冷启的形态），
//   然后把**真实响应**喂给仓内真实的 userInfoFromAuthMe()，
//   并用**旧判据**跑一遍作对照。
//
//   判据：新的判据必须认出身份（⇒ 冷启不会 clearJwt ⇒ 不会被登出）；
//         旧判据必须认不出（⇒ 证明旧实现确实会清状态，即根因复现）。
//
// 运行方式（不设环境变量时整体跳过，不污染常规门禁）：
//   LIVE_AUTH_BASE_URL=https://llmgo.kxpms.cn \
//   LIVE_AUTH_PASSWORD=... npx vitest run src/auth-me-hydration.live.test.ts
//
// ⚠ 需要凭据的测试**不进**常规 `vitest run` 的必跑面：环境变量缺失即 skip。

import { describe, expect, it } from 'vitest'
import { userInfoFromAuthMe } from './store'

const BASE = process.env.LIVE_AUTH_BASE_URL ?? ''
const PASSWORD = process.env.LIVE_AUTH_PASSWORD ?? ''
const USER = process.env.LIVE_AUTH_USER ?? 'admin'
const ENABLED = Boolean(BASE && PASSWORD)

describe('真实端点上的冷启水合判据', () => {
  it.skipIf(!ENABLED)('★ 真实 /api/auth/me 响应必须被新判据认出（旧的认不出）', async () => {
    // 1) 登录，取回会话 cookie（浏览器里是 HttpOnly 的 llmgw_session）
    const loginRes = await fetch(`${BASE}/api/auth/token`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: USER, password: PASSWORD }),
    })
    expect(loginRes.status, '登录必须成功').toBe(200)
    const setCookie = loginRes.headers.get('set-cookie') ?? ''
    expect(setCookie, '登录必须下发 llmgw_session cookie').toContain('llmgw_session=')
    const cookie = setCookie.split(';')[0]

    // 2) 冷启形态：只有 cookie，没有 Authorization 头（内存里没有 JWT）
    const meRes = await fetch(`${BASE}/api/auth/me`, {
      headers: { Cookie: cookie },
    })
    expect(meRes.status, '/api/auth/me 必须 200').toBe(200)
    const me = await meRes.json()

    // 3) 线上真实形态的自证：用户嵌在 .user 下（顶层取不到 id）
    //    —— 这一条把「根因是什么」钉在**线上证据**上，而不是钉在注释里。
    expect(typeof me?.user?.id, '线上响应把用户嵌在 .user 下').toBe('number')
    expect(typeof me?.id, '顶层没有 id（旧判据读的就是这里）').toBe('undefined')

    // 4) 旧判据（修复前的 App.vue 判据）：必然认不出 ⇒ 必然走 clearJwt()
    const oldPredicateSeesUser =
      typeof me?.id === 'number' && typeof me?.username === 'string'
    expect(oldPredicateSeesUser, '旧判据必须认不出（根因复现）').toBe(false)

    // 5) 新判据（修复后）：必须认出 ⇒ 冷启不会清状态 ⇒ 不会被登出
    const user = userInfoFromAuthMe(me)
    expect(user, '新判据必须认出身份').not.toBeNull()
    expect(user?.id).toBe(me.user.id)
    expect(user?.role).toBe(me.user.role)
    expect(user?.tenant_id).toBe(me.user.tenant_id)
  })

  it.skipIf(!ENABLED)('★ 详情页要用的端点在真实会话下必须 200（不触发硬登出）', async () => {
    const loginRes = await fetch(`${BASE}/api/auth/token`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: USER, password: PASSWORD }),
    })
    const setCookie = loginRes.headers.get('set-cookie') ?? ''
    const cookie = setCookie.split(';')[0]
    const rid = process.env.LIVE_AUTH_REQUEST_ID ?? ''
    if (!rid) return

    // /api/logs/:id 不在 /api/admin/ 前缀下 —— 它 401 会走
    // isSessionExpiredAuthLoss ⇒ clearAll() + 硬跳登录页 ⇒ 又一次「登录态丢失」。
    for (const p of [`/api/admin/request-detail/${rid}`, `/api/logs/${rid}`]) {
      const r = await fetch(`${BASE}${p}`, { headers: { Cookie: cookie } })
      expect(r.status, `${p} 必须 200`).toBe(200)
    }
  })
})
