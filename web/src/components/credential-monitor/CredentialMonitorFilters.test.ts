// CredentialMonitorFilters.test.ts — 筛选/快捷过滤/批量入口的事件透传，
// 以及源码断言（§4.7-4 拆分后的筛选行子组件）。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import CredentialMonitorFilters from './CredentialMonitorFilters.vue'

const source = readFileSync(resolve(process.cwd(), 'src/components/credential-monitor/CredentialMonitorFilters.vue'), 'utf8')

function mountFilters(props: Record<string, unknown> = {}) {
  return mount(CredentialMonitorFilters, {
    props: {
      availStateFilter: '',
      healthFilter: '',
      quickFilter: 'none',
      selectedCount: 0,
      canManage: true,
      ...props,
    },
  })
}

describe('CredentialMonitorFilters', () => {
  it('可用性/健康下拉 change 透传 update 事件', async () => {
    const w = mountFilters()
    const selects = w.findAll('select')
    await selects[0].setValue('ready')
    expect(w.emitted('update:availStateFilter')).toEqual([['ready']])
    await selects[1].setValue('warning')
    expect(w.emitted('update:healthFilter')).toEqual([['warning']])
  })

  it('快捷过滤按钮透传 update:quickFilter', async () => {
    const w = mountFilters()
    const buttons = w.findAll('.quick-filter-group button')
    await buttons[1].trigger('click')
    expect(w.emitted('update:quickFilter')).toEqual([['broken']])
    await buttons[2].trigger('click')
    expect(w.emitted('update:quickFilter')![1]).toEqual(['low-rate'])
  })

  it('批量按钮：透传 batch action；未选中或无权限时禁用', async () => {
    const w = mountFilters({ selectedCount: 3 })
    const [promoteBtn, demoteBtn] = w.findAll('button.btn-success, button.btn-danger')
    await promoteBtn.trigger('click')
    await demoteBtn.trigger('click')
    expect(w.emitted('batch')).toEqual([['promote'], ['demote']])

    const w2 = mountFilters({ selectedCount: 0 })
    expect((w2.find('button.btn-success').element as HTMLButtonElement).disabled).toBe(true)

    const w3 = mountFilters({ selectedCount: 3, canManage: false })
    expect((w3.find('button.btn-success').element as HTMLButtonElement).disabled).toBe(true)
  })

  it('源码断言：保留 top-bar-secondary 视觉与快捷过滤样式', () => {
    expect(source).toContain('top-bar-secondary')
    expect(source).toContain('qf-active')
    expect(source).toContain('批量恢复')
    expect(source).toContain('批量降级')
  })
})
