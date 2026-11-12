import { it } from 'vitest'
import { measureAll } from './localeValueQuality'
it('probe measured', () => {
  const { byLocale, byLocaleAllKeys } = measureAll()
  for (const loc of ['de-DE', 'fr-FR', 'ja-JP', 'zh-TW']) {
    const a = byLocaleAllKeys.get(loc)
    const q = byLocale.get(loc)
    console.log(`MEAS ${loc} untranslated=${q?.untranslated} sameAsSource=${a?.sameAsSource}`)
  }
})
