//PIAnalysis剩余7处手工替换（真实缩进形态）
import fs from 'fs';
const p = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/views/admin/PIAnalysisView.vue';
let s = fs.readFileSync(p, 'utf8');
const P = "common.piAnalysis.page.";
const subs = [
 ["<h1>PI错误分析</h1>", `<h1>{{ t('${P}title') }}</h1>`],
 ["刷新\n", ` {{ t('${P}refresh') }}\n`],
 ["错误队列\n", ` {{ t('${P}tabQueue') }}\n`],
 [" 分析结果\n", ` {{ t('${P}tabResults') }}\n`],
 [":disabled=\"queueOffset ===0\">上一页</button>", `:disabled="queueOffset ===0">{{ t('${P}prevPage') }}</button>`],
 [":disabled=\"resultsOffset ===0\">上一页</button>", `:disabled="resultsOffset ===0">{{ t('${P}prevPage') }}</button>`],
 [" <th>错误类型</th>", ` <th>{{ t('${P}colErrorType') }}</th>`],
 [" <th>创建时间</th>", ` <th>{{ t('${P}colCreatedAt') }}</th>`],
 [" <th>操作</th>", ` <th>{{ t('${P}colActions') }}</th>`],
 [" 详情\n", ` {{ t('${P}detailButton') }}\n`],
];
let missing = [];
for (const [old, nw] of subs) {
 if (!s.includes(old)) { missing.push(JSON.stringify(old.slice(0,40))); continue; }
 s = s.replace(old, nw);
}
fs.writeFileSync(p, s);
console.log('replaced:', subs.length - missing.length, 'missing:', missing.length);
if (missing.length) console.log(missing.join('\n'));
