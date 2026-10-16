import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import SanitizeRulesView from './SanitizeRulesView.vue'
import { setTransport, resetTransport } from '@/api/transport'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { locale, setLocale } from '@/i18n'
const originalLocale = locale.value

vi.mock('@/hyper', async original => ({ ...await original<typeof import('@/hyper')>(), useHyperPage: () => {} }))
const holder = vi.hoisted(() => ({ auth: null as unknown }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => holder.auth }))
const rule = { id: 'rule-one', name: 'Rule one', type: 'custom', kind: 'pattern', pattern: 'token', group: 0, scopes: ['input'], action: 'mask', enabled: true, priority: 0 }
const instance = { instance_id: 'node-a', version: 0, config_digest: 'digest', last_seen: '2026-10-16T00:00:00Z', last_refresh: '2026-10-16T00:00:00Z', error: '' }
const current = { version: 0, config: { rules: [rule] }, config_digest: 'digest', status: instance }
const event = { id: 1, fingerprint: 'fp', tenant_id: 'tenant-a', rule_id: 'rule-one', version: 0, phase: 'input', action: 'mask', status: 'pending', sample_masked: '<masked>', count: 2, first_seen: '2026-10-16T00:00:00Z', last_seen: '2026-10-16T00:00:01Z', type: 'custom', field: 'body' }
const version = { id: 1, parent_id: 0, config: { rules: [rule] }, actor: 'owner', comment: 'initial', created_at: '2026-10-16T00:00:00Z' }
const response = (data: unknown) => ({ status: 200, statusText: 'OK', ok: true, text: async () => JSON.stringify(data) })
let auth: { role: string; userInfo: { id: number; tenant_id: string } }
let calls: string[], signals: Array<AbortSignal | undefined>
const mounted: ReturnType<typeof mount>[] = []
function view(role = 'super_admin', tenant = 'tenant-a') { auth.role = role; auth.userInfo.tenant_id = tenant; const w = mount(SanitizeRulesView); mounted.push(w); return w }
function install() {
  setTransport(async req => {
    calls.push(req.path); signals.push(req.signal)
    if (req.path.includes('/events')) return response({ events: [event] })
    if (req.path.endsWith('/versions')) return response({ versions: [version] })
    if (req.path.endsWith('/instances')) return response({ target_version: 1, instances: [instance] })
    return response(current)
  })
}
beforeEach(() => { setLocale('zh-CN'); auth = reactive({ role: 'super_admin', userInfo: { id: 1, tenant_id: 'tenant-a' } }); holder.auth = auth; calls = []; signals = []; install() })
afterEach(() => { for (const w of mounted.splice(0)) w.unmount(); resetTransport(); setLocale(originalLocale) })
const click = async (w: ReturnType<typeof mount>, id: string) => { await w.get(`[data-testid="${id}"]`).trigger('click'); await flushPromises() }

describe('脱敏规则只读面：真实传输与分区权限', () => {
  it('user无请求、无取数按钮、有拒绝内容', async () => { const w = view('user'); await flushPromises(); expect(calls).toEqual([]); expect(w.find('[data-testid="sr-load"]').exists()).toBe(false); expect(w.get('[data-testid="sr-nope"]').text()).toContain(zhCN.sr.denied) })
  it('无租户tenant_admin被拒绝', () => { const w = view('tenant_admin', ''); expect(w.find('[data-testid="sr-nope"]').exists()).toBe(true); expect(calls).toEqual([]) })
  it('tenant_admin只看到事件且只发GET事件请求', async () => { const w = view('tenant_admin'); expect(w.find('[data-testid="sr-rules-load"]').exists()).toBe(false); expect(w.find('[data-testid="sr-versions-load"]').exists()).toBe(false); expect(w.find('[data-testid="sr-instances-load"]').exists()).toBe(false); await click(w, 'sr-load'); expect(calls).toHaveLength(1); expect(calls[0]).toContain('/events?'); expect(calls[0]).not.toContain('tenant_id'); expect(w.get('[data-testid="sr-scope"]').text()).toBe(zhCN.sr.ownTenant); expect(w.get('[data-testid="sr-event-structure"]').text()).toContain('custom / body') })
  it.each(['super_admin', 'admin_key'])('%s四个GET都可由按钮到达，不自动取数', async role => { const w = view(role); await flushPromises(); expect(calls).toEqual([]); for (const id of ['sr-load', 'sr-rules-load', 'sr-versions-load', 'sr-instances-load']) await click(w, id); expect(calls).toHaveLength(4); expect(w.find('[data-testid="sr-rule"]').exists()).toBe(true); expect(w.find('[data-testid="sr-version"]').exists()).toBe(true); expect(w.find('[data-testid="sr-instance"]').exists()).toBe(true); expect(w.get('[data-testid="sr-fallback"]').text()).toBe(zhCN.sr.fallback); expect(w.get('[data-testid="sr-instance"]').text()).toContain(zhCN.sr.versionDiffers) })
  it('事件筛选参数与上限发往服务端', async () => { const w = view(); await w.get('[data-testid="sr-action"]').setValue('mask'); await w.get('[data-testid="sr-status"]').setValue('pending'); await w.get('[data-testid="sr-limit"]').setValue('20'); await click(w, 'sr-load'); const q = new URL(calls[0]!, 'https://example.test').searchParams; expect(q.get('action')).toBe('mask'); expect(q.get('status')).toBe('pending'); expect(q.get('limit')).toBe('20') })
  it('空事件与未读取不同', async () => { setTransport(async req => { calls.push(req.path); return response({ events: [] }) }); const w = view(); expect(w.text()).toContain(zhCN.sr.notLoaded); await click(w, 'sr-load'); expect(w.get('[data-testid="sr-events-empty"]').text()).toBe(zhCN.sr.empty) })
  it('异常响应显示错误且保留已加载内容', async () => { const w = view(); await click(w, 'sr-load'); setTransport(async () => response({ events: [{ id: 'bad' }] })); await click(w, 'sr-load'); expect(w.get('[data-testid="sr-events-error"]').text()).toBe(zhCN.sr.invalidResponse); expect(w.get('[data-testid="sr-event"]').text()).toContain('rule-one') })
  it('角色降级清除已加载内容', async () => { const w = view(); await click(w, 'sr-rules-load'); auth.role = 'user'; await flushPromises(); expect(w.find('[data-testid="sr-rule"]').exists()).toBe(false); expect(w.find('[data-testid="sr-nope"]').exists()).toBe(true); auth.role = 'super_admin'; await flushPromises(); expect(w.find('[data-testid="sr-rule"]').exists()).toBe(false) })
  it('账号切换取消在途请求且旧响应不能回填', async () => { let resolve!: (value: ReturnType<typeof response>) => void; setTransport(req => { signals.push(req.signal); return new Promise(r => { resolve = r }) }); const w = view(); await w.get('[data-testid="sr-load"]').trigger('click'); auth.userInfo.id = 2; await flushPromises(); expect(signals[0]?.aborted).toBe(true); resolve(response({ events: [event] })); await flushPromises(); expect(w.find('[data-testid="sr-event"]').exists()).toBe(false); expect(w.get('[data-testid="sr-load"]').attributes('disabled')).toBeUndefined() })
  it('租户切换清空事件且取消请求', async () => { const w = view('tenant_admin'); await click(w, 'sr-load'); setTransport(req => { signals.push(req.signal); return new Promise(() => {}) }); await w.get('[data-testid="sr-load"]').trigger('click'); auth.userInfo.tenant_id = 'tenant-b'; await flushPromises(); expect(w.find('[data-testid="sr-event"]').exists()).toBe(false); expect(signals.at(-1)?.aborted).toBe(true) })
  it.each(['sr-load', 'sr-rules-load', 'sr-versions-load', 'sr-instances-load'])('%s账号切换后新响应胜出，旧响应不得覆盖', async id => {
    const pending: Array<(value: ReturnType<typeof response>) => void> = []
    setTransport(req => { signals.push(req.signal); return new Promise(r => pending.push(r)) })
    const w = view(); await w.get(`[data-testid="${id}"]`).trigger('click'); auth.userInfo.id = 2; await flushPromises()
    await w.get(`[data-testid="${id}"]`).trigger('click')
    const payload = id === 'sr-load' ? { events: [{ ...event, rule_id: 'new-row' }] } : id === 'sr-rules-load' ? { ...current, config: { rules: [{ ...rule, name: 'new-row' }] } } : id === 'sr-versions-load' ? { versions: [{ ...version, comment: 'new-row' }] } : { target_version: 1, instances: [{ ...instance, instance_id: 'new-row' }] }
    pending[1]!(response(payload)); await flushPromises(); expect(w.text()).toContain('new-row')
    pending[0]!(response(id === 'sr-load' ? { events: [event] } : id === 'sr-rules-load' ? current : id === 'sr-versions-load' ? { versions: [version] } : { target_version: 1, instances: [instance] })); await flushPromises()
    expect(w.text()).toContain('new-row'); expect(signals[0]?.aborted).toBe(true)
  })
  it('四区在途请求独立，降级全取消并清空', async () => { const w = view(); for (const id of ['sr-load', 'sr-rules-load', 'sr-versions-load', 'sr-instances-load']) await click(w, id); setTransport(req => { signals.push(req.signal); return new Promise(() => {}) }); signals = []; for (const id of ['sr-load', 'sr-rules-load', 'sr-versions-load', 'sr-instances-load']) await w.get(`[data-testid="${id}"]`).trigger('click'); expect(new Set(signals).size).toBe(4); auth.role = 'tenant_admin'; await flushPromises(); expect(signals.every(s => s?.aborted)).toBe(true); expect(w.find('[data-testid="sr-rule"]').exists()).toBe(false); expect(w.find('[data-testid="sr-event"]').exists()).toBe(false); expect(w.find('[data-testid="sr-load"]').exists()).toBe(true) })
  it('非法上限不发请求而显示稳定错误', async () => { const w = view(); await w.get('[data-testid="sr-limit"]').setValue('1001'); await click(w, 'sr-load'); expect(calls).toEqual([]); expect(w.get('[data-testid="sr-events-error"]').text()).toBe(zhCN.sr.invalidFilter) })
  it('卸载取消在途请求', async () => { setTransport(req => { signals.push(req.signal); return new Promise(() => {}) }); const w = view(); await w.get('[data-testid="sr-load"]').trigger('click'); w.unmount(); expect(signals[0]?.aborted).toBe(true) })
  it('同区重复点击不并发请求', async () => { setTransport(req => { calls.push(req.path); return new Promise(() => {}) }); const w = view(); const button = w.get('[data-testid="sr-load"]'); await Promise.all([button.trigger('click'), button.trigger('click')]); expect(calls).toHaveLength(1) })
})

describe('脱敏规则：接线、词典与移动触控', () => {
  it('抽屉admin档对tenant与legacy可达，对user不可见', () => { const nav = DRAWER_NAV.find(x => x.key === 'sanitize-rules'); expect(nav).toMatchObject({ to: '/sanitize-rules', titleKey: 'nav.sanitizeRules', requiresRole: 'admin' }); for (const role of ['tenant_admin', 'super_admin', 'admin_key']) expect(navItemsFor(DRAWER_NAV, role)).toContain(nav); expect(navItemsFor(DRAWER_NAV, 'user')).not.toContain(nav) })
  it('路由实际懒加载该视图', () => { const source = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8'); expect(source).toContain("path: '/sanitize-rules'"); expect(source).toContain("component: () => import('@/views/SanitizeRulesView.vue')") })
  it('两语种键集逐键一致且值非空', () => { expect(Object.keys(zhCN.sr).sort()).toEqual(Object.keys(enUS.sr).sort()); for (const dict of [zhCN.sr, enUS.sr]) for (const value of Object.values(dict)) expect(value.trim().length).toBeGreaterThan(0) })
  it('含复核/草稿/发布/回滚写侧、触控至少48px、编码合法', () => { const file = readFileSync(join(process.cwd(), 'src/views/SanitizeRulesView.vue')); const source = file.toString('utf8'); expect(source).toContain('publishSanitizeRules'); expect(source).toContain('rollbackSanitizeRules'); expect(source).toContain('reviewSanitizeRuleEvent'); expect(source).toContain('validateSanitizeRules'); const targets = [...source.matchAll(/min-height:\s*(\d+)px/g)].map(m => Number(m[1])); expect(targets.length).toBeGreaterThan(0); expect(targets.every(n => n >= 48)).toBe(true); expect(file.includes(Buffer.from([239,191,189]))).toBe(false); expect(file.includes(Buffer.from([0]))).toBe(false) })
})
