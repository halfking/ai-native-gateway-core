import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(process.cwd(), 'src/components/shell/AppTopbar.vue'), 'utf8')

describe('AppTopbar responsive navigation', () => {
  it('contains wide mobile navigation without overflowing the page', () => {
    const mobileStyles = source.slice(source.indexOf('@media (max-width: 768px)'))

    expect(mobileStyles).toMatch(/\.app-topbar__nav\s*{[^}]*overflow-x:\s*auto/s)
    expect(mobileStyles).toMatch(/\.app-topbar__nav\s*{[^}]*flex-wrap:\s*nowrap/s)
    expect(mobileStyles).toMatch(/\.app-topbar__link\s*{[^}]*flex:\s*0 0 auto/s)
  })

  it('keeps essential actions visible on narrow screens', () => {
    const narrowStyles = source.slice(source.indexOf('@media (max-width: 480px)'))

    expect(narrowStyles).toMatch(/\.app-topbar__brand-text,\s*\.app-topbar__meta\s*{[^}]*display:\s*none/s)
    expect(narrowStyles).not.toMatch(/\.app-topbar__actions\s*{[^}]*display:\s*none/s)
  })

  it('does not duplicate the API version prefix', () => {
    expect(source).toContain('<span class="app-topbar__version-tag">{{ props.versionInfo.version }}</span>')
    expect(source).not.toContain('<span class="app-topbar__version-tag">v{{ props.versionInfo.version }}</span>')
  })
})
