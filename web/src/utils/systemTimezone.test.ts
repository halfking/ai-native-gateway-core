// systemTimezone.test.ts — 系统时区全局状态测试（2026-11-20 时区设置轮）。
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  browserTimezone,
  fetchSystemTimezone,
  onSystemTimezoneChange,
  resetSystemTimezoneForTest,
  setSystemTimezoneForTest,
  systemTimezone,
  systemTimezoneState,
} from './systemTimezone'

afterEach(() => {
  resetSystemTimezoneForTest()
  vi.restoreAllMocks()
})

describe('systemTimezone', () => {
  it('未加载时回退浏览器本地时区', () => {
    // 模块初始状态 timezone=''；systemTimezone() 应回退而非空串。
    expect(systemTimezone()).not.toBe('')
    expect(systemTimezone()).toBe(browserTimezone())
  })

  it('setSystemTimezoneForTest 注入后生效并触发订阅', () => {
    let notified = 0
    const unsub = onSystemTimezoneChange(() => notified++)
    setSystemTimezoneForTest('Asia/Shanghai')
    expect(systemTimezone()).toBe('Asia/Shanghai')
    expect(notified).toBe(1)
    unsub()
    setSystemTimezoneForTest('UTC')
    expect(notified).toBe(1) // 退订后不再通知
  })

  it('fetchSystemTimezone 解析后端响应并写入状态', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true,
      json: async () => ({ timezone: 'Asia/Shanghai', default: 'Asia/Shanghai' }),
    })))
    await fetchSystemTimezone()
    expect(systemTimezone()).toBe('Asia/Shanghai')
    expect(systemTimezoneState().loaded).toBe(true)
    expect(systemTimezoneState().isDefault).toBe(true)
  })

  it('fetchSystemTimezone 失败静默保留回退值', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 500 })))
    await fetchSystemTimezone()
    expect(systemTimezoneState().loaded).toBe(false)
  })

  it('fetchSystemTimezone 非默认时区 isDefault=false', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true,
      json: async () => ({ timezone: 'America/New_York', default: 'Asia/Shanghai' }),
    })))
    await fetchSystemTimezone()
    expect(systemTimezoneState().isDefault).toBe(false)
  })
})
