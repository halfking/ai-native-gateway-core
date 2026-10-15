import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import DispatchJournalView from './DispatchJournalView.vue'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { setLocale } from '@/i18n'
import {
  fetchDispatchJournal,
  dispatchJournalParamsAreAcceptable,
  dispatchJournalRoleCanReadTenant,
  dispatchJournalRoleIsPrivileged,
  DISPATCH_JOURNAL_TERMINAL_ACTIONS,
  DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES,
  DISPATCH_JOURNAL_RING_CAPACITY,
  DISPATCH_JOURNAL_NOT_FOUND_MESSAGE,
  type DispatchJournalSnapshot,
  type DispatchJournalEntry,
} from '@/api/dispatchJournal'

/**
 * 单请求调度轨迹面回归护栏（2026-10-14，第一百一十二批）。
 *
 * ★★★ 权限：**admin 档，不是 superAdmin** —— 可执行注册处
 *   `cmd/gateway/main.go:7143-7144` 传的是 `requestJourneyWrapAdmin`
 *   （→ `admin.AdminMiddleware`，**不检查 role**）
 *   ⇒ 抽屉席**不设** `requiresRole`，且**不得**进 `AppDrawer.spec.ts` 的
 *     superAdmin 白名单。⚠️ 与批 109/110/111 连续三批相反。
 *
 * ★★★★★★ 本族是本仓**第一个真正做租户鉴权的读端点**：权限是
 *   **「角色 × 路径里的租户」二维**的（`admin/journal_handlers.go:59-67`）：
 *   ```go
 *   privileged := auth.Role == "super_admin" || auth.Role == "admin_key"
 *   if !privileged && auth.Role != "tenant_admin" { 404 }
 *   if !privileged && callerTenant != tenantID     { 404 }
 *   ```
 *   ⇒ 与批 75/76/77/104/106/109/110 的「读路径不隔离」**完全相反**
 *   ⇒ ⇒ 前端**不需要**段落级门控，但**理由不同**：不是「档位挡住了跨租户」
 *     而是「后端自己按调用方租户挡了」。
 *
 * ★★ 越权返 **404 而不是 403**（连普通用户都是 404）⇒ 客户端**不能**用
 *   「没报权限错」推断「有权限」；`404` 有七种成因、文案完全相同。
 *
 * 判据钉十组不变量：
 *  ① ★★ admin 档 ⇒ 视图**无角色分支**（结构层 + 行为层），且租户框只是便利不是门。
 *  ② ★★★ 两段路径都必须合法（含 `/` `\` NUL 或 `.` `..` 必然 400）。
 *  ③ ★★★★ **八条可自验不变式**，每条都配正控（正常载荷不得误报）。
 *  ④ ★★★★★ 截断是**两段式**的：`truncated` 只覆盖第二段，
 *      第一段（环形 128）的丢弃**没有任何标记** —— 唯一线索是首条 seq ≠ 1。
 *  ⑤ ★★★★ `truncated === true` ⇒ 拿满窗口；`truncated_count === 0` ⟺ `!truncated`。
 *  ⑥ ★★★★ `entries` 为空是**二义**的，不是「这次请求很干净」。
 *  ⑦ ★★★★ 12 个可选键都是**值类型 + omitempty** ⇒ 值为 0 与「键缺失」不可区分。
 *  ⑧ ★★★ 回显的 `tenant_id` / `request_id` 恒等于查询条件。
 *  ⑨ ★★ 越权与不存在同形（404）⇒ 页面据实说明，不许把 404 说成「没这条请求」。
 *  ⑩ ★ 新增触控控件 ≥48 CSS px。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function counts(over: Record<string, unknown> = {}) {
  return { retries: 0, node_switches: 0, model_switches: 0, capacity_waits: 0, scheduled_waits: 0, ...over }
}

function entry(over: Record<string, unknown> = {}) {
  return {
    seq: 1,
    at: '2026-10-14T03:00:00Z',
    action: 'dispatch',
    attempt: 1,
    counts: counts(),
    ...over,
  }
}

/** ★ 合法快照：seq 从 1 连续、末条终态、version = 末条 seq、attempt/counts 非递减。 */
function goodEntries(n: number): DispatchJournalEntry[] {
  const out: DispatchJournalEntry[] = []
  for (let i = 1; i <= n; i++) {
    out.push(
      entry({
        seq: i,
        at: `2026-10-14T03:00:${String(i).padStart(2, '0')}Z`,
        action: i === n ? 'completed' : 'dispatch',
        attempt: i,
        counts: counts({ retries: Math.max(0, i - 1) }),
      }),
    )
  }
  return out
}

function snap(over: Partial<DispatchJournalSnapshot> = {}): DispatchJournalSnapshot {
  const entries = over.entries ?? goodEntries(3)
  return {
    tenant_id: 'tenant-a',
    request_id: 'req-1',
    entries,
    truncated: false,
    truncated_count: 0,
    snapshot_version: entries.length > 0 ? entries[entries.length - 1]!.seq : 0,
    ...over,
  } as DispatchJournalSnapshot
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { userInfo: { id: 1, tenant_id: 'tenant-a', role: 'tenant_admin', enabled: true } as unknown }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/dispatchJournal', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dispatchJournal')>()
  return { ...actual, fetchDispatchJournal: vi.fn() }
})

const mFetch = fetchDispatchJournal as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(DispatchJournalView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

/** ★★★ 取某一格文本 —— 负断言必须与被测对象**同格**。 */
function cell(w: W, id: string): string {
  return byId(w, id).text()
}

/** 填好两段并查询，返回已加载完的 wrapper。 */
async function lookup(w: W, tenant = 'tenant-a', request = 'req-1') {
  await byId(w, 'dj-tenant').setValue(tenant)
  await byId(w, 'dj-request').setValue(request)
  await byId(w, 'dj-load').trigger('click')
  await flushPromises()
}

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

function viewSource(): string {
  return readFileSync(join(process.cwd(), 'src/views/DispatchJournalView.vue'), 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.userInfo = { id: 1, tenant_id: 'tenant-a', role: 'tenant_admin', enabled: true } as unknown
  mFetch.mockResolvedValue(snap())
})

// ── ① 权限姿态：admin 档 + 无角色分支 ─────────────────────────────────────

describe('① admin 档的处置', () => {
  it('①-1 ★★★ 二维权限：平台超管可读任意租户，租户管理员只能读本租户', () => {
    // ★ 直接量模块的判据，把「角色 × 路径租户」这层二维关系钉死
    expect(dispatchJournalRoleIsPrivileged('super_admin')).toBe(true)
    expect(dispatchJournalRoleIsPrivileged('admin_key')).toBe(true)
    expect(dispatchJournalRoleIsPrivileged('tenant_admin')).toBe(false)
    expect(dispatchJournalRoleCanReadTenant('super_admin', 'caller-a', 'other-b')).toBe(true)
    expect(dispatchJournalRoleCanReadTenant('tenant_admin', 'caller-a', 'caller-a')).toBe(true)
    expect(dispatchJournalRoleCanReadTenant('tenant_admin', 'caller-a', 'other-b')).toBe(false)
  })

  it('①-2 ★★★ 换角色/换租户重挂 ⇒ 渲染逐字节相同（视图无角色分支）', () => {
    mFetch.mockResolvedValue(snap())
    const a = mount(DispatchJournalView)
    authState.userInfo = { id: 2, tenant_id: 'other-b', role: 'super_admin', enabled: true } as unknown
    const b = mount(DispatchJournalView)
    return flushPromises().then(() => {
      expect(a.html()).toBe(b.html())
    })
  })

  it('①-3 ★★ 视图源码（剥注释后）不含按角色分支的判据', () => {
    // ★ 剥注释是关键：视图文件头**大量**写着 super_admin / tenant_admin
    //   作为**契约说明**；不断言剥注释就会把文档当成行为。
    const src = stripComments(viewSource())
    expect(src).not.toMatch(/super_admin|tenant_admin|admin_key|isSuperAdmin|requiresRole/)
  })

  it('①-4 ★★ 租户框从 JWT 预填（便利，不是安全门）', async () => {
    const w = await mountView()
    // ★★ 预填**不等于**授权：非特权调用方改租户，后端照样 404
    // ★ `wrapper.element` 的类型是 `VueNode<Element>` ⇒ 直接读 `.value` 报 TS2339
    //   （批 111 记过同族：先写 `as any` 会绕过类型，要写**具体**的 HTMLInputElement）
    expect((byId(w, 'dj-tenant').element as HTMLInputElement).value).toBe('tenant-a')
    const html = stripComments(viewSource())
    expect(html).not.toMatch(/v-if="[^\"]*role/)
  })

  it('①-5 ★★★ 页面据实说明「越权是 404 不是 403」，不得说成「没这条请求」', async () => {
    const w = await mountView()
    const note = cell(w, 'dj-permission-note')
    expect(note).toContain('404')
    expect(note).toContain('403')
    // ★★ 不得出现「无权限 / 403」这类与后端相反的措辞
    expect(note).not.toContain('403 拒绝')
  })
})

// ── ② 参数合法性 ─────────────────────────────────────────────────────────

describe('② 路径段合法性', () => {
  it('②-1 ★★★ 两段都合法才发得出去；否则按钮禁用', async () => {
    const w = await mountView()
    await byId(w, 'dj-tenant').setValue('tenant-a')
    await byId(w, 'dj-request').setValue('')
    await flushPromises()
    expect((byId(w, 'dj-load').element as HTMLButtonElement).disabled).toBe(true)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('②-2 ★★★ 含 `/` 的段必然 400 ⇒ 本地就不发 + 显式提示', async () => {
    const w = await mountView()
    await byId(w, 'dj-tenant').setValue('tenant/a')
    await byId(w, 'dj-request').setValue('req-1')
    await flushPromises()
    expect(hasId(w, 'dj-params-bad')).toBe(true)
    expect((byId(w, 'dj-load').element as HTMLButtonElement).disabled).toBe(true)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('②-3 ★★ `.` / `..` / `\` / NUL 四种同样被拒', () => {
    // ★★ 后端 `parseDispatchJournalPath` 是 **trim 后**判空 + 拒 `.`/`..` + 拒含 `/ \ NUL`
    for (const bad of ['.', '..', 'a/b', 'a\\b', 'a b', '   ']) {
      expect(dispatchJournalParamsAreAcceptable(bad, 'req-1')).toBe(false)
      expect(dispatchJournalParamsAreAcceptable('tenant-a', bad)).toBe(false)
    }
    expect(dispatchJournalParamsAreAcceptable('tenant-a', 'req-1')).toBe(true)
  })

  it('②-4 ★★ 段两端的空格会被后端 TrimSpace ⇒ 本地也按 trim 判', async () => {
    mFetch.mockResolvedValue(snap())
    const w = await mountView()
    await lookup(w, '  tenant-a  ', 'req-1')
    expect(mFetch).toHaveBeenCalledTimes(1)
    // ★ 回显比对也要按 trim（后端返回的是 trim 后的值）
    expect(hasId(w, 'dj-tenant-echo-bad')).toBe(false)
    expect(hasId(w, 'dj-request-echo-bad')).toBe(false)
  })
})

// ── ③ 八条可自验不变式（每条都配正控）────────────────────────────────────

describe('③ 可自验不变式', () => {
  it('③-1 ★★★★ 合法载荷 ⇒ 八条告警**一条都不报**（正控的总闸）', async () => {
    const w = await mountView()
    await lookup(w)
    for (const id of [
      'dj-seqstart-bad',
      'dj-seq-bad',
      'dj-terminal-bad',
      'dj-attempt-bad',
      'dj-counts-bad',
      'dj-version-bad',
      'dj-truncated-bad',
      'dj-truncatedcount-bad',
    ]) {
      expect(hasId(w, id)).toBe(false)
    }
  })

  it('③-2 ★★★★ seq 不连续 ⇒ 报违例', async () => {
    mFetch.mockResolvedValue(snap({ entries: goodEntries(3).map((e, i) => ({ ...e, seq: i === 1 ? 9 : e.seq })) }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-seq-bad')).toBe(true)
  })

  it('③-3 ★★★★ 首条 seq 不为 1 ⇒ 报违例（且同时被当作「隐形丢弃」的线索）', async () => {
    // ★★ 首条 seq ≠ 1 有两种成因：环形丢弃，或者载荷被换过 ⇒ 两者都要报
    const entries = goodEntries(3).map((e) => ({ ...e, seq: e.seq + 5 }))
    mFetch.mockResolvedValue(snap({ entries }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-seqstart-bad')).toBe(true)
    expect(hasId(w, 'dj-leading-dropped')).toBe(true)
  })

  it('③-4 ★★★★ 末条不是终态 ⇒ 报违例', async () => {
    const entries = goodEntries(3)
    entries[entries.length - 1] = { ...entries[entries.length - 1]!, action: 'dispatch' }
    mFetch.mockResolvedValue(snap({ entries }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-terminal-bad')).toBe(true)
  })

  it('③-5 ★★ 三个终态值各自都算终态（逐个钉，别只测 completed）', async () => {
    for (const a of DISPATCH_JOURNAL_TERMINAL_ACTIONS) {
      const entries = goodEntries(2)
      entries[1] = { ...entries[1]!, action: a }
      mFetch.mockResolvedValue(snap({ entries }))
      const w = await mountView()
      await lookup(w)
      expect(hasId(w, 'dj-terminal-bad')).toBe(false)
    }
  })

  it('③-6 ★★★★ snapshot_version 不等于末条 seq ⇒ 报违例', async () => {
    mFetch.mockResolvedValue(snap({ snapshot_version: 99 }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-version-bad')).toBe(true)
  })

  it('③-7 ★★★ attempt 递减 ⇒ 报违例（它是累计量）', async () => {
    const entries = goodEntries(3)
    entries[2] = { ...entries[2]!, attempt: 0 }
    mFetch.mockResolvedValue(snap({ entries }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-attempt-bad')).toBe(true)
  })

  it('③-8 ★★★ counts 递减 ⇒ 报违例（它是累计快照）', async () => {
    const entries = goodEntries(3)
    entries[2] = { ...entries[2]!, counts: counts({ retries: 0 }) }
    mFetch.mockResolvedValue(snap({ entries }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-counts-bad')).toBe(true)
  })

  it('③-10 ★★★★ 末条是终态 ⇒ 那一格**必须**带「（终态）」标记', async () => {
    // ★★★ S16（把标记整段删掉）第一版是 NO_EFFECT，原因是 ③-5 只断
    //   「终态**不该**报告警」，**没有一条断「标记要出现」** ——
    //   ⇒ 判据钉的是「不该报错的分支」，而变异砍的是「该出现的内容」。
    //   ⇒ 两个方向都要钉，否则「配了正控」只是说说。
    const entries = goodEntries(2)
    entries[1] = { ...entries[1]!, action: 'failed' }
    mFetch.mockResolvedValue(snap({ entries }))
    const w = await mountView()
    await lookup(w)
    expect(cell(w, 'dj-e-2-action')).toContain('failed')
    expect(cell(w, 'dj-e-2-action')).toContain('（终态）')
  })

  it('③-11 ★★ 非终态 ⇒ **不得**带「（终态）」标记（配负控）', async () => {
    const entries = goodEntries(2)
    mFetch.mockResolvedValue(snap({ entries }))
    const w = await mountView()
    await lookup(w)
    // ★★ 负断言**必须同格**：只断 seq=1 那一格，不能放大到整页
    expect(cell(w, 'dj-e-1-action')).not.toContain('（终态）')
    expect(cell(w, 'dj-e-2-action')).toContain('（终态）')
  })

  it('③-9 ★★ 页面把终态动作集合显式列出来（不变式可核对）', async () => {
    const w = await mountView()
    await lookup(w)
    const t = cell(w, 'dj-terminal-set')
    for (const a of DISPATCH_JOURNAL_TERMINAL_ACTIONS) expect(t).toContain(a)
  })
})

// ── ④⑤ 截断的两段式 ─────────────────────────────────────────────────────

describe('④⑤ 截断的两段式', () => {
  it('④-1 ★★★★★ `truncated === false` **不得**被渲染成「轨迹完整」', async () => {
    const w = await mountView()
    await lookup(w)
    expect(cell(w, 'dj-s-truncated')).toBe('未截断')
    // ★★★ 两段式说明**必须**常驻，而不是只在截断时才说
    expect(hasId(w, 'dj-two-stage')).toBe(true)
    expect(cell(w, 'dj-two-stage')).toContain(String(DISPATCH_JOURNAL_RING_CAPACITY))
    expect(cell(w, 'dj-two-stage')).toContain(String(DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES))
  })

  it('④-2 ★★★★ 首条 seq 为 1（未丢头部）⇒ **不得**报隐形丢弃（正控）', async () => {
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-leading-dropped')).toBe(false)
  })

  it('④-3 ★★★★ 标记已截断且拿满窗口 ⇒ 不报违例（正控）', async () => {
    const n = DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES
    const entries = goodEntries(n).map((e, i) => ({ ...e, seq: i + 1 }))
    mFetch.mockResolvedValue(snap({ entries, truncated: true, truncated_count: 12 }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-truncated-bad')).toBe(false)
    expect(hasId(w, 'dj-truncatedcount-bad')).toBe(false)
    expect(cell(w, 'dj-s-entries')).toBe(String(n))
  })

  it('④-4 ★★★★ 标记已截断却没拿满窗口 ⇒ 报违例', async () => {
    mFetch.mockResolvedValue(snap({ entries: goodEntries(3), truncated: true, truncated_count: 12 }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-truncated-bad')).toBe(true)
  })

  it('④-5 ★★★★ `truncated_count === 0` 却标为已截断 ⇒ 报违例', async () => {
    const n = DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES
    const entries = goodEntries(n)
    mFetch.mockResolvedValue(snap({ entries, truncated: true, truncated_count: 0 }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-truncatedcount-bad')).toBe(true)
  })

  it('④-6 ★★ 未截断却给了正的 truncated_count ⇒ 同样报违例（**反向**那一半）', async () => {
    // ★★★ 只钉「已截断却 0」这一半是最常见的漏法：
    //   「未截断却有非 0 计数」同样自相矛盾，且是最普通的载荷形态。
    mFetch.mockResolvedValue(snap({ entries: goodEntries(3), truncated: false, truncated_count: 5 }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-truncatedcount-bad')).toBe(true)
  })
})

// ── ⑥ 空 entries 的二义性 ────────────────────────────────────────────────

describe('⑥ 空 entries 是二义的', () => {
  it('⑥-1 ★★★★ 空 entries ⇒ 必须说「无法区分」，**不是**「这个请求很干净」', async () => {
    mFetch.mockResolvedValue(snap({ entries: [], snapshot_version: 0 }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-empty-ambiguous')).toBe(true)
    const t = cell(w, 'dj-empty-ambiguous')
    expect(t).toContain('淘汰')
    expect(t).toContain('重启')
  })

  it('⑥-2 ★★ 非空 ⇒ **不得**报二义提示（正控）', async () => {
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-empty-ambiguous')).toBe(false)
  })

  it('⑥-3 ★★ 空 entries ⇒ 一条行卡片都不渲染', async () => {
    mFetch.mockResolvedValue(snap({ entries: [], snapshot_version: 0 }))
    const w = await mountView()
    await lookup(w)
    expect(w.findAll('[data-testid^="dj-e-"]').length).toBe(0)
  })
})

// ── ⑦ omitempty 吃掉 0 ──────────────────────────────────────────────────

describe('⑦ 值类型 omitempty ⇒ 0 与「没有」不可区分', () => {
  it('⑦-1 ★★★★ 可选数字键缺失 ⇒ 一律说「未提供（键缺失或值为 0）」', async () => {
    mFetch.mockResolvedValue(snap({ entries: [entry({ seq: 1, action: 'completed' })] }))
    const w = await mountView()
    await lookup(w)
    for (const k of ['http_status', 'credential_id', 'provider_id']) {
      const t = cell(w, `dj-e-1-${k}`)
      expect(t).toContain('未提供')
      expect(t).toContain('不可区分')
    }
  })

  it('⑦-2 ★★★ 负断言**必须同格**：别的格显示真值不得连带判这条格错', async () => {
    // ★★★ 批 109/110/112 连续三次踩「负断言范围过宽」：
    //   这里 http_status 有值、credential_id 没值 ⇒ 两格必须给**不同**措辞。
    mFetch.mockResolvedValue(
      snap({ entries: [entry({ seq: 1, action: 'completed', http_status: 429 })] }),
    )
    const w = await mountView()
    await lookup(w)
    expect(cell(w, 'dj-e-1-http_status')).toBe('429')
    expect(cell(w, 'dj-e-1-credential_id')).toContain('未提供')
  })

  // ★★ 原本还有一条「值真的是 0 时也不许渲染成 0」，**已按纪律删掉**：
  //   `domains/dispatch/journal.go:26` 是 `CredentialID int json:"credential_id,omitempty"`
  //   —— 值类型 + omitempty ⇒ 值为 0 时**后端根本不发这个键**
  //   ⇒ 「`credential_id` 键在且值为 0」是**后端不可达**的载荷。
  //   为不可达输入加防御分支 = **自造判据**（承批 105「一条自己造的恒真判据」）。
  //   ⇒ 而模块的 `dispatchJournalOptionalNumberIsPresent` 语义上也**只答「键在不在」**
  //     （其注释自陈「回答的是这个 0 有没有落进响应，不是值是不是 0」）⇒ 两者一致。
  //   ⇒ 真正可达的语义是 ⑦-1（缺键 ⇒ 未提供）与 ⑦-2（有非零值 ⇒ 原样显示）。
  it('⑦-4 ★★★ 四条告警类判据的负断言范围也必须同格', async () => {
    // ★ seq 号出现在多张卡片的 data-testid 里 ⇒ 对整页断 not.toContain 会被别处命中
    mFetch.mockResolvedValue(snap())
    const w = await mountView()
    await lookup(w)
    expect(cell(w, 'dj-s-version')).toBe('3')
    expect(cell(w, 'dj-e-3-attempt')).toBe('3')
  })
})

// ── ⑧ 回显一致性 ─────────────────────────────────────────────────────────

describe('⑧ 回显一致性', () => {
  it('⑧-1 ★★★ 回显的 tenant_id 与查询不一致 ⇒ 报违例', async () => {
    mFetch.mockResolvedValue(snap({ tenant_id: 'other-b' }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-tenant-echo-bad')).toBe(true)
  })

  it('⑧-2 ★★★ 回显的 request_id 与查询不一致 ⇒ 报违例', async () => {
    mFetch.mockResolvedValue(snap({ request_id: 'req-other' }))
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-request-echo-bad')).toBe(true)
  })

  it('⑧-3 ★★ 一致 ⇒ 两条都不报（正控）', async () => {
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-tenant-echo-bad')).toBe(false)
    expect(hasId(w, 'dj-request-echo-bad')).toBe(false)
  })
})

// ── ⑨ 错误与 404 ────────────────────────────────────────────────────────

describe('⑨ 错误呈现', () => {
  it('⑨-1 ★★ 加载失败 ⇒ 走 error 段，且清空上一份快照', async () => {
    // ★★★ 第一版在 `lookup` **之前**就 `mockRejectedValueOnce` ⇒ 那次拒绝被
    //   第一次查询吃掉了 ⇒ 快照根本没渲染 ⇒ 后面那条 `dj-s-entries` 断言必然红。
    //   ⇒ 「先渲出旧数据，再让它失败」这个场景，mock 必须**按调用次序**排。
    const w = await mountView()
    await lookup(w)
    expect(hasId(w, 'dj-s-entries')).toBe(true)
    mFetch.mockRejectedValueOnce(new Error('boom'))
    await byId(w, 'dj-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'dj-error')).toBe('boom')
    expect(hasId(w, 'dj-s-entries')).toBe(false)
  })

  it('⑨-2 ★★★ 404 的七种成因文案相同 ⇒ 页面**不得**替它判断「快照不存在」', async () => {
    // ★★ `not found` 同时表示「越权」「快照不存在」「路径与快照不符」…
    mFetch.mockRejectedValue(new Error(DISPATCH_JOURNAL_NOT_FOUND_MESSAGE))
    const w = await mountView()
    await lookup(w)
    expect(cell(w, 'dj-error')).toBe('not found')
    expect(w.text()).not.toContain('没有这条请求')
  })

  it('⑨-3 ★★ 503 journal unavailable 与 404 分开呈现', async () => {
    mFetch.mockRejectedValue(new Error('journal unavailable'))
    const w = await mountView()
    await lookup(w)
    expect(cell(w, 'dj-error')).toBe('journal unavailable')
  })
})

// ── ⑩ 触控目标 ───────────────────────────────────────────────────────────

describe('⑩ 触控目标', () => {
  it('⑩-1 ★ 新增控件都 ≥48 CSS px', () => {
    const src = viewSource()
    const scoped = src.slice(src.indexOf('<style scoped>'))
    const vals = [...scoped.matchAll(/min-height:\s*(\d+(?:\.\d+)?)px/g)].map((m) => Number(m[1]))
    expect(vals.length).toBeGreaterThan(0)
    for (const v of vals) expect(v).toBeGreaterThanOrEqual(48)
  })
})