/**
 * color-audit-scan.mjs — color-token-audit.mjs 的可单测纯函数部分。
 *
 * exemptColorMixBlacks(line, state)：对一行扫描其中的 color-mix(...) 表达式，
 * 仅豁免其黑白成分（#000/#fff 是明度调节，两主题语义一致）；非黑白成分色
 * （潜在品牌/数据色硬编码）保留原样，由调用方的 HEX/RGB 扫描照常报告。
 *
 * 跨行支持：CSS 允许 color-mix 的参数跨行书写。若行尾括号未平衡，state.inMix
 * 置 true 且 state.depth 记录未闭合深度，下一行从该深度继续扫描——否则续行里
 * 的黑白成分（如 `, #000);`）会被误报为违规（2026-09-13 审计轮修正）。
 * 每个文件（对 .vue 为每个 style 块）开始扫描前应重置 state。
 */

export const MIX_BLACKWHITE_RE = /#000000\b|#000\b|#ffffff\b|#fff\b/gi

const MIX_OPEN = 'color-mix('

/**
 * @param {string} line 已去除注释的一行
 * @param {{ inMix: boolean, depth: number }} state 跨行状态，就地更新
 * @returns {string} 黑白成分替换为 __MIX__ 占位的行文本
 */
export function exemptColorMixBlacks(line, state = { inMix: false, depth: 0 }) {
  let out = ''
  let i = 0
  for (;;) {
    if (state.inMix) {
      let j = 0
      while (j < line.length && state.depth > 0) {
        if (line[j] === '(') state.depth++
        else if (line[j] === ')') state.depth--
        j++
      }
      out += line.slice(0, j).replace(MIX_BLACKWHITE_RE, '__MIX__')
      i = j
      state.inMix = state.depth > 0
      if (state.inMix) return out
    }
    const idx = line.indexOf(MIX_OPEN, i)
    if (idx === -1) return out + line.slice(i)
    out += line.slice(i, idx)
    let depth = 1
    let j = idx + MIX_OPEN.length
    while (j < line.length && depth > 0) {
      if (line[j] === '(') depth++
      else if (line[j] === ')') depth--
      j++
    }
    out += line.slice(idx, j).replace(MIX_BLACKWHITE_RE, '__MIX__')
    i = j
    state.inMix = depth > 0
    state.depth = depth
  }
}
