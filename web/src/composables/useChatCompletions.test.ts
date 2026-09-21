import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import {
  buildChatCompletionBody,
  messagesForApi,
  type ChatCompletionOptions,
} from './useChatCompletions'

describe('buildChatCompletionBody', () => {
  const base: ChatCompletionOptions = {
    apiKey: 'sk-test',
    model: 'auto',
    messages: [{ role: 'user', content: 'hi' }],
    taskId: 'task-1',
    gwSessionId: 'sid-1',
  }

  it('defaults to stream true and max_tokens 2048', () => {
    const body = buildChatCompletionBody(base, 'sid-1')
    expect(body.stream).toBe(true)
    expect(body.max_tokens).toBe(2048)
    expect(body.model).toBe('auto')
  })

  it('sets stream false for chat mode', () => {
    const body = buildChatCompletionBody({ ...base, stream: false }, 'sid-1')
    expect(body.stream).toBe(false)
  })

  it('injects system prompt ahead of messages', () => {
    const body = buildChatCompletionBody(
      { ...base, systemPrompt: '  Be terse.  ' },
      'sid-1',
    )
    const msgs = body.messages as Array<{ role: string; content: string }>
    expect(msgs[0]).toEqual({ role: 'system', content: 'Be terse.' })
    expect(msgs[1]).toEqual({ role: 'user', content: 'hi' })
  })

  it('passes sampling params and omits empty stop', () => {
    const body = buildChatCompletionBody(
      {
        ...base,
        temperature: 0.2,
        topP: 0.9,
        presencePenalty: 0.1,
        frequencyPenalty: -0.5,
        stop: [],
      },
      null,
    )
    expect(body.temperature).toBe(0.2)
    expect(body.top_p).toBe(0.9)
    expect(body.presence_penalty).toBe(0.1)
    expect(body.frequency_penalty).toBe(-0.5)
    expect(body.stop).toBeUndefined()
  })

  it('serializes single stop as string and multi as array', () => {
    expect(
      buildChatCompletionBody({ ...base, stop: ['END'] }, null).stop,
    ).toBe('END')
    expect(
      buildChatCompletionBody({ ...base, stop: ['A', 'B'] }, null).stop,
    ).toEqual(['A', 'B'])
  })
})

describe('messagesForApi', () => {
  it('strips assistant error bubbles', () => {
    const out = messagesForApi([
      { role: 'user', content: 'q' },
      { role: 'assistant', content: '错误：boom' },
      { role: 'assistant', content: 'ok' },
    ])
    expect(out).toEqual([
      { role: 'user', content: 'q' },
      { role: 'assistant', content: 'ok' },
    ])
  })
})

describe('shouldTryCacheResume', () => {
  it('does not auto-resume on multi-turn follow-up (prior assistant exists)', async () => {
    const { shouldTryCacheResume } = await import('./useChatCompletions')
    expect(
      shouldTryCacheResume({
        apiKey: 'sk',
        model: 'auto',
        taskId: 't1',
        gwSessionId: 'gw-1',
        messages: [
          { role: 'user', content: 'q1' },
          { role: 'assistant', content: 'a1' },
          { role: 'user', content: 'q2' },
        ],
      }),
    ).toBe(false)
  })

  it('auto-resumes when only a user turn exists (interrupted first reply)', async () => {
    const { shouldTryCacheResume } = await import('./useChatCompletions')
    expect(
      shouldTryCacheResume({
        apiKey: 'sk',
        model: 'auto',
        taskId: 't1',
        gwSessionId: 'gw-1',
        messages: [{ role: 'user', content: 'q1' }],
      }),
    ).toBe(true)
  })

  it('forceResumeFromCache always opts in', async () => {
    const { shouldTryCacheResume } = await import('./useChatCompletions')
    expect(
      shouldTryCacheResume({
        apiKey: 'sk',
        model: 'auto',
        taskId: 't1',
        gwSessionId: 'gw-1',
        forceResumeFromCache: true,
        messages: [
          { role: 'user', content: 'q1' },
          { role: 'assistant', content: 'a1' },
          { role: 'user', content: 'q2' },
        ],
      }),
    ).toBe(true)
  })
})

describe('chatCompletion multi-turn must not replay prior pending cache', () => {
  const originalFetch = globalThis.fetch

  beforeEach(() => {
    vi.resetModules()
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
    vi.restoreAllMocks()
  })

  it('issues a fresh upstream call on turn 2 even when pending cache has turn-1 body', async () => {
    const getPendingResponse = vi.fn(async () => ({
      status: 'completed' as const,
      body: 'data: {"choices":[{"delta":{"content":"OLD_TURN1"}}]}\n\n',
    }))
    vi.doMock('../api', () => ({
      createGatewaySession: vi.fn(async () => ({ session_id: 'gw-1' })),
      getPendingResponse,
    }))

    globalThis.fetch = vi.fn(async () =>
      new Response(
        JSON.stringify({
          choices: [{ message: { content: 'NEW_TURN2' } }],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    ) as unknown as typeof fetch

    const { chatCompletion } = await import('./useChatCompletions')
    const result = await chatCompletion({
      apiKey: 'sk',
      model: 'auto',
      taskId: 't1',
      gwSessionId: 'gw-1',
      stream: false,
      messages: [
        { role: 'user', content: 'q1' },
        { role: 'assistant', content: 'a1' },
        { role: 'user', content: 'q2' },
      ],
    })

    expect(result.content).toBe('NEW_TURN2')
    expect(result.resumed).toBeUndefined()
    expect(getPendingResponse).not.toHaveBeenCalled()
    expect(globalThis.fetch).toHaveBeenCalled()
  })
})

describe('chatCompletion abort + non-stream', () => {
  const originalFetch = globalThis.fetch

  beforeEach(() => {
    vi.resetModules()
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
    vi.restoreAllMocks()
  })

  it('sends stream:false and returns JSON content', async () => {
    globalThis.fetch = vi.fn(async () =>
      new Response(
        JSON.stringify({
          choices: [{ message: { content: 'hello-batch' } }],
          usage: { prompt_tokens: 1, completion_tokens: 2, total_tokens: 3 },
          model: 'gpt-test',
        }),
        {
          status: 200,
          headers: {
            'Content-Type': 'application/json',
            'X-Gw-Auto-Decision': JSON.stringify({ chosen_model: 'gpt-test' }),
          },
        },
      ),
    ) as unknown as typeof fetch

    vi.doMock('../api', () => ({
      createGatewaySession: vi.fn(async () => ({ session_id: 'gw-1' })),
      getPendingResponse: vi.fn(async () => ({ status: 'not_found' })),
    }))

    const { chatCompletion } = await import('./useChatCompletions')
    const result = await chatCompletion({
      apiKey: 'sk',
      model: 'auto',
      messages: [{ role: 'user', content: 'hi' }],
      taskId: 't1',
      gwSessionId: 'gw-1',
      stream: false,
      temperature: 0.5,
    })

    expect(result.content).toBe('hello-batch')
    expect(result.usage?.totalTokens).toBe(3)
    const call = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    const body = JSON.parse(call[1].body as string)
    expect(body.stream).toBe(false)
    expect(body.temperature).toBe(0.5)
  })

  it('throws AbortError when signal already aborted', async () => {
    vi.doMock('../api', () => ({
      createGatewaySession: vi.fn(async () => ({ session_id: 'gw-1' })),
      getPendingResponse: vi.fn(async () => ({ status: 'not_found' })),
    }))
    const { chatCompletion, isAbortError } = await import('./useChatCompletions')
    const ctrl = new AbortController()
    ctrl.abort()
    await expect(
      chatCompletion({
        apiKey: 'sk',
        model: 'auto',
        messages: [{ role: 'user', content: 'hi' }],
        taskId: 't1',
        gwSessionId: 'gw-1',
        signal: ctrl.signal,
      }),
    ).rejects.toSatisfy((e: unknown) => isAbortError(e))
  })
})
