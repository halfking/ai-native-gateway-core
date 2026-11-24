import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
const cjk = /[\u4e00-\u9fff\u3400-\u4dbf]/;
// ★ -z 化（2026-11-25 12h 修订审计轮）：★ `--name-only` 默认受 core.quotePath
// ★ 转义 ⇒ 非 ASCII 路径被转成八进制＋外层双引号 ★ ⇒ ★ 而本脚本按
// ★ `/\.(vue|ts)$/` 挑候选文件 ⇒ ★ 转义形态下中文文件名末尾带引号 ⇒ **被误滤掉**
// ★ ⇒ ★ ⇒ 必须走 `-z` 拿原始 UTF-8 字节 ★
// ★ ★ 顺带：-z 化后拿到的是**原始路径**（可能含空格/元字符）⇒ ★ 拼进命令行时
// ★ ★ 必须做 shell 安全引用 ★ ★ ⇒ 否则「文件名里有空格」会把命令拆断 ★
const q = s => "'" + s.replace(/'/g, "'\\''") + "'";
const nameCmd = 'git diff ' + BASE + '..HEAD --name-only -z -- src/';
const files = execSync(nameCmd, { encoding: 'utf8' })
 .split('\0')
 .map(s => s.trim())
 .filter(f => /\.(vue|ts)$/.test(f) && f.length >0);
console.log('candidate files:', files.length);
const report = [];
for (const f of files) {
 const cmd = 'git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + q(f);
 let out = '';
 try {
 out = execSync(cmd, { encoding: 'utf8', maxBuffer:50 *1024 *1024 });
 } catch (e) {
 continue;
 }
 if (!out) continue;
 const plus = out.split('\n').filter(l => l.charAt(0) === '+' && !l.startsWith('+++'));
 const added = plus.filter(l => cjk.test(l)).length;
 if (added >0) report.push({ f, added });
}
report.sort((a, b) => b.added - a.added);
let total =0;
for (const r of report) total += r.added;
console.log('total files:', report.length, 'total added cjk lines:', total);
for (const r of report.slice(0,30)) console.log(r.added, r.f);
