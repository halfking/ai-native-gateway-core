// exportFile.spec.ts — 下载降级链行为判据（docs/UI规范/19 §3.1/§3.2）。
// 判据按「链条顺序固定、触发时判定、失败可见」三条规则设计：
//   - canShare 可用 ⇒ 走 ①分享面，不建 a[download]；
//   - canShare 不可用（桌面基线）⇒ 走 ③blob 四步——桌面零回归（D-1）；
//   - 用户取消分享 = 'cancelled'，不再兜底下载（取消后又弹下载是双打扰）；
//   - 通道全失败 ⇒ 抛 ExportChannelError（D-4 如实报错的机制面）。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { exportFile, setFileExportBridge, ExportChannelError } from './exportFile'

interface NavShape {
  canShare?: (data: ShareData) => boolean
  share?: (data: ShareData) => Promise<void>
}

function stubNavigator(nav: NavShape | null): void {
  vi.stubGlobal('navigator', nav ?? {})
}

const realNavigator = navigator

afterEach(() => {
  vi.unstubAllGlobals()
  setFileExportBridge(null)
  // 还原真实 navigator（stubGlobal('navigator') 需显式回填）
  Object.defineProperty(globalThis, 'navigator', { value: realNavigator, configurable: true })
})

function clickSpy() {
  const clicks: Array<{ download: string; href: string }> = []
  const desc = Object.getOwnPropertyDescriptor(HTMLAnchorElement.prototype, 'click')
  Object.defineProperty(HTMLAnchorElement.prototype, 'click', {
    configurable: true,
    value(this: HTMLAnchorElement) {
      clicks.push({ download: this.download, href: this.href })
    },
  })
  return {
    clicks,
    restore() {
      if (desc) Object.defineProperty(HTMLAnchorElement.prototype, 'click', desc)
    },
  }
}

describe('exportFile 降级链', () => {
  it('① canShare 为真 ⇒ 走系统分享面，不建下载锚（D-2 机制面）', async () => {
    stubNavigator({
      canShare: () => true,
      share: vi.fn().mockResolvedValue(undefined),
    })
    const anchor = clickSpy()
    try {
      const out = await exportFile({ filename: 'a.csv', blob: new Blob(['x'], { type: 'text/csv' }) })
      expect(out).toBe('shared')
      expect(anchor.clicks.length).toBe(0)
    } finally {
      anchor.restore()
    }
  })

  it('① 分享被用户取消(AbortError) ⇒ cancelled，不兜底下载', async () => {
    stubNavigator({
      canShare: () => true,
      share: vi.fn().mockRejectedValue(new DOMException('abort', 'AbortError')),
    })
    const anchor = clickSpy()
    try {
      const out = await exportFile({ filename: 'a.csv', blob: new Blob(['x']) })
      expect(out).toBe('cancelled')
      expect(anchor.clicks.length).toBe(0)
    } finally {
      anchor.restore()
    }
  })

  it('① 分享通道失败(非取消) ⇒ 降级 ③ blob 兜底', async () => {
    stubNavigator({
      canShare: () => true,
      share: vi.fn().mockRejectedValue(new TypeError('no user gesture')),
    })
    const anchor = clickSpy()
    try {
      const out = await exportFile({ filename: 'a.csv', blob: new Blob(['x']) })
      expect(out).toBe('downloaded')
      expect(anchor.clicks.length).toBe(1)
      expect(anchor.clicks[0]?.download).toBe('a.csv')
    } finally {
      anchor.restore()
    }
  })

  it('canShare 为 false（桌面基线）⇒ ③ blob 四步：append→click→remove→revoke（D-1 零回归）', async () => {
    stubNavigator({ canShare: () => false, share: vi.fn() })
    const anchor = clickSpy()
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    try {
      const out = await exportFile({ filename: 'r.json', blob: new Blob(['{}'], { type: 'application/json' }) })
      expect(out).toBe('downloaded')
      expect(anchor.clicks.length).toBe(1)
      expect(anchor.clicks[0]?.download).toBe('r.json')
      expect(anchor.clicks[0]?.href).toMatch(/^blob:/)
      expect(revoke).toHaveBeenCalledTimes(1)
    } finally {
      anchor.restore()
      revoke.mockRestore()
    }
  })

  it('canShare 缺失（老宿主）⇒ 直接 ③', async () => {
    stubNavigator({})
    const anchor = clickSpy()
    try {
      const out = await exportFile({ filename: 'a.txt', blob: new Blob(['x']) })
      expect(out).toBe('downloaded')
    } finally {
      anchor.restore()
    }
  })

  it('② 壳桥已注册且返回 true ⇒ 由桥消费，不建下载锚', async () => {
    stubNavigator({})
    const anchor = clickSpy()
    const bridge = vi.fn().mockResolvedValue(true)
    setFileExportBridge(bridge)
    try {
      const out = await exportFile({ filename: 'a.csv', blob: new Blob(['x']) })
      expect(out).toBe('shared')
      expect(bridge).toHaveBeenCalledTimes(1)
      expect(anchor.clicks.length).toBe(0)
    } finally {
      anchor.restore()
    }
  })

  it('② 壳桥抛错/返回 false ⇒ 降级 ③', async () => {
    stubNavigator({})
    const anchor = clickSpy()
    setFileExportBridge(() => Promise.reject(new Error('bridge down')))
    try {
      const out = await exportFile({ filename: 'a.csv', blob: new Blob(['x']) })
      expect(out).toBe('downloaded')
      expect(anchor.clicks.length).toBe(1)
    } finally {
      anchor.restore()
    }
  })
})

describe('失败可见（19 §3.1 规则 4）', () => {
  it('③ 不可用（无 DOM）⇒ 抛 ExportChannelError，不静默', async () => {
    stubNavigator({})
    const origCreate = URL.createObjectURL
    // @ts-expect-error 测试通道全断
    URL.createObjectURL = undefined
    try {
      await expect(
        exportFile({ filename: 'a.csv', blob: new Blob(['x']) }),
      ).rejects.toBeInstanceOf(ExportChannelError)
    } finally {
      URL.createObjectURL = origCreate
    }
  })
})
