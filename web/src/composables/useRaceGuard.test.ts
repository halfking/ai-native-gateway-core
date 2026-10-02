import { describe, expect, it } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import { useRaceGuard, type RaceGuard } from './useRaceGuard'

describe('useRaceGuard', () => {
  it('begin 递增代际；旧代 stale、当代 current', () => {
    const race = useRaceGuard()
    const g1 = race.begin()
    expect(race.current(g1)).toBe(true)
    const g2 = race.begin()
    expect(race.stale(g1)).toBe(true)
    expect(race.current(g2)).toBe(true)
    expect(race.stale(g2)).toBe(false)
  })

  it('非组件环境可用（无卸载钩子，退化为纯代际守卫）', () => {
    const race = useRaceGuard()
    const g1 = race.begin()
    expect(race.current(g1)).toBe(true)
    const g2 = race.begin()
    expect(race.current(g2)).toBe(true)
    expect(race.stale(g1)).toBe(true)
    expect(race.stale(g2)).toBe(false)
  })

  it('代际不回绕复用', () => {
    const race = useRaceGuard()
    const gens = [race.begin(), race.begin(), race.begin(), race.begin()]
    expect(new Set(gens).size).toBe(4)
    for (const g of gens.slice(0, 3)) expect(race.stale(g)).toBe(true)
    expect(race.current(gens[3])).toBe(true)
  })

  it('组件卸载后一切代际 stale（in-flight 响应作废）', () => {
    let race: RaceGuard | null = null
    const wrapper = mount(
      defineComponent({
        setup() {
          race = useRaceGuard()
          return () => h('div')
        },
      }),
    )
    expect(race).not.toBeNull()
    const g = race!.begin()
    expect(race!.current(g)).toBe(true)
    wrapper.unmount()
    expect(race!.stale(g)).toBe(true)
    expect(race!.current(g)).toBe(false)
    expect(race!.stale(race!.begin())).toBe(true)
  })
})
