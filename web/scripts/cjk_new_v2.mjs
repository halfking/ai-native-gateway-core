//归因脚本 v2：锁定基线3a3403c1e（2026-11-09，CJK基线4480实际取的参照点）以来新增含中文行
// 注意：node -e 内嵌时空格会被吞，必须落盘执行
import { execSync } from 'node:child_process';

const BASE = '3a3403c1e';
const cjk = /[\u4e00-\u9fff\u3400-\u4dbf]/;

const files = execSync('git diff ' + BASE + '..HEAD --name-only -- src/', { encoding: 'utf8' })
 .split('\n').filter((f) => /\.(vue|ts)$/.test(f));

const report = [];
for (const f of files) {
 let out = '';
 try {
 out = execSync('git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + f, { encoding: 'utf8', maxBuffer:50 *1024 *1024 });
 } catch (e) { continue; }
 const added = out.split('\n')
 .filter((l) => l.startsWith('+') && !l.startsWith('+++'))
 .filter((l) => cjk.test(l)).length;
 if (added >0) report.push({ f, added });
}
report.sort((a, b) => b.added - a.added);
let total =0;
for (const r of report) total += r.added;
console.log('total files:', report.length, 'total added cjk lines:', total);
for (const r of report.slice(0,30)) console.log(r.added, r.f);
