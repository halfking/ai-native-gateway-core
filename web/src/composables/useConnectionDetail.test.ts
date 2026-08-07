// useConnectionDetail.test.ts — 连接详情弹窗状态 composable 单元测试
//
// 覆盖：初始关闭 / 管理员点击展开 / 管理员关闭并退出编辑态 / 非管理员点击无效果。
import { describe, it, expect } from 'vitest'
import { ref } from 'vue'
import { useConnectionDetail } from './useConnectionDetail'

describe('useConnectionDetail', () => {
  it('initial state: popup hidden', () => {
    const isAdmin = ref(true)
    const isEditingUrl = ref(false)
    const api = useConnectionDetail({ isAdmin, isEditingUrl })
    expect(api.showConnectionDetail.value).toBe(false)
  })

  it('toggleConnectionDetail opens the popup for admin', () => {
    const isAdmin = ref(true)
    const isEditingUrl = ref(false)
    const api = useConnectionDetail({ isAdmin, isEditingUrl })
    api.toggleConnectionDetail()
    expect(api.showConnectionDetail.value).toBe(true)
  })

  it('toggleConnectionDetail closes popup and exits edit mode for admin', () => {
    const isAdmin = ref(true)
    const isEditingUrl = ref(true)
    const api = useConnectionDetail({ isAdmin, isEditingUrl })
    api.toggleConnectionDetail()
    expect(api.showConnectionDetail.value).toBe(true)
    // 编辑态在弹窗内，关闭弹窗时退出编辑态
    api.toggleConnectionDetail()
    expect(api.showConnectionDetail.value).toBe(false)
    expect(isEditingUrl.value).toBe(false)
  })

  it('toggleConnectionDetail does nothing for non-admin', () => {
    const isAdmin = ref(false)
    const isEditingUrl = ref(false)
    const api = useConnectionDetail({ isAdmin, isEditingUrl })
    api.toggleConnectionDetail()
    expect(api.showConnectionDetail.value).toBe(false)
    expect(isEditingUrl.value).toBe(false)
  })
})
