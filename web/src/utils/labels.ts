/** Human-readable labels for license / release status enums. Mirrors
 *  ai-native-maintain's `maintain-web/src/utils/labels.ts` for parity with
 *  /maintain/activate. i18n is opt-in: when vue-i18n is available we
 *  resolve the i18n key, otherwise we fall back to the Chinese string. */

const LICENSE_STATE: Record<string, [string, string]> = {
  none: ['labels.licenseNone', '未激活'],
  active: ['labels.licenseActive', '已激活'],
  expired: ['labels.licenseExpired', '已过期'],
  revoked: ['labels.licenseRevoked', '已吊销'],
  grace: ['labels.licenseActive', '宽限期'],
  pending: ['labels.licenseNone', '待生效'],
}

const LICENSE_MODE: Record<string, string> = {
  licensed: '已授权',
  community: '社区版',
  restricted: '受限模式',
}

type I18nLike = {
  global?: { t?: (key: string) => unknown }
}

function tt(key: string, fallback: string): string {
  try {
    const i18n = (typeof window !== 'undefined' ? (window as unknown as { __VUE_I18N__?: I18nLike }).__VUE_I18N__ : null)
    const translated = i18n?.global?.t?.(key)
    if (typeof translated === 'string' && translated && translated !== key) return translated
  } catch {
    /* fall through */
  }
  return fallback
}

export function licenseStateLabel(state?: string | null): string {
  if (!state) return '未知'
  const hit = LICENSE_STATE[state]
  return hit ? tt(hit[0], hit[1]) : state
}

export function licenseModeLabel(mode?: string | null): string {
  if (!mode) return ''
  return LICENSE_MODE[mode] || mode
}

export function isLicenseActive(state?: string | null): boolean {
  return state === 'active' || state === 'grace'
}