// StatCard.test.ts — 结构 class、tone/compact 变体、插槽优先级与响应式源码断言。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import StatCard from './StatCard.vue'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/StatCard.vue'), 'utf8')

describe('StatCard', () => {
  it('renders label/value/sub in the global .stat-card structure', () => {
    const w = mount(StatCard, { props: { label: '总请求数', value: 1234, sub: '近 24h' } })
    expect(w.find('.stat-card').exists()).toBe(true)
    expect(w.find('.app-stat-card__label').text()).toBe('总请求数')
    expect(w.find('.app-stat-card__value').text()).toBe('1234')
    expect(w.find('.app-stat-card__sub').text()).toBe('近 24h')
  })

  it('omits the sub row when no sub prop and no sub slot', () => {
    const w = mount(StatCard, { props: { label: 'l', value: 1 } })
    expect(w.find('.app-stat-card__sub').exists()).toBe(false)
  })

  it('applies the compact modifier class', () => {
    const w = mount(StatCard, { props: { label: 'l', compact: true } })
    expect(w.find('.stat-card--compact').exists()).toBe(true)
  })

  it.each([
    ['success', 'app-stat-card--success'],
    ['warning', 'app-stat-card--warning'],
    ['danger', 'app-stat-card--danger'],
  ] as const)('applies the %s tone class', (tone, cls) => {
    const w = mount(StatCard, { props: { label: 'l', tone } })
    expect(w.find(`.${cls}`).exists()).toBe(true)
  })

  it('neutral tone adds no tone class', () => {
    const w = mount(StatCard, { props: { label: 'l', tone: 'neutral' } })
    expect(w.find('.app-stat-card--success').exists()).toBe(false)
    expect(w.find('.app-stat-card--warning').exists()).toBe(false)
    expect(w.find('.app-stat-card--danger').exists()).toBe(false)
  })

  it('prefers the #value slot over the value prop', () => {
    const w = mount(StatCard, {
      props: { label: 'l', value: 'prop' },
      slots: { value: '<strong>slot</strong>' },
    })
    expect(w.find('.app-stat-card__value').text()).toBe('slot')
  })

  it('keeps the small-screen value scale-down under 480px (responsive source assertion)', () => {
    expect(source).toMatch(/@media \(max-width: 480px\)/)
    const mobileBlock = source.split('@media (max-width: 480px)')[1] ?? ''
    expect(mobileBlock).toContain('font-size: 18px')
    // tone 色条使用逻辑属性，RTL 下自动镜像
    expect(source).toContain('border-inline-start')
  })
})
