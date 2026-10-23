// auth-me-hydration.test.ts — 冷启水合的登录态判据（2026-10-23 登录态丢失根因）
//
// ★ 被这组判据钉住的线上事故：
//   用户点「实时拓扑」被弹回登录页；从请求列表进详情也偶发掉登录态。
//   取证（245 生产日志 + 真实响应）确认：**与后端 401 无关**，
//   `/api/admin/topology/*` 全部 200。真正的链条是：
//
//   冷启（首屏 / 刷新 / 深链 / 新标签页 / location.reload()）
//     → App.vue 探测 GET /api/auth/me
//     → 该端点真实响应把用户**嵌在 `.user` 下**
//         {"access_token":[REDACTED:secret]],"expires_at":…,"user":{"id":41,"username":…}}
//     → 旧判据直接读 `raw.id` ⇒ undefined ⇒ 判「形状异常」
//     → 落进水合 catch 的 `clearJwt()` ⇒ userInfo + localStorage 双清
//     → `/live` 等 requiresAuth 路由 `isAuthed()` 假 → 弹回 `/?login=super_admin]   ⇒ 用户观感 =「被登出」
//
// 为什么总览没事：`/` 是 public 路由，且不经过冷启 ⇒ 这条水合路径根本不跑。
// 为什么会「运行一段时间后就好了」：只有**发生冷启**的那一次才会触发；
// 纯 SPA 内跳转不会重新探测。

import { describe, expect, it } from 'vitest'
import { userInfoFromAuthMe, setUserInfo, store, clearJwt } from './store'
import { envelopeCodeOfObject, isMaintenanceEnvelopeCode, envelopeCode } from './api/_core'



/** 生产实测形状（245，`GET /api/auth/me` 200）。 */
const WIRE_NESTED = {
  access_token: 'jwt-abc',
  expires_at: '2026-10-30T15:10:39+08:00',
  user: {
    id: 41,
    tenant_id: 'default',
    username: '[REDACTED:account]',
    display_name: '系统管理员',
    email: '',
    role: 'super_admin',
    enabled: true,
    last_login_at: '2026-10-23T15:02:18.835978+08:00',
    created_at: '2026-07-15T03:12:29.48647+08:00',
  },
}

/** 降级形状（JWT 签发失败，admin/users.go:678 直接 writeJSON(u)）。 */
const WIRE_FLAT = { ...WIRE_NESTED.user }

/** 维护/边缘窗口的顶层信封（2xx，无身份字段）。 */
const WIRE_MAINTENANCE = { code: 'maintenance_mode', message: '维护中', request_id: 'r-1' }
const WIRE_MAINTAIN_NS = { code: 'maintain.upstream_degraded', message: '依赖降级' }

describe('userInfoFromAuthMe — 身份判据必须与 setUserInfo 同源', () => {
  it('★ 认生产实测的嵌套形态（用户嵌在 .user 下）—— 这条红了就是事故复发', () => {
    const u = userInfoFromAuthMe(WIRE_NESTED)
    expect(u).not.toBeNull()
    expect(u?.id).toBe(41)
    expect(u?.role).toBe('super_admin')
    expect(u?.tenant_id).toBe('default')
  })

  it('认降级的扁平形态', () => {
    const u = userInfoFromAuthMe(WIRE_FLAT)
    expect(u?.id).toBe(41)
    expect(u?.username).toBe(WIRE_FLAT.username)
  })

  it('挡掉维护信封 —— 绝不能把它当成 userInfo 造成假登录态', () => {
    expect(userInfoFromAuthMe(WIRE_MAINTENANCE)).toBeNull()
    expect(userInfoFromAuthMe(WIRE_MAINTAIN_NS)).toBeNull()
  })

  it('挡掉残缺 / 空 / 非对象体', () => {
    expect(userInfoFromAuthMe(null)).toBeNull()
    expect(userInfoFromAuthMe(undefined)).toBeNull()
    expect(userInfoFromAuthMe('nope')).toBeNull()
    expect(userInfoFromAuthMe({})).toBeNull()
    // id 类型不对（字符串）也必须判否，否则 role 能过而 id 过不了
    expect(userInfoFromAuthMe({ ...WIRE_FLAT, id: '41' })).toBeNull()
    expect(userInfoFromAuthMe({ ...WIRE_FLAT, username: 123 })).toBeNull()
  })

  it('★ 解析结果与 setUserInfo 的归一口径一致（不得「校验比 setter 严」）', () => {
    // 同一份响应：走解析器得到的身份，必须等于直接喂给 setter 后落库的身份。
    // 这条通用式是本次事故的根：判据读的面 ≠ setter 读的面。
    clearJwt()
    setUserInfo(WIRE_NESTED as never)
    const viaSetter = { ...store.userInfo }
    clearJwt()
    setUserInfo(userInfoFromAuthMe(WIRE_NESTED))
    const viaResolver = { ...store.userInfo }
    expect(viaResolver).toEqual(viaSetter)
    expect(viaResolver?.id).toBe(41)
    // 收尾：别把登录态留给同文件后面的用例
    clearJwt()
  })
})

describe('envelopeCodeOfObject — 自造 Error 也能把 code 带给判据', () => {
  it('取顶层 code 与 error.code，优先级同 envelopeCode', () => {
    expect(envelopeCodeOfObject(WIRE_MAINTENANCE)).toBe('maintenance_mode')
    expect(envelopeCodeOfObject({ error: { code: 'maintain.x' } })).toBe('maintain.x')
    // error.code 优先于顶层
    expect(envelopeCodeOfObject({ code: 'top', error: { code: 'nested' } })).toBe('nested')
    expect(envelopeCodeOfObject(null)).toBe('')
    expect(envelopeCodeOfObject('str')).toBe('')
    expect(envelopeCodeOfObject({})).toBe('')
  })

  it('与字符串版 envelopeCode 逐字同口径', () => {
    const wire = JSON.stringify(WIRE_MAINTENANCE)
    expect(envelopeCodeOfObject(JSON.parse(wire))).toBe(envelopeCode(wire))
    expect(envelopeCode('not json')).toBe('')
  })

  it('★ 维护码真的能被识别 —— App.vue 自抛 Error 时挂的就是它', () => {
    // 复现 App.vue 水合 catch 的判据形状：自造 Error 携带 code/status。
    const err = Object.assign(new Error('unexpected /api/auth/me payload shape'), {
      status: 200,
      code: envelopeCodeOfObject(WIRE_MAINTENANCE),
    }) as { status?: unknown; code?: unknown }
    const maintenance = isMaintenanceEnvelopeCode(err.code)
    const status = err.status
    const transient = status === 0 || (typeof status === 'number' && status >= 500 && status !== 501)
    // 维护态 ⇒ 不清任何状态（否则维护窗口把用户踢回登录页）
    expect(maintenance).toBe(true)
    expect(maintenance || transient).toBe(true)
  })
})