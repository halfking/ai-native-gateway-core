// QualityCorrelationsView.nullbreakdown.test.ts — 「后端返 null」这条契约的门禁。
//
// ★ 这道门存在的理由（2026-10-20 复核轮，
//   docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.8）：
//
//   逐页扫描在 `/quality-correlations` 上抓到本轮**唯一一条** JS 运行时异常：
//
//     [Vue Error] TypeError: Cannot read properties of null (reading 'reduce')
//       at ac.fn (http://127.0.0.1:8782/assets/QualityCorrelationsView-*.js:1:2961)
//
//   根因是**契约两端不一致**：
//     · 后端 `var breakdown []QualityCorrelationRow` 是 Go 的 **nil slice**
//       ⇒ JSON 序列化成 **`null`** 而不是 `[]`（同 handler 的 `insights`
//       被显式初始化成 `[]` ⇒ 同一响应里两个数组字段形态不一致，是疏漏）；
//     · 前端 TS 类型把 `breakdown` 声明成**非空数组** ⇒ 类型是**假的**，
//       于是页面直接 `resp.value.breakdown.reduce(...)`，null 时当场抛异常。
//
//   触发条件是**合法业务状态**（无数据），不是异常输入。
//
//   这道门钉的**不变量**：**后端把数组字段返成 null 时，页面必须渲染空态、
//   不得抛异常。** 它与「后端是否返 null」无关 ⇒ 后端改成恒返 `[]` 之后
//   这道门**仍然有效**（前端不能依赖后端的空值形态）。
//
//   量具纪律：这里**不**断言「后端返 null」这个具体形态（那是后端那道门的职责），
//   只断言**页面的承受力**；否则后端修好之后这道门就变成永远绿的废门。
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import QualityCorrelationsView from './QualityCorrelationsView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

const { ctrl, mockApi } = vi.hoisted(() => {
  const ctrl = {
    /** 'rows' = 正常数组；'null' = 后端返 null（Go nil slice 的真实序列化形态） */
    shape: 'rows' as 'rows' | 'null',
    reset() {
      ctrl.shape = 'rows'
    },
  }
  return { ctrl, mockApi: () => ({}) }
})

vi.mock('../api', () => ({
  getQualityCorrelations: vi.fn(async () => {
    if (ctrl.shape === 'null') {
      // 后端在「无数据」时的真实响应：两个数组字段都是 null。
      return {
        generated_at: '2026-10-20T00:00:00.000Z',
        window_days: 7,
        breakdown: null,
        insights: null,
      }
    }
    return {
      generated_at: '2026-10-20T00:00:00.000Z',
      window_days: 7,
      breakdown: [
        { bucket: '0-500', samples: 12, success_rate: 0.9, avg_latency_ms: 100, avg_quality: 0.9, avg_cost_usd: 0.01 },
      ],
      insights: [],
    }
  }),
}))

vi.mock('../components/ui/KxDateRangePicker.vue', () => ({
  default: { props: ['modelValue', 'presets', 'maxSpanDays'], template: '<div class="kxdaterangepicker-stub" />' },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { dateRange: { title: '时间范围', startDate: '开始', endDate: '结束' }, button: { apply: '应用' } },
      qualityCorrelations: {
        title: '质量相关性',
        subtitle: '按维度分桶看质量',
        filter: {
          window: '时间窗', bucketBy: '分桶维度', loading: '加载中…', refresh: '刷新',
          days: { d1: '1 天', d7: '7 天', d30: '30 天', d90: '90 天' },
          by: { prompt_length: '提示词长度', tools: '工具调用', images: '图片', code_block: '代码块' },
        },
        meta: { generated: '生成于', window: '窗口', windowDays: '{n} 天', totalSamples: '总样本', needSamples: '样本不足' },
        breakdown: {
          title: '按{by}分桶',
          headers: { bucket: '分桶', samples: '样本数', success: '成功率', latency: '延迟', quality: '质量', cost: '成本' },
          empty: '暂无分桶数据',
        },
        insights: {
          title: '相关性洞察', hint: '说明', buckets: '{n} 个分桶', samples: '{n} 个样本',
          emptyInsufficient: '样本不足，无法给出洞察', emptyUnexpected: '没有发现显著相关性',
        },
      },
      hyper: { dataView: { table: '表格', cards: '卡片' } },
    },
  },
})

function mockWindowClass(cls: 'compact' | 'expanded'): void {
  _resetForTests()
  const lo = cls === 'compact' ? 0 : 1280
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (q: string) => {
      const min = q.match(/min-width:\s*([\d.]+)px/)
      const max = q.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return {
        matches, media: q, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
      }
    },
  })
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('QualityCorrelationsView：后端把数组字段返成 null 时', () => {
  it('挂载不抛异常（旧实现在这里抛 TypeError: Cannot read properties of null）', async () => {
    ctrl.shape = 'null'
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    let w: Awaited<ReturnType<typeof mount>> | undefined
    expect(() => {
      w = mount(QualityCorrelationsView, { global: { plugins: [i18n] } })
    }).not.toThrow()
    await flushPromises()
    await flushPromises()
    // 错误也可能被 Vue 的 error handler 吞掉后只打日志 ⇒ 两条都要看
    const logged = [...spy.mock.calls, ...warn.mock.calls].map((c) => String(c[0])).join('\n')
    expect(logged, '页面在 null 响应下打了错误日志').not.toMatch(/TypeError|Cannot read properties of null/)
    spy.mockRestore()
    warn.mockRestore()
    expect(w).toBeTruthy()
  })

  it('渲染空态而不是崩掉：出「暂无分桶数据」，总样本为 0', async () => {
    ctrl.shape = 'null'
    const w = mount(QualityCorrelationsView, { global: { plugins: [i18n] } })
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('暂无分桶数据')
    // 总样本这一格不能是空/NaN —— 旧实现在这里直接抛
    expect(w.text()).toMatch(/总样本[:：]\s*0/)
    expect(w.text()).not.toMatch(/NaN/)
  })

  it('compact 档同样不崩（compact 分支另有 :empty 判定）', async () => {
    ctrl.shape = 'null'
    mockWindowClass('compact')
    const w = mount(QualityCorrelationsView, { global: { plugins: [i18n] } })
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('暂无分桶数据')
  })

  it('正常数组响应仍然照常渲染（这道门不能把正常路径也钉死）', async () => {
    ctrl.shape = 'rows'
    const w = mount(QualityCorrelationsView, { global: { plugins: [i18n] } })
    await flushPromises()
    await flushPromises()
    expect(w.findAll('.qc-table tbody tr')).toHaveLength(1)
    expect(w.text()).toMatch(/总样本[:：]\s*12/)
  })
})
