//对齐守卫口径，找「基线时不存在/计数0」与「基线后新增」的 CJK 文件
//守卫口径：src/**/*.{vue,ts,tsx,js,mjs}，排除 locales/i18n/design，排除 *.test/*.spec
import { execSync } from 'node:child_process';
import fs from 'fs';
import path from 'path';
import ts from 'typescript';

const CUR = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src';
const BASE = '/tmp/cjk-baseline-tree/web/src';
const EXCLUDED = new Set(['node_modules', 'dist', '.git', 'locales', 'i18n', 'design']);
const CJK = /[一-鿿]+/g;

function collect(dir) {
 const out = [];
 (function rec(d) {
 for (const e of fs.readdirSync(d, { withFileTypes: true })) {
 const p = path.join(d, e.name);
 if (e.isDirectory()) {
 if (EXCLUDED.has(e.name) || e.name.startsWith('.')) continue;
 rec(p);
 } else if (e.isFile() && /\.(vue|ts|tsx|js|mjs)$/.test(e.name)
 && !/\.test\.[cm]?[jt]s?$/.test(e.name)
 && !/\.spec\.[cm]?[jt]s?$/.test(e.name)) {
 out.push(p);
 }
 }
 })(dir);
 return out;
}

//剥离注释（简版守卫同款思路：TS 解析器剥 script 注释 + html 注释 + 单行 // 注释）
//对 vue：剥离 <!-- --> 与 script 内注释。此处用近似：直接数「模板属性/文本节点中的CJK」。
//为对齐守卫总数，只做相对比较：同算法跑两棵树。
function stripComments(src, file) {
 let s = src;
 if (file.endsWith('.vue')) {
 s = s.replace(/<!--[\s\S]*?-->/g, m => m.replace(/[^\n]/g, ' '));
 }
 try {
 const sf = ts.createSourceFile(file, s, ts.ScriptTarget.Latest, true, file.endsWith('.vue') ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
 const ranges = [];
 sf.statements.forEach(n => {});
 //用 lexer trivia 方式太复杂；退而求其次：行级 // 与块注释
 s = s.replace(/\/\/[^\n]*/g, m => m.replace(/[^\n]/g, ' '));
 s = s.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
 } catch (e) {}
 return s;
}

function countTree(root) {
 const files = collect(root);
 const m = new Map();
 let total =0;
 for (const f of files) {
 const rel = path.relative(root, f);
 const src = stripComments(fs.readFileSync(f, 'utf8'), f);
 const n = (src.match(CJK) || []).length;
 m.set(rel, n);
 total += n;
 }
 return { total, m };
}

const cur = countTree(CUR);
const base = countTree(BASE);
console.log('cur total:', cur.total, 'base total:', base.total);
const rel = [];
for (const [f, n] of cur.m) {
 const b = base.m.get(f) ?? 0;
 if (n > b) rel.push({ f, n, b, d: n - b });
}
rel.sort((a, b) => b.d - a.d);
console.log('=== 新增 CJK 段数 Top（守卫近似口径） ===');
for (const r of rel.slice(0, 30)) console.log(r.d, r.f, '(' + r.b + '→' + r.n + ')');
let sum =0;
for (const r of rel) sum += r.d;
console.log('sum delta:', sum);
