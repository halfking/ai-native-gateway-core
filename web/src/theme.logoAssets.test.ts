import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { applyTheme, currentTheme, detectTheme, logoSrc } from './theme'

/**
 * 品牌 logo 资产 + 主题切换链路回归（2026-10-28）。
 *
 * 用户报告：网站 logo「大小不对、样式也不对，亮暗皮肤也没分别」。
 * 三处真因（各自都能单独造成用户看到的现象）：
 *
 * ① 资产几何缩水：`scripts/gen-brand-icons.py` 的 render_icon 把 glyph 先
 *    paste 到 4× 超采样画布再缩回，而 recolored_logo 的 thumbnail() **只缩不放**
 *    ⇒ 源墨迹 bbox（496×601）到不了 0.66 目标 ⇒ 生成的 1024² 画布上图形
 *    只占 12% 宽 ⇒ 60px 展示位上实际图形只有 ~8px（一团糊）。
 *    经典资产（开轩设计系统 kx-design canonical）墨迹占 48% 宽。
 *
 * ② 暗色变体不换色：生成器的 DARK_RANK_STOPS 映射出的墨迹与亮色版
 *    几乎不可分，暗色页头上是一团看不清的深色；
 *    经典暗色版是冰蓝亮墨（#c0d0f0 族）。
 *
 * ③ 主题切换自激循环（**会把整个 SPA 冻死**）：监听 data-theme 的
 *    MutationObserver 回调里调用 detectTheme()，而 detectTheme() 会走
 *    applyThemeFromUrl() → applyTheme() → **写** documentElement.data-theme
 *    ⇒ MutationObserver 对「值未变的 setAttribute」同样投递记录
 *    ⇒ 观察者被自己的写入唤醒 ⇒ 自激微任务循环。
 *    实测：无头 Chrome 下 `?theme=light` 页面连 setTimeout(0) 都不再触发、
 *    截图挂起；去掉参数即恢复。
 *
 * ⇒ 资产几何与配色在【位图】里（判据 = 读 PNG 的 alpha 通道），
 *   ③④ 在【代码】里（判据 = 读源码 + 跑 DOM 行为）。
 */

const publicDir = resolve(process.cwd(), 'public')

/** 读 PNG 的 alpha 通道，返回墨迹 bbox 与占画布比例（不依赖第三方图像库）。 */
function readPngInkBox(file: string): {
  width: number
  height: number
  inkW: number
  inkH: number
  ratio: number
  /** 墨迹的平均亮度（0-255），用来判「亮版深墨 / 暗版亮墨」。 */
  inkLuma: number
  alphaLevels: number
} {
  const buf = readFileSync(resolve(publicDir, file))
  // PNG 签名
  expect(buf.subarray(0, 8)).toEqual(
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
  )
  // 遍历 chunk，读 IHDR
  let off = 8
  let width = 0
  let height = 0
  let bitDepth = 0
  let colorType = 0
  const idat: Buffer[] = []
  while (off < buf.length) {
    const len = buf.readUInt32BE(off)
    const type = buf.subarray(off + 4, off + 8).toString('ascii')
    const data = buf.subarray(off + 8, off + 8 + len)
    if (type === 'IHDR') {
      width = data.readUInt32BE(0)
      height = data.readUInt32BE(4)
      bitDepth = data[8]
      colorType = data[9]
    } else if (type === 'IDAT') {
      idat.push(Buffer.from(data))
    } else if (type === 'IEND') {
      break
    }
    off += 12 + len
  }
  expect(bitDepth).toBe(8)
  // colorType: 6 = RGBA
  expect(colorType).toBe(6)

  // 解 IDAT（zlib）→ 逐行反滤波 → 取 alpha
  const raw = inflateSync(Buffer.concat(idat))
  const bpp = 4
  const stride = width * bpp
  const out = Buffer.alloc(height * stride)
  let pos = 0
  for (let y = 0; y < height; y++) {
    const filter = raw[pos++]
    const row = raw.subarray(pos, pos + stride)
    pos += stride
    const cur = out.subarray(y * stride, (y + 1) * stride)
    const prev = y > 0 ? out.subarray((y - 1) * stride, y * stride) : null
    for (let x = 0; x < stride; x++) {
      const rawByte = row[x]
      const a = x >= bpp ? cur[x - bpp] : 0
      const b = prev ? prev[x] : 0
      const c = prev && x >= bpp ? prev[x - bpp] : 0
      let v: number
      switch (filter) {
        case 0: v = rawByte; break
        case 1: v = rawByte + a; break
        case 2: v = rawByte + b; break
        case 3: v = rawByte + ((a + b) >> 1); break
        case 4: {
          const p = a + b - c
          const pa = Math.abs(p - a)
          const pb = Math.abs(p - b)
          const pc = Math.abs(p - c)
          const pred = pa <= pb && pa <= pc ? a : pb <= pc ? b : c
          v = rawByte + pred
          break
        }
        default: throw new Error(`未处理的 PNG filter: ${filter}`)
      }
      cur[x] = v & 0xff
    }
  }

  let minX = width
  let minY = height
  let maxX = -1
  let maxY = -1
  let count = 0
  let lumaSum = 0
  const alphaSet = new Set<number>()
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = y * stride + x * bpp
      const alpha = out[i + 3]
      if (alpha > 0) alphaSet.add(alpha)
      if (alpha < 128) continue
      count++
      lumaSum += 0.299 * out[i] + 0.587 * out[i + 1] + 0.114 * out[i + 2]
      if (x < minX) minX = x
      if (x > maxX) maxX = x
      if (y < minY) minY = y
      if (y > maxY) maxY = y
    }
  }
  expect(count).toBeGreaterThan(0)
  const inkW = maxX - minX + 1
  const inkH = maxY - minY + 1
  return {
    width,
    height,
    inkW,
    inkH,
    ratio: inkW / width,
    inkLuma: lumaSum / count,
    alphaLevels: alphaSet.size,
  }
}

// zlib inflate：Node 内置，避免为测试引入依赖
import { inflateSync } from 'node:zlib'

describe('品牌 logo 资产（web/public）', () => {
  it('logo-icon.png / logo-icon-dark.png 都是 1024² RGBA（文件名被 auth 白名单硬编码）', () => {
    for (const f of ['logo-icon.png', 'logo-icon-dark.png']) {
      const box = readPngInkBox(f)
      expect(`${f}:${box.width}x${box.height}`).toBe(`${f}:1024x1024`)
    }
  })

  it('图形占画布宽度 ≥ 30%（经典资产 48%；缩水 bug 会掉到 12%）', () => {
    for (const f of ['logo-icon.png', 'logo-icon-dark.png']) {
      const box = readPngInkBox(f)
      // 60px 展示位上，12% ⇒ 实际图形 7px（糊成一团）；48% ⇒ 29px（可辨）
      expect(`${f} ratio=${box.ratio.toFixed(3)}`).toMatch(/ratio=0\.[3-9]/)
      expect(box.ratio).toBeGreaterThanOrEqual(0.3)
      // 高宽比 ~1.24（496×601 的窗格纹+字标），缩水不改变比例但会变小
      expect(box.inkH / box.inkW).toBeGreaterThan(1.0)
    }
  })

  it('亮版是深墨、暗版是亮墨（暗色皮肤上必须看得见）', () => {
    const light = readPngInkBox('logo-icon.png')
    const dark = readPngInkBox('logo-icon-dark.png')
    // 亮版深海军蓝（墨迹平均亮度 < 110）、暗版冰蓝（> 140）
    expect(light.inkLuma).toBeLessThan(110)
    expect(dark.inkLuma).toBeGreaterThan(140)
    // 两版几何必须一致（同一枚 logo 的明暗变体，不是两个设计）
    expect(Math.abs(light.inkW - dark.inkW)).toBeLessThanOrEqual(2)
    expect(Math.abs(light.inkH - dark.inkH)).toBeLessThanOrEqual(2)
  })

  it('logoSrc() 按皮肤选亮暗两版', () => {
    expect(logoSrc('light')).toBe('/logo-icon.png')
    expect(logoSrc('dark')).toBe('/logo-icon-dark.png')
  })

  it('生成器不再自造变体：brand/ 下有经典资产直通源', () => {
    const gen = readFileSync(resolve(process.cwd(), '../scripts/gen-brand-icons.py'), 'utf8')
    // 头部 logo 走「字节级拷贝」，不再走 render_icon（缩水的来源）
    expect(gen).not.toContain('def render_icon')
    // 两版都从 brand/ 直通到 web/public/，文件名不变（auth 白名单硬编码）
    expect(gen).toMatch(/for name in \("logo-icon\.png", "logo-icon-dark\.png"\)/)
    expect(gen).toMatch(/src = os\.path\.join\(BRAND, name\)/)
    expect(gen).toMatch(/dst = os\.path\.join\(ROOT, "web", "public", name\)/)
  })

  it('brand/ 下存着两份经典资产（生成器的直通源）', () => {
    for (const f of ['logo-icon.png', 'logo-icon-dark.png']) {
      const p = resolve(process.cwd(), '../brand', f)
      expect(existsSync(p)).toBe(true)
      // 直通源与 web/public 下的成品必须逐字节一致（不许中途被改）
      expect(readFileSync(p).equals(readFileSync(resolve(publicDir, f)))).toBe(true)
    }
  })
})

describe('主题切换链路（logo 换色 + 不冻死页面）', () => {
  it('applyTheme 幂等：同值不重写 data-theme（观察者自激循环的根因）', async () => {
    const root = document.documentElement
    applyTheme('light')
    let writes = 0
    const observer = new MutationObserver((records) => { writes += records.length })
    observer.observe(root, { attributes: true, attributeFilter: ['data-theme'] })

    // 同值重复应用：不产生属性变更记录 ⇒ 监听者不会被自己的写入唤醒
    applyTheme('light')
    applyTheme('light')
    await new Promise((r) => setTimeout(r, 0))
    expect(writes).toBe(0)

    // 真实变更：产生且仅产生 1 条记录
    applyTheme('dark')
    await new Promise((r) => setTimeout(r, 0))
    expect(writes).toBe(1)
    observer.disconnect()
  })

  it('currentTheme() 是纯读：不会写 data-theme', async () => {
    const root = document.documentElement
    applyTheme('dark')
    let writes = 0
    const observer = new MutationObserver((records) => { writes += records.length })
    observer.observe(root, { attributes: true, attributeFilter: ['data-theme'] })
    expect(currentTheme()).toBe('dark')
    await new Promise((r) => setTimeout(r, 0))
    expect(writes).toBe(0)
    observer.disconnect()
  })

  it('观察者回调用 currentTheme()（无副作用），不再用 detectTheme()', () => {
    // 四个 logo 观察者点：回调里调 detectTheme() ⇒ 写属性 ⇒ 自激循环 ⇒ 页面冻结
    const observers = [
      'src/App.vue',
      'src/components/LoginModal.vue',
      'src/components/shell/AppTopbar.vue',
      'src/views/LandingView.vue',
    ]
    for (const f of observers) {
      const src = readFileSync(resolve(process.cwd(), f), 'utf8')
      const lines = src.split('\n')
      for (const line of lines) {
        if (!line.includes('new MutationObserver')) continue
        expect(`${f}: ${line.trim()}`).toContain('currentTheme()')
        expect(`${f}: ${line.trim()}`).not.toContain('detectTheme()')
      }
      // 观察者必须存在（否则本用例 vacuous 通过）
      expect(src).toContain("attributeFilter: ['data-theme']")
    }
  })

  it('detectTheme() 仍按 URL > localStorage > 系统 的优先级求值（行为未变）', () => {
    // URL 永远赢（jsdom 里 location.search 不可直接赋值 ⇒ 用 history 改 URL）
    window.history.replaceState({}, '', '/?theme=dark')
    try {
      expect(detectTheme()).toBe('dark')
    } finally {
      window.history.replaceState({}, '', '/')
    }
    // 无 URL 参数时退回 localStorage
    localStorage.setItem('llmgw_theme', 'dark')
    try {
      expect(detectTheme()).toBe('dark')
    } finally {
      localStorage.removeItem('llmgw_theme')
    }
  })
})
