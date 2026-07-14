import { useI18n } from 'vue-i18n'

/** Resolve enum/status labels with fallback when API returns unknown values. */
export function useEnumLabel() {
  const { t, te } = useI18n()

  return function enumLabel(prefix: string, value?: string | null): string {
    if (value == null || value === '' || value === 'undefined') {
      const unknownKey = `${prefix}._unknown`
      return te(unknownKey) ? t(unknownKey) : '—'
    }
    const key = `${prefix}.${value}`
    if (te(key)) return t(key)
    const unknownKey = `${prefix}._unknown`
    return te(unknownKey) ? t(unknownKey) : String(value)
  }
}
