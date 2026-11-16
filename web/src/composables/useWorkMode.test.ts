// useWorkMode.test.ts —— 工作模式状态层的钉测（2026-11-17）
//
// 钉住四件事：
//  1. ★ 白名单降级：平台设置项是 TypeString + Options（服务端**不**校验取值域），
//     所以读侧必须自己降级 —— 不是 'simple' 一律按 'expert'。
//  2. 默认是 expert：升级后既有用户的界面必须一行不变。
//  3. 切换写本机选择（localStorage），**不改**平台默认（不替别人做决定）。
//  4. 放弃本机选择 ⇒ 回到平台默认。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const getSettingMock = vi.fn()
vi.mock('../api/settings', () => ({
  getSetting: (...args: unknown[]) => getSettingMock(...args),
}))

import {
  _resetWorkModeForTests,
  coerceWorkMode,
  useWorkMode,
  WORK_MODE_SETTING_KEY,
  WORK_MODE_STORAGE_KEY,
} from './useWorkMode'

describe('工作模式状态层', () => {
  beforeEach(() => {
    // 模块级单例会跨用例存活 ⇒ 每个用例前复位（否则上个用例的选择会泄漏）。
    _resetWorkModeForTests()
    localStorage.clear()
    getSettingMock.mockReset()
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('★ 白名单降级：只有 simple 是简易模式，其余一律 expert', () => {
    expect(coerceWorkMode('simple')).toBe('simple')
    expect(coerceWorkMode('expert')).toBe('expert')
    expect(coerceWorkMode('Simple')).toBe('expert')
    expect(coerceWorkMode('SIMPLE')).toBe('expert')
    expect(coerceWorkMode('')).toBe('expert')
    expect(coerceWorkMode(null)).toBe('expert')
    expect(coerceWorkMode(undefined)).toBe('expert')
    expect(coerceWorkMode(1)).toBe('expert')
    expect(coerceWorkMode({})).toBe('expert')
  })

  it('默认是专家模式（升级不改变既有用户界面）', () => {
    const { mode, isSimple, isExpert } = useWorkMode()
    expect(mode.value).toBe('expert')
    expect(isSimple.value).toBe(false)
    expect(isExpert.value).toBe(true)
  })

  it('切换写本机选择，并立即生效', () => {
    const { mode, setMode, source } = useWorkMode()
    setMode('simple')
    expect(mode.value).toBe('simple')
    expect(localStorage.getItem(WORK_MODE_STORAGE_KEY)).toBe('simple')
    expect(source.value).toBe('local')
  })

  it('★ 切模式不改平台默认：平台设置项一个字节都不写', () => {
    getSettingMock.mockResolvedValue({ spec: {}, value: 'expert', source: 'default' })
    const { setMode } = useWorkMode()
    setMode('simple')
    // 只允许一次 GET（读平台默认），不允许任何写平台的调用。
    expect(getSettingMock).not.toHaveBeenCalled()
  })

  it('平台默认在「没有本机选择」时生效', async () => {
    getSettingMock.mockResolvedValue({ spec: {}, value: 'simple', source: 'db' })
    const { refreshPlatformDefault, mode, platformDefault } = useWorkMode()
    await refreshPlatformDefault()
    expect(getSettingMock).toHaveBeenCalledWith(WORK_MODE_SETTING_KEY)
    expect(platformDefault.value).toBe('simple')
    expect(mode.value).toBe('simple')
  })

  it('平台默认是脏值时降级为 expert，不让界面炸掉', async () => {
    getSettingMock.mockResolvedValue({ spec: {}, value: 'weird-value', source: 'db' })
    const { refreshPlatformDefault, mode, platformDefault } = useWorkMode()
    await refreshPlatformDefault()
    expect(platformDefault.value).toBe('expert')
    expect(mode.value).toBe('expert')
  })

  it('本机选择优先于平台默认', async () => {
    localStorage.setItem(WORK_MODE_STORAGE_KEY, 'simple')
    getSettingMock.mockResolvedValue({ spec: {}, value: 'expert', source: 'db' })
    const { refreshPlatformDefault, mode } = useWorkMode()
    await refreshPlatformDefault()
    expect(mode.value).toBe('simple')
  })

  it('放弃本机选择 ⇒ 回到平台默认', async () => {
    getSettingMock.mockResolvedValue({ spec: {}, value: 'simple', source: 'db' })
    const { setMode, refreshPlatformDefault, resetToPlatformDefault, mode } = useWorkMode()
    setMode('expert')
    await refreshPlatformDefault()
    resetToPlatformDefault()
    expect(localStorage.getItem(WORK_MODE_STORAGE_KEY)).toBe(null)
    expect(mode.value).toBe('simple')
  })

  it('平台读取失败不阻塞界面（退回本机选择 / 兜底 expert）', async () => {
    getSettingMock.mockRejectedValue(new Error('boom'))
    const { refreshPlatformDefault, mode, platformDefault } = useWorkMode()
    await refreshPlatformDefault()
    expect(platformDefault.value).toBe(null)
    expect(mode.value).toBe('expert')
  })
})
