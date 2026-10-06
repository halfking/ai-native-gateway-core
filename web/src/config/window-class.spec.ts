/**
 * window-class / useWindowClass 门禁（docs/UI规范/00 §5.4 · H0）。
 *
 * 本 spec 的存在意义是**钉住双镜像**：语义四档的数值必须由
 * `config/breakpoints.ts` 派生。一旦有人改了 breakpoints 而忘了同步本层，
 * 这里立刻红，而不是等到某台手机上底栏不显示。
 *
 * 另外两条不可省：
 * - 媒体查询只能是传统 min/max-width 语法（仓库禁止 `width <= Xpx` 范围语法）；
 * - `derive()` 必须取**最宽**命中档（1440 同时命中三条 min-width，
 *   窄到宽取会把它判成 medium —— 这是实现里最容易写反的一处）。
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { BREAKPOINTS } from './breakpoints'
import {
  WINDOW_CLASS_ATTR,
  WINDOW_CLASS_MAX_PX,
  WINDOW_CLASS_MEDIA,
  WINDOW_CLASS_ORDER,
  WINDOW_CLASS_PX,
  currentWindowClass,
  isDesktopOnlyAllowed,
} from './window-class'
import { useWindowClass, _resetForTests, getWindowClass } from '../composables/useWindowClass'

type FakeMql = {
  matches: boolean
  media: string
  addEventListener: (type: string, listener: () => void) => void
  removeEventListener: (type: string, listener: () => void) => void
  fire: (matches: boolean) => void
}

function createFakeMql(media: string, initialMatches: boolean): FakeMql {
  const listeners = new Set<() => void>()
  const mql: FakeMql = {
    matches: initialMatches,
    media,
    addEventListener: (_t, l) => listeners.add(l),
    removeEventListener: (_t, l) => listeners.delete(l),
    fire(next: boolean) {
      mql.matches = next
      listeners.forEach((l) => l())
    },
  }
  return mql
}

/** 按视口宽度真实求值媒体查询（支持 .98 分数边界）。 */
function widthMatchesQuery(width: number, query: string): boolean {
  const max = query.match(/max-width:\s*([\d.]+)px/)
  if (max) return width <= parseFloat(max[1])
  const min = query.match(/min-width:\s*([\d.]+)px/)
  if (min) return width >= parseFloat(min[1])
  return false
}

function installMatchMediaForWidth(width: number): FakeMql[] {
  const created: FakeMql[] = []
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => {
      const mql = createFakeMql(query, widthMatchesQuery(width, query))
      created.push(mql)
      return mql
    },
  })
  return created
}

describe('window-class：断点双镜像', () => {
  it('四档下界与上界严格由 breakpoints.ts 派生（改 SSOT 必红）', () => {
    expect(WINDOW_CLASS_PX.compact).toBe(0)
    expect(WINDOW_CLASS_PX.medium).toBe(BREAKPOINTS.tablet)
    expect(WINDOW_CLASS_PX.expanded).toBe(BREAKPOINTS.desktop)
    expect(WINDOW_CLASS_PX.large).toBe(BREAKPOINTS.wide)

    expect(WINDOW_CLASS_MAX_PX.compact).toBe(BREAKPOINTS.tablet)
    expect(WINDOW_CLASS_MAX_PX.medium).toBe(BREAKPOINTS.desktop)
    expect(WINDOW_CLASS_MAX_PX.expanded).toBe(BREAKPOINTS.wide)
    expect(WINDOW_CLASS_MAX_PX.large).toBe(Number.POSITIVE_INFINITY)
  })

  it('相邻档位边界严丝合缝：上一档上界 === 下一档下界，无缝隙无重叠', () => {
    for (let i = 0; i < WINDOW_CLASS_ORDER.length - 1; i++) {
      const cur = WINDOW_CLASS_ORDER[i]
      const next = WINDOW_CLASS_ORDER[i + 1]
      expect(WINDOW_CLASS_MAX_PX[cur]).toBe(WINDOW_CLASS_PX[next])
    }
  })

  it('本仓映射值就是 768/1024/1440（决策 D2：不照抄参考仓 600/960/1280）', () => {
    expect(WINDOW_CLASS_PX.medium).toBe(768)
    expect(WINDOW_CLASS_PX.expanded).toBe(1024)
    expect(WINDOW_CLASS_PX.large).toBe(1440)
  })

  it('媒体查询只用传统 min/max-width 语法，无范围语法（仓库门禁禁止项）', () => {
    for (const query of Object.values(WINDOW_CLASS_MEDIA)) {
      expect(query).toMatch(/^\((min|max)-width:\s*[\d.]+px\)$/)
      expect(query).not.toMatch(/[<>]=?/) // 禁 width<= / width>= 范围语法
    }
  })

  it('源码不含脱离 BREAKPOINTS 的裸像素魔法数', () => {
    const src = readFileSync(
      resolve(process.cwd(), 'src/config/window-class.ts'),
      'utf8',
    )
    // 去掉注释后再扫，避免把文档里的说明文字当成违规
    const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
    // 允许：0 / Infinity / 0.02（分数边界构造）/ 派生自 BREAKPOINTS 的成员访问
    const magic = code.match(/(?<![\w.)\]])\d{3,4}(?![\w.)\]])/g) ?? []
    expect(magic, `发现裸像素数：${magic.join(', ')}`).toEqual([])
  })
})

describe('currentWindowClass：纯函数边界', () => {
  const cases: Array<[number, string]> = [
    [320, 'compact'],
    [375, 'compact'],
    [767, 'compact'],
    [767.98, 'compact'],
    [768, 'medium'],
    [800, 'medium'],
    [1023, 'medium'],
    [1024, 'expanded'],
    [1280, 'expanded'],
    [1439, 'expanded'],
    [1440, 'large'],
    [1920, 'large'],
    [2560, 'large'],
    // ★ 横屏真机宽度：这两条把「横屏手机会落到哪一档」钉死，防止有人改断点时
    //   静默改掉真机行为。背景见 docs/UI规范/15 §2.1：
    //   914 ≥ 768 ⇒ medium（平板档布局），740 < 768 ⇒ compact。
    //   两者**不在同一档**，而真机上横屏手机两种都可能出现 ⇒ 横屏验收必须两档都跑。
    [740, 'compact'],
    [914, 'medium'],
  ]
  it.each(cases)('宽度 %i → %s', (width, expected) => {
    expect(currentWindowClass(width)).toBe(expected)
  })

  it('desktopOnly 判定：仅 compact 禁止', () => {
    expect(isDesktopOnlyAllowed('compact')).toBe(false)
    expect(isDesktopOnlyAllowed('medium')).toBe(true)
    expect(isDesktopOnlyAllowed('expanded')).toBe(true)
    expect(isDesktopOnlyAllowed('large')).toBe(true)
  })
})

describe('useWindowClass：单例绑定与推导', () => {
  beforeEach(() => _resetForTests())
  afterEach(() => _resetForTests())

  it('1440 同时命中三条 min-width，必须取最宽档 large（不是 medium）', () => {
    installMatchMediaForWidth(1440)
    const { windowClass } = useWindowClass()
    expect(windowClass.value).toBe('large')
  })

  it('320 手机 → compact，且 isMobileShell=true', () => {
    installMatchMediaForWidth(320)
    const { windowClass, isCompact, isMobileShell, isLarge } = useWindowClass()
    expect(windowClass.value).toBe('compact')
    expect(isCompact.value).toBe(true)
    expect(isMobileShell.value).toBe(true)
    expect(isLarge.value).toBe(false)
  })

  it('800 平板 → medium，isMobileShell=true（沿用 <1024 旧口径）', () => {
    installMatchMediaForWidth(800)
    const { windowClass, isMedium, isMobileShell, isCompact } = useWindowClass()
    expect(windowClass.value).toBe('medium')
    expect(isMedium.value).toBe(true)
    expect(isMobileShell.value).toBe(true)
    expect(isCompact.value).toBe(false)
  })

  it('1280 常规桌面 → expanded（不是 large）', () => {
    installMatchMediaForWidth(1280)
    const { windowClass, isExpanded, isMobileShell } = useWindowClass()
    expect(windowClass.value).toBe('expanded')
    expect(isExpanded.value).toBe(true)
    expect(isMobileShell.value).toBe(false)
  })

  it('只绑定三条 min-width 查询，compact 作为兜底无独立监听', () => {
    const created = installMatchMediaForWidth(1280)
    useWindowClass()
    expect(created.map((m) => m.media).sort()).toEqual(
      [
        WINDOW_CLASS_MEDIA.medium,
        WINDOW_CLASS_MEDIA.expanded,
        WINDOW_CLASS_MEDIA.large,
      ].sort(),
    )
  })

  it('档位变化时 ref 与根节点 data 属性同步更新', () => {
    const created = installMatchMediaForWidth(1280)
    const { windowClass } = useWindowClass()
    expect(windowClass.value).toBe('expanded')
    expect(document.documentElement.getAttribute(WINDOW_CLASS_ATTR)).toBe('expanded')

    // 折叠屏展开 / 旋转 → 落到手机宽度
    const medium = created.find((m) => m.media === WINDOW_CLASS_MEDIA.medium)!
    const expanded = created.find((m) => m.media === WINDOW_CLASS_MEDIA.expanded)!
    const large = created.find((m) => m.media === WINDOW_CLASS_MEDIA.large)!
    medium.fire(false)
    expanded.fire(false)
    large.fire(false)

    expect(windowClass.value).toBe('compact')
    expect(document.documentElement.getAttribute(WINDOW_CLASS_ATTR)).toBe('compact')
    expect(getWindowClass()).toBe('compact')
  })

  it('全局单例：多次调用共享同一 ref', () => {
    installMatchMediaForWidth(768)
    const a = useWindowClass()
    const b = useWindowClass()
    expect(a.windowClass).toBe(b.windowClass)
  })

  it('matchMedia 不可用时安全降级为默认 expanded，不抛错', () => {
    Object.defineProperty(window, 'matchMedia', { writable: true, configurable: true, value: undefined })
    const { windowClass, isCompact, isExpanded } = useWindowClass()
    expect(windowClass.value).toBe('expanded')
    expect(isCompact.value).toBe(false)
    expect(isExpanded.value).toBe(true)
  })
})
