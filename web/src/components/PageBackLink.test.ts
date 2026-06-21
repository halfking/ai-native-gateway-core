import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import PageBackLink from './PageBackLink.vue'

// PageBackLink.test.ts — v6.0 audit T11 (2026-06-22)
// Verifies jsdom + @vue/test-utils can mount a real .vue file.
// PageBackLink is a 48-line button that calls router.back() or
// router.push(props.to). We don't stub vue-router because we only
// assert the rendered DOM here, not the click behavior. The router
// injection warning in stderr is harmless for these tests.
//
// Note: this component uses <button> (not <a>) and props are `to`/`label`
// (not `href`). Template is `← {{ displayLabel }}` where displayLabel
// is `props.label || '返回'`. There is NO slot — content comes from
// the label prop only.
describe('PageBackLink', () => {
  it('renders a button.page-back with default label "返回"', () => {
    const wrapper = mount(PageBackLink)
    const btn = wrapper.find('button.page-back')
    expect(btn.exists()).toBe(true)
    expect(btn.text()).toContain('返回')
  })

  it('renders the custom label when label prop is set', () => {
    const wrapper = mount(PageBackLink, {
      props: { label: 'back to dashboard' },
    })
    expect(wrapper.find('button.page-back').text()).toContain('back to dashboard')
    expect(wrapper.find('button.page-back').text()).not.toContain('返回')
  })
})