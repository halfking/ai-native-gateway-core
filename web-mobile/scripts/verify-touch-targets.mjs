// verify-touch-targets.mjs — 触控热区门（UI规范 17 §4-R1 × 06 §7）
//
// ## 为什么要有这道门
//
// R1 原文：「**新增**触控控件一律 ≥48 CSS px；44px 是存量控件下限，不是新标准。」
// 这条规则在**移动端此前没有任何门**（`web/` 侧 10 §4.6.5 记了「已加门禁」，
// `web-mobile/` 没有）。实测后果：本专题 2026-10-06 新增的 4 个视图里，
// 5 处 chip 写成 32-40px（`.logs__chip` 32/36、`.providers__chip` 36、
// `.usage__dim` 36、`.routing__go` 40），全部违反 R1 —— 一次都没被发现。
//
// 根因与 §11.25 的 dispose 缺口同族：**规范写了、没人验 ⇒ 等于没有**。
//
// ## 扫什么 / 不扫什么
//
// 扫：<style scoped> 块里 min-height 低于 48px 的**选择器**。
// 不扫 shared.css 的 .btn / .btn--sm —— 它们是 44px 的**存量**基线，R1 明确
// 「44px 是存量控件下限，不是新标准」。把存量也扫进来会让这道门一开始就红，
// 于是大家学会忽略它 —— 门一旦变成背景噪音就等于没有。
//
// 豁免：显式 `/* R1-legacy */` 标注的行（表示「已知存量，故意不改」），
// 用于把存量控件从新规里摘出来时留下可审计的痕迹，而不是悄悄放过。

import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, relative, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// ⚠️ 2026-10-06 改法（两次才对）：
//   ① 原用 `new URL('..', import.meta.url).pathname` —— macOS 上带 /private 前缀。
//   ② 换 fileURLToPath 后，`new URL('..')` 仍指**脚本所在目录的父目录**；
//      而门期望 ROOT = web-mobile/ 本身（脚本在 web-mobile/scripts/）。
//      在 selftest 里表现为「ROOT 指到 tmpdir 上一层，扫 0 个文件」。
//   ③ 修成 dirname(脚本) 后又少退一层 —— 门在 scripts/ 里，ROOT 要的是
//      web-mobile/（脚本目录的**父**目录）。真实运行时报「扫描目录缺失」。
//   ⇒ resolve(dirname(脚本), '..')：脚本目录 + 明确一层，不靠猜。
//   ★ 这条本身就是「一道扫不到文件却报 OK 的门 = 比没有门更坏」的实例：
//     上面三种错法都曾产出漂亮的绿灯（因为当时还没有「扫到 0 个必须红」这道自查）。
//     是第 5 组自测（目录缺失必须红）把它们全部照出来的。
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const TARGET = 48
const SCAN_DIRS = ['src/views', 'src/components']

function walk(dir, acc = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) walk(p, acc)
    else if (p.endsWith('.vue')) acc.push(p)
  }
  return acc
}

const violations = []
let scanned = 0

// ★ 扫不到文件必须**报错**，不能静默 OK：目录被改名/路径写错时，
//   「0 个违规」与「全部合规」在输出上原本无法区分。
if (SCAN_DIRS.some((d) => !existsSync(join(ROOT, d)))) {
  const missing = SCAN_DIRS.filter((d) => !existsSync(join(ROOT, d)))
  console.error(`touch-target R1 扫描目录缺失：${missing.join(', ')}（ROOT=${ROOT}）`)
  process.exit(1)
}

for (const dir of SCAN_DIRS) {
  for (const file of walk(join(ROOT, dir))) {
    scanned++
    const lines = readFileSync(file, 'utf8').split('\n')
    let selector = '(file scope)'
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i]
      // 记当前所处的选择器，便于定位
      // 匹配形如 `.foo {` / `.foo .bar {` 的选择器行尾。
      // ★ 用「取最后一个 { 之前的全部内容」而不是一串字符类白名单 ——
      //   白名单版本在自测样本 4 上漏匹配，报成 `(file scope)`，定位不到人。
      const brace = line.indexOf('{')
      if (brace > 0 && !line.trimStart().startsWith('@') && !line.trimStart().startsWith('/*')) {
        const maybe = line.slice(0, brace).trim()
        if (/^[.#][^0-9]/.test(maybe)) selector = maybe
      }
      const m = line.match(/min-height:\s*(\d+)px/)
      if (!m) continue
      const v = Number(m[1])
      if (v >= TARGET) continue
      // 存量豁免：显式标注
      const ctx = lines.slice(Math.max(0, i - 3), i + 1).join('\n')
      if (ctx.includes('R1-legacy')) continue
      violations.push({ file: relative(ROOT, file), line: i + 1, selector, value: v })
    }
  }
}

if (violations.length > 0) {
  console.error(`\ntouch-target R1 不通过：${violations.length} 处 min-height < ${TARGET}px`)
  console.error(`（UI规范 17 §4-R1：新增触控控件一律 ≥48 CSS px；44px 是存量下限）\n`)
  for (const v of violations) {
    console.error(`  ${v.file}:${v.line}  ${v.selector}  min-height: ${v.value}px`)
  }
  console.error(`\n修法：抬到 ${TARGET}px，或加 /* R1-legacy */ 标注为已知存量。\n`)
  process.exit(1)
}

if (scanned === 0) {
  console.error('touch-target R1 扫到 0 个 .vue —— 视为失败（路径写错时不该报 OK）')
  process.exit(1)
}
console.log(`touch-target R1 OK: ${scanned} 个 .vue 中无 min-height < ${TARGET}px 的选择器`)
