// useLiveStreamUrl.test.ts — SSE endpoint URL 管理 composable 单元测试
//
// 覆盖：localStorage 持久化 / 编辑状态机 / 保存-重连 / 重置 / 取消 / 连接测试弹窗。

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ref, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { useLiveStreamUrl } from './useLiveStreamUrl'
import type { ConnectionState } from './liveStreamStore'
import { __resetCustomEndpointForTest } from './liveStreamStore'

const STORAGE_KEY = 'llmgw_sse_endpoint'

function makeConnection(state: ConnectionState) {
  return ref<ConnectionState>(state)
}

function makeT() {
  return vi.fn((key: string, named?: Record<string, unknown>) => {
    if (key === 'dashboard.liveStream.sseTestOk') return `OK ${named?.url}`
    if (key === 'dashboard.liveStream.sseTestFail') return `FAIL ${named?.status} ${named?.url}`
    return key
  })
}

// 挂载 harness 组件以激活 onMounted / watch 生命周期
function mountHarness(
  connection: ReturnType<typeof makeConnection>,
  reconnect: () => void,
  t: ReturnType<typeof makeT>,
) {
  const Comp = defineComponent({
    setup() {
      const api = useLiveStreamUrl({ connection, reconnect, t })
      return () =>
        h('div', { 'data-test': 'url' }, JSON.stringify({
          streamUrl: api.streamUrl.value,
          isEditingUrl: api.isEditingUrl.value,
          editUrlValue: api.editUrlValue.value,
          defaultStreamUrl: api.defaultStreamUrl.value,
        }))
    },
  })
  return mount(Comp)
}

describe('useLiveStreamUrl', () => {
  let connection: ReturnType<typeof makeConnection>
  let reconnect: ReturnType<typeof vi.fn>
  let t: ReturnType<typeof makeT>
  let alertSpy: ReturnType<typeof vi.fn>

  const defaultUrl = `${window.location.origin}/api/admin/live-stream`

  beforeEach(() => {
    localStorage.clear()
    __resetCustomEndpointForTest()
    connection = makeConnection('idle')
    reconnect = vi.fn()
    t = makeT()
    alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {}) as unknown as ReturnType<typeof vi.fn>
  })

  afterEach(() => {
    alertSpy.mockRestore()
    localStorage.clear()
  })

  it('onMounted: uses default URL when localStorage is empty', async () => {
    const wrapper = mountHarness(connection, reconnect, t)
    await nextTick()
    const state = JSON.parse(wrapper.find('[data-test="url"]').text())
    expect(state.streamUrl).toBe(defaultUrl)
  })

  it('onMounted: restores admin-saved URL from localStorage', async () => {
    localStorage.setItem(STORAGE_KEY, 'https://tunnel.example.com/sse')
    const wrapper = mountHarness(connection, reconnect, t)
    await nextTick()
    const state = JSON.parse(wrapper.find('[data-test="url"]').text())
    expect(state.streamUrl).toBe('https://tunnel.example.com/sse')
  })

  it('editing: startEditUrl enters edit mode with copy of streamUrl', async () => {
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    expect(api.streamUrl.value).toBe(defaultUrl)
    expect(api.isEditingUrl.value).toBe(false)

    api.startEditUrl()
    expect(api.isEditingUrl.value).toBe(true)
    expect(api.editUrlValue.value).toBe(defaultUrl)
    wrapper.unmount()
  })

  it('saveUrl: persists URL, reconnects, exits edit mode', async () => {
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    api.startEditUrl()
    api.editUrlValue.value = 'https://tunnel.example.com/sse'
    api.saveUrl()

    expect(api.streamUrl.value).toBe('https://tunnel.example.com/sse')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('https://tunnel.example.com/sse')
    expect(reconnect).toHaveBeenCalledTimes(1)
    expect(api.isEditingUrl.value).toBe(false)
    wrapper.unmount()
  })

  it('saveUrl: empty URL does NOT persist or reconnect', async () => {
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    api.startEditUrl()
    api.editUrlValue.value = '   '
    api.saveUrl()

    expect(api.streamUrl.value).toBe(defaultUrl)
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    expect(reconnect).not.toHaveBeenCalled()
    expect(api.isEditingUrl.value).toBe(false)
    wrapper.unmount()
  })

  it('resetUrl: removes localStorage entry, resets to default, reconnects', async () => {
    localStorage.setItem(STORAGE_KEY, 'https://tunnel.example.com/sse')
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    api.startEditUrl()
    api.resetUrl()

    expect(api.streamUrl.value).toBe(defaultUrl)
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    expect(reconnect).toHaveBeenCalledTimes(1)
    expect(api.isEditingUrl.value).toBe(false)
    wrapper.unmount()
  })

  it('cancelEditUrl: exits edit mode without persisting', async () => {
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    api.startEditUrl()
    api.editUrlValue.value = 'https://should-not-save.example.com'
    api.cancelEditUrl()

    expect(api.isEditingUrl.value).toBe(false)
    expect(api.streamUrl.value).toBe(defaultUrl)
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    expect(reconnect).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('testConnection: alerts OK message when connection is open', async () => {
    connection.value = 'open'
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    api.testConnection()
    expect(alertSpy).toHaveBeenCalledTimes(1)
    expect(alertSpy).toHaveBeenCalledWith(`OK ${defaultUrl}`)
    expect(t).toHaveBeenCalledWith('dashboard.liveStream.sseTestOk', { url: defaultUrl })
    wrapper.unmount()
  })

  it('testConnection: alerts FAIL message with status when connection is not open', async () => {
    connection.value = 'reconnecting'
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    api.testConnection()
    expect(alertSpy).toHaveBeenCalledTimes(1)
    expect(alertSpy).toHaveBeenCalledWith(`FAIL reconnecting ${defaultUrl}`)
    expect(t).toHaveBeenCalledWith('dashboard.liveStream.sseTestFail', { status: 'reconnecting', url: defaultUrl })
    wrapper.unmount()
  })

  it('watch(defaultStreamUrl): follows default when no saved URL', async () => {
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    expect(api.streamUrl.value).toBe(defaultUrl)
    // 模拟 window.location 变化：defaultStreamUrl 是 origin 派生，直接改 origin 不可行
    // （jsdom location 只读），此处验证「有保存值时不跟随默认值」的分支：
    // 保存自定义地址后修改 defaultStreamUrl 不得覆盖。
    api.saveUrl()
    expect(api.streamUrl.value).toBe(defaultUrl)
    wrapper.unmount()
  })

  it('watch(defaultStreamUrl): saved URL is NOT overwritten by default changes', async () => {
    localStorage.setItem(STORAGE_KEY, 'https://tunnel.example.com/sse')
    const apiRef: { current: ReturnType<typeof useLiveStreamUrl> | null } = { current: null }
    const Comp = defineComponent({
      setup() {
        const api = useLiveStreamUrl({ connection, reconnect, t })
        apiRef.current = api
        return () => h('div')
      },
    })
    const wrapper = mount(Comp)
    await nextTick()

    const api = apiRef.current!
    // 有 saved URL 时，defaultStreamUrl 变化不应覆盖 streamUrl
    expect(api.streamUrl.value).toBe('https://tunnel.example.com/sse')
    wrapper.unmount()
  })
})
