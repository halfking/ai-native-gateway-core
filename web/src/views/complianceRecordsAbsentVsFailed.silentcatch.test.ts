// complianceRecordsAbsentVsFailed.silentcatch.test.ts —— 合规页不得把「没查到」说成「0 条」
//
// ## 挡住的是什么
//
// `OutputComplianceView.loadRecords` 原来是：
//
//     } catch (e: unknown) {
//       // API不存在时使用空数据
//       records.value = []
//       recordsTotal.value = 0
//     }
//
// 注释本身就把「取不到」定义成了「没有」。后果是**同一句假话出现两次**：
//   ① 表格 `v-else-if="records.length === 0"` → 一句「暂无合规记录」
//   ② `recordsTotal = 0` 让分页文案渲染成「共 0 条」
//
// 为什么把这一处排在第九批第一位：这是**合规页**。「0 条违规」不是一条
// 随便的 UI 文案，它会被当作结论引用（审计、复盘、上报）。其余几处的
// 假话最多让人多点一次，这一处会让人**漏报**。
//
// 修法：新增 `recordsError`，catch 写原因；模板新增 `v-else-if="recordsError"`
// 取代空态，并把分页整块收在 `v-if="!recordsError"` 后面。
//
// ★ 只加横幅、留着 `records.length === 0` 那一格，是**不完整**的修法：
//   页面会同时说「加载失败」和「没有命中记录」，而「没有命中」是会被当真的那句。
//   ⇒ ④ 专门断言那句事实陈述在失败时**不出现**。
//
// ★ i18n 用**真实语种文件**，不是桩：`recordsNotLoaded` 这个键要是
//   只在某个语种漏了，本判据会跟着红（i18n-audit 之外多一道网）。
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import outputComplianceZh from '../locales/zh-CN/outputCompliance'

const reqMock = vi.fn()
const listSettingsMock = vi.fn()
const updateSettingMock = vi.fn()

// 键集按视图的 import 语句列全：漏一个具名导出会报
// `No "x" export is defined on the mock`，而那个抛点若落在被测的 try 里，
// 成功用例会误走失败分支。
vi.mock('../api/_core', () => ({ req: (...a: unknown[]) => reqMock(...a) }))
vi.mock('../api/settings', () => ({
  listSettings: (...a: unknown[]) => listSettingsMock(...a),
  updateSetting: (...a: unknown[]) => updateSettingMock(...a),
}))
// useActionMessage 只依赖 vue（composables/useActionMessage.ts 顶部 import），
// 留真即可；桩它只会增加桩与被测对象的错位面积。

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: { 'zh-CN': { outputCompliance: outputComplianceZh } },
})

const STATS = {
  total_checks: 1200,
  pii_hits: 3,
  secret_hits: 1,
  toxicity_hits: 0,
  jailbreak_hits: 2,
  avg_latency_ms: 12.5,
  last_updated: '2026-10-03T10:00:00Z',
}
const RECORD = {
  id: 1,
  session_id: 'sess-1',
  tenant_id: 't1',
  check_type: 'pii',
  hit_type: 'phone',
  severity: 0.8,
  redacted: true,
  content_preview: '138****0000',
  created_at: '2026-10-03T09:00:00Z',
}

type Vm = {
  activeTab: 'overview' | 'config' | 'records'
  loadRecords: () => Promise<void>
  recordsError: string
}

async function renderRecords() {
  const View = (await import('./OutputComplianceView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  // 记录区是 `v-if="activeTab === 'records'"`，不切过去就断言不到。
  // 直接改状态而不是点标签：点击要多穿一层 async，桩件失败会表现成「产品断言红」。
  ;(w.vm as unknown as Vm).activeTab = 'records'
  await flushPromises()
  return w
}

function vm(w: Awaited<ReturnType<typeof renderRecords>>) {
  return w.vm as unknown as Vm
}

describe('OutputComplianceView 命中记录：取不到 ≠ 没有违规', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    listSettingsMock.mockResolvedValue([])
    // 按 URL 分流：默认两个端点都成功，各自给真实形状的响应。
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/stats')) return Promise.resolve({ ...STATS })
      if (url.includes('/records')) return Promise.resolve({ records: [], total: 0 })
      return Promise.reject(new Error(`未预期的 URL：${url}`))
    })
  })

  it('① 记录查询失败 → 横幅出现，且「暂无合规记录」那句不出现', async () => {
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/stats')) return Promise.resolve({ ...STATS })
      if (url.includes('/records')) return Promise.reject(new Error('records 502'))
      return Promise.reject(new Error(`未预期的 URL：${url}`))
    })
    const w = await renderRecords()
    const banner = w.findAll('.error-banner').find((el) => el.text().includes('records 502'))
    expect(banner, '必须有一条带失败原因的横幅').toBeTruthy()
    // ★ 核心断言：那句会被当成结论引用的话，**不许出现**。
    // 只加横幅而留着 `records.length === 0` 那一格，这一句照样会渲染。
    expect(w.text()).not.toContain(outputComplianceZh.empty)
  })

  it('② 记录查询失败 → 分页整块不渲染（否则「共 0 条」是同一句假话的第二次出现）', async () => {
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/stats')) return Promise.resolve({ ...STATS })
      if (url.includes('/records')) return Promise.reject(new Error('records 502'))
      return Promise.reject(new Error(`未预期的 URL：${url}`))
    })
    const w = await renderRecords()
    expect(w.find('.pagination').exists(), '失败时不该有分页').toBe(false)
    expect(w.text()).not.toContain(outputComplianceZh.pagination.info.replace('{current}', '1'))
  })

  it('③ 记录查询成功且真为空 → 那句「暂无合规记录」必须出现（正向对照，防永远说失败）', async () => {
    const w = await renderRecords()
    expect(w.text()).toContain(outputComplianceZh.empty)
    expect(w.findAll('.error-banner').filter((el) => el.text().includes('recordsNotLoaded') || el.text().includes('加载失败'))).toHaveLength(0)
    expect(w.find('.pagination').exists()).toBe(true)
  })

  it('④ 记录查询成功且有数据 → 行渲染出来，表格不是空的', async () => {
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/stats')) return Promise.resolve({ ...STATS })
      if (url.includes('/records')) return Promise.resolve({ records: [RECORD], total: 1 })
      return Promise.reject(new Error(`未预期的 URL：${url}`))
    })
    const w = await renderRecords()
    expect(w.findAll('tbody tr').length).toBeGreaterThan(0)
    expect(w.text()).toContain('sess-1')
    expect(w.text()).not.toContain(outputComplianceZh.empty)
  })

  it('⑤ 先失败后重试成功 → 横幅消失，错误态可恢复（正向对照，也是防恒绿）', async () => {
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/stats')) return Promise.resolve({ ...STATS })
      if (url.includes('/records')) return Promise.reject(new Error('records 502'))
      return Promise.reject(new Error(`未预期的 URL：${url}`))
    })
    const w = await renderRecords()
    expect(vm(w).recordsError, '前置：确实处于失败态').toBe('records 502')

    // 重试端点改成成功，再调一次 loadRecords
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/stats')) return Promise.resolve({ ...STATS })
      if (url.includes('/records')) return Promise.resolve({ records: [RECORD], total: 1 })
      return Promise.reject(new Error(`未预期的 URL：${url}`))
    })
    await vm(w).loadRecords()
    await flushPromises()

    expect(vm(w).recordsError, '成功后必须清空错误态').toBe('')
    expect(w.text()).not.toContain('records 502')
    expect(w.find('.pagination').exists(), '成功后分页要回来').toBe(true)
  })
})
