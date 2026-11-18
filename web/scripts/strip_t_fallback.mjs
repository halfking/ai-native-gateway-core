//删除 t('key', '中文fallback') 的第二参数——仅当键已确认存在于 zh-CN 词条时安全
//用法：npx tsx scripts/strip_t_fallback.mjs <file...>
import fs from 'fs';
import { countFile } from '../src/i18n/hardcodedCjk.ts';
const CJK = /[一-鿿]/;
let totalRemoved =0;
for (const f of process.argv.slice(2)) {
 const before = countFile(f);
 let src = fs.readFileSync(f, 'utf8');
 //匹配 t('...', '中文…') 与 t("...", '中文…')——第二参数是单引号含中文的字面量
 //也处理 t(`动态`, '中文')形态？先只处理静态键。
 const re = /(t\(\s*'(?:[^'\\]|\\.)*'\s*,\s*)'(?:[^'\\]|\\.)*'(?!')\s*(\))/g;
 let count =0;
 src = src.replace(re, (m, p1, p2) => {
 //仅当原第二参数含中文才删
 const full = m;
 if (!CJK.test(full)) return m;
 count++;
 return p1 + p2;
 });
 if (count >0) {
 fs.writeFileSync(f, src);
 totalRemoved += count;
 }
 const after = countFile(f);
 console.log(f, 'removed:', count, 'guard:', before, '→', after);
}
console.log('total removed:', totalRemoved);
