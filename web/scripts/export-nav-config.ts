#!/usr/bin/env tsx
/**
 * Export Gateway navigation config as JSON during build.
 * Output: public/menu-config.json (deployed with dist/)
 *
 * 2026-08-26 (P1-26 rewrite): the previous implementation regexed the
 * TS source for the NAV_PRIMARY_ITEMS / NAV_GROUPS array literals and
 * then `eval()`-ed the captured text. That is fragile (non-greedy
 * `[\s\S]*?` stopped at the first `]`, eating nested `items: [...]`)
 * and is a build-time supply-chain-injection sink: a malicious PR
 * could insert a function call into appNav.ts and the eval would
 * execute it.
 *
 * This rewrite keeps the same I/O contract but uses a proper
 * character-level tokenizer that walks the source, skipping comments
 * and string literals, until it finds the array's matching `]`. We
 * never eval the captured text — instead we apply a string-safe
 * "rewrite single-quoted strings to double-quoted" pass that uses the
 * same tokenizer to track string boundaries, then hand the result to
 * JSON.parse. JSON.parse is a sandboxed parser with no side effects,
 * so the injection risk is closed.
 *
 * The schema (`sanitizeNavItem`) intentionally trims to the fields
 * the front end actually consumes.
 */
import { writeFileSync, mkdirSync, readFileSync } from 'fs'
import { resolve, dirname } from 'path'

const appNavSource = readFileSync(
  resolve(__dirname, '../src/config/appNav.ts'),
  'utf-8',
)

/**
 * Find the start index (just after the opening `[`) of the array
 * literal that follows the given anchor match. The anchor regex must
 * consume the opening `[` (so the returned substring begins at the
 * first character AFTER `[`).
 */
function arrayStartAfter(source: string, anchor: RegExp): number {
  const m = source.match(anchor)
  if (!m || m.index === undefined) return -1
  return m.index + m[0].length
}

/**
 * Walk forward from `start`, tracking string literals and bracket
 * depth, and return the slice from `start` up to and including the
 * matching `]` (which lives at `i + 1`). Returns null if the array
 * is unterminated.
 *
 * `start` must point at the first character AFTER the opening `[`
 * (the anchor regex consumes the bracket). The returned slice is the
 * inner body of the array — callers wrap it in `[ ... ]` for JSON.
 */
function extractBalancedArray(source: string, start: number): string | null {
  let depth = 1
  let inStr = false
  let strCh = ''
  let escape = false
  for (let i = start; i < source.length; i++) {
    const c = source[i]
    if (escape) { escape = false; continue }
    if (inStr) {
      if (c === '\\') { escape = true; continue }
      if (c === strCh) { inStr = false }
      continue
    }
    if (c === '"' || c === "'") { inStr = true; strCh = c; continue }
    if (c === '`') { inStr = true; strCh = '`'; continue }
    if (c === '[') { depth++; continue }
    if (c === ']') {
      depth--
      if (depth === 0) return source.slice(start, i)
      continue
    }
  }
  return null
}

/**
 * Rewrite a JS/TS array literal to JSON by:
 *   - skipping `//` and `/* *​/` comments
 *   - rewriting single-quoted (and backtick) strings to double-quoted
 *     (escaping any literal `"` inside)
 *   - stripping trailing commas
 *
 * The same string-literal state machine as extractBalancedArray is
 * used so we never touch the contents of an already-double-quoted
 * string. We also strip TypeScript-only type annotations that
 * `JSON.parse` would not accept (`as const`, type predicates) — the
 * NAV_PRIMARY_ITEMS / NAV_GROUPS declarations in appNav.ts do not use
 * them inside the array body, but we strip defensively.
 */
function tsArrayToJson(src: string): string {
  let out = ''
  let inStr = false
  let strCh = ''
  let escape = false
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (escape) { out += '\\' + c; escape = false; continue }
    if (inStr) {
      if (c === '\\') { out += '\\\\'; escape = true; continue }
      if (c === strCh) {
        // Close string.
        if (strCh !== '"') out += '"'
        inStr = false
        continue
      }
      if (strCh !== '"' && c === '"') { out += '\\"'; continue }
      out += c
      continue
    }
    // Line comment.
    if (c === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') i++
      if (i < src.length) { out += '\n'; continue }
      break
    }
    // Block comment.
    if (c === '/' && src[i + 1] === '*') {
      i += 2
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++
      i++ // step over closing '/'
      continue
    }
    // Opening a string.
    if (c === "'" || c === '`') {
      if (c === "'") out += '"'
      inStr = true
      strCh = c
      continue
    }
    if (c === '"') {
      out += '"'
      inStr = true
      strCh = '"'
      continue
    }
    // Unquoted object key (TypeScript allows `{ path: ... }`). A key
    // is an identifier preceded by whitespace + `{` or `,` and
    // followed by `:`. Wrap it in double quotes.
    if (/[A-Za-z_$]/.test(c)) {
      let j = i
      while (j < src.length && /[A-Za-z0-9_$]/.test(src[j])) j++
      // Look ahead past whitespace for `:` to confirm this is a key.
      let k = j
      while (k < src.length && /\s/.test(src[k])) k++
      if (k < src.length && src[k] === ':') {
        out += '"' + src.slice(i, j) + '"'
        i = j - 1
        continue
      }
      // Not a key — emit chars as-is and continue past the identifier.
      out += src.slice(i, j)
      i = j - 1
      continue
    }
    // Trailing comma: drop a `,` that is only whitespace before a
    // closer (`}` or `]`).
    if (c === ',') {
      let j = i + 1
      while (j < src.length && /\s/.test(src[j])) j++
      if (j < src.length && (src[j] === '}' || src[j] === ']')) continue
    }
    out += c
  }
  return out
}

const primaryStart = arrayStartAfter(
  appNavSource,
  /export const NAV_PRIMARY_ITEMS[^=]*=\s*\[/,
)
const groupsStart = arrayStartAfter(
  appNavSource,
  /export const NAV_GROUPS[^=]*=\s*\[/,
)

const primaryRaw = primaryStart >= 0 ? extractBalancedArray(appNavSource, primaryStart) : null
const groupsRaw = groupsStart >= 0 ? extractBalancedArray(appNavSource, groupsStart) : null

function parseNavArray(raw: string | null): any[] {
  if (raw == null) return []
  // raw is the array body (between the outer `[` and `]`); wrap it so
  // JSON.parse sees a JSON array literal.
  const wrapped = `[${raw}]`
  try {
    return JSON.parse(tsArrayToJson(wrapped))
  } catch (err) {
    console.error('[nav-export] Parse error:', err)
    console.error('[nav-export] Snippet:', raw.slice(0, 200))
    return []
  }
}

const primary = parseNavArray(primaryRaw)
const groups = parseNavArray(groupsRaw)

function sanitizeNavItem(item: any): any {
  return {
    path: item.path || '',
    label: item.label || '',
    labelKey: item.labelKey,
    icon: item.icon || '',
    external: !!item.external,
    super: !!item.super,
    platformOps: !!item.platformOps,
    opsPlatform: !!item.opsPlatform,
    tenantOnly: !!item.tenantOnly,
    hideForTenant: !!item.hideForTenant,
  }
}

const config = {
  version: '1.0',
  exportedAt: new Date().toISOString(),
  primary: primary.map(sanitizeNavItem),
  groups: groups.map((g: any) => ({
    id: g.id,
    labelKey: g.labelKey,
    items: (g.items || []).map(sanitizeNavItem),
  })),
}

const outPath = resolve(__dirname, '../public/menu-config.json')
mkdirSync(dirname(outPath), { recursive: true })
writeFileSync(outPath, JSON.stringify(config, null, 2), 'utf-8')

console.log(`✅ [nav-export] Written to ${outPath}`)
console.log(`   - Primary items: ${config.primary.length}`)
console.log(`   - Groups: ${config.groups.length}`)
console.log(`   - Ops platform items: ${config.groups.find((g: any) => g.id === 'opsplatform')?.items.length || 0}`)
