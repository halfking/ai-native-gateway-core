import { countFile, maskSource } from '../src/i18n/hardcodedCjk.ts';
import fs from 'fs';
const files = process.argv.slice(2);
for (const f of files) {
 console.log('===', f, 'guard count:', countFile(f));
 const src = fs.readFileSync(f, 'utf8');
 const masked = maskSource(f, src);
 const lines = masked.split('\n');
 lines.forEach((l, i) => {
 if (/[一-鿿]/.test(l)) console.log(' L' + (i +1) + ':', l.trim().slice(0,100));
 });
}
