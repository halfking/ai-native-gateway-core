import { useI18n } from 'vue-i18n'

/** Localized tenant status label with raw fallback. */
export function useTenantStatusLabel() {
  const { t, te } = useI18n()

  function tenantStatusLabel(status: string): string {
    const key = `tenants.status.${status}`
    return te(key) ? t(key) : status
  }

  return { tenantStatusLabel }
}
