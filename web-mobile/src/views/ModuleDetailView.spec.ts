import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import ModuleDetailView from './ModuleDetailView.vue'
import {
  fetchModule,
  fetchModuleConfig,
  type ModuleDetail,
  type ModuleWithStatus,
  type FeishuBotConfigSummary,
} from '@/api/modules'
import { setLocale, locale } from '@/i18n'

/**
 * ModuleDetailView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 配置键是**三**态：键缺 / 值为 null / 有值。三栏**分开列**，
 *     `value:null` **不许**被并进「键缺」。
 * 2. ★★★★★★ 跨端点自相矛盾必须**并列呈现**、不 reconcile：
 *     模块面失败回落 `true`，配置摘要失败回落 `false`。
 * 3. ★★★★★ 配置摘要两个**零值产物**：`allowed_user_count: 1`
 *     （`Split("", ",")` 的长度）、`quiet_hours_window: "–"`（两端空拼接）。
 * 4. ★★ `/config` 只有 feishu_bot 实现 ⇒ 其它模块**不打**那个请求。
 * 5. ★★ 501 是本仓首次出现的状态码，**单列**渲染。
 * 6. ★★ 抛错不许退化成「空详情」。
 * 7. ★★ 不碰 toggle（写）与 test（真给飞书发消息）。
 */

// ★★ 必须用 reactive：普通对象 Vue 追踪不到 params 变化，watch(key) 不触发
const routeMock = reactive({ params: { key: 'compression' as string } })
const routeRef = { get value() { return routeMock } }
vi.mock('vue-router', () => ({ useRoute: () => routeRef.value }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/modules', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modules')>()
  return { ...actual, fetchModule: vi.fn(), fetchModuleConfig: vi.fn() }
})

const detailMock = fetchModule as unknown as ReturnType<typeof vi.fn>
const summaryMock = fetchModuleConfig as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function mod(over: Record<string, unknown> = {}): ModuleWithStatus {
  return {
    key: 'compression',
    name: '会话压缩',
    description: '智能压缩超长对话上下文',
    capabilities: ['多模式压缩'],
    icon: '🗜️',
    category: 'compression',
    setting_key: 'compression.enabled',
    config_keys: ['compression.mode', 'compression.window_fraction', 'compression.gone'],
    docs_url: '/admin/compression',
    danger_level: 'warning',
    enabled: true,
    source: 'db',
    can_toggle_enabled: true,
    ...over,
  } as ModuleWithStatus
}

/** 抄自 `GET /api/admin/modules/{key}`（admin/modules.go:836-839）。 */
function detailOf(over: Record<string, unknown> = {}): ModuleDetail {
  return {
    module: mod(),
    config: {
      'compression.mode': { value: 'auto_threshold', source: 'db', spec: { key: 'compression.mode' } },
      'compression.window_fraction': { value: null, source: 'db', spec: { key: 'compression.window_fraction' } },
    },
    ...over,
  } as ModuleDetail
}

/** 抄自 `feishuBotConfigSummary`（admin/modules.go:1327-1357），20 键齐全。 */
function summaryOf(over: Record<string, unknown> = {}): FeishuBotConfigSummary {
  return {
    enabled: true,
    webhook_url_set: true,
    verify_token_set: false,
    encrypt_key_set: false,
    connection_mode: 'webhook',
    notify_on_alert: true,
    notify_on_approval: false,
    allowed_user_count: 3,
    alert_severity_min: 'warning',
    alert_rate_limit_min: 5,
    alert_dedup_window_sec: 60,
    quiet_hours_enabled: false,
    quiet_hours_window: '22:00–07:00',
    card_template: 'default',
    approval_expiry_min: 30,
    approval_mention_crit: true,
    commands_enabled: true,
    commands_admin_only: true,
    signature_required: false,
    timestamp_window_sec: 300,
    ...over,
  } as FeishuBotConfigSummary
}

function setMocks(opts: { detail?: unknown; summary?: unknown } = {}): void {
  const put = (m: ReturnType<typeof vi.fn>, v: unknown, dft: unknown) => {
    if (v instanceof Error) m.mockRejectedValue(v)
    else m.mockResolvedValue(v ?? dft)
  }
  put(detailMock, opts.detail, detailOf())
  put(summaryMock, opts.summary, summaryOf())
}

async function mountView(
  key = 'compression',
  opts: Parameters<typeof setMocks>[0] = {},
): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  routeMock.params = { key }
  setMocks(opts)
  const w = mount(ModuleDetailView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** ★★ 计数格：按「标签 → 值」成对取，避免靠文案猜。 */
function countCells(w: ReturnType<typeof mount>, label: string): string {
  const cell = w
    .findAll('.mdt__cell')
    .find((c) => c.find('.mdt__cell-l').text() === label)
  return cell?.find('.mdt__cell-v').text() ?? ''
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

describe('★★★★★★★★ 配置键是三态，三栏必须分开列', () => {
  // 夹具：3 个声明键 ⇒ 1 个有值、1 个值为 null、1 个键整个不在响应里
  it('★★★★★★★ 三态各归各栏，数字对得上', async () => {
    const w = await mountView()
    expect(countCells(w, '已取到值')).toBe('1')
    expect(countCells(w, '值为空')).toBe('1')
    expect(countCells(w, '没取到键')).toBe('1')
    expect(countCells(w, '声明总数')).toBe('3')
  })

  it('★★★★★★★ ★ `value:null` 的键**不**被算进「没取到键」', async () => {
    const w = await mountView()
    const lists = w.findAll('.mdt__list').map((n) => n.text())
    // 值为 null 的那个键只出现在「键在但值为空」栏
    expect(lists.some((x) => x.includes('compression.window_fraction') && x.includes('配了空值'))).toBe(true)
    // 且它**不**出现在「键整个不在响应里」栏
    const absentPanel = w
      .findAll('.mdt__sub')
      .filter((n) => n.text() === '键整个不在响应里')
    expect(absentPanel).toHaveLength(1)
    expect(absentPanel[0]!.element.nextElementSibling?.textContent ?? '').toContain('compression.gone')
    expect(absentPanel[0]!.element.nextElementSibling?.textContent ?? '').not.toContain('window_fraction')
  })

  it('★★★★★ 全部取到时「值为空」与「没取到键」都是 0', async () => {
    const w = await mountView(
      'compression',
      {
        detail: detailOf({
          module: mod({ config_keys: ['a', 'b'] }),
          config: { a: { value: 1, source: 'db', spec: {} }, b: { value: 'x', source: 'db', spec: {} } },
        }),
      },
    )
    expect(countCells(w, '已取到值')).toBe('2')
    expect(countCells(w, '值为空')).toBe('0')
    expect(countCells(w, '没取到键')).toBe('0')
  })

  it('★★★★★ 没有声明任何键时给出说明而不是空白', async () => {
    const w = await mountView('compression', { detail: detailOf({ module: mod({ config_keys: [] }), config: {} }) })
    expect(countCells(w, '声明总数')).toBe('0')
    expect(w.findAll('.mdt__note').map((n) => n.text()).some((x) => x.includes('没有声明任何配置键'))).toBe(true)
  })

  it('★★★★★ 页面上明说这是三态、且第②态不算「缺」', async () => {
    const w = await mountView()
    const warns = w.findAll('.mdt__note--warn').map((n) => n.text())
    // ★★ 断言串必须从**实际文案**里挑一段不含 markdown 星号的：
    //   文案是「有**三**种状态」，'三种状态' **不是**它的子串（星号把它切开了）。
    const note = warns.find((x) => x.includes('种状态'))
    expect(note).toBeDefined()
    expect(note).toContain('不是两种')
    expect(note).toContain('第 ② 栏不算')
  })
})

describe('★★★★★★★★ 跨端点自相矛盾：并列呈现、不 reconcile', () => {
  it('★★★★★★★ 模块面说启用、摘要说停用 ⇒ 必须出矛盾提示', async () => {
    const w = await mountView('feishu_bot', {
      detail: detailOf({ module: mod({ key: 'feishu_bot', enabled: true, source: 'default' }) }),
      summary: summaryOf({ enabled: false }),
    })
    await w.find('button.mdt__btn--summary').trigger('click')
    await flushPromises()
    await flushPromises()
    const warns = w.findAll('.mdt__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('相反答案'))).toBe(true)
  })

  it('★★★★★ 两处一致 ⇒ 不出矛盾提示', async () => {
    const w = await mountView('feishu_bot', {
      detail: detailOf({ module: mod({ key: 'feishu_bot', enabled: true, source: 'db' }) }),
      summary: summaryOf({ enabled: true }),
    })
    await w.find('button.mdt__btn--summary').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('相反答案'))).toBe(false)
  })
})

describe('★★★★★★★ 配置摘要的两个零值产物', () => {
  it('★★★★★★★ 白名单人数为 1 ⇒ 提示它可能是空串切分产物', async () => {
    const w = await mountView('feishu_bot', { summary: summaryOf({ allowed_user_count: 1 }) })
    await w.find('button.mdt__btn--summary').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('空串按逗号切开'))).toBe(true)
  })

  it('★★★★★★ 白名单人数为 3 ⇒ 不出那条提示（不能恒真）', async () => {
    const w = await mountView('feishu_bot', { summary: summaryOf({ allowed_user_count: 3 }) })
    await w.find('button.mdt__btn--summary').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('空串按逗号切开'))).toBe(false)
  })

  it('★★★★★★★ 静默时段为字面量 "–" ⇒ 渲染成「未设置」而不是把破折号当值', async () => {
    const w = await mountView('feishu_bot', { summary: summaryOf({ quiet_hours_window: '–' }) })
    await w.find('button.mdt__btn--summary').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(countCells(w, '静默时段')).toBe('（未设置）')
  })

  it('★★★★★ 真有静默时段 ⇒ 原样显示', async () => {
    const w = await mountView('feishu_bot', { summary: summaryOf({ quiet_hours_window: '22:00–07:00' }) })
    await w.find('button.mdt__btn--summary').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(countCells(w, '静默时段')).toBe('22:00–07:00')
  })
})

describe('★★★★★★ /config 只有 feishu_bot 实现', () => {
  it('★★★★★★ 非 feishu_bot ⇒ **不打**那个注定 501 的请求，且明说', async () => {
    const w = await mountView('compression')
    expect(summaryMock).not.toHaveBeenCalled()
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('只给 feishu_bot 实现'))).toBe(
      true,
    )
  })

  it('★★★★★ feishu_bot ⇒ 给出读取按钮', async () => {
    const w = await mountView('feishu_bot')
    expect(w.findAll('button.mdt__btn--summary')).toHaveLength(1)
    expect(w.findAll('button.mdt__btn--refresh')).toHaveLength(1)
    expect(summaryMock).not.toHaveBeenCalled()
  })

  it('★★★★★★★ 501 单列渲染，不混进「其他错误」', async () => {
    const w = await mountView('feishu_bot', {
      summary: new Error('config endpoint not implemented for module: feishu_bot'),
    })
    await w.find('button.mdt__btn--summary').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.find('.mdt__msg--err').text()).toContain('not implemented')
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('第一次出现 501'))).toBe(true)
  })
})

describe('★★★★★★ 抛错不许退化成空详情', () => {
  it('★★★★★★ 报错时**整个详情面板不出现**（用只存在于正确形态里的锚判）', async () => {
    const w = await mountView('compression', { detail: new Error('unknown module: nope') })
    // ★ 「配置键声明数为 0」证明不了「面板没渲染」—— 空态与错误态在数字维度一样
    expect(w.findAll('.mdt__grid')).toHaveLength(0)
    expect(w.findAll('.mdt__list')).toHaveLength(0)
    expect(w.find('.mdt__msg--err').text()).toContain('unknown module')
  })

  // ★ 首屏就失败证明不了「出错时必须清空」—— 必须造「先成功 → 再失败」
  it('★★★★★★★ 先成功、再失败 ⇒ 面板整体消失', async () => {
    const w = await mountView('compression')
    expect(w.findAll('.mdt__grid').length).toBeGreaterThan(0)
    expect(w.findAll('.mdt__list').length).toBeGreaterThan(0)

    detailMock.mockRejectedValue(new Error('later boom'))
    await w.find('button.mdt__btn--refresh').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(w.findAll('.mdt__grid')).toHaveLength(0)
    expect(w.findAll('.mdt__list')).toHaveLength(0)
    expect(w.find('.mdt__msg--err').text()).toContain('later boom')
  })
})

describe('★★★★★★ 503 的两种文案', () => {
  it('★★★★★★ `settings registry not initialised` ⇒ 说注册表没起', async () => {
    const w = await mountView('compression', { detail: new Error('settings registry not initialised') })
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('设置注册表没初始化'))).toBe(
      true,
    )
  })

  it('★★★★★ `settings not initialised` 也能识别', async () => {
    const w = await mountView('compression', { detail: new Error('settings not initialised') })
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('设置注册表没初始化'))).toBe(
      true,
    )
  })
})

describe('★★★★★★ 只读页与 module 自身的判读', () => {
  it('★★★★★★ 兜底态显示「没能读到配置」', async () => {
    const w = await mountView('compression', {
      detail: detailOf({ module: mod({ enabled: true, source: 'default' }) }),
    })
    expect(w.findAll('.mdt__badge-t').map((n) => n.text())).toContain('没能读到配置')
  })

  it('★★★★★ 真读到 db ⇒ 显示「启用」', async () => {
    const w = await mountView('compression', { detail: detailOf({ module: mod({ enabled: true, source: 'db' }) }) })
    expect(w.findAll('.mdt__badge-t').map((n) => n.text())).toContain('启用')
  })

  // ★★ 这两条是**两个不同的分支**，别混：`v-if="moduleIsAlwaysOn"` 抢先于
  //   `v-else-if="moduleHasNoSettingKey"` ⇒ setting_key 为空**且**落在兜底里时
  //   只会看到 alwaysOnNote，noSettingKeyNote 渲染不出来。
  it('★★★★★★ setting_key 为空 + 落在兜底 ⇒ 走「常开」那条', async () => {
    const w = await mountView('compression', {
      detail: detailOf({ module: mod({ setting_key: '', enabled: true, source: 'default' }) }),
    })
    const notes = w.findAll('.mdt__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('压根没查任何设置'))).toBe(true)
  })

  it('★★★★★ setting_key 为空但真读到值 ⇒ 走「不会去查任何设置」那条', async () => {
    // ★ isAlwaysOn 要求 source==='default'；这里 source==='db' ⇒ 两个分支都可达
    const w = await mountView('compression', {
      detail: detailOf({ module: mod({ setting_key: '', enabled: true, source: 'db' }) }),
    })
    expect(w.findAll('.mdt__note').map((n) => n.text()).some((x) => x.includes('不会去查任何设置'))).toBe(true)
  })

  it('★★★★★ `blocked_reason` 存在 ⇒ 原样显示', async () => {
    const w = await mountView('compression', {
      detail: detailOf({ module: mod({ blocked_reason: '需先启用依赖模块: 会话缓存', can_toggle_enabled: false }) }),
    })
    expect(w.findAll('.mdt__note').map((n) => n.text()).some((x) => x.includes('需先启用依赖模块: 会话缓存'))).toBe(
      true,
    )
  })

  it('★★★★★★ 页面上没有任何 toggle / test 控件（两者都不是只读）', async () => {
    const w = await mountView('feishu_bot')
    const texts = w.findAll('button').map((b) => b.text())
    expect(texts.some((x) => x.includes('切换'))).toBe(false)
    expect(texts.some((x) => x.includes('测试'))).toBe(false)
    // ★ 明说 test 端点有外部副作用
    expect(w.findAll('.mdt__note--warn').map((n) => n.text()).some((x) => x.includes('真发一条消息'))).toBe(true)
  })

  it('★★★★★ 依赖全是可选 ⇒ 明说不会进阻塞原因', async () => {
    const w = await mountView('compression', {
      detail: detailOf({ module: mod({ dependencies: [{ key: 'a', name: 'A', required: false }] }) }),
    })
    expect(w.findAll('.mdt__note').map((n) => n.text()).some((x) => x.includes('全都不是必需的'))).toBe(true)
  })
})

describe('★★★★★ 路由参数变了要重取', () => {
  it('★★★★★ key 变化 ⇒ 重新请求', async () => {
    const w = await mountView('compression')
    expect(detailMock).toHaveBeenCalledTimes(1)
    routeMock.params = { key: 'feishu_bot' }
    await w.vm.$nextTick()
    await flushPromises()
    await flushPromises()
    expect(detailMock).toHaveBeenCalledTimes(2)
  })
})
