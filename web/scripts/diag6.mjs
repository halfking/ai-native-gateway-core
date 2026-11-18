import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
const nameCmd = 'git diff ' + BASE + '..HEAD --name-only -- src/';
const files = execSync(nameCmd, { encoding: 'utf8' })
 .split('\n')
 .map(s => s.trim())
 .filter(f => /\.(vue|ts)$/.test(f) && f.length >0);
console.log('candidates:', files.length);
console.log('first3:', JSON.stringify(files.slice(0,3)));
//直接测第一个文件
const f0 = files[0];
const cmd = 'git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + f0;
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
