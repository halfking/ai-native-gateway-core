//导出 common.piAnalysis.page50键的 zh/en权威值工单
import fs from 'fs';
const zhMod = await import('/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/locales/zh-CN/common.ts');
const enMod = await import('/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/locales/en-US/common.ts');
const zh = zhMod.default.default ?? zhMod.default;
const en = enMod.default.default ?? enMod.default;
const jobs = Object.entries(zh.piAnalysis.page).map(([k, v]) => ({ key: k, zh: v, en: en.piAnalysis?.page?.[k] ?? '' }));
fs.writeFileSync('/tmp/job_pi_page.json', JSON.stringify(jobs, null,1));
console.log('jobs:', jobs.length, 'en missing:', jobs.filter(j => !j.en).length);
