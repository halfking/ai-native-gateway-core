/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

async function readViewSource(): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/views/TenantDetailView.vue'),
    resolve(process.cwd(), 'src/views/TenantDetailView.vue'),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      // try the next candidate (running tests from repo root vs web/)
    }
  }
  throw new Error('TenantDetailView.vue not found in candidate paths')
}

describe('tenant billing audit contract', () => {
  it('uses the provider, credential, model, token, and margin dimensions', async () => {
    const source = await readViewSource()
    expect(source).toContain("activeTab === 'billing'")
    expect(source).toContain('row.credential_label')
    expect(source).toContain('row.cache_read_tokens')
    expect(source).toContain('row.gross_margin_rate')
    expect(source).toContain('billingOwnerUser')
  })

  it('clears per-tenant state when navigating to a different tenant', async () => {
    const source = await readViewSource()
    expect(source).toMatch(/resetTenantScopedState\(\)/)
    expect(source).toContain('billing.value = null')
  })

  it('refreshes billing data when filters change', async () => {
    const source = await readViewSource()
    expect(source).toContain('onBillingFilterChange')
    expect(source).toMatch(/onBillingFilterChange[\s\S]*activeTab\.value === 'billing'/)
  })
})
