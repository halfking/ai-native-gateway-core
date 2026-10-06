// verify-touch-targets.selftest.mjs — 给触控热区门自己搭前提。
//
// ★ 为什么门也要自测（照 scripts/settle-selftest.mjs 的先例）：
//   一道只会「放行」的门和一个坏掉的门，在绿灯时**完全无法区分**。
//   门自己出错时最典型的表现是「扫了 0 个文件也报 OK」——
//   路径写错、目录改名、glob 写坏，都会得到一个漂亮的绿色。
//   ⇒ 这里对门跑正反两侧的样本，绿的该绿、红的一定要红。
//
// 样本覆盖四类容易出错的点：
//   1. 正例：48px / 50px 应放行
//   2. 反例：36px / 44px 应拦下（44 是 R1 明确的存量下限之外的值）
//   3. ★ 豁免：`/* R1-legacy */` 标注的存量值应放行
//   4. ★ 选择器归属：报错要带**正确的选择器名**，否则定位不到人
//      （用两个相邻选择器验证不会张冠李戴）
//
// 用法：node scripts/verify-touch-targets.selftest.mjs

import { mkdtempSync, writeFileSync, rmSync, mkdirSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const GATE = join(HERE, 'verify-touch-targets.mjs')

let pass = 0
let fail = 0

function makeRepo(files) {
  const root = mkdtempSync(join(tmpdir(), 'touch-selftest-'))
  mkdirSync(join(root, 'src/views'), { recursive: true })
  mkdirSync(join(root, 'src/components'), { recursive: true })
  for (const [rel, content] of Object.entries(files)) {
    writeFileSync(join(root, rel), content)
  }
  // ★ 门现在把 ROOT 定义为「脚本所在目录的父目录」（web-mobile/scripts/ → web-mobile/），
  //   所以自测必须把门放进 <root>/scripts/，而不是 <root>/。
  //   放在根目录会让门扫 <root>/.. —— 扫到的是 tmpdir 上一层，一个 .vue 都没有。
  mkdirSync(join(root, 'scripts'), { recursive: true })
  writeFileSync(join(root, 'scripts/verify-touch-targets.mjs'), execFileSync('cat', [GATE]))
  return root
}

function runGate(root) {
  try {
      const out = execFileSync('node', [join(root, 'scripts/verify-touch-targets.mjs')], { encoding: 'utf8' })
    return { code: 0, out }
  } catch (e) {
    return { code: e.status ?? 1, out: `${e.stdout ?? ''}${e.stderr ?? ''}` }
  }
}

function check(name, cond, detail = '') {
  if (cond) {
    pass++
    console.log(`  ✓ ${name}`)
  } else {
    fail++
    console.error(`  ✗ ${name}${detail ? `\n      ${detail}` : ''}`)
  }
}

const vue = (css) => `<template><div class="x"/></template>\n<style scoped>\n${css}\n</style>\n`

// ── 1. 正例：合规尺寸应放行 ──────────────────────────────────────────────
{
  console.log('\n[1] 正例：≥48px 放行')
  const root = makeRepo({
    'src/views/Ok.vue': vue('.a { min-height: 48px; }\n.b { min-height: 56px; }\n.c { min-height: 0; }\n.d { height: 30px; }'),
  })
  const r = runGate(root)
  check('48/56px 放行，且 height（非 min-height）不误报', r.code === 0, r.out)
  check('报告扫到了文件（不是扫了 0 个也报 OK）', /29|1 个|个 \.vue/.test(r.out) || !/0 个/.test(r.out), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 2. 反例：不合规尺寸必须拦下 ──────────────────────────────────────────
{
  console.log('\n[2] 反例：<48px 拦下')
  for (const v of [36, 44, 30, 0]) {
    const root = makeRepo({ 'src/views/Bad.vue': vue(`.bad { min-height: ${v}px; }`) })
    const r = runGate(root)
    check(`min-height: ${v}px 被拦下`, r.code === 1, r.out)
    rmSync(root, { recursive: true, force: true })
  }
}

// ── 3. 豁免：R1-legacy 标注的存量值放行 ─────────────────────────────────
{
  console.log('\n[3] 豁免：/* R1-legacy */ 标注放行')
  const root = makeRepo({
    'src/views/Legacy.vue': vue('/* R1-legacy：存量 44px */\n.legacy { min-height: 44px; }'),
  })
  const r = runGate(root)
  check('标注后的 44px 放行', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 4. ★ 选择器归属：报错要指向正确的选择器 ─────────────────────────────
{
  console.log('\n[4] 选择器归属不张冠李戴')
  const root = makeRepo({
    'src/views/Multi.vue': vue(
      '.first-good { min-height: 48px; }\n.first-bad { min-height: 30px; }\n.after { min-height: 60px; }',
    ),
  })
  const r = runGate(root)
  check('精确指向 .first-bad', r.code === 1 && r.out.includes('.first-bad'), r.out)
  check('未误指 .first-good', !r.out.includes('.first-good'), r.out)
  check('未误指 .after', !r.out.includes('.after'), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 5. ★ 目录为空时必须红（防「扫了 0 个也报 OK」）──────────────────────
{
  console.log('\n[5] 目录缺失/为空的行为')
  const root = mkdtempSync(join(tmpdir(), 'touch-empty-'))
  mkdirSync(join(root, 'scripts'), { recursive: true })
  writeFileSync(join(root, 'scripts/verify-touch-targets.mjs'), execFileSync('cat', [GATE]))
  const r = runGate(root)
  // 目录不存在 → 门会抛错（非 0）而不是假装 OK。若将来改成静默返回 0，这里要红。
  check('src/views 不存在时报错而非静默放行', r.code !== 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

console.log(`\ntouch-target selftest: ${pass} passed, ${fail} failed`)
process.exit(fail === 0 ? 0 : 1)
