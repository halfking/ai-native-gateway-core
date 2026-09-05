// errorVocab.test.ts — structured error vocabulary gate (2026-09-05 audit F2-#1/#2).
//
// Guards the single-source contract between utils/errorVocab.ts and the
// errorVocab.* locale namespace: every wire value mapped in the vocab must
// resolve to a real zh-CN leaf key, and every badge must come from the shared
// tone set so RoutingAttemptsTimeline and ErrorDetailTab render identically.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import vm from 'vm'
import {
  ATTEMPT_RESULT_VOCAB,
  ERROR_KIND_VOCAB,
  ERROR_STAGE_VOCAB,
  RETRYABLE_VOCAB,
  attemptResultBadgeClass,
  attemptResultI18nKey,
  errorKindBadgeClass,
  errorKindI18nKey,
  errorStageI18nKey,
  rawVocabFallback,
  retryableBadgeClass,
  retryableI18nKey,
  type ErrorVocabBadge,
} from './errorVocab'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)

const BADGE_CLASSES: ReadonlySet<ErrorVocabBadge> = new Set([
  'badge-success',
  'badge-muted',
  'badge-orange',
  'badge-red',
])

/** Load a locale module file through the same vm approach as parity.test.ts. */
function loadLocaleModule(locale: string, module: string): Record<string, unknown> {
  const src = readFileSync(join(__dirname, '..', 'locales', locale, `${module}.ts`), 'utf8')
  const code = src.replace(/^export default /m, 'module.exports = ')
  const moduleObj: { exports: Record<string, unknown> } = { exports: {} }
  const sandbox = { module: moduleObj }
  vm.createContext(sandbox)
  vm.runInContext(code, sandbox, { filename: `${locale}/${module}.ts` })
  return moduleObj.exports
}

function zhLeaf(path: string): string | undefined {
  let node: unknown = loadLocaleModule('zh-CN', 'errorVocab')
  for (const segment of path.split('.')) {
    if (typeof node !== 'object' || node === null) return undefined
    node = (node as Record<string, unknown>)[segment]
  }
  return typeof node === 'string' ? node : undefined
}

describe('errorVocab', () => {
  it('maps every entry to an existing zh-CN leaf and a shared badge tone', () => {
    const groups = [ERROR_KIND_VOCAB, ERROR_STAGE_VOCAB, ATTEMPT_RESULT_VOCAB, RETRYABLE_VOCAB]
    for (const group of groups) {
      for (const vocab of Object.values(group)) {
        // Entry keys already carry their subgroup ('kind.*', 'stage.*', ...).
        expect(zhLeaf(vocab.key), `missing zh-CN leaf for errorVocab.${vocab.key}`).toBeTruthy()
        expect(BADGE_CLASSES.has(vocab.badge), `unexpected badge class ${vocab.badge}`).toBe(true)
      }
    }
  })

  it('resolves the anchor kinds shared by timeline and error table', () => {
    expect(errorKindI18nKey('upstream_overloaded')).toBe('errorVocab.kind.upstream_overloaded')
    expect(errorKindI18nKey('context_length')).toBe('errorVocab.kind.context_length_exceeded')
    expect(errorKindBadgeClass('auth')).toBe('badge-red')
    expect(errorKindBadgeClass('transient')).toBe('badge-orange')
    expect(errorStageI18nKey('preflight')).toBe('errorVocab.stage.preflight')
    expect(retryableI18nKey(true)).toBe('errorVocab.retryable.yes')
    expect(retryableI18nKey(null)).toBeNull()
    expect(retryableBadgeClass(false)).toBe('badge-muted')
    expect(attemptResultI18nKey('stream_interrupted')).toBe('errorVocab.result.stream_interrupted')
    expect(attemptResultBadgeClass('success')).toBe('badge-success')
  })

  it('falls back to raw text / danger tone for unknown wire values', () => {
    expect(errorKindI18nKey('brand_new_kind')).toBeNull()
    expect(errorKindBadgeClass('brand_new_kind')).toBe('badge-red')
    expect(rawVocabFallback('brand_new_kind')).toBe('brand new kind')
    expect(errorKindI18nKey(undefined)).toBeNull()
    expect(attemptResultI18nKey('mystery')).toBeNull()
    expect(attemptResultBadgeClass('mystery')).toBe('badge-red')
  })
})
