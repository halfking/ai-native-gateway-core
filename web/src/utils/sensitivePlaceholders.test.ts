import { describe, expect, it } from 'vitest'
import {
  extractSensitivePlaceholders,
  splitSensitivePlaceholders,
  truncateForPreview,
} from './sensitivePlaceholders'

describe('sensitivePlaceholders', () => {
  it('extracts unique placeholders', () => {
    const text = 'a {SENSITIVE:phone:1} b {SENSITIVE:email:2} c {SENSITIVE:phone:1}'
    expect(extractSensitivePlaceholders(text)).toEqual([
      { placeholder: '{SENSITIVE:phone:1}', type: 'phone', index: 1 },
      { placeholder: '{SENSITIVE:email:2}', type: 'email', index: 2 },
    ])
  })

  it('splits for highlight', () => {
    const parts = splitSensitivePlaceholders('hi {SENSITIVE:phone:1}!')
    expect(parts.map((p) => p.kind)).toEqual(['text', 'ph', 'text'])
    expect(parts[1].value).toBe('{SENSITIVE:phone:1}')
  })

  it('truncates large previews', () => {
    const big = 'x'.repeat(300_000)
    const r = truncateForPreview(big, 1000)
    expect(r.truncated).toBe(true)
    expect(r.text.length).toBeLessThan(1200)
  })
})
