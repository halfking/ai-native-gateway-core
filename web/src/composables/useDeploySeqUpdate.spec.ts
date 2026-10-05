// useDeploySeqUpdate.spec.ts — 部署序号检查链行为判据（docs/UI规范/18 §3/§4）。
// 与 web-mobile/src/hyper/update/useDeploySeqUpdate.spec.ts 同形（双侧同步义务）。
// 每条对应一类真实错误实现：
//   - 缺 cache:'no-store' ⇒ 永远读到旧序号；
//   - 异常冒泡 ⇒ 检查打断页面（18 §4 末段）；
//   - 序号变化直接 reload 不看三条件 ⇒ 丢用户输入。
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  DEPLOY_SEQ_META,
  UPDATE_FIRST_CHECK_DELAY_MS,
  UPDATE_IDLE_THRESHOLD_MS,
  UPDATE_POLL_MS,
  useDeploySeqUpdate,
  elementBlocksAutoReload,
  shouldAutoReload,
} from './useDeploySeqUpdate'

function pageDoc(seq: string): Document {
  const html = `<html><head><meta name="${DEPLOY_SEQ_META}" content="${seq}"></head><body></body></html>`
  return new DOMParser().parseFromString(html, 'text/html')
}

function htmlResponse(body: string): Response {
  return new Response(body, { status: 200, headers: { 'content-type': 'text/html' } })
}

const { checkNow, status, reason, bannerVisible, localSeq, start, resetDeploySeqUpdateForTest } =
  useDeploySeqUpdate()

function boot(opts?: {
  local?: string
  remote?: string
  remoteSeq?: string
  reject?: boolean
  activeEl?: Element | null
}) {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  const fetchImpl = async (url: string, init?: RequestInit): Promise<Response> => {
    calls.push({ url, init })
    if (opts?.reject) throw new TypeError('network down')
    const body =
      opts?.remote !== undefined
        ? opts.remote
        : `<html><head><meta name="${DEPLOY_SEQ_META}" content="${opts?.remoteSeq ?? '2451'}"></head></html>`
    return htmlResponse(String(body))
  }
  const reload = vi.fn()
  start({
    fetchImpl,
    doc: pageDoc(opts?.local ?? '2450'),
    reloadImpl: reload,
    now: () => 1_700_000_000_000,
    ...(opts?.activeEl !== undefined ? { activeElementProvider: () => opts.activeEl as Element | null } : {}),
  })
  return { calls, reload }
}

afterEach(() => {
  resetDeploySeqUpdateForTest()
  vi.restoreAllMocks()
})

describe('纯逻辑判据', () => {
  it('阈值常量按规范取值', () => {
    expect(UPDATE_POLL_MS).toBe(5 * 60_000)
    expect(UPDATE_FIRST_CHECK_DELAY_MS).toBeGreaterThanOrEqual(3_000)
    expect(UPDATE_IDLE_THRESHOLD_MS).toBeGreaterThanOrEqual(10_000)
  })

  it('自动重载三条件：缺一即降级', () => {
    expect(shouldAutoReload({ hasInputFocus: false, msSinceInteraction: 60_000 })).toBe(true)
    expect(shouldAutoReload({ hasInputFocus: true, msSinceInteraction: 600_000 })).toBe(false)
    expect(shouldAutoReload({ hasInputFocus: false, msSinceInteraction: UPDATE_IDLE_THRESHOLD_MS - 1 })).toBe(false)
  })

  it('contenteditable 判定（计算值/属性/标记三信号）', () => {
    const doc = pageDoc('2450')
    const editable = doc.createElement('div')
    editable.setAttribute('contenteditable', 'true')
    expect(elementBlocksAutoReload(editable)).toBe(true)
    const input = doc.createElement('input')
    expect(elementBlocksAutoReload(input)).toBe(true)
    expect(elementBlocksAutoReload(doc.createElement('div'))).toBe(false)
    expect(elementBlocksAutoReload(null)).toBe(false)
  })
})

describe('检查链', () => {
  it('请求 /index.html 且必须 cache:no-store', async () => {
    const { calls } = boot()
    await checkNow()
    expect(calls.length).toBe(1)
    expect(calls[0]?.url).toBe('/index.html')
    expect(calls[0]?.init?.cache).toBe('no-store')
  })

  it('本页缺 meta ⇒ 不可判定(no-local) 且不发请求', async () => {
    const { calls } = boot({ local: '' })
    await checkNow()
    expect(status.value).toBe('indeterminate')
    expect(reason.value).toBe('no-local')
    expect(calls.length).toBe(0)
    expect(localSeq.value).toBeNull()
  })

  it('序号相等 ⇒ latest 不出条', async () => {
    boot({ remoteSeq: '2450' })
    await checkNow()
    expect(status.value).toBe('latest')
    expect(bannerVisible.value).toBe(false)
  })

  it('远端旧构建无 meta ⇒ 不可判定(no-remote)——「我不知道」不得显示成「已是最新」', async () => {
    boot({ remote: '<html><head></head></html>' })
    await checkNow()
    expect(status.value).toBe('indeterminate')
    expect(reason.value).toBe('no-remote')
  })

  it('fetch 异常 ⇒ 不可判定(network)，不冒泡', async () => {
    boot({ reject: true })
    await expect(checkNow()).resolves.toBeUndefined()
    expect(status.value).toBe('indeterminate')
    expect(reason.value).toBe('network')
  })
})

describe('序号变化 → 自动重载三条件', () => {
  it('静置 ⇒ 直接 reload', async () => {
    const { reload } = boot({ remoteSeq: '2451' })
    await checkNow()
    expect(reload).toHaveBeenCalledTimes(1)
    expect(bannerVisible.value).toBe(false)
  })

  it('刚交互 ⇒ 出提示条不 reload', async () => {
    const { reload } = boot({ remoteSeq: '2451' })
    document.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await checkNow()
    expect(reload).not.toHaveBeenCalled()
    expect(bannerVisible.value).toBe(true)
  })

  it('焦点在输入控件 ⇒ 出提示条不 reload', async () => {
    const input = document.createElement('input')
    const { reload } = boot({ remoteSeq: '2451', activeEl: input })
    await checkNow()
    expect(reload).not.toHaveBeenCalled()
    expect(bannerVisible.value).toBe(true)
  })
})
