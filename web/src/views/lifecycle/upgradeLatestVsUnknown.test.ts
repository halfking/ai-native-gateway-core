// upgradeLatestVsUnknown.test.ts —— 升级页不得在检查失败时说「已是最新」
//
// ## 挡住的是什么
//
// `UpdateActivateView.loadUpgrade` 原来是 `catch { upgrade.value = null }`。
// 下游 `UpdateActivateVersionsCard` 的三处于是**同时**变成假话：
//
//   ① `<el-tag v-else type="success">已是最新</el-tag>` —— 绿色标签
//   ② `<template v-else-if="!canUpgrade">当前已是最新版本</template>`
//   ③ `latestVersion` 回退到 `currentVersion`
//      ⇒「当前版本 / 最新版本」两栏显示同一个数，看着就像「确认过是最新的」
//
// 升级页上的这句话是**安全更新**级别的假话：用户据此跳过补丁。
//
// `loadCatalog` 失败更隐蔽：整块版本区是 `v-else-if="catalog && topVersions.length"`，
// 条件不成立时**什么都不渲染**——空白读起来就是「没有可安装的版本」。
//
// ## 判据钉住的是什么
//
//   ① 检查失败 → 不出现「已是最新」，出现「状态未知」+ 原因
//   ② 检查失败 → 「最新版本」不再回退成当前版本（两栏不能同数）
//   ③ 检查失败 → 不出现「当前已是最新版本」
//   ④ 目录失败 → 有说明，而不是空白
//   ⑤ 检查成功且有更新 → 正常显示「未安装」+ 可升级（正向对照）
//   ⑥ 检查成功且确为最新 → 才显示「已是最新」（正向对照，防「永远说未知」）
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { bootstrapApi } from '../../api/bootstrap'
import { updateActivateApi } from '../../api/updateActivate'

// 本判据分两段：
//   段一**直接给卡片喂 props** —— 守卡片自己的分支结构。
//   段二挂**整页** —— 守「catch 写进 ref → :check-error 绑到卡片」这段接线。
// 段一在时，段二看起来多余；实际上段一**完全看不见** `:check-error`，
// 有人把那个绑定摘掉，段一 6 例照样全绿。这正是 T1 点名的
// 「产出正确但没到消费者」，所以两段都要在。
//
// 段二第一版把 api 桩只挂在 upgradeStatus 上，红了又换下一个端点，
// 一次一条。onMounted → refreshAll 并发调 5 个取数面
// （loadStatus/loadUpgrade/loadLicense/loadCatalog/loadModules），
// 桩必须覆盖全部，缺一个就报 `Failed to parse URL from /api/...`。
vi.mock('../../api/bootstrap', () => ({
  bootstrapApi: {
    status: vi.fn(),
    fingerprint: vi.fn(),
    activateQuick: vi.fn(),
  },
  markBootstrapActivated: vi.fn(),
}))
vi.mock('../../api/updateActivate', () => ({
  updateActivateApi: {
    upgradeStatus: vi.fn(),
    upgradeCheck: vi.fn(),
    licenseStatus: vi.fn(),
    downloadsCatalog: vi.fn(),
    modulesCatalog: vi.fn(),
  },
}))
vi.mock('../../utils/deviceFingerprint', () => ({
  ensureInstanceId: () => 'inst-test',
  setInstanceId: vi.fn(),
  readStoredHardwareHash: () => '',
  collectClientFingerprint: vi.fn(),
  resolveHardwareHash: async () => 'hash-test',
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: { 'zh-CN': {} },
})

const NEWER = {
  has_update: true,
  latest_version: '3.0.0',
  current_version: '2.9.0',
  current_build_seq: 10,
  update_mandatory: false,
}
const SAME = { has_update: false, latest_version: '2.9.0', current_version: '2.9.0', update_mandatory: false }
// 形状照抄 `Release`（api/updateActivate.ts:56）：模板读的是
// `release_date` / `channel`，我第一版写的 `released_at` / `notes`
// 两栏都渲染成 undefined —— 夹具字段名错了，判据照样能跑，只是没在测东西。
const CATALOG = {
  versions: [
    { version: '3.0.0', build_seq: 11, channel: 'stable', release_date: '2026-10-01', items: [] },
    { version: '2.9.0', build_seq: 10, channel: 'stable', release_date: '2026-09-01', items: [] },
  ],
}

async function render() {
  const View = (await import('../../components/lifecycle/UpdateActivateVersionsCard.vue')).default
  const w = mount(View, {
    props: {
      catalog: CATALOG as never,
      loading: false,
      checking: false,
      upgradeStatus: null,
      currentVersion: '2.9.0',
      activated: true,
    },
    global: { plugins: [i18n] },
    shallow: false,
  })
  await flushPromises()
  await flushPromises()
  return w
}

describe('升级卡：检查失败时不得说「已是最新」', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('① 检查失败 → 「状态未知」而不是「已是最新」', async () => {
    const w = await render()
    await w.setProps({ checkError: '升级检查失败' })
    await flushPromises()
    const text = w.text()
    expect(text).toContain('状态未知')
    expect(text).not.toContain('已是最新')
  })

  it('② 检查失败 → 「当前已是最新版本」不出现', async () => {
    const w = await render()
    await w.setProps({ checkError: '升级检查失败' })
    await flushPromises()
    expect(w.text()).not.toContain('当前已是最新版本')
    expect(w.text()).toContain('升级状态未知')
  })

  // ③ 第一版判据写成 /最新版本[\s\S]{0,40}2\.9\.0/ 全文正则，红了。
  // 根因是**判据**而非产品：「最新版本」格子后面紧跟着版本列表，
  // 列表里 2.9.0 那一行（已安装）落在 40 字窗口内 ⇒ 正则必然命中。
  // 教训同本轮另外两处：全文正则会把「同一页里别处出现的同名字符串」
  // 算进本节点的取值。凡是断言某个**格子**的取值，就锚那个节点。
  async function bannerValues(w: Awaited<ReturnType<typeof render>>) {
    const cells = w.findAll('.version-banner .banner-cell')
    expect(cells, '.version-banner 应恰好两栏（当前版本 / 最新版本）').toHaveLength(2)
    const labels = cells.map((c) => c.find('.banner-label').text())
    const values = cells.map((c) => c.find('.banner-value').text().trim())
    return { labels, values }
  }

  it('③ 检查失败 → 「最新版本」不回退成当前版本（两栏不能同数）', async () => {
    const w = await render()
    await w.setProps({ checkError: '升级检查失败' })
    await flushPromises()
    const { labels, values } = await bannerValues(w)
    expect(labels[0]).toContain('当前版本')
    expect(labels[1]).toContain('最新版本')
    expect(values[0]).toBe('2.9.0')
    // 关键断言：这一栏是「—」，不是 2.9.0。
    // 用「两栏不同」而不是「这一栏不是某个字面量」——前者在
    // 2.9.0 不是当前版本时仍然成立，不会随夹具取值漂移。
    expect(values[1]).toBe('—')
    expect(values[1]).not.toBe(values[0])
  })

  it('④ 目录失败 → 有说明而不是空白', async () => {
    const w = await render()
    await w.setProps({ catalog: null, catalogError: '版本目录加载失败' })
    await flushPromises()
    expect(w.text()).toContain('版本目录加载失败')
  })

  it('⑤ 检查成功且有更新 → 「未安装」且可升级（正向对照）', async () => {
    const w = await render()
    await w.setProps({ upgradeStatus: NEWER as never })
    await flushPromises()
    const text = w.text()
    expect(text).toContain('未安装')
    expect(text).not.toContain('已是最新')
  })

  it('⑥ 检查成功且确为最新 → 才显示「已是最新」（正向对照，防永远说未知）', async () => {
    const w = await render()
    await w.setProps({ upgradeStatus: SAME as never })
    await flushPromises()
    expect(w.text()).toContain('已是最新')
    // ③ 断言 values[1] === '—'，只有这里证明「同一节点在另一种数据下确实
    // 会显示版本号」，才能排除 ③ 恒绿是因为锚点根本读不到东西。
    const { values } = await bannerValues(w)
    expect(values[1]).toBe('2.9.0')
    expect(values[1]).toBe(values[0])
  })
})

// ===== 段二：整页接线 =====
//
// 守的是 `:check-error="upgradeCheckError"` / `:catalog-error="catalogError"`
// 这两个绑定，以及 `loadUpgrade` / `loadCatalog` 的 catch 真的把原因写进了 ref。
// 段一直接喂 props，看不见这一段。
describe('升级页接线：catch 写的原因必须真的到卡片', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    // 默认「一切正常」，每条用例只改自己关心的那一个端点。
    vi.mocked(bootstrapApi.status).mockResolvedValue({
      activated: true,
      instance_id: 'inst-test',
      service_version: '2.9.0',
    } as never)
    vi.mocked(bootstrapApi.fingerprint).mockResolvedValue({ hardware_hash: 'hash-test', instance_id: 'inst-test' } as never)
    vi.mocked(updateActivateApi.upgradeStatus).mockResolvedValue(SAME as never)
    vi.mocked(updateActivateApi.licenseStatus).mockResolvedValue({ state: 'active' } as never)
    vi.mocked(updateActivateApi.downloadsCatalog).mockResolvedValue(CATALOG as never)
    vi.mocked(updateActivateApi.modulesCatalog).mockResolvedValue({ items: [] } as never)
  })

  async function renderPage() {
    const View = (await import('./UpdateActivateView.vue')).default
    const w = mount(View, {
      global: {
        plugins: [i18n],
        stubs: {
          RouterLink: true,
          OperationAgreementDialog: true,
          // 兄弟卡片与本判据无关，桩掉。VersionsCard **故意不桩** ——
          // 桩了它，W1 会因为「读不到任何东西」而红，那是量具坏了不是产品坏了。
          UpdateActivateSiteCard: true,
          UpdateActivateLicenseCard: true,
          UpdateActivateModulesCard: true,
        },
      },
    })
    // refreshAll 是 5 个并发取数，onMounted 还 await 了一层 ⇒ 三次 flush 足够，
    // 少一次就是「时序上没跑到就断言」的假红。
    await flushPromises()
    await flushPromises()
    await flushPromises()
    return w
  }

  it('W1 升级检查失败 → 整页真的渲染出「状态未知」', async () => {
    vi.mocked(updateActivateApi.upgradeStatus).mockRejectedValue(new Error('中心不可达'))
    const w = await renderPage()
    const card = w.findComponent({ name: 'UpdateActivateVersionsCard' })
    expect(card.exists(), '版本卡必须真的渲染（没被 stub 掉）').toBe(true)
    expect(card.props('checkError')).toBe('中心不可达')
    const text = card.text()
    expect(text).toContain('状态未知')
    expect(text).not.toContain('已是最新')
  })

  it('W2 版本目录失败 → 整页真的渲染出「版本目录加载失败」', async () => {
    vi.mocked(updateActivateApi.downloadsCatalog).mockRejectedValue(new Error('目录端点 502'))
    const w = await renderPage()
    const card = w.findComponent({ name: 'UpdateActivateVersionsCard' })
    expect(card.props('catalogError')).toBe('目录端点 502')
    expect(card.text()).toContain('版本目录加载失败')
  })

  it('W3 两个端点都成功 → 不许说「状态未知」（正向对照：防永远说未知，也证明卡片没被桩掉）', async () => {
    const w = await renderPage()
    const card = w.findComponent({ name: 'UpdateActivateVersionsCard' })
    expect(card.props('checkError')).toBe('')
    expect(card.props('catalogError')).toBe('')
    const text = card.text()
    expect(text).toContain('已是最新')
    expect(text).not.toContain('状态未知')
  })

  it('W4 拒绝原因带非 Error 值时也要有话可说（不留空白 ref）', async () => {
    vi.mocked(updateActivateApi.upgradeStatus).mockRejectedValue('timeout')
    const w = await renderPage()
    const card = w.findComponent({ name: 'UpdateActivateVersionsCard' })
    expect(card.props('checkError'), '字符串拒绝也要落到 ref 上').toBe('升级检查失败')
  })
})
