import { afterEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import NotFoundView from './NotFoundView.vue'
import { locale, setLocale } from '@/i18n'

const originLocale = locale.value

describe('NotFoundView title ownership', () => {
  afterEach(() => setLocale(originLocale))

  it('leaves the route title to the app topbar instead of rendering a duplicate page heading', () => {
    setLocale('zh-CN')
    const wrapper = mount(NotFoundView)

    expect(wrapper.find('h1').exists()).toBe(false)
    expect(wrapper.text()).toContain('返回总览')
    wrapper.unmount()
  })
})
