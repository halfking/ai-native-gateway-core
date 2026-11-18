import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
const cjk = new RegExp('[\\u4e00-\\u9fff\\u3400-\\u4dbf]');
const nameCmd = 'git diff ' + BASE + '..HEAD --name-only -- src/';
const files = execSync(nameCmd, { encoding: 'utf8' })
 .split('\n')
 .map(s => s.trim())
 .filter(f => /\.(vue|ts)$/.test(f) && f.length >0);
console.log('candidates:', files.length);
const report = [];
for (const f of files) {
 const cmd = 'git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + f;
 let out = '';
 try {
 out = execSync(cmd, { encoding: 'utf8', maxBuffer:50 *1024 *1024 });
 } catch (e) {
 console.log('ERR', f, String(e.message).slice(0,80));
 continue;
 }
 if (!out) continue;
 const plus = out.split('\n').filter(l => l.startsWith('+') && !l.startsWith('+++'));
 const added = plus.filter(l => cjk.test(l)).length;
 if (added >0) report.push({ f, added });
}
report.sort((a, b) => b.added - a.added);
let total =0;
for (const r of report) total += r.added;
console.log('total files:', report.length, 'total added cjk lines:', total);
for (const r of report.slice(0,30)) console.log(r.added, r.f);
