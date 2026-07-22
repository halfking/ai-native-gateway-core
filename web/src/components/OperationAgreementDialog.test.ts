import { mount } from '@vue/test-utils'
import { describe, expect, it, beforeEach } from 'vitest'
import { createI18n } from 'vue-i18n'
import OperationAgreementDialog from './OperationAgreementDialog.vue'

// 构造一个不依赖完整 element-plus 的测试环境：stub 掉三个 el-* 组件，
// 只验证组件自身的逻辑（占位符替换、emit、localStorage 写入、按钮 disabled）。

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: {
    en: {
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

// ElDialog stub：暴露 modelValue + title，并在 v-model 写为 false 时触发 update:modelValue
const ElDialogStub = {
  compatConfig: { MODE: 3 as const },
  template: `<div class="dlg"><div class="dlg-title">{{ title }}</div><slot /><slot name="footer" /></div>`,
  props: { modelValue: Boolean, title: { type: String, default: '' } },
  emits: ['update:modelValue', 'open'],
}

const ElCheckboxStub = {
  template: '<input type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', $event.target.checked)" />',
  props: { modelValue: Boolean },
  emits: ['update:modelValue'],
}

const ElButtonStub = {
  template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
  props: { disabled: Boolean, type: String },
  emits: ['click'],
}

function makeWrapper(props: any) {
  return mount(OperationAgreementDialog, {
    props,
    global: {
      plugins: [i18n],
      stubs: { ElDialog: ElDialogStub, ElCheckbox: ElCheckboxStub, ElButton: ElButtonStub },
    },
  })
}

describe('OperationAgreementDialog', () => {
  beforeEach(() => localStorageMock.clear())

  it('download: title replaces {title} placeholder with agreement.title', () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    const html = w.html()
    expect(html).toContain('Please read User Agreement before downloading')
    expect(html).not.toContain('{title}')
  })

  it('activate: title replaces {title} placeholder', () => {
    const w = makeWrapper({ modelValue: true, scope: 'activate', version: '2026-07-15' })
    expect(w.html()).toContain('Please read User Agreement before activating')
  })

  it('Accept button is disabled until checkbox is checked', () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    const acceptBtn = w.find('button.btn-primary')
    expect(acceptBtn.attributes('disabled')).toBeDefined()
  })

  it('emits agreed + writes localStorage when user accepts', async () => {
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    await w.find('input[type="checkbox"]').setValue(true)
    await w.find('button.btn-primary').trigger('click')
    expect(w.emitted('agreed')).toBeTruthy()
    expect(localStorageMock.getItem('llmgw_op_agreement_download_2026-07-15')).toBeTruthy()
  })

  it('emits update:modelValue=false (cancellation path) when dialog closed without agreement', async () => {
    // 模拟 el-dialog 内部关闭：直接 emit update:modelValue=false 给 wrapper
    const w = makeWrapper({ modelValue: true, scope: 'download', version: '2026-07-15' })
    // 触发 dialogVisible setter (computed setter)
    await (w.getComponent(ElDialogStub as any).vm as any).$emit('update:modelValue', false)
    expect(w.emitted('update:modelValue')).toBeTruthy()
    const last = w.emitted('update:modelValue')!.at(-1)!
    expect(last[0]).toBe(false)
    expect(w.emitted('cancelled')).toBeTruthy()
  })
})
