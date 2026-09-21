// errorVocab.ts — structured error vocabulary single source of truth (2026-09-05 audit F2-#1/#2).
//
// Before this module, `error_kind` / `stage` / `retryable` / attempt `result`
// labels were maintained independently in RoutingAttemptsTimeline.vue (zh-CN
// hardcoded maps) and ErrorDetailTab.vue (raw English passthrough), so the same
// `upstream_overloaded` rendered as a zh badge on the request timeline but as
// raw English in the vendor credential error table. This module is now the ONE
// place that maps a wire value to:
//   - an i18n leaf key under the `errorVocab.*` namespace (labels live in
//     src/locales/<locale>/errorVocab.ts, 8 locales, gated by parity.test.ts)
//   - a shared badge tone class so both consumers render identically
//
// Wire values are aligned with the backend taxonomy in errorsx/classify.go
// (`Kind*` constants) as surfaced through
// domains/streaming/executors/supplier_error_logger.go (error_kind / stage /
// is_retryable columns of supplier_errors_unified). `context_length` is kept as
// a legacy alias of `context_length_exceeded` for older rows.
//
// Unknown / future values: lookups return null and the caller falls back to
// rendering the raw wire value (underscores rendered as spaces) with a neutral
// badge, exactly like the pre-existing behaviour for unmapped kinds.

export type ErrorVocabBadge = 'badge-success' | 'badge-muted' | 'badge-orange' | 'badge-red'

export interface ErrorVocabEntry {
  /** Leaf key under the `errorVocab.` i18n namespace (e.g. 'kind.upstream_overloaded'). */
  key: string
  badge: ErrorVocabBadge
}

const entry = (key: string, badge: ErrorVocabBadge): ErrorVocabEntry => ({ key, badge })

/**
 * error_kind vocabulary — mirrors errorsx/classify.go Kind* values.
 * badge tones: badge-red = credential-fatal / deterministic failure,
 * badge-orange = retryable / transient family, badge-muted = neutral
 * (canceled, non-fatal client-side mismatches).
 */
export const ERROR_KIND_VOCAB: Readonly<Record<string, ErrorVocabEntry>> = Object.freeze({
  transient: entry('kind.transient', 'badge-orange'),
  timeout: entry('kind.timeout', 'badge-orange'),
  network: entry('kind.network', 'badge-orange'),
  rate_limit: entry('kind.rate_limit', 'badge-orange'),
  auth: entry('kind.auth', 'badge-red'),
  auth_revoked: entry('kind.auth_revoked', 'badge-red'),
  quota: entry('kind.quota', 'badge-red'),
  quota_periodic: entry('kind.quota_periodic', 'badge-red'),
  quota_balance: entry('kind.quota_balance', 'badge-red'),
  quota_permanent: entry('kind.quota_permanent', 'badge-red'),
  upstream_down: entry('kind.upstream_down', 'badge-red'),
  upstream_overloaded: entry('kind.upstream_overloaded', 'badge-orange'),
  model_not_found: entry('kind.model_not_found', 'badge-red'),
  model_deprecated: entry('kind.model_deprecated', 'badge-red'),
  context_length_exceeded: entry('kind.context_length_exceeded', 'badge-red'),
  // Legacy alias persisted before the backend renamed to context_length_exceeded.
  context_length: entry('kind.context_length_exceeded', 'badge-red'),
  content_filter: entry('kind.content_filter', 'badge-red'),
  stream_timeout: entry('kind.stream_timeout', 'badge-orange'),
  canceled: entry('kind.canceled', 'badge-muted'),
  client_bug: entry('kind.client_bug', 'badge-red'),
  concurrent: entry('kind.concurrent', 'badge-orange'),
  empty_response: entry('kind.empty_response', 'badge-orange'),
  conversion_error: entry('kind.conversion_error', 'badge-red'),
  unsupported_feature: entry('kind.unsupported_feature', 'badge-red'),
  tool_call_id_mismatch: entry('kind.tool_call_id_mismatch', 'badge-muted'),
  upstream_context_loss: entry('kind.upstream_context_loss', 'badge-orange'),
  circuit_open: entry('kind.circuit_open', 'badge-orange'),
  no_available_channel: entry('kind.no_available_channel', 'badge-red'),
  fp_slot_saturated: entry('kind.fp_slot_saturated', 'badge-orange'),
})

/** Failure stage vocabulary (supplier_errors_unified.stage enum). */
export const ERROR_STAGE_VOCAB: Readonly<Record<string, ErrorVocabEntry>> = Object.freeze({
  preflight: entry('stage.preflight', 'badge-muted'),
  connect: entry('stage.connect', 'badge-muted'),
  upstream: entry('stage.upstream', 'badge-muted'),
  stream: entry('stage.stream', 'badge-muted'),
})

/**
 * Attempt result vocabulary (RoutingAttemptsTimeline attempt cards).
 * Values come from request_logs attempt outcomes, which predate the
 * errorsx taxonomy (e.g. `unauthorized`, `stream_interrupted`).
 */
export const ATTEMPT_RESULT_VOCAB: Readonly<Record<string, ErrorVocabEntry>> = Object.freeze({
  success: entry('result.success', 'badge-success'),
  canceled: entry('result.canceled', 'badge-muted'),
  timeout: entry('result.timeout', 'badge-orange'),
  model_not_found: entry('result.model_not_found', 'badge-red'),
  rate_limit: entry('result.rate_limit', 'badge-orange'),
  unauthorized: entry('result.unauthorized', 'badge-red'),
  concurrent: entry('result.concurrent', 'badge-orange'),
  empty_response: entry('result.empty_response', 'badge-orange'),
  stream_interrupted: entry('result.stream_interrupted', 'badge-orange'),
  error: entry('result.error', 'badge-red'),
})

/** Retryable two-state vocabulary; unified badge pairing across consumers. */
export const RETRYABLE_VOCAB: Readonly<{ yes: ErrorVocabEntry; no: ErrorVocabEntry }> = Object.freeze({
  yes: entry('retryable.yes', 'badge-orange'),
  no: entry('retryable.no', 'badge-muted'),
})

export const ERROR_VOCAB_NAMESPACE = 'errorVocab'

function fullKey(leaf: string): string {
  return `${ERROR_VOCAB_NAMESPACE}.${leaf}`
}

/** Human-readable fallback for wire values missing from the vocabulary. */
export function rawVocabFallback(value: string): string {
  return value.replace(/_/g, ' ')
}

export function errorKindVocab(kind?: string | null): ErrorVocabEntry | null {
  if (!kind) return null
  return ERROR_KIND_VOCAB[kind] ?? null
}

/** Full i18n key (errorVocab.kind.*) or null when the kind is unknown. */
export function errorKindI18nKey(kind?: string | null): string | null {
  const vocab = errorKindVocab(kind)
  return vocab ? fullKey(vocab.key) : null
}

/** Shared badge tone; unknown kinds default to the danger tone (error context). */
export function errorKindBadgeClass(kind?: string | null): string {
  return errorKindVocab(kind)?.badge ?? 'badge-red'
}

export function errorStageVocab(stage?: string | null): ErrorVocabEntry | null {
  if (!stage) return null
  return ERROR_STAGE_VOCAB[stage] ?? null
}

export function errorStageI18nKey(stage?: string | null): string | null {
  const vocab = errorStageVocab(stage)
  return vocab ? fullKey(vocab.key) : null
}

export function errorStageBadgeClass(stage?: string | null): string {
  return errorStageVocab(stage)?.badge ?? 'badge-muted'
}

export function attemptResultVocab(result?: string | null): ErrorVocabEntry | null {
  if (!result) return null
  return ATTEMPT_RESULT_VOCAB[result] ?? null
}

export function attemptResultI18nKey(result?: string | null): string | null {
  const vocab = attemptResultVocab(result)
  return vocab ? fullKey(vocab.key) : null
}

export function attemptResultBadgeClass(result?: string | null): string {
  return attemptResultVocab(result)?.badge ?? 'badge-red'
}

export function retryableVocab(retryable?: boolean | null): ErrorVocabEntry | null {
  if (retryable === true) return RETRYABLE_VOCAB.yes
  if (retryable === false) return RETRYABLE_VOCAB.no
  return null
}

export function retryableI18nKey(retryable?: boolean | null): string | null {
  const vocab = retryableVocab(retryable)
  return vocab ? fullKey(vocab.key) : null
}

export function retryableBadgeClass(retryable?: boolean | null): string | null {
  return retryableVocab(retryable)?.badge ?? null
}
