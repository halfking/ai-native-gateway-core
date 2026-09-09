import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { useActionMessage } from './useActionMessage'

describe('useActionMessage', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('notifySuccess 展示成功并在默认 3s 后自动清除', () => {
    const { message, error, notifySuccess } = useActionMessage()
    notifySuccess('已保存')
    expect(message.value).toBe('已保存')
    expect(error.value).toBe('')
    vi.advanceTimersByTime(2999)
    expect(message.value).toBe('已保存')
    vi.advanceTimersByTime(1)
    expect(message.value).toBe('')
  })

  it('notifyError 默认驻留', () => {
    const { error, notifyError } = useActionMessage()
    notifyError('失败')
    expect(error.value).toBe('失败')
    vi.advanceTimersByTime(60_000)
    expect(error.value).toBe('失败')
  })

  it('notifyError 支持 errorTimeoutMs 自动清除', () => {
    const { error, notifyError } = useActionMessage({ errorTimeoutMs: 6000 })
    notifyError('失败')
    vi.advanceTimersByTime(6000)
    expect(error.value).toBe('')
  })

  it('成功与失败互斥', () => {
    const { message, error, notifySuccess, notifyError } = useActionMessage()
    notifySuccess('ok')
    notifyError('bad')
    expect(message.value).toBe('')
    expect(error.value).toBe('bad')
    notifySuccess('ok2')
    expect(error.value).toBe('')
    expect(message.value).toBe('ok2')
  })

  it('重复 notifySuccess 重置计时器（不提前消失）', () => {
    const { message, notifySuccess } = useActionMessage()
    notifySuccess('第一次')
    vi.advanceTimersByTime(2000)
    notifySuccess('第二次')
    vi.advanceTimersByTime(2999)
    expect(message.value).toBe('第二次')
    vi.advanceTimersByTime(1)
    expect(message.value).toBe('')
  })

  it('clear 立即清空两侧', () => {
    const { message, error, notifySuccess, notifyError, clear } = useActionMessage({ errorTimeoutMs: 5000 })
    notifySuccess('ok')
    notifyError('bad')
    clear()
    expect(message.value).toBe('')
    expect(error.value).toBe('')
  })

  it('successTimeoutMs=0 驻留', () => {
    const { message, notifySuccess } = useActionMessage({ successTimeoutMs: 0 })
    notifySuccess('驻留')
    vi.advanceTimersByTime(60_000)
    expect(message.value).toBe('驻留')
  })
})
