// SelfCheckPanel.featuredError.test.ts —— 系统特色模型加载失败不得说成「没配置」
//
// ## 挡住的是什么
//
// SelfCheckPanel 的「自检设置」弹窗里有一块**只读**展示：
// 「系统特色模型（只读，在 模型管理 → 特色模型 中配置）」。
// 修前（2026-10-03）：
//
//     try   { systemFeaturedModels.value = res.models ?? [] }
//     catch { systemFeaturedModels.value = [] }
//
// 模板是：
//
//     v-if="systemFeaturedLoading"     → 加载中…
//     v-else-if="length === 0"         → 暂无系统特色模型
//
// ⇒ 请求失败时面板写着「暂无系统特色模型」。
//
// **危害比一般空态更深一层**：这块是只读的，标签还明写「在 模型管理 →
// 特色模型 中配置」。用户读到「暂无」的自然反应是**去配置页添加模型**，
// 而配置其实是好的 —— 这次失败纯属读取端问题。
// 把一次读取失败讲成「你没配」，会把人引向完全错误的排查方向：
// 改完配置重试仍看到「暂无」，直到有人怀疑读取端为止。
//
// 与本轮 pie 降级、错误下钻失败、credits 降级显示 0 同族：
// **把「不知道」渲染成「知道」比不显示更糟**；这一处代价更高，
// 因为它自带一个「你应该去别处修」的误导性指引。
//
// ## 三态
//
// ① 加载失败     → 显示失败原因 + 明确「为空不代表未配置」，**不含**「暂无系统特色模型」
// ② 成功且为空   → 显示「暂无系统特色模型」（真·没配，合法答案）
// ③ 成功且有数据 → 渲染标签
//
// ② 与 ① 必须分得开：把「真的没配」也讲成「失败」同样是错的。
//
// ## 为什么本文件不引入 i18n
//
// SelfCheckPanel 全文件 0 处 t()，全是硬编码中文。为这一处单独加
// i18n key 会把它变成「半 i18n 半硬编码」，反而更难维护。
//
// ## 判据怎么驱动组件（这一段本身是踩坑记录）
//
// 第一版走 UI 点击（找「设置」按钮 → trigger('click')），三条用例全红，
// 探针显示 **`.modal-overlay` 数量为 0** —— 弹窗压根没开。
// 根因：`openSettings()` 在 `settings.value` 为空时直接 return，
// 而 `settings` 由 onMounted 的 `loadAll()` 填充，那里有 5 个并发请求，
// 任一失败就整体落进 catch，`settings` 保持 null。
//
// ⇒ **不要赌「点按钮能打开弹窗」**：它同时依赖 6 个 API 与一次 flush 时序。
// 本判据改为直接调 setupState 上的 `openSettings()`（组件未 defineExpose，
// 但 test-utils 的 `w.vm` 能拿到），并在调用前显式断言
// `settings` 已就位 —— 失败时给出「判据停在错误的位置」而不是恒绿。
//
// 另：mock 必须挂在组件**真正 import 的模块**上
// （`../api/system` 与 `../api-selfcheck`，不是桶文件 `../api`），
// 且要覆盖 loadAll 的**整个取数面**，不是「弹窗需要的那一个」——
// 只 mock 一个时，每次红都只是暴露下一个没 mock 的端点。
//
// ## 反向对照（实测，变异先断言确实生效）
//
// · catch 退回 `{ systemFeaturedModels.value = [] }` → ① 红
// · 只删模板失败态分支、保留错误状态               → ① 红（有状态没消费者）
// · 失败态判定恒真（`v-else-if="true"`）           → ② ③ 红（防恒绿）

import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import SelfCheckPanel from './SelfCheckPanel.vue'

const getFeaturedModelsDynamic = vi.fn()

const settingsStub = {
  enabled: false,
  normal_interval_seconds: 3600,
  fault_interval_seconds: 60,
  max_models: 10,
  max_tokens_per_run: 1000,
  model_source: 'both',
}

vi.mock('../api/system', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../api/system')
  return { ...actual, getFeaturedModelsDynamic: (...a: unknown[]) => getFeaturedModelsDynamic(...a) }
})
vi.mock('../api-selfcheck', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../api-selfcheck')
  return {
    ...actual,
    fetchSelfCheckSettings: async () => settingsStub,
    // 组件读的是 st.summary.*（SelfCheckPanel.vue:358-362），少这一层
    // 就会在渲染时抛 "Cannot read properties of undefined (reading 'total_runs')"。
    fetchSelfCheckStats: async () => ({
      enabled: false,
      summary: { total_runs: 0, success_rate: 0, success_runs: 0, partial_runs: 0, failed_runs: 0 },
      by_model: [],
      error_breakdown: [],
      trend: [],
    }),
    fetchSelfCheckRuns: async () => ({ items: [], total: 0 }),
    fetchSelfCheckModels: async () => ({ models: [] }),
    fetchSelfCheckRunDetail: async () => ({}),
    fetchSelfCheckTriggerAvailability: async () => ({ available: false }),
    triggerSelfCheck: async () => ({}),
    fetchProbeSystemHealth: async () => ({}),
  }
})

async function mountPanel() {
  const w = mount(SelfCheckPanel)
  await flushPromises()
  const vm = w.vm as unknown as Record<string, unknown> & { openSettings: () => void }
  // 前提断言：弹窗能否打开取决于 settings 是否就位。
  // 不显式断言的话，「弹窗没开」会被误读成「修复没生效」。
  if (!vm.settings) {
    throw new Error(
      'settings 未就位，弹窗打不开 —— 判据停在了错误的位置。' +
      `先看 w.text() 定位（loadAll 的 5 个请求有任何一个失败都会让 settings 保持 null）。`,
    )
  }
  vm.openSettings()
  await flushPromises()
  return w
}

beforeEach(() => {
  getFeaturedModelsDynamic.mockReset()
})

describe('系统特色模型三态', () => {
  it('① 加载失败 → 显示失败原因，且不说「暂无系统特色模型」', async () => {
    const err: any = new Error('boom')
    err.detail = 'featured view not migrated'
    getFeaturedModelsDynamic.mockRejectedValue(err)

    const w = await mountPanel()
    const text = w.text()

    expect(text).toContain('featured view not migrated')
    expect(text).toContain('为空不代表未配置')
    // 关键：不得出现「你没配」这个结论。
    expect(text).not.toContain('暂无系统特色模型')
  })

  it('② 成功但为空 → 显示「暂无系统特色模型」，不显示失败', async () => {
    getFeaturedModelsDynamic.mockResolvedValue({ models: [] })

    const w = await mountPanel()
    const text = w.text()

    expect(text).toContain('暂无系统特色模型')
    expect(text).not.toContain('系统特色模型加载失败')
  })

  it('③ 成功且有数据 → 渲染特色模型标签', async () => {
    getFeaturedModelsDynamic.mockResolvedValue({ models: [{ name: 'deepseek-v3' }] })

    const w = await mountPanel()
    const text = w.text()

    expect(text).toContain('deepseek-v3')
    expect(text).not.toContain('系统特色模型加载失败')
    expect(text).not.toContain('暂无系统特色模型')
  })
})
