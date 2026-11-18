//最后3处 v2：标题是 "PI错误分析"（带空格），分页是 "===0"（=两侧有空格）
import fs from 'fs';
const p = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/views/admin/PIAnalysisView.vue';
let s = fs.readFileSync(p, 'utf8');
const P = "common.piAnalysis.page.";
const subs = [
 ['<h1>PI错误分析</h1>', `<h1>{{ t('${P}title') }}</h1>`],
 [':disabled="queueOffset ===0">上一页</button>', `:disabled="queueOffset ===0">{{ t('${P}prevPage') }}</button>`],
 [':disabled="resultsOffset ===0">上一页</button>', `:disabled="resultsOffset ===0">{{ t('${P}prevPage') }}</button>`],
];
let missing = [];
for (const [old, nw] of subs) {
 if (!s.includes(old)) { missing.push(JSON.stringify(old)); continue; }
 s = s.replace(old, nw);
}
fs.writeFileSync(p, s);
console.log('replaced:', subs.length - missing.length, 'missing:', missing.length);
if (missing.length) console.log(missing.join('\n'));
