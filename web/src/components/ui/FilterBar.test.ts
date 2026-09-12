// FilterBar.test.ts — 声明式定义渲染、v-model、chips、搜索/清除事件与
// <768 折叠行为（mock useBreakpoint）+ 响应式源码断言。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import FilterBar from './FilterBar.vue'
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
      common: { button: { filter: '筛选', search: '搜索', clear: '清除' } },
    },
  },
})

const defs: FilterDefinition[] = [
  { key: 'keyword', type: 'search', label: '关键词', placeholder: '输入关键词' },
  { key: 'status', type: 'select', label: '状态', options: ['ok', 'bad'], placeholder: '全部' },
  { key: 'time', type: 'daterange', fromKey: 'from', toKey: 'to', fromLabel: '开始', toLabel: '结束' },
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
    expect(w.findAll('input[type="datetime-local"]').length).toBe(2)
    expect(w.text()).toContain('关键词')
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

  it('响应式源码断言：1023 纵向堆叠+全宽搜索、769 隐藏折叠入口、isSmall 折叠逻辑', () => {
    expect(source).toMatch(/@media \(max-width: 1023px\)/)
    const stackBlock = source.split('@media (max-width: 1023px)')[1] ?? ''
    expect(stackBlock).toContain('flex-direction: column')
    expect(stackBlock).toMatch(/\.filter-bar__search\s*{[^}]*width:\s*100%/s)
    expect(source).toMatch(/@media \(min-width: 769px\)/)
    expect(source).toContain('isSmall')
    expect(source).toContain('isMobile')
  })
})
