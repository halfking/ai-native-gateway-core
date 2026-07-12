import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

async function readViewSource(filename: string): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/views', filename),
    resolve(process.cwd(), 'src/views', filename),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error(`${filename} not found in candidate paths`)
}

describe('dashboard degraded-mode contract', () => {
  it('exposes degraded markers in the UsageSummary type', async () => {
    const source = await readViewSource('DashboardViewV2.vue')
    // The dashboard view must compute a `degradedHint` and a `degradedView`
    // when summary or overview reports `degraded: true`.
    expect(source).toMatch(/degradedHint/)
    expect(source).toMatch(/degradedView/)
    expect(source).toMatch(/summary\.value\?\.hint/)
    expect(source).toMatch(/overview\.value\?\.hint/)
  })

  it('renders the degraded hint as a non-blocking info banner', async () => {
    const source = await readViewSource('DashboardViewV2.vue')
    expect(source).toContain('dashboard-degraded-hint')
    expect(source).toMatch(/<div[^>]*class="alert alert-info"/)
    // The degraded hint must come BEFORE the destructive alert-danger
    // so the operator sees the friendly explanation first.
    const infoIdx = source.indexOf('alert alert-info')
    const dangerIdx = source.indexOf('alert alert-danger')
    expect(infoIdx).toBeGreaterThan(-1)
    expect(dangerIdx).toBeGreaterThan(-1)
    expect(infoIdx).toBeLessThan(dangerIdx)
  })

  it('updates the UsageSummary type to include degradation fields', async () => {
    const source = await readFile(
      resolve(process.cwd(), 'web/src/api/usage.ts').toString(),
      'utf8',
    ).catch(() => readFile(resolve(process.cwd(), 'src/api/usage.ts'), 'utf8'))
    expect(source).toContain('degraded?:')
    expect(source).toContain('missing_view?:')
    expect(source).toContain('error_code?:')
    expect(source).toContain('hint?:')
  })
})