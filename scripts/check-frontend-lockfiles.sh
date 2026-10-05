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
# are not in sync" 失败，而不是在 CI 里失败。CI 此前只跑 pnpm，完全看不见。
#
# 2026-10-04 审计轮：web-mobile（统一入口的移动前端，deploy-local/seamless
# 生产路径）纳入门内。它的契约是 **pnpm-only**：仓库不携带 package-lock.json，
# node-pm 的 pnpm 分支用 --frozen-lockfile 安装；npm 分支退回非冻结 install
# 仅是未装 pnpm 的开发机兜底。因此对 web-mobile 只要求 pnpm-lock.yaml 在
# 版本控制里且与 package.json 声明区间一致——这恰是生产路径真正消费的东西。
#
# 判定方式不是"两个 lockfile 的完整解析树相同"（pnpm 与 npm 的扁平化布局本就
# 不同，比不了），而是三方对**声明区间**必须逐字相同：
#   package.json            dependencies/devDependencies[name]
#   package-lock.json       packages[""].dependencies/devDependencies[name]（dual）
#   pnpm-lock.yaml          importers['.'].dependencies/devDependencies[name].specifier
# 只要有一方漏了重新生成，区间就对不上，门会红。
#
# 同时实跑安装命令（--dry-run / --lockfile-only，不动 node_modules），
# 确保不是"区间看起来一致但命令跑不通"。
#
# 2026-10-05（R43 移交项 4 收口）：pnpm 缺失默认仍是 skip —— npm-only 的开发机
# 与 CI 的 verify-npm 作业刻意不装 pnpm，硬 fail 会误伤。但官方 pnpm 作业里
# skip 等于把门的整半边静默放行（pnpm/action-setup 一旦失效，pnpm 那半边永远
# 不再被验证而门照样绿）。因此提供 LOCKFILES_REQUIRE_PNPM=1：置位时 pnpm 不在
# PATH 直接 FAIL。verify-ci.yml 的官方 pnpm 作业已置位。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

fail=0
note() { printf '[lockfiles] %s\n' "$*"; }
bad()  { printf '[lockfiles] FAIL: %s\n' "$*" >&2; fail=1; }

command -v node >/dev/null 2>&1 || { bad "node 不在 PATH 上"; exit 1; }

# check_dir <dir> <mode>
#   mode=dual  : package.json + package-lock.json + pnpm-lock.yaml 三方一致（web/）
#   mode=pnpm  : package.json + pnpm-lock.yaml 两方一致（web-mobile/）
check_dir() {
    dir_check="$1"
    mode_check="$2"

    note "== ${dir_check}（${mode_check}）"

    # ── 1. lockfile 必须在版本控制里 ────────────────────────────────────
    # 没被 git 跟踪的 lockfile 等于不存在：别人 clone 下来根本没有它。
    dir_fail=0
    lf_list="$dir_check/package.json $dir_check/pnpm-lock.yaml"
    [ "$mode_check" = dual ] && lf_list="$dir_check/package-lock.json $lf_list"
    for lf in $lf_list; do
      [ -f "$lf" ] || { bad "$lf 不存在"; dir_fail=1; continue; }
      if git ls-files --error-unmatch "$lf" >/dev/null 2>&1; then
        note "ok  $lf 已跟踪"
      else
        bad "$lf 未被 git 跟踪 —— 双包管理器模式下缺失它等于该路径不可用"
        dir_fail=1
      fi
    done
    [ "$dir_fail" -eq 0 ] || return 0

    # ── 2. 声明区间必须逐字一致 ─────────────────────────────────────────
    note "比对 $dir_check 的声明区间"
    node - "$dir_check" "$mode_check" <<'NODE'
const fs = require('fs');
const path = require('path');
const dir = process.argv[2];
const mode = process.argv[3]; // 'dual' | 'pnpm'
const read = (f) => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));

const pkg = read('package.json');
const hasNpmLock = mode === 'dual';

// package-lock.json v3 mirrors the declared ranges under packages[""].
const rootEntry = hasNpmLock
  ? ((read('package-lock.json').packages || {})[''] || {})
  : {};

const yaml = fs.readFileSync(path.join(dir, 'pnpm-lock.yaml'), 'utf8');

const declared = Object.assign({}, pkg.dependencies, pkg.devDependencies);

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

for (const [name, range] of Object.entries(declared)) {
  if (hasNpmLock) {
    const inPkgLock = (rootEntry.dependencies && rootEntry.dependencies[name])
      || (rootEntry.devDependencies && rootEntry.devDependencies[name]);
    if (inPkgLock !== range) {
      problems.push(`${name}: package.json=${range}  package-lock.json=${inPkgLock ?? '(缺失)'}`);
    }
  }
  if (!(name in pnpmSpecifiers)) {
    problems.push(`${name}: package.json=${range}  pnpm-lock.yaml=(缺失)`);
  } else if (pnpmSpecifiers[name] !== range) {
    problems.push(`${name}: package.json=${range}  pnpm-lock.yaml=${pnpmSpecifiers[name]}`);
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
console.log('[lockfiles] 声明区间一致');
NODE
    if [ $? -ne 0 ]; then
      bad "$dir_check 声明区间比对失败 —— 改了 package.json 后必须重新生成 lockfile："
      bad "    (cd $dir_check && pnpm install --lockfile-only)$( [ "$mode_check" = dual ] && echo " && (cd $dir_check && npm install --package-lock-only)" )"
    fi

    # ── 3. 安装命令真的跑得通（不落盘） ────────────────────────────────
    # npm ci 只对 dual 契约有意义（web-mobile 无 package-lock.json，npm 分支
    # 本就是非冻结兜底，不在门内）。
    if [ "$mode_check" = dual ] && command -v npm >/dev/null 2>&1; then
      note "验证 $dir_check npm ci --dry-run"
      if ! (cd "$dir_check" && npm ci --dry-run --ignore-scripts >/dev/null 2>&1); then
        bad "$dir_check npm ci --dry-run 失败 —— npm 路径当前不可用"
      else
        note "ok  $dir_check npm ci --dry-run"
      fi
    fi

    if command -v pnpm >/dev/null 2>&1; then
      # --lockfile-only rewrites nothing when the lockfile is already correct and
      # never touches node_modules, so this is safe to run against a live checkout.
      note "验证 $dir_check pnpm install --frozen-lockfile --lockfile-only"
      if ! (cd "$dir_check" && pnpm install --frozen-lockfile --lockfile-only --ignore-scripts >/dev/null 2>&1); then
        bad "$dir_check pnpm install --frozen-lockfile 失败 —— pnpm 官方路径当前不可用"
      else
        note "ok  $dir_check pnpm install --frozen-lockfile"
      fi
    else
      # R43 移交项 4：谁在跑这半边门，谁就必须声明 pnpm 可缺席。官方 pnpm
      # 作业置 LOCKFILES_REQUIRE_PNPM=1 —— 那里 pnpm 缺失是环境坏了，必须红，
      # 不能让「pnpm 半边没验」伪装成绿。
      if [ "${LOCKFILES_REQUIRE_PNPM:-0}" = "1" ]; then
        bad "pnpm 不在 PATH 上 —— LOCKFILES_REQUIRE_PNPM=1（官方 pnpm 作业）要求 pnpm 必须存在，skip 会把 pnpm 半边门静默放行"
      else
        note "skip: pnpm 不在 PATH 上，$dir_check 只验证 npm 路径（若有）"
      fi
    fi
    return 0
}

check_dir "${WEB_DIR:-web}" dual
check_dir "${WEB_MOBILE_DIR:-web-mobile}" pnpm

exit "$fail"
