/**
 * hyper/back.spec.ts — 覆盖层登记 + 单一返回仲裁门禁（docs/UI规范/00 §5.4 · H1）。
 *
 * 重点是三条硬约束：
 * 1. 覆盖层不可关闭 / 脏表单时，**消费返回并保持现状**，绝不穿透到背景路由；
 * 2. 单飞 —— 快速连按不双 pop；
 * 3. 不用 `history.length` 判断业务父页，只认 entry 链。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { OverlayRegistry, overlays } from './overlay'
import {
  BackDispatcher,
  back,
  consumePendingPop,
  peekPendingPop,
  type NavigationPort,
} from './back'
import { NavigationStore } from './context'
import type { NavigationScope } from './types'

const SCOPE: NavigationScope = { serverId: 'gw', accountId: 'u1' }

function freshStore(): NavigationStore {
  const s = new NavigationStore()
  s.setScope(SCOPE)
  return s
}

function freshDispatcher(reg = new OverlayRegistry()): BackDispatcher {
  return new BackDispatcher()
}

function port(overrides: Partial<NavigationPort> = {}): NavigationPort & { pop: ReturnType<typeof vi.fn> } {
  return {
    pop: vi.fn().mockResolvedValue(true),
    replace: vi.fn().mockResolvedValue(true),
    isAtRoot: () => true,
    deliverToSystem: () => false,
    ...overrides,
  } as NavigationPort & { pop: ReturnType<typeof vi.fn> }
}

beforeEach(() => {
  overlays._reset()
  back._reset()
})

describe('OverlayRegistry：栈与仲裁', () => {
  let reg: OverlayRegistry
  beforeEach(() => {
    reg = new OverlayRegistry()
  })

  it('top() 取 priority 最大者；同 priority 取后登记者', () => {
    // 三层都不给 priority → 落回登记序，c 最后登记，应为栈顶
    reg.register({ id: 'a', presentation: 'modal', dismissible: true, dirty: false, close: () => {} })
    reg.register({ id: 'b', presentation: 'sheet', dismissible: true, dirty: false, close: () => {} })
    reg.register({ id: 'c', presentation: 'focus', dismissible: true, dirty: false, close: () => {} })
    expect(reg.top()?.id).toBe('c')
    // 显式 priority 高者压过后登记者
    reg.register({ id: 'd', presentation: 'sheet', dismissible: true, dirty: false, close: () => {}, priority: 99 })
    expect(reg.top()?.id).toBe('d')
    // 显式 priority 低者不抢占
    reg.register({ id: 'e', presentation: 'modal', dismissible: true, dirty: false, close: () => {}, priority: 1 })
    expect(reg.top()?.id).toBe('d')
  })

  it('同 id 重复登记是替换而非堆叠（KeepAlive 反复激活不产生两层）', () => {
    const off1 = reg.register({ id: 'x', presentation: 'modal', dismissible: true, dirty: false, close: () => {} })
    const off2 = reg.register({ id: 'x', presentation: 'modal', dismissible: true, dirty: false, close: () => {} })
    expect(reg.depth).toBe(1)
    off2()
    expect(reg.depth).toBe(0)
    off1() // 重复注销必须幂等，不能误删别人的层
    expect(reg.depth).toBe(0)
  })

  it('注销函数幂等', () => {
    const off = reg.register({ id: 'x', presentation: 'modal', dismissible: true, dirty: false, close: () => {} })
    off()
    off()
    off()
    expect(reg.depth).toBe(0)
  })

  it('closeTop：dismissible=false 时拒绝且不调 close', () => {
    const close = vi.fn()
    reg.register({ id: 'sys', presentation: 'modal', dismissible: false, dirty: false, close })
    return expect(reg.closeTop()).resolves.toBe(false).then(() => expect(close).not.toHaveBeenCalled())
  })

  it('closeTop：closeGuard 拒绝时消费返回，层仍在栈顶', async () => {
    const close = vi.fn()
    reg.register({
      id: 'dirty',
      presentation: 'modal',
      dismissible: true,
      dirty: true,
      close,
      closeGuard: () => false,
    })
    await expect(reg.closeTop()).resolves.toBe(false)
    expect(close).not.toHaveBeenCalled()
    expect(reg.depth).toBe(1)
  })

  it('closeTop：closeGuard 通过后才执行 close', async () => {
    const close = vi.fn()
    reg.register({
      id: 'ok',
      presentation: 'modal',
      dismissible: true,
      dirty: true,
      close,
      closeGuard: () => true,
    })
    await expect(reg.closeTop()).resolves.toBe(true)
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('closeTop：close 抛错时返回 false 而不是让异常冒泡', async () => {
    reg.register({
      id: 'boom',
      presentation: 'modal',
      dismissible: true,
      dirty: false,
      close: () => {
        throw new Error('component unmounted')
      },
    })
    await expect(reg.closeTop()).resolves.toBe(false)
  })

  it('订阅者抛错不影响其他订阅者，也不让 registry 崩', () => {
    const good = vi.fn()
    reg.subscribe(() => {
      throw new Error('bad listener')
    })
    reg.subscribe(good)
    expect(() => reg.register({ id: 'a', presentation: 'modal', dismissible: true, dirty: false, close: () => {} })).not.toThrow()
    expect(good).toHaveBeenCalled()
  })
})

describe('BackDispatcher：仲裁次序', () => {
  it('子 consumer 优先于覆盖层（先关选择器，不是关弹窗）', async () => {
    const d = freshDispatcher()
    const consumerClose = vi.fn()
    const overlayClose = vi.fn()
    d.registerConsumer({ id: 'picker', close: consumerClose, priority: 10 })
    overlays.register({ id: 'ov', presentation: 'modal', dismissible: true, dirty: false, close: overlayClose })

    await expect(d.request('system')).resolves.toBe('dismissed-overlay')
    expect(consumerClose).toHaveBeenCalledTimes(1)
    expect(overlayClose).not.toHaveBeenCalled()
  })

  it('无覆盖层时按 entry 前驱返回，不看 history.length', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/a', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/b', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const p = port()
    // 即便浏览器历史里有 10 条，entry 链只有 1 条可退 —— 判据必须是 entry 链。
    // （jsdom 的 history.length 只读，用 defineProperty 覆盖）
    Object.defineProperty(window.history, 'length', { configurable: true, value: 10 })
    try {
      await expect(d.request('button', { store: s, port: p })).resolves.toBe('navigated')
      expect(p.pop).toHaveBeenCalledTimes(1)
      // 关键：request() 只登记意图，**不动 cursor**。
      // cursor 由 RouterHistoryAdapter 在导航成功后统一左移。
      expect(s.current()?.fullPath).toBe('/b')
      expect(peekPendingPop()).toBe(s.current()?.id)
    } finally {
      Reflect.deleteProperty(window.history, 'length')
    }
  })

  it('两阶段提交：request 登记意图，适配器成功后才左移 cursor', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/a', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/b', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const p = port()
    await d.request('button', { store: s, port: p })
    // 模拟 afterEach：取走 pending 并对齐
    const pending = consumePendingPop()
    expect(pending).toBeTruthy()
    expect(s.applyExternalPop(pending!)).toBe(true)
    expect(s.current()?.fullPath).toBe('/a')
    // pending 只能被消费一次
    expect(consumePendingPop()).toBeNull()
  })

  it('守卫拒绝：cursor 完全不动，也不留 pending 标记', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/a', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/b', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const p = port({ pop: vi.fn().mockResolvedValue(false) })
    await expect(d.request('gesture', { store: s, port: p })).resolves.toBe('consumed-by-guard')
    expect(s.current()?.fullPath).toBe('/b') // 原地不动
    expect(peekPendingPop()).toBeNull() // 没有残留意图
    // 下一次返回仍能正常退一层，不会「跳过」
    await expect(d.request('button', { store: s, port: port() })).resolves.toBe('navigated')
  })

  it('applyExternalPop 只在 cursor 仍指向发起方时才左移（防旧意图改新位置）', () => {
    const s = freshStore()
    s.push({ fullPath: '/a', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/b', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const bId = s.current()!.id
    // 期间已经前进到别处
    s.push({ fullPath: '/c', presentation: 'page', openedBy: 'push', scope: SCOPE })
    expect(s.applyExternalPop(bId)).toBe(false)
    expect(s.current()?.fullPath).toBe('/c')
  })

  it('脏表单消费返回：页面不后退、路由端口不被调', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/keys', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/keys/1', presentation: 'page', openedBy: 'push', scope: SCOPE })
    overlays.register({
      id: 'edit',
      presentation: 'modal',
      dismissible: true,
      dirty: true,
      close: vi.fn(),
      closeGuard: () => false,
    })
    const p = port()
    await expect(d.request('gesture', { store: s, port: p })).resolves.toBe('consumed-by-guard')
    expect(p.pop).not.toHaveBeenCalled()
    expect(s.current()?.fullPath).toBe('/keys/1') // 原地不动
  })

  it('专注层上的确认框先关确认框，不直接退出专注', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/logs', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const focusClose = vi.fn()
    const confirmClose = vi.fn()
    overlays.register({ id: 'focus', presentation: 'focus', dismissible: true, dirty: false, close: focusClose, priority: 10 })
    overlays.register({ id: 'confirm', presentation: 'modal', dismissible: true, dirty: false, close: confirmClose, priority: 20 })

    await d.request('button', { store: s, port: port() })
    expect(confirmClose).toHaveBeenCalledTimes(1)
    expect(focusClose).not.toHaveBeenCalled()
  })

  it('根页无层：浏览器留在原地，不杀进程', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const p = port({ isAtRoot: () => true, deliverToSystem: () => false })
    await expect(d.request('system', { store: s, port: p })).resolves.toBe('delivered-to-system')
    expect(p.pop).not.toHaveBeenCalled()
    expect(p.replace).not.toHaveBeenCalled()
  })

  it('原生根页把意图交还系统', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const deliver = vi.fn().mockReturnValue(true)
    await expect(
      d.request('system', { store: s, port: port({ isAtRoot: () => true, deliverToSystem: deliver }) }),
    ).resolves.toBe('delivered-to-system')
    expect(deliver).toHaveBeenCalledTimes(1)
  })

  it('无端口（未装配）时返回 unsupported，不冒充 success', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/a', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/b', presentation: 'page', openedBy: 'push', scope: SCOPE })
    // 有前驱但没有路由端口 → 如实报 unsupported，**不**把 no-op 冒充 success
    d.setPort(null)
    await expect(d.request('button', { store: s })).resolves.toBe('unsupported')
    // 未装配时连覆盖层仲裁仍可用（那一半不依赖路由）
    d._reset()
    const s2 = freshStore()
    const close = vi.fn()
    overlays.register({ id: 'ov', presentation: 'modal', dismissible: true, dirty: false, close })
    await expect(d.request('button', { store: s2 })).resolves.toBe('dismissed-overlay')
    expect(close).toHaveBeenCalledTimes(1)
  })
})

describe('BackDispatcher：单飞', () => {
  it('进行中的返回不接受第二次（不双 pop）', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/a', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/b', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/c', presentation: 'page', openedBy: 'push', scope: SCOPE })

    let release!: () => void
    const gate = new Promise<void>((res) => {
      release = res
    })
    const p = port({ pop: vi.fn(async () => { await gate; return true }) })

    const first = d.request('button', { store: s, port: p })
    // 立刻再按两次
    await expect(d.request('button', { store: s, port: p })).resolves.toBe('in-flight')
    await expect(d.request('button', { store: s, port: p })).resolves.toBe('in-flight')
    release()
    await expect(first).resolves.toBe('navigated')
    expect(p.pop).toHaveBeenCalledTimes(1)
  })

  it('返回处理完异常后仍解除单飞，不会永久卡死', async () => {
    const d = freshDispatcher()
    const s = freshStore()
    s.push({ fullPath: '/a', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/b', presentation: 'page', openedBy: 'push', scope: SCOPE })
    s.push({ fullPath: '/c', presentation: 'page', openedBy: 'push', scope: SCOPE })
    const boom = port({
      pop: vi.fn().mockRejectedValue(new Error('router exploded')),
    })
    // pop 抛错 → 外层 finally 解锁
    await expect(d.request('button', { store: s, port: boom })).rejects.toThrow('router exploded')
    expect(d.isBusy).toBe(false)
    await expect(d.request('button', { store: s, port: port() })).resolves.toBe('navigated')
  })
})
