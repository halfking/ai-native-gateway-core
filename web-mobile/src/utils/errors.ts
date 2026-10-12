import { ApiError } from '@/api/client'
import { t } from '@/i18n'

/** Show localized, permission-safe API errors in operator workflows. */
export function formatApiError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 0) return t('common.networkError')
    if (cause.status === 401) {
      // ★ 401 不等于会话过期（2026-10-13 实测坐实）。
      //   后端刻意把「端点未注册」与「未认证」回成逐字节相同的 401
      //   （防端点枚举），所以单看 401 无法断言会话死了 ——
      //   `client.ts` 会在踢登录前探一次 `/api/auth/me`，
      //   `sessionAlive === true` 就是那次探针的结论：**会话是好的**。
      //   此时说「登录状态已过期，请重新登录」会把用户朝错误方向引导
      //   （实测页面仍停在费用对账、外壳完好，文案却让用户去重新登录）。
      //   `undefined`（本次没探）与 `false`（探针也失败）都保留原文案。
      if (cause.sessionAlive === true) return t('common.sessionAliveUnauthorized')
      return t('common.sessionExpired')
    }
    if (cause.status === 403) return t('common.forbidden')
    if (cause.status === 404) return t('common.notFoundOrDenied')
    if (cause.status === 409) return t('common.conflict')
    if (cause.status >= 500) return t('common.serverError')
  }
  return cause instanceof Error && cause.message ? cause.message : t('common.error')
}
