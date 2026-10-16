import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import SanitizeRulesView from './SanitizeRulesView.vue'
import { setTransport, resetTransport, type TransportRequest } from '@/api/transport'
import { locale, setLocale } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
const originalLocale = locale.value
vi.mock('@/hyper', async original => ({ ...await original<typeof import('@/hyper')>(), useHyperPage: () => {} }))
const holder = vi.hoisted(() => ({ auth: null as unknown }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => holder.auth }))
const config = { rules: [{ id: 'one', name: 'One', type: 'custom', kind: 'pattern', pattern: 'token', group: 0, scopes: ['input'], action: 'mask', enabled: true, priority: 0 }] }
const current = { version: 7, config, config_digest: 'a'.repeat(64), status: { instance_id: 'node', version: 7, config_digest: 'a'.repeat(64), last_seen: 'now', last_refresh: 'now', error: '' } }
const version = { id: 7, parent_id: 6, config, actor: 'owner', comment: 'initial', created_at: '2026-10-16T00:00:00Z' }
const response = (data: unknown) => ({ status: 200, statusText: 'OK', ok: true, text: async () => JSON.stringify(data) })
const errorResponse = (status: number, body: unknown) => ({ status, statusText: 'Err', ok: false, text: async () => JSON.stringify(body) })
let auth: { role: string; userInfo: { id: number; tenant_id: string } }, calls: TransportRequest[]
const mounted: ReturnType<typeof mount>[] = []
function view(role = 'super_admin') { auth.role = role; const w = mount(SanitizeRulesView); mounted.push(w); return w }
const click = async (w: ReturnType<typeof mount>, id: string) => { await w.get(`[data-testid="${id}"]`).trigger('click'); await flushPromises() }
const input = (w: ReturnType<typeof mount>, id: string) => w.get(`[data-testid="${id}"]`)
async function copyValidate(w: ReturnType<typeof mount>) { await click(w, 'sr-rules-load'); await click(w, 'sr-draft-copy'); await click(w, 'sr-draft-validate') }
beforeEach(() => {
  setLocale('zh-CN'); auth = reactive({ role: 'super_admin', userInfo: { id: 1, tenant_id: 'tenant-a' } }); holder.auth = auth; calls = []
  setTransport(async req => {
    calls.push(req)
    if (req.method === 'POST') {
      if (req.path.includes('/publish')) return response({ ...version, id: 8, parent_id: 7, config_digest: 'b'.repeat(64), comment: req.body ? JSON.parse(req.body).comment : '' })
      if (req.path.includes('/rollback')) return response({ ...version, id: 8, parent_id: 7, config_digest: 'c'.repeat(64), rollback_of: 7, comment: req.body ? JSON.parse(req.body).comment : '' })
      return response({ valid: true, config_digest: 'b'.repeat(64) })
    }
    if (req.path.includes('/versions')) return response({ versions: [version] })
    return response(current)
  })
})
afterEach(() => { for (const w of mounted.splice(0)) w.unmount(); resetTransport(); setLocale(originalLocale) })
describe('发布草稿写侧：可达UI与真实传输', () => {
  it.each(['super_admin', 'admin_key'])('%s已验证草稿可发布、必须二次确认', async role => {
    const w = view(role); await copyValidate(w); await input(w, 'sr-publish-comment').setValue('上线')
    await click(w, 'sr-publish-prepare'); expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/publish'))).toHaveLength(0)
    expect(w.get('[data-testid="sr-publish-confirm-note"]').text()).toContain('7'); expect(w.get('[data-testid="sr-publish-confirm-note"]').text()).toContain('上线')
    await click(w, 'sr-publish-submit'); const post = calls.find(r => r.method === 'POST' && r.path.endsWith('/publish'))!
    expect(JSON.parse(post.body!)).toEqual({ base_version: 7, config, comment: '上线' })
    expect(w.get('[data-testid="sr-publish-success"]').text()).toBe(zhCN.sr.publishSaved)
    expect((w.find('[data-testid="sr-draft-input"]').element as HTMLTextAreaElement).value).toBe('')
    expect(w.find('[data-testid="sr-draft-valid"]').exists()).toBe(false)
  })
  it('未读取规则时发布按钮disabled', async () => {
    const w = view(); expect(w.get('[data-testid="sr-publish-prepare"]').attributes('disabled')).toBeDefined()
  })
  it('已读取但未验证草稿时发布按钮disabled且点击不发请求', async () => {
    const w = view(); await click(w, 'sr-rules-load'); expect(w.get('[data-testid="sr-publish-prepare"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="sr-publish-prepare"]').trigger('click'); await flushPromises()
    expect(calls.filter(r => r.method === 'POST')).toEqual([])
  })
  it('已验证草稿但评论为空不进入确认态且不发送', async () => {
    const w = view(); await copyValidate(w); await click(w, 'sr-publish-prepare')
    expect(w.get('[data-testid="sr-publish-error"]').text()).toBe(zhCN.sr.invalidPublishComment)
    expect(w.find('[data-testid="sr-publish-confirm-note"]').exists()).toBe(false)
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/publish'))).toEqual([])
  })
  it('中文评论按UTF-8字节拒绝且保留输入', async () => {
    const w = view(); await copyValidate(w); await input(w, 'sr-publish-comment').setValue('中'.repeat(2000))
    await click(w, 'sr-publish-prepare')
    expect(w.get('[data-testid="sr-publish-error"]').text()).toBe(zhCN.sr.invalidPublishComment)
    expect((input(w, 'sr-publish-comment').element as HTMLTextAreaElement).value.length).toBe(2000)
  })
  it.each(['tenant_admin', 'user', 'admin', 'unknown'])('%s无发布入口', async role => {
    const w = view(role); await flushPromises()
    expect(w.find('[data-testid="sr-publish-form"]').exists()).toBe(false)
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/publish'))).toEqual([])
  })
  it('失败保留草稿、错误、版本', async () => {
    const w = view(); await copyValidate(w); await input(w, 'sr-publish-comment').setValue('comment')
    setTransport(async () => errorResponse(409, { error: 'conflict' }))
    await click(w, 'sr-publish-prepare'); await click(w, 'sr-publish-submit')
    expect(w.get('[data-testid="sr-publish-error"]').text()).toBe('conflict')
    expect((input(w, 'sr-publish-comment').element as HTMLTextAreaElement).value).toBe('comment')
    expect(w.find('[data-testid="sr-draft-valid"]').exists()).toBe(true)
    expect(w.find('[data-testid="sr-publish-prepare"]').exists()).toBe(true)
  })
  it('坏响应不更新版本并显示稳定错误', async () => {
    const w = view(); await copyValidate(w); await input(w, 'sr-publish-comment').setValue('comment')
    setTransport(async () => response({ id: 'bad' }))
    await click(w, 'sr-publish-prepare'); await click(w, 'sr-publish-submit')
    expect(w.get('[data-testid="sr-publish-error"]').text()).toBe(zhCN.sr.invalidResponse)
    expect(w.find('[data-testid="sr-publish-prepare"]').exists()).toBe(true)
  })
  it('同发布不可并发与刷新隔离', async () => {
    const w = view(); await copyValidate(w); await input(w, 'sr-publish-comment').setValue('comment')
    setTransport(req => { calls.push(req); return new Promise(() => {}) })
    await click(w, 'sr-publish-prepare')
    const button = w.get('[data-testid="sr-publish-submit"]')
    await Promise.all([button.trigger('click'), button.trigger('click')])
    await w.get('[data-testid="sr-load"]').trigger('click')
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/publish'))).toHaveLength(1)
  })
  it.each(['id', 'tenant', 'role', 'unmount'])('发布期间%s变化取消在途', async change => {
    const w = view(); await copyValidate(w); await input(w, 'sr-publish-comment').setValue('comment')
    let signal: AbortSignal | undefined; setTransport(req => { signal = req.signal; return new Promise(() => {}) })
    await click(w, 'sr-publish-prepare'); await w.get('[data-testid="sr-publish-submit"]').trigger('click')
    if (change === 'id') auth.userInfo.id = 2
    else if (change === 'tenant') auth.userInfo.tenant_id = 'b'
    else if (change === 'role') auth.role = 'user'
    else w.unmount()
    await flushPromises(); expect(signal?.aborted).toBe(true)
  })
  it('确认期间冻结输入，返回编辑可修改', async () => {
    const w = view(); await copyValidate(w); await input(w, 'sr-publish-comment').setValue('comment')
    await click(w, 'sr-publish-prepare'); expect(input(w, 'sr-publish-comment').attributes('disabled')).toBeDefined()
    await click(w, 'sr-publish-back'); expect(input(w, 'sr-publish-comment').attributes('disabled')).toBeUndefined()
  })
})
describe('回滚写侧：可达UI与真实传输', () => {
  async function setupWithVersions() { const w = view(); await click(w, 'sr-rules-load'); await click(w, 'sr-versions-load'); return w }
  it('manager可回滚到任意已发布版本，必须二次确认', async () => {
    const w = await setupWithVersions(); await click(w, 'sr-rollback-open'); await input(w, 'sr-rollback-comment').setValue('退回')
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/rollback'))).toHaveLength(0)
    await click(w, 'sr-rollback-prepare'); expect(w.get('[data-testid="sr-rollback-confirm-note"]').text()).toContain('7')
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/rollback'))).toHaveLength(0)
    await click(w, 'sr-rollback-submit'); const post = calls.find(r => r.method === 'POST' && r.path.endsWith('/rollback'))!
    expect(JSON.parse(post.body!)).toEqual({ base_version: 7, target_version: 7, comment: '退回' })
    expect(w.get('[data-testid="sr-rollback-success"]').text()).toBe(zhCN.sr.rollbackSaved)
  })
  it.each(['tenant_admin', 'user', 'admin'])('%s无回滚入口', async role => {
    const w = view(role); await flushPromises()
    expect(w.find('[data-testid="sr-rollback-open"]').exists()).toBe(false)
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/rollback'))).toEqual([])
  })
  it('回滚评论必填且UTF-8字节受限', async () => {
    const w = await setupWithVersions(); await click(w, 'sr-rollback-open'); await click(w, 'sr-rollback-prepare')
    expect(w.get('[data-testid="sr-rollback-error"]').text()).toBe(zhCN.sr.invalidPublishComment)
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/rollback'))).toEqual([])
    await input(w, 'sr-rollback-comment').setValue('中'.repeat(2000)); await click(w, 'sr-rollback-prepare')
    expect(w.get('[data-testid="sr-rollback-error"]').text()).toBe(zhCN.sr.invalidPublishComment)
  })
  it('失败保留回滚评论并显示稳定错误', async () => {
    const w = await setupWithVersions(); await click(w, 'sr-rollback-open'); await input(w, 'sr-rollback-comment').setValue('comment')
    setTransport(async () => errorResponse(404, { error: 'not found' }))
    await click(w, 'sr-rollback-prepare'); await click(w, 'sr-rollback-submit')
    expect(w.get('[data-testid="sr-rollback-error"]').text()).toBe('not found')
    expect((input(w, 'sr-rollback-comment').element as HTMLTextAreaElement).value).toBe('comment')
    expect(w.find('[data-testid="sr-rollback-prepare"]').exists()).toBe(true)
  })
  it('同回滚不可并发与刷新隔离', async () => {
    const w = await setupWithVersions(); await click(w, 'sr-rollback-open'); await input(w, 'sr-rollback-comment').setValue('comment')
    setTransport(req => { calls.push(req); return new Promise(() => {}) })
    await click(w, 'sr-rollback-prepare')
    const button = w.get('[data-testid="sr-rollback-submit"]')
    await Promise.all([button.trigger('click'), button.trigger('click')])
    await w.get('[data-testid="sr-load"]').trigger('click')
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/rollback'))).toHaveLength(1)
  })
  it.each(['id', 'tenant', 'role', 'unmount'])('回滚期间%s变化取消在途', async change => {
    const w = await setupWithVersions(); await click(w, 'sr-rollback-open'); await input(w, 'sr-rollback-comment').setValue('comment')
    let signal: AbortSignal | undefined; setTransport(req => { signal = req.signal; return new Promise(() => {}) })
    await click(w, 'sr-rollback-prepare'); await w.get('[data-testid="sr-rollback-submit"]').trigger('click')
    if (change === 'id') auth.userInfo.id = 2
    else if (change === 'tenant') auth.userInfo.tenant_id = 'b'
    else if (change === 'role') auth.role = 'user'
    else w.unmount()
    await flushPromises(); expect(signal?.aborted).toBe(true)
  })
  it('回滚进行中发布卡被锁定（manager 拒绝 prepare 真实 POST）', async () => {
    const w = await setupWithVersions(); await click(w, 'sr-rollback-open'); await input(w, 'sr-rollback-comment').setValue('comment')
    setTransport(req => { calls.push(req); return new Promise(() => {}) })
    await click(w, 'sr-rollback-prepare'); await w.get('[data-testid="sr-rollback-submit"]').trigger('click')
    await copyValidate(w)
    expect(calls.filter(r => r.method === 'POST' && r.path.endsWith('/publish'))).toEqual([])
  })
})
