// V1DataFrozenBanner.test.ts — 横幅的**渲染**判据（审计 §9.73.7）。
//
// # 为什么必须有这道门（composable 的测试不够）
//
// `useV1DataHorizon.test.ts` 覆盖三态转换，但它**完全不需要组件存在**：
// 把 `V1DataFrozenBanner.vue` 整个删掉，那 7 条用例依然全绿。
// 那样就得到一个「有状态、有测试、但没有任何消费者」的 composable
// —— 正是 §9.37 记的「没有第二个消费方的字段在事实层面是装饰」。
//
// ⇒ 这里**真的挂载组件**，断言四种状态下渲染出什么；
// 另有一条读源文件的断言，把 App.vue 与横幅接起来（挂载整个 App.vue
// 需要 router + store + 一堆依赖，代价远大于收益，而这里要证明的
// 只是「App.vue 确实渲染了这个组件」这一件事）。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../api/v1DataHorizon', () => ({ getV1DataHorizon: vi.fn() }))
import { getV1DataHorizon } from '../../api/v1DataHorizon'
import { refreshV1DataHorizon, __resetV1DataHorizonForTests } from '../../composables/useV1DataHorizon'
import V1DataFrozenBanner from './V1DataFrozenBanner.vue'

const mockGet = vi.mocked(getV1DataHorizon)

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      app: {
        v1DataFrozen: {
          title: '流量数据已停更',
          titleUnavailable: '无法确认流量数据状态',
          affects: '受影响读点档位',
          gateKey: '控制开关',
          retry: '重新检查',
          failedHint: '未确认状态不等于状态正常',
        },
      },
    },
  },
})

const notice = {
  frozen: true as const,
  unknown: false as const,
  source: 'request_logs',
  gate_key: 'storage.request_logs_write_enabled',
  effect: 'EFFECT_TEXT',
  silence: 'SILENCE_TEXT',
  affects: ['silently_frozen', 'silently_empty'],
}

function mountBanner() {
  return mount(V1DataFrozenBanner, { global: { plugins: [i18n] } })
}

describe('V1DataFrozenBanner', () => {
  beforeEach(() => {
    __resetV1DataHorizonForTests()
    mockGet.mockReset()
  })

  it('unknown 态：什么都不渲染（页面刚起来闪一条比不闪更糟）', () => {
    expect(mountBanner().html()).toBe('<!--v-if-->')
  })

  it('live 态：什么都不渲染', async () => {
    mockGet.mockResolvedValue({ v1_data_horizon: null, '//': '' })
    await refreshV1DataHorizon()
    expect(mountBanner().html()).toBe('<!--v-if-->')
  })

  it('frozen 态：渲染标题 + 后端给的两句 + 开关键 + 档位', async () => {
    mockGet.mockResolvedValue({ v1_data_horizon: notice, '//': '' })
    await refreshV1DataHorizon()
    const html = mountBanner().html()
    expect(html).toContain('流量数据已停更')
    // 后端给的两句是「这个实例」的实话，必须原样出现在页面上。
    expect(html).toContain('EFFECT_TEXT')
    expect(html).toContain('SILENCE_TEXT')
    // 运维要知道改哪里。
    expect(html).toContain('storage.request_logs_write_enabled')
    expect(html).toContain('silently_frozen')
  })

  it('★ failed 态：仍渲染横幅，但**不**展示后端那两句（我们并不知道是不是停更）', async () => {
    mockGet.mockRejectedValue(new Error('down'))
    await refreshV1DataHorizon()
    const html = mountBanner().html()
    expect(html).toContain('无法确认流量数据状态')
    expect(html).toContain('未确认状态不等于状态正常')
    // 关键：failed 时不能显示「数据已停更」——那是**未验证的断言**。
    expect(html).not.toContain('流量数据已停更')
    // 也不能把后端的话挂出来（那时并没有后端响应）。
    expect(html).not.toContain('SILENCE_TEXT')
  })

  it('failed 态用更强的视觉标记（连「是不是停更」都不知道，页面数字都该被怀疑）', async () => {
    mockGet.mockRejectedValue(new Error('down'))
    await refreshV1DataHorizon()
    expect(mountBanner().html()).toContain('v1-frozen-banner--failed')
  })
})

describe('App.vue 接了横幅（读源文件）', () => {
  const __dirname = dirname(fileURLToPath(import.meta.url))
  const appVue = readFileSync(join(__dirname, '..', '..', 'App.vue'), 'utf8')

  it('App.vue 真的渲染了 V1DataFrozenBanner', () => {
    expect(appVue).toMatch(/import\s+V1DataFrozenBanner\s+from\s+['"][^'"]*V1DataFrozenBanner\.vue['"]/)
    // <V1DataFrozenBanner /> 或带属性形式。排除「只 import 不渲染」——
    // 那正是「有状态无消费者」的形态。
    expect(appVue).toMatch(/<V1DataFrozenBanner\s*\/?>/)
  })

  it('App.vue 挂载时拉一次告示（否则横幅永远停在 unknown 态）', () => {
    expect(appVue).toMatch(/refreshV1DataHorizon\(\)/)
  })
})
