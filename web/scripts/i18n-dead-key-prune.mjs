#!/usr/bin/env node
// i18n-dead-key-prune.mjs — 语言包死键**安全批量删除器**（2026-10-27 第 13 增量）
//
// ★ 定位：配套 `i18n-dead-key-guard.mjs` 与 `i18n-shared-dead-count.mjs` ——
//   那两道门负责「判死」与「棘轮」，本工具负责「把判死的键安全地删掉」。
//   ⇒ 删活键不可逆 ⇒ 本工具内置**三道闸**（见下），任何一道不过就**还原文件**。
//
// ★ 本工具先后踩过 **14 个缺陷**，每一个都写在这里，因为它们各自都是一类可复用的坑：
//   ① `lineOf()` 每次 slice().split() ⇒ O(n²) ⇒ OOM ⇒ 改预计算行首 + 二分
//   ② `{` / `}` 分支漏 i++ ⇒ 死循环 OOM
//   ③ 空容器剪枝用「内部有叶子被删」推断「孩子全死」⇒ 连活着的兄弟键一起删
//      ⇒ 改「落盘后迭代式真空清理」（只删**现在确实没有孩子**的对象）
//   ④ prune 用逐个 splice ⇒ 删除后元素左移 ⇒ 跳过一个元素 ⇒ 括号不平衡
//      ⇒ 改「一次 splice 删整段」
//   ⑤ 叶子行不带逗号、逗号单独占一行 ⇒ 留下孤儿逗号 ⇒ 语法坏
//   ⑥ 对象闭合行不带逗号、逗号在下一行 ⇒ 同上
//   ⑦ 值比对未做完整反转义（TS 源码 '\n' 运行时是真换行）⇒ 误判「值不符」
//   ⑧ 值表是 Map 却按普通对象取 ⇒ 全 undefined
//   ⑨ 数组型键用 `val !== want`（**引用相等**）⇒ 数组必判「不符」⇒ 删不净
//      ⇒ 改深度相等
//   ⑩ **引号键**（`'24h': '24h'`）/ **数字开头键**（`400: '…'`）解析器不认
//      ⇒ ① 计划里的这类键报「节点找不到」；
//      ⇒ ② ★★ 更危险：父容器被判「真空」⇒ **剪枝把仍含活内容的整块删掉**
//      ⇒ 修：引号 token 后面是 `:` ⇒ 它是**键**；标识符起始字符集加数字
//   ⑪ ★★ **一行多键**（`a: 'x', b: 'y',`）—— 行级删除会把**同行的兄弟键一起删掉**
//      ⇒ 实测 zh-TW/dashboard.ts 一次就带走 8 个未申报的活键
//      ⇒ ★★ 解法：删除从**行级**改成**字符区间级**
//   ⑬ ★★ **吃掉行尾换行的条件错了** ⇒「同一行还有别的键」时也吃换行 ⇒ **下一行被粘上来**
//      ⇒ 修正为「**整行都会变空时**才吃换行」。
//   ⑭ ★★ **行尾注释会变成孤儿**（`subtitle: '…', // brand "开轩" kept consistent`）
//      ⇒ 键被删后注释变成孤零零一行，而它描述的键已不存在 ⇒ 误导后来人
//      ⇒ 修正为「**注释随它描述的键一起走**」。
//
// ★★ 另有一处**工具之外**的坑：批量删除后**棘轮门会暂时变红**
//   （删除进行中计数高于**最终**基线）⇒ ⇒ **棘轮门不进「每批」门列表**，
//   ★★ 只在删除**结束后**下调基线再跑。
//
// ★ 本轮最值钱的结构性改进（三道闸）：
//   A. **解析器 ↔ 运行时真值对拍**（`--check-parser` + 逐文件前置闸）
//      ⇒ 剪枝判据的可信度不再靠约定，而是**逐字相等**的可证伪条件
//   B. **硬不变量**：剪枝后存活叶子集 == 原叶子集 − 申报删除集
//      ⇒ 任何「多删」都被当场抓住并还原文件
//   C. **落盘后语法自检**：坏文件绝不留在工作区
//
// ★ 分批跑批纪律（本轮实战得来的，**别再空转**）：
//   ① **每批独立验证**：删完一批就跑全套门 + 语法全检 + 8 语种键集合一致
//      ⇒ 出问题能定位到具体批次（而不是「最后发现不对」）。
//   ② **无进展即停**：每轮重新导出守门报告，比对删前/删后的 sharedDead 计数；
//      **待删键数没下降 ⇒ 立刻停机**。
//      ★ 本轮曾因判据写反而「每轮都被拦下 ⇒ 计划不变 ⇒ 空转 16 轮」，
//      ★ 日志上却一路显示「验证: ✔ 全绿」—— 看起来在推进，实际一个键都没删。
//   ③ **计划要每批重算**：不用上一批的旧计划。
//
// ★ 另一个坑（bash）：**变量后面紧跟中文时必须写 \`\${VAR}\`** ——
//   bash 会把 \`$VAR\` 后面的多字节 UTF-8 吞进变量名 ⇒ \`set -u\` 下报「未绑定的变量」。
//
// 用法：node scripts/i18n-dead-key-prune.mjs <plan.json> [--apply] [--ns <命名空间>] [--check-parser]
//   plan.json: { "<namespace>": ["key.path", ...], ... }   ← 命名空间 = 语言包模块名
//
// ★★★ 2026-10-26 订正（12h 审计轮）：**默认必须是只读**。
//   ★ 原实现是 `const dryRun = argv.includes('--dry-run')` ⇒ **不给参数就落盘**
//   ⇒ 而同族的 `fix-locale-dead-keys.mjs` 用的是相反约定（`--apply` 才动手）
//   ⇒ ⇒ **同一个仓里两个删除工具的默认方向相反** ⇒ 一个手滑的调用就改工作树。
//   ★⇒ 改成 `--apply` 才写（与同族一致）⇒ 误调用退化成「什么都没做」。
//   ★ 本工具虽是**安全**的（删完做语法自检、坏掉就还原、叶子集不变式兜底），
//   ★★ 但「安全」不等于「该默认动手」——★ 破坏性工具的默认值应该是**保守**的。
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve, join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const LOCALES = ['zh-CN', 'zh-TW', 'ja-JP', 'en-US', 'de-DE', 'es-ES', 'fr-FR', 'ar-SA'];
// ★ 值表来自**死键门自己**（computeDeadKeyFacts.perLocaleValues）⇒ 与删除口径同源。
//   （用 localeValueQuality 的 loader 会漏掉**数组型键** ⇒ 实测
//     `sessions.list.tableHeaders` 是数组 ⇒ loader 说没有 ⇒ 删不掉 ⇒ 8 语种结构不一致）
const guard = await import(join(ROOT, 'scripts/i18n-dead-key-guard.mjs'));
const scanner = await import(join(ROOT, 'src/i18n/scanner.ts'));
const facts = await guard.computeDeadKeyFacts(scanner.loadLocale);
const localesDir = join(ROOT, 'src/locales');

const argv = process.argv.slice(2);
const planFile = argv.find((a) => !a.startsWith('--'));
// ★★★ 2026-10-26：默认**只读**，必须显式 `--apply` 才落盘（同族约定一致）。
//   ★ 保留 `--dry-run` 作为**显式**的只读声明（向后兼容；它等价于不写）。
const apply = argv.includes('--apply');
const dryRun = !apply || argv.includes('--dry-run');
const checkParser = argv.includes('--check-parser');
const nsFilter = argv.includes('--ns') ? argv[argv.indexOf('--ns') + 1] : null;
const plan = planFile ? JSON.parse(readFileSync(planFile, 'utf8')) : {};

/**
 * 解析一个语言包模块文件，产出节点树。
 * 节点: { path: string[], kind: 'leaf'|'object', keyStart, valueEnd, ... }
 *
 * ★★ 本解析器的**可信度**决定剪枝判据的可信度 ⇒ 必须与运行时真值**逐字相等**
 *   （见 `crossCheckParser`）⇒ 所以它要认全：引号键、数字开头键、数组值、
 *   一行多键、跨行值。
 */
function parseModule(src) {
  const nodes = new Map();          // path.join('.') -> node
  // ★ 行号查找：预计算行首数组 + 二分（原来每次都 slice().split() ⇒ O(n²) ⇒ 直接 OOM）
  const lineStarts = [0];
  for (let i = 0; i < src.length; i++) if (src[i] === '\n') lineStarts.push(i + 1);
  const lineOf = (idx) => {
    let lo = 0, hi = lineStarts.length - 1;
    while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (lineStarts[mid] <= idx) lo = mid; else hi = mid - 1; }
    return lo + 1;
  };
  let i = 0;
  let stack = [];                  // 当前对象栈
  let pendingKey = null;            // 待进入 {} 的键
  let pendingKeyStart = -1;         // ★ 待进入 {} 的键的**字符起点**（字符级删除要用）
  let pendingKeyLine = 0;
  const root = { path: [], kind: 'object', openLine: 1, closeLine: 0, keyStart: -1, children: [], parent: null };
  stack.push(root);

  const pathOf = (p) => p.join('.');

  let __it = 0;
  while (i < src.length) {
    if (++__it > 2000000) { console.error('★ 死循环 offset=' + i + ' 上下文=' + JSON.stringify(src.slice(Math.max(0,i-120), i+60))); throw new Error('infinite loop'); }
    const ch = src[i];
    if (ch === '\n') { i++; continue; }
    if (ch === '/' && src[i + 1] === '/') { while (i < src.length && src[i] !== '\n') i++; continue; }
    if (ch === '/' && src[i + 1] === '*') {
      i += 2;
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++;
      i += 2; continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      const q = ch; const start = i; i++;
      while (i < src.length) {
        if (src[i] === '\\') { i += 2; continue; }
        if (src[i] === q) { i++; break; }
        i++;
      }
      const raw = src.slice(start, i);
      // ★★ 缺陷 ⑩：**引号键**（`'24h': '24h'` / `'user.create': '…'` / `'5m': '…'`）
      //   仓内语言包实测有 168 处这种写法（8 语种）。
      //   原实现只把引号 token 当「值」⇒ **从不登记为键** ⇒
      //   ① 计划里的 `…overview.24h` 报「节点找不到」；
      //   ② ★★ 更危险：父容器被判「真空」⇒ **剪枝把仍含活内容的整块删掉**。
      //   ⇒ 正解：**看引号 token 后面是不是 `:`** ⇒ 是则它是**键**，否则才是**值**。
      {
        let k = i;
        while (k < src.length && /\s/.test(src[k])) k++;
        if (src[k] === ':') {
          const inner = raw.slice(1, -1).replace(/\\'/g, "'").replace(/\\\\/g, '\\');
          pendingKey = inner;
          pendingKeyStart = start;
          pendingKeyLine = lineOf(start);
          continue;
        }
      }
      // 若这是某个键的值 ⇒ 记录叶子
      if (pendingKey !== null) {
        const parent = stack[stack.length - 1];
        if (!parent) {
          throw new Error('parse: 栈空（内容括号不平衡）offset=' + i + ' ctx=' + JSON.stringify(src.slice(Math.max(0, i - 140), i + 40)));
        }
        const p = [...parent.path, pendingKey];
        nodes.set(pathOf(p), { path: p, kind: 'leaf', keyStart: pendingKeyStart, valueEnd: i, openLine: pendingKeyLine, closeLine: lineOf(i - 1), valueLine: pendingKeyLine, rawValue: raw, parent });
        parent.children.push(p);
        pendingKey = null;
        pendingKeyStart = -1;
      }
      continue;
    }
    if (ch === '[') {
      // ★ 数组型键（如 `tableHeaders: ['会话 ID', ...]`）—— 仓内语言包确实有。
      const arrStart = i;
      let depth = 0;
      while (i < src.length) {
        const c = src[i];
        if (c === "'" || c === '"' || c === '`') {
          const q = c; i++;
          while (i < src.length) { if (src[i] === '\\') { i += 2; continue; } if (src[i] === q) { i++; break; } i++; }
          continue;
        }
        if (c === '[') depth++;
        else if (c === ']') { depth--; if (depth === 0) { i++; break; } }
        i++;
      }
      if (pendingKey !== null) {
        const parent = stack[stack.length - 1];
        const p = [...parent.path, pendingKey];
        const rawArr = src.slice(arrStart, i);
        nodes.set(pathOf(p), { path: p, kind: 'leaf', keyStart: pendingKeyStart, valueEnd: i, openLine: pendingKeyLine, closeLine: lineOf(i - 1), valueLine: pendingKeyLine, rawValue: rawArr, parent });
        parent.children.push(p);
        pendingKey = null;
        pendingKeyStart = -1;
      }
      continue;
    }
    if (ch === '{') {
      const parent = stack[stack.length - 1];
      const p = pendingKey !== null ? [...parent.path, pendingKey] : parent.path;
      const node = { path: p, kind: 'object', keyStart: pendingKey !== null ? pendingKeyStart : i, openBrace: i, closeBrace: -1, openLine: lineOf(i), closeLine: 0, children: [], parent };
      nodes.set(pathOf(p), node);
      if (pendingKey !== null) parent.children.push(p);
      stack.push(node);
      pendingKey = null;
      pendingKeyStart = -1;
      i++;
      continue;
    }
    if (ch === '}') {
      const node = stack.pop();
      if (node) { node.closeBrace = i; node.closeLine = lineOf(i); }
      i++;
      continue;
    }
    if (/[0-9]/.test(ch) && pendingKey !== null) {
      // ★★ 缺陷 ⑫：**值的形态不止字符串** —— `amount: 2900` 是**数字字面量**
      //   （仓内实测 `public.support.tiers.coffee.amount` 等 4 个键 × 8 语种）。
      //   原实现只认「引号 token / 数组 / 对象」三种值 ⇒ **数字值从不被登记**
      //   ⇒ 该键不是叶子 ⇒ ① 计划里报「节点找不到」；② 父容器被判「真空」
      //   ⇒ ★ 前置闸（解析器 ↔ 运行时真值对拍）当场拦下 ⇒ 没造成损害。
      let j = i;
      while (j < src.length && /[0-9]/.test(src[j])) j++;
      if (src[j] === '.') { j++; while (j < src.length && /[0-9]/.test(src[j])) j++; }
      if (src[j] === 'e' || src[j] === 'E') {
        let m = j + 1;
        if (src[m] === '+' || src[m] === '-') m++;
        if (/[0-9]/.test(src[m] || '')) { j = m; while (j < src.length && /[0-9]/.test(src[j])) j++; }
      }
      const rawNum = src.slice(i, j);
      const parent = stack[stack.length - 1];
      const p = [...parent.path, pendingKey];
      nodes.set(pathOf(p), { path: p, kind: 'leaf', keyStart: pendingKeyStart, valueEnd: j, openLine: pendingKeyLine, closeLine: lineOf(j - 1), valueLine: pendingKeyLine, rawValue: rawNum, parent });
      parent.children.push(p);
      pendingKey = null;
      pendingKeyStart = -1;
      i = j;
      continue;
    }
    if (/[A-Za-z0-9_$]/.test(ch)) {
      // ★★ 缺陷 ⑩b：键名可以**以数字开头**（`400: '…'` / `1day: '…'`），
      //   而原来的起始字符集 `/[A-Za-z_$]/` 不含数字 ⇒ 这类键从不被登记。
      let j = i;
      while (j < src.length && /[A-Za-z0-9_$]/.test(src[j])) j++;
      const word = src.slice(i, j);
      let k = j;
      while (k < src.length && /\s/.test(src[k])) k++;
      if (src[k] === ':') {
        pendingKey = word;
        pendingKeyStart = i;
        pendingKeyLine = lineOf(i);
        i = j;
        continue;
      }
      // ★ 值位置上的标识符字面量（`true` / `false` / `null`）—— 同缺陷 ⑫
      if (pendingKey !== null) {
        const parent = stack[stack.length - 1];
        const p = [...parent.path, pendingKey];
        nodes.set(pathOf(p), { path: p, kind: 'leaf', keyStart: pendingKeyStart, valueEnd: j, openLine: pendingKeyLine, closeLine: lineOf(j - 1), valueLine: pendingKeyLine, rawValue: word, parent });
        parent.children.push(p);
        pendingKey = null;
        pendingKeyStart = -1;
      }
      i = j;
      continue;
    }
    i++;
  }
  return { nodes, root };
}

/**
 * ★ 取叶子节点的**运行时值**。
 *   ★★ 关键：TS 源码里的字面量 `'a\nb'` 在运行时是**真换行** ⇒
 *   ★★ 手写反转义（只处理 \' 和 \\）会与运行时值对不上 ⇒ 误判「值不符」⇒ 跳过
 *   ⇒ 正解：**直接求值这个字面量**（它就是一个字符串字面量，求值安全且逐字等于运行时值）。
 */
function leafInfo(src, node) {
  const raw = node.rawValue;
  let val;
  try {
    val = new Function('return (' + raw + ');')();
  } catch {
    val = raw.slice(1, -1);
  }
  return { val, raw };
}

/**
 * ★ 深度相等（数组/对象）。
 *   ★★ 缺陷 ⑨：`val !== want` 是**引用相等** ⇒ 对数组型键**永远为真**
 *   ⇒ 8 个语种各 1 个数组键被反复跳过 ⇒ 删不净 ⇒ 8 语种结构不一致。
 */
function sameValue(a, b) {
  if (a === b) return true;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b)) return false;
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (!sameValue(a[i], b[i])) return false;
    return true;
  }
  if (a && b && typeof a === 'object' && typeof b === 'object') {
    const ka = Object.keys(a), kb = Object.keys(b);
    if (ka.length !== kb.length) return false;
    for (const k of ka) {
      if (!Object.prototype.hasOwnProperty.call(b, k)) return false;
      if (!sameValue(a[k], b[k])) return false;
    }
    return true;
  }
  return false;
}

/** ★ 语法自检：把 `export default {...}` 当 CJS 求值一遍（只解析，不执行副作用）。 */
function checkSyntax(content) {
  try {
    new Function(content.replace(/^export default/m, 'module.exports ='));
    return null;
  } catch (e) {
    return (e && e.message ? e.message : String(e)).split('\n')[0];
  }
}

/** 这个逗号是不是「独占一行」（前后都是空白）？ */
function standaloneComma(src, idx) {
  if (src[idx] !== ',') return false;
  let ls = idx;
  while (ls > 0 && src[ls - 1] !== '\n') ls--;
  let le = idx;
  while (le < src.length && src[le] !== '\n') le++;
  return !src.slice(ls, idx).trim() && !src.slice(idx + 1, le).trim();
}

/**
 * ★★★ 计算「删掉这一段文本」应当删除的**字符区间**。
 *
 * ★★ 缺陷 ⑪：仓内语言包大量**一行多键**（`a: 'x', b: 'y',`）
 *   ⇒ 行级删除会把**同行的兄弟键一起删掉**（实测一次带走 8 个未申报的活键）
 *   ⇒ 删除必须落在**字符区间**上，而不是行号上。
 *
 * 规则（覆盖仓内实测到的全部排版）：
 *   ① 值后面**同一行**有逗号 ⇒ 连逗号一起删
 *   ② 值后面**下一行是独占一行的逗号** ⇒ 逗号另算一个区间（不跨行合并）
 *   ③ 值后面没有逗号（它是最后一个键）⇒ 删**前面那个独占一行的逗号**
 *   ④ 前面那个逗号与前一个键**同行**（属于前一个键）⇒ 不许删（尾逗号可省）
 *   ⑤ 删完后该行若只剩空白 ⇒ 整行（含换行）一起吃掉
 */
function computeDeleteSpans(src, start, end) {
  const spans = [];
  // ① 同行后随逗号
  let i = end;
  while (i < src.length && (src[i] === ' ' || src[i] === '\t')) i++;
  if (src[i] === ',') {
    spans.push([start, i + 1]);
    return spans;
  }
  // ② 下一行的独占逗号（**另算区间**，不与主区间跨行合并）
  let j = end;
  while (j < src.length && /\s/.test(src[j])) j++;
  if (src[j] === ',' && standaloneComma(src, j)) {
    let ls = j;
    while (ls > 0 && src[ls - 1] !== '\n') ls--;
    let le = j;
    while (le < src.length && src[le] !== '\n') le++;
    spans.push([start, end]);
    spans.push([ls, le < src.length ? le + 1 : le]);
    return spans;
  }
  // ③ 没有后随逗号 ⇒ 它是最后一个键 ⇒ 前面那个「独占一行的逗号」属于它
  let k = start - 1;
  while (k >= 0 && (src[k] === ' ' || src[k] === '\t')) k--;
  if (src[k] === ',' && standaloneComma(src, k)) {
    spans.push([k, end]);
    return spans;
  }
  // ④ 前面的逗号与前一个键同行 ⇒ 属于前一个键 ⇒ 不许删
  spans.push([start, end]);
  return spans;
}

/** 主区间若让该行只剩空白 ⇒ 整行（含换行）一起吃掉（只对**不跨行**的主区间生效）。 */
function widenIfLineEmptied(src, spans) {
  const [s, e] = spans[0];
  if (src.slice(s, e).includes('\n')) return spans;
  let ls = s;
  while (ls > 0 && src[ls - 1] !== '\n') ls--;
  let le = e;
  while (le < src.length && src[le] !== '\n') le++;
  if (!src.slice(ls, s).trim() && !src.slice(e, le).trim()) {
    spans[0] = [ls, le < src.length ? le + 1 : le];
  }
  return spans;
}

/**
 * ★ 吃掉「主区间前面只有缩进」与「主区间后面只剩空白」这两截。
 *   ★ 不吃 ⇒ 删除后会留下一行只有空格的残渣（实测 `export default {\n  \n  \n}`）。
 *   ★ 跨行区间（多行对象块）也适用 ⇒ 缩进与末尾换行都要吃掉。
 */
function absorbSurroundingWhitespace(src, spans) {
  if (!spans.length) return spans;
  let [s, e] = spans[0];
  // ① 前面的缩进
  let ls = s;
  while (ls > 0 && src[ls - 1] !== '\n') ls--;
  const beforeWsOnly = !src.slice(ls, s).trim();
  if (beforeWsOnly) s = ls;
  // ② 后面到行尾只剩空白 ⇒ 连换行一起吃
  //   ★★ 缺陷 ⑬：**必须整行都会变空时才吃换行** ——
  //   ★★ 否则「同一行还有别的键」时也会吃掉行尾换行 ⇒ **下一行被粘上来**
  //   ★★ （实测：`reloadAria: 'X', degradedViewLabel: '視圖',` 删掉后者后，
  //   ★★   下一行的内容被粘到了 `reloadAria` 那一行）。
  //   ★★ 且必须确认 e 还没跨过换行（`widenIfLineEmptied` 可能已把 e 推过换行）。
  if (beforeWsOnly && e > 0 && src[e - 1] !== '\n') {
    let le = e;
    while (le < src.length && src[le] !== '\n') le++;
    if (!src.slice(e, le).trim()) e = le < src.length ? le + 1 : le;
  }
  spans[0] = [s, e];
  return spans;
}

/**
 * ★★ 缺陷 ⑭：**行尾注释会变成孤儿**。
 *   仓内语言包大量这种写法：`subtitle: 'Kaixuan MaaS Console', // brand "开轩" kept consistent`
 *   ⇒ 键被删后，注释变成**孤零零的一行**，而它描述的键已经不存在了 ⇒ 误导后来人。
 *   ⇒ 正解：**注释随它描述的键一起走** —— 若这一行剩下的只有「空白 + 注释」，
 *   ★   则把整行（含注释与换行）一起删掉。
 */
function absorbTrailingComment(src, spans) {
  if (!spans.length) return spans;
  const [s0, e0] = spans[0];
  let le = e0;
  while (le < src.length && src[le] !== '\n') le++;
  const rest = src.slice(e0, le).trim();
  if (!rest.startsWith('//')) return spans;
  let ls = s0;
  while (ls > 0 && src[ls - 1] !== '\n') ls--;
  const beforeWsOnly = !src.slice(ls, s0).trim();
  // 前面还有别的键 ⇒ 只吃注释本身，保留换行（那一行还有活键）
  if (!beforeWsOnly) { spans[0] = [s0, le]; return spans; }
  spans[0] = [ls, le < src.length ? le + 1 : le];
  return spans;
}

/** 把一组字符区间合并成互不重叠的区间列表（按起点升序）。 */
function mergeSpans(spans) {
  const sorted = [...spans].sort((a, b) => a[0] - b[0]);
  const merged = [];
  for (const sp of sorted) {
    const last = merged[merged.length - 1];
    if (last && sp[0] <= last[1]) { last[1] = Math.max(last[1], sp[1]); continue; }
    merged.push([...sp]);
  }
  return merged;
}

/** 从内容里删掉这些字符区间（从后往前删 ⇒ 偏移不漂）。 */
function applySpans(src, spans) {
  const merged = mergeSpans(spans);
  let out = src;
  for (let i = merged.length - 1; i >= 0; i--) {
    const [a, b] = merged[i];
    out = out.slice(0, a) + out.slice(b);
  }
  return { content: out, count: merged.length };
}

/**
 * ★ 迭代清理「真空容器」：反复解析内容 ⇒ 找出**当前确实没有任何孩子**的对象
 *   （且不是根、且在允许清单里）⇒ 删掉它的字符区间 ⇒ 再解析，直到没有变化。
 *   ★ 只删「真空」的 ⇒ 绝不可能误删还活着的兄弟键。
 *   ★★ 缺陷 ⑩c：原实现把**文件里所有空对象**都剪掉（包括与本批无关的）⇒
 *   ★★ 收敛为：**只剪「本批删过子孙的祖先」**（`prunePaths` 清单）。
 */
function pruneEmptyObjects(content, prunePaths) {
  let cur = content;
  for (let round = 0; round < 30; round++) {
    const { nodes, root } = parseModule(cur);
    const empties = [];
    for (const [pathKey, node] of nodes) {
      if (pathKey === '' || node.kind !== 'object') continue;
      if (node.children.length !== 0) continue;
      if (prunePaths && !prunePaths.has(pathKey)) continue;
      empties.push(node);
    }
    if (!empties.length) break;
    const spans = [];
    for (const n of empties) {
      // 对象的删除区间 = 「键名 + 值（{…}）」这一整段
      const start = n.keyStart >= 0 ? n.keyStart : n.openBrace;
      const end = n.closeBrace >= 0 ? n.closeBrace + 1 : n.openBrace + 1;
      spans.push(...absorbSurroundingWhitespace(cur, computeDeleteSpans(cur, start, end)));
    }
    const applied = applySpans(cur, spans);
    cur = applied.content;
    stats.pruned += empties.length;
  }
  return cur;
}

/** 取一次解析里所有**叶子**的键路径集合（用于硬不变量比对）。 */
function leafPathsOf(parseResult) {
  const s = new Set();
  for (const [k, n] of parseResult.nodes) if (n.kind === 'leaf') s.add(k);
  return s;
}

/**
 * ★★★ 解析器 ↔ 运行时真值 对拍（量具必须与事实源同源）。
 *   删除器的「剪枝」判据是「**这个对象现在没有孩子了**」，而「有没有孩子」是
 *   **解析器说了算** ⇒ 解析器漏认一个键 ⇒ 父容器被判真空 ⇒ **剪枝把活内容删掉**。
 *   ⇒ 「剪枝判对没有」不能靠约定，必须**拿运行时真值对拍**。
 */
function crossCheckParser(loc, ns, nodes, values) {
  const parserLeaves = leafPathsOf({ nodes });
  const runtimeLeaves = new Set();
  const prefix = ns + '.';
  for (const k of values.keys()) if (k.startsWith(prefix)) runtimeLeaves.add(k.slice(prefix.length));
  const parserOnly = [...parserLeaves].filter((k) => !runtimeLeaves.has(k));
  const runtimeOnly = [...runtimeLeaves].filter((k) => !parserLeaves.has(k));
  return { parserLeaves, runtimeLeaves, parserOnly, runtimeOnly };
}

const stats = { deletedKeys: 0, deletedSpans: 0, skipped: 0, pruned: 0, invariantBreaks: 0, parserMismatch: 0 };
const skippedDetail = [];

// ★★ `--check-parser` 模式：只做「解析器 ↔ 运行时真值」对拍，不删任何东西。
if (checkParser) {
  let bad = 0, checked = 0;
  for (const loc of LOCALES) {
    const values = facts.perLocaleValues.get(loc);
    for (const ns of [...facts.nsToFile.keys()]) {
      const fname = facts.nsToFile.get(ns);
      if (!fname) continue;
      const file = join(localesDir, loc, fname);
      let src;
      try { src = readFileSync(file, 'utf8'); } catch { continue; }
      const { nodes } = parseModule(src);
      const cc = crossCheckParser(loc, ns, nodes, values);
      checked++;
      if (cc.parserOnly.length || cc.runtimeOnly.length) {
        bad++;
        console.log(`  ✗ ${loc}/${fname} 解析器与运行时不一致：解析器多 ${cc.parserOnly.length} 个 / 漏 ${cc.runtimeOnly.length} 个`);
        if (cc.parserOnly.length) console.log(`      解析器多出: ${cc.parserOnly.slice(0, 8).join(', ')}`);
        if (cc.runtimeOnly.length) console.log(`      解析器漏掉: ${cc.runtimeOnly.slice(0, 8).join(', ')}`);
      }
    }
  }
  console.log(`对拍完成：${checked} 个文件，不一致 ${bad} 个`);
  process.exit(bad ? 1 : 0);
}

for (const loc of LOCALES) {
  const values = facts.perLocaleValues.get(loc);
  const nsList = nsFilter ? [nsFilter] : Object.keys(plan);
  for (const ns of nsList) {
    const targets = plan[ns];
    if (!targets || !targets.length) continue;
    const file = join(localesDir, loc, `${ns}.ts`);
    let src;
    try { src = readFileSync(file, 'utf8'); } catch { console.log(`  ⚠ ${loc}/${ns}.ts 读不到`); continue; }

    const { nodes, root } = parseModule(src);
    const beforeLeaves = leafPathsOf({ nodes });
    const touchedObjects = new Set();   // 删过子键的对象（= 允许剪枝的子树）
    const deletedPaths = new Set();     // ★ 本批**实际**删除的叶子键路径（模块内相对路径）
    const spans = [];

    // ★★★ 前置闸：解析器必须与运行时真值**逐字一致**（否则剪枝判据不可信 ⇒ 该文件不许动）
    {
      const cc = crossCheckParser(loc, ns, nodes, values);
      if (cc.parserOnly.length || cc.runtimeOnly.length) {
        console.log(`  ✗ ${loc}/${ns}.ts 解析器与运行时不一致（多 ${cc.parserOnly.length} / 漏 ${cc.runtimeOnly.length}）⇒ 该文件整体跳过，未落盘`);
        if (cc.parserOnly.length) console.log(`      解析器多出: ${cc.parserOnly.slice(0, 6).join(', ')}`);
        if (cc.runtimeOnly.length) console.log(`      解析器漏掉: ${cc.runtimeOnly.slice(0, 6).join(', ')}`);
        stats.parserMismatch++;
        stats.skipped++;
        continue;
      }
    }

    for (const keyPath of targets) {
      const full = `${ns}.${keyPath}`;
      const node = nodes.get(keyPath);
      if (!node) { stats.skipped++; skippedDetail.push(`${loc} ${full}：节点找不到`); continue; }
      if (node.kind !== 'leaf') { stats.skipped++; skippedDetail.push(`${loc} ${full}：不是叶子（对象）`); continue; }
      const { val } = leafInfo(src, node);
      const want = values.get(full);   // ★ 值表是 **Map** ⇒ 必须用 .get（按普通对象取会全 undefined）
      if (want === undefined) { stats.skipped++; skippedDetail.push(`${loc} ${full}：loader 里没有这个键`); continue; }
      if (!sameValue(val, want)) { stats.skipped++; skippedDetail.push(`${loc} ${full}：值不符（找到 ${JSON.stringify(val)} ≠ 期望 ${JSON.stringify(want)}）`); continue; }
      // ★★ 字符区间级删除（缺陷 ⑪：一行多键 ⇒ 行级删除会连带删掉同行的兄弟键）
      const sp = absorbTrailingComment(src, absorbSurroundingWhitespace(src, widenIfLineEmptied(src, computeDeleteSpans(src, node.keyStart, node.valueEnd))));
      spans.push(...sp);
      stats.deletedKeys++;
      deletedPaths.add(keyPath);
      // 记录祖先对象
      let p = node.parent;
      while (p) { touchedObjects.add(p.path.join('.')); p = p.parent; }
    }

    if (!spans.length) continue;

    // ① 删叶子
    let newContent = applySpans(src, spans).content;
    // ② 剪枝（真空容器）
    newContent = pruneEmptyObjects(newContent, touchedObjects);
    if (!dryRun) {
      // ★ 落盘后语法自检：坏掉就**还原原文件**（绝不把坏文件留在工作区）
      const syntaxErr = checkSyntax(newContent);
      if (syntaxErr) {
        console.log(`  ✗ ${loc}/${ns}.ts 删完语法不通过（${syntaxErr}）⇒ 已还原，未落盘`);
        stats.skipped++;
        continue;
      }
      // ★★★ 硬不变量：**存活叶子集 == 原叶子集 − 本批实际删除的叶子集**。
      //   ★ 不管剪枝逻辑怎么错，只要它多删了**任何一个没被申报**的叶子键 ⇒ 这里就红 ⇒ 还原。
      const afterLeaves = leafPathsOf(parseModule(newContent));
      const unexpected = [];
      for (const k of beforeLeaves) {
        if (!deletedPaths.has(k) && !afterLeaves.has(k)) unexpected.push(k);
      }
      if (unexpected.length) {
        console.log(`  ✗ ${loc}/${ns}.ts ★不变量破口：多删了 ${unexpected.length} 个未申报的键（${unexpected.slice(0, 5).join(', ')}）⇒ 已还原，未落盘`);
        stats.invariantBreaks++;
        stats.skipped++;
        continue;
      }
      const stillThere = [];
      for (const k of deletedPaths) if (afterLeaves.has(k)) stillThere.push(k);
      if (stillThere.length) {
        console.log(`  ✗ ${loc}/${ns}.ts ★不变量破口：申报删了但仍存活 ${stillThere.length} 个（${stillThere.slice(0, 5).join(', ')}）⇒ 已还原，未落盘`);
        stats.invariantBreaks++;
        stats.skipped++;
        continue;
      }
      writeFileSync(file, newContent, 'utf8');
    }
  }
}

console.log(dryRun ? '[DRY-RUN] ' : '');
console.log(`删除叶子键 ${stats.deletedKeys} 处（字符区间若干），剪掉空对象块 ${stats.pruned} 个，跳过 ${stats.skipped} 处，不变量破口 ${stats.invariantBreaks} 处，解析器不一致 ${stats.parserMismatch} 处`);
if (skippedDetail.length) {
  console.log('\n跳过的（≤20 条）：');
  for (const s of skippedDetail.slice(0, 20)) console.log('  ' + s);
  if (skippedDetail.length > 20) console.log(`  … 共 ${skippedDetail.length} 条`);
}
