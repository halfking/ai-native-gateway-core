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
    // ★ 2026-10-27 第 23 增量：断言从「字面 18px」改成「**降档语义**」——
    //   小屏值必须（a）小于桌面基准 `--kx-text-2xl` = 24px，（b）落在 DESIGN.md §4 的角色阶上。
    //   写死 18px 会在字号阶调整时假红；「小于 24 且在阶上」才是这条断言真正的意图。
    const m = mobileBlock.match(/font-size:\s*(\d+(?:\.\d+)?)px/)
    expect(m).not.toBeNull()
    const mobilePx = Number(m![1])
    expect(mobilePx).toBeLessThan(24)
    expect([11, 12, 13, 14, 16, 20, 24, 30, 38]).toContain(mobilePx)
    // ★ 2026-10-25：这里原来断言 source 含 'border-inline-start'，
    //   注释写「tone 色条使用逻辑属性，RTL 下自动镜像」。
    //   ★ 但那条彩色侧边条本身是 craft-floor 的**硬禁项**（卡片上 >1px 的
    //   彩色 border-left/right）⇒ 旧断言在要求一个被禁的形态，必然红。
    //   现形态是「形状 + 颜色」双通道：tone 标记改成标签前的**状态点**，
    //   色盲也能看出这张卡带标记；圆点不涉及方向，旧断言关心的 RTL 镜像
    //   随之消失（新的断言改为反向钉住「不要再长出侧边条」）。
    expect(source).toContain('.app-stat-card__label::before')
    expect(source).not.toContain('border-inline-start')
  })
})
