import fs from 'fs';
import path from 'path';
const WEB = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/locales';
const loc = process.argv[2];
const trFile = process.argv[3];
const tr = JSON.parse(fs.readFileSync(trFile, 'utf8'));
const byFile = {};
for (const [k, v] of Object.entries(tr)) {
 const ns = k.split('.')[0];
 (byFile[ns] ||= []).push([k, v]);
}
const esc = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
let total =0;
const failed = [];
for (const [ns, items] of Object.entries(byFile)) {
 const file = path.join(WEB, loc, ns + '.ts');
 let text = fs.readFileSync(file, 'utf8');
 for (const [k, v] of items) {
 const leaf = k.split('.').pop();
 const sv = String(v).replace(/\\/g, '\\\\').replace(/'/g, "\\'");
 //匹配 leaf: '任意单引号值'，要求全文唯一（不同嵌套同名 leaf 会拒绝）
 const re = new RegExp("((?:^|\\n)(\\s*)" + esc(leaf) + "\\s*:\\s*)'[^'\\n]*'");
 const count = (text.match(new RegExp(re.source, 'g')) || []).length;
 if (count ===0) { failed.push(k + ' [no match]'); continue; }
 if (count >1) { failed.push(k + ' [ambiguous x' + count + ']'); continue; }
 text = text.replace(re, "$1'" + sv + "'");
 total++;
 }
 fs.writeFileSync(file, text);
}
console.log(loc, 'replaced:', total, 'failed:', failed.length);
if (failed.length) console.log(failed.join('\n'));
