// CredentialMonitorTable.test.ts — 行渲染、勾选/打开详情事件透传，
// 以及响应式源码断言（§4.7-4 拆分后的主列表子组件）。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import CredentialMonitorTable, { type CredentialRow } from './CredentialMonitorTable.vue'

const source = readFileSync(resolve(process.cwd(), 'src/components/credential-monitor/CredentialMonitorTable.vue'), 'utf8')

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
