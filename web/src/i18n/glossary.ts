// glossary.ts — Codified glossary for the LLM Gateway console i18n system.
//
// Loaded at build time by the parity test (`scripts/check-i18n-parity.*`) to
// whitelist values that look like English but are intentionally kept
// English (brand names, backend enum strings, technical acronyms, units).
//
// Goals:
//   1. Translate every key in every locale. Never translate the values
//      enumerated in TECH_ACRONYMS (backend enums, status codes, role
//      identifiers, provider codes).
//   2. Keep brand names consistent across all locales (BRAND_TERMS).
//   3. Use locale-specific translations for technical *units* (UNITS),
//      but keep universal abbreviations (B/KB/MB/GB, RPM/TPM) verbatim.
//   4. Pure-types module: no side effects, no runtime imports.
//
// If you add a new enum value to the backend (e.g. a new credential status
// string), add it to TECH_ACRONYMS so the parity test does not flag the
// locale files for missing translation.

/* eslint-disable @typescript-eslint/no-unused-vars */

/* -------------------------------------------------------------------------- */
/* Brand & product names                                                       */
/* -------------------------------------------------------------------------- */

/**
 * Canonical brand-name spellings. These strings MUST NOT be transliterated
 * or translated — they are trademarks/product names that ship verbatim
 * across every locale.
 */
export const BRAND_TERMS = {
  /** Simplified Chinese (source locale). */
  'zh-CN': {
    kaixuan: 'Kaixuan',
    llmGateway: 'LLM Gateway',
    maas: 'MaaS',
  },
  /** English (reference). */
  'en-US': {
    kaixuan: 'Kaixuan',
    llmGateway: 'LLM Gateway',
    maas: 'MaaS',
  },
  /** Traditional Chinese. */
  'zh-TW': {
    kaixuan: 'Kaixuan',
    llmGateway: 'LLM Gateway',
    maas: 'MaaS',
  },
  /** Japanese. */
  'ja-JP': {
    kaixuan: 'Kaixuan',
    llmGateway: 'LLM Gateway',
    maas: 'MaaS',
  },
  /** German — compound-noun capitalisation matches product typography. */
  'de-DE': {
    kaixuan: 'Kaixuan',
    llmGateway: 'LLM-Gateway',
    maas: 'MaaS',
  },
  /** French — feminine noun for "gateway". */
  'fr-FR': {
    kaixuan: 'Kaixuan',
    llmGateway: 'Passerelle LLM',
    maas: 'MaaS',
  },
  /** Spanish — generic noun. */
  'es-ES': {
    kaixuan: 'Kaixuan',
    llmGateway: 'LLM Gateway',
    maas: 'MaaS',
  },
  /** Arabic — kept English inside RTL text; bidi-isolated by the runtime. */
  'ar-SA': {
    kaixuan: 'Kaixuan',
    llmGateway: 'بوابة LLM',
    maas: 'MaaS',
  },
} as const

export type BrandKey = keyof (typeof BRAND_TERMS)['en-US']

/* -------------------------------------------------------------------------- */
/* Strings that MUST NOT be translated (backend enums, acronyms, codes)        */
/* -------------------------------------------------------------------------- */

/**
 * Whitelist of strings that the parity test will *not* flag as missing
 * translations. Anything not in this set must have an explicit translation
 * for each locale.
 *
 * Sources:
 *   - Status enums (providerhealth, credentialstate, ratelimit)
 *   - Role identifiers (RBAC)
 *   - Provider codes (provider registry)
 *   - Error-kind codes (relay/errorsx)
 *   - Health states (gateway/circuit)
 *   - Source identifiers (manifest origin)
 *   - Universal acronyms (industry-standard abbreviations)
 */
export const TECH_ACRONYMS: ReadonlySet<string> = new Set<string>([
  // ---- Universal acronyms (industry-standard abbreviations) ----
  'MaaS',
  'LLM',
  'TPM',
  'RPM',
  'JWT',
  'JWTs',
  'JSON',
  'API',
  'HTTP',
  'HTTPS',
  'URL',
  'WebSocket',
  'TLS',
  'SSE',
  'RBAC',
  'CRUD',
  'UUID',
  'YAML',
  'CSV',
  'SQL',
  'MCP',
  'CORS',
  'SDK',
  'UI',
  'UX',
  'PDF',
  'HTML',
  'CSS',
  'JS',
  'TS',
  'Vue',
  'DB',
  'DNS',
  'IP',
  'TCP',
  'UDP',
  'TLS',
  'SSL',
  'POST',
  'GET',
  'PUT',
  'DELETE',
  'PATCH',

  // ---- Credential / status enums (providerhealth, credentialstate) ----
  'active',
  'pending',
  'expired',
  'success',
  'failed',
  'healthy',
  'warning',
  'unreachable',
  'broken',
  'cooling',
  'degraded',
  'quarantined',
  'manual_disabled',
  'manual_offline',
  'revoked',
  'enabled',
  'disabled',
  'inactive',

  // ---- Health states (gateway/circuit) ----
  'ready',
  'none',
  'closed',
  'open',
  'half_open',
  'half-open',

  // ---- Role identifiers (RBAC) ----
  'super_admin',
  'tenant_admin',

  // ---- Provider codes (provider registry) ----
  'openai-completions',
  'openai-responses',
  'anthropic-messages',
  'gemini-generate',
  'anthropic',
  'ollama',
  'cohere',
  'gemini',
  'openai',

  // ---- Error kinds (relay/errorsx) ----
  'model_not_found',
  'provider_error',
  'timeout',
  'rate_limit',
  'rate_limited',
  'auth_failed',
  'auth_error',
  'invalid_request',
  'context_length_exceeded',
  'context_length',
  'insufficient_quota',
  'quota_exceeded',
  'overloaded',
  'internal_error',
  'bad_gateway',
  'service_unavailable',
  'gateway_timeout',
  'circuit_open',
  'upstream_error',
  'upstream_timeout',
  'upstream_unreachable',

  // ---- Source identifiers (manifest origin) ----
  'api',
  'manifest',
  'manifest_only',
  'source',
  'none',
  'unknown',

  // ---- Work types (request classification) ----
  'chat',
  'completion',
  'embedding',
  'embeddings',
  'rerank',
  'image',
  'audio',
  'transcription',

  // ---- Work type categories (WorkTypesView) ----
  '通用',
  '研发',
  '营销',
  '采集',
  '多媒体',
  '企业',

  // ---- Compression / data-lifecycle enums ----
  'gzip',
  'br',
  'deflate',
  'zstd',
  'auto',

  // ---- Decision audit outcomes ----
  'allow',
  'deny',
  'challenge',
  'skip',

  // ---- Common verbs / nouns that appear as enum-like values ----
  'create',
  'update',
  'delete',
  'list',
  'get',
  'read',
  'write',
])

/* -------------------------------------------------------------------------- */
/* Units — translated per locale (for unit-display helpers in components)      */
/* -------------------------------------------------------------------------- */

/**
 * Localised unit labels for the most common units shown in the console.
 * Components may import this map directly to render e.g. "{n} credits".
 *
 * Units NOT listed here (B / KB / MB / GB, RPM / TPM, ms, %) are universal
 * abbreviations and must be kept verbatim across all locales.
 */
export const UNITS = {
  'zh-CN': {
    credits: '积分',
    seconds: '秒',
    minutes: '分',
    hours: '小时',
    days: '天',
    requests: '请求',
    tokens: '令牌',
    items: '个',
  },
  'en-US': {
    credits: 'credits',
    seconds: 's',
    minutes: 'min',
    hours: 'h',
    days: 'd',
    requests: 'requests',
    tokens: 'tokens',
    items: 'items',
  },
  'zh-TW': {
    credits: '積分',
    seconds: '秒',
    minutes: '分',
    hours: '小時',
    days: '天',
    requests: '請求',
    tokens: '權杖',
    items: '個',
  },
  'ja-JP': {
    credits: 'クレジット',
    seconds: '秒',
    minutes: '分',
    hours: '時間',
    days: '日',
    requests: 'リクエスト',
    tokens: 'トークン',
    items: '件',
  },
  'de-DE': {
    credits: 'Guthaben',
    seconds: 'Sek.',
    minutes: 'Min.',
    hours: 'Std.',
    days: 'Tg.',
    requests: 'Anfragen',
    tokens: 'Tokens',
    items: 'Einträge',
  },
  'fr-FR': {
    credits: 'Crédits',
    seconds: 's',
    minutes: 'min',
    hours: 'h',
    days: 'j',
    requests: 'requêtes',
    tokens: 'jetons',
    items: 'éléments',
  },
  'es-ES': {
    credits: 'créditos',
    seconds: 's',
    minutes: 'min',
    hours: 'h',
    days: 'd',
    requests: 'solicitudes',
    tokens: 'tokens',
    items: 'elementos',
  },
  'ar-SA': {
    credits: 'رصيد',
    seconds: 'ث',
    minutes: 'د',
    hours: 'س',
    days: 'ي',
    requests: 'طلبات',
    tokens: 'رموز',
    items: 'عناصر',
  },
} as const

export type LocaleCode = keyof typeof UNITS
export type UnitKey = keyof (typeof UNITS)['en-US']

/* -------------------------------------------------------------------------- */
/* Type-level documentation                                                   */
/* -------------------------------------------------------------------------- */

/**
 * `Localizable<T>` is a documentation-only branded type. Use it on string
 * fields that are user-facing prose and should be translated per locale.
 *
 * @example
 * ```ts
 * interface LoginForm {
 *   title: Localizable<string>   // user-facing; translate per locale
 *   apiKey: string                // raw user input; never translate
 *   status: Localizable<'active' | 'pending'>  // enum value — do NOT translate
 * }
 * ```
 *
 * The type alias is structurally identical to `T` (it does not affect
 * type-checking at runtime) but signals intent to reviewers and tooling.
 */
export type Localizable<T> = T

/**
 * `RawEnum<T>` marks string fields whose values come from the backend as
 * enum tokens. These strings MUST be passed through verbatim — they may
 * match a value in TECH_ACRONYMS.
 *
 * Like `Localizable`, this is a documentation-only type alias.
 */
export type RawEnum<T extends string> = T

/**
 * Compile-time sentinel: if a developer accidentally types
 *   `const x: never = TECH_ACRONYMS.has('something')`
 * the type system forces them to think about whether the string is an
 * enum value (add to TECH_ACRONYMS) or user-prose (must be translated).
 */
export type TechAcronym = Exclude<string, never>

/* -------------------------------------------------------------------------- */
/* Runtime helpers (still pure, no side effects)                              */
/* -------------------------------------------------------------------------- */

/** Type-narrowing guard: returns true iff the input is a whitelisted term. */
export function isTechAcronym(value: string): boolean {
  return TECH_ACRONYMS.has(value)
}

/** Look up the localised unit label for a locale. Falls back to en-US. */
export function getUnit<K extends UnitKey>(
  locale: string,
  key: K,
): (typeof UNITS)[LocaleCode][K] {
  const table = (UNITS as Record<string, Record<K, string>>)[locale]
  if (table && key in table) return table[key] as (typeof UNITS)[LocaleCode][K]
  return UNITS['en-US'][key]
}

/** Look up a brand-name translation. Falls back to en-US. */
export function getBrand<K extends BrandKey>(
  locale: string,
  key: K,
): string {
  const table = (BRAND_TERMS as Record<string, Record<K, string>>)[locale]
  if (table && key in table) return table[key]
  return BRAND_TERMS['en-US'][key]
}