// Smoke test for cache resume helpers (Track C, 2026-06-21).
// Run with: node scripts/smoke-cache-resume.mjs
//
// These are pure-function tests that mirror the logic in
// web/src/composables/useChatCompletions.ts. They run against fixtures
// without needing vitest or any build pipeline — useful for fast CI
// smoke checks before the full e2e deploy verification.

import assert from 'node:assert/strict'

// ── Mirror of parseSsePayload from web/src/composables/useChatCompletions.ts ──
function parseSsePayload(line) {
  const trimmed = line.trim()
  if (!trimmed.startsWith('data:')) return { delta: '', usage: null }
  const payload = trimmed.slice(5).trim()
  if (!payload || payload === '[DONE]') return { delta: '', usage: null }
  try {
    const obj = JSON.parse(payload)
    const choices = obj?.choices
    const delta = choices?.[0]?.delta?.content ?? ''
    const usage = parseUsageFromObject(obj?.usage ?? obj)
    return { delta: delta || '', usage }
  } catch {
    return { delta: '', usage: null }
  }
}

function numField(obj, ...keys) {
  for (const k of keys) {
    const v = obj[k]
    if (typeof v === 'number' && Number.isFinite(v)) return v
  }
  return 0
}

function parseUsageFromObject(obj) {
  if (!obj || typeof obj !== 'object') return null
  const u = obj.usage ?? obj
  if (!u || typeof u !== 'object') return null
  const promptTokens = numField(u, 'prompt_tokens', 'input_tokens')
  const completionTokens = numField(u, 'completion_tokens', 'output_tokens')
  let totalTokens = numField(u, 'total_tokens')
  if (totalTokens === 0 && (promptTokens > 0 || completionTokens > 0)) {
    totalTokens = promptTokens + completionTokens
  }
  if (promptTokens === 0 && completionTokens === 0 && totalTokens === 0) return null
  return { promptTokens, completionTokens, totalTokens }
}

// ── Mirror of replayCachedSseToResult ─────────────────────────────────────
function replayCachedSseToResult(sseBody, onDelta) {
  let content = ''
  let usage = null
  for (const rawLine of sseBody.split('\n')) {
    const { delta, usage: lineUsage } = parseSsePayload(rawLine)
    if (lineUsage) usage = lineUsage
    if (delta) {
      content += delta
      onDelta?.(delta)
    }
  }
  return { content, usage }
}

// ── Mirror of shouldTryCacheResume ────────────────────────────────────────
function shouldTryCacheResume(opts) {
  if (opts.forceResumeFromCache) return true
  const last = opts.messages[opts.messages.length - 1]
  return last?.role === 'user'
}

// ── Tests ────────────────────────────────────────────────────────────────
function test(name, fn) {
  try {
    fn()
    console.log(`  ✓ ${name}`)
  } catch (e) {
    console.error(`  ✗ ${name}`)
    console.error(`    ${e.message}`)
    process.exitCode = 1
  }
}

console.log('replayCachedSseToResult:')

test('concatenates deltas from data: lines', () => {
  const sse = [
    'data: {"choices":[{"delta":{"content":"Hello"}}]}',
    '',
    'data: {"choices":[{"delta":{"content":", world"}}]}',
    '',
    'data: [DONE]',
    '',
  ].join('\n')
  const out = replayCachedSseToResult(sse)
  assert.equal(out.content, 'Hello, world')
})

test('emits deltas via onDelta callback', () => {
  const sse = 'data: {"choices":[{"delta":{"content":"abc"}}]}\n\ndata: {"choices":[{"delta":{"content":"def"}}]}\n\n'
  const chunks = []
  replayCachedSseToResult(sse, (d) => chunks.push(d))
  assert.deepEqual(chunks, ['abc', 'def'])
})

test('extracts usage from final chunk', () => {
  const sse = [
    'data: {"choices":[{"delta":{"content":"x"}}]}',
    '',
    'data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}',
    '',
    'data: [DONE]',
  ].join('\n')
  const out = replayCachedSseToResult(sse)
  assert.deepEqual(out.usage, { promptTokens: 10, completionTokens: 5, totalTokens: 15 })
})

test('skips [DONE] sentinel', () => {
  const sse = 'data: {"choices":[{"delta":{"content":"hi"}}]}\n\ndata: [DONE]\n\n'
  const out = replayCachedSseToResult(sse)
  assert.equal(out.content, 'hi')
})

test('tolerates malformed JSON lines', () => {
  const sse = [
    'data: {"choices":[{"delta":{"content":"a"}}]}',
    'data: not json at all',
    'data: {"choices":[{"delta":{"content":"b"}}]}',
    '',
  ].join('\n')
  const out = replayCachedSseToResult(sse)
  assert.equal(out.content, 'ab')
})

test('returns empty content for empty body', () => {
  const out = replayCachedSseToResult('')
  assert.equal(out.content, '')
  assert.equal(out.usage, null)
})

console.log('\nshouldTryCacheResume:')

test('returns true when forceResumeFromCache is set', () => {
  assert.equal(
    shouldTryCacheResume({ messages: [{ role: 'assistant', content: 'hi' }], forceResumeFromCache: true }),
    true,
  )
})

test('returns true when last message is user', () => {
  assert.equal(
    shouldTryCacheResume({ messages: [{ role: 'user', content: 'q' }] }),
    true,
  )
})

test('returns false when last message is assistant (full reply already shown)', () => {
  assert.equal(
    shouldTryCacheResume({ messages: [{ role: 'assistant', content: 'a' }] }),
    false,
  )
})

test('returns false for empty messages', () => {
  assert.equal(shouldTryCacheResume({ messages: [] }), false)
})

console.log('\nIntegration:')

test('end-to-end: replay a realistic SSE body yields matching content + usage', () => {
  const sse = [
    'data: {"id":"cmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Once "}}]}',
    '',
    'data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"upon "}}]}',
    '',
    'data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"a "}}]}',
    '',
    'data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"time"}}],"finish_reason":"stop"}',
    '',
    'data: {"id":"cmpl-1","choices":[{"index":0,"delta":{}}],"usage":{"prompt_tokens":42,"completion_tokens":4,"total_tokens":46}}',
    '',
    'data: [DONE]',
    '',
  ].join('\n')
  const out = replayCachedSseToResult(sse)
  assert.equal(out.content, 'Once upon a time')
  assert.deepEqual(out.usage, { promptTokens: 42, completionTokens: 4, totalTokens: 46 })
})

if (process.exitCode === 1) {
  console.error('\n❌ Smoke test FAILED')
} else {
  console.log('\n✅ All smoke tests passed')
}