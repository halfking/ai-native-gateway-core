// FilterBar.test.ts — 声明式定义渲染、v-model、chips、搜索/清除事件与
// <768 折叠行为（mock useBreakpoint）+ 响应式源码断言。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import FilterBar from './FilterBar.vue'
import KxDateRangePicker from './KxDateRangePicker.vue'
import type { FilterDefinition } from './filter-types'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/FilterBar.vue'), 'utf8')

const bpState = vi.hoisted(() => ({ isMobile: false, isSmall: false }))
vi.mock('../../composables/useBreakpoint', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../composables/useBreakpoint')>()
  const { computed, readonly, ref } = await import('vue')
  return {
    ...actual,
    useBreakpoint: () => ({
      isMobile: readonly(ref(bpState.isMobile)),
      isTablet: readonly(ref(!bpState.isMobile)),
      isSmall: readonly(ref(bpState.isSmall)),
      isDesktop: computed(() => !bpState.isMobile),
    }),
  }
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: {
        button: { filter: '筛选', search: '搜索', clear: '清除' },
        // KxDateRangePicker 触发器词条（daterange 断言用）
        dateRange: { title: '时间范围', custom: '自定义', startDate: '开始日期', endDate: '结束日期' },
      },
    },
  },
})

const defs: FilterDefinition[] = [
  { key: 'keyword', type: 'search', label: '关键词', placeholder: '输入关键词' },
  { key: 'status', type: 'select', label: '状态', options: ['ok', 'bad'], placeholder: '全部' },
  { key: 'time', type: 'daterange', fromKey: 'from', toKey: 'to', label: '时间' },
]

function mountBar(props: Record<string, unknown> = {}) {
  return mount(FilterBar, {
    props: {
      definitions: defs,
      modelValue: { keyword: '', status: '', from: '', to: '' },
      ...props,
    },
    global: { plugins: [i18n] },
  })
}

describe('FilterBar', () => {
  beforeEach(() => {
    bpState.isMobile = false
    bpState.isSmall = false
  })

  it('按 definitions 渲染 search/select/daterange 控件', () => {
    const w = mountBar()
    expect(w.find('input[type="text"]').exists()).toBe(true)
    expect(w.find('select').exists()).toBe(true)
    // 2026-09-30 统一日历轮：daterange 由两个 datetime-local input 收敛为单个
    // KxDateRangePicker（datetime 精度、instant 即时生效、无预设网格）
    const picker = w.findComponent(KxDateRangePicker)
    expect(picker.exists()).toBe(true)
    expect(picker.props('precision')).toBe('datetime')
    expect(picker.props('instant')).toBe(true)
    expect(picker.props('presets')).toEqual([])
    expect(w.find('.kx-dr-trigger').exists()).toBe(true)
    expect(w.text()).toContain('关键词')
  })

  it('daterange：datetime-local ↔ 组件空格分隔边界互转，apply 一次写回两端键', async () => {
    // filters 存 datetime-local 的 'T' 分隔（外部契约不变），组件模型为空格分隔
    const w = mountBar({ modelValue: { keyword: '', status: '', from: '2026-09-01T00:00', to: '2026-09-02T23:30' } })
    const picker = w.findComponent(KxDateRangePicker)
    expect(picker.props('modelValue')).toEqual({ start: '2026-09-01 00:00', end: '2026-09-02 23:30' })
    // 触发器标签展示两端（shortRangeLabel：MM/DD HH:mm）
    expect(w.find('.kx-dr-trigger__label').text()).toContain('09/01 00:00 – 09/02 23:30')

    // 组件 apply（空格分隔）→ 同一次 update:modelValue 写回两个 'T' 分隔键
    await picker.vm.$emit('apply', { start: '2026-09-03 08:00', end: '2026-09-04 09:30' })
    const events = w.emitted('update:modelValue')!
    expect(events[events.length - 1][0]).toEqual({ keyword: '', status: '', from: '2026-09-03T08:00', to: '2026-09-04T09:30' })
  })

  it('daterange：任一端为空时组件收 null（半选不整体生效，两端齐备才有效）', () => {
    const w = mountBar({ modelValue: { keyword: '', status: '', from: '2026-09-01T00:00', to: '' } })
    expect(w.findComponent(KxDateRangePicker).props('modelValue')).toBe(null)
    // 半选端仍以 chip 呈现（filters 键值未被丢弃；P3-5 后 chip 标签回退 def.label）
    expect(w.find('.active-filter-chip').text()).toContain('时间: 2026-09-01T00:00')
  })

  it('输入更新 v-model:filters（不可变更新）', async () => {
    const w = mountBar()
    await w.find('input[type="text"]').setValue('gpt-4')
    const events = w.emitted('update:modelValue')!
    expect(events[events.length - 1][0]).toEqual({ keyword: 'gpt-4', status: '', from: '', to: '' })
  })

  it('search 类型提供 suggestions 时渲染 FilterInput 自动补全', () => {
    const w = mountBar({ definitions: [{ key: 'kw', type: 'search', suggestions: ['a', 'b'] }] })
    expect(w.find('.filter-input').exists()).toBe(true)
  })

  it('搜索按钮派发 search；有已生效条件时显示清除按钮', async () => {
    const w = mountBar({ modelValue: { keyword: 'x', status: 'ok', from: '', to: '' } })
    await w.find('.filter-bar__search').trigger('click')
    expect(w.emitted('search')).toHaveLength(1)

    expect(w.find('.active-filter-chip').exists()).toBe(true)
    const clearBtn = w.findAll('button').find((b) => b.text().includes('清除'))!
    await clearBtn.trigger('click')
    const events = w.emitted('update:modelValue')!
    expect(events[events.length - 1][0]).toEqual({ keyword: '', status: '', from: '', to: '' })
    expect(w.emitted('clear')).toHaveLength(1)
  })

  it('chips 展示非空条件，点击 chip 清除对应键', async () => {
    const w = mountBar({ modelValue: { keyword: 'gpt', status: '', from: '', to: '' } })
    const chip = w.find('.active-filter-chip')
    expect(chip.text()).toContain('关键词: gpt')
    await chip.trigger('click')
    const events = w.emitted('update:modelValue')!
    expect((events[events.length - 1][0] as Record<string, string>).keyword).toBe('')
  })

  it('<768（isSmall）：默认折叠为「筛选（N）」，展开后可见面板', async () => {
    bpState.isMobile = true
    bpState.isSmall = true
    const w = mountBar({ modelValue: { keyword: 'x', status: 'ok', from: '', to: '' } })
    const toggle = w.find('.filter-bar__toggle')
    expect(toggle.exists()).toBe(true)
    expect(toggle.text()).toContain('筛选（2）')
    expect(w.find('.filter-bar__panel').exists()).toBe(false)

    await toggle.trigger('click')
    expect(w.find('.filter-bar__panel').exists()).toBe(true)
  })

  it('>=768：无折叠入口，面板直接可见', () => {
    const w = mountBar()
    expect(w.find('.filter-bar__toggle').exists()).toBe(false)
    expect(w.find('.filter-bar__panel').exists()).toBe(true)
  })

  it('响应式源码断言：1024 白名单档纵向堆叠+全宽搜索、isSmall 折叠逻辑', () => {
    // 2026-09-13 审计修正：堆叠断点收敛到白名单值 1024；769 死规则已移除
    expect(source).toMatch(/@media \(max-width: 1024px\)/)
    const stackBlock = source.split('@media (max-width: 1024px)')[1] ?? ''
    expect(stackBlock).toContain('flex-direction: column')
    expect(stackBlock).toMatch(/\.filter-bar__search\s*{[^}]*width:\s*100%/s)
    expect(source).not.toMatch(/max-width:\s*1023px/)
    expect(source).not.toMatch(/min-width:\s*769px/)
    expect(source).toContain('isSmall')
    expect(source).toContain('isMobile')
  })
})
