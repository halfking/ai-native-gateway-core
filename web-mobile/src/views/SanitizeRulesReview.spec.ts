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
const event = { id: 1, fingerprint: 'fp', tenant_id: 'tenant-a', rule_id: 'one', version: 0, phase: 'input', action: 'mask', status: 'pending', sample_masked: '[masked]', count: 1, first_seen: 'now', last_seen: 'now' }
const response = (data: unknown) => ({ status: 200, statusText: 'OK', ok: true, text: async () => JSON.stringify(data) })
let auth: { role: string; userInfo: { id: number; tenant_id: string } }, calls: TransportRequest[]
const mounted: ReturnType<typeof mount>[] = []
function view(role = 'super_admin', tenant = 'tenant-a') { auth.role = role; auth.userInfo.tenant_id = tenant; const w = mount(SanitizeRulesView); mounted.push(w); return w }
const click = async (w: ReturnType<typeof mount>, id: string) => { await w.get(`[data-testid="${id}"]`).trigger('click'); await flushPromises() }
async function open(w: ReturnType<typeof mount>) { await click(w, 'sr-load'); await click(w, 'sr-review-open') }
beforeEach(() => {
  setLocale('zh-CN'); auth = reactive({ role: 'super_admin', userInfo: { id: 1, tenant_id: 'tenant-a' } }); holder.auth = auth; calls = []
  setTransport(async req => { calls.push(req); return response(req.method === 'POST' ? { ...event, ...JSON.parse(req.body ?? '{}'), reviewer: 'owner' } : { events: [event] }) })
})
afterEach(() => { for (const w of mounted.splice(0)) w.unmount(); resetTransport(); setLocale(originalLocale) })
describe('事件复核写侧：可达UI与真实传输', () => {
  it.each(['super_admin', 'admin_key', 'tenant_admin'])('%s可复核且必须二次确认', async role => {
    const w = view(role); await open(w); await w.get('[data-testid="sr-review-comment"]').setValue('复核理由')
    await click(w, 'sr-review-prepare'); expect(calls.filter(r => r.method === 'POST')).toHaveLength(0)
    expect(w.get('[data-testid="sr-review-confirm-note"]').text()).toContain('#1')
    await click(w, 'sr-review-submit'); const post = calls.find(r => r.method === 'POST')!
    expect(post.path).toBe('/api/admin/sanitize-rules/events/1/review'); expect(JSON.parse(post.body!)).toEqual({ status: 'confirmed_sensitive', comment: '复核理由' })
    expect(w.get('[data-testid="sr-review-success"]').text()).toBe(zhCN.sr.reviewSaved); expect(w.get('[data-testid="sr-event"]').text()).toContain('confirmed_sensitive')
    expect(w.find('[data-testid="sr-review-form"]').exists()).toBe(false)
  })
  it.each(['false_positive', 'ignored'])('%s终态可从下拉选择并提交', async status => { const w = view(); await open(w); await w.get('[data-testid="sr-review-status"]').setValue(status); await click(w, 'sr-review-prepare'); await click(w, 'sr-review-submit'); expect(JSON.parse(calls.at(-1)!.body!).status).toBe(status) })
  it.each([['user', 'tenant-a'], ['tenant_admin', '']])('%s/%s无入口无写请求', async (role, tenant) => { const w = view(role, tenant); await flushPromises(); expect(w.find('[data-testid="sr-review-open"]').exists()).toBe(false); expect(calls).toEqual([]) })
  it('取消编辑与取消确认均不写入', async () => { const w = view(); await open(w); await click(w, 'sr-review-prepare'); await click(w, 'sr-review-back'); expect(w.find('[data-testid="sr-review-submit"]').exists()).toBe(false); await click(w, 'sr-review-cancel'); expect(w.find('[data-testid="sr-review-form"]').exists()).toBe(false); expect(calls.filter(r => r.method === 'POST')).toEqual([]) })
  it('中文评论按UTF8字节拒绝且保留输入', async () => { const w = view(); await open(w); const text = '中'.repeat(667); await w.get('[data-testid="sr-review-comment"]').setValue(text); await click(w, 'sr-review-prepare'); expect(w.get('[data-testid="sr-review-error"]').text()).toBe(zhCN.sr.invalidReview); expect(calls.filter(r => r.method === 'POST')).toEqual([]); expect((w.get('[data-testid="sr-review-comment"]').element as HTMLTextAreaElement).value).toBe(text) })
  it('失败保留事件与评论并返回编辑态', async () => { const w = view(); await open(w); await w.get('[data-testid="sr-review-comment"]').setValue('reason'); setTransport(async req => { calls.push(req); return { status: 503, statusText: 'Unavailable', ok: false, text: async () => JSON.stringify({ error: 'storage failed' }) } }); await click(w, 'sr-review-prepare'); await click(w, 'sr-review-submit'); expect(w.get('[data-testid="sr-review-error"]').text()).toBe('storage failed'); expect((w.get('[data-testid="sr-review-comment"]').element as HTMLTextAreaElement).value).toBe('reason'); expect(w.get('[data-testid="sr-event"]').text()).toContain('pending'); expect(w.find('[data-testid="sr-review-prepare"]').exists()).toBe(true) })
  it('坏响应不替换事件且显示稳定错误', async () => { const w = view(); await open(w); setTransport(async () => response({ ...event, id: 2, status: 'confirmed_sensitive' })); await click(w, 'sr-review-prepare'); await click(w, 'sr-review-submit'); expect(w.get('[data-testid="sr-review-error"]').text()).toBe(zhCN.sr.invalidResponse); expect(w.get('[data-testid="sr-event"]').text()).toContain('pending') })
  it('在途重复提交与刷新不产生第二请求', async () => { const w = view(); await open(w); setTransport(req => { calls.push(req); return new Promise(() => {}) }); await click(w, 'sr-review-prepare'); const button = w.get('[data-testid="sr-review-submit"]'); await Promise.all([button.trigger('click'), button.trigger('click')]); await w.get('[data-testid="sr-load"]').trigger('click'); expect(calls.filter(r => r.method === 'POST')).toHaveLength(1); expect(calls).toHaveLength(2) })
  it.each(['id', 'tenant', 'role', 'unmount'])('%s变化取消在途且迟到成功不回填', async change => {
    const w = view(); await open(w); let resolve!: (v: ReturnType<typeof response>) => void; let signal: AbortSignal | undefined
    setTransport(req => { signal = req.signal; return new Promise(r => { resolve = r }) }); await click(w, 'sr-review-prepare'); await w.get('[data-testid="sr-review-submit"]').trigger('click')
    if (change === 'id') auth.userInfo.id = 2; else if (change === 'tenant') auth.userInfo.tenant_id = 'tenant-b'; else if (change === 'role') auth.role = 'user'; else w.unmount()
    await flushPromises(); expect(signal?.aborted).toBe(true); resolve(response({ ...event, status: 'confirmed_sensitive' })); await flushPromises()
    if (change !== 'unmount') { expect(w.find('[data-testid="sr-review-form"]').exists()).toBe(false); expect(w.find('[data-testid="sr-review-success"]').exists()).toBe(false); expect(w.find('[data-testid="sr-event"]').exists()).toBe(false) }
  })
  it('重新读取事件清空未提交表单与评论', async () => { const w = view(); await open(w); await w.get('[data-testid="sr-review-comment"]').setValue('private draft'); await click(w, 'sr-load'); expect(w.find('[data-testid="sr-review-form"]').exists()).toBe(false); await click(w, 'sr-review-open'); expect((w.get('[data-testid="sr-review-comment"]').element as HTMLTextAreaElement).value).toBe('') })
  it('确认阶段冻结输入，返回编辑可修改', async () => { const w = view(); await open(w); await click(w, 'sr-review-prepare'); expect(w.get('[data-testid="sr-review-comment"]').attributes('disabled')).toBeDefined(); expect(w.get('[data-testid="sr-review-status"]').attributes('disabled')).toBeDefined(); await click(w, 'sr-review-back'); expect(w.get('[data-testid="sr-review-comment"]').attributes('disabled')).toBeUndefined() })
  it('身份切换后新复核先成功，旧成功不覆盖', async () => {
    const w = view(); await open(w); let resolve!: (v: ReturnType<typeof response>) => void
    setTransport(() => new Promise(r => { resolve = r })); await click(w, 'sr-review-prepare'); await w.get('[data-testid="sr-review-submit"]').trigger('click'); auth.userInfo.id = 2; await flushPromises()
    setTransport(async req => response(req.method === 'POST' ? { ...event, status: 'ignored', reviewer: 'new owner' } : { events: [event] })); await open(w); await w.get('[data-testid="sr-review-status"]').setValue('ignored'); await click(w, 'sr-review-prepare'); await click(w, 'sr-review-submit')
    resolve(response({ ...event, status: 'confirmed_sensitive', reviewer: 'old owner' })); await flushPromises(); expect(w.get('[data-testid="sr-event"]').text()).toContain('new owner'); expect(w.get('[data-testid="sr-event"]').text()).not.toContain('old owner')
  })
  it.each(['tenant', 'role', 'unmount'])('%s变化后迟到失败不污染新身份', async change => {
    const w = view(); await open(w); let reject!: (e: Error) => void; let signal: AbortSignal | undefined
    setTransport(req => { signal = req.signal; return new Promise((_r, j) => { reject = j }) }); await click(w, 'sr-review-prepare'); await w.get('[data-testid="sr-review-submit"]').trigger('click')
    if (change === 'tenant') auth.userInfo.tenant_id = 'tenant-b'; else if (change === 'role') auth.role = 'user'; else w.unmount()
    await flushPromises(); expect(signal?.aborted).toBe(true); reject(new Error('old failure')); await flushPromises()
    if (change !== 'unmount') { expect(w.find('[data-testid="sr-review-error"]').exists()).toBe(false); expect(w.find('[data-testid="sr-review-success"]').exists()).toBe(false); expect(w.find('[data-testid="sr-review-form"]').exists()).toBe(false) }
  })
  it('身份切换后旧失败不污染新表单', async () => {
    const w = view(); await open(w); let reject!: (e: Error) => void
    setTransport(() => new Promise((_r, j) => { reject = j })); await click(w, 'sr-review-prepare'); await w.get('[data-testid="sr-review-submit"]').trigger('click'); auth.userInfo.id = 2; await flushPromises()
    setTransport(async () => response({ events: [event] })); await open(w); await w.get('[data-testid="sr-review-comment"]').setValue('new reason'); reject(new Error('old failure')); await flushPromises()
    expect(w.find('[data-testid="sr-review-error"]').exists()).toBe(false); expect((w.get('[data-testid="sr-review-comment"]').element as HTMLTextAreaElement).value).toBe('new reason')
  })
})
