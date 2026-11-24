import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
// ★ -z 化（2026-11-25 12h 修订审计轮）：★ `--name-only` 默认受 core.quotePath
// ★ 转义 ⇒ 非 ASCII 路径被转成八进制＋外层双引号 ★ ⇒ ★ 而本脚本按
// ★ `/\.(vue|ts)$/` 挑候选文件 ⇒ ★ 转义形态下中文文件名末尾带引号 ⇒ **被误滤掉**
// ★ ⇒ ★ ⇒ 必须走 `-z` 拿原始 UTF-8 字节 ★
// ★ ★ 顺带：-z 化后拿到的是**原始路径**（可能含空格/元字符）⇒ ★ 拼进命令行时
// ★ ★ 必须做 shell 安全引用 ★ ★
const q = s => "'" + s.replace(/'/g, "'\\''") + "'";
const nameCmd = 'git diff ' + BASE + '..HEAD --name-only -z -- src/';
const files = execSync(nameCmd, { encoding: 'utf8' })
 .split('\0')
 .map(s => s.trim())
 .filter(f => /\.(vue|ts)$/.test(f) && f.length >0);
console.log('candidates:', files.length);
console.log('first3:', JSON.stringify(files.slice(0,3)));
//直接测第一个文件
const f0 = files[0];
const cmd = 'git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + q(f0);
console.log('cmd0:', JSON.stringify(cmd));
let out = '';
try {
 out = execSync(cmd, { encoding: 'utf8', maxBuffer:50 *1024 *1024 });
 console.log('out0 len:', out.length);
 const plus = out.split('\n').filter(l => l.startsWith('+') && !l.startsWith('+++'));
 console.log('plus0:', plus.length);
} catch (e) {
 console.log('FAILED0:', String(e.message).slice(0,150));
}
