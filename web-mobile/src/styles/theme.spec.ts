import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// theme.spec.ts —— 状态色「文字压在 soft 填充上」的对比度棘轮。
//
// 为什么要这道门（一次真实的回退）：
//   `theme.css` 自己的注释记着上一轮按 AA 正文 4.5:1 修过两个 token ——
//   「原 #16845b on #e6f7ef = 4.22、#4a6ee0 on #eaf0ff = 4.00，都差一点」，
//   success 与 info 因此被压深。**但 warning 与 danger 没有一起改**，而
//   `--app-warning: #b7791f` 压在 `--app-warning-soft: #fff7e8` 上只有
//   **3.42:1**，12px 徽标文字不合格。
//   这次是 2026-10-07 把 `layoutAudit()` 跑到真实线上页面时才抓到的
//   （/m/alerts 上 5 处 `badge--warning`，12 档视口全部复现）。
//
//   ⇒ 上一轮那两个值是**人工目测改的，没有任何判据钉住**，所以必然回退。
//     这道门就是那个缺失的钉子。
//
// ⚠️ 门槛按 **AA 正文 4.5:1**，不是大字 3:1：徽标是 12px，属正常字号。
//    判据的 low-contrast 阈值也是 4.5，两边口径一致，改一处要改两处。

const css = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/** 取出某个选择器块里的 `--name: value`。 */
function tokensIn(selector: string): Map<string, string> {
  const at = css.indexOf(`${selector} {`)
  if (at < 0) throw new Error(`theme.css 里找不到块 ${selector}`)
  const body = css.slice(at, css.indexOf('\n}', at))
  const m = new Map<string, string>()
  for (const t of body.matchAll(/--([a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    const name = t.at(1), val = t.at(2)
    if (name !== undefined && val !== undefined) m.set(name, val.trim())
  }
  return m
}

const light = tokensIn(':root')
const dark = tokensIn('html.dark')

/** 取 token，缺就抛 —— `expect(x).toBeTruthy()` **不做**类型收窄，vue-tsc -b 会拦。 */
function must(tokens: Map<string, string>, name: string, where: string): string {
  const v = tokens.get(name)
  if (!v) throw new Error(`theme.css 的 ${where} 块里没有 --${name}`)
  return v
}

function hex(s: string): [number, number, number] {
  const h = s.trim().replace('#', '')
  if (!/^[0-9a-fA-F]{6}$/.test(h)) throw new Error(`不是 6 位 hex：${s}`)
  return [Number.parseInt(h.slice(0, 2), 16),
          Number.parseInt(h.slice(2, 4), 16),
          Number.parseInt(h.slice(4, 6), 16)]
}

function lum(rgb: [number, number, number]): number {
  const c = rgb.map((v) => {
    const x = v / 255
    return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
  })
  // noUncheckedIndexedAccess 下 c[i] 是 number|undefined ⇒ 解构并显式兜底。
  const [r = 0, g = 0, b = 0] = c
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(fg: [number, number, number], bg: [number, number, number]): number {
  const a = lum(fg), b = lum(bg)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

/** `rgb(R G B / A%)` 叠在给底色上 —— 暗色 soft 是半透明，必须先合成再算。 */
function compositeOverSoft(value: string, surface: string): [number, number, number] {
  const m = value.match(/^rgb\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\/\s*([\d.]+)%\s*\)$/i)
  if (!m) return hex(value)
  const g = (i: number, what: string): number => {
    const v = m.at(i)
    if (v === undefined) throw new Error(`soft 颜色 ${value} 缺 ${what}`)
    return Number(v)
  }
  const a = g(4, 'alpha') / 100
  const fg: [number, number, number] = [g(1, 'r'), g(2, 'g'), g(3, 'b')]
  const bg = hex(surface)
  const mix = fg.map((v, i) => Math.round(v * a + (bg[i] ?? 0) * (1 - a)))
  return [mix[0] ?? 0, mix[1] ?? 0, mix[2] ?? 0]
}

/**
 * 状态色的全集。**门只检查这里列的**，所以新增一个状态色时必须同时加进本表 ——
 * 这是硬约束的代价：漏加不会让门变绿，只会让新色**无人检查**。
 * ⇒ 第二条断言专门盯着这件事：`theme.css` 里出现但本表没有的状态色会让门变红。
 */
const STATUSES = ['success', 'warning', 'danger', 'info'] as const
const FLOOR = 4.5

describe('状态色徽标对比度（AA 正文 4.5:1）', () => {
  for (const s of STATUSES) {
    it(`亮色 --app-${s} 压在 --app-${s}-soft 上 ≥ ${FLOOR}`, () => {
      const fg = must(light, `app-${s}`, ':root')
      const soft = must(light, `app-${s}-soft`, ':root')
      const r = contrast(hex(fg), hex(soft))
      expect(r, `--app-${s} ${fg} 压在 --app-${s}-soft ${soft} 上只有 ${r.toFixed(2)}:1`).toBeGreaterThanOrEqual(FLOOR)
    })

    it(`暗色 --app-${s} 压在 ${s}-soft（半透明，合成到 surface）上 ≥ ${FLOOR}`, () => {
      const fg = must(dark, `app-${s}`, 'html.dark')
      const soft = must(dark, `app-${s}-soft`, 'html.dark')
      const surface = must(dark, 'app-surface', 'html.dark')
      const bg = compositeOverSoft(soft, surface)
      const r = contrast(hex(fg), bg)
      expect(r, `暗色 --app-${s} ${fg} 压合成底 ${bg} 上只有 ${r.toFixed(2)}:1`).toBeGreaterThanOrEqual(FLOOR)
    })
  }

  // 棘轮：theme.css 里新加了状态色却没进 STATUSES ⇒ 门红，而不是静默放过。
  it('theme.css 里没有「本表未覆盖」的状态色', () => {
    const declared = new Set<string>()
    for (const t of light.keys()) {
      const name = t.match(/^app-([a-z0-9]+)(?:-soft)?$/)?.at(1)
      if (name && light.has(`app-${name}`) && light.has(`app-${name}-soft`)) declared.add(name)
    }
    // 只盯「有 solid 前景 + 有 soft 填充」这一对形态；其余 token（primary/text/border）不算。
    const stateful = new Set([...declared].filter((d) => !['primary', 'surface', 'bg'].includes(d)))
    const missing = [...stateful].filter((d) => !(STATUSES as readonly string[]).includes(d))
    expect(
      missing,
      `theme.css 出现新的状态色 ${missing.join(', ')}，已加 -soft 配对 —— ` +
      '请把它加进本文件 STATUSES 并定门槛，否则新色无人检查',
    ).toEqual([])
    // 反向：表里列了但 theme.css 没有的，同样是漂移。
    const vanished = (STATUSES as readonly string[]).filter((s) => !light.has(`app-${s}`))
    expect(vanished, `STATUSES 列了 ${vanished.join(', ')} 但 theme.css 里没有该 token`).toEqual([])
  })
})
