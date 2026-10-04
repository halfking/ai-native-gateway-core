import { beforeEach, describe, expect, it, vi } from 'vitest'
import { BackDispatcher } from './backDispatcher'

describe('backDispatcher（06 §5 单一仲裁）', () => {
  let bd: BackDispatcher
  beforeEach(() => {
    bd = new BackDispatcher()
  })

  it('覆盖层打开时返回只关层；拒绝即消费', () => {
    const beforeClose = vi.fn(() => true)
    const onClose = vi.fn()
    bd.register({ id: 'a', beforeClose, onClose })
    const out = bd.dispatchBack(() => true, () => {})
    expect(out).toEqual({ kind: 'overlay-consumed', id: 'a' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('beforeClose 拒绝：返回被消费、层不关（脏表单守卫）', () => {
    bd.register({ id: 'a', beforeClose: () => false, onClose: () => {} })
    const out = bd.dispatchBack(() => true, () => {})
    expect(out).toEqual({ kind: 'overlay-rejected', id: 'a' })
  })

  it('无层时 pop 前驱；无前驱 fallback（根页停留，不跳外域）', () => {
    const goBack = vi.fn()
    expect(bd.dispatchBack(() => true, goBack)).toEqual({ kind: 'pop' })
    expect(goBack).toHaveBeenCalledTimes(1)
    expect(bd.dispatchBack(() => false, goBack)).toEqual({ kind: 'fallback' })
  })

  it('多层级：最上层先仲裁', () => {
    const closeA = vi.fn()
    const closeB = vi.fn()
    bd.register({ id: 'a', beforeClose: () => true, onClose: closeA })
    bd.register({ id: 'b', beforeClose: () => true, onClose: closeB })
    const out = bd.dispatchBack(() => true, () => {})
    expect(out).toEqual({ kind: 'overlay-consumed', id: 'b' })
    expect(closeB).toHaveBeenCalled()
    expect(closeA).not.toHaveBeenCalled()
  })
})
