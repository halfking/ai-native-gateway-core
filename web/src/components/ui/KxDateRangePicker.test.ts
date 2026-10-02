// KxDateRangePicker.test.ts — 统一时间范围组件契约测试（2026-09-30）。
// 覆盖：触发器渲染、面板打开、预设回填、应用提交（update:modelValue + apply）、
// 结束早于开始禁用、maxSpanDays 超限禁用、instant 模式即时提交。
// EP 弹层内容走 teleport，这里对 ElPopover/ElDatePicker 打桩后直测面板逻辑。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import KxDateRangePicker from './KxDateRangePicker.vue'
import { calendarDayKey, isAfterDay, makeDateRangePresets, rangeSpanDays } from './kxDatePresets'

// 固定时钟：触发器「近 7 天」匹配依赖当前日期，必须冻结
const FIXED = Date.UTC(2026, 8, 30, 12, 0, 0)
// 用块体而非表达式体：vi.setSystemTime/useRealTimers 返回 VitestUtils，
// 箭头表达式体会把该返回值当成 hook 的清理回调返回，触发 TS2322
beforeEach(() => {
  vi.setSystemTime(FIXED)
})
afterEach(() => {
  vi.useRealTimers()
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: {
        button: { apply: '应用' },
        dateRange: {
          title: '时间范围',
          custom: '自定义',
          startDate: '开始日期',
          endDate: '结束日期',
          openAria: '选择时间范围',
          endBeforeStart: '结束需不早于开始',
          afterLatest: '不能晚于 {date}',
          spanTooLong: '跨度超过 {n} 天',
          preset: {
            today: '今天',
            yesterday: '昨天',
            last7d: '近 7 天',
            last14d: '近 14 天',
            last30d: '近 30 天',
            thisMonth: '本月',
            lastMonth: '上月',
          },
        },
      },
    },
  },
})

const ElPopoverStub = {
  props: ['visible', 'width', 'disabled'],
  emits: ['update:visible'],
  template: `<div class="popover-stub"><slot name="reference" /><slot /></div>`,
}
const ElDatePickerStub = {
  props: ['modelValue', 'type', 'valueFormat', 'format', 'clearable', 'disabledDate'],
  emits: ['update:modelValue', 'change'],
  template: `<input class="dp-stub" :value="modelValue" @input="$emit('update:modelValue', $event.target.value); $emit('change', $event.target.value)" />`,
}

function mountPicker(props: Record<string, unknown> = {}) {
  return mount(KxDateRangePicker, {
    props: {
      modelValue: { start: '2026-09-24', end: '2026-09-30' },
      ...props,
    },
    global: {
      plugins: [i18n],
      stubs: { ElPopover: ElPopoverStub, ElDatePicker: ElDatePickerStub },
    },
  })
}

describe('KxDateRangePicker', () => {
  it('触发器显示预设标签 + 短范围', () => {
    const w = mountPicker()
    const label = w.get('.kx-dr-trigger__label').text()
    expect(label).toContain('近 7 天')
    expect(label).toContain('09/24 – 09/30')
  })

  it('非匹配预设时显示自定义 + 范围', () => {
    const w = mountPicker({ modelValue: { start: '2026-09-01', end: '2026-09-15' } })
    expect(w.get('.kx-dr-trigger__label').text()).toContain('自定义')
  })

  it('默认渲染 8 项预设中的 7 项（date 精度无 last24h）', () => {
    const w = mountPicker()
    expect(w.findAll('.kx-dr-preset')).toHaveLength(7)
  })

  it('预设点击回填草稿，应用后提交并校验映射', async () => {
    const w = mountPicker()
    const presets = w.findAll('.kx-dr-preset')
    const last30 = presets.find((b) => b.text() === '近 30 天')!
    await last30.trigger('click')
    const stubs = w.findAll('.dp-stub')
    expect((stubs[0].element as HTMLInputElement).value).not.toBe('')
    await w.get('.kx-dr-apply').trigger('click')
    const apply = w.emitted('apply')!
    expect(apply).toHaveLength(1)
    expect(rangeSpanDays(apply[0][0] as { start: string; end: string })).toBe(30)
    expect(w.emitted('update:modelValue')).toHaveLength(1)
  })

  it('结束早于开始：应用禁用并提示', async () => {
    const w = mountPicker()
    const stubs = w.findAll('.dp-stub')
    await stubs[0].setValue('2026-10-05')
    await stubs[1].setValue('2026-10-01')
    expect(w.get('.kx-dr-apply').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('结束需不早于开始')
  })

  it('maxSpanDays 超限禁用并提示', async () => {
    const w = mountPicker({ maxSpanDays: 7 })
    const stubs = w.findAll('.dp-stub')
    await stubs[0].setValue('2026-08-01')
    await stubs[1].setValue('2026-09-30')
    expect(w.get('.kx-dr-apply').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('跨度超过 7 天')
  })

  it('instant 模式：预设点击立即提交且无应用按钮', async () => {
    const w = mountPicker({ instant: true })
    expect(w.find('.kx-dr-apply').exists()).toBe(false)
    const presets = w.findAll('.kx-dr-preset')
    await presets.find((b) => b.text() === '今天')!.trigger('click')
    const apply = w.emitted('apply')!
    expect(apply).toHaveLength(1)
    const range = apply[0][0] as { start: string; end: string }
    expect(range.start).toBe(range.end)
  })

  // 2026-09-30 审计 P3-2：instant 分支此前直接 commit，绕过 canApply/maxSpanDays。
  it('未传 notAfter 时今天仍可选，看板预设不受影响', () => {
    const w = mountPicker()
    const disable = w.findAllComponents(ElDatePickerStub)[0].props('disabledDate') as (cell: Date) => boolean
    expect(disable(new Date(2026, 8, 30))).toBe(false)
    expect(isAfterDay('2026-09-30 12:00', '2026-09-30')).toBe(false)
  })

  it('notAfter 拦住今天和未来：日历禁用，应用按钮不可用', async () => {
    const w = mountPicker({
      notAfter: '2026-09-29',
      modelValue: { start: '2026-09-23', end: '2026-09-29' },
    })
    const disable = w.findAllComponents(ElDatePickerStub)[1].props('disabledDate') as (cell: Date) => boolean
    expect(disable(new Date(2026, 8, 30))).toBe(true)
    expect(disable(new Date(2026, 8, 29))).toBe(false)
    expect(calendarDayKey(new Date(2026, 8, 29))).toBe('2026-09-29')
    const stubs = w.findAll('.dp-stub')
    await stubs[0].setValue('2026-09-23')
    await stubs[1].setValue('2026-09-30')
    expect(w.get('.kx-dr-apply').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('不能晚于 2026-09-29')
    await w.get('.kx-dr-apply').trigger('click')
    expect(w.emitted('apply')).toBeUndefined()
    await stubs[1].setValue('2026-09-29')
    expect(w.get('.kx-dr-apply').attributes('disabled')).toBeUndefined()
  })

  it('instant 模式：maxSpanDays 超限预设被 canApply 拦截，限内预设照常提交', async () => {
    const w = mountPicker({ instant: true, maxSpanDays: 3 })
    const presets = w.findAll('.kx-dr-preset')
    await presets.find((b) => b.text() === '近 30 天')!.trigger('click')
    expect(w.emitted('apply')).toBeUndefined()
    await presets.find((b) => b.text() === '今天')!.trigger('click')
    const apply = w.emitted('apply')!
    expect(apply).toHaveLength(1)
  })
})

