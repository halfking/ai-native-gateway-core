import { execSync } from 'node:child_process';
const BASE = '3a3403c1e';
//只测第一个文件，把每一步打出来
const cmd = 'git diff ' + BASE + '..HEAD --unified=0 --no-color -- src/views/RequestLogsView.vue';
console.log('about to run:', JSON.stringify(cmd));
let out = '';
try {
 out = execSync(cmd, { encoding: 'utf8', maxBuffer:50 *1024 *1024 });
 console.log('out len:', out.length);
} catch (e) {
 console.log('FAILED:', String(e.message).slice(0,200));
}
