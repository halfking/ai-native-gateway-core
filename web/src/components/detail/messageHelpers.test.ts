import { describe, expect, it } from 'vitest'
import {
  deriveConversationTurns,
  extractAssistantReply,
  extractLastUserPrompt,
  firstUserPrompt,
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

  it('extracts last user prompt and assistant reply', () => {
    expect(extractLastUserPrompt({
      messages: [
        { role: 'user', content: 'first' },
        { role: 'assistant', content: 'mid' },
        { role: 'user', content: [{ type: 'text', text: 'latest ask' }] },
      ],
    })).toBe('latest ask')
    expect(extractAssistantReply({
      choices: [{ message: { role: 'assistant', content: 'hello world' } }],
    })).toBe('hello world')
  })

  it('derives turns by splitting on each user message', () => {
    const turns = deriveConversationTurns([
      { role: 'system', content: 'sys' },
      { role: 'user', content: 'ask one' },
      { role: 'assistant', content: 'reply one' },
      { role: 'user', content: 'ask two' },
      { role: 'assistant', content: 'reply two' },
    ])
    expect(turns).toHaveLength(2)
    expect(turns[0].number).toBe(1)
    expect(turns[0].userPreview).toBe('ask one')
    expect(turns[0].assistantCount).toBe(1)
    // leading system is attached to the first user turn
    expect(turns[0].messages[0]).toEqual({ role: 'system', content: 'sys' })
    expect(turns[1].userPreview).toBe('ask two')
    expect(turns[1].messages).toHaveLength(2)
  })

  it('drops system-only blocks with no user message', () => {
    const turns = deriveConversationTurns([
      { role: 'system', content: 'only sys' },
    ])
    expect(turns).toHaveLength(0)
  })

  it('extracts the first user prompt', () => {
    expect(firstUserPrompt({
      messages: [
        { role: 'system', content: 'sys' },
        { role: 'assistant', content: 'hi' },
        { role: 'user', content: [{ type: 'text', text: 'first ask' }] },
        { role: 'user', content: 'second ask' },
      ],
    })).toBe('first ask')
    expect(firstUserPrompt({ messages: [{ role: 'assistant', content: 'no user' }] })).toBe('')
  })

  it('truncates long user previews', () => {
    const big = Array.from({ length: 12 }, (_, i) => `line ${i}`).join('\n')
    const turns = deriveConversationTurns([
      { role: 'user', content: big },
      { role: 'assistant', content: 'ok' },
    ])
    expect(turns).toHaveLength(1)
    expect(turns[0].truncated).toBe(true)
    expect(turns[0].userPreview.split('\n')).toHaveLength(6)
    expect(turns[0].userPreviewFull).toBe(big)
  })
})
