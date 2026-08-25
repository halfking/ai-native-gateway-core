import { describe, expect, it } from 'vitest'
import {
  extractMessagesFromBody,
  filterMessages,
  previewText,
} from './messageHelpers'

describe('messageHelpers', () => {
  it('extracts messages array from chat body', () => {
    const msgs = extractMessagesFromBody({
      messages: [
        { role: 'system', content: 's' },
        { role: 'user', content: 'u' },
      ],
    })
    expect(msgs).toHaveLength(2)
    expect(filterMessages(msgs, 'user')).toHaveLength(1)
  })

  it('previews long text with truncation', () => {
    const long = 'a\nb\nc\nd\ne'
    const p = previewText(long, 3)
    expect(p.truncated).toBe(true)
    expect(p.text.split('\n')).toHaveLength(3)
  })
})
