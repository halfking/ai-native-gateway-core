//诊断2：单文件 diff 为何拿不到
import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
const f = 'src/views/RequestLogsView.vue';
const cmd = 'git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + f;
console.log('cmd =', JSON.stringify(cmd));
try {
 const out = execSync(cmd, { encoding: 'utf8', maxBuffer:50 *1024 *1024 });
 const lines = out.split('\n');
 console.log('diff lines:', lines.length);
 const plus = lines.filter(l => l.startsWith('+') && !l.startsWith('+++'));
 console.log('plus lines:', plus.length);
 const cjk = plus.filter(l => /[\u4e00-\u9fff]/.test(l));
 console.log('cjk plus lines:', cjk.length);
 console.log(cjk.slice(0,5).join('\n'));
} catch (e) {
 console.log('FAILED:', e.message.slice(0,300));
}
