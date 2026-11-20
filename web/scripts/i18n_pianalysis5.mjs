//最后3处 v3：分页 "==0" 全形态（=间有空格），标题 "PI错误分析"
import fs from 'fs';
const p = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/views/admin/PIAnalysisView.vue';
let s = fs.readFileSync(p, 'utf8');
const P = "common.piAnalysis.page.";
const subs = [
 ['<h1>PI错误分析</h1>', `<h1>{{ t('${P}title') }}</h1>`],
 ['>上一页</button>', `>{{ t('${P}prevPage') }}</button>`],
 ['>下一页</button>', `>{{ t('${P}nextPage') }}</button>`],
];
let missing = [];
for (const [old, nw] of subs) {
 if (!s.includes(old)) { missing.push(JSON.stringify(old)); continue; }
 s = s.split(old).join(nw);
}
fs.writeFileSync(p, s);
console.log('replaced:', subs.length - missing.length, 'missing:', missing.length);
if (missing.length) console.log(missing.join('\n'));
