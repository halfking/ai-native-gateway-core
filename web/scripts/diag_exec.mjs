//诊断：execSync 的 git diff 在本环境里如何表现
import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
// ★ -z 化（2026-11-25 12h 修订审计轮）：★ `--name-only` 默认受 core.quotePath
// ★ 转义 ⇒ 非 ASCII 路径被转成八进制＋外层双引号 ★ ⇒ 而这里统计的是**条目数**
// ★ ⇒ 转义不改判定 ★ ⇒ 但统一走 `-z` 后枚举面才是「拿到真实名字」的那一种 ★
const cmd = 'git diff ' + BASE + '..HEAD --name-only -z -- src/';
console.log('cmd =', JSON.stringify(cmd));
try {
 const out = execSync(cmd, { encoding: 'utf8' });
 console.log('ok, names =', out.split('\0').filter(s => s.length > 0).length);
} catch (e) {
 console.log('FAILED:', e.message.slice(0,200));
}
