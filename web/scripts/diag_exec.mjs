//诊断：execSync 的 git diff 在本环境里如何表现
import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
const cmd = 'git diff ' + BASE + '..HEAD --name-only -- src/';
console.log('cmd =', JSON.stringify(cmd));
try {
 const out = execSync(cmd, { encoding: 'utf8' });
 console.log('ok, lines =', out.split('\n').length);
} catch (e) {
 console.log('FAILED:', e.message.slice(0,200));
}
