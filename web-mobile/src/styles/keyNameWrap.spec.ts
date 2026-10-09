import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const KEYS_VIEW = resolve(process.cwd(), 'src/views/KeysView.vue')

describe('窄屏密钥名称可辨识', () => {
  const source = readFileSync(KEYS_VIEW, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
  const rule = source.match(/\.key-card__name\s*\{([^}]*)\}/)?.[1]

  it('长名称允许在卡片内换行，不会把不同后缀截成同一个前缀', () => {
    expect(rule).toBeTruthy()
    expect(rule).toMatch(/min-width\s*:\s*0/)
    expect(rule).toMatch(/overflow-wrap\s*:\s*anywhere/)
    expect(rule).not.toMatch(/text-overflow\s*:\s*ellipsis/)
    expect(rule).not.toMatch(/white-space\s*:\s*nowrap/)
  })
})
