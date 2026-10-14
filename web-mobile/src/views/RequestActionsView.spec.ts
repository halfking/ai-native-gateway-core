import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import RequestActionsView from './RequestActionsView.vue'
import { setLocale, t } from '@/i18n'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchRequestActions,
  requestActionsRoleIsAllowed,
  requestActionsUnavailableReason,
  requestActionsIsMissingId,
  requestActionsMatchesRequest,
  requestActionsRowsEchoRequestId,
  actionExtraKeys,
  actionIsKnown,
  GO_ZERO_TIME,
  LIVE_ACTIONS_NOT_WIRED_MESSAGE,
  LIVE_ACTIONS_UNAVAILABLE_MESSAGE,
  MISSING_REQUEST_ID_MESSAGE,
  type RequestActionsResponse,
  type RequestActionEntry,
} from '@/api/requestActions'

/**
 * 请求动作时间线面回归护栏（2026-10-14，第一百一十八批）。
 *
 * ★★★★★★ **权限档是 `'super_admin'`，成因与批 113/114 相反、与批 115/117 同族。**
 *   批 113/114 是「服务端**有**租户维度、只是租户键可能返空串」⇒ 用 `'admin'`；
 *   本批是**第三类**：服务端**完全没有租户维度**。
 *   四处 + 一处**对照**（① 组逐字断言）：
 *   ① 注册处 `admin/handler.go:1045` 传 `admin(...)` ⇒ `AdminMiddleware`（只认证不检 role）；
 *   ② `admin/request_actions.go` 全文没有 `AuthContext` / `claims.Role` / `IsTenantAdmin`；
 *   ③ 唯一过滤是 `:62` 的 `request_id` 精确比较，而它来自 **URL 路径段**，
 *      由 `middleware/requestid_mw.go:83-89` 生成（crypto/rand 16 字节 hex，不含租户信息）；
 *   ④ ★★ **对照**：读**同一个** store 的 `/api/admin/live-stream` 在 hub 内算了租户作用域
 *      （`live_stream_sse.go:2263-2268`）并逐帧过滤（`:1875-1882`）
 *      ⇒ 同一份数据、同一个注册包装，**一边过滤一边不过滤** ⇒ 后端不一致，不是设计。
 *
 * ★★★★ **本页是移动端独有的入口**：这个模块全仓零调用方，
 *   桌面端靠 SSE 实时流看动作，刷新后没有回放 ⇒ 本页补的是「刷新后还能看」。
 *
 * 判据钉十一组：
 *  ① ★★★★★★ 权限档（四处源码断言 + 深链兜底**连请求都不发**）。
 *  ② ★★★★★ 回显两半：顶层 id vs 请求 id、**逐行** id vs 顶层 id。
 *  ③ ★★★★★ `count` 是服务端算的 `len(actions)`。
 *  ④ ★★★★ `detail` 键**永远不该出现**（后端已摊平）。
 *  ⑤ ★★★★ `credential_label` 在本端点**永远不该出现**（labels 传 nil）。
 *  ⑥ ★★★★ 排序：按 `(ts, seq)` 升序；`seq` 唯一（后端去重）。
 *  ⑦ ★★★★★ `action` 是**开放字符串**：未知取值**照样显示**，不是异常。
 *  ⑧ ★★★★ 摊平上来的额外键必须显示出来。
 *  ⑨ ★★★★ 零值 `ts` 单独提示（它会排到最前，顺序看着反常）。
 *  ⑩ ★★★★ 错误归因：502 / 503 / 400 三种**完全可区分**；失败后清空陈旧。
 *  ⑪ ★★★ 触控 ≥48 CSS px、路由/抽屉/i18n 接线、编码门。
 */

// ── 夹具：守住模块头写明的不变式 ────────────────────────────────────────────

/** 一条已解包形态的动作行。`ts` 必须让全表升序成立。 */
function row(seq: number, action: string, ts: string, over: Partial<RequestActionEntry> = {}): RequestActionEntry {
  return { request_id: 'req-abc', seq, action, ts, ...over }
}

/** 一份自洽响应：三行、`ts` 升序、`seq` 唯一、每行回显、count 等于长度。 */
function okResp(over: Partial<RequestActionsResponse> = {}): RequestActionsResponse {
  const actions = [
    row(1, 'arrive', '2026-10-14T01:00:00Z'),
    row(2, 'route_resolved', '2026-10-14T01:00:01Z', { model: 'gpt-4o', credential_id: 42 }),
    row(3, 'upstream_request', '2026-10-14T01:00:02Z'),
  ]
  return { request_id: 'req-abc', actions, count: actions.length, ...over }
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/requestActions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/requestActions')>()
  return { ...actual, fetchRequestActions: vi.fn() }
})

const mFetch = fetchRequestActions as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(RequestActionsView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}
function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}
/** ★★ 文本断言一律用 `text()`，**不用** `html()` —— 后者会序列化模板注释。 */
function textOf(w: W, id: string): string {
  return byId(w, id).text()
}
function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/RequestActionsView.vue'), 'utf8')
}
/** ★ 读源码前先让路径写错**硬失败**，否则 expect 落在别处很难查。 */
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}

/** 填请求 ID 并点读取。 */
async function loadWith(w: W, id = 'req-abc') {
  const el = byId(w, 'ra-request-id').element as HTMLInputElement
  el.value = id
  await byId(w, 'ra-request-id').setValue(id)
  await byId(w, 'ra-load').trigger('click').then(() => flushPromises())
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mFetch.mockResolvedValue(okResp())
})

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档 super_admin（第三类：服务端没有租户维度）', () => {
  it('①-1 ★★★★★★ 只有 super_admin 能进', () => {
    expect(requestActionsRoleIsAllowed('super_admin')).toBe(true)
    expect(requestActionsRoleIsAllowed('tenant_admin')).toBe(false)
    expect(requestActionsRoleIsAllowed('user')).toBe(false)
    expect(requestActionsRoleIsAllowed('')).toBe(false)
  })

  it('①-2 ★★★★★ 抽屉席存在且是 super_admin 档', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'request-actions')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/request-actions')
    expect(seat!.requiresRole).toBe('super_admin')
  })

  it('①-3 ★★★★★★ 读 Go 源码：注册处**逐字**是 admin(...)（不是 superAdmin）', () => {
    const src = goSrc('admin/handler.go')
    // ★★ 钉住注册处本身的形态：哪天改成 h.superAdmin，①-1/①-2 仍全绿（它们只看前端档位）。
    expect(src).toContain('mux.HandleFunc("/api/admin/requests/{id}/actions", admin(h.handleRequestActions))')
    expect(src).not.toContain('mux.HandleFunc("/api/admin/requests/{id}/actions", h.superAdmin(')
  })

  it('①-4 ★★★★★★ 读 Go 源码：handler 既不检 role、也不读 AuthContext、也没有租户条件', () => {
    const src = goSrc('admin/request_actions.go')
    expect(src).not.toMatch(/claims\.Role|IsSuperAdmin|IsTenantAdmin|AuthContext/)
    // ★★ 「完全没有租户维度」的正向钉法：唯一的过滤是 request_id 精确比较。
    expect(src).toContain('if !ok || ev.RequestID != requestID {')
    expect(src).not.toMatch(/tenant|Tenant/)
  })

  it('①-5 ★★★★★★ 读 Go 源码：那个 request_id 不含租户信息（crypto/rand 全局随机）', () => {
    const src = goSrc('middleware/requestid_mw.go')
    // ★ 这一条是「为什么 request_id 不能当租户键」的**事实侧**依据。
    expect(src).toContain('func generateRequestID() string')
    expect(src).toContain('rand.Read(b)')
    expect(src).toContain('hex.EncodeToString(b)')
  })

  it('①-6 ★★★★★★★★ 读 Go 源码：**姊妹端点有**租户维度（本批权限判定的决定性对照）', () => {
    // ★★★★ 这是本批最硬的一条证据：同一个 store、同一个注册包装，SSE 那边**过滤**，
    //   REST 回退这边**不过滤** ⇒ 所以它归「后端不一致」，而不是「设计上就是全局的」。
    const reg = goSrc('admin/handler.go')
    expect(reg).toContain('mux.HandleFunc("/api/admin/live-stream", admin(h.liveStreamHub.HandleLiveStream))')
    const sse = goSrc('admin/live_stream_sse.go')
    // hub 入口算作用域：tenant_admin ⇒ 取 tenant_id 且 isSuper=false
    expect(sse).toContain('if IsTenantAdmin(r) {')
    expect(sse).toContain('tenantID = GetTenantID(r)')
    // 扇出时逐帧过滤：非超管客户端只在 Request.TenantID 相等时收帧
    expect(sse).toContain('return env.Request.TenantID == c.tenantID')
  })

  it('①-7 ★★★★★ 深链兜底：非超管**连请求都不发**', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(hasId(w, 'ra-nope')).toBe(true)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('①-7b ★★★★★★ 兜底段下**没有取数入口**（深链进来就点不着）', async () => {
    // ★★★ 本页**不自动加载** ⇒ 只断言「挂载时没发请求」是**恒真**的半个判据
    //   （批 116 的 S4、批 117 的 S29 都是同一个病）。真正的风险面是
    //   「兜底段有没有把取数入口留在屏上」。
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(hasId(w, 'ra-load')).toBe(false)
    expect(hasId(w, 'ra-request-id')).toBe(false)
    expect(w.findAll('button').length).toBe(0)
    expect(w.findAll('input').length).toBe(0)
  })

  it('①-7c ★★★★ 超管档下取数入口**都在**（与 ①-7b 成对的阴性对照）', async () => {
    const w = await mountView()
    expect(hasId(w, 'ra-load')).toBe(true)
    expect(hasId(w, 'ra-request-id')).toBe(true)
  })

  it('①-8 ★★★★ 兜底段要说清「是前端拦的」，且**不**谎称后端 403', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(textOf(w, 'ra-nope-msg')).toBe(t('ra.nope.msg'))
    expect(textOf(w, 'ra-nope-why')).toBe(t('ra.nope.why'))
    // ★★ 本族**没有** forbidden 分支（后端没有 role 门）⇒ 代码形态上不许有。
    const zh = readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')
    expect(zh).not.toMatch(/ra: \{[\s\S]*?forbidden:/)
  })

  it('①-9 ★★★ 超管**不**看到兜底段', async () => {
    const w = await mountView()
    expect(hasId(w, 'ra-nope')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② 回显两半
// ══════════════════════════════════════════════════════════════════════════

describe('② 回显两半：顶层 vs 请求、逐行 vs 顶层', () => {
  it('②-1 ★★★★★ 正常响应：顶层回显与请求一致，不报红', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-echo-bad')).toBe(false)
    expect(hasId(w, 'ra-row-echo-bad')).toBe(false)
    expect(textOf(w, 'ra-echo')).toBe('req-abc')
  })

  it('②-2 ★★★★★ 顶层回显**换人** ⇒ 报错', async () => {
    mFetch.mockResolvedValue(okResp({ request_id: 'req-OTHER' }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-echo-bad')).toBe(true)
  })

  it('②-3 ★★★★★★ **只有一行**的 id 不对 ⇒ 逐行那条必须咬住（顶层是对的）', async () => {
    // ★★★★ 这条是 ②-1 的阴性孪生：顶层照抄正确、只有中间一行被换掉。
    //   只查「顶层 vs 请求」的话这条**恒绿** ⇒ 判据会看起来齐了其实漏了一半。
    const actions = [
      row(1, 'arrive', '2026-10-14T01:00:00Z'),
      row(2, 'route_resolved', '2026-10-14T01:00:01Z', { request_id: 'req-OTHER' }),
      row(3, 'upstream_request', '2026-10-14T01:00:02Z'),
    ]
    const r = okResp({ actions, count: 3 })
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-echo-bad')).toBe(false)
    expect(hasId(w, 'ra-row-echo-bad')).toBe(true)
    // ★ `mockResolvedValue` 存的是 **Promise**，`mock.results[0].value` 拿到的是未解包的那个
    //   ⇒ 必须拿自己造的那个对象来断言，不能从 mock 的返回值里取。
    expect(requestActionsRowsEchoRequestId(r)).toBe(false)
    expect(requestActionsMatchesRequest(r, 'req-abc')).toBe(true)
  })

  it('②-4 ★★★ 所有行都换成同一个别的 id ⇒ 仍判不成立', async () => {
    const actions = [
      row(1, 'arrive', '2026-10-14T01:00:00Z', { request_id: 'req-X' }),
      row(2, 'route_resolved', '2026-10-14T01:00:01Z', { request_id: 'req-X' }),
    ]
    const r = okResp({ actions, count: 2 })
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await loadWith(w)
    expect(requestActionsRowsEchoRequestId(r)).toBe(false)
    expect(hasId(w, 'ra-row-echo-bad')).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ count
// ══════════════════════════════════════════════════════════════════════════

describe('③ count 与条数自洽', () => {
  it('③-1 ★★★★★ 正常响应不报错，且显示 count 与条数', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-count-bad')).toBe(false)
    expect(textOf(w, 'ra-count')).toBe('3')
    expect(textOf(w, 'ra-actions-count')).toBe('3')
  })

  it('③-2 ★★★★★ count 与条数不符 ⇒ 报错', async () => {
    mFetch.mockResolvedValue(okResp({ count: 9 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-count-bad')).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④⑤ detail / credential_label：两个「永远不该出现」的键
// ══════════════════════════════════════════════════════════════════════════

describe('④⑤ 两个「永远不该出现」的键', () => {
  it('④-1 ★★★★★ 正常响应里 detail 键不出现', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-detail-key-bad')).toBe(false)
  })

  it('④-2 ★★★★★ detail 键出现 ⇒ 报错（后端 `:108` 已 delete 并摊平）', async () => {
    const actions = [row(1, 'arrive', '2026-10-14T01:00:00Z', { detail: { queue_depth: 3 } })]
    mFetch.mockResolvedValue(okResp({ actions, count: 1 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-detail-key-bad')).toBe(true)
  })

  it('⑤-1 ★★★★★ credential_label 出现 ⇒ 报错（那是 SSE 才注入的）', async () => {
    const actions = [row(1, 'arrive', '2026-10-14T01:00:00Z', { credential_label: 'cred-a' })]
    mFetch.mockResolvedValue(okResp({ actions, count: 1 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-credential-label-bad')).toBe(true)
  })

  it('⑤-2 ★★★ 正常响应里 credential_label 不出现', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-credential-label-bad')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 排序与去重
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 排序与 seq 去重', () => {
  it('⑥-1 ★★★★★ 正常响应：升序且 seq 唯一 ⇒ 不报错', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-order-bad')).toBe(false)
    expect(hasId(w, 'ra-seq-bad')).toBe(false)
  })

  it('⑥-2 ★★★★★ ts 逆序 ⇒ 报顺序错', async () => {
    const actions = [
      row(1, 'arrive', '2026-10-14T02:00:00Z'),
      row(2, 'route_resolved', '2026-10-14T01:00:00Z'),
    ]
    mFetch.mockResolvedValue(okResp({ actions, count: 2 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-order-bad')).toBe(true)
  })

  it('⑥-3 ★★★★ ts 相同但 seq 逆序 ⇒ **也算**顺序错（后端按 seq 兜底）', async () => {
    const actions = [
      row(9, 'arrive', '2026-10-14T01:00:00Z'),
      row(2, 'route_resolved', '2026-10-14T01:00:00Z'),
    ]
    mFetch.mockResolvedValue(okResp({ actions, count: 2 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-order-bad')).toBe(true)
  })

  it('⑥-4 ★★★★★ seq 重复 ⇒ 报错（后端按 seq 去重过）', async () => {
    const actions = [
      row(1, 'arrive', '2026-10-14T01:00:00Z'),
      row(1, 'route_resolved', '2026-10-14T01:00:01Z'),
    ]
    mFetch.mockResolvedValue(okResp({ actions, count: 2 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-seq-bad')).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑦ 开放字符串：未知取值照样显示
// ══════════════════════════════════════════════════════════════════════════

describe('⑦ action 是开放字符串', () => {
  it('⑦-1 ★★★★★ 未知取值**照样渲染**并打标记，不是异常', async () => {
    const actions = [row(1, 'arrive', '2026-10-14T01:00:00Z'), row(2, 'brand_new_action', '2026-10-14T01:00:01Z')]
    mFetch.mockResolvedValue(okResp({ actions, count: 2 }))
    const w = await mountView()
    await loadWith(w)
    // ★★ 关键：行必须**在屏上**（后端只判非空，不校验枚举 ⇒ 校验枚举会误伤正常响应）。
    expect(hasId(w, 'ra-row-2')).toBe(true)
    expect(textOf(w, 'ra-row-2')).toContain('brand_new_action')
    expect(hasId(w, 'ra-unknown-tag')).toBe(true)
    expect(textOf(w, 'ra-unknown-actions')).toBe(t('ra.check.unknownActions', { n: 1 }))
    expect(actionIsKnown(actions[1]!)).toBe(false)
  })

  it('⑦-2 ★★★★ 全部已知时**不**出现未知标记', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-unknown-tag')).toBe(false)
    expect(textOf(w, 'ra-unknown-actions')).toBe(t('ra.check.unknownActions', { n: 0 }))
  })

  it('⑦-3 ★★★ 空时间线：未知计数仍是 0（不能因为没有行就不显示）', async () => {
    mFetch.mockResolvedValue(okResp({ actions: [], count: 0 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-actions-empty')).toBe(true)
    expect(textOf(w, 'ra-unknown-actions')).toBe(t('ra.check.unknownActions', { n: 0 }))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑧⑨ 额外键与零值 ts
// ══════════════════════════════════════════════════════════════════════════

describe('⑧⑨ 摊平上来的额外键、零值 ts', () => {
  it('⑧-1 ★★★★★ detail 摊平的额外键**必须显示**出来', async () => {
    // ★ 后端把 detail 的键提升到顶层且**不覆盖**已有键 ⇒ 它们是合法上下文，
    //   不显示等于把后端给的信息丢掉。
    const actions = [row(1, 'model_enqueued', '2026-10-14T01:00:00Z', { queue_depth: 3, node_id: 'n1' })]
    mFetch.mockResolvedValue(okResp({ actions, count: 1 }))
    const w = await mountView()
    await loadWith(w)
    expect(textOf(w, 'ra-extra-1')).toContain('queue_depth')
    expect(textOf(w, 'ra-extra-1')).toContain('node_id')
    expect(textOf(w, 'ra-extra-keys')).toBe(t('ra.check.extraKeys', { list: 'node_id / queue_depth' }))
    expect(actionExtraKeys(actions[0]!)).toEqual(['node_id', 'queue_depth'])
  })

  it('⑧-2 ★★★★ 没有额外键时显示「无」（不是空白）', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(textOf(w, 'ra-extra-keys')).toBe(t('ra.check.extraKeys', { list: t('ra.check.none') }))
  })

  it('⑧-3 ★★★★ 四个恒在键与已知条件键**不算**额外键', () => {
    expect(
      actionExtraKeys(row(1, 'arrive', '2026-10-14T01:00:00Z', { model: 'gpt-4o', retry: true, credential_id: 7 })),
    ).toEqual([])
  })

  it('⑨-1 ★★★★★ 零值 ts 要单独提示（它会排到最前，顺序看着反常）', async () => {
    const actions = [
      row(1, 'arrive', GO_ZERO_TIME),
      row(2, 'reply', '2026-10-14T01:00:01Z'),
    ]
    mFetch.mockResolvedValue(okResp({ actions, count: 2 }))
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-zero-ts')).toBe(true)
    expect(textOf(w, 'ra-zero-ts')).toBe(t('ra.check.zeroTs', { n: 1 }))
  })

  it('⑨-2 ★★★★ 没有零值 ts 时**不**提示', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-zero-ts')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑩ 错误归因与陈旧数据
// ══════════════════════════════════════════════════════════════════════════

describe('⑩ 错误归因与陈旧数据', () => {
  it('⑩-1 ★★★★★ 502 单独归因（本仓少见的网关错误码）', async () => {
    const e = Object.assign(new Error(LIVE_ACTIONS_UNAVAILABLE_MESSAGE), { status: 502 })
    mFetch.mockRejectedValue(e)
    const w = await mountView()
    await loadWith(w)
    expect(requestActionsUnavailableReason(502, LIVE_ACTIONS_UNAVAILABLE_MESSAGE)).toBe('store_unavailable')
    expect(textOf(w, 'ra-error')).toBe(t('ra.err.storeUnavailable'))
  })

  it('⑩-2 ★★★★★ 503 归因为「未装配」', async () => {
    const e = Object.assign(new Error(LIVE_ACTIONS_NOT_WIRED_MESSAGE), { status: 503 })
    mFetch.mockRejectedValue(e)
    const w = await mountView()
    await loadWith(w)
    expect(requestActionsUnavailableReason(503, LIVE_ACTIONS_NOT_WIRED_MESSAGE)).toBe('not_wired')
    expect(textOf(w, 'ra-error')).toBe(t('ra.err.notWired'))
  })

  it('⑩-3 ★★★★ 400 归因为「缺少请求 ID」', async () => {
    const e = Object.assign(new Error(MISSING_REQUEST_ID_MESSAGE), { status: 400 })
    mFetch.mockRejectedValue(e)
    const w = await mountView()
    await loadWith(w)
    expect(requestActionsIsMissingId(400, MISSING_REQUEST_ID_MESSAGE)).toBe(true)
    expect(textOf(w, 'ra-error')).toBe(t('ra.err.missingId'))
  })

  it('⑩-4 ★★★★ 502 与 503 **不可混淆**（把 502 当 503 会丢掉「上游 Redis 挂了」）', () => {
    expect(requestActionsUnavailableReason(502, LIVE_ACTIONS_NOT_WIRED_MESSAGE)).toBe(null)
    expect(requestActionsUnavailableReason(503, LIVE_ACTIONS_UNAVAILABLE_MESSAGE)).toBe(null)
  })

  it('⑩-5 ★★★ 未知错误原样透出（不吞）', async () => {
    mFetch.mockRejectedValue(new Error('boom 42'))
    const w = await mountView()
    await loadWith(w)
    expect(textOf(w, 'ra-error')).toBe('boom 42')
  })

  it('⑩-6 ★★★★ 请求失败后清空上一轮数据，不留陈旧', async () => {
    const w = await mountView()
    await loadWith(w)
    expect(hasId(w, 'ra-row-1')).toBe(true)
    mFetch.mockRejectedValue(new Error('boom'))
    await loadWith(w)
    expect(hasId(w, 'ra-row-1')).toBe(false)
    expect(hasId(w, 'ra-echo')).toBe(false)
  })

  it('⑩-7 ★★★★ 路径段为空时**不发请求**（后端会 400）', async () => {
    const w = await mountView()
    expect(hasId(w, 'ra-params-bad')).toBe(true)
    await byId(w, 'ra-request-id').trigger('keyup.enter')
    await flushPromises()
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('⑩-8 ★★★ 只输入空格也算空（trim 之后判）', async () => {
    const w = await mountView()
    await byId(w, 'ra-request-id').setValue('   ')
    expect(hasId(w, 'ra-params-bad')).toBe(true)
    expect(mFetch).not.toHaveBeenCalled()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑪ 触控与接线
// ══════════════════════════════════════════════════════════════════════════

describe('⑪ 触控与接线', () => {
  it('⑪-1 ★★★ 新增触控控件全部 ≥48 CSS px', () => {
    const src = viewSrc()
    for (const sel of ['ra__btn', 'ra__input']) {
      const re = new RegExp(`\\.${sel}\\s*[,{][\\s\\S]{0,200}?min-height:\\s*(\\d+)px`)
      const m = re.exec(src)
      expect(m, `找不到 ${sel} 的 min-height`).toBeTruthy()
      // ★ 把数值**解析出来**再比大小，不要 `toContain('48px')`（那样写 480 也过）。
      expect(Number(m![1]), `${sel} 触控高度不足 48`).toBeGreaterThanOrEqual(48)
    }
  })

  it('⑪-2 ★★★★ 路由存在且 meta 是 super_admin', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toContain("path: '/request-actions'")
    expect(src).toContain("component: () => import('@/views/RequestActionsView.vue')")
    expect(src).toMatch(/ra\.title'[^}]*requiresRole: 'super_admin'/)
  })

  it('⑪-3 ★★★ i18n 两侧都有 ra 前缀与 nav 标题键', () => {
    const zh = readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')
    const en = readFileSync(join(process.cwd(), 'src/i18n/en-US.ts'), 'utf8')
    expect(zh).toContain('  ra: {')
    expect(en).toContain('  ra: {')
    expect(zh).toContain("requestActions: '动作时间线',")
    expect(en).toContain("requestActions: 'Action timeline',")
  })

  it('⑪-4 ★★★★ 页面必须写明「短期回放不是历史」', async () => {
    // ★ 这句是**用户可感知的正确性前提**：队列 LTRIM 5000 ⇒ 更老的事件已经没了，
    //   而响应里没有任何字段说明这件事 ⇒ 不说出来就是误导。
    const w = await mountView()
    await loadWith(w)
    expect(textOf(w, 'ra-replay-note')).toBe(t('ra.scope.replay'))
  })

  it('⑪-5 ★★★ 本批源文件里没有 U+FFFD，也没有 NUL', () => {
    for (const f of [
      'src/views/RequestActionsView.vue',
      'src/views/RequestActionsView.spec.ts',
      'src/api/requestActions.ts',
    ]) {
      const p = join(process.cwd(), f)
      expect(existsSync(p), `文件不存在：${f}`).toBe(true)
      const s = readFileSync(p, 'utf8')
      expect(s.includes('\uFFFD'), `${f} 含 U+FFFD 替换字符（编码损坏）`).toBe(false)
      expect(s.includes('\u0000'), `${f} 含 NUL`).toBe(false)
    }
  })

  it('⑪-6 ★★★ 用例标题里不得出现替换字符（变异台按 fullName 子串匹配）', () => {
    const self = readFileSync(join(process.cwd(), 'src/views/RequestActionsView.spec.ts'), 'utf8')
    let checked = 0
    for (const line of self.split('\n')) {
      if (/^\s*(describe|it)\(/.test(line)) {
        checked++
        expect(line, '用例标题行含 U+FFFD').not.toContain('\uFFFD')
      }
    }
    // ★ 必须确认**真的扫到了行**，否则这条会在「正则失配」时恒绿。
    expect(checked).toBeGreaterThan(40)
  })
})