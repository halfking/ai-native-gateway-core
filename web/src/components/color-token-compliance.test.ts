import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const targets = [
  'src/components/LiveRequestStreamV2.vue',
  'src/components/SystemStatusIndicator.vue',
  'src/components/shell/AppTopbar.vue',
  'src/components/shell/LifecycleShell.vue',
  'src/components/shell/UserMenuDropdown.vue',
]
const styleSource = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')

describe('priority component color tokens', () => {
  it.each(targets)('%s contains no hardcoded colors', (file) => {
    const source = readFileSync(resolve(process.cwd(), file), 'utf8')

    expect(source).not.toMatch(/#[0-9a-f]{3,8}\b/i)
    expect(source).not.toMatch(/rgba?\s*\(/i)
  })

  it('bridges soft status tokens in both themes', () => {
    for (const token of ['success-soft', 'warning-soft', 'danger-soft']) {
      expect(styleSource.match(new RegExp(`--${token}: var\\(--kx-${token}\\)`, 'g'))).toHaveLength(2)
    }
  })
})
