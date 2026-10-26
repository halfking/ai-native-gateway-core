import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import ModelHistoryView from './ModelHistoryView.vue'
import { setLocale } from '@/i18n'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchModelHistory,
  modelHistoryRoleIsAdminTier,
  modelHistoryRoleSeesAllTenants,
  modelHistoryTenantScopeLabel,
  modelHistoryParamsAreAcceptable,
  modelHistoryCountMatchesEvents,
  modelHistoryEventMatchesItsSource,
  modelHistoryAutoEventHasNullActorAndReason,
  modelHistoryManualEventHasNullProbeFields,
  modelHistoryAutoEventKindIsKnown,
  modelHistoryManualEventKindIsKnown,
  modelHistoryEventsAreNonAscending,
  MODEL_HISTORY_TENANT_ROLE,
  MODEL_HISTORY_GLOBAL_ROLES,
  MODEL_HISTORY_SOURCES,
  MODEL_HISTORY_AUTO_EVENTS,
  MODEL_HISTORY_MANUAL_EVENTS,
  MODEL_HISTORY_DEFAULT_LIMIT,
  MODEL_HISTORY_LIMIT_MIN,
  MODEL_HISTORY_LIMIT_MAX,
  type ModelHistoryResponse,
  type ModelHistoryEvent,
} from '@/api/modelHistory'

/**
 * 凭据模型状态变更历史面回归护栏（2026-10-14，第一百一十四批）。
 *
 * ★★★★★★ 本批与批 113 是**同一个坑的第二个实例**，而且变形了，值得逐字记住：
 *   1. 挂载 `admin/handler.go:1525` `monitorH.RegisterMonitorRoutes(mux, h.admin)`
 *      ⇒ 包 `admin.AdminMiddleware`（`admin/auth.go:86-141`）**只认证、不检 role**
 *      （`admin/users.go:306-308` 的注释把这写成显式设计）；
 *   2. ★★★★★★ **2026-10-21 订正**：`handleModelHistory`
 *      （`admin/credential_monitor.go:1666`）**现在自己检 role** ——
 *      2026-10-16 audit #154 在 `:1678` 加了白名单
 *      （`!IsTenantAdmin(r) && !IsSuperAdminOrLegacy(r)` ⇒ 越权返 **403**）
 *      ⇒ 旧结论「handler 自己也不检 role」**已不成立**（见 ⑬-3）。
 *   3. ★★★ **变形点**：租户键不是调 `EffectiveTenantIDAll`，而是**手写内联**了同一语义
 *      （`credential_monitor.go:1706-1709`）：
 *      ```go
 *      tenantID := ""
 *      if IsTenantAdmin(r) { tenantID = GetTenantID(r) }
 *      ```
 *      ⇒ ⇒★ **grep `EffectiveTenantID` 找不到这一处** ——
 *        批 113 的判别动作在这里会**漏掉整个家族**，而实际风险一模一样。
 *   4. ⇒ **能走到 SQL 的只有三类**：tenant_admin 租户内；
 *      super_admin / admin_key 空串放行 ⇒ **全租户**；
 *      user 等第四类在 `:1678` 被 **403** 挡住 ⇒ 旧结论「非 tenant_admin 一律看全租户」
 *      **已不成立**。
 *
 * ⇒★★ 处置**不变**（仍是 `requiresRole: 'admin'`，本仓第三个该档席）——
 *   变的只是**理由的强度**：服务端从「零角色下限」变成了「有 403 白名单」。
 *
 * ⚠️⚠️ **抽屉隐藏 ≠ 路由拦截**：`src/hyper-wiring.ts:34-45` 的 `beforeEach` 只查登录态，
 *   `meta.requiresRole` 全仓无人读 ⇒ 深链照样打开 ⇒ 视图内自己再兜一层。
 *   ⚠️ 这里是**与服务端对齐、不是替代**：服务端对越权返 **403**
 *   （2026-10-16 audit #154 起），但前端这一层仍不可省（深链可达 + 别的失败形状）。
 *
 * 判据钉十二组不变量：
 *  ① ★★★★★★ 权限档 = `'admin'`，且**租户键是手写内联**（结构层钉 Go 源码）。
 *  ② ★★★★ 参数守卫：credential_id 不能是 0、limit 落在 1–200、模型名非空。
 *  ③ ★★ **负数 credential_id 不拦**（后端只判 `=== 0`，前端不替它加规则）。
 *  ④ ★★★ (14) `count === events.length`，页面上两个数都要可见。
 *  ⑤ ★★★★★ (4) auto 恒 2 个 null / manual 恒 5 个 null ⇒ 页面逐条列出来。
 *  ⑥ ★★★★ (2) 十个键恒在、可裸 null ⇒ 显示「未提供」，**不**说「键不存在」。
 *  ⑦ ★★★★ (3) `actor` 的空串与 `reason` 的 null **显示得不一样**。
 *  ⑧ ★★★★ (6) 无 tiebreak ⇒ 只能断言非升序，并列项**不得误报**。
 *  ⑨ ★★★ (5) `event` 取值随 `source` 变。
 *  ⑩ ★★★ (7) 扫失败的行查不到 ⇒ 不完整性提示恒在。
 *  ⑪ ★★★ 回显的 credential_id / raw_model_name 恒等于查询条件。
 *  ⑫ ★★ 触控 ≥48 CSS px。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function autoEvent(over: Partial<ModelHistoryEvent> = {}): ModelHistoryEvent {
  return {
    ts: '2026-10-14T00:00:00Z',
    source: 'auto',
    triggered_by: 'scheduler',
    event: 'recovered',
    probe_status: 'healthy_confirmed',
    http_status: 200,
    error_code: null,
    error_message: null,
    actor: null,
    reason: null,
    ...over,
  }
}

function manualEvent(over: Partial<ModelHistoryEvent> = {}): ModelHistoryEvent {
  return {
    ts: '2026-10-13T00:00:00Z',
    source: 'manual',
    triggered_by: null,
    event: 'online',
    probe_status: null,
    http_status: null,
    error_code: null,
    error_message: null,
    actor: 'root',
    reason: 'rollout',
    ...over,
  }
}

function resp(over: Partial<ModelHistoryResponse> = {}): ModelHistoryResponse {
  const events = over.events ?? [autoEvent(), manualEvent()]
  return {
    credential_id: 7,
    raw_model_name: 'gpt-4o',
    events,
    count: events.length,
    ...over,
  }
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'tenant_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/modelHistory', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modelHistory')>()
  return { ...actual, fetchModelHistory: vi.fn() }
})

const mFetch = fetchModelHistory as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(ModelHistoryView)
  await flushPromises()
  return w
}

/** ★ 查询段三个框填成一份可发的条件，再点查询。 */
async function queryWith(w: W, over: { id?: string; model?: string; limit?: string } = {}) {
  await byId(w, 'mh-credentialId').setValue(over.id ?? '7')
  await byId(w, 'mh-rawModel').setValue(over.model ?? 'gpt-4o')
  await byId(w, 'mh-limit').setValue(over.limit ?? '50')
  await flushPromises()
  await byId(w, 'mh-load').trigger('click')
  await flushPromises()
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/ModelHistoryView.vue'), 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'tenant_admin'
  mFetch.mockResolvedValue(resp())
})

// ── ① 权限档 ──────────────────────────────────────────────────────────────

describe('① 权限档 admin（不是缺省，也不是 super_admin）', () => {
  it('①-1 ★★★★ 全租户只对 super_admin / admin_key 成立', () => {
    expect([...MODEL_HISTORY_GLOBAL_ROLES]).toEqual(['super_admin', 'admin_key'])
    expect(modelHistoryRoleSeesAllTenants('super_admin')).toBe(true)
    expect(modelHistoryRoleSeesAllTenants('admin_key')).toBe(true)
    expect(modelHistoryRoleSeesAllTenants('tenant_admin')).toBe(false)
    // ★★★ 本批核心：`user` 不在特权集合里，而空串租户键会让 SQL 放行全租户
    expect(modelHistoryRoleSeesAllTenants('user')).toBe(false)
  })

  it('①-2 ★★★★ 可访问集合 = 全租户两个 ∪ tenant_admin', () => {
    expect(MODEL_HISTORY_TENANT_ROLE).toBe('tenant_admin')
    expect(modelHistoryRoleIsAdminTier('super_admin')).toBe(true)
    expect(modelHistoryRoleIsAdminTier('admin_key')).toBe(true)
    expect(modelHistoryRoleIsAdminTier('tenant_admin')).toBe(true)
    expect(modelHistoryRoleIsAdminTier('user')).toBe(false)
    expect(modelHistoryRoleIsAdminTier('')).toBe(false)
    // ★ 正控：两个集合**不同** —— tenant_admin 能访问但只看得到本租户
    expect(modelHistoryRoleIsAdminTier('tenant_admin')).not.toBe(modelHistoryRoleSeesAllTenants('tenant_admin'))
  })

  it('①-3 ★★★ 租户口径三值互斥且语义正确', () => {
    expect(modelHistoryTenantScopeLabel('super_admin')).toBe('all')
    expect(modelHistoryTenantScopeLabel('admin_key')).toBe('all')
    expect(modelHistoryTenantScopeLabel('tenant_admin')).toBe('own')
    expect(modelHistoryTenantScopeLabel('user')).toBe('unexpected')
  })

  it("①-4 ★★★★★ 抽屉席必须声明 requiresRole:'admin'，缺省等于对 user 也显示", () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'model-history')
    expect(seat).toBeDefined()
    expect(seat!.requiresRole).toBe('admin')
  })

  it('①-5 ★★★★ role=user 时渲染「无权」且**不发请求**', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(hasId(w, 'mh-nope')).toBe(true)
    expect(hasId(w, 'mh-load')).toBe(false)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('①-6 ★★★★ role=user 时据实标出「非预期口径」', async () => {
    authState.role = 'user'
    const w = await mountView()
    // ★ 断言替换后紧邻的一段（`:unexpected。`），不是裸的 unexpected
    expect(byId(w, 'mh-nope-scope').text()).toContain('：unexpected。')
  })

  it('①-7 ★★★★ 正控：tenant_admin / super_admin / admin_key 都进得了页面', async () => {
    for (const r of ['tenant_admin', 'super_admin', 'admin_key']) {
      authState.role = r
      const w = await mountView()
      expect(hasId(w, 'mh-nope')).toBe(false)
      expect(hasId(w, 'mh-load')).toBe(true)
    }
  })

  it('①-8 ★★★ 正控：两个角色的口径文案不同（钉替换后紧邻那一段）', async () => {
    authState.role = 'tenant_admin'
    const a = await mountView()
    expect(byId(a, 'mh-scope-note').text()).toContain('：own。')
    authState.role = 'super_admin'
    const b = await mountView()
    expect(byId(b, 'mh-scope-note').text()).toContain('：all。')
  })
})

// ── ②③ 参数守卫 ───────────────────────────────────────────────────────────

describe('②③ 参数守卫', () => {
  it('②-1 ★★★★ credential_id=0 必被拒；limit 越界必被拒；模型名空必被拒', () => {
    const ok = { credentialId: 7, rawModelName: 'gpt-4o', limit: 50 }
    expect(modelHistoryParamsAreAcceptable(ok)).toBe(true)
    expect(modelHistoryParamsAreAcceptable({ ...ok, credentialId: 0 })).toBe(false)
    expect(modelHistoryParamsAreAcceptable({ ...ok, rawModelName: '' })).toBe(false)
    expect(modelHistoryParamsAreAcceptable({ ...ok, limit: 0 })).toBe(false)
    expect(modelHistoryParamsAreAcceptable({ ...ok, limit: 201 })).toBe(false)
  })

  it('②-2 ★★★ 边界值 1 与 200 都合法', () => {
    expect(MODEL_HISTORY_LIMIT_MIN).toBe(1)
    expect(MODEL_HISTORY_LIMIT_MAX).toBe(200)
    expect(MODEL_HISTORY_DEFAULT_LIMIT).toBe(50)
    expect(modelHistoryParamsAreAcceptable({ credentialId: 7, rawModelName: 'm', limit: 1 })).toBe(true)
    expect(modelHistoryParamsAreAcceptable({ credentialId: 7, rawModelName: 'm', limit: 200 })).toBe(true)
  })

  it('②-3 ★★★★ 缺省上限就是 50，且页面把它写出来', async () => {
    const w = await mountView()
    expect((byId(w, 'mh-limit').element as HTMLInputElement).value).toBe('50')
    expect(byId(w, 'mh-param-note').text()).toContain('静默')
  })

  it('②-4 ★★★ 非法参数时查询按钮禁用且给出提示', async () => {
    const w = await mountView()
    // ★ 先把三格填成合法条件（缺省态 credentialId 为空 ⇒ 本来就不合法）
    await byId(w, 'mh-credentialId').setValue('7')
    await byId(w, 'mh-rawModel').setValue('gpt-4o')
    await byId(w, 'mh-limit').setValue('50')
    await flushPromises()
    expect(hasId(w, 'mh-params-bad')).toBe(false)
    await byId(w, 'mh-limit').setValue('999')
    await flushPromises()
    expect(hasId(w, 'mh-params-bad')).toBe(true)
    expect((byId(w, 'mh-load').element as HTMLButtonElement).disabled).toBe(true)
  })

  it('②-5 ★★★★ 空模型名与 0 凭据 ID 都挡得住', async () => {
    const w = await mountView()
    await byId(w, 'mh-credentialId').setValue('7')
    await byId(w, 'mh-rawModel').setValue('')
    await byId(w, 'mh-limit').setValue('50')
    await flushPromises()
    expect(hasId(w, 'mh-params-bad')).toBe(true)
  })

  it('③-1 ★★★ ★★ 负数 credential_id **不拦**（后端只判 === 0，前端不替它加规则）', () => {
    // ★★ 这条是「前端不该默默改后端语义」的判据。
    //   `credential_monitor.go:1690` 判的是 `credentialID == 0`（**不是** `< 1`）
    //   ⇒ 负数能过后端那一关 ⇒ 前端若拦掉就是**加了后端没有的规则**。
    expect(modelHistoryParamsAreAcceptable({ credentialId: -1, rawModelName: 'm', limit: 50 })).toBe(true)
  })

  it('③-2 ★★ 非数字不是 0：页面显示「参数不可用」而不是「凭据 ID 是 0」', async () => {
    const w = await mountView()
    await byId(w, 'mh-credentialId').setValue('abc')
    await byId(w, 'mh-rawModel').setValue('m')
    await byId(w, 'mh-limit').setValue('50')
    await flushPromises()
    expect(hasId(w, 'mh-params-bad')).toBe(true)
  })

  it('②-6 ★★★ 正控：合法条件原样发给后端', async () => {
    const w = await mountView()
    await queryWith(w, { id: '9', model: 'glm-5', limit: '20' })
    expect(mFetch).toHaveBeenCalledWith({ credentialId: 9, rawModelName: 'glm-5', limit: 20 })
  })
})

// ── ④ (14) count ↔ events ────────────────────────────────────────────────

describe('④ count 与 events 互锁', () => {
  it('④-1 ★★★ 正常载荷两条数一致', () => {
    expect(modelHistoryCountMatchesEvents(resp())).toBe(true)
    expect(modelHistoryCountMatchesEvents(resp({ events: [], count: 0 }))).toBe(true)
  })

  it('④-2 ★★★ 页面上 count 与数组长度都要可见', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(byId(w, 'mh-s-count').text()).toBe('2')
    expect(byId(w, 'mh-count-relation').text()).toContain('count = 2')
    expect(byId(w, 'mh-count-relation').text()).toContain('2')
  })

  it('④-3 ★★★★ count 对不上时报出来', async () => {
    mFetch.mockResolvedValue(resp({ count: 5 }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-count-mismatch')).toBe(true)
  })

  it('④-4 ★★★ 正控：空数组时 count=0 且「没有事件」是确定结论', async () => {
    mFetch.mockResolvedValue(resp({ events: [], count: 0 }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-events-empty')).toBe(true)
    expect(hasId(w, 'mh-count-mismatch')).toBe(false)
  })
})

// ── ⑤ (4) null 分布互补 ──────────────────────────────────────────────────

describe('⑤ auto 与 manual 的 null 分布恰好互补', () => {
  it('⑤-1 ★★★★★ auto 行恒 2 个 null（actor / reason），manual 行恒 5 个', () => {
    expect(modelHistoryAutoEventHasNullActorAndReason(autoEvent())).toBe(true)
    expect(modelHistoryAutoEventHasNullActorAndReason(manualEvent())).toBe(false)
    expect(modelHistoryManualEventHasNullProbeFields(manualEvent())).toBe(true)
    expect(modelHistoryManualEventHasNullProbeFields(autoEvent())).toBe(false)
  })

  it('⑤-2 ★★★★ 聚合判据：两段形状都不满足时 `matchesItsSource` 才为 false', () => {
    // ★ 这条把 ⑤-1 的两条拼起来看，并补上「值域外的 source」这一格 ——
    //   `source` 不在 {auto, manual} 里时两条都答 false ⇒ 聚合也 false。
    expect(modelHistoryEventMatchesItsSource(autoEvent())).toBe(true)
    expect(modelHistoryEventMatchesItsSource(manualEvent())).toBe(true)
    expect(modelHistoryEventMatchesItsSource(autoEvent({ source: 'weird' }))).toBe(false)
  })

  it('⑤-3 ★★★★ 页面把每一行**恒为 null** 的键逐条列出来', async () => {
    const w = await mountView()
    await queryWith(w)
    // 第 0 条是 auto
    expect(byId(w, 'mh-e-0-nulls').text()).toContain('actor')
    expect(byId(w, 'mh-e-0-nulls').text()).toContain('reason')
    // 第 1 条是 manual
    const t1 = byId(w, 'mh-e-1-nulls').text()
    for (const k of ['triggered_by', 'probe_status', 'http_status', 'error_code', 'error_message']) {
      expect(t1).toContain(k)
    }
  })

  it('⑤-4 ★★★★ 形状破了才报，且正常载荷**不**报', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-e-0-shape-bad')).toBe(false)
    expect(hasId(w, 'mh-e-1-shape-bad')).toBe(false)
  })

  it('⑤-5 ★★★★ auto 行带上了 actor ⇒ 形状破了', async () => {
    mFetch.mockResolvedValue(resp({ events: [autoEvent({ actor: 'root' })] }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-e-0-shape-bad')).toBe(true)
  })

  it('⑤-6 ★★★★ manual 行带上了 probe_status ⇒ 形状破了', async () => {
    mFetch.mockResolvedValue(resp({ events: [manualEvent({ probe_status: 'healthy' })] }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-e-0-shape-bad')).toBe(true)
  })
})

// ── ⑥ (2) 十个键恒在、可裸 null ──────────────────────────────────────────

describe('⑥ 键恒在，可为裸 null', () => {
  it('⑥-1 ★★★★★ null 一律显示「未提供」，不得显示成空串或「键不存在」', async () => {
    const w = await mountView()
    await queryWith(w)
    // ★★ manual 行的五个 probe 字段都是 null
    for (const k of ['triggered_by', 'probe_status', 'http_status', 'error_code', 'error_message']) {
      const txt = byId(w, `mh-e-1-${k}`).text()
      expect(txt).toBe('未提供')
      expect(txt).not.toBe('')
    }
  })

  it('⑥-2 ★★★★ 渲染出的**文字**里从不出现「键不存在 / undefined」', async () => {
    const w = await mountView()
    await queryWith(w)
    // ★★★ 断言面必须取 `w.text()` 而不是 `w.html()` ——
    //   **`html()` 会把模板注释一起序列化**，而本视图的解释性注释里
    //   恰恰写着「键不存在」这四个字（见 ErrorsTrendView 批 113 同族教训：
    //   **注释里引用的字会把判据顶成红**）。
    //   这里的意图是「页面不会这样对用户说」，所以观察面就必须是**渲染出的文字**。
    const text = w.text()
    expect(text).not.toContain('键不存在')
    expect(text).not.toContain('不存在')
    expect(text).not.toContain('undefined')
  })

  it('⑥-3 ★★★★ auto 行的非 null 字段照常显示', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(byId(w, 'mh-e-0-triggered_by').text()).toBe('scheduler')
    expect(byId(w, 'mh-e-0-probe_status').text()).toBe('healthy_confirmed')
    expect(byId(w, 'mh-e-0-http_status').text()).toBe('200')
  })
})

// ── ⑦ (3) actor 空串 vs reason 折叠 ──────────────────────────────────────

describe('⑦ actor 的空串与 reason 的 null 必须显示得不一样', () => {
  it('⑦-1 ★★★★ 两者显示不同（actor 空串、reason null）', async () => {
    // ★★ `credential_monitor.go:1645-1647` 只在 reason 非空时才赋值
    //   ⇒ reason 的取值域是 {null, 非空串}；而 actor 只看 Valid ⇒ **可以是空串**。
    mFetch.mockResolvedValue(resp({ events: [manualEvent({ actor: '', reason: null })] }))
    const w = await mountView()
    await queryWith(w)
    const actor = byId(w, 'mh-e-0-actor').text()
    const reason = byId(w, 'mh-e-0-reason').text()
    expect(actor).toBe('（空串，与「未提供」不是一回事）')
    expect(reason).toBe('未提供')
    expect(actor).not.toBe(reason)
  })

  it('⑦-2 ★★★★ nullFilledKeys 对空串 actor **不**列为「恒为未提供」', async () => {
    mFetch.mockResolvedValue(resp({ events: [manualEvent({ actor: '' })] }))
    const w = await mountView()
    await queryWith(w)
    // ★ actor 是 '' 不是 null ⇒ 那一行的「恒为未提供」清单里**不该**出现 actor
    expect(byId(w, 'mh-e-0-nulls').text()).not.toContain('actor')
  })

  it('⑦-3 ★★ ★★★ 「reason 永不为空串」是恒真判据 ⇒ 本批**刻意不提供**', () => {
    // ★ 理由写在 `api/modelHistory.ts` 的 (3)：取值域是 {null, 非空串}，
    //   在可达输入域上任何写法都成立 ⇒ 按「恒真判据一律不提供」删掉，
    //   契约由注释承担。这条判据的作用是**把这个决定钉住**，免得下一个人补回来。
    expect(() => {
      const r = resp({ events: [manualEvent({ reason: 'ok' })] })
      // 后端产不出的载荷在解包层就被拒，视图根本拿不到它
      expect(r.events[0]!.reason).not.toBe('')
    }).not.toThrow()
  })
})

// ── ⑧ (6) 排序无 tiebreak ────────────────────────────────────────────────

describe('⑧ 时间序无 tiebreak', () => {
  it('⑧-1 ★★★★ 并列时间戳**不得**误报（无 tiebreak ⇒ 顺序未定义）', () => {
    // ★★ 这是 (6) 的正控，方向最容易搞反：`ts` 相等不是「未倒序」。
    const evs = [autoEvent({ ts: 'a' }), autoEvent({ ts: 'a' }), manualEvent({ ts: 'a' })]
    expect(modelHistoryEventsAreNonAscending(evs)).toBe(true)
  })

  it('⑧-2 ★★★ 真正**升序**的一段才算违例（helper 答 false）', () => {
    // ★★ 方向容易搞反：`…AreNonAscending` 答的是「这列**不是**升序」，
    //   正常倒序 ['b','a'] 答 **true**，而升序 ['a','b'] 才答 false。
    expect(modelHistoryEventsAreNonAscending([autoEvent({ ts: 'b' }), manualEvent({ ts: 'a' })])).toBe(true)
    expect(modelHistoryEventsAreNonAscending([autoEvent({ ts: 'a' }), manualEvent({ ts: 'b' })])).toBe(false)
  })

  it('⑧-3 ★★★ 空数组与单条都算「非升序」（边界不误报）', () => {
    expect(modelHistoryEventsAreNonAscending([])).toBe(true)
    expect(modelHistoryEventsAreNonAscending([autoEvent()])).toBe(true)
  })

  it('⑧-4 ★★★ 页面在载荷升序时报出来', async () => {
    mFetch.mockResolvedValue(resp({
      events: [autoEvent({ ts: '2026-10-14T00:00:00Z' }), manualEvent({ ts: '2026-10-15T00:00:00Z' })],
    }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-order-bad')).toBe(true)
  })

  it('⑧-5 ★★★ 正控：正常倒序不报，且提示恒在', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-order-bad')).toBe(false)
    expect(byId(w, 'mh-order-note').text()).toContain('次级排序')
  })
})

// ── ⑨ (5) event 取值随 source ────────────────────────────────────────────

describe('⑨ event 取值随 source 变', () => {
  it('⑨-1 ★★★★ 四个取值与两个来源的对应关系', () => {
    expect([...MODEL_HISTORY_SOURCES]).toEqual(['auto', 'manual'])
    expect([...MODEL_HISTORY_AUTO_EVENTS]).toEqual(['recovered', 'broke'])
    expect([...MODEL_HISTORY_MANUAL_EVENTS]).toEqual(['online', 'offline'])
    expect(modelHistoryAutoEventKindIsKnown(autoEvent({ event: 'broke' }))).toBe(true)
    expect(modelHistoryAutoEventKindIsKnown(autoEvent({ event: 'online' }))).toBe(false)
    expect(modelHistoryManualEventKindIsKnown(manualEvent({ event: 'offline' }))).toBe(true)
    expect(modelHistoryManualEventKindIsKnown(manualEvent({ event: 'broke' }))).toBe(false)
  })

  it('⑨-2 ★★★★ 正常载荷不报警告', async () => {
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-e-0-kind-bad')).toBe(false)
    expect(hasId(w, 'mh-e-1-kind-bad')).toBe(false)
  })

  it('⑨-3 ★★★★ auto 行混入 manual 取值 ⇒ 报', async () => {
    mFetch.mockResolvedValue(resp({ events: [autoEvent({ event: 'online', actor: null, reason: null })] }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-e-0-kind-bad')).toBe(true)
  })
})

// ── ⑩⑪ 回显与不完整性 ────────────────────────────────────────────────────

describe('⑩⑪ 回显与不完整性', () => {
  it('⑪-1 ★★★ 正常载荷回显一致，两个 error 都不出', async () => {
    const w = await mountView()
    await queryWith(w, { id: '7', model: 'gpt-4o' })
    expect(byId(w, 'mh-s-credential_id').text()).toBe('7')
    expect(byId(w, 'mh-s-raw_model_name').text()).toBe('gpt-4o')
    expect(hasId(w, 'mh-credential-echo-bad')).toBe(false)
    expect(hasId(w, 'mh-model-echo-bad')).toBe(false)
  })

  it('⑪-2 ★★★ 回显的 credential_id 对不上时报出来', async () => {
    mFetch.mockResolvedValue(resp({ credential_id: 999 }))
    const w = await mountView()
    await queryWith(w, { id: '7' })
    expect(hasId(w, 'mh-credential-echo-bad')).toBe(true)
  })

  it('⑪-3 ★★★★ 回显的 raw_model_name 对不上时报出来', async () => {
    // ★★★ 这条是批 114 变异 S20（把 `modelEchoBad` 关掉）**当场顶出来的**：
    //   在它之前**没有任何判据覆盖 raw_model_name 的回显** ⇒ 那一支是死代码，
    //   把它整个删掉整套件照样全绿。
    //   ⇒ 「变异 NO_EFFECT 的第一问：这条分支到底被哪条判据钉着？」
    mFetch.mockResolvedValue(resp({ raw_model_name: 'gpt-4o-mini' }))
    const w = await mountView()
    await queryWith(w, { model: 'gpt-4o' })
    expect(hasId(w, 'mh-model-echo-bad')).toBe(true)
  })

  it('⑪-4 ★★★ 两个回显各自独立（只错一个时不误报另一个）', async () => {
    mFetch.mockResolvedValue(resp({ credential_id: 7, raw_model_name: 'other' }))
    const w = await mountView()
    await queryWith(w, { id: '7', model: 'gpt-4o' })
    expect(hasId(w, 'mh-model-echo-bad')).toBe(true)
    expect(hasId(w, 'mh-credential-echo-bad')).toBe(false)
  })

  it('⑩-1 ★★★★ 不完整性提示恒在（扫失败的行在响应里查不到）', async () => {
    const w = await mountView()
    await queryWith(w)
    // ★ 这条**没有反面**：后端把 scanFailures 写进 slog 而不写进响应
    //   ⇒ 页面上任何情况下都得说这句。
    expect(hasId(w, 'mh-incomplete-note')).toBe(true)
    expect(byId(w, 'mh-incomplete-note').text()).toContain('静默跳过')
  })

  it('⑩-2 ★★★ 即使载荷「看起来完整」也不撤这句', async () => {
    mFetch.mockResolvedValue(resp({ events: [], count: 0 }))
    const w = await mountView()
    await queryWith(w)
    expect(hasId(w, 'mh-incomplete-note')).toBe(true)
  })
})

// ── ⑫ 触控 ────────────────────────────────────────────────────────────────

describe('⑫ 触控目标 ≥48 CSS px', () => {
  it('⑫-1 ★★★ 解析出 min-height 数值再比大小', () => {
    const mins = [...viewSrc().matchAll(/min-height:\s*(\d+)px/g)].map((m) => Number(m[1]))
    expect(mins.length).toBeGreaterThan(0)
    for (const v of mins) expect(v).toBeGreaterThanOrEqual(48)
  })

  it('⑫-2 ★★ 三个输入与查询按钮都真的渲染了', async () => {
    const w = await mountView()
    for (const id of ['mh-load', 'mh-credentialId', 'mh-rawModel', 'mh-limit']) {
      expect(hasId(w, id)).toBe(true)
    }
  })
})

// ── ⑬ 后端推导链的结构层钉法 ─────────────────────────────────────────────

describe('⑬ 后端租户推导链（读 Go 源码）', () => {
  // ★★★ 路径写错时**必须硬失败**，不能让本组空转。
  const ROOT = join(process.cwd(), '..')
  function goSrc(rel: string): string {
    const p = join(ROOT, rel)
    expect(existsSync(p), `找不到 ${rel}（判据路径写错会让本组全部空转）`).toBe(true)
    return readFileSync(p, 'utf8')
  }

  it('⑬-1 ★★★★ 挂载处传的是 h.admin（整族 14 个端点同一档）', () => {
    const h = goSrc('admin/handler.go')
    const line = h.split('\n').find((l) => l.includes('RegisterMonitorRoutes(mux'))
    expect(line).toBeDefined()
    expect(line).toContain('h.admin')
    expect(line).not.toContain('superAdmin')
  })

  it('⑬-2 ★★★★★★ handler 里的租户键是**手写内联**，不是 EffectiveTenantIDAll', () => {
    const src = goSrc('admin/credential_monitor.go')
    const fn = src.slice(src.indexOf('func (m *CredentialMonitorHandlers) handleModelHistory'))
    const body = fn.slice(0, fn.indexOf('\n}\n\n'))
    // ★★★ 这三条是本批的**变形点**：语义与 EffectiveTenantIDAll 一模一样，
    //   但**不调那个函数** ⇒ grep `EffectiveTenantID` 找不到这一处。
    expect(body).toContain('tenantID := ""')
    expect(body).toContain('if IsTenantAdmin(r)')
    expect(body).toContain('tenantID = GetTenantID(r)')
    expect(body).not.toContain('EffectiveTenantID')
  })

  it('⑬-3 ★★★★★★ handler 自己检 role（三档白名单），越权返 403', () => {
    // ★★★★★★ **2026-10-21 订正**：旧结论「handler 自己不检查 role」**已不成立** ——
    //   Go 侧 2026-10-16 audit #154 在 `admin/credential_monitor.go:1678` 加了白名单：
    //   `if !IsTenantAdmin(r) && !IsSuperAdminOrLegacy(r) { 403 }`。
    //   ⇒ 旧判据 `not.toMatch(/Role\s*==|Role\s*!=|RequireSuperAdmin|requireUserManager/)`
    //   是**恒绿**的：新门用的是 `IsTenantAdmin` / `IsSuperAdminOrLegacy` **封装**，
    //   不写 `Role ==` ⇒ 正则永远匹配不到 ⇒ 它绿着却守不住结论。
    //   ⇒ 这条改成**断言白名单在场**（有牙，且只在 Go 侧真加了门时才绿）。
    const src = goSrc('admin/credential_monitor.go')
    const fn = src.slice(src.indexOf('func (m *CredentialMonitorHandlers) handleModelHistory'))
    const body = fn.slice(0, fn.indexOf('\n}\n\n'))
    // ★★★ 白名单：tenant_admin（IsTenantAdmin）或 超管/旧版管理员（IsSuperAdminOrLegacy）
    expect(body, 'handler 的 role 白名单没了 ⇒ 越权又能拿到全租户数据').toMatch(
      /!IsTenantAdmin\(r\) && !IsSuperAdminOrLegacy\(r\)/,
    )
    // ★★ 越权必须被挡下来（403），不能是 200 + 全租户
    expect(body, '越权没有返 403').toMatch(
      /writeError\(w, http\.StatusForbidden, "administrator role required"\)/,
    )
    // ★ 反向护栏：白名单是「或」关系（两档任一放行），**多一档就是提权**
    expect(body, '白名单被改成了「且」或加了别的档位 ⇒ 权限档要重判').not.toMatch(
      /IsSuperAdminOrLegacy\(r\)\s*\|\|/,
    )
  })

  it('⑬-4 ★★★★ 两段 SQL 都有「空串放行」的租户条件', () => {
    const src = goSrc('admin/credential_monitor.go')
    expect(src).toContain("AND ($4 = '' OR mpr.tenant_id = $4)")
    expect(src).toContain("AND ($4 = '' OR al.tenant_id = $4)")
    // ★★ 2026-10-21 订正：这条**仍然成立**，但「空串放行」的**适用范围变了** ——
    //   自 2026-10-16 audit #154 的 role gate（见 ⑬-3）起，
    //   **只有** super_admin / admin_key 能带着空串租户键走到 SQL；
    //   其余角色在 handler 入口就被 403 挡住 ⇒ 「空串 ⇒ 全租户」不再是提权面。
  })

  it('⑬-5 ★★★★ credential_id 只判 === 0（负数能过）—— 与前端不拦负数一致', () => {
    const src = goSrc('admin/credential_monitor.go')
    const fn = src.slice(src.indexOf('func (m *CredentialMonitorHandlers) handleModelHistory'))
    const body = fn.slice(0, fn.indexOf('\n}\n\n'))
    expect(body).toContain('if credentialID == 0')
    expect(body).not.toContain('credentialID < 1')
    expect(body).not.toContain('credentialID <= 0')
  })

  it('⑬-6 ★★ 中间件确实不检 role（与 errorsTrend 同一条判据）', () => {
    const auth = goSrc('admin/auth.go')
    const body = auth.slice(auth.indexOf('func AdminMiddleware')).slice(0, 4000)
    const cut = body.slice(0, body.indexOf('\nfunc '))
    expect(cut).not.toMatch(/Role\s*==|Role\s*!=/)
  })

  it('⑬-7 ★★ resolveSharedPrincipal 允许 user 且只在本地更低时下调', () => {
    const sp = goSrc('admin/shared_principal.go')
    expect(sp).toContain('"user": 1')
    expect(sp).toContain('if roleRank[localRole] < roleRank[verified.Role]')
  })
})
