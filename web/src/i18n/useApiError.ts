// useApiError.ts — render a thrown API error in the active locale.

import { i18n } from './index'

const TE = i18n.global.t.bind(i18n.global)

/** Return a localized message for any thrown value (API or otherwise). */
export function resolveApiError(err: unknown): string {
  if (err instanceof TypeError && /fetch|network/i.test(err.message)) {
    return TE('errors.network')
  }

  const status = (err as { status?: number; response?: { status?: number } })?.status
    ?? (err as { response?: { status?: number } })?.response?.status

  if (status) {
    const byStatus = TE(`errors.byStatus.${status}`)
    if (byStatus && !/^errors\./.test(byStatus)) return byStatus
  }

  if (typeof err === 'string' && err) return err
  if (err instanceof Error && err.message) return err.message
  if (typeof err === 'object' && err !== null) {
    const msg = (err as { message?: unknown }).message
    if (typeof msg === 'string' && msg) return msg
  }

  return TE('errors.unknown')
}
