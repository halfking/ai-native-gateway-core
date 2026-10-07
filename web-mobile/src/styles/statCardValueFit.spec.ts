import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

// 量值换行门禁（UI规范 10 §4.6.74 / D-TRUNC-01）。
//
// 为什么需要门：`font_scale` 只放大渲染字号、rem/em 都不放大（§4.6.72 真机实测），
// 所以「让版式变宽来容纳变大的数字」这条路在 Android 上结构性走不通
// —— 已从两头各证伪一次（rem 不缩放；em 也不缩放，9em 仍是 9×14）。
// 唯一的出路是让内容自己换行，而**换行恰恰是最容易被后来者改回去的一行**：
// `white-space: nowrap` 看起来只是「防抖/防串行」的常规写法，
// 很容易在「统一一下卡片样式」时被无声改回 nowrap，而版式读数完全不会报警。
//
// 本组断言钉三件事：
//   ① `.stat-card__value` 允许换行，且没有被裸 `nowrap` 覆盖回来；
//   ② `overflow-wrap` 与 `white-space` 成对（单配 break-word 是惰性的）；
//   ③ 值槽承载的确实是量值，不是标识符（换行会从中间劈开标识符）。

const ROOT = resolve(process.cwd(), 'src')
const STAT_CARD = resolve(ROOT, 'components/common/StatCard.vue')

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) walk(full, out)
    else if (/\.(vue|ts)$/.test(entry)) out.push(full)
  }
  return out
}

/** 取 `.stat-card__value { … }` 这一条规则体（CSS 注释先剥掉，免得注释里的字样被当成声明）。 */
function valueRule(src: string): string {
  const stripped = src.replace(/\/\*[\s\S]*?\*\//g, '')
  const m = stripped.match(/\.stat-card__value\s*\{([^}]*)\}/)
  if (!m || m[1] == null) throw new Error('StatCard.vue 里找不到 .stat-card__value 规则')
  return m[1]
}

describe('stat 卡量值换行（§4.6.74 / D-TRUNC-01）', () => {
  const rule = valueRule(readFileSync(STAT_CARD, 'utf8'))

  it('值槽允许换行：white-space 不是 nowrap', () => {
    expect(rule).toMatch(/white-space\s*:\s*normal/)
    expect(rule).not.toMatch(/white-space\s*:\s*nowrap/)
  })

  it('overflow-wrap 与 white-space 成对存在（单配 overflow-wrap 是惰性的）', () => {
    // 依据：溢出读数在「只加 overflow-wrap、white-space 仍为 nowrap」时与不加时逐项相同。
    expect(rule).toMatch(/overflow-wrap\s*:\s*break-word/)
    expect(rule).toMatch(/white-space\s*:\s*normal/)
  })

  it('不用 anywhere：它参与 min-content 计算，会改动网格轨道下限', () => {
    expect(rule).not.toMatch(/overflow-wrap\s*:\s*anywhere/)
  })

  it('值槽只承载量值（换行不会从中间劈开标识符）', () => {
    // 所有 StatCard 调用点的 value 都必须是格式化后的量值或字面量 '—'，
    // 不允许出现 key/model/credential 之类标识符字段直传。
    const callers = walk(join(ROOT, 'views')).filter((f) => f.endsWith('.vue'))
    const bindings: string[] = []
    for (const f of callers) {
      const src = readFileSync(f, 'utf8')
      for (const m of src.matchAll(/<StatCard\b[\s\S]*?\/>/g)) {
        const v = m[0].match(/:value="([^"]*)"/)
        if (v) bindings.push(`${relative(ROOT, f)}: ${v[1]}`)
      }
    }
    expect(bindings.length).toBeGreaterThan(0)
    const forbidden = /\b(key|keys|model|models|name|credential|token_id|api_key)\b(?![^(]*\))/i
    for (const b of bindings) {
      // 允许 fmtInt / fmtUsd / fmtNum / relativeTime 之类；禁止裸标识符字段
      expect(b, `疑似把标识符直接塞进 stat 卡值槽：${b}`).not.toMatch(
        new RegExp(forbidden.source, 'i'),
      )
    }
  })
})