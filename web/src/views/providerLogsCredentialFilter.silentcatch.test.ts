// providerLogsCredentialFilter.silentcatch.test.ts —— 凭据下拉取不到 ≠ 这个凭据没有记录
//
// ## 挡住的是什么
//
// `LogsTab.loadCredentials` 原来是 `catch { credentials.value = [] }`。
// 凭据下拉失败后只剩「全部凭据」，用户按某个凭据筛出零条日志，
// 结论会是「这个凭据没有调用记录」—— 而真相是「凭据清单没加载出来」。
//
// 与 `RequestLogsView.loadKeys` 同一形状（第九批 P2 修过），同一个病两处。
//
// ⚠️ 不能复用主列表的 `error`：那个是「日志没加载出来」。混在一起的话，
//    「日志列表正常 + 凭据下拉空」会被读成「这个凭据没有记录」——
//    错误文案在，但它指错了对象，比没有文案更糟。判据 ③ 专门钉这一条。
//
// ## 判据钉住的是什么
//
// ① 凭据加载失败 → 出现 warning 级说明（不是主列表的 danger）
// ② 成功 → 不出现那条说明，且凭据进了下拉（正向对照，防永远说失败）
// ③ 凭据失败但**日志列表成功** → 主列表的 danger 不许出现（两者是不同的事）
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import providerDetailZh from '../locales/zh-CN/providerDetail'

const getProviderLogsMock = vi.fn()
const getProviderCredentialsMock = vi.fn()

vi.mock('../api', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../api')
  return {
    ...actual,
    getProviderLogs: (...a: unknown[]) => getProviderLogsMock(...a),
    getProviderCredentials: (...a: unknown[]) => getProviderCredentialsMock(...a),
  }
})
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
  messages: { 'zh-CN': { providerDetail: providerDetailZh } },
})

const CRED = { id: 7, provider_id: 1, label: '主用凭据', status: 'active' }
const MSG = providerDetailZh.logs.credentialsLoadFailed

async function renderLogs() {
  const View = (await import('./provider-detail/LogsTab.vue')).default
  const w = mount(View, {
    props: { providerId: 1 },
    global: { plugins: [i18n], stubs: { ModelPicker: true, RouterLink: true } },
    shallow: false,
  })
  await flushPromises()
  await flushPromises()
  return w
}

describe('LogsTab：凭据下拉取不到时不得被读成「这个凭据没有记录」', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getProviderLogsMock.mockResolvedValue({ items: [], total: 0 })
    getProviderCredentialsMock.mockResolvedValue([CRED])
  })

  it('① 凭据加载失败 → 出现 warning 级说明', async () => {
    getProviderCredentialsMock.mockRejectedValue(new Error('creds 502'))
    const w = await renderLogs()
    const warn = w.findAll('.alert-warning').find((el) => el.text().includes(MSG))
    expect(warn, '必须有一条凭据加载失败说明').toBeTruthy()
  })

  it('② 凭据加载成功 → 不出现那条说明，且凭据进了下拉（正向对照）', async () => {
    const w = await renderLogs()
    expect(w.text()).not.toContain(MSG)
    // 前置：凭据确实渲染进下拉了，否则 ① 可能只是「什么都没渲染」
    expect(w.findAll('select.cf-cred option').length).toBeGreaterThan(1)
  })

  it('③ 凭据失败但日志列表成功 → 主列表的 danger 不许出现（错误要指对对象）', async () => {
    getProviderCredentialsMock.mockRejectedValue(new Error('creds 502'))
    const w = await renderLogs()
    expect(w.text()).toContain(MSG)
    // 「日志没加载出来」这句话不许出现：日志是真的加载成功了。
    // 把两者混成一条，比只加横幅更糟。
    expect(w.findAll('.alert-danger'), '凭据失败不该被说成日志列表失败').toHaveLength(0)
  })

  it('④ 换 provider 重试成功 → 那条说明必须消失（错误态可恢复）', async () => {
    getProviderCredentialsMock.mockRejectedValue(new Error('creds 502'))
    const w = await renderLogs()
    expect(w.text(), '前置：确实处于失败态').toContain(MSG)

    getProviderCredentialsMock.mockResolvedValue([CRED])
    // watch(() => props.providerId) 会重新 loadCredentials
    await w.setProps({ providerId: 2 })
    await flushPromises()
    await flushPromises()

    expect(w.text(), '重试成功后必须清掉失败说明').not.toContain(MSG)
    expect(w.findAll('.alert-warning')).toHaveLength(0)
  })
})
