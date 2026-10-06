import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ApprovalConfigView from './ApprovalConfigView.vue'
import {
  fetchApprovalConfig,
  fetchApprovalConfigStats,
  type ApprovalConfig,
  type ApprovalConfigStats,
} from '@/api/approvalConfig'
import { setLocale, locale } from '@/i18n'

/**
 * ApprovalConfigView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 「没有配置」**不是错误**：后端返回**合成默认配置**，
 *    其中 timeout_seconds=3600 与 auto_reject=true 是**凭空造的**。
 * 2. ★★★★★ stats 是 config 的**纯函数** ⇒ 页面独立复算并核对。
 * 3. ★★★★ 两个端点都失败时**不许**渲染半个页面。
 * 4. ★★★ 404 是**裸文本**（整族未注册）⇒ 要与「路径打错」区分开。
 * 5. ★★ 抽屉席**必须不设** `requiresRole`（admin 档，tenant_admin 可用）。
 */

const routeMock: { value: { query: Record<string, string> } } = { value: { query: { tenant: 'acme' } } }
vi.mock('vue-router', () => ({ useRoute: () => routeMock.value }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/approvalConfig', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/approvalConfig')>()
  return { ...actual, fetchApprovalConfig: vi.fn(), fetchApprovalConfigStats: vi.fn() }
})

const cfgMock = fetchApprovalConfig as unknown as ReturnType<typeof vi.fn>
const statMock = fetchApprovalConfigStats as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function config(over: Record<string, unknown> = {}): ApprovalConfig {
  return {
    tenant_id: 'acme',
    enabled: true,
    mode: 'manual',
    approvers: [{ user_id: 'u1', name: 'A', role: 'admin', priority: 1, enabled: true }],
    channels: [{ type: 'feishu', config: { app_id: 'x' }, enabled: true }],
    timeout_seconds: 900,
    auto_reject_on_timeout: false,
    rules: [
      {
        name: 'r1',
        enabled: true,
        priority: 5,
        conditions: [{ field: 'cost', operator: 'gt', value: '1' }],
        action: { type: 'require_approval', risk_level: 'HIGH', reason: 'why' },
      },
    ],
    created_at: '2026-03-01T08:00:00Z',
    updated_at: '2026-09-01T08:00:00Z',
    ...over,
  } as ApprovalConfig
}

function stats(over: Record<string, unknown> = {}): ApprovalConfigStats {
  return {
    tenant_id: 'acme',
    enabled: true,
    mode: 'manual',
    approver_count: 1,
    enabled_approvers: 1,
    rule_count: 1,
    enabled_rules: 1,
    channel_count: 1,
    enabled_channels: 1,
    timeout_seconds: 900,
    last_updated: '2026-09-01T08:00:00Z',
    ...over,
  } as ApprovalConfigStats
}

/** ★ 后端对无配置行的租户返回的**合成默认**。 */
function synthConfig(): ApprovalConfig {
  return {
    tenant_id: 'acme',
    enabled: false,
    mode: 'disabled',
    approvers: [],
    channels: [],
    timeout_seconds: 3600,
    auto_reject_on_timeout: true,
    rules: [],
    created_at: '0001-01-01T00:00:00Z',
    updated_at: '0001-01-01T00:00:00Z',
  } as ApprovalConfig
}

/** ★ helper 接受覆盖 + 按 instanceof Error 分派，否则「先设 reject」会被默认值吃掉。 */
function setCfg(m: typeof cfgMock, v: unknown, dft: unknown = config()) {
  if (v instanceof Error) m.mockRejectedValue(v)
  else m.mockResolvedValue(v ?? dft)
}
function setStat(m: typeof statMock, v: unknown, dft: unknown = stats()) {
  if (v instanceof Error) m.mockRejectedValue(v)
  else m.mockResolvedValue(v ?? dft)
}

async function mountView(
  opts: { cfg?: unknown; stat?: unknown; query?: Record<string, string> } = {},
): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  routeMock.value = { query: opts.query ?? { tenant: 'acme' } }
  setCfg(cfgMock, opts.cfg)
  setStat(statMock, opts.stat)
  const w = mount(ApprovalConfigView, { attachTo: document.body, global: { plugins: [pinia] } })
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
  it('★★★★★★ approval-config 席没有 requiresRole', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'approval-config')
    expect(seat).toBeDefined()
    // ★ 与 model-policies 那族**相反**：这一族走 wrapAdmin，tenant_admin 可用
    expect(seat?.requiresRole).toBeUndefined()
  })
})

describe('★★★★★★★★ 从未配置过 ⇒ 合成默认必须被标出来', () => {
  it('★★★★★★★★ 出现「合成默认」面板，并说破 3600 不是查到的', async () => {
    const w = await mountView({
      cfg: synthConfig(),
      stat: stats({
        enabled: false,
        mode: 'disabled',
        approver_count: 0,
        enabled_approvers: 0,
        rule_count: 0,
        enabled_rules: 0,
        channel_count: 0,
        enabled_channels: 0,
        timeout_seconds: 3600,
        last_updated: '0001-01-01T00:00:00Z',
      }),
    })
    expect(w.text()).toContain('合成')
    expect(w.text()).toContain('不是')
    expect(w.findAll('.acfg__tag').length).toBeGreaterThan(0)
  })

  it('★★★★★★ 真配置里恰好也是 3600 ⇒ **不能**误标成合成', async () => {
    const w = await mountView({ cfg: config({ timeout_seconds: 3600 }) })
    expect(w.findAll('.acfg__tag')).toHaveLength(0)
  })

  it('★★★★★ 真配置时不出那个面板', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('这看起来是合成出来的默认配置')
  })
})

describe('★★★★★★★★ stats 是 config 的纯函数 ⇒ 页面独立复算', () => {
  it('★★★★★★★★ 一致 ⇒ 说「对得上」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('对得上')
    expect(w.text()).toContain('独立复算')
  })

  it('★★★★★★★★ 不一致 ⇒ 说「对不上」并当作异常', async () => {
    const w = await mountView({ stat: stats({ approver_count: 99 }) })
    const notes = w.findAll('.acfg__note--warn').map((n) => n.text())
    expect(notes.some((x) => x.includes('对不上'))).toBe(true)
    expect(w.text()).toContain('异常')
  })

  it('★★★★★ 复算读的是 config 里的**数组**，不是后端给的数', async () => {
    const w = await mountView({
      cfg: config({
        approvers: [
          { user_id: 'u1', name: 'A', role: 'admin', priority: 1, enabled: true },
          { user_id: 'u2', name: 'B', role: 'admin', priority: 2, enabled: false },
        ],
      }),
      stat: stats({ approver_count: 2, enabled_approvers: 1 }),
    })
    expect(w.text()).toContain('审批人 2 个')
    expect(w.text()).toContain('对得上')
  })
})

describe('★★★★ 通知渠道（无独立端点，含停用的）', () => {
  it('★★★★ 渠道面板存在且列出 config.channels', async () => {
    const w = await mountView()
    // ★★ 不能写 `w.text()).toContain('通知渠道')`：那条说明文案
    //   `channelsNote` 里就带着「通知渠道」四个字 ⇒ 恒真，面板标题删了也不红。
    //   必须按**节点**判：面板标题元素本身。
    const titles = w.findAll('.acfg__panel-title').map((n) => n.text())
    expect(titles).toContain('通知渠道')
    expect(w.findAll('.acfg__row').length).toBeGreaterThan(0)
    expect(w.text()).toContain('feishu')
    expect(w.text()).toContain('app_id=x')
  })

  it('★★★★ ★ 明说渠道**没有独立端点**且**包含停用的**（与审批人/规则相反）', async () => {
    const w = await mountView()
    const notes = w.findAll('.acfg__note').map((n) => n.text())
    const ch = notes.find((x) => x.includes('没有独立端点'))
    expect(ch).toBeDefined()
    expect(ch).toContain('包含停用')
  })

  it('★★★ 停用的渠道**也在**这个列表里（与 approvers/rules 不同源）', async () => {
    const w = await mountView({
      cfg: config({ channels: [{ type: 'email', config: null, enabled: false }] }),
    })
    const rows = w.findAll('.acfg__row')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('email')
    expect(rows[0]!.text()).toContain('未启用')
  })

  it('★★★ `config` 为 null（nil map）⇒ 明说「没有配置项」', async () => {
    const w = await mountView({
      cfg: config({ channels: [{ type: 'email', config: null, enabled: true }] }),
    })
    expect(w.findAll('.acfg__row')[0]!.text()).toContain('没有配置项')
  })

  it('★★★★ `channels` 为 null ⇒ 渲染成「没有配置任何通知渠道」', async () => {
    const w = await mountView({ cfg: config({ channels: null }) })
    expect(w.text()).toContain('没有配置任何通知渠道')
    expect(w.findAll('.acfg__msg--err')).toHaveLength(0)
  })
})

describe('★★★ 两个端点都必须成功，不许渲染半个页面', () => {
  it('★★★ config 失败 ⇒ 错误态且不渲染配置面板', async () => {
    const w = await mountView({ cfg: new Error('boom') })
    expect(w.findAll('.acfg__msg--err').length).toBeGreaterThan(0)
    expect(w.text()).not.toContain('模式')
  })

  it('★★★ stats 失败 ⇒ 同样整个页面不渲染（不许只缺统计那半）', async () => {
    const w = await mountView({ stat: new Error('boom') })
    expect(w.findAll('.acfg__msg--err').length).toBeGreaterThan(0)
    // ★★ 半页面比整页报错更危险：看起来像「统计是 0」
    expect(w.text()).not.toContain('审批人（总数）')
  })

  it('★★★★★★ ★ 第二次取数失败 ⇒ **必须清空**上一轮的成功结果', async () => {
    // ★★★ 只测「首屏就失败」是**恒真**的：首屏本来就没有上一轮结果，
    //   把 catch 里的 `config.value = null` 整行删掉，首屏那次照样通过。
    //   必须造「先成功 → 再失败」。
    setCfg(cfgMock, config())
    setStat(statMock, stats())
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ApprovalConfigView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    expect(w.text()).toContain('模式')

    setStat(statMock, new Error('boom'))
    await w.find('#acfg-code').setValue('globex')
    await flushPromises()
    await flushPromises()
    // ★ 失败后不许把上一轮的配置留在屏幕上 —— 那会被读成「globex 也这样」
    expect(w.findAll('.acfg__msg--err').length).toBeGreaterThan(0)
    expect(w.text()).not.toContain('模式')
    expect(w.text()).not.toContain('审批人（总数）')
  })

  it('★★★ 抛错后**必须**重新请求成功才恢复（先失败 → 再成功）', async () => {
    setCfg(cfgMock, new Error('boom'), config())
    setStat(statMock, stats())
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(ApprovalConfigView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    expect(w.text()).not.toContain('模式')

    setCfg(cfgMock, config())
    await w.find('#acfg-code').setValue('globex')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('模式')
  })
})

describe('★★★ 404 的两种来源要分开说', () => {
  it('★★★ 裸文本 404 ⇒ 说「整族可能没注册」，不许说成「没有配置」', async () => {
    const w = await mountView({ cfg: new Error('404 page not found') })
    const warns = w.findAll('.acfg__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('没注册'))).toBe(true)
    expect(w.text()).not.toContain('这看起来是合成出来的默认配置')
  })

  it('★★ `unknown sub-resource` ⇒ 说「多半打到了复数路径」', async () => {
    const w = await mountView({ cfg: new Error('unknown sub-resource: approval-config') })
    const warns = w.findAll('.acfg__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('复数'))).toBe(true)
  })
})

describe('★★ 路径与只读边界', () => {
  it('★★ 明说接口挂在单数前缀下', async () => {
    const w = await mountView()
    expect(w.text()).toContain('tenant-approval-config')
  })

  it('★★ 明说本页只读', async () => {
    const w = await mountView()
    const notes = w.findAll('.acfg__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('只读'))).toBe(true)
  })

  it('★★ 没填租户码 ⇒ 不发请求', async () => {
    const w = await mountView({ query: {} })
    expect(cfgMock).not.toHaveBeenCalled()
    expect(w.text()).not.toContain('模式')
  })
})