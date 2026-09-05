import { describe, expect, it } from 'vitest'
import {
  deriveConversationTurns,
  extractAssistantReply,
  extractLastUserPrompt,
  firstUserPrompt,
  extractMessagesFromBody,
  filterMessages,
  lastUserPrompt,
  pickTurnRequestMessages,
  previewText,
  splitTurnMessages,
  summarizeTurnUserInstruction,
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

  it('extracts the last user prompt', () => {
    expect(lastUserPrompt({
      messages: [
        { role: 'user', content: 'first' },
        { role: 'assistant', content: 'mid' },
        { role: 'user', content: [{ type: 'text', text: 'latest ask' }] },
      ],
    })).toBe('latest ask')
    expect(lastUserPrompt({ messages: [{ role: 'assistant', content: 'no user' }] })).toBe('')
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

  it('pickTurnRequestMessages prefers outbound body over request_delta', () => {
    // request_delta is incremental; only this turn's user/tool messages.
    // outbound_body is the full prompt; system + history + new user.
    const requestDelta = { messages: [{ role: 'user', content: 'new ask' }] }
    const outboundBody = {
      messages: [
        { role: 'system', content: 'sys' },
        { role: 'user', content: 'earlier' },
        { role: 'assistant', content: 'earlier reply' },
        { role: 'user', content: 'new ask' },
      ],
    }
    const msgs = pickTurnRequestMessages(requestDelta, outboundBody)
    // Outbound wins; system + history survive.
    expect(msgs.map((m) => m.role)).toEqual(['system', 'user', 'assistant', 'user'])
  })

  it('pickTurnRequestMessages falls back to request_delta when outbound empty', () => {
    const requestDelta = { messages: [{ role: 'user', content: 'only delta' }] }
    const msgs = pickTurnRequestMessages(requestDelta, null)
    expect(msgs).toHaveLength(1)
    expect(msgs[0]).toEqual({ role: 'user', content: 'only delta' })
  })

  it('splitTurnMessages keeps request and response separate', () => {
    const requestDelta = { messages: [{ role: 'user', content: 'q1' }] }
    const responseDelta = { choices: [{ message: { role: 'assistant', content: 'r1' } }] }
    const outboundBody = null
    const { requestMessages, responseMessages } = splitTurnMessages(
      requestDelta, responseDelta, outboundBody,
    )
    expect(requestMessages.map((m) => m.role)).toEqual(['user'])
    expect(responseMessages).toHaveLength(1)
    expect(String(responseMessages[0].role)).toBe('assistant')
  })

  it('summarizeTurnUserInstruction picks last user from outbound for current turn', () => {
    const requestDelta = { messages: [{ role: 'user', content: 'earlier ask' }] }
    const outboundBody = {
      messages: [
        { role: 'system', content: 'sys' },
        { role: 'user', content: 'earlier ask' },
        { role: 'assistant', content: 'reply' },
        { role: 'user', content: 'latest question' },
      ],
    }
    const summary = summarizeTurnUserInstruction(requestDelta, outboundBody)
    expect(summary.full).toBe('latest question')
  })

  it('summarizeTurnUserInstruction falls back to first user when outbound missing', () => {
    const requestDelta = { messages: [{ role: 'user', content: 'delta only' }] }
    const summary = summarizeTurnUserInstruction(requestDelta, null)
    expect(summary.full).toBe('delta only')
  })
})
