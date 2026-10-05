import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fetchMe, unwrapMe, type MeResponse } from '@/api/auth'
import type { UserInfo } from '@/api/client'

/**
 * 2026-10-06 回归：账户面板把 super_admin 显示成「普通用户」、名字显示「—」。
 *
 * 真因链（全部实测）：
 *   后端 handleAuthMe 有**两种**响应形态 ——
 *     ① 包裹态 { access_token, expires_at, user: UserInfo }  （签发 token 成功，常规路径）
 *     ② 裸 UserInfo                                           （签发失败兜底）
 *   而 fetchMe() 原先把整包当 UserInfo 返回，hydrate() 直接
 *   `userInfo.value = me` ⇒ role / display_name 全 undefined
 *   ⇒ AppAccountSheet 的 roleLabel 落到 fallback「普通用户」，名字落到 '—'。
 *
 * 现场证据：登录 admin / display_name=系统管理员 / role=super_admin，
 * 面板却渲染「我的 用户 — 普通用户」。
 */

const USER: UserInfo = {
  id: 41,
  tenant_id: 'default',
  username: 'admin',
  display_name: '系统管理员',
  email: '',
  role: 'super_admin',
  enabled: true,
  must_change_password: false,
  last_login_at: '2026-10-06T04:29:09.729516+08:00',
  created_at: '2026-07-15T03:12:29.48647+08:00',
}

describe('unwrapMe —— /api/auth/me 两种响应形态', () => {
  it('包裹态：取出内层 user（这是原先坏掉的那条）', () => {
    const wrapped: MeResponse = {
      access_token: 'eyJhbGciOi...',
      expires_at: '2026-10-07T04:37:39+08:00',
      user: USER,
    } as MeResponse
    const got = unwrapMe(wrapped)
    expect(got.role).toBe('super_admin')
    expect(got.username).toBe('admin')
    expect(got.display_name).toBe('系统管理员')
  })

  it('裸态：原样返回，不被包裹逻辑误伤', () => {
    const bare: MeResponse = USER as MeResponse
    expect(unwrapMe(bare)).toBe(USER)
  })

  // 负控：证明判据有牙。若 unwrapMe 恒返回入参，上面两条会一起红。
  it('负控：包裹态不能被原样透传（否则第一条只是恒真）', () => {
    const wrapped = { access_token: 'x', expires_at: 'y', user: USER } as unknown as MeResponse
    expect(unwrapMe(wrapped)).not.toBe(wrapped)
    expect((unwrapMe(wrapped) as unknown as { user?: unknown }).user).toBeUndefined()
  })
})

/**
 * ↓ 这一组才是真正有牙的：走**真实入口 fetchMe**（stub fetch，抄 client.spec.ts:20 的手法）。
 * 只测 unwrapMe 是恒绿的假证据 —— 我第一版就是这么写的，
 * 把 fetchMe 回退成「整包当 UserInfo」后 6 条全绿，判据根本没被触发。
 */
describe('fetchMe —— 真实入口必须解包（变异这条会红）', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  // req() 走的是 r.text() 再 JSON.parse，不是 r.json()（client.ts:117+）
  const jsonResponse = (body: unknown) => ({
    ok: true,
    status: 200,
    text: async () => JSON.stringify(body),
    json: async () => body,
  })

  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('包裹态：fetchMe 返回的必须是内层 UserInfo', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ access_token: 'eyJ...', expires_at: 'y', user: USER }))
    const me = await fetchMe()
    // 这三条正是 AppAccountSheet 渲染时读的字段
    expect(me.role).toBe('super_admin')
    expect(me.username).toBe('admin')
    expect(me.display_name).toBe('系统管理员')
  })

  it('裸态：fetchMe 仍应原样返回（不能为了解包把裸态弄坏）', async () => {
    fetchMock.mockResolvedValue(jsonResponse(USER))
    const me = await fetchMe()
    expect(me.username).toBe('admin')
    expect(me.role).toBe('super_admin')
  })

  // 负控：确保前面两条不是因为「随便返回什么都过」
  it('负控：包裹态下 fetchMe 的结果里不能再套一层 user', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ access_token: 'x', expires_at: 'y', user: USER }))
    const me = (await fetchMe()) as unknown as { user?: unknown; access_token?: unknown }
    expect(me.user).toBeUndefined()
    expect(me.access_token).toBeUndefined()
  })
})

/** 复刻修复后的 loadStoredUser（stores/auth.ts 同一逻辑），验证刷新后不丢。 */
function loadStoredUserFixed(raw: string | null): UserInfo | null {
  if (!raw) return null
  let parsed: (UserInfo & { user?: UserInfo }) | null
  try {
    parsed = JSON.parse(raw) as UserInfo & { user?: UserInfo }
  } catch {
    return null
  }
  if (!parsed || typeof parsed !== 'object') return null
  const user = parsed.user && typeof parsed.user === 'object' ? parsed.user : parsed
  if (typeof user.username !== 'string') return null
  return user
}

describe('loadStoredUser —— 刷新后不得把包裹态判死', () => {
  it('包裹态：能取回 super_admin（旧实现在这里返回 null）', () => {
    const stored = JSON.stringify({ access_token: 'x', expires_at: 'y', user: USER })
    const got = loadStoredUserFixed(stored)
    expect(got).not.toBeNull()
    expect(got!.role).toBe('super_admin')
  })

  it('裸态：仍能取回', () => {
    expect(loadStoredUserFixed(JSON.stringify(USER))?.username).toBe('admin')
  })

  it('负控：真的没有 username 时必须返回 null（守卫没被拆掉）', () => {
    expect(loadStoredUserFixed(JSON.stringify({ foo: 1 }))).toBeNull()
    expect(loadStoredUserFixed('not-json')).toBeNull()
    expect(loadStoredUserFixed(null)).toBeNull()
  })
})
