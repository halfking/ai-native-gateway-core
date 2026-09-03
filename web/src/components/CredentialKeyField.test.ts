import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import CredentialKeyField from './CredentialKeyField.vue'

const { revealCredentialKey } = vi.hoisted(() => ({ revealCredentialKey: vi.fn() }))
vi.mock('../api/providers', () => ({ revealCredentialKey }))

describe('CredentialKeyField', () => {
  it('does not expose reveal when permission is absent', async () => {
    const wrapper = mount(CredentialKeyField, {
      props: { providerId: 1, credentialId: 2, masked: 'sk-***', canReveal: false },
    })
    expect(wrapper.text()).toContain('sk-***')
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('uses the injected reveal callback and supports copy/hide', async () => {
    const reveal = vi.fn().mockResolvedValue('sk-secret')
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    const wrapper = mount(CredentialKeyField, {
      props: {
        providerId: 1,
        credentialId: 2,
        masked: 'sk-***',
        canReveal: true,
        revealKey: reveal,
      },
    })

    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(reveal).toHaveBeenCalledWith(2, 1)
    expect(wrapper.text()).toContain('sk-secret')
    expect(wrapper.emitted('revealed')?.[0]).toEqual(['sk-secret'])

    await wrapper.findAll('button')[0].trigger('click')
    expect(writeText).toHaveBeenCalledWith('sk-secret')
    await wrapper.findAll('button')[1].trigger('click')
    expect(wrapper.text()).toContain('sk-***')
    expect(wrapper.emitted('hidden')).toBeTruthy()
  })

  it('clears a revealed key when the credential changes', async () => {
    const wrapper = mount(CredentialKeyField, {
      props: {
        providerId: 1,
        credentialId: 2,
        masked: 'sk-a***',
        canReveal: true,
        revealKey: vi.fn().mockResolvedValue('sk-secret-a'),
      },
    })

    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('sk-secret-a')

    await wrapper.setProps({ credentialId: 3, masked: 'sk-b***' })
    expect(wrapper.text()).not.toContain('sk-secret-a')
    expect(wrapper.text()).toContain('sk-b***')
  })

  it('renders callback errors', async () => {
    const wrapper = mount(CredentialKeyField, {
      props: { credentialId: 2, canReveal: true, revealKey: vi.fn().mockRejectedValue(new Error('denied')) },
    })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('denied')
  })
})
