// PlanEditor.test.ts — 三形态方案编辑器的 payload 形状钉测（2026-11-21）
//
// ★ 钉的是「编辑器产出的 payload 会被后端 NormalizePlan 原样接受」这一层契约：
//   · script ⇒ {kind:'script', steps:[{action, params, note?}]}（≤8 步）
//   · api    ⇒ {method, path(/api/admin/ 前缀), body?, timeout_ms?}
//   · prompt ⇒ {kind:'prompt', prompt, expect}
//   · JSON 不合法 ⇒ getPayload 返回 {ok:false, error}（不抛异常给父组件）
// ★ <script setup> 不暴露内部 ref ⇒ 本钉测走**黑盒**：DOM 驱动 + defineExpose
//   的 getPayload()。归一化与门控语义在后端 fail-closed；这里只锁前端拼装形状。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { nextTick } from 'vue'
import PlanEditor from './PlanEditor.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      errorPlaybook: {
        kind: { script: '脚本步骤', api: '管理面 API', prompt: 'AI 提示词' },
        form: {
          planKind: '方案形态',
          expect: '期望产物',
          expectAnalysis: '分析（只读）',
          expectProposal: '提案（可执行）',
          prompt: '提示词正文',
          promptHint: '交给 PI 智能体或运维执行的完整指令，最长 16KB',
          method: 'HTTP 方法',
          path: '管理面路径',
          pathHint: '必须以 /api/admin/ 开头；只有登记在册的闭集路由可执行',
          body: '请求体（可选 JSON）',
          timeout: '超时（毫秒，≤60000）',
          stepsHint: '最多 8 步；动作只能选白名单闭集原语',
          addStep: '添加步骤',
          removeStep: '移除',
          stepAction: '动作',
          stepParams: '参数（JSON）',
          stepNote: '备注（可选）',
          invalidJsonStep: '步骤 {n} 的参数不是合法 JSON',
          invalidJsonBody: '请求体不是合法 JSON',
          stepsRequired: '脚本方案至少需要一个步骤',
        },
      },
    },
  },
})

function mountEditor(props: { initialKind?: string; initialPlan?: unknown } = {}) {
  return mount(PlanEditor, {
    props: props as never,
    global: { plugins: [i18n] },
  })
}

function payloadOf(wrapper: ReturnType<typeof mount>): () => ReturnType<ReturnType<typeof getPayloadFn>> {
  return getPayloadFn(wrapper)
}

function getPayloadFn(wrapper: ReturnType<typeof mount>) {
  const exposed = (wrapper.vm as unknown as { getPayload: () => { ok: boolean; payload?: unknown; error?: string } })
  return () => exposed.getPayload()
}

async function selectKind(wrapper: ReturnType<typeof mount>, kind: string): Promise<void> {
  const radio = wrapper.findAll('input[name="ep-plan-kind"]').find((i) => (i.element as HTMLInputElement).value === kind)
  expect(radio, `kind 选项 ${kind} 应存在`).toBeTruthy()
  await radio!.setValue(true)
}

describe('PlanEditor getPayload 形状契约（黑盒）', () => {
  it('prompt 形态：默认产出 {kind:prompt, prompt, expect:analysis}', async () => {
    const w = mountEditor()
    await selectKind(w, 'prompt')
    const ta = w.find('textarea')
    expect(ta.exists()).toBe(true)
    await ta.setValue('分析根因')
    const res = getPayloadFn(w)()
    expect(res.ok).toBe(true)
    expect(res.payload).toEqual({
      plan_kind: 'prompt',
      plan: { kind: 'prompt', prompt: '分析根因', expect: 'analysis' },
    })
  })

  it('api 形态：method/path/body/timeout 组装成请求形状', async () => {
    const w = mountEditor()
    await selectKind(w, 'api')
    const inputs = w.findAll('input.ep-input')
    // api 面板：[timeout 数字输入, path 文本输入]
    await inputs[1].setValue('/api/admin/pi-analysis/trigger')
    const selects = w.findAll('select.ep-input')
    await selects[0].setValue('POST')
    await w.find('textarea').setValue('{"batch":10}')
    // timeout 数字输入
    await inputs[0].setValue('5000')
    const res = getPayloadFn(w)() as { ok: boolean; payload?: { plan: Record<string, unknown> } }
    expect(res.ok).toBe(true)
    expect(res.payload!.plan).toEqual({
      method: 'POST',
      path: '/api/admin/pi-analysis/trigger',
      body: { batch: 10 },
      timeout_ms: 5000,
    })
  })

  it('script 形态：steps 拼装 + note 空省略；坏 JSON 返回 ok:false 不抛异常', async () => {
    const w = mountEditor()
    await selectKind(w, 'script')
    const params = w.find('textarea.ep-input--mono')
    expect(params.exists()).toBe(true)
    await params.setValue('{"message":"观察一下"}')
    const note = w.find('td input.ep-input')
    await note.setValue('第一步只读')
    const ok = getPayloadFn(w)() as { ok: boolean; payload?: { plan: { steps: Array<Record<string, unknown>> } } }
    expect(ok.ok).toBe(true)
    expect(ok.payload!.plan.steps).toEqual([
      { action: 'record_note', params: { message: '观察一下' }, note: '第一步只读' },
    ])

    // 添加第二步并写坏 JSON ⇒ ok:false，错误文案落在该步（页面内联展示）
    const addBtn = w.findAll('button').find((b) => b.text().includes('添加步骤'))
    expect(addBtn).toBeTruthy()
    await addBtn!.trigger('click')
    const tas = w.findAll('textarea.ep-input--mono')
    expect(tas.length).toBe(2)
    await tas[1].setValue('{bad')
    const bad = getPayloadFn(w)() as { ok: boolean; error?: string }
    expect(bad.ok).toBe(false)
    expect(bad.error).toBeTruthy()
    await nextTick() // v-if 的内联错误是响应式渲染，DOM 落屏要等一个 tick
    expect(w.text()).toContain('不是合法 JSON')
  })

  it('编辑态回填：initialPlan 的 steps 反序列化进表单（textarea 可见原 JSON）', () => {
    const w = mountEditor({
      initialKind: 'script',
      initialPlan: {
        kind: 'script',
        steps: [{ action: 'set_settings_kv', params: { key: 'security.enabled', value: false }, note: '' }],
      },
    })
    const ta = w.find('textarea.ep-input--mono')
    expect(ta.exists()).toBe(true)
    expect(JSON.parse((ta.element as HTMLTextAreaElement).value)).toEqual({ key: 'security.enabled', value: false })
    expect((w.find('td select.ep-input').element as HTMLSelectElement).value).toBe('set_settings_kv')
  })
})
