import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
const f = 'src/views/RequestLogsView.vue';
const cmd = 'git diff ' + BASE + '..HEAD --unified=0 --no-color -- ' + f;
const out = execSync(cmd, { encoding: 'utf8' });
const lines = out.split('\n');
const plus = lines.filter(l => l.startsWith('+') && !l.startsWith('+++'));
console.log('plus:', plus.length);
const cjkRe = new RegExp('[\\u4e00-\\u9fff\\u3400-\\u4dbf]');
const cjk = plus.filter(l => cjkRe.test(l));
console.log('cjk:', cjk.length);
//也测字面量汉字类
const cjkLit = plus.filter(l => /[一-鿿]/.test(l));
console.log('cjk literal range:', cjkLit.length);
