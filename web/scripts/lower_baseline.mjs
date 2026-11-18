import { measureAll, buildBaseline, writeBaseline } from '../src/i18n/localeValueQuality.ts';
import fs from 'fs';
const { byLocale, byLocaleAllKeys } = measureAll();
const draft = buildBaseline(byLocale, byLocaleAllKeys);
const before = JSON.parse(fs.readFileSync(new URL('../src/i18n/localeValueBaseline.json', import.meta.url), 'utf8'));
console.log('==降额对比（旧 → 新） ==');
const groups = ['untranslated', 'sameAsSource', 'cjkInNonCjkLocale', 'cjkInNonCjkLocaleAllKeys'];
for (const g of groups) {
 if (!draft[g]) continue;
 for (const loc of Object.keys(draft[g])) {
 const o = before[g]?.[loc], n = draft[g][loc];
 if (o !== undefined && o !== n) console.log(g + '.' + loc + ':', o, '→', n, o < n ? '⬆违规!' : '');
 }
}
writeBaseline(draft);
console.log('written. updated =', draft.updated);
