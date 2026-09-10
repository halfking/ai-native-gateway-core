// One-shot dead-code reachability scan for candidate 23 (R12).
// Walks import graph from entry points; lists views/components with zero
// inbound edges from non-test source files.
import fs from 'node:fs';
import path from 'node:path';

const ROOT = path.resolve('src');
const EXTS = ['.ts', '.tsx', '.vue', '.js', '.mjs', '.css'];

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

const allFiles = walk(ROOT);
const byReal = new Map(allFiles.map(f => [f.replace(/\\/g, '/').toLowerCase(), f]));

function resolveImport(fromFile, spec) {
  let base;
  if (spec.startsWith('@/')) base = path.join(ROOT, spec.slice(2));
  else if (spec.startsWith('.')) base = path.resolve(path.dirname(fromFile), spec);
  else if (spec.startsWith('src/')) base = path.join(path.dirname(ROOT), spec);
  else return null; // bare module
  const candidates = [];
  for (const suffix of ['', ...EXTS, '/index.ts', '/index.vue']) {
    candidates.push(base + suffix);
  }
  for (const c of candidates) {
    const hit = byReal.get(c.replace(/\\/g, '/').toLowerCase());
    if (hit) return hit;
  }
  return null;
}

const IMPORT_RE = /(?:import\s+[^'"]*?from\s+|import\s*\(\s*|import\s+|require\s*\(\s*)['"]([^'"]+)['"]/g;

const edges = new Map(); // file -> Set(imported files)
const inbound = new Map(); // file -> Set of referencing files
for (const f of allFiles) {
  if (!/\.(ts|vue|js|mjs)$/.test(f)) continue;
  const text = fs.readFileSync(f, 'utf8');
  const set = new Set();
  for (const m of text.matchAll(IMPORT_RE)) {
    const target = resolveImport(f, m[1]);
    if (target && target !== f) set.add(target);
  }
  edges.set(f, set);
  for (const t of set) {
    if (!inbound.has(t)) inbound.set(t, new Set());
    inbound.get(t).add(f);
  }
}

const isTest = f => /\.(test|spec)\.[tj]s$/.test(f) || /__tests__\//.test(f);
const isView = f => /\/views\//.test(f);
const isComponent = f => /\/components\//.test(f);

// Reachability from the real entry: a file is dead when main.ts cannot
// reach it (cascade-safe — components imported only by dead views die too).
// Test files are excluded from traversal: they never keep runtime code alive.
const ENTRY = path.join(ROOT, 'main.ts');
const reachable = new Set();
const stack = [ENTRY];
while (stack.length) {
  const cur = stack.pop();
  if (reachable.has(cur)) continue;
  reachable.add(cur);
  for (const t of edges.get(cur) || []) {
    if (!isTest(t)) stack.push(t);
  }
}

const deadViews = [];
const deadComponents = [];
const deadOther = [];
for (const f of allFiles) {
  if (!/\.(vue|ts)$/.test(f) || isTest(f)) continue;
  if (reachable.has(f)) continue;
  const norm = f.replace(/\\/g, '/');
  const rel = path.relative(ROOT, f).replace(/\\/g, '/');
  if (isView(norm)) deadViews.push(rel);
  else if (isComponent(norm)) deadComponents.push(rel);
  else deadOther.push(rel);
}

console.log('DEAD VIEWS:', deadViews.length);
deadViews.forEach(v => console.log('  ', v));
console.log('DEAD COMPONENTS:', deadComponents.length);
deadComponents.forEach(v => console.log('  ', v));
console.log('DEAD OTHER:', deadOther.length);
deadOther.forEach(v => console.log('  ', v));

// Orphaned test files: tests whose subject is unreachable.
const deadStems = new Set();
for (const d of [...deadViews, ...deadComponents, ...deadOther]) {
  deadStems.add(d.replace(/\.(vue|ts)$/, ''));
}
const orphanTests = [];
for (const f of allFiles) {
  if (!isTest(f)) continue;
  const rel = path.relative(ROOT, f).replace(/\\/g, '/');
  const stem = rel.replace(/\.(test|spec)\.[tj]s$/, '');
  if (deadStems.has(stem)) orphanTests.push(rel);
}
console.log('ORPHAN TESTS:', orphanTests.length);
orphanTests.forEach(v => console.log('  ', v));
