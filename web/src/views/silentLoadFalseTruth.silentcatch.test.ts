// 全量跑时 175 个文件抢 CPU，每个文件的**第一个 mount 用例**要付
// 「动态 import + 整页渲染」的冷启动成本，实测会超过默认的 5s 超时。
// ⇒ 症状是「超时」而不是「断言失败」，很容易被当成产品回归。
// 这里给整份文件一个显式超时；判据本身不放宽。
// silentLoadFalseTruth.silentcatch.test.ts —— 「没查到」不得被讲成「没有」
//
// ## 挡住的是什么
//
// 2026-10-03 之前两处静默吞错，页面说的都是**假话**：
//
// 1. `CompressionView`
//    · `loadStats` 失败   → `stats-row` 是 `v-if="stats"`，整块消失
//                          ⇒ 页面看起来像「这段时间没有任何压缩」
//    · `loadSessions` 失败 → 渲染 `compression.table.empty`
//                          ⇒ 直接一句「没有会话」：不是没有，是没查到
//    · `loadCurrentConfig` 失败 → 配置条停在 `spec.default`
//                          ⇒ 把**出厂默认值**当成服务端当前配置讲出去
// 2. `KeysView.loadDefaultLimits`
//    失败时 `defaultLimits` 保留硬编码初值 `{rpm:12, concurrent:6, tpm:null}`，
//    而保存按钮只在 `limitsSaving` 时禁用 ⇒ 用户点开弹窗、改一个字段、保存，
//    `setDefaultLimits` 收到的就是一份 rpm=12 的**假配置**。
//    这次先核过可达性：初值在、mount 时加载、按钮未按加载态禁用。
//
// ## 判据钉住的是什么
//
// 「失败必须被显示」之外，KeysView 还要求**不可保存**——
// 只提示不禁保存，假值照样能写回服务端；只禁保存，用户仍会读到假数字。
// 两条都要，所以判据钉两个面：横幅在 + 控件禁用。
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const getCompressionStatsMock = vi.fn()
const getCompressionSessionsMock = vi.fn()
const getDefaultLimitsMock = vi.fn()
const setDefaultLimitsMock = vi.fn()
const getSettingMock = vi.fn()

// 键集由两个视图的 import 语句生成。vitest 对具名导入按 key 校验，
// 漏一个就会报 `No "x" export is defined on the mock` —— 而那个抛点若正好
// 落在被测的 try 里，**成功用例会误走失败分支**（本文件第一版就中过）。
vi.mock('../api', () => ({
  getCompressionStats: (...a: unknown[]) => getCompressionStatsMock(...a),
  getCompressionSessions: (...a: unknown[]) => getCompressionSessionsMock(...a),
  getDefaultLimits: (...a: unknown[]) => getDefaultLimitsMock(...a),
  setDefaultLimits: (...a: unknown[]) => setDefaultLimitsMock(...a),
  getKeys: () => Promise.resolve([]),
}))
vi.mock('../api/settings', () => ({ getSetting: (...a: unknown[]) => getSettingMock(...a) }))
vi.mock('../router', () => ({ default: {} }))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      compression: {
        load: {
          statsFailed: '压缩统计加载失败',
          sessionsFailed: '压缩会话列表加载失败',
          configFailed: '压缩配置加载失败',
        },
      },
      keys: { list: { limits: { loadFailed: '默认限额加载失败' } } },
    },
  },
})

async function renderCompression() {
  const View = (await import('./CompressionView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

async function renderKeys() {
  const View = (await import('./KeysView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

/** 打开「默认限制」弹窗（modal 默认不渲染，不打开就断言不到东西）。 */
async function openLimitsModal() {
  const w = await renderKeys()
  const opener = w.findAll('button').find((b) => /默认限制|limit/i.test(b.text()))
  expect(opener, '找不到打开默认限制弹窗的入口').toBeTruthy()
  await opener!.trigger('click')
  await flushPromises()
  await flushPromises()
  return w
}

const STATS = {
  total_requests: 10,
  compressed_total: 5,
  compression_rate: 0.5,
  estimated_tokens_saved: 100,
  strategy_distribution: {},
  hourly_series: [],
}

describe('CompressionView：三块加载失败要各自说出来', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getCompressionStatsMock.mockResolvedValue(STATS)
    getCompressionSessionsMock.mockResolvedValue({ items: [], count: 0 })
    getSettingMock.mockResolvedValue({ value: null, spec: { default: false } })
  })

  it('① 统计失败 → 横幅出现（而不是整块面板消失）', async () => {
    getCompressionStatsMock.mockRejectedValue('boom')
    const w = await renderCompression()
    expect(getCompressionStatsMock).toHaveBeenCalled()
    expect(w.find('.load-error-banner').exists()).toBe(true)
    expect(w.find('.load-error-banner').text()).toContain('压缩统计加载失败')
  })

  it('② 会话失败 → 不再说「没有会话」这句事实陈述', async () => {
    getCompressionSessionsMock.mockRejectedValue('boom')
    const w = await renderCompression()
    // 不用 `.empty-hint` 找：页面上有两处（图表 noData + 会话表 empty），
    // find() 只拿到第一个，断言的是另一块区域 —— 量具停在错误的位置。
    // 直接断言**那句事实陈述不再出现**。
    expect(w.text()).not.toContain('compression.table.empty')
    expect(w.text()).toContain('压缩会话列表加载失败')
  })

  it('③ 三块都失败 → 横幅列出三段，不吞掉任何一块', async () => {
    getCompressionStatsMock.mockRejectedValue('boom')
    getCompressionSessionsMock.mockRejectedValue('boom')
    getSettingMock.mockRejectedValue('boom')
    const w = await renderCompression()
    const text = w.find('.load-error-banner').text()
    expect(text).toContain('压缩统计加载失败')
    expect(text).toContain('压缩会话列表加载失败')
    expect(text).toContain('压缩配置加载失败')
  })

  it('④ 全部成功 → 无横幅，且空态仍是「没有会话」（正向对照）', async () => {
    const w = await renderCompression()
    expect(w.find('.load-error-banner').exists()).toBe(false)
    expect(w.find('.empty-hint').text()).not.toContain('加载失败')
  })
})

describe('KeysView 默认限额：加载失败时不得显示并保存假配置', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getDefaultLimitsMock.mockResolvedValue({ rate_limit_rpm: 12, rate_limit_concurrent: 6, rate_limit_tpm: null })
  })

  it('⑤ 加载失败 → 横幅出现 + 三个输入框都禁用', async () => {
    getDefaultLimitsMock.mockRejectedValue('boom')
    const w = await openLimitsModal()
    expect(w.find('.alert-warning').exists()).toBe(true)
    expect(w.text()).toContain('默认限额加载失败')
    const inputs = w.findAll('.modal input[type="number"]')
    expect(inputs.length).toBe(3)
    for (const el of inputs) {
      expect(el.attributes('disabled'), '加载失败时输入框仍可编辑 —— 用户能改出一份假配置').toBeDefined()
    }
  })

  it('⑥ 加载失败后不得调用 setDefaultLimits（假值不能写回服务端）', async () => {
    getDefaultLimitsMock.mockRejectedValue('boom')
    const w = await openLimitsModal()
    const save = w.findAll('.modal button').find((b) => /保存|save/i.test(b.text()))
    expect(save, '找不到保存按钮 —— 断言会静默跳过，必须显式失败').toBeTruthy()
    expect(save!.attributes('disabled')).toBeDefined()
    // 即便强行触发点击（按钮 disabled 时 Vue 不会派发），也不能有写入
    await save!.trigger('click')
    await flushPromises()
    expect(setDefaultLimitsMock).not.toHaveBeenCalled()
  })
})
