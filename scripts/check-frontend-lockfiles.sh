#!/usr/bin/env bash
# scripts/check-frontend-lockfiles.sh
# 双 lockfile 同步门：package.json / package-lock.json / pnpm-lock.yaml 必须
# 对每个声明依赖给出同一个版本区间。
#
# 为什么要这道门（2026-10-02）：
# 仓库同时跟踪 package-lock.json 与 pnpm-lock.yaml（pnpm 官方、npm 并行支持），
# 而 deploy-kaixuan1.sh 与 deploy-seamless.sh 已经在生产路径上跑 `npm ci`。
# 两个 lockfile 之间没有任何强制同步的手段：改了 package.json 只重新生成
# pnpm-lock.yaml，npm 那条路就会在**生产主机上**以
# "npm ci can only install packages when your package.json and package-lock.json
# are in sync" 失败，而不是在 CI 里失败。CI 此前只跑 pnpm，完全看不见。
#
# 判定方式不是"两个 lockfile 的完整解析树相同"（pnpm 与 npm 的扁平化布局本就
# 不同，比不了），而是三方对**声明区间**必须逐字相同：
#   package.json            dependencies/devDependencies[name]
#   package-lock.json       packages[""].dependencies/devDependencies[name]
#   pnpm-lock.yaml          importers['.'].dependencies/devDependencies[name].specifier
# 只要有一方漏了重新生成，区间就对不上，门会红。
#
# 同时实跑两条安装命令（--dry-run / --lockfile-only，不动 node_modules），
# 确保不是"区间看起来一致但命令跑不通"。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WEB_DIR="${WEB_DIR:-web}"
fail=0
note() { printf '[lockfiles] %s\n' "$*"; }
bad()  { printf '[lockfiles] FAIL: %s\n' "$*" >&2; fail=1; }

command -v node >/dev/null 2>&1 || { bad "node 不在 PATH 上"; exit 1; }

# ── 1. 两个 lockfile 必须在版本控制里 ─────────────────────────────────────
# 没被 git 跟踪的 lockfile 等于不存在：别人 clone 下来根本没有它。
for lf in "$WEB_DIR/package-lock.json" "$WEB_DIR/pnpm-lock.yaml" "$WEB_DIR/package.json"; do
  [ -f "$lf" ] || { bad "$lf 不存在"; continue; }
  if git ls-files --error-unmatch "$lf" >/dev/null 2>&1; then
    note "ok  $lf 已跟踪"
  else
    bad "$lf 未被 git 跟踪 —— 双包管理器模式下缺失它等于该路径不可用"
  fi
done
((fail == 0)) || exit 1

# ── 2. 三方声明区间必须逐字一致 ──────────────────────────────────────────
note "比对 package.json / package-lock.json / pnpm-lock.yaml 的声明区间"
node - "$WEB_DIR" <<'NODE'
const fs = require('fs');
const path = require('path');
const dir = process.argv[2];
const read = (f) => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));

const pkg = read('package.json');
const lock = read('package-lock.json');
const yaml = fs.readFileSync(path.join(dir, 'pnpm-lock.yaml'), 'utf8');

const declared = Object.assign({}, pkg.dependencies, pkg.devDependencies);

// package-lock.json v3 mirrors the declared ranges under packages[""].
const rootEntry = (lock.packages && lock.packages['']) || {};

// pnpm-lock v9 lists them under importers['.'] with a `specifier:` key.
// Parsed by indentation rather than with a YAML dependency: node has none, and
// pulling one in would defeat "no new dependency". The block we want is the
// first `dependencies:`/`devDependencies:` group under `importers: .`, which is
// the only place specifier: lines appear before the `packages:`/`snapshots:`
// sections start.
const importersStart = yaml.indexOf('\nimporters:');
const packagesStart = yaml.search(/\n(?:packages|snapshots):/);
let importerBlock = '';
if (importersStart !== -1) {
  const end = packagesStart === -1 ? yaml.length : packagesStart;
  importerBlock = yaml.slice(importersStart, end);
}

const pnpmSpecifiers = {};
{
  // Lines look like:  <name>:\n ... specifier: <range>
  const re = /^\s{6}'?([^'\s:]+)'?:\s*$\n\s+specifier:\s*(.+?)\s*$/gm;
  let m;
  while ((m = re.exec(importerBlock)) !== null) {
    pnpmSpecifiers[m[1]] = m[2];
  }
}

const problems = [];
const semverish = /^[0-9]/;

for (const [name, range] of Object.entries(declared)) {
  const inPkgLock = (rootEntry.dependencies && rootEntry.dependencies[name])
    || (rootEntry.devDependencies && rootEntry.devDependencies[name]);
  if (inPkgLock !== range) {
    problems.push(`${name}: package.json=${range}  package-lock.json=${inPkgLock ?? '(缺失)'}`);
  }
  if (semverish.test(range) || range.startsWith('^') || range.startsWith('~')) {
    if (!(name in pnpmSpecifiers)) {
      problems.push(`${name}: package.json=${range}  pnpm-lock.yaml=(缺失)`);
    } else if (pnpmSpecifiers[name] !== range) {
      problems.push(`${name}: package.json=${range}  pnpm-lock.yaml=${pnpmSpecifiers[name]}`);
    }
  }
}

// And the other direction: a range sitting in a lockfile but not declared means
// somebody removed the dep from package.json without regenerating the lock.
for (const name of Object.keys(pnpmSpecifiers)) {
  if (!(name in declared)) {
    problems.push(`${name}: 只在 pnpm-lock.yaml 里，package.json 已无此依赖`);
  }
}

console.log(`[lockfiles]   声明依赖 ${Object.keys(declared).length} 个，pnpm specifier ${Object.keys(pnpmSpecifiers).length} 个`);
if (problems.length) {
  for (const p of problems) console.error(`[lockfiles] FAIL: ${p}`);
  process.exit(1);
}
console.log('[lockfiles] 三方声明区间一致');
NODE
if [ $? -ne 0 ]; then
  bad "声明区间比对失败 —— 改了 package.json 后必须同时重新生成两个 lockfile："
  bad "    (cd $WEB_DIR && pnpm install --lockfile-only) && (cd $WEB_DIR && npm install --package-lock-only)"
  fail=1
fi

# ── 3. 两条安装命令都要真的跑得通（不落盘） ───────────────────────────────
if command -v npm >/dev/null 2>&1; then
  note "验证 npm ci --dry-run"
  if ! (cd "$WEB_DIR" && npm ci --dry-run --ignore-scripts >/dev/null 2>&1); then
    bad "npm ci --dry-run 失败 —— npm 路径当前不可用"
  else
    note "ok  npm ci --dry-run"
  fi
fi

if command -v pnpm >/dev/null 2>&1; then
  note "验证 pnpm install --frozen-lockfile --lockfile-only"
  # --lockfile-only rewrites nothing when the lockfile is already correct and
  # never touches node_modules, so this is safe to run against a live checkout.
  if ! (cd "$WEB_DIR" && pnpm install --frozen-lockfile --lockfile-only --ignore-scripts >/dev/null 2>&1); then
    bad "pnpm install --frozen-lockfile 失败 —— pnpm 官方路径当前不可用"
  else
    note "ok  pnpm install --frozen-lockfile"
  fi
else
  note "skip: pnpm 不在 PATH 上，只验证 npm 路径"
fi

exit "$fail"
