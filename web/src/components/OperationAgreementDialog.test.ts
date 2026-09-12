import { mount } from '@vue/test-utils'
import { describe, expect, it, beforeEach } from 'vitest'
import { createI18n } from 'vue-i18n'
import OperationAgreementDialog from './OperationAgreementDialog.vue'

// 构造一个不依赖完整 element-plus 的测试环境：stub 掉 el-checkbox，
// 壳为真实 AppModal（2026-09-13 迁移，Teleport 打桩），验证组件自身的
// 逻辑（占位符替换、emit、localStorage 写入、按钮 disabled、门控关闭）。

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: {
    en: {
      common: { button: { close: 'Close' } },
      public: {
        agreement: {
          title: 'User Agreement',
          intro: 'Welcome.',
          points: ['Rights', 'Data', 'Open source', 'Disclaimer'],
          disclaimer: 'Full terms.',
          checkbox: 'I agree',
          fullLink: 'User Agreement',
          accept: 'Accept and continue',
        },
        opAgreement: {
          downloadTitle: 'Please read {title} before downloading',
          activateTitle: 'Please read {title} before activating',
        },
      },
    },
  },
})

const localStorageMock = (() => {
  let store: Record<string, string> = {}
  return {
    getItem: (k: string) => store[k] ?? null,
    setItem: (k: string, v: string) => { store[k] = v },
    clear: () => { store = {} },
  }
})()
;(globalThis as any).localStorage = localStorageMock

const ElCheckboxStub = {
  template: '<input type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', $event.target.checked)" />',
  props: { modelValue: Boolean },
  emits: ['update:modelValue'],
}

function makeWrapper(props: any) {
  return mount(OperationAgreementDialog, {
    props,
    global: {
      plugins: [i18n],
      stubs: { Teleport: true, ElCheckbox: ElCheckboxStub },
    },
    attachTo: document.body,
  })
}

describe('OperationAgreementDialog', () => {
  beforeEach(() => {
    localStorageMock.clear()
    document.body.innerHTML = ''
  })

  it('download: title replaces {title} placeholder with agreement.title', () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    const html = w.html()
    expect(html).toContain('Please read User Agreement before downloading')
    expect(html).not.toContain('{title}')
    w.unmount()
  })

  it('activate: title replaces {title} placeholder', () => {
    const w = makeWrapper({ modelValue: true, scope: 'activate', version: '2026-07-15' })
    expect(w.html()).toContain('Please read User Agreement before activating')
    w.unmount()
  })

  it('Accept button is disabled until checkbox is checked', () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    const acceptBtn = w.find('button.btn-primary')
    expect(acceptBtn.attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('emits agreed + writes localStorage when user accepts', async () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    await w.find('input[type="checkbox"]').setValue(true)
    await w.find('button.btn-primary').trigger('click')
    expect(w.emitted('agreed')).toBeTruthy()
    expect(localStorageMock.getItem('llmgw_op_agreement_download_2026-07-15')).toBeTruthy()
    w.unmount()
  })

  it('cancel button emits update:modelValue=false + cancelled (closed without agreement)', async () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    await w.find('button.btn-ghost').trigger('click')
    const last = w.emitted('update:modelValue')!.at(-1)!
    expect(last[0]).toBe(false)
    expect(w.emitted('cancelled')).toBeTruthy()
    w.unmount()
  })

  it('gating: ESC and mask click do NOT close (closeOnMask/escClose disabled)', async () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await w.find('.app-modal').trigger('click')
    expect(w.emitted('update:modelValue')).toBeUndefined()
    expect(w.emitted('cancelled')).toBeUndefined()
    w.unmount()
  })

  it('reopen resets the checkbox (原 el-dialog @open 复位语义)', async () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    await w.find('input[type="checkbox"]').setValue(true)
    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    expect((w.find('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)
    w.unmount()
  })
})
