import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ChangePasswordDialog from './ChangePasswordDialog.vue'

const changeMyPasswordMock = vi.fn()

vi.mock('../api', () => ({
  changeMyPassword: (...args: any[]) => changeMyPasswordMock(...args),
}))

describe('ChangePasswordDialog', () => {
  beforeEach(() => {
    changeMyPasswordMock.mockReset()
  })

  it('submits old and new password then emits success', async () => {
    changeMyPasswordMock.mockResolvedValue({ status: 'password_changed' })

    const wrapper = mount(ChangePasswordDialog, {
      props: { modelValue: true },
      global: {
        stubs: { Teleport: true },
      },
    })

    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('OldPass123')
    await inputs[1].setValue('NewPass123')
    await inputs[2].setValue('NewPass123')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(changeMyPasswordMock).toHaveBeenCalledWith('OldPass123', 'NewPass123')
    expect(wrapper.emitted('success')).toHaveLength(1)
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([false])
  })

  it('shows validation error when confirmation mismatches', async () => {
    const wrapper = mount(ChangePasswordDialog, {
      props: { modelValue: true },
      global: {
        stubs: { Teleport: true },
      },
    })

    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('OldPass123')
    await inputs[1].setValue('NewPass123')
    await inputs[2].setValue('NewPass999')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(changeMyPasswordMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('两次输入的新密码不一致')
  })

  it('renders live password policy feedback and disables submit until valid', async () => {
    const wrapper = mount(ChangePasswordDialog, {
      props: { modelValue: true },
      global: {
        stubs: { Teleport: true },
      },
    })

    expect((wrapper.find('button[type="submit"]').element as HTMLButtonElement).disabled).toBe(true)

    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('OldPass123')
    await inputs[1].setValue('lowercase123')
    await inputs[2].setValue('lowercase123')
    await flushPromises()

    expect(wrapper.text()).toContain('○ 包含大写字母')
    expect((wrapper.find('button[type="submit"]').element as HTMLButtonElement).disabled).toBe(true)

    await inputs[1].setValue('ValidPass123')
    await inputs[2].setValue('ValidPass123')
    await flushPromises()

    expect(wrapper.text()).toContain('✓ 包含大写字母')
    expect(wrapper.text()).toContain('✓ 两次输入一致')
    expect((wrapper.find('button[type="submit"]').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('hides cancel controls in forced mode', async () => {
    const wrapper = mount(ChangePasswordDialog, {
      props: { modelValue: true, forced: true },
      global: {
        stubs: { Teleport: true },
      },
    })

    expect(wrapper.text()).toContain('首次登录需要先修改密码后才能继续使用')
    expect(wrapper.text()).not.toContain('取消')
    expect(wrapper.find('.password-modal__close').exists()).toBe(false)
  })
})
