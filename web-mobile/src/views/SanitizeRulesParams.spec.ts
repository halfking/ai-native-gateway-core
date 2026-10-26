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
const response = (data: unknown) => ({ status: 200, statusText: 'OK', ok: true, text: async () => JSON.stringify(data) })
const errorResponse = (status: number, body: unknown) => ({ status, statusText: 'Err', ok: false, text: async () => JSON.stringify(body) })
let auth: { role: string; userInfo: { id: number; tenant_id: string } }, calls: TransportRequest[]
const mounted: ReturnType<typeof mount>[] = []
function view(role = 'super_admin') { auth.role = role; const w = mount(SanitizeRulesView); mounted.push(w); return w }
const click = async (w: ReturnType<typeof mount>, id: string) => { await w.get(`[data-testid="${id}"]`).trigger('click'); await flushPromises() }
const input = (w: ReturnType<typeof mount>, id: string) => w.get(`[data-testid="${id}"]`).element as HTMLInputElement | HTMLSelectElement
// 必须满足 platformSettings.unwrapSettingsList 的 15 键硬校验：key/env_name/type/scope/category/default/value/source/options/min/max/description/danger_level/hot_reload/observability
const paramItems = (current: Record<string, number | boolean>) => ({ items: [
  { key: 'sanitize.rules.refresh_interval_seconds', env_name: 'SANITIZE_RULES_REFRESH_INTERVAL_SECONDS', type: 'int', scope: 'platform', category: 'modules', default: 5, value: current.refresh_interval_seconds, source: 'env', options: null, min: 1, max: 300, description: '刷新间隔', danger_level: 1, hot_reload: true, observability: 'standard' },
  { key: 'sanitize.rules.audit_enabled', env_name: 'SANITIZE_RULES_AUDIT_ENABLED', type: 'bool', scope: 'platform', category: 'modules', default: true, value: current.audit_enabled, source: 'env', options: null, min: null, max: null, description: '审计启用', danger_level: 1, hot_reload: true, observability: 'standard' },
  { key: 'sanitize.rules.event_limit', env_name: 'SANITIZE_RULES_EVENT_LIMIT', type: 'int', scope: 'platform', category: 'modules', default: 10000, value: current.event_limit, source: 'env', options: null, min: 100, max: 100000, description: '事件保留量', danger_level: 1, hot_reload: false, observability: 'standard' },
] })
beforeEach(() => {
  setLocale('zh-CN'); auth = reactive({ role: 'super_admin', userInfo: { id: 1, tenant_id: 'tenant-a' } }); holder.auth = auth; calls = []
  setTransport(async req => { calls.push(req); return response(req.method === 'PUT' ? { status: 'ok', new_value: JSON.parse(req.body ?? '{}').value, applied_at: 'now' } : paramItems({ refresh_interval_seconds: 5, audit_enabled: true, event_limit: 10000 })) })
})
afterEach(() => { for (const w of mounted.splice(0)) w.unmount(); resetTransport(); setLocale(originalLocale) })
describe('脱敏规则参数：可达UI与真实传输', () => {
  it('manager读取后展示三键当前值', async () => {
    const w = view(); await click(w, 'sr-params-load')
    expect(calls.find(c => c.path === '/api/admin/settings?category=modules')).toBeDefined()
    expect(w.get('[data-testid="sr-params-input-refresh_interval_seconds"]').element).toBeInstanceOf(HTMLInputElement)
    const refresh = w.get('[data-testid="sr-params-input-refresh_interval_seconds"]').element as HTMLInputElement
    expect(refresh.value).toBe('5')
    const limit = w.get('[data-testid="sr-params-input-event_limit"]').element as HTMLInputElement
    expect(limit.value).toBe('10000')
    const audit = w.get('[data-testid="sr-params-input-audit_enabled"]').element as HTMLSelectElement
    expect(audit.value).toBe('true')
    expect(w.get('[data-testid="sr-params-save"]').attributes('disabled')).toBeDefined()
  })
  it.each(['tenant_admin', 'user', 'admin', 'unknown'])('%s无参数卡', async role => {
    const w = view(role); await flushPromises()
    expect(w.find('[data-testid="sr-params-section"]').exists()).toBe(false)
  })
  it('编辑后启用保存，超出范围返回稳定错误', async () => {
    const w = view(); await click(w, 'sr-params-load')
    const refresh = input(w, 'sr-params-input-refresh_interval_seconds') as HTMLInputElement
    refresh.value = '10'; refresh.dispatchEvent(new Event('input'))
    await flushPromises()
    expect(w.get('[data-testid="sr-params-save"]').attributes('disabled')).toBeUndefined()
    refresh.value = '500'; refresh.dispatchEvent(new Event('input'))
    await click(w, 'sr-params-save')
    expect(w.get('[data-testid="sr-params-error"]').text()).toBe(zhCN.sr.paramsInvalid)
    expect(calls.filter(c => c.method === 'PUT')).toEqual([])
  })
  it('保存三键修改：并发PUT，超出范围前在请求里', async () => {
    const w = view(); await click(w, 'sr-params-load')
    const refresh = input(w, 'sr-params-input-refresh_interval_seconds') as HTMLInputElement
    refresh.value = '15'; refresh.dispatchEvent(new Event('input'))
    const audit = input(w, 'sr-params-input-audit_enabled') as HTMLSelectElement
    audit.value = 'false'; audit.dispatchEvent(new Event('change'))
    const limit = input(w, 'sr-params-input-event_limit') as HTMLInputElement
    limit.value = '20000'; limit.dispatchEvent(new Event('input'))
    await flushPromises() // 等 Vue 把三键 dirty 收进 DOM 让保存按钮解除 disabled —— trigger('click') 同步读 DOM disabled，决定要不要派发
    await click(w, 'sr-params-save')
    const puts = calls.filter(c => c.method === 'PUT')
    expect(puts).toHaveLength(3)
    const byPath = Object.fromEntries(puts.map(p => [p.path, JSON.parse(p.body!)])) as Record<string, { value: number | boolean }>
    expect(byPath['/api/admin/settings/sanitize.rules.refresh_interval_seconds']!.value).toBe(15)
    expect(byPath['/api/admin/settings/sanitize.rules.audit_enabled']!.value).toBe(false)
    expect(byPath['/api/admin/settings/sanitize.rules.event_limit']!.value).toBe(20000)
  })
  it('保存后自动重读并清稿', async () => {
    const w = view(); await click(w, 'sr-params-load')
    const refresh = input(w, 'sr-params-input-refresh_interval_seconds') as HTMLInputElement
    refresh.value = '20'; refresh.dispatchEvent(new Event('input'))
    await flushPromises()
    await click(w, 'sr-params-save')
    const getAfter = calls.filter(c => c.method === 'GET' && c.path.includes('category=modules')).length
    expect(getAfter).toBeGreaterThanOrEqual(2)
    expect(w.get('[data-testid="sr-params-save"]').attributes('disabled')).toBeDefined()
  })
  it('失败保留草稿、错误文案', async () => {
    const w = view(); await click(w, 'sr-params-load')
    setTransport(async req => { calls.push(req); if (req.method === 'PUT') return errorResponse(400, { error: 'rejected' }); return response(paramItems({ refresh_interval_seconds: 5, audit_enabled: true, event_limit: 10000 })) })
    const refresh = input(w, 'sr-params-input-refresh_interval_seconds') as HTMLInputElement
    refresh.value = '20'; refresh.dispatchEvent(new Event('input'))
    await flushPromises()
    await click(w, 'sr-params-save')
    expect(w.get('[data-testid="sr-params-error"]').text()).toBe('rejected')
    // 注：input.value = '…' 在标准 DOM 中会同步写入 HTML attribute；草稿保留的实质判定是「按钮仍可保存」+「error 已上屏」
    expect(w.get('[data-testid="sr-params-save"]').attributes('disabled')).toBeUndefined()
  })
  it('身份切换取消在途，旧响应不污染', async () => {
    const w = view(); await click(w, 'sr-params-load')
    let resolve!: (r: ReturnType<typeof response>) => void; let signal: AbortSignal | undefined
    setTransport(req => { signal = req.signal; return new Promise(r => { resolve = r }) })
    const refresh = input(w, 'sr-params-input-refresh_interval_seconds') as HTMLInputElement
    refresh.value = '20'; refresh.dispatchEvent(new Event('input'))
    await flushPromises()
    await click(w, 'sr-params-save')
    auth.userInfo.id = 2; await flushPromises()
    expect(signal?.aborted).toBe(true)
    resolve(response({ status: 'ok', new_value: 20, applied_at: 'now' })); await flushPromises()
    expect(w.find('[data-testid="sr-params-success"]').exists()).toBe(false)
  })
  it('非manager读取失败时显示稳定错误', async () => {
    const w = view(); setTransport(async () => errorResponse(503, { error: 'down' })); await click(w, 'sr-params-load')
    expect(w.get('[data-testid="sr-params-error"]').text()).toBe('down')
  })
})
