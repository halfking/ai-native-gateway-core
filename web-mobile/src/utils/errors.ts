import { ApiError } from '@/api/client'
import { t } from '@/i18n'

/** Show localized, permission-safe API errors in operator workflows. */
export function formatApiError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 0) return t('common.networkError')
    if (cause.status === 401) return t('common.sessionExpired')
    if (cause.status === 403) return t('common.forbidden')
    if (cause.status === 404) return t('common.notFoundOrDenied')
    if (cause.status === 409) return t('common.conflict')
    if (cause.status >= 500) return t('common.serverError')
  }
  return cause instanceof Error && cause.message ? cause.message : t('common.error')
}
