// cjk_new_in_files.mjs - for each file with CJK, how many lines were added since baseline commit3a3403c1e
import { execSync } from 'node:child_process';

const BASE = '3a3403c1e';
const cjk = /[\u4e00-\u9fff]/;

// get files changed since baseline
const files = execSync('git diff --name-only ' + BASE + '..HEAD -- src/', { encoding: 'utf8' })
 .split('\n').filter((f) => /\.(vue|ts)$/.test(f));

const report = [];
for (const f of files) {
 let out = '';
 try {
 out = execSync('git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + f + ' | grep "^+" | grep -v "^+++"', { encoding: 'utf8', maxBuffer:50 *1024 *1024 });
 } catch (e) { continue; }
 const added = out ? out.split('\n').filter((l) => cjk.test(l.slice(1))).length :0;
 if (added >0) report.push({ f, added });
}

report.sort((a, b) => b.added - a.added);
let total =0;
for (const r of report) { total += r.added; }
console.log('=== files with most ADDED cjk lines since baseline (top20) ===');
for (const r of report.slice(0,20)) console.log(r.added, r.f);
console.log('total files:', report.length, 'total added cjk lines:', total);
