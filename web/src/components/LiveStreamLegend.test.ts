import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

async function readLegendSource(): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/components/LiveStreamLegend.vue'),
    resolve(process.cwd(), 'src/components/LiveStreamLegend.vue'),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error('LiveStreamLegend.vue not found')
}

describe('LiveStreamLegend probe icon', () => {
  it('uses the same radar SVG as RequestTile instead of a T badge', async () => {
    const source = await readLegendSource()
    expect(source).toContain('legend-probe-icon')
    expect(source).toContain('viewBox="0 0 16 16"')
    expect(source).not.toMatch(/legend-probe-badge">T</)
  })

  it('documents routing vs llm in-flight stage markers', async () => {
    const source = await readLegendSource()
    expect(source).toContain('legend-stage--routing')
    expect(source).toContain('legend-stage--llm')
    expect(source).toContain('路由中')
    expect(source).toContain('等大模型')
  })
})
