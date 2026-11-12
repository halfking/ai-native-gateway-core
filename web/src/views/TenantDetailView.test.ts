import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

async function readViewSource(): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/views/TenantDetailView.vue'),
    resolve(process.cwd(), 'src/views/TenantDetailView.vue'),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      // try the next candidate (running tests from repo root vs web/)
    }
  }
  throw new Error('TenantDetailView.vue not found in candidate paths')
}

describe('tenant billing audit contract', () => {
  it('uses the provider, credential, model, token, and margin dimensions', async () => {
    const source = await readViewSource()
    expect(source).toContain("activeTab === 'billing'")
    expect(source).toContain('row.credential_label')
    expect(source).toContain('row.cache_read_tokens')
    expect(source).toContain('row.gross_margin_rate')
    expect(source).toContain('billingOwnerUser')
  })

  it('clears per-tenant state when navigating to a different tenant', async () => {
    const source = await readViewSource()
    expect(source).toMatch(/resetTenantScopedState\(\)/)
    expect(source).toContain('billing.value = null')
  })

  it('refreshes billing data when filters change', async () => {
    const source = await readViewSource()
    expect(source).toContain('onBillingFilterChange')
    expect(source).toMatch(/onBillingFilterChange[\s\S]*activeTab\.value === 'billing'/)
  })
})

// 2026-11-13 落库轮：租户新增用户对话框的两道构建门 + 契约钉。
// 背景（2026-11-12 并行会话 154 部署轮实测）：el-* 组件不显式 import 会撞构建门
// （unplugin 自动注册在该视图不可用），el-dialog 无 max-height 会在矮视口溢出。
describe('tenant create-user dialog contract', () => {
  it('explicitly imports the element-plus components it uses', async () => {
    const source = await readViewSource()
    expect(source).toContain(
      "import { ElButton, ElDialog, ElForm, ElFormItem, ElInput, ElSelect, ElOption } from 'element-plus'",
    )
  })

  it('clamps the dialog height and binds tenant_id to the viewed tenant', async () => {
    const source = await readViewSource()
    expect(source).toMatch(/<el-dialog[^>]*max-height:\s*80vh/)
    expect(source).toMatch(/tenant_id:\s*tenantCode\.value/)
  })

  it('validates before submit and offers only the backend closed role set', async () => {
    const source = await readViewSource()
    // 前端先拦空用户名/短密码（后端 users.go 仍有 ≥8 + 复杂度 + 闭集校验，双保险）
    expect(source).toContain("t('tenants.detail.usernameRequired')")
    expect(source).toContain("t('tenants.detail.passwordTooShort')")
    // 角色下拉与后端闭集 {tenant_admin, super_admin} 一致
    expect(source).toContain('value="tenant_admin"')
    expect(source).toContain('value="super_admin"')
    // 提交走 api.createUser（后端 POST /api/users），不内联 fetch
    expect(source).toMatch(/await createUser\(/)
  })

  it('keeps the dialog fully i18n-ed (no hardcoded CJK in the new markup)', async () => {
    const source = await readViewSource()
    const start = source.indexOf('<!-- Create User Dialog -->')
    const end = source.indexOf('</el-dialog>', start)
    const dialogBlock = source.slice(start, end)
    expect(dialogBlock).not.toMatch(/[\u4e00-\u9fff]/)
    // 入口按钮在 Users Tab 头部（对话框之外），也走 i18n
    expect(source).toContain('$t(\'tenants.detail.createUserBtn\')')
  })
})
