//标题最后一处：按行号直接替换（第4行）
import fs from 'fs';
const p = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/views/admin/PIAnalysisView.vue';
const lines = fs.readFileSync(p, 'utf8').split('\n');
if (!lines[3].includes('<h1>')) throw new Error('line4 mismatch: ' + lines[3]);
lines[3] = " <h1>{{ t('common.piAnalysis.page.title') }}</h1>";
fs.writeFileSync(p, lines.join('\n'));
console.log('done:', lines[3]);
