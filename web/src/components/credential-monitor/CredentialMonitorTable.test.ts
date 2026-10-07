// CredentialMonitorTable.test.ts — 行渲染、勾选/打开详情事件透传，
// 以及响应式源码断言（§4.7-4 拆分后的主列表子组件）。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import CredentialMonitorTable, { type CredentialRow } from './CredentialMonitorTable.vue'

const source = readFileSync(resolve(process.cwd(), 'src/components/credential-monitor/CredentialMonitorTable.vue'), 'utf8')

// 表头与提示文案走 credentialMonitor.table 命名空间；这里只装配用得到的键，
// 与 CredentialDetailDrawer.test.ts 同一套做法（不引入真 locale 包，保持单测独立）。
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      credentialMonitor: {
        table: {
          cell: {
            modelsNotMeasured: '该凭据本次未返回模型计数（未测量），不是「0 个可用」',
          },
        },
      },
    },
  },
})

function row(id: number, overrides: Partial<CredentialRow> = {}): CredentialRow {
  return {
    id,
    label: `cred-${id}`,
    provider_name: 'provider-a',
    provider_id: 1,
    availability_state: 'ready',
    health_status: 'healthy',
    model_total: 10,
    model_available: 8,
    modelTotal: 10,
    modelAvailable: 8,
    aggregated_success_rate: 0.95,
    broken_model_count: 0,
    concurrency_limit: 5,
    effective_concurrency: 5,
    ...overrides,
  } as CredentialRow
}

function mountTable(rows: CredentialRow[], selectedIds: Set<number> = new Set()) {
  return mount(CredentialMonitorTable, {
    props: { rows, selectedIds },
    global: { plugins: [i18n] },
  })
}

describe('CredentialMonitorTable', () => {
  it('渲染行数据与勾选态', () => {
    const w = mountTable([row(1), row(2)], new Set([1]))
    expect(w.findAll('tbody tr')).toHaveLength(2)
    const checkboxes = w.findAll('tbody input[type="checkbox"]')
    expect((checkboxes[0].element as HTMLInputElement).checked).toBe(true)
    expect((checkboxes[1].element as HTMLInputElement).checked).toBe(false)
    expect(w.text()).toContain('cred-1')
    expect(w.text()).toContain('provider-a')
  })

  it('表头全选 checkbox 反映全选状态并透传 toggle-all', async () => {
    const w = mountTable([row(1), row(2)])
    const head = w.find('thead input[type="checkbox"]')
    expect((head.element as HTMLInputElement).checked).toBe(false)

    await head.setValue(true)
    expect(w.emitted('toggle-all')).toHaveLength(1)

    const w2 = mountTable([row(1)], new Set([1]))
    expect((w2.find('thead input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true)
  })

  it('行勾选透传 toggle(id)，点击行透传 open(cred)，勾选单元格不冒泡触发行点击', async () => {
    const w = mountTable([row(7)])
    await w.find('tbody input[type="checkbox"]').setValue(true)
    expect(w.emitted('toggle')).toEqual([[7]])

    const firstCell = w.find('tbody tr')
    await firstCell.trigger('click')
    expect(w.emitted('open')).toHaveLength(1)
    expect((w.emitted('open')![0] as unknown[])[0]).toMatchObject({ id: 7 })

    const emittedBefore = w.emitted('open')!.length
    await w.find('tbody td').trigger('click')
    expect(w.emitted('open')!.length).toBe(emittedBefore)
  })

  it('响应式源码断言：dense 表格、CredentialStatusBar、无 drawer-backdrop 遗留', () => {
    expect(source).toContain('data-table dense')
    expect(source).toContain('CredentialStatusBar')
    expect(source).not.toContain('drawer-backdrop')
    expect(source).not.toContain('drawer-panel')
  })
})

// ── 2026-10-07 模型计数缺失时的渲染 ────────────────────────────────────────
// 245 实测 /api/credentials/monitor-summary?mode=core 的 65 条里，model_available
// 与 model_total 各缺 7 条（99001/99002/32/36/47/55/74，如 canary-cred-A/B）。
// 旧实现用 `?? 0` 把它们补成 0，表格于是显示「0/0」，且 `0 < 0` 为假不带任何
// 告警样式 —— 读起来像「已测量、全部不可用」。
describe('模型计数的未测量形态', () => {
  it('缺计数时显示「—」，且不出现 0/0 或 undefined', () => {
    const w = mountTable([
      row(1, { modelTotal: null, modelAvailable: null, model_total: undefined, model_available: undefined }),
    ])
    const cell = w.findAll('tbody td')[5]
    expect(cell.text()).toBe('—')
    expect(cell.text()).not.toContain('0/0')
    expect(w.text()).not.toContain('undefined')
  })

  it('缺计数时带 cell-muted，并给出说明缺口的 title（不得无样式）', () => {
    const w = mountTable([
      row(1, { modelTotal: null, modelAvailable: null, model_total: undefined, model_available: undefined }),
    ])
    const span = w.findAll('tbody td')[5].find('span')
    expect(span.classes()).toContain('cell-muted')
    expect(span.classes()).not.toContain('rate-warn')
    expect(span.attributes('title')).toContain('未返回模型计数')
  })

  // 反向用例（防「修法过度」）：守卫若写成 `if (!c.modelTotal)` 或 `if (!x && !y)`，
  // 真测出来就是 0 的凭据会被一起吞成「—」。0 是实测结论，必须照常显示。
  it('真测出来就是 0/0 的凭据照常显示 0/0，不被当成缺失吞掉', () => {
    const w = mountTable([
      row(1, { modelTotal: 0, modelAvailable: 0, model_total: 0, model_available: 0 }),
    ])
    const cell = w.findAll('tbody td')[5]
    expect(cell.text()).toBe('0/0')
    expect(cell.text()).not.toBe('—')
  })

  it('部分缺失（只有 available）同样按未测量处理，不渲染半截数字', () => {
    const w = mountTable([
      row(1, { modelTotal: 10, modelAvailable: null, model_total: 10, model_available: undefined }),
    ])
    expect(w.findAll('tbody td')[5].text()).toBe('—')
  })

  it('有计数时按可用率给 rate-warn（8/10 仍告警，10/10 不告警）', () => {
    const warn = mountTable([row(1, { modelTotal: 10, modelAvailable: 8, model_total: 10, model_available: 8 })])
    expect(warn.findAll('tbody td')[5].find('span').classes()).toContain('rate-warn')
    expect(warn.findAll('tbody td')[5].text()).toBe('8/10')

    const ok = mountTable([row(2, { modelTotal: 10, modelAvailable: 10, model_total: 10, model_available: 10 })])
    expect(ok.findAll('tbody td')[5].find('span').classes()).not.toContain('rate-warn')
    expect(ok.findAll('tbody td')[5].text()).toBe('10/10')
  })

  it('宿主视图不再用 `?? 0` 把缺失补成 0（源码断言）', () => {
    const viewSource = readFileSync(resolve(process.cwd(), 'src/views/CredentialMonitorView.vue'), 'utf8')
    expect(viewSource).toContain('modelTotal: c.model_total ?? null')
    expect(viewSource).toContain('modelAvailable: c.model_available ?? null')
    expect(viewSource).not.toContain('modelTotal: c.model_total ?? 0')
    expect(viewSource).not.toContain('modelAvailable: c.model_available ?? 0')
  })
})
