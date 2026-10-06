import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ModelPoliciesView from './ModelPoliciesView.vue'
import {
  fetchTenantModelPolicies,
  checkTenantModelPolicy,
  type TenantModelPolicyList,
  type TenantModelPolicyCheck,
  type TenantModelPolicy,
} from '@/api/modelPolicies'
import { setLocale, locale } from '@/i18n'

/**
 * ModelPoliciesView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 抽屉席**必须** super_admin —— 整棵 model-policies 子树都是
 *    `h.superAdmin` 硬门槛（后端文件头把 check 标成 `(admin)` 是**过时的**）。
 * 2. ★★★★★★ check **不做租户隔离**：后端 SQL 里只有 canonical_name，
 *    tenantCode 一个字没用 ⇒ 页面必须说破「查的是全局名录」。
 * 3. ★★★★ `vendor` 后端**永不提供** ⇒ 厂商格必须显式说明，不许留空白。
 * 4. ★★★★ `exists:false` 有多种成因 ⇒ 不许断言「拼错了」。
 * 5. ★★★★ `count` 是派生值 ⇒ 不许当全库条数。
 * 6. ★★★★ `include_deleted` 只认字面 `"true"` ⇒ 切分段必须**重新取数**。
 * 7. ★★ 抛错不许退化成空名单。
 */

const routeMock: { value: { query: Record<string, string> } } = { value: { query: { tenant: 'acme' } } }
vi.mock('vue-router', () => ({ useRoute: () => routeMock.value }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/modelPolicies', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modelPolicies')>()
  return {
    ...actual,
    fetchTenantModelPolicies: vi.fn(),
    checkTenantModelPolicy: vi.fn(),
  }
})

const listMock = fetchTenantModelPolicies as unknown as ReturnType<typeof vi.fn>
const checkMock = checkTenantModelPolicy as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** 抄自 `TenantModelPolicyWire`（model_policies.go:44-54）。 */
function policy(over: Record<string, unknown> = {}): TenantModelPolicy {
  return {
    id: 7,
    tenant_id: 'acme',
    canonical_name: 'gpt-4o',
    reason: 'denylist for cost',
    created_by: 'root',
    created_at: '2026-05-01T10:00:00Z',
    updated_at: '2026-05-01T10:00:00Z',
    ...over,
  } as TenantModelPolicy
}

function policyList(policies: TenantModelPolicy[] = [policy()]): TenantModelPolicyList {
  return { policies, count: policies.length, tenant: 'acme' }
}

function checkFound(): TenantModelPolicyCheck {
  return { exists: true, canonical_name: 'gpt-4o', family: 'gpt', modality: 'text' }
}

function checkMissing(): TenantModelPolicyCheck {
  return { exists: false, canonical_name: 'nope-9000', modality: 'text' }
}

/**
 * ★ helper 必须**接受覆盖参数**：默认走 `??`，并按 `instanceof Error` 分派 reject。
 *   否则「先设 reject 再 mountView」会被 helper 的默认 `mockResolvedValue` 吃掉 ——
 *   测试照样绿，而它压根没测到错误路径。
 */
function setList(m: typeof listMock, v: unknown, dft: unknown = policyList()) {
  if (v instanceof Error) m.mockRejectedValue(v)
  else m.mockResolvedValue(v ?? dft)
}

function setCheck(m: typeof checkMock, v: unknown, dft: unknown = checkFound()) {
  if (v instanceof Error) m.mockRejectedValue(v)
  else m.mockResolvedValue(v ?? dft)
}

async function mountView(
  opts: { list?: unknown; query?: Record<string, string> } = {},
): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  routeMock.value = { query: opts.query ?? { tenant: 'acme' } }
  setList(listMock, opts.list)
  setCheck(checkMock, undefined)
  const w = mount(ModelPoliciesView, { attachTo: document.body, global: { plugins: [pinia] } })
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

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 抽屉席必须是 super_admin', () => {
  it('★★★★★★ model-policies 席设了 requiresRole', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'model-policies')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBe('super_admin')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ check 不做租户隔离，必须说破', () => {
  it('★★★★★★ 明说这一栏查的是**全局**名录、与租户无关', async () => {
    const w = await mountView()
    const warns = w.findAll('.mp__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('全局') && x.includes('无关'))).toBe(true)
  })

  it('★★★★★★ ★ 不许把 check 描述成「本租户可用的模型」', async () => {
    const w = await mountView()
    // ★ 按**节点**判读，不能整页 not.toContain —— 页面上多处文案都可能撞词
    const panel = w.findAll('.mp__panel').find((p) => p.text().includes('模型名校验'))
    expect(panel).toBeDefined()
    expect(panel!.text()).not.toContain('本租户可用')
  })

  it('★★★★★ check 拿到租户码（虽然后端不用它）', async () => {
    const w = await mountView()
    await w.find('#mp-check-name').setValue('gpt-4o')
    await w.find('.mp__btn').trigger('click')
    await flushPromises()
    expect(checkMock).toHaveBeenCalledWith('acme', 'gpt-4o')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ `vendor` 后端永不提供 ⇒ 不许留空白', () => {
  it('★★★★★★ 厂商格必须显式说明「后端不提供」', async () => {
    const w = await mountView()
    await w.find('#mp-check-name').setValue('gpt-4o')
    await w.find('.mp__btn').trigger('click')
    await flushPromises()
    const cells = w.findAll('.mp__cell').map((c) => c.text())
    const vendor = cells.find((c) => c.includes('厂商'))
    expect(vendor).toBeDefined()
    // ★★ 空白格 = 信息丢失；必须显式说明
    expect(vendor!).toContain('后端不提供')
    expect(vendor).not.toBe('厂商')
  })

  it('★★★★★ 命中形态（有 family）也照样说明厂商不可用', async () => {
    setCheck(checkMock, checkFound())
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ModelPoliciesView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await w.find('#mp-check-name').setValue('gpt-4o')
    await w.find('.mp__btn').trigger('click')
    await flushPromises()
    const cells = w.findAll('.mp__cell').map((c) => c.text())
    expect(cells.find((c) => c.includes('厂商'))).toContain('后端不提供')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ `exists:false` 有多种成因，不许断言「拼错了」', () => {
  it('★★★★★★ 未登记 ⇒ 出警告且列出多种成因', async () => {
    setCheck(checkMock, checkMissing())
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ModelPoliciesView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await w.find('#mp-check-name').setValue('nope-9000')
    await w.find('.mp__btn').trigger('click')
    await flushPromises()
    const warns = w.findAll('.mp__note--warn').map((n) => n.text())
    const unknown = warns.find((x) => x.includes('下线'))
    expect(unknown).toBeDefined()
    expect(unknown).toContain('已下线')
  })

  it('★★★★★ 未命中时 `modality` 仍渲染成 `text`（后端初值，不是「没有模态」）', async () => {
    setCheck(checkMock, checkMissing())
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ModelPoliciesView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await w.find('#mp-check-name').setValue('nope-9000')
    await w.find('.mp__btn').trigger('click')
    await flushPromises()
    const cells = w.findAll('.mp__cell').map((c) => c.text())
    expect(cells.find((c) => c.includes('模态'))).toContain('text')
  })

  it('★★★★★ `family` 键缺失 ⇒ 明说「响应里没有这一项」，不渲染空白', async () => {
    // ★ 造 Go 实际吐出的形状：`family` 键**整个不存在**（不是 null）
    const noFamily = JSON.parse(JSON.stringify(checkFound())) as Record<string, unknown>
    delete noFamily.family
    setCheck(checkMock, noFamily)
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ModelPoliciesView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await w.find('#mp-check-name').setValue('gpt-4o')
    await w.find('.mp__btn').trigger('click')
    await flushPromises()
    const cells = w.findAll('.mp__cell').map((c) => c.text())
    expect(cells.find((c) => c.includes('模型族'))).toContain('没有这一项')
  })

  it('★★★★★ check 抛错 ⇒ 显示错误，**不**清成「未登记」', async () => {
    setCheck(checkMock, new Error('canonical_name is required'))
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ModelPoliciesView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await w.find('#mp-check-name').setValue('gpt-4o')
    await w.find('.mp__btn').trigger('click')
    await flushPromises()
    expect(w.findAll('.mp__msg--err').length).toBeGreaterThan(0)
    // ★★ 「先成功 → 再失败」序列：失败后不许留下上一轮的结果
    expect(w.findAll('.mp__cell')).toHaveLength(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 软删除标记', () => {
  it('★★★★★★ 已软删 ⇒ 打「已软删」并显示删除人', async () => {
    const w = await mountView({
      list: policyList([policy({ deleted_at: '2026-06-01T08:00:00Z', deleted_by: 'root' })]),
    })
    expect(w.text()).toContain('已软删')
    expect(w.text()).toContain('删除人：root')
  })

  it('★★★★★ 删除人键缺失 ⇒ 明说「不详」，不编一个', async () => {
    const w = await mountView({ list: policyList([policy({ deleted_at: '2026-06-01T08:00:00Z' })]) })
    const notes = w.findAll('.mp__note--warn').map((n) => n.text())
    const by = notes.find((x) => x.includes('删除人'))
    expect(by).toBeDefined()
    expect(by).toContain('不详')
  })

  it('★★★★★ 未软删 ⇒ 打「生效中」，不出删除人行', async () => {
    const w = await mountView()
    const badge = w.findAll('.mp__badge-t').map((n) => n.text())
    expect(badge).toContain('生效中')
    expect(badge).not.toContain('已软删')
  })

  it('★★★★★ 软删计数只数真正带 `deleted_at` 的行', async () => {
    const w = await mountView({
      list: policyList([
        policy({ id: 1 }),
        policy({ id: 2, deleted_at: '2026-06-01T08:00:00Z', deleted_by: 'root' }),
      ]),
    })
    expect(w.text()).toContain('软删 1 条')
    expect(w.text()).toContain('显示 2 条')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ include_deleted 只认字面 "true"', () => {
  it('★★★★★★ 明说这一点（`1` / `TRUE` / `yes` 一律当「不含」）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('include_deleted=true')
  })

  it('★★★★★★ 切「含软删」⇒ **重新请求**并带上 include_deleted: true', async () => {
    const w = await mountView()
    await w.findAll('.mp__seg-btn')[1]!.trigger('click')
    await flushPromises()
    expect(listMock).toHaveBeenLastCalledWith('acme', { includeDeleted: true })
  })

  it('★★★★★ 切回「仅生效」⇒ 重新请求并带 includeDeleted: false', async () => {
    const w = await mountView()
    await w.findAll('.mp__seg-btn')[1]!.trigger('click')
    await flushPromises()
    await w.findAll('.mp__seg-btn')[0]!.trigger('click')
    await flushPromises()
    expect(listMock).toHaveBeenLastCalledWith('acme', { includeDeleted: false })
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ `count` 是派生值', () => {
  it('★★★★ 明说它不是全库条数、也证明不了没丢行', async () => {
    const w = await mountView()
    const notes = w.findAll('.mp__note').map((n) => n.text())
    const cnt = notes.find((x) => x.includes('派生值'))
    expect(cnt).toBeDefined()
    expect(cnt).toContain('全库条数')
    expect(cnt).toContain('恒等于条数')
    expect(cnt).toContain('证明不了')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 抛错不许退化成空名单', () => {
  it('★★ 404 `tenant not found` ⇒ 出错误 + 明说「不一定是不存在」', async () => {
    const w = await mountView({ list: new Error('tenant not found') })
    expect(w.findAll('.mp__msg--err').length).toBeGreaterThan(0)
    const warns = w.findAll('.mp__note--warn').map((n) => n.text())
    // ★★★ 这条 404 也可能是「查库失败」⇒ 文案不许断言「租户不存在」
    expect(warns.some((x) => x.includes('不一定'))).toBe(true)
    expect(w.findAll('.mp__item')).toHaveLength(0)
    // ★★★ 光断言 `.mp__item` 为 0 是**无牙**的：把 catch 改成
    //   `list.value = {policies: [], count: 0, tenant}` 时它照样是 0。
    //   真正区分「退化成空名单」与「正确地不渲染列表」的是**列表面板整个不出现**
    //   （`v-if="list"`），而它带的那格计数徽标 `.mp__badge` 就是标志。
    expect(w.findAll('.mp__badge')).toHaveLength(0)
    expect(w.text()).not.toContain('显示 0 条')
  })

  it('★★ 503 ⇒ 明说「没有配置数据库」', async () => {
    const w = await mountView({ list: new Error('database not configured') })
    expect(w.text()).toContain('没有配置数据库')
    // ★ 同上：错误态下不许渲染出列表面板（否则错误被伪装成「没有策略」）
    expect(w.findAll('.mp__badge')).toHaveLength(0)
  })

  it('★★ 抛错后**必须**重新请求成功才恢复（先失败 → 再成功）', async () => {
    setList(listMock, new Error('tenant not found'), policyList())
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ModelPoliciesView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    expect(w.findAll('.mp__item')).toHaveLength(0)

    setList(listMock, policyList())
    await w.findAll('.mp__seg-btn')[1]!.trigger('click')
    await flushPromises()
    expect(w.findAll('.mp__item')).toHaveLength(1)
  })

  it('★★ 没填租户码 ⇒ 不发请求、不渲染空名单', async () => {
    const w = await mountView({ query: {} })
    expect(listMock).not.toHaveBeenCalled()
    expect(w.findAll('.mp__item')).toHaveLength(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 只读边界', () => {
  it('★★ 明说本页只读，且不碰四个写操作', async () => {
    const w = await mountView()
    const notes = w.findAll('.mp__note--warn').map((n) => n.text())
    const ro = notes.find((x) => x.includes('只读'))
    expect(ro).toBeDefined()
    expect(ro).toContain('创建')
    expect(ro).toContain('软删')
  })

  it('★★ 页面上没有任何写按钮（只有分段控件与校验按钮）', async () => {
    const w = await mountView()
    // ★ 校验是 POST 但**只读**（后端只有一条 SELECT），可以留
    expect(w.findAll('.mp__seg-btn')).toHaveLength(2)
    expect(w.findAll('.mp__btn')).toHaveLength(1)
  })
})