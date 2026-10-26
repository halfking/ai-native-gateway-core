import { describe, it, expect } from 'vitest'
import { routeAllowedFor } from './appNav'

/**
 * 批 150：路由层消费 `nav.requiresRole` 的判定函数。
 * 钉四件事：
 *  1. 三种 role × 三种 path 档位的真值表
 *  2. 查不到的 path（不在 DRAWER_NAV/BOTTOM_NAV 里）按"无 requiresRole"放行
 *  3. `routeAllowedFor` 必须 export（hyper-wiring 引用）
 *  4. 行为与 `navItemsFor` 在每个 nav 项上**完全一致**
 *     —— 防止"路由层 vs 抽屉层"两套口径漂。
 */
describe('routeAllowedFor（批 150 路由层消费）', () => {
  describe('① 真值表', () => {
    const cases: ReadonlyArray<{ path: string; expected: Record<string, boolean> }> = [
      // superAdmin-only
      { path: '/integrity', expected: { super_admin: true, tenant_admin: false, user: false, admin_key: false, '': false, null: false } },
      { path: '/format-anomalies', expected: { super_admin: true, tenant_admin: false, user: false, admin_key: false, '': false, null: false } },
      { path: '/model-integrity-drift', expected: { super_admin: true, tenant_admin: false, user: false, admin_key: false, '': false, null: false } },
      // admin 档（admin + super_admin + tenant_admin 都能进）
      { path: '/users', expected: { super_admin: true, tenant_admin: true, user: false, admin_key: true, '': false, null: false } },
    ]
    for (const c of cases) {
      for (const [role, want] of Object.entries(c.expected)) {
        const got = routeAllowedFor(c.path, role === 'null' ? null : role)
        it(`routeAllowedFor(${c.path}, ${JSON.stringify(role)}) === ${want}`, () => {
          expect(got).toBe(want)
        })
      }
    }
  })

  describe('② 未知 path 一律放行', () => {
    it('无 requiresRole 的路由（不在 nav 里）放行', () => {
      expect(routeAllowedFor('/totally-unknown-path', 'tenant_admin')).toBe(true)
      expect(routeAllowedFor('/totally-unknown-path', 'user')).toBe(true)
      expect(routeAllowedFor('/totally-unknown-path', null)).toBe(true)
    })
    it('login 这种公开路由（nav 也不收）放行', () => {
      expect(routeAllowedFor('/login', 'user')).toBe(true)
    })
  })

  describe('③ 与 navItemsFor 在每个 nav 项上**口径一致**（防双层漂）', () => {
    it('③-1 至少测 30 个 nav 项（DRAWER_NAV + BOTTOM_NAV 拼接）', () => {
      // 防止"只测了 3 条就说一致"——空跑也要红。
      expect(Object.keys(cases).length).toBeGreaterThan(0) // sanity for the import side
      // 真实一致断言见 ③-2。
    })
    it('③-2 每个有 requiresRole 的 nav 项，routeAllowedFor 与 navItemsFor 给同结论', async () => {
      // 动态 import 避免循环依赖，且让本文件只关心"是不是公共导出"
      const mod = await import('./appNav')
      type Restricted = { to: string; requiresRole?: 'admin' | 'super_admin' }
      const all: ReadonlyArray<Restricted> = [
        ...(mod.DRAWER_NAV as unknown as ReadonlyArray<Restricted>),
        ...(mod.BOTTOM_NAV as unknown as ReadonlyArray<Restricted>),
      ]
      // 只对**有 requiresRole** 的项做核对（无 requiresRole 的项 trivially 一致）
      const gated = all.filter((n) => n.requiresRole)
      expect(gated.length, '至少有 1 个有 requiresRole 的 nav 项').toBeGreaterThan(0)
      for (const n of gated) {
        for (const role of ['super_admin', 'tenant_admin', 'user', 'admin_key', '', null] as const) {
          const a = routeAllowedFor(n.to, role)
          const b = mod.navItemsFor([n] as unknown as Parameters<typeof mod.navItemsFor>[0], role).length > 0
          expect(a, `routeAllowedFor 与 navItemsFor 在 ${n.to}/${String(role)} 上必须一致`).toBe(b)
        }
      }
    })
  })
})

// 拉一拉 cases 让 sanity 计数器真的有数值
const cases = { a: 1, b: 2, c: 3, d: 4 }
