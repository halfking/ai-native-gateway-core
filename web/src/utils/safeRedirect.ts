// safeRedirect.ts — keep post-auth / 401 recovery on the original SPA path.
// Only same-origin relative paths are accepted so a `redirect` query cannot
// bounce the user onto another origin.

export function parseSafeInternalRedirect(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  const value = raw.trim()
  if (!value.startsWith('/') || value.startsWith('//') || value.includes('://')) return null

  let decoded = value
  try {
    if (/%[0-9A-Fa-f]{2}/.test(value)) decoded = decodeURIComponent(value)
  } catch {
    decoded = value
  }
  if (!decoded.startsWith('/') || decoded.startsWith('//') || decoded.includes('://')) return null

  try {
    const url = new URL(decoded, 'http://llmgw.invalid')
    if (url.pathname === '/login') return null
    return `${url.pathname}${url.search}${url.hash}`
  } catch {
    return null
  }
}

export function inlineLoginPath(currentPathWithQuery: string): string {
  const redirect = parseSafeInternalRedirect(currentPathWithQuery)
  if (!redirect || redirect === '/' || redirect.startsWith('/login')) return '/?login=1'
  return `/?login=1&redirect=${encodeURIComponent(redirect)}`
}

export function locationFromInternalPath(raw: unknown): { path: string; query?: Record<string, string>; hash?: string } | null {
  const parsed = parseSafeInternalRedirect(raw)
  if (!parsed) return null
  try {
    const url = new URL(parsed, 'http://llmgw.invalid')
    const query: Record<string, string> = {}
    url.searchParams.forEach((value, key) => {
      query[key] = value
    })
    return {
      path: url.pathname,
      ...(Object.keys(query).length ? { query } : {}),
      ...(url.hash ? { hash: url.hash } : {}),
    }
  } catch {
    return null
  }
}
