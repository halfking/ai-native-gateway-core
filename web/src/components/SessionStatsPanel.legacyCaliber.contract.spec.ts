// SessionStatsPanel.legacyCaliber.contract.spec.ts —— N1「一个面板里两把尺」的源码契约门。
//
// ★ 为什么用源码契约而不是 mount：面板依赖 i18n / router / element-plus /
//   echarts，mount 成本高且与真实浏览器行为无关；本门钉的是**代码形状**
//   （标记挂在哪几块、判据取自哪里），与同族做法一致
//   （components/SessionStatsPanel.probeScopeReload.test.ts 同族）。
//   ⇒ 渲染层的行为由 components/analytics/StatsLegacyCaliberNote.spec.ts 断。
//
// ★ 判据三条：
//   ① 趋势 / 健康度 / 错误 / 性能这四块必须挂上「旧口径」标记；
//   ② 判据必须来自**响应**的 probe_scope（不得看本地 includeProbes 开关）；
//   ③ 面板自己的「含探测」徽标也是同一个真源 ⇒ 两处不得分叉。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/${p}`),
  resolve(process.cwd(), `src/${p}`),
]

async function read(rel: string): Promise<string> {
  for (const c of CANDS(rel)) {
    try {
      return await readFile(c, 'utf8')
    } catch {
      /* next */
    }
  }
  throw new Error(`${rel} not found`)
}

describe('N1 · 会话统计面板必须给「旧口径」那几块挂披露标记', () => {
  it('① 面板必须引入并渲染 StatsLegacyCaliberNote', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    expect(src).toContain("import StatsLegacyCaliberNote from './analytics/StatsLegacyCaliberNote.vue'")
    expect(src).toContain('<StatsLegacyCaliberNote')
  })

  it('② 标记必须挂在趋势/健康度图与延迟/错误概览这两组之前（各一处）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const notes = [...src.matchAll(/<StatsLegacyCaliberNote[^>]*>/g)].map((m) => m[0])
    expect(notes.length, '标记至少要挂两处（图表组 + 信号组）').toBeGreaterThanOrEqual(2)
    for (const n of notes) {
      // ★ 判据取自**响应**：overview.probe_scope，不是本地 includeProbes 开关。
      expect(n).toContain(':probe-scope="overview?.probe_scope"')
      expect(n, '标记不得改看本地开关').not.toContain('includeProbes')
    }
    // 位置：趋势图之前、信号组之前
    const chartsIdx = src.indexOf('<div class="charts-row">')
    const signalsIdx = src.indexOf('<SessionStatsSignals')
    expect(chartsIdx).toBeGreaterThan(-1)
    expect(signalsIdx).toBeGreaterThan(-1)
    const noteIdxs = [...src.matchAll(/<StatsLegacyCaliberNote/g)].map((m) => m.index)
    expect(noteIdxs.some((i) => i < chartsIdx), '趋势/健康度那组之前必须有标记').toBe(true)
    expect(noteIdxs.some((i) => i < signalsIdx && i > chartsIdx), '延迟/错误那组之前必须有标记').toBe(true)
  })

  it('③ 面板自己的「含探测」徽标也必须以 probe_scope 为唯一真源', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    expect(src).toContain('isProbeScopeIncluded(overview.value?.probe_scope)')
    expect(src).toContain("import { isProbeScopeIncluded } from './analytics/statsLegacyCaliber'")
    // 徽标本身仍然亮在 includeProbesActive 上（两处同源 ⇒ 不会互相打脸）。
    expect(src).toMatch(/v-if="includeProbesActive"/)
  })

  it('④ 判定口径必须是「响应说含探测」，不得看本地开关', async () => {
    const src = await read('components/analytics/statsLegacyCaliber.ts')
    expect(src).toContain("probeScope === 'included'")
    expect(src).toContain("probeScope === 'degraded_included'")
    // ★ 判据函数里不得出现本地开关的取名（注释里提到它是可以的，
    //   那是说明「为什么不看它」——只看可执行代码段）。
    const code = src
      .split('\n')
      .filter((l) => !l.trimStart().startsWith('//'))
      .join('\n')
    expect(code).not.toContain('includeProbes')
  })
})
