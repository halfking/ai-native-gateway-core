import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

describe('tenant billing audit contract', () => {
  it('uses the provider, credential, model, token, and margin dimensions', async () => {
    const source = await readFile(resolve(process.cwd(), 'web/src/views/TenantDetailView.vue'), 'utf8').catch(() =>
      readFile(resolve(process.cwd(), 'src/views/TenantDetailView.vue'), 'utf8'))
    expect(source).toContain("activeTab === 'billing'")
    expect(source).toContain('row.credential_label')
    expect(source).toContain('row.cache_read_tokens')
    expect(source).toContain('row.gross_margin_rate')
    expect(source).toContain('billingOwnerUser')
  })
})
