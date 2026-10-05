// useDeploySeqUpdate.spec.ts — 检查链行为判据（UI规范 18 §4）。
// 每条对应一类真实错误实现：
//   - 缺 cache:'no-store' ⇒ 永远读到旧序号（18 §3 末段）；
//   - 异常冒泡 ⇒ 检查打断页面（18 §4 末段「检查失败绝不打断页面」）；
//   - 序号变化直接 reload 不看三条件 ⇒ 丢用户输入。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useDeploySeqUpdate } from './useDeploySeqUpdate'
import { DEPLOY_SEQ_META, UPDATE_FIRST_CHECK_DELAY_MS, UPDATE_POLL_MS } from './deploySeq'

function pageDoc(seq: string): Document {
  const html = `<html><head><meta name="${DEPLOY_SEQ_META}" content="${seq}"></head><body></body></html>`
  return new DOMParser().parseFromString(html, 'text/html')
}

function htmlResponse(body: string, init?: ResponseInit): Response {
  return new Response(body, { status: 200, headers: { 'content-type': 'text/html' }, ...init })
}

const { checkNow, status, reason, bannerVisible, checkCount, localSeq, resetUpdateCheckForTest, start } =
  useDeploySeqUpdate()

function boot(opts?: { local?: string; remote?: string | null; remoteSeq?: string; reject?: boolean; activeEl?: Element | null }) {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  const doc = pageDoc(opts?.local ?? '2450')
  const fetchImpl = async (url: string, init?: RequestInit): Promise<Response> => {
    calls.push({ url, init })
    if (opts?.reject) throw new TypeError('network down')
    const remoteBody =
      opts?.remote !== undefined
        ? opts.remote
        : `<html><head><meta name="${DEPLOY_SEQ_META}" content="${opts?.remoteSeq ?? '2451'}"></head></html>`
    return htmlResponse(String(remoteBody))
  }
  const reload = vi.fn()
  let fixedNow = 1_700_000_000_000
  start({
    fetchImpl,
    doc,
    reloadImpl: reload,
    now: () => fixedNow,
    ...(opts?.activeEl !== undefined ? { activeElementProvider: () => opts.activeEl as Element | null } : {}),
  })
  return { calls, reload, doc, tick: (ms: number) => (fixedNow += ms) }
}

afterEach(() => {
  resetUpdateCheckForTest()
  vi.restoreAllMocks()
})

describe('checkNow — 检查链', () => {
  it('请求入口文档且必须带 cache:no-store（否则读到的是旧序号）', async () => {
    const { calls } = boot()
    await checkNow()
    expect(calls.length).toBe(1)
    expect(calls[0]?.init?.cache).toBe('no-store')
  })

  it('本页缺 seq meta ⇒ 不可判定(no-local)且不发网络请求', async () => {
    const { calls } = boot({ local: '' })
    await checkNow()
    expect(status.value).toBe('indeterminate')
    expect(reason.value).toBe('no-local')
    expect(calls.length).toBe(0)
    expect(localSeq.value).toBeNull()
  })

  it('序号相等 ⇒ latest，不出提示条', async () => {
    boot({ remoteSeq: '2450' })
    await checkNow()
    expect(status.value).toBe('latest')
    expect(bannerVisible.value).toBe(false)
    expect(checkCount.value).toBe(1)
  })

  it('远端旧构建无 meta ⇒ 不可判定(no-remote)，不得显示「已是最新」', async () => {
    boot({ remote: '<html><head></head></html>' })
    await checkNow()
    expect(status.value).toBe('indeterminate')
    expect(reason.value).toBe('no-remote')
  })

  it('fetch 异常 ⇒ 不可判定(network)，不冒泡打断页面', async () => {
    boot({ reject: true })
    await expect(checkNow()).resolves.toBeUndefined()
    expect(status.value).toBe('indeterminate')
    expect(reason.value).toBe('network')
  })

  it('HTTP 非 200 ⇒ 同样不可判定(network)', async () => {
    const doc = pageDoc('2450')
    start({
      doc,
      fetchImpl: async () => new Response('nope', { status: 503 }),
      reloadImpl: vi.fn(),
      now: () => 0,
    })
    await checkNow()
    expect(status.value).toBe('indeterminate')
    expect(reason.value).toBe('network')
  })
})

describe('时机契约（18 §4 表）', () => {
  it('首次检查晚于首屏落地延迟，不与首屏数据抢网络', async () => {
    vi.useFakeTimers()
    try {
      const { calls } = boot({ remoteSeq: '2450' })
      expect(calls.length).toBe(0)
      await vi.advanceTimersByTimeAsync(UPDATE_FIRST_CHECK_DELAY_MS - 1)
      expect(calls.length).toBe(0)
      await vi.advanceTimersByTimeAsync(1)
      expect(calls.length).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('页面不可见时暂停轮询，回前台补查一次', async () => {
    vi.useFakeTimers()
    const setHidden = (hidden: boolean) => {
      Object.defineProperty(document, 'hidden', { value: hidden, configurable: true })
      document.dispatchEvent(new Event('visibilitychange'))
    }
    try {
      const { calls } = boot({ remoteSeq: '2450' })
      await vi.advanceTimersByTimeAsync(UPDATE_FIRST_CHECK_DELAY_MS)
      expect(calls.length).toBe(1)
      setHidden(true)
      await vi.advanceTimersByTimeAsync(UPDATE_POLL_MS * 3)
      expect(calls.length).toBe(1)
      setHidden(false)
      expect(calls.length).toBe(2)
      await vi.advanceTimersByTimeAsync(UPDATE_POLL_MS)
      expect(calls.length).toBe(3)
    } finally {
      setHidden(false)
      Object.defineProperty(document, 'hidden', {
        get: () => false,
        configurable: true,
      })
      vi.useRealTimers()
    }
  })
})

describe('序号变化 → 自动重载三条件（18 §4）', () => {
  it('从未交互（静置）⇒ 直接 reload，不出提示条', async () => {
    const { reload } = boot({ remoteSeq: '2451' })
    await checkNow()
    expect(reload).toHaveBeenCalledTimes(1)
    expect(bannerVisible.value).toBe(false)
  })

  it('刚交互过（未过静置阈值）⇒ 不 reload，出非阻塞提示条', async () => {
    const { reload } = boot({ remoteSeq: '2451' })
    document.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await checkNow()
    expect(reload).not.toHaveBeenCalled()
    expect(bannerVisible.value).toBe(true)
  })

  it('焦点在输入控件 ⇒ 不 reload，出提示条', async () => {
    const input = document.createElement('input')
    const { reload } = boot({ remoteSeq: '2451', activeEl: input })
    await checkNow()
    expect(reload).not.toHaveBeenCalled()
    expect(bannerVisible.value).toBe(true)
  })
})
