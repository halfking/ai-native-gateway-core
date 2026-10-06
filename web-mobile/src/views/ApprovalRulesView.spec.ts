import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ApprovalRulesView from './ApprovalRulesView.vue'
import {
  fetchApprovalApprovers,
  fetchApprovalRules,
  type Approver,
  type ApproverList,
  type ApprovalRule,
  type ApprovalRuleList,
} from '@/api/approvalConfig'
import { setLocale, locale } from '@/i18n'

/**
 * ApprovalRulesView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 这两个端点的 SQL **都带 `AND enabled = true`** ⇒ 停用的不在这儿。
 * 2. ★★★★ 空列表序列化成 **`null`** ⇒ 页面必须渲染成「0 条」而不是崩/当错误。
 * 3. ★★ 邮箱/手机是**键不存在** ⇒ 显示「未填」不留空白。
 * 4. ★★ 优先级方向相反（审批人升序、规则降序）。
 * 5. ★★ 抛错不许退化成空清单。
 */

const routeMock: { value: { query: Record<string, string> } } = { value: { query: { tenant: 'acme' } } }
vi.mock('vue-router', () => ({ useRoute: () => routeMock.value }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/approvalConfig', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/approvalConfig')>()
  return { ...actual, fetchApprovalApprovers: vi.fn(), fetchApprovalRules: vi.fn() }
})

const appMock = fetchApprovalApprovers as unknown as ReturnType<typeof vi.fn>
const ruleMock = fetchApprovalRules as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function approver(over: Record<string, unknown> = {}): Approver {
  return {
    user_id: 'u1',
    name: 'Alice',
    role: 'admin',
    priority: 1,
    enabled: true,
    ...over,
  } as Approver
}

function rule(over: Record<string, unknown> = {}): ApprovalRule {
  return {
    name: 'high-cost',
    enabled: true,
    priority: 10,
    conditions: [{ field: 'cost', operator: 'gt', value: '100' }],
    action: { type: 'require_approval', risk_level: 'HIGH', reason: 'expensive' },
    ...over,
  } as ApprovalRule
}

/** ★ helper 接受覆盖 + instanceof Error 分派。 */
function setApp(m: typeof appMock, v: unknown, dft: unknown = { approvers: [approver()], count: 1 } as ApproverList) {
  if (v instanceof Error) m.mockRejectedValue(v)
  else m.mockResolvedValue(v ?? dft)
}
function setRule(m: typeof ruleMock, v: unknown, dft: unknown = { rules: [rule()], count: 1 } as ApprovalRuleList) {
  if (v instanceof Error) m.mockRejectedValue(v)
  else m.mockResolvedValue(v ?? dft)
}

async function mountView(
  opts: { approvers?: unknown; rules?: unknown; query?: Record<string, string> } = {},
): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  routeMock.value = { query: opts.query ?? { tenant: 'acme' } }
  setApp(appMock, opts.approvers)
  setRule(ruleMock, opts.rules)
  const w = mount(ApprovalRulesView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  routeMock.value = { query: { tenant: 'acme' } }
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

describe('★★★★★★ 抽屉席**必须不设** requiresRole（admin 档）', () => {
  it('★★★★★★ approval-rules 席没有 requiresRole', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'approval-rules')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBeUndefined()
  })
})

describe('★★★★★★★★ 空列表是 `null`，不许崩也不许当错误', () => {
  it('★★★★★★★★ `approvers: null` ⇒ 渲染成「没有启用中的审批人」且**无错误态**', async () => {
    const w = await mountView({
      approvers: { approvers: null, count: 0 },
      rules: { rules: null, count: 0 },
    })
    expect(w.findAll('.ar__msg--err')).toHaveLength(0)
    expect(w.text()).toContain('没有启用中的审批人')
    expect(w.text()).toContain('没有启用中的规则')
    expect(w.text()).toContain('显示 0 条')
  })

  it('★★★★★★★★ `null` 与 `[]` 渲染结果相同（归一化）', async () => {
    const withNull = await mountView({
      approvers: { approvers: null, count: 0 },
      rules: { rules: null, count: 0 },
    })
    const t1 = withNull.findAll('.ar__msg').map((n) => n.text())
    for (const w of mountedList) w.unmount()
    mountedList = []
    const withEmpty = await mountView({
      approvers: { approvers: [], count: 0 },
      rules: { rules: [], count: 0 },
    })
    const t2 = withEmpty.findAll('.ar__msg').map((n) => n.text())
    expect(t1).toEqual(t2)
  })

  it('★★★★★ 有数据时正常渲染条目', async () => {
    const w = await mountView()
    expect(w.findAll('.ar__item').length).toBeGreaterThan(0)
    expect(w.text()).toContain('Alice')
    expect(w.text()).toContain('high-cost')
  })
})

describe('★★★★★★★★ 只回启用中的行', () => {
  it('★★★★★★★★ 明说停用的审批人/规则不在这儿，且口径与配置页不同', async () => {
    const w = await mountView()
    const warns = w.findAll('.ar__note--warn').map((n) => n.text())
    const only = warns.find((x) => x.includes('停用'))
    expect(only).toBeDefined()
    expect(only).toContain('不在这儿')
    expect(only).toContain('不是数据错')
  })
})

describe('★★★ 优先级方向相反', () => {
  it('★★★ 审批人升序、规则降序，两处都说明白', async () => {
    const w = await mountView()
    const notes = w.findAll('.ar__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('从小到大'))).toBe(true)
    expect(notes.some((x) => x.includes('从大到小'))).toBe(true)
    expect(notes.some((x) => x.includes('方向相反'))).toBe(true)
  })
})

describe('★★★ omitempty 造成的键缺失', () => {
  it('★★★ ★ 邮箱/手机键不存在 ⇒ 显示「未填」，不留空白', async () => {
    // ★ 造 Go 实际吐出的形状：COALESCE(...,'') 再被 omitempty 省掉
    const bare = JSON.parse(JSON.stringify(approver())) as Record<string, unknown>
    expect('email' in bare).toBe(false)
    const w = await mountView({ approvers: { approvers: [bare as unknown as Approver], count: 1 } })
    const metas = w.findAll('.ar__meta').map((n) => n.text())
    expect(metas.find((x) => x.includes('邮箱'))).toContain('未填')
    expect(metas.find((x) => x.includes('手机'))).toContain('未填')
  })

  it('★★★ 有邮箱时显示真实值', async () => {
    const w = await mountView({
      approvers: { approvers: [approver({ email: 'a@x.test', phone: '13800000000' })], count: 1 },
    })
    const metas = w.findAll('.ar__meta').map((n) => n.text())
    expect(metas.find((x) => x.includes('邮箱'))).toContain('a@x.test')
    expect(metas.find((x) => x.includes('手机'))).toContain('13800000000')
  })
})

describe('★★ 规则条件为 null', () => {
  it('★★ 明说「一个条件都没有」，不是「条件未知」', async () => {
    const w = await mountView({ rules: { rules: [rule({ conditions: null })], count: 1 } })
    const warns = w.findAll('.ar__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('一个条件都没有'))).toBe(true)
  })

  it('★★ 有条件时列出条件', async () => {
    const w = await mountView()
    expect(w.text()).toContain('cost gt 100')
  })
})

describe('★★ 抛错不许退化成空清单', () => {
  it('★★ approvers 失败 ⇒ 错误态，且**不**显示「没有启用中的审批人」', async () => {
    const w = await mountView({ approvers: new Error('boom') })
    expect(w.findAll('.ar__msg--err').length).toBeGreaterThan(0)
    // ★★★ 判据要够狠：退化成 null 时这一句也会出现，两种形态**长得一样**
    expect(w.text()).not.toContain('没有启用中的审批人')
    expect(w.findAll('.ar__badge')).toHaveLength(0)
  })

  it('★★ rules 失败 ⇒ 同样不许显示「没有启用中的规则」', async () => {
    const w = await mountView({ rules: new Error('boom') })
    expect(w.text()).not.toContain('没有启用中的规则')
  })

  it('★★ 抛错后**必须**重新请求成功才恢复（先失败 → 再成功）', async () => {
    setApp(appMock, new Error('boom'))
    setRule(ruleMock, { rules: [rule()], count: 1 })
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ApprovalRulesView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    expect(w.text()).not.toContain('Alice')

    setApp(appMock, { approvers: [approver()], count: 1 })
    await w.find('#ar-code').setValue('globex')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('Alice')
  })
})

describe('★★ 404 两种来源', () => {
  it('★★ 裸文本 404 ⇒ 说「整族可能没注册」', async () => {
    const w = await mountView({ approvers: new Error('404 page not found') })
    const warns = w.findAll('.ar__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('没注册'))).toBe(true)
  })

  it('★ `unknown sub-resource` ⇒ 说「多半打到了复数路径」', async () => {
    const w = await mountView({ rules: new Error('unknown sub-resource: approvers') })
    const warns = w.findAll('.ar__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('复数'))).toBe(true)
  })
})

describe('★★ 只读边界', () => {
  it('★★ 明说本页只读', async () => {
    const w = await mountView()
    expect(w.findAll('.ar__note').some((n) => n.text().includes('只读'))).toBe(true)
  })

  it('★ 没填租户码 ⇒ 不发请求', async () => {
    const w = await mountView({ query: {} })
    expect(appMock).not.toHaveBeenCalled()
    expect(w.findAll('.ar__item')).toHaveLength(0)
  })
})