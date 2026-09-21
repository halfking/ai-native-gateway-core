import { describe, expect, it } from 'vitest'

// color-token-audit.mjs 的 color-mix 豁免纯函数（源在 scripts/lib/，不在 vitest
// include 的 src/** 内，故测试放 src/ 以相对路径引入；类型经 .d.mts 提供）。
// 豁免语义：仅黑白成分（#000/#fff，明度调节、两主题一致）替换为 __MIX__；
// 非黑白成分（品牌/数据色硬编码）必须保留，交由调用方 HEX 扫描照常报告。
import { exemptColorMixBlacks, stripVarFallbacks } from '../scripts/lib/color-audit-scan.mjs'

function scan(lines: string[]) {
  const state = { inMix: false, depth: 0 }
  return lines.map((l) => exemptColorMixBlacks(l, state))
}

describe('exemptColorMixBlacks (color-token-audit core)', () => {
  it('exempts black/white components only, keeps brand colors reportable', () => {
    const state = { inMix: false, depth: 0 }
    // var() 内嵌 + 尾部黑白成分（§十八 修复场景：[^)]* 截断会漏掉 #000）
    expect(exemptColorMixBlacks('color-mix(in srgb, var(--success) 88%, #000)', state)).toBe(
      'color-mix(in srgb, var(--success) 88%, __MIX__)',
    )
    // 非黑白成分保留（漏报防护核心）
    expect(exemptColorMixBlacks('color-mix(in srgb, #1e4fd6 50%, #fff)', state)).toBe(
      'color-mix(in srgb, #1e4fd6 50%, __MIX__)',
    )
    // 3/6 位与大小写
    expect(exemptColorMixBlacks('color-mix(in srgb, var(--p) 70%, #000000)', state)).toContain(
      '__MIX__',
    )
    expect(exemptColorMixBlacks('color-mix(in srgb, var(--p) 90%, #FFFFFF)', state)).toContain(
      '__MIX__',
    )
  })

  it('spans multiple lines without false-reporting continuation blacks', () => {
    const out = scan([
      '.x {',
      '  border: 1px solid',
      '  color-mix(in srgb,',
      '    var(--accent) 60%,',
      '    #000);',
      '}',
    ])
    // 续行的 #000 被豁免（此前按行扫描会误报）
    expect(out[4]).toBe('    __MIX__);')
    expect(out.join('\n')).not.toMatch(/#000\b|#fff\b/i)
  })

  it('reports a brand hex on a continuation line (no over-exemption)', () => {
    const out = scan(['color-mix(in srgb,', '  var(--x) 50%,', '  #1e4fd6);'])
    expect(out[2]).toContain('#1e4fd6')
  })

  it('handles multiple color-mix() on one line', () => {
    const state = { inMix: false, depth: 0 }
    expect(
      exemptColorMixBlacks(
        'a: color-mix(in srgb, var(--p) 70%, #000), b: color-mix(in srgb, var(--q) 10%, #fff)',
        state,
      ),
    ).toBe('a: color-mix(in srgb, var(--p) 70%, __MIX__), b: color-mix(in srgb, var(--q) 10%, __MIX__)')
  })

  it('navigates nested rgba() inside color-mix', () => {
    const state = { inMix: false, depth: 0 }
    expect(
      exemptColorMixBlacks('color-mix(in srgb, rgba(0, 0, 0, 0.2) 50%, var(--c))', state),
    ).toBe('color-mix(in srgb, rgba(0, 0, 0, 0.2) 50%, var(--c))')
  })
})

describe('stripVarFallbacks (color-token-audit fallback extraction)', () => {
  it('extracts a simple hex fallback and preserves var structure', () => {
    const out = stripVarFallbacks('color: var(--danger, #c2413b);')
    expect(out.fallbacks).toEqual([' #c2413b'])
    expect(out.line).toBe('color: var(--danger,__FALLBACK__);')
  })

  it('extracts a nested rgba fallback the [^)]+ regex used to swallow (E-P1)', () => {
    const out = stripVarFallbacks('background: var(--kx-surface-soft, rgba(0, 0, 0, 0.03));')
    // 兜底文本必须完整保留 rgba 字面量,供调用方 HEX/RGB 扫描上报
    expect(out.fallbacks).toEqual([' rgba(0, 0, 0, 0.03)'])
    expect(out.line).toBe('background: var(--kx-surface-soft,__FALLBACK__);')
  })

  it('handles doubly nested var() fallbacks', () => {
    const out = stripVarFallbacks(
      'background: var(--kx-primary-soft, var(--accent-soft, rgba(0, 0, 0, 0.06)));',
    )
    expect(out.fallbacks).toEqual([' var(--accent-soft, rgba(0, 0, 0, 0.06))'])
    expect(out.line).toBe(
      'background: var(--kx-primary-soft,__FALLBACK__);',
    )
  })

  it('leaves var() without fallback untouched', () => {
    const out = stripVarFallbacks('color: var(--text-primary);')
    expect(out.fallbacks).toEqual([])
    expect(out.line).toBe('color: var(--text-primary);')
  })

  it('keeps rgba(var(--x, triplet), alpha) structural exemption intact', () => {
    const out = stripVarFallbacks('box-shadow: 0 0 0 0 rgba(var(--accent-rgb, 63, 120, 255), .5);')
    // 十进制三元组兜底不是 hex/rgba 字面量:提取但由调用方扫描判定不报;
    // 占位替换后仍保持 var() 结构,rgba(var(...)) 豁免正则可命中
    expect(out.fallbacks).toEqual([' 63, 120, 255'])
    expect(out.line).toBe(
      'box-shadow: 0 0 0 0 rgba(var(--accent-rgb,__FALLBACK__), .5);',
    )
    expect(out.line).toMatch(/rgba\(var\(--accent-rgb,__FALLBACK__\), \.5\)/)
  })

  it('returns original text for a cross-line (unclosed) var()', () => {
    const out = stripVarFallbacks('background: var(--x, rgba(0, 0, 0,')
    expect(out.fallbacks).toEqual([])
    expect(out.line).toBe('background: var(--x, rgba(0, 0, 0,')
  })
})
