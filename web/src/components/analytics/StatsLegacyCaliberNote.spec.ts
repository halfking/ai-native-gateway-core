// StatsLegacyCaliberNote.spec.ts —— N1「一个面板里两把尺」的披露标记行为门。
//
// ★ 背景（缺陷 N1）：会话统计面板一次拉 7 个端点，前端把 include_probes 发给
//   全部，但后端**只在 session-overview 上认它** ⇒ session-trend /
//   session-health / errors / performance 仍是旧宇宙 ⇒ 面板顶部的「含探测」
//   徽标亮了（徽标读的是总览响应的 probe_scope），而趋势 / 健康度 / 错误 /
//   性能那几块的数字还是另一把尺 ⇒ 一个面板里两把尺，且**没有任何提示**
//   告诉读者哪个是哪个。
//
// ★ 本门断「标记何时出现」：**只看响应里的 probe_scope**，绝不看本地开关。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { testI18n } from '../../test-utils/i18n'
import StatsLegacyCaliberNote from './StatsLegacyCaliberNote.vue'

function factory(probeScope?: string | null) {
  return mount(StatsLegacyCaliberNote, {
    props: { probeScope },
    global: { plugins: [testI18n] },
  })
}

describe('N1 · 「旧口径」披露标记必须跟响应走', () => {
  it('① probe_scope=included ⇒ 标记出现，且带标签与标题', () => {
    const w = factory('included')
    const note = w.find('[data-testid="stats-legacy-caliber-note"]')
    expect(note.exists(), '含探测时必须给出旧口径标记').toBe(true)
    expect(note.text()).toBe('旧口径')
    expect(note.attributes('title')).toBeTruthy()
  })

  it('② probe_scope=degraded_included ⇒ 同样出现（降级也是分叉）', () => {
    expect(factory('degraded_included').find('[data-testid="stats-legacy-caliber-note"]').exists()).toBe(true)
  })

  it('③ probe_scope=excluded ⇒ 不出现（两把尺一致时不制造噪音）', () => {
    expect(factory('excluded').find('[data-testid="stats-legacy-caliber-note"]').exists()).toBe(false)
  })

  it('④ probe_scope 缺省/未知 ⇒ 不出现', () => {
    expect(factory(undefined).find('[data-testid="stats-legacy-caliber-note"]').exists()).toBe(false)
    expect(factory(null).find('[data-testid="stats-legacy-caliber-note"]').exists()).toBe(false)
  })

  it('⑤ 标题必须说清「哪些块」与「不能直接与 KPI 对比」', () => {
    const title = factory('included').find('[data-testid="stats-legacy-caliber-note"]').attributes('title') ?? ''
    expect(title).toContain('趋势')
    expect(title).toContain('健康度')
    expect(title).toContain('错误')
    expect(title).toContain('性能')
    expect(title).toContain('KPI')
  })
})
