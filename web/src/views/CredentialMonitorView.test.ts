// CredentialMonitorView.test.ts — 装配层源码断言（§4.7-4 拆分后本视图只留装配）：
// 子组件引用、批量弹窗 AppModal 承载、无手写弹层/表格遗留。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const source = readFileSync(resolve(process.cwd(), 'src/views/CredentialMonitorView.vue'), 'utf8')

describe('CredentialMonitorView（装配层）', () => {
  it('引用 credential-monitor 子组件与 ui 组件', () => {
    expect(source).toContain('components/credential-monitor/CredentialMonitorTable.vue')
    expect(source).toContain('components/credential-monitor/CredentialMonitorFilters.vue')
    expect(source).toContain('components/credential-monitor/CredentialDetailDrawer.vue')
    expect(source).toContain("import AppModal from '../components/ui/AppModal.vue'")
    expect(source).toContain('StatsRow')
  })

  it('批量弹窗迁 ui/AppModal sm；详情抽屉 v-model 装配', () => {
    expect(source).toMatch(/<AppModal[^>]*v-model="batchDialogOpen"/s)
    expect(source).toContain('size="sm"')
    expect(source).toContain('<CredentialDetailDrawer v-model="selectedCred"')
  })

  it('无手写 drawer-backdrop / 手写表格遗留（视觉结构已下沉子组件）', () => {
    expect(source).not.toContain('drawer-backdrop')
    expect(source).not.toContain('<table')
    // 抽屉壳由 CredentialDetailDrawer 承载，视图不直接 import 通用抽屉组件
    expect(source).not.toContain("ui/AppDrawer.vue'")
  })

  it('保留 720px 之外无新增碎片断点（行1工具栏无 media）', () => {
    expect(source).not.toMatch(/@media \(max-width:/)
  })
})
