#!/usr/bin/env node
// gen-simple-mode-ledger.mjs — 从 appNav.ts + zh-CN/nav.ts 生成「简易模式 v2 · 条目归属台账」
//
// 为什么要有这个工具：
//   台账是「专家面 63 条入口一条不删」这句话的**唯一证据**。
//   手抄的台账会随 appNav.ts 漂移 —— 2026-11-16 的 v1 稿就写着「61 个入口 / 60 个落地路径」，
//   而当日 appNav.ts 已经是 63 条目 / 62 路径（`/live2` 加入 + 「自动更新」跨两组重复）。
//   ⇒ **台账必须由真源生成**（LESSONS 同款纪律：断言的期望不能从被断言对象的运行态派生，
//   但**清单本身**必须从真源派生）。
//
// 用法：
//   node scripts/audit/gen-simple-mode-ledger.mjs                 # 打印统计
//   node scripts/audit/gen-simple-mode-ledger.mjs --html          # 打印表格 <tbody> 行
//   node scripts/audit/gen-simple-mode-ledger.mjs --json          # 打印结构化结果
//   node scripts/audit/gen-simple-mode-ledger.mjs --check <html>  # 校验某个 HTML 里台账是否齐
//
// 退出码：0 = 正常；2 = --check 失败（台账行数与真源不符）；3 = 工具自身失败

import { readFileSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(HERE, '..', '..');
const NAV_TS = resolve(ROOT, 'web/src/config/appNav.ts');
const LOCALE_TS = resolve(ROOT, 'web/src/locales/zh-CN/nav.ts');
const SANITIZE_LOCALE_TS = resolve(ROOT, 'web/src/locales/zh-CN/sanitizeRules.ts');
const SESSION_ANALYTICS_LOCALE_TS = resolve(ROOT, 'web/src/locales/zh-CN/sessionAnalytics.ts');

const argv = process.argv.slice(2);
const has = (k) => argv.includes('--' + k);
const arg = (k, d) => {
  const i = argv.indexOf('--' + k);
  return i >= 0 && argv[i + 1] && !argv[i + 1].startsWith('--') ? argv[i + 1] : d;
};

function die(msg, code = 3) {
  console.error('[gen-ledger] FATAL: ' + msg);
  process.exit(code);
}

// ------------------------------------------------------------------ 真源解析

function parseLocale(src) {
  // 只取 item: { ... } 与 group: { ... } 两块，键值对形如 key: '中文' 或 key: "中文"
  const out = { item: {}, group: {} };
  for (const block of ['item', 'group']) {
    const re = new RegExp('\\n\\s{2,4}' + block + ':\\s*\\{([\\s\\S]*?)\\n\\s{2,4}\\},', 'm');
    const m = re.exec(src);
    if (!m) continue;
    const body = m[1];
    const kv = /(\w+):\s*(['"])(.*?)\2/g;
    let mm;
    while ((mm = kv.exec(body)) !== null) out[block][mm[1]] = mm[3];
  }
  return out;
}

// 解析 export default { ... } 形式的翻译文件
function parseDefaultExport(src) {
  const out = {};
  const re = /export\s+default\s*\{([\s\S]*)\};?\s*$/m;
  const m = re.exec(src);
  if (!m) return out;
  const body = m[1];
  const kv = /(\w+):\s*(['"])(.*?)\2/g;
  let mm;
  while ((mm = kv.exec(body)) !== null) out[mm[1]] = mm[3];
  return out;
}

function parseNav(src) {
  const items = [];
  let group = null;

  // NAV_PRIMARY_ITEMS 块
  const primaryStart = src.indexOf('NAV_PRIMARY_ITEMS');
  const groupsStart = src.indexOf('NAV_GROUPS');
  if (primaryStart < 0 || groupsStart < 0) die('appNav.ts 里找不到 NAV_PRIMARY_ITEMS / NAV_GROUPS');
  const primaryBlock = src.slice(primaryStart, groupsStart);
  const groupsBlock = src.slice(groupsStart);

  const entryRe = /\{\s*path:\s*'([^']+)'([\s\S]*?)\}/g;
  const labelRe = /labelKey:\s*'([^']+)'/;
  const labelPlainRe = /label:\s*'([^']+)'/;

  for (const m of primaryBlock.matchAll(entryRe)) {
    const tail = m[2];
    const lk = (labelRe.exec(tail) || [])[1];
    const lp = (labelPlainRe.exec(tail) || [])[1];
    items.push({ path: m[1], labelKey: lk || null, labelPlain: lp || null, group: 'primary' });
  }

  const groupRe = /id:\s*'([^']+)'/g;
  const positions = [];
  let gm;
  while ((gm = groupRe.exec(groupsBlock)) !== null) positions.push({ id: gm[1], idx: gm.index });
  positions.push({ id: '__end__', idx: groupsBlock.length });

  for (let i = 0; i < positions.length - 1; i++) {
    const seg = groupsBlock.slice(positions[i].idx, positions[i + 1].idx);
    for (const m of seg.matchAll(entryRe)) {
      const tail = m[2];
      const lk = (labelRe.exec(tail) || [])[1];
      const lp = (labelPlainRe.exec(tail) || [])[1];
      items.push({ path: m[1], labelKey: lk || null, labelPlain: lp || null, group: positions[i].id });
    }
  }
  return items;
}

// ------------------------------------------------------------------ v2 位置规则

// 落点只有四种：直链 / 组内下拉 / 高级折叠 / 站外链接。
// ★ 规则写死在这里，而不是写进 HTML —— HTML 是产物，规则是真源。
const V2_DIRECT = ['/dashboard', '/live', '/chat', '/admin/settings'];
// ★ 分组用**专家面分组 id**做键（与 web/src/config/simpleNav.ts 同款），
//   中文名只用于展示 —— 两边对同一 path 的判断必须能逐字比对。
// ★ 2026-11-17 修正：本表曾列 '/usage'，但**专家面导航真源（appNav.ts）里
//   根本没有用量页** ⇒ 它匹配不到任何 item，是个永远落不了地的死条目；
//   且与 web/src/config/simpleNav.ts 的同名表**同源同错** ⇒ 两边互相掩盖、
//   对拍永远绿（2026-11-17 实测）。总览页的「查看用量」按钮正是照着这个
//   幻觉写的 ⇒ 点了跳 vue-router 的 no-match 空白页。
// ★ 最终取值：改为**专家面导航真源里真实存在**的 '/tenant/usage'
//   （appNav.ts:146，tenantOnly 项）⇒ 清单条目首次真正命中真源，
//   简易模式「请求与会话」组由此多出一条能落地的入口（符合「专家面一条不删」）。
const V2_GROUPS = {
  'models-routing': ['/models', '/providers'],
  'tenant-users': ['/users', '/keys', '/tenants'],
  'requests-sessions': ['/request-logs', '/tenant/usage'],
  guide: ['/examples', '/examples#auto-config', '/help'],
};
// 分组 id → 展示用中文名
const V2_GROUP_CN = {
  'models-routing': '供应商与模型',
  'tenant-users': '密钥与用户',
  'requests-sessions': '请求',
  'guide': '接入指南',
};
// 专家面分组 id → 台账里的中文分组名
const GROUP_CN = {
  primary: '顶层主链接',
  operations: '运营管理',
  'models-routing': '供应商与模型',
  'tenant-users': '密钥与用户',
  'requests-sessions': '请求与会话',
  'data-ops': '系统设置', // 2026-11-27 非管理员菜单重构轮：数据运维 → 系统设置（zh-CN 同轮改）
  opsplatform: '运维中心',
  guide: '接入指南',
  chat: '对话',
};

function v2Position(path) {
  if (path.startsWith('http')) return { kind: 'external', cn: '站外链接', bucket: 'advanced' };
  if (V2_DIRECT.includes(path)) return { kind: 'direct', cn: '顶栏直链', bucket: 'direct' };
  for (const [g, paths] of Object.entries(V2_GROUPS)) {
    if (paths.includes(path)) return { kind: 'group', cn: V2_GROUP_CN[g] || g, bucket: g };
  }
  return { kind: 'advanced', cn: '高级折叠', bucket: 'advanced' };
}

// ------------------------------------------------------------------ 组装

const locale = parseLocale(readFileSync(LOCALE_TS, 'utf8'));
const sanitizeLocale = parseDefaultExport(readFileSync(SANITIZE_LOCALE_TS, 'utf8'));
// ★ sessionAnalytics 命名空间（2026-11-27）：/admin/session-analytics/* 三条目的
//   labelKey 落在 sessionAnalytics.nav.* ⇒ 用 parseDefaultExport 抓到嵌套 nav 块
//   后按点路径解析（parseDefaultExport 是平面的，嵌套 key 会覆盖成内层值；
//   sessionAnalytics.ts 里 nav 块在顶部且无同名冲突 ⇒ 平面值即 nav 值）。
const sessionAnalyticsLocale = parseDefaultExport(readFileSync(SESSION_ANALYTICS_LOCALE_TS, 'utf8'));
const items = parseNav(readFileSync(NAV_TS, 'utf8'));

// ★ 2026-11-17 新增硬门：分组清单里的每条 path 都必须在专家面导航真源里存在。
//   幻觉条目（曾列过的 '/usage'）匹配不到任何 item ⇒ 永远落不了地，
//   且与 web/src/config/simpleNav.ts 的同名表**同源同错** ⇒ 两边互相掩盖、
//   对拍永远绿。代价是总览页的「查看用量」按钮照着幻觉写 ⇒ 点了跳空白页。
//   ⇒ 这里直接 die：台账生成器是台账的必经之路，硬门比软注释可靠。
//   （只校验 V2_GROUPS：它的语义是「专家面分组里保留的路径」，必须来自真源；
//     V2_DIRECT 的语义是「路由直链」，属另一层语义，另由 router 侧保证。）
const navPaths = new Set(items.map((it) => it.path));
for (const [groupId, paths] of Object.entries(V2_GROUPS)) {
  for (const p of paths) {
    if (!navPaths.has(p)) {
      die(
        `V2_GROUPS['${groupId}'] 里的 ${p} 在专家面导航真源（appNav.ts）里不存在` +
          ` ⇒ 派生不出来（死条目）`,
      );
    }
  }
}

function labelOf(it) {
  if (it.labelPlain) return it.labelPlain;
  const k = it.labelKey || '';
  if (k.startsWith('nav.item.')) return locale.item[k.slice('nav.item.'.length)] || k;
  if (k.startsWith('nav.group.')) return locale.group[k.slice('nav.group.'.length)] || k;
  // 少数条目用别处的 key（如 sanitizeRules.title）
  if (k.startsWith('sanitizeRules.')) return sanitizeLocale[k.slice('sanitizeRules.'.length)] || k;
  // sessionAnalytics.nav.*（人员/智能体/模型排行三条）
  if (k.startsWith('sessionAnalytics.nav.')) return sessionAnalyticsLocale[k.slice('sessionAnalytics.nav.'.length)] || k;
  // 其他未知 key 回落到 key 本身
  return k || it.path;
}

const rows = items.map((it) => ({
  name: labelOf(it),
  path: it.path,
  group: GROUP_CN[it.group] || it.group,
  pos: v2Position(it.path),
  labelKey: it.labelKey,
}));

const distinctPaths = new Set(rows.map((r) => r.path)).size;
const counts = rows.reduce((acc, r) => {
  acc[r.pos.cn] = (acc[r.pos.cn] || 0) + 1;
  return acc;
}, {});
const missingLabel = rows.filter((r) => r.name.startsWith('nav.') || !r.name);

// ------------------------------------------------------------------ 输出

if (has('json')) {
  // bucket = 简易模式的落点桶（direct / 分组中文名 / advanced）。
  // ★ 它与 web/src/config/simpleNav.ts 的 SIMPLE_DIRECT_PATHS / SIMPLE_GROUP_PATHS
  //   是**两份独立实现**（一个 Go 仓外脚本、一个前端模块）⇒ 简单模式前端测试
  //   会逐条对拍：两边对同一 path 的判断必须一致。
  const buckets = rows.reduce((acc, r) => {
    const b = r.pos.bucket;
    acc[b] = (acc[b] || 0) + 1;
    return acc;
  }, {});
  console.log(JSON.stringify({ total: rows.length, distinctPaths, counts, buckets, rows }, null, 2));
} else if (has('html')) {
  for (const r of rows) {
    const chip =
      r.pos.kind === 'direct' ? 'chip chip-primary' :
      r.pos.kind === 'group' ? 'chip chip-ok' :
      r.pos.kind === 'external' ? 'chip chip-warn' : 'chip chip-muted';
    const extra = r.pos.kind === 'external' ? ' <span class="chip chip-warn">站外</span>' : '';
    console.log(
      '          <tr data-ledger-row><td>' + r.name + '</td><td class="path">' + r.path + '</td><td>' + r.group +
      '</td><td><span class="' + chip + '">' + r.pos.cn + '</span>' + extra + '</td></tr>'
    );
  }
} else if (has('check')) {
  const target = arg('check');
  if (!target || !existsSync(target)) die('--check 需要一个存在的 HTML 路径');
  const html = readFileSync(target, 'utf8');
  const marker = 'data-ledger-row';
  const n = (html.match(new RegExp(marker, 'g')) || []).length;
  if (n !== rows.length) {
    console.error('[gen-ledger] FAIL: HTML 里台账行 ' + n + ' 条，真源 ' + rows.length + ' 条');
    process.exit(2);
  }
  console.log('[gen-ledger] OK: 台账行数与真源一致（' + n + ' 条 / ' + distinctPaths + ' 个落地路径）');
} else {
  console.log('[gen-ledger] 真源：' + NAV_TS.replace(ROOT + '/', ''));
  console.log('[gen-ledger] 条目 ' + rows.length + ' 条，落地路径 ' + distinctPaths + ' 个');
  for (const [k, v] of Object.entries(counts)) console.log('  ' + k + ': ' + v);
  if (missingLabel.length) {
    console.log('[gen-ledger] ⚠ 未解析出中文名的条目（需人工确认）：');
    for (const r of missingLabel) console.log('  - ' + r.labelKey + ' → ' + r.path);
  }
}
