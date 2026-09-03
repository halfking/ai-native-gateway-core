// useConfirmDialog.test.ts — 统一确认交互 composable 测试。
// 覆盖：确认路径 resolve true；取消/关闭路径 resolve false；默认文案走 i18n。

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { confirmDialog } from './useConfirmDialog'

const confirmSpy = vi.fn()

vi.mock('element-plus', () => ({
  ElMessageBox: {
    confirm: (...args: unknown[]) => confirmSpy(...args),
  },
}))

describe('useConfirmDialog.confirmDialog', () => {
  beforeEach(() => {
    confirmSpy.mockReset()
  })

  it('resolves true when the user confirms', async () => {
    confirmSpy.mockResolvedValue(undefined)
    await expect(confirmDialog('删除该规则？')).resolves.toBe(true)
    expect(confirmSpy).toHaveBeenCalledOnce()
  })

  it('resolves false when the user cancels (reject is normalized)', async () => {
    confirmSpy.mockRejectedValue('cancel')
    await expect(confirmDialog('删除该规则？')).resolves.toBe(false)
  })

  it('passes message as first argument with i18n default title/buttons', async () => {
    confirmSpy.mockResolvedValue(undefined)
    await confirmDialog('msg')
    const [message, title, options] = confirmSpy.mock.calls[0] as [string, string, Record<string, unknown>]
    expect(message).toBe('msg')
    // 默认标题/按钮来自 i18n（测试环境 locale=zh-CN）
    expect(typeof title).toBe('string')
    expect(title.length).toBeGreaterThan(0)
    expect(options.type).toBe('warning')
  })

  it('forwards overrides (title / buttons / type)', async () => {
    confirmSpy.mockResolvedValue(undefined)
    await confirmDialog('msg', {
      title: '危险操作',
      confirmButtonText: '执行',
      cancelButtonText: '返回',
      type: 'error',
    })
    const [message, title, options] = confirmSpy.mock.calls[0] as [string, string, Record<string, unknown>]
    expect(message).toBe('msg')
    expect(title).toBe('危险操作')
    expect(options.confirmButtonText).toBe('执行')
    expect(options.cancelButtonText).toBe('返回')
    expect(options.type).toBe('error')
  })
})
