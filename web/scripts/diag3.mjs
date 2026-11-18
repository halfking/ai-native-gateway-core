import { execSync } from 'node:child_process';
const out = execSync('git diff ' + '3a3403c1e' + '..HEAD --unified=0 --no-color -- src/views/RequestLogsView.vue', { encoding: 'utf8' });
const lines = out.split('\n');
for (let i =0; i < Math.min(5, lines.length); i++) {
 console.log(i, JSON.stringify(lines[i].charAt(0)), lines[i].charCodeAt(0));
}
