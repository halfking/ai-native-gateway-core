import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ModelPolicyAuditView from './ModelPolicyAuditView.vue'
import {
  fetchTenantModelPolicyAudit,
  type TenantModelPolicyAudit,
  type TenantModelPolicyAuditRow,
} from '@/api/modelPolicies'
import { setLocale, locale } from '@/i18n'

/**
 * ModelPolicyAuditView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 抽屉席**必须** super_admin（整棵子树都是 h.superAdmin）。
 * 2. ★★★★★★ 空列表**不能**当成「没有变更过」：租户码拼错也是 200 + 空。
 * 3. ★★★★★ 没有「更早一页」（`ORDER BY ts DESC LIMIT n`，无 OFFSET 无游标）
 *    ⇒ 页面刻意不放翻页控件。
 * 4. ★★★★ limit 越界**回落 100**，不是 clamp 到 500 ⇒ 页面要显示**实际生效**的值。
 * 5. ★★★ `actor === 'system'` 是**没设 GUC** 的兜底，不是账号名。
 * 6. ★★★ 动作第一个是 `insert` 不是 `create`（CHECK 约束闭合集合）。
 */

const routeMock: { value: { query: Record<string, string> } } = { value: { query: { tenant: 'acme' } } }
vi.mock('vue-router', () => ({ useRoute: () => routeMock.value }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/modelPolicies', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modelPolicies')>()
  return { ...actual, fetchTenantModelPolicyAudit: vi.fn() }
})

const auditMock = fetchTenantModelPolicyAudit as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** 抄自 `auditRow`（model_policies.go:607-616）。 */
function auditRow(over: Record<string, unknown> = {}): TenantModelPolicyAuditRow {
  return {
    id: 101,
    ts: '2026-06-01T08:00:00Z',
    action: 'insert',
    policy_id: 7,
    tenant_id: 'acme',
    canonical_name: 'gpt-4o',
    reason: 'denylist for cost',
    actor: 'root',
    ...over,
  } as TenantModelPolicyAuditRow
}

function auditResp(rows: TenantModelPolicyAuditRow[] = [auditRow()]): TenantModelPolicyAudit {
  return { audit: rows, count: rows.length }
}

/**
 * ★ helper 必须**接受覆盖参数**：默认走 `??`，并按 `instanceof Error` 分派 reject。
 *   否则「先设 reject 再 mountView」会被 helper 的默认 `mockResolvedValue` 吃掉。
 */
function setAudit(m: typeof auditMock, v: unknown, dft: unknown = auditResp()) {
  if (v instanceof Error) m.mockRejectedValue(v)
  else m.mockResolvedValue(v ?? dft)
}

async function mountView(
  opts: { audit?: unknown; query?: Record<string, string> } = {},
): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  routeMock.value = { query: opts.query ?? { tenant: 'acme' } }
  setAudit(auditMock, opts.audit)
  const w = mount(ModelPolicyAuditView, { attachTo: document.body, global: { plugins: [pinia] } })
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
  it('★★★★★★ model-policy-audit 席设了 requiresRole', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'model-policy-audit')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBe('super_admin')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 没有「更早一页」', () => {
  it('★★★★★★ 明说无 OFFSET 无游标、拿不到更早的', async () => {
    const w = await mountView()
    const warns = w.findAll('.mpa__note--warn').map((n) => n.text())
    const older = warns.find((x) => x.includes('更早'))
    expect(older).toBeDefined()
    expect(older).toContain('OFFSET')
  })

  it('★★★★★★ ★ 页面上**没有**任何翻页控件', async () => {
    const w = await mountView()
    // ★ 只有租户码输入框 + 三档 limit 分段；不许出现「上一页/下一页」
    expect(w.findAll('.mpa__seg-btn')).toHaveLength(3)
    const text = w.text()
    for (const label of ['上一页', '下一页', '加载更多', '更早', '上一页']) {
      if (label === '更早') continue // 出现在「没有更早一页」的说明里
      expect(text).not.toContain(label)
    }
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 空列表不能当成「没有变更过」', () => {
  it('★★★★★★ 空审计 ⇒ 必须出「不能当成没变更过」的警告', async () => {
    const w = await mountView({ audit: auditResp([]) })
    const warns = w.findAll('.mpa__note--warn').map((n) => n.text())
    const amb = warns.find((x) => x.includes('空列表'))
    expect(amb).toBeDefined()
    expect(amb).toContain('租户码拼错')
  })

  it('★★★★★★ ★ 「没有变更过」只允许出现在**免责警告**里，不许被当成结论', async () => {
    const w = await mountView({ audit: auditResp([]) })
    // ★★★ 不能写整页 `not.toContain('没有变更过')`：那条免责说明**本身**就引述了
    //   这几个字（`空列表**不能**当成「这个租户没有变更过」`）⇒ 会被自己判红。
    //   正确做法是**按节点作用域**：凡提到它的节点，必须全部是 `.mpa__note--warn`。
    const mentions = w.findAll('.mpa__msg, .mpa__meta, .mpa__title, .mpa__note').filter((n) =>
      n.text().includes('没有变更过'),
    )
    expect(mentions.length).toBeGreaterThan(0)
    for (const n of mentions) {
      expect(n.classes()).toContain('mpa__note--warn')
    }
    // ★ 而且普通提示语（.mpa__msg）里**一个都不许有**
    expect(w.findAll('.mpa__msg').filter((n) => n.text().includes('没有变更过'))).toHaveLength(0)
  })

  it('★★★★★ 空审计是正常 200，**不是**错误态', async () => {
    const w = await mountView({ audit: auditResp([]) })
    expect(w.findAll('.mpa__msg--err')).toHaveLength(0)
    expect(w.findAll('.mpa__item')).toHaveLength(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ limit 越界回落 100，不是 clamp', () => {
  it('★★★★★ 合法档位 ⇒ 显示后端实际按 N 条返回', async () => {
    const w = await mountView()
    const notes = w.findAll('.mpa__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('实际按 100 条'))).toBe(true)
  })

  it('★★★★★ 切档位 ⇒ **重新请求**并带新的 limit', async () => {
    const w = await mountView()
    await w.findAll('.mpa__seg-btn')[2]!.trigger('click') // 500
    await flushPromises()
    expect(auditMock).toHaveBeenLastCalledWith('acme', { limit: 500 })
    const notes = w.findAll('.mpa__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('实际按 500 条'))).toBe(true)
  })

  it('★★★★★ 页面只提供 1~500 之内的档位（不给会被回落的选择）', async () => {
    const w = await mountView()
    const labels = w.findAll('.mpa__seg-btn').map((b) => b.text())
    expect(labels).toEqual(['25', '100', '500'])
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 触发器写入的三条事实必须说破', () => {
  it('★★★★★★ 明说 delete 记的是删除前的理由、改成同值不留痕', async () => {
    const w = await mountView()
    const notes = w.findAll('.mpa__note').map((n) => n.text())
    const trig = notes.find((x) => x.includes('触发器'))
    expect(trig).toBeDefined()
    expect(trig).toContain('删除前')
    expect(trig).toContain('不会留下任何痕迹')
  })

  it('★★★★★★ ★ 不许把前后不一致说成数据错', async () => {
    const w = await mountView()
    const trig = w.findAll('.mpa__note')
      .map((n) => n.text())
      .find((x) => x.includes('触发器'))!
    expect(trig).toContain('这是设计不是数据错')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ `actor === "system"` 是无署名，不是账号', () => {
  it('★★★★★★ 渲染成「无署名」而不是把 system 当人名', async () => {
    const w = await mountView({ audit: auditResp([auditRow({ actor: 'system' })]) })
    const metas = w.findAll('.mpa__meta').map((n) => n.text())
    const actor = metas.find((x) => x.includes('操作者'))
    expect(actor).toBeDefined()
    expect(actor).toContain('无署名')
  })

  it('★★★★★ 正常 actor 原样显示', async () => {
    const w = await mountView({ audit: auditResp([auditRow({ actor: 'alice' })]) })
    const metas = w.findAll('.mpa__meta').map((n) => n.text())
    expect(metas.some((x) => x.includes('操作者') && x.includes('alice'))).toBe(true)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 动作集合是闭合的，第一个是 insert', () => {
  it('★★★★ 四种动作都渲染得出来', async () => {
    const w = await mountView({
      audit: auditResp([
        auditRow({ id: 1, action: 'insert' }),
        auditRow({ id: 2, action: 'update' }),
        auditRow({ id: 3, action: 'delete' }),
        auditRow({ id: 4, action: 'undelete' }),
      ]),
    })
    const titles = w.findAll('.mpa__title').map((n) => n.text())
    expect(titles).toEqual(['insert', 'update', 'delete', 'undelete'])
  })

  it('★★★★★ 不把 `insert` 翻成 `create`（后端原样就是 insert）', async () => {
    const w = await mountView({ audit: auditResp([auditRow({ action: 'insert' })]) })
    const title = w.find('.mpa__title').text()
    expect(title).toBe('insert')
    expect(title).not.toBe('create')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ policy_id 键缺失', () => {
  it('★★★★ ★ 键整个不存在 ⇒ 明说「未挂到具体策略」', async () => {
    // ★ 造 Go 实际吐出的形状：omitempty ⇒ 键不存在（不是 null）
    const orphan = JSON.parse(JSON.stringify(auditRow())) as Record<string, unknown>
    delete orphan.policy_id
    const w = await mountView({ audit: auditResp([orphan as unknown as TenantModelPolicyAuditRow]) })
    const metas = w.findAll('.mpa__meta').map((n) => n.text())
    const ref = metas.find((x) => x.includes('关联策略'))
    expect(ref).toBeDefined()
    expect(ref).toContain('未挂到具体策略')
  })

  it('★★★★ 有 policy_id ⇒ 显示 #编号', async () => {
    const w = await mountView({ audit: auditResp([auditRow({ policy_id: 42 })]) })
    const metas = w.findAll('.mpa__meta').map((n) => n.text())
    expect(metas.find((x) => x.includes('关联策略'))).toContain('#42')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 抛错不许退化成空审计', () => {
  it('★★ 503 ⇒ 明说「没有配置数据库」且不渲染空审计', async () => {
    const w = await mountView({ audit: new Error('database not configured') })
    expect(w.findAll('.mpa__msg--err').length).toBeGreaterThan(0)
    expect(w.text()).toContain('没有配置数据库')
    // ★★ 不许落进「空列表 ⇒ 不能当成没变更过」那条分支，那会把错误伪装成空结果
    expect(w.text()).not.toContain('空列表')
    // ★★★ 而且 `.mpa__item` 为 0 本身**无牙**（退化成空审计时也是 0），
    //   真正区分的是列表面板整个不出现 —— 它的计数徽标 `.mpa__badge` 是标志。
    expect(w.findAll('.mpa__badge')).toHaveLength(0)
  })

  it('★★ 抛错后**必须**重新请求成功才恢复（先失败 → 再成功）', async () => {
    setAudit(auditMock, new Error('database not configured'), auditResp())
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ModelPolicyAuditView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    expect(w.findAll('.mpa__item')).toHaveLength(0)

    setAudit(auditMock, auditResp())
    await w.findAll('.mpa__seg-btn')[0]!.trigger('click')
    await flushPromises()
    expect(w.findAll('.mpa__item')).toHaveLength(1)
  })

  it('★★ 没填租户码 ⇒ 不发请求', async () => {
    const w = await mountView({ query: {} })
    expect(auditMock).not.toHaveBeenCalled()
    expect(w.findAll('.mpa__item')).toHaveLength(0)
  })
})