import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

async function readViewSource(): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/views/TenantDashboardView.vue'),
    resolve(process.cwd(), 'src/views/TenantDashboardView.vue'),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error('TenantDashboardView.vue not found in candidate paths')
}

describe('tenant dashboard degraded-mode contract', () => {
  it('exposes degraded markers via MaasUsageSummary type', async () => {
    const maasTs = await readFile(
      resolve(process.cwd(), 'web/src/api/maas.ts'),
      'utf8',
    ).catch(() => readFile(resolve(process.cwd(), 'src/api/maas.ts'), 'utf8'))
    expect(maasTs).toContain('export interface MaasUsageSummary')
    expect(maasTs).toMatch(/MaasUsageSummary[\s\S]*?degraded\?:\s*boolean/)
    expect(maasTs).toMatch(/MaasUsageSummary[\s\S]*?missing_view\?:\s*string/)
    expect(maasTs).toMatch(/MaasUsageSummary[\s\S]*?hint\?:\s*string/)
  })

  it('computes degradedHint from summary.degraded', async () => {
    const source = await readViewSource()
    expect(source).toMatch(/const degradedHint = computed/)
    expect(source).toMatch(/summary\.value\??\.degraded/)
    expect(source).toMatch(/summary\.value\??\.hint|summary\.value\??\.missing_view/)
    expect(source).toMatch(/summary\.value\??\.missing_view/)
  })

  it('renders the degraded hint as a non-blocking info banner', async () => {
    const source = await readViewSource()
    expect(source).toContain('tenant-dashboard-degraded-hint')
    expect(source).toMatch(/<div[^>]*class="alert alert-info"/)
    expect(source).toContain('alert-meta')
  })

  it('places the degraded banner BEFORE the alert-danger so the layout stays intact', async () => {
    const source = await readViewSource()
    const infoIdx = source.indexOf('alert alert-info')
    const dangerIdx = source.indexOf('alert alert-danger')
    expect(infoIdx).toBeGreaterThan(-1)
    expect(dangerIdx).toBeGreaterThan(-1)
    expect(infoIdx).toBeLessThan(dangerIdx)
  })
})