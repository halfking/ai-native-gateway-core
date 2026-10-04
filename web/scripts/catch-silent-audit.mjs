// catch-silent-audit.mjs —— 量化「静默吞错」的 catch 块。
//
// ## 为什么要有这个脚本
//
// 第五轮审计遗留项写着「views 下约 100 处 catch {}，数据加载类静默吞错的
// 数量未核查」。那个「约 100」是 grep 出来的**文本出现次数**，不是缺陷数：
// 它把 `catch (e) { notify(e) }` 这种有处理的块也算进去，把同一函数里的
// 多处算成多处。
//
// 2026-10-03 实测（AST，非文本）：
//   views 下 123 个源文件，catch 块 471 个（文本次数 504 —— 差值来自注释/字符串）
//   其中空 catch{}            47
//   无任何日志/用户可见输出     153（见下「第二次口径修正」）
//   且属数据加载类函数名        46
//
// 「约 100」与实测差了两倍以上，且方向不明 —— 这正是不能把散文当基线的原因。
//
// ## 判据口径：三类信号，不是一类
//
// 第一版只有一个 VISIBLE 白名单，把三种性质不同的东西混在一起判：
//
//	SELF_VISIBLE（自证可见）—— **调用它就产生了用户可见输出**，无需再查：
//	    console.error/warn、ElMessage.error/warning、alert(、notify/toast。
//	STATE（只写了状态）—— 写了个 error 变量/状态，**页面到底渲不渲染是另一件事**。
//	    这一类绝不能算「已处理」：本轮实测里就有一处
//	    `setTabError('pricing', …)` 的消费者写成了 `text="{{ tabErrors.pricing }}"`,
//	    模板里那串花括号是**字面量**，用户看到的是坏掉的模板而不是错误信息。
//	SILENT（静默）—— 上面两类都没有。
//
// ## 第二次口径修正（2026-10-03 晚，量具自身的缺陷）
//
// 第一版的 VISIBLE 里写的是 `message\s*\.\s*(error|warning)` —— **小写 m**。
// 而本仓可见输出压倒性地走 Element Plus 的 `ElMessage.error`（**大写 M**），
// 正则大小写敏感 ⇒ 23 处「已经有 toast 提示」的 catch 被报成静默。
// 同批还漏了 9 处 `setError(...)`（ProxyView，有 error-banner 消费者）与
// 1 处 `setTabError(...)`（消费者是坏的）。
//
// 即「186 处静默」里有 33 处是**量具的假阳性**，真实静默 153 处。
// 这与本轮已记的另外三处同型：量具停在错误的位置，而报告长得像结论。
// ⇒ 引用这个基线前先看 `--selfcheck`：它会证明新词表是**承重**的。
//
// ## 用法
//
//   node scripts/catch-silent-audit.mjs            # 统计 + 明细
//   node scripts/catch-silent-audit.mjs --summary  # 只出统计
//   node scripts/catch-silent-audit.mjs --selfcheck # 证明词表真的生效
//
// 注意：本脚本**是量具不是门**。它不给退出码，因为「静默」在很多地方是
// 有意为之（persistTab 写 localStorage 失败不值得打扰用户）。
// 修哪些由人判断 —— 前提是数字是真的。

import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';

const ROOT = path.resolve('src/views');
const SUMMARY_ONLY = process.argv.includes('--summary');

// 加载类函数名前缀 —— 这些的 catch 会让页面「静默停在旧数据/空数据」。
const LOADER = /^(load|fetch|get|query|refresh|reload|apply|sync|init|ensure|open|list)/i;

// 「这是一个错误态」的名字词根。刻意放宽到 stale/fail：只认 err 会漏掉
// `bgStatusStale` 这类命名，而漏认的后果是「已修的站点仍被算进真静默」。
const ERRORISH = /(err|stale|fail)/i;

/**
 * 跨作用域已处理的豁免表（`HANDLED_ELSEWHERE`，三种机制见其定义处）。
 *
 * ## 为什么需要它
 *
 * `for (const n of list) { try {...} catch { failed.push(n) } }`
 * 这种形状里，catch 块内**确实**没有可见输出，但失败并没有被丢：
 * 它被记进 `failed`，横幅在循环之后统一写。分类器只看 catch 块，
 * 于是把这两处报成「真静默」——而它们本轮已经修好了。
 *
 * ⇒ **真静默这个数字是上界**，除非豁免表把它们说清楚。
 * 登记而不是加规则：这类形状可以一眼看出，规则不行。
 * 与 admin/degrade_marker_test.go 的 degradeMarkerOutOfScope 同一设计。
 */
// 豁免表：catch 块**自身**静默，但失败确实到了用户 —— 只是路径跨出了 catch 块。
//
// ⚠️ 表名必须说真话。第一版叫 `DEFERRED_REPORT`（延迟报告），字面上只承认
// 「循环后统一报告」一种机制，于是往里加第二条时，名字开始骗人：
// 表混着**三种性质不同**的机制，而「延迟报告」只描述其中一种。
// 名字骗人的豁免表会退化成一段没人核的散文。
//
//   A 局部累加器：catch 只 push 进局部数组，块外统一写进一个状态
//   B 返回值即信号：catch `return false`，调用方据此换一句话
//   C 显式登记：形状一眼可辨，规则写不出来（与 degradeMarkerOutOfScope 同设计，
//     两侧都卡：表项失效即 exit 7）
//
// 每条都必须写清是**哪一种**，以及守它的那份判据在哪个文件里。
// 没有判据的豁免 = 散文，不予登记。
const HANDLED_ELSEWHERE = {
  'ProbeHealthDetailView.vue:loadMonitor': {
    mech: 'A 局部累加器',
    why: 'catch 内只把失败记进 failedCreds，横幅在循环之后统一写',
    judge: 'ProbeHealthDetailView.tabError.test.ts',
  },
  'RequestLogsView.vue:loadCredentialOptions': {
    mech: 'A 局部累加器',
    why: 'failedProviders 在 Promise.all 之后统一转成 filterOptionsError + 横幅',
    judge: 'requestFilterOptions.silentcatch.test.ts',
  },
  'UsageTrendExplorer.vue:loadFilterOptions': {
    mech: 'A 局部累加器',
    why: '两个来源各自 push 进 problems，块外统一写 filterOptionsError + .ute__filter-error 横幅',
    judge: 'partialAggregate.silentcatch.test.ts',
  },
  'TurnsListView.vue:refreshSessionSummary': {
    mech: 'B 返回值即信号',
    why: 'catch return false，调用方 resummarize 据此说「已触发但新归纳没取回来」而不是「已触发重新归纳」',
    // ⚠️ 这条判据一开始指错了文件（指向 silentLoadFalseTruth，那份根本不含
    // TurnsListView），是反向对照把它打回来的：把 `refreshed ?` 换成 `true ?`
    // 时没有任何判据报红。⇒ 豁免登记不是「我修过了」，是「判据咬得住」。
    judge: 'turnsResummarizeStale.silentcatch.test.ts',
  },
};

// 用户或运维能看见的失败信号 —— **只认「调用即产生输出」的那一类**。
//
// 大小写是硬伤：2026-10-03 第一版写 `message\s*\.\s*(error|warning)`（小写 m），
// 本仓实际用的是 Element Plus 的 `ElMessage.error`（大写 M）⇒ 23 处假阳性。
// 这里改成大小写不敏感的 message 家族，并由 --selfcheck 证明它是承重的。
const SELF_VISIBLE = new RegExp([
  'console\\s*\\.\\s*(error|warn)',
  'notify', 'toast', 'globalMsg',
  // ElMessage / Message / elMessage / $message —— 一律认，不再靠大小写赌
  '(?:el)?message\\s*\\.\\s*(error|warning)',
  '\\$message', 'alert\\(',
].join('|'), 'i');

// 第一版的旧词表，保留只为 --selfcheck 能证明「新词表真的更准」。
const LEGACY_VISIBLE = new RegExp([
  'console\\s*\\.\\s*(error|warn)',
  '[Ee]rror\\s*\\.\\s*value',
  '[Ee]rr\\s*\\.\\s*value',
  'notify', 'toast', 'globalMsg',
  'message\\s*\\.\\s*(error|warning)',
  '\\$message', 'alert\\(',
].join('|'));

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (/\.(vue|ts|tsx)$/.test(p)) out.push(p);
  }
  return out;
}

// .vue 的 <script setup> 必须先按块投影成独立 TS 单元再交给解析器。
// 2026-10-03 第一版直接喂整份 .vue，views 下只解析出 11 个 catch ——
// 把「122 个文件里有 11 处问题」这个结论差点当成事实写进报告。
// 量具选错总体，报出来的数就长得像结论。
function scriptOf(file) {
  const raw = fs.readFileSync(file, 'utf8');
  if (!file.endsWith('.vue')) return raw;
  const m = raw.match(/<script[^>]*>([\s\S]*?)<\/script>/);
  return m ? m[1] : null;
}

function enclosingName(node) {
  for (let p = node.parent; p; p = p.parent) {
    if ((ts.isFunctionDeclaration(p) || ts.isMethodDeclaration(p)) && p.name) {
      return p.name.getText();
    }
    if (ts.isVariableDeclaration(p) && ts.isIdentifier(p.name) && p.initializer
        && (ts.isArrowFunction(p.initializer) || ts.isFunctionExpression(p.initializer))) {
      return p.name.getText();
    }
  }
  return '?';
}

const files = walk(ROOT);

/**
 * catch 块里被写入的状态。
 *
 * 只认两种形态：`(ref).value = …` 的 ref 名，和**符合 setter 命名约定**的调用
 * （`^set[A-Z]`）。第一版还把「名字里含 Error 的任意调用」都算状态，于是
 * `formatCredentialError(...)`（纯格式化）和 `Error(`（构造/类型）都被当成了
 * 状态名，报出「模板里没有消费者」这种查下去根本不存在的结论。
 * 判据宁可漏报也不要把查不下去的东西报成缺陷。
 */
function stateNames(text) {
  const out = new Set();
  // ① 按**名字**词根（`err|stale|fail`）—— 覆盖 `bgStatusStale` 这类命名。
  //    与右值无关：`error.value = someMessage` 的右值不是字面量，也必须认出来。
  //    （第一版收紧时把它和下面那条并成一条循环，`error.value = e.message`
  //    就整体漏认了，真静默从 127 飙到 296 —— 两条规则是**并联**，不是串联。）
  for (const m of text.matchAll(/\b([A-Za-z_$][\w$]*)\s*\.\s*value\s*=/g)) {
    if (ERRORISH.test(m[1])) out.add(m[1]);
  }
  // ② 按**右值**：状态被**整个赋成**一个错误字面量。`msgKind.value = 'err'` 就是
  //    这一类 —— 它的名字 `msg` 完全不含 err，第一版因此把
  //    `CredentialModelsPanel.load` 报成「真静默」，而那一处其实有
  //    `msg` + `msgKind='err'` 的错误条并在模板里渲染。
  //    **右值比名字可靠**：名字是约定，右值是这一次赋值的语义。
  //    ⚠️ 必须是「整个右值就是一个字符串字面量」，不能是「右值里出现过某个
  //    含 err 的字面量」—— 后者会把多行赋值里无关的 `'failed'` 也算进来，
  //    实测把 canActivate / totalPages / statusTabs 全认成了错误态。
  for (const m of text.matchAll(/\b([A-Za-z_$][\w$]*)\s*\.\s*value\s*=\s*(['"`])([^'"`\n]*)\2\s*;?/g)) {
    if (/\b(err|error|fail|failed|stale|warn|warning)\b/i.test(m[3])) out.add(m[1]);
  }
  for (const m of text.matchAll(/\b(set[A-Z]\w*)\s*\(/g)) out.add(m[1]);
  return [...out];
}

/**
 * 把 setter 名解析成它真正写的那个 ref，再顺着 computed 找一层派生名。
 *
 * 为什么必须解析：catch 里写的是 `setError(tab, msg)`，状态藏在
 * `function setError` 的函数体里（`errorByTab.value[tab] = msg`），而模板
 * 渲染的又可能是 `const error = computed(() => errorByTab.value[...])`。
 * 第一版直接把 `setError(` 切成 `Error` 当状态名，于是把 ProxyView 那个
 * **确实存在**的 `error-banner` 报成「模板里没有消费者」——
 * 量具的假阳性比它要抓的缺陷更吵。
 */
function resolveStateNames(code, names) {
  const out = new Set();
  for (const n of names) {
    out.add(n);
    // computed 派生名：对**所有**解析出的 ref 都追一跳，不只 `set*`。
    // 第一版只追 setter，于是 `statsError.value = …` 这种直接赋值追不到渲染它的
    // computed（CompressionView 的 `loadError` 由三个 error 拼成）⇒ 把已经修好的
    // 「有消费者」报成「模板里没有消费者」。
    //
    // 窗口必须**卡在下一条 const 声明之前**：`[\s\S]{0,160}?` 一路惰性匹配时，
    // `const loading = computed(...)` 的窗口会把下一行的
    // `const error = computed(() => errorByTab…)` 一起吞了 —— 解析出 `loading`，
    // 真正的 `error` 反而因为起点被消耗而丢失。无语句边界的窗口是量具自己的洞。
    for (const c of code.matchAll(/\bconst\s+([A-Za-z_$][\w$]*)\s*=\s*computed\(/g)) {
      const rest = code.slice(c.index + c[0].length);
      const stop = rest.search(/\n\s*(?:const|let|var|function)\s/);
      const body = rest.slice(0, stop === -1 ? 240 : stop);
      if (new RegExp(`\\b${n}\\b`).test(body)) out.add(c[1]);
    }
    if (!/^set[A-Z]/.test(n)) continue;
    // setter 函数体里被写的 ref（`setError(...)` 的状态藏在函数体里）
    const fnRe = new RegExp(`(?:function\\s+${n}\\b|const\\s+${n}\\s*=\\s*(?:\\([^)]*\\)|\\w+)\\s*=>)[\\s\\S]{0,400}?\\b([A-Za-z_$][\\w$]*)\\s*\\.\\s*value`, 'm');
    const m = code.match(fnRe);
    if (!m) continue;
    out.add(m[1]);
    for (const c of code.matchAll(/\bconst\s+([A-Za-z_$][\w$]*)\s*=\s*computed\(/g)) {
      const rest = code.slice(c.index + c[0].length);
      const stop = rest.search(/\n\s*(?:const|let|var|function)\s/);
      const body = rest.slice(0, stop === -1 ? 240 : stop);
      if (new RegExp(`\\b${m[1]}\\b`).test(body)) out.add(c[1]);
    }
  }
  return [...out];
}

/**
 * 该状态在 .vue 模板里到底有没有**真绑定**。
 *
 * 「设了状态」与「用户看得见」之间隔着一个模板绑定：STATE 类不算已处理。
 * 本轮实测命中一个真实缺陷 —— `setTabError('pricing', …)` 的消费者写成
 * `text="{{ tabErrors.pricing }}"`，花括号在引号里是**字面量**，
 * 用户看到的是一串花括号而不是错误信息。
 */
function bindingKind(template, names) {
  if (!template) return '无模板';
  if (names.length === 0) return 'n/a';
  // 剥注释放在**函数内部**：修复说明的注释里会原样写出被修的那段
  // （`text="{{ tabErrors.pricing }}"`）。放在调用方剥，任何直接调用
  // bindingKind 的新入口都会重新踩这个洞。
  let bound = false, literal = false;
  let rest = template.replace(/<!--[\s\S]*?-->/g, ' ');
  for (const n of names) {
    if (!new RegExp(`(?<![\\w$])${n}\\b`).test(template)) continue;
    // 先摘掉「引号里的花括号」形态：`attr="{{ x }}"` 不参与插值，
    // 若不先摘掉，它会被下面的插值规则当成绑定 ⇒ 明明是字面量误用却判成 ok。
    // 顺序反了，这个洞就在（合成地锚专门守它）。
    const litRe = new RegExp(`\\w[\\w-]*\\s*=\\s*"\\{\\{[^}]*\\b${n}\\b[^}]*\\}\\}"`, 'g');
    if (litRe.test(rest)) literal = true;
    rest = rest.replace(litRe, ' ');
    // 插值：既包括 {{ n }}，也包括 {{ t('…', { n }) }} —— 经 i18n 参数传进去的
    // 同样会被用户看见。第一版只认「紧跟花括号」那一种，把 AgentRegistryView
    // 的 `{{ t('…loadError', { err: statsError }) }}` 报成了没有消费者。
    if (new RegExp(`\\{\\{[^}]*\\b${n}\\b`).test(rest)) bound = true;
    if (new RegExp(`[:@]\\w[\\w-]*\\s*=\\s*"[^"]*\\b${n}\\b`).test(rest)) bound = true; // 属性绑定
  }
  if (bound) return literal ? 'ok（另有字面量误用）' : 'ok';
  if (literal) return '字面量误用';
  return '模板里没有消费者';
}

function templateOf(file) {
  if (!file.endsWith('.vue')) return null;
  const raw = fs.readFileSync(file, 'utf8');
  const m = raw.match(/<template>([\s\S]*)<\/template>/);
  if (!m) return '';
  // 注释必须先剥掉：修复说明的注释里会**原样写出**被修的那段
  // （`text="{{ tabErrors.pricing }}"`）。不剥的话，已经修好的字面量误用
  // 会继续被报成缺陷 —— 本轮实测：修完之后仍然显示「另有字面量误用」。
  // 与本文件记过的「选择器命中模板注释」是同一条纪律。
  return m[1].replace(/<!--[\s\S]*?-->/g, '');
}

/**
 * 独立数一遍 catch 块，用途是**证明遍历没漏**。
 *
 * 为什么需要：分类逻辑里任何一次提前 return 都会让 ts.forEachChild 不再下潜，
 * 嵌套 catch 会被静默漏掉 —— 而总数变小这件事在报告里长得像「数据变了」，
 * 不像「量具坏了」。本轮实测踩过一次：加完早退后 catch 块从 471 掉到 469。
 * 两个数字必须相等，不等就是遍历有洞。
 */
function countCatches(node) {
  let n = ts.isCatchClause(node) ? 1 : 0;
  ts.forEachChild(node, (c) => { n += countCatches(c); });
  return n;
}

/**
 * 按给定词表跑一遍全量分类。
 *
 * 抽成函数是给 --selfcheck 用的：它必须用**同一个总体、同一套规则**跑两遍，
 * 只换 SELF_VISIBLE。第一次 selfcheck 就是在循环外面手写了一个「代理判定」，
 * 而代理与被代理的规则不同源 —— 比的是另一个东西，数字当然对不上。
 */
function classify(selfVisibleRe) {
  let total = 0, empty = 0, parsed = 0, selfVisible = 0, stateOnly = 0, silent = 0;
  const rows = [];
  const deferredSeen = {};
  for (const file of files) {
  const code = scriptOf(file);
  if (!code || !code.trim()) continue;
  const sf = ts.createSourceFile(file, code, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  parsed++;
  const expected = countCatches(sf);
  let seen = 0;
  const visit = (node) => {
    if (ts.isCatchClause(node)) {
      // 注意：**这里不能提前 return**。return 会跳过下面的 forEachChild，
      // 嵌套的 catch 就再也数不到，总数会静默变小（见 countCatches 的注释）。
      seen++;
      total++;
      const isEmpty = node.block.statements.length === 0;
      if (isEmpty) empty++;
      const text = node.block.getText(sf);
      const fn = enclosingName(node);
      const base = {
        file: path.relative(process.cwd(), file),
        line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1,
        fn,
        stmts: node.block.statements.length,
        empty: isEmpty,
        loader: LOADER.test(fn),
        test: file.endsWith('.test.ts'),
      };
      if (selfVisibleRe.test(text)) {
        selfVisible++;
      } else {
        const names = resolveStateNames(code, stateNames(text));
        const deferredKey = `${path.basename(file)}:${fn}`;
        if (HANDLED_ELSEWHERE[deferredKey]) {
          // 失败被记账了，只是报告路径跨出了 catch 块 —— 不算「真静默」
          stateOnly++;
          deferredSeen[deferredKey] = (deferredSeen[deferredKey] || 0) + 1;
          rows.push({ ...base, kind: 'DEFERRED', states: `（${HANDLED_ELSEWHERE[deferredKey].mech}）`, binding: 'n/a' });
        } else if (names.length > 0) {
          stateOnly++;
          rows.push({ ...base, kind: 'STATE', states: names.join(','), binding: bindingKind(templateOf(file), names) });
        } else {
          silent++;
          rows.push({ ...base, kind: 'SILENT' });
        }
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  if (seen !== expected) {
    console.error(`遍历有洞：${file} 独立计数 ${expected}，实际访问 ${seen}`);
    process.exit(3);
  }
}

  // 豁免表两边都要卡：表项失效（那个函数已经没有跨作用域处理的 catch 了）就红，
  // 否则它会变成一段没人核的散文。
  for (const [k, e] of Object.entries(HANDLED_ELSEWHERE)) {
    if (!deferredSeen[k]) {
      console.error(
        `HANDLED_ELSEWHERE 列了 ${k}，但它已不再有「${e.mech}」的 catch（判据：${e.judge}）：${e.why}`,
      );
      process.exit(7);
    }
    // 「没有判据的豁免 = 散文」这句不能只是口号：把判据文件的存在性也卡住。
    // 否则某天判据被删了，豁免还在，量具照样把这一处算成「已处理」。
    // ⚠️ ROOT 已经是 src/views，别再拼一层 'src' —— 这个门当场抓到了自己。
    const judgePath = e.judge ? path.resolve(ROOT, e.judge) : '(未声明 judge)';
    if (!e.judge || !fs.existsSync(judgePath)) {
      console.error(`HANDLED_ELSEWHERE[${k}] 声明的判据不存在：${judgePath}`);
      process.exit(8);
    }
  }
  return { total, empty, parsed, selfVisible, stateOnly, silent, rows, deferredSeen };
}

const main = classify(SELF_VISIBLE);
const { total, empty, parsed, silent, selfVisible, stateOnly, rows } = main;
const prod = rows.filter((r) => !r.test);
const prodLoaders = prod.filter((r) => r.loader && r.kind === 'SILENT');

// --selfcheck：证明新词表是**承重**的，否则上面那个数字依然是「量具说没问题」。
// 做法是把新词表退回第一版，数一数静默数会不会回升 —— 不回升就说明这次改动
// 什么都没改变（这正是本轮吃过的一次亏：变异静默未发生却被当成验证通过）。
if (process.argv.includes('--selfcheck')) {
  // 变异 = 抽掉 ElMessage 家族（退回第一版的**小写 m** 形态）。
  // 这里**不能**给整条正则加 i 标志：第一版那次的 selfcheck 就是这么写的，
  // 结果变异根本没发生（`message\.` 在 i 标志下照样匹配 ElMessage.error）。
  const noCaseFix = new RegExp([
    'console\\s*\\.\\s*(error|warn)',
    'notify', 'toast', 'globalMsg',
    'message\\s*\\.\\s*(error|warning)', // 小写 m = 修正前
    '\\$message', 'alert\\(',
  ].join('|')); // 全小写 ⇒ 不需要 i
  const neu = classify(SELF_VISIBLE);
  const old = classify(noCaseFix);
  console.log(`--selfcheck：同一总体跑两遍 —— 自证可见 新 ${neu.selfVisible} / 旧 ${old.selfVisible}；真静默 新 ${neu.silent} / 旧 ${old.silent}`);
  console.log(`  总体核对：新 ${neu.total} / 旧 ${old.total}（必须相等，不等说明变异改了遍历）`);
  if (neu.total !== old.total) {
    console.error('selfcheck 失败：两次分类的 catch 总数不同 —— 变异动到了遍历本身');
    process.exit(4);
  }
  // 断言的是**分类归属**而不是静默总数：大小写修正把 23 处「已有 toast」的
  // catch 从「非自证」挪进「自证可见」；真静默总数可能不变（新的 stateNames
  // 也会认 `error(`，两条修正有重叠），所以拿 silent 当判据会误报失败。
  if (old.selfVisible >= neu.selfVisible) {
    console.error('selfcheck 失败：大小写修正没有改变任何分类 —— 本次修正没有承重，数字不可信');
    process.exit(2);
  }
  // 地锚：两个**人工核过**的样本，判定必须与事实一致。
  // 只有「数字变了」不够 —— 变的方向也得对。
  const anchors = [
    ['ProxyView.vue', 'ok'],                        // 422 行确有 error-banner
    // 原来这条地锚写的是「ok（另有字面量误用）」——它编码了
    // **缺陷还在**的旧预期。缺陷修掉后它就该变成 'ok'，否则地锚就成了
    // 「必须保持坏」的门。字面量误用的识别能力改由下面的合成样本守住。
    ['ProbeHealthDetailView.vue', 'ok'],
    ['AgentRegistryView.vue', 'ok'],                // 369 行经 i18n 参数渲染
  ];
  for (const [file, want] of anchors) {
    const hit = neu.rows.find((r) => r.file.endsWith(file) && r.kind === 'STATE');
    if (!hit) { console.error(`selfcheck 失败：${file} 不再出现在 STATE 分类里，分类规则变了`); process.exit(5); }
    if (hit.binding !== want) {
      console.error(`selfcheck 失败：${file} 的 binding 判定是「${hit.binding}」，人工核实应为「${want}」`);
      process.exit(5);
    }
    console.log(`  地锚通过：${file} → ${hit.binding}（states=${hit.states}）`);
  }
  // 合成反向样本：字面量误用必须被认出来，且不能被模板注释骗过。
  const lit = bindingKind('<span b="{{ x }}"></span>', ['x']);
  if (lit !== '字面量误用') {
    console.error(`selfcheck 失败：引号里的花括号应判为「字面量误用」，实得「${lit}」`);
    process.exit(6);
  }
  const notLit = bindingKind('<!-- <span b="{{ x }}"></span> --><span :b="x"></span>', ['x']);
  if (notLit !== 'ok') {
    console.error(`selfcheck 失败：模板注释里的字面量不该被算作缺陷，实得「${notLit}」`);
    process.exit(6);
  }
  console.log('  地锚通过：字面量误用可识别，且模板注释不再误报');
  console.log('selfcheck 通过：新词表确实承重，且方向与人工核实一致。');
  process.exit(0);
}

console.log(`解析文件 ${parsed}/${files.length}`);
console.log(`views 下 catch 块: ${total}   空 catch: ${empty}`);
console.log(`自证可见 ${selfVisible}   只写了状态 ${stateOnly}   真静默 ${silent}`);
// 豁免表按机制分别报数，不报一个总数 —— 「4 处已处理」看不出这 4 处是不是
// 同一种形状，也看不出表里混进了新机制。
const byMech = {};
for (const k of Object.keys(main.deferredSeen)) {
  const m = HANDLED_ELSEWHERE[k].mech;
  byMech[m] = (byMech[m] || 0) + 1;
}
console.log(
  `（跨作用域已处理 ${Object.keys(main.deferredSeen).length} 处，已从真静默中排除：` +
    Object.entries(byMech).map(([m, n]) => `${m} ${n}`).join('、') +
    `）`,
);
console.log(`生产代码真静默: ${prod.filter((r) => r.kind === 'SILENT').length}   其中加载类: ${prodLoaders.length}`);

if (SUMMARY_ONLY) process.exit(0);

console.log('\n=== 生产代码：真静默 + 空块 ===');
for (const r of prod.filter((r) => r.kind === 'SILENT' && r.empty)) {
  console.log(`${r.file}:${r.line}\tfn=${r.fn}\t${r.loader ? 'LOADER' : ''}`);
}
console.log('\n=== 生产代码：真静默 + 非空块 + 加载类 ===');
for (const r of prodLoaders.filter((r) => !r.empty)) {
  console.log(`${r.file}:${r.line}\tfn=${r.fn}\tstmts=${r.stmts}`);
}
console.log('\n=== 只写了状态：消费者核对（binding≠ok 的是真缺陷候选）===');
for (const r of prod.filter((r) => r.kind === 'STATE' && r.binding !== 'ok' && r.binding !== 'n/a')) {
  console.log(`${r.file}:${r.line}\tfn=${r.fn}\tstates=${r.states}\tbinding=${r.binding}`);
}
console.log(`\n=== 生产代码：真静默 + 非空块 + 非加载类（${prod.filter((r) => r.kind === 'SILENT' && !r.empty && !r.loader).length} 处，多数为 persistTab 一类，可不动）===`);
