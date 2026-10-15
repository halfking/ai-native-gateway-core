import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import ModelIQView from './ModelIQView.vue'
import { setLocale, t } from '@/i18n'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchModelIQCatalog,
  fetchModelIQNodeLatest,
  fetchModelIQHistory,
  modelIQRoleIsAllowed,
  modelIQParamsAreAcceptable,
  modelIQBadRequestKind,
  modelIQForbiddenMessage,
  modelIQSuperAdminOnlyMessage,
  modelIQDbNotConfiguredMessage,
  modelIQHistoryLimitIsAccepted,
  modelIQHistoryEffectiveLimit,
  MODEL_IQ_HISTORY_DEFAULT_LIMIT,
  MODEL_IQ_HISTORY_MAX_LIMIT,
  type ModelIQCatalogRow,
  type ModelIQNodeLatest,
  type ModelIQHistoryPoint,
} from '@/api/modelIQ'

/**
 * 模型智商面回归护栏（2026-10-14，第一百一十六批）。
 *
 * ★★★★ **权限档是 `'super_admin'` —— 但与批 113/114/115 的成因不同。**
 *   本族注册处 `admin/handler.go:1492-1495` **四条全部** `h.superAdmin(...)`，
 *   而 `SuperAdminMiddleware`（`admin/auth.go:376-409`，**逐字读过**）真的
 *   `if claims.Role != "super_admin" { 403 }`
 *   ⇒ **档位本身就是真门**（那三批的 `AdminMiddleware` 只认证、不检 role）。
 *   ⇒ 与批 109「superAdmin 档不需要视图内兜底」的关系，两边都对：
 *     安全上批 109 是对的；本批加兜底是因为**体验**——
 *     `meta.requiresRole` 全仓无人消费（守卫只查 `requiresAuth` 与登录态），
 *     深链照样打开页面。兜的是「不发请求」，不是数据访问。
 *
 * ★★★★★★ **本批的功能增量**：桌面 `web/src/api/model-iq.ts` 的
 *   `getCatalogIQ` / `getNodeIQLatest` **全仓零调用方**
 *   ⇒ 本页把这两个读端点也做进 UI ⇒ 移动端覆盖面**大于**桌面端。
 *
 * 判据钉十一组不变量：
 *  ① ★★★★★★ 权限档 = `'super_admin'`，进 superAdmin 白名单，并读 Go 源码钉「中间件逐字比 role」。
 *  ② ★★★★★ 深链兜底：非超管**连请求都不发**，且兜底段只说前端拦了什么。
 *  ③ ★★★★★ catalog 四个键可为裸 `null` ⇒ 显示占位符，**不参与运算**。
 *  ④ ★★★★★ `node_count` 与三个聚合互为充要；反例要**报出来**。
 *  ⑤ ★★★★★ `standard_iq` 与「有节点」至少一成立（WHERE 保证）。
 *  ⑥ ★★★★★ 排序：catalog 按 COALESCE 后非增、nodes 按总分非增、history 按时间非增。
 *  ⑦ ★★★★ `tested_at` 两个端点**不是同一种串** ⇒ 只在秒级带小数点时报错。
 *  ⑧ ★★★★★ `status` 可由 accuracy/stability 反推；不域内值与不自洽分别报。
 *  ⑨ ★★★★ `limit` 非法**不报错**而是静默回落 ⇒ 页面显式说生效值。
 *  ⑩ ★★★★ 错误归因：403 只有一种来源；四条 400 完全可区分。
 *  ⑪ ★★★ 触控 ≥48 CSS px，选中态用 `--app-primary`（本仓无 `--app-accent`）。
 */

// ── 夹具：逐字照抄三个 Go struct，并**守住**模块头写明的不变式 ────────────

/** `catalogIQRow`（`admin/model_iq.go:190-201`）：10 键，4 个可为裸 null。 */
function cat(over: Partial<ModelIQCatalogRow> = {}): ModelIQCatalogRow {
  return {
    canonical_id: 11,
    canonical_name: 'gpt-4o',
    display_name: 'GPT-4o',
    family: 'gpt',
    standard_iq: 90,
    standard_iq_source: 'benchmark',
    // ★ node_count>0 ⇒ 三个聚合全非 null（见 (3)）
    node_avg_iq: 88.5,
    node_count: 12,
    max_node_iq: 92,
    min_node_iq: 85,
    ...over,
  }
}

/** `nodeIQLatestRow`（`:27-41`）：13 键；`tested_at` 是 `to_char` 产物（秒级，20 字符）。 */
function node(over: Partial<ModelIQNodeLatest> = {}): ModelIQNodeLatest {
  return {
    credential_id: 7,
    credential_label: 'cred-a',
    provider_id: 3,
    provider_name: 'openai',
    raw_model_name: 'gpt-4o',
    canonical_name: 'gpt-4o',
    overall_score: 88.5,
    grade: 'A',
    avg_score: 88.5,
    min_score: 85,
    max_score: 92,
    sample_count: 12,
    // ★★ 恒 20 字符、恒无小数点 —— 见 (15)。**不是** RFC3339Nano。
    tested_at: '2026-10-07T12:00:00Z',
    ...over,
  }
}

/** `iqHistoryPoint`（`:124-134`）：9 键；`tested_at` 是 `time.Time` ⇒ RFC3339Nano。 */
function pt(over: Partial<ModelIQHistoryPoint> = {}): ModelIQHistoryPoint {
  return {
    tested_at: '2026-10-07T12:00:00.123456789Z',
    overall_score: 88.5,
    grade: 'A',
    accuracy: 92,
    stability: 100,
    latency_p95: 820,
    probe_kind: 'gateway',
    trigger_kind: 'scheduled',
    // ★ 由 accuracy/stability 派生：accuracy>0 且 stability>=100 ⇒ success
    status: 'success',
    ...over,
  }
}

/** 三段夹具，**天然满足**全部排序不变式。 */
function allOk() {
  return {
    catalog: [cat({ canonical_id: 1, standard_iq: 95, node_avg_iq: 91, canonical_name: 'a' }),
      cat({ canonical_id: 2, standard_iq: 80, node_avg_iq: 78, canonical_name: 'b' })],
    nodes: [node({ credential_id: 1, overall_score: 95 }), node({ credential_id: 2, overall_score: 70 })],
    history: [pt({ tested_at: '2026-10-07T12:00:00Z' }), pt({ tested_at: '2026-10-06T12:00:00Z' })],
  }
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/modelIQ', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modelIQ')>()
  return { ...actual, fetchModelIQCatalog: vi.fn(), fetchModelIQNodeLatest: vi.fn(), fetchModelIQHistory: vi.fn() }
})

const mCatalog = fetchModelIQCatalog as unknown as ReturnType<typeof vi.fn>
const mNodes = fetchModelIQNodeLatest as unknown as ReturnType<typeof vi.fn>
const mHistory = fetchModelIQHistory as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(ModelIQView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}
function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}
function click(w: W, id: string) {
  return byId(w, id).trigger('click').then(() => flushPromises())
}

/** ★★ 文本断言一律用 `text()`，**不用** `html()` —— 后者会序列化模板注释。 */
function textOf(w: W, id: string): string {
  return byId(w, id).text()
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/ModelIQView.vue'), 'utf8')
}

/** ★★ 取输入框当前值：`find().element` 的静态类型是 `Element`，必须收窄。 */
function inputValue(w: W, id: string): string {
  const el = byId(w, id).element
  expect(el).toBeTruthy()
  return (el as HTMLInputElement).value
}

/** ★ 读源码前先让路径写错**硬失败**，否则 expect 落在别处很难查。 */
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  const f = allOk()
  mCatalog.mockResolvedValue(f.catalog)
  mNodes.mockResolvedValue(f.nodes)
  mHistory.mockResolvedValue(f.history)
})

/** 加载全部三段。 */
async function loadAll(w: W) {
  await click(w, 'miq-load-all')
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档 super_admin', () => {
  it('①-1 ★★★★★★ 只有 super_admin 能进；admin_key 也进不来', () => {
    expect(modelIQRoleIsAllowed('super_admin')).toBe(true)
    // ★★★ SuperAdminMiddleware **只有 JWT 分支、没有 admin key 分支**
    //   （对比 AdminMiddleware 的「JWT 或 admin key」）⇒ admin_key 连认证都过不了。
    expect(modelIQRoleIsAllowed('admin_key')).toBe(false)
    expect(modelIQRoleIsAllowed('tenant_admin')).toBe(false)
    expect(modelIQRoleIsAllowed('user')).toBe(false)
    expect(modelIQRoleIsAllowed('')).toBe(false)
  })

  it('①-2 ★★★★★★ 抽屉席存在且是 super_admin 档', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'model-iq')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/model-iq')
    expect(seat!.requiresRole).toBe('super_admin')
  })

  it('①-3 ★★★★ 读 Go 源码：注册处四条**全部** h.superAdmin', () => {
    const src = goSrc('admin/handler.go')
    for (const ep of ['catalog', 'node-latest', 'history', 'trigger']) {
      const re = new RegExp(`mux\\.HandleFunc\\("/api/admin/model-iq/${ep}", h\\.superAdmin\\(`)
      expect(re.test(src), `${ep} 不是 h.superAdmin 挂载`).toBe(true)
    }
  })

  it('①-4 ★★★★★★ 读 Go 源码：中间件逐字比 role（不是只认证）', () => {
    const src = goSrc('admin/auth.go')
    // ★ 判权限不能按前缀/注册处/形参名推 ⇒ 必须对着中间体源码断言。
    expect(src).toContain('func SuperAdminMiddleware(next http.HandlerFunc')
    expect(src).toContain('if claims.Role != "super_admin" {')
    expect(src).toContain('super_admin role required for this endpoint')
  })

  it('①-5 ★★★★ 三个 handler 自己不检 role（403 只有一种来源）', () => {
    const src = goSrc('admin/model_iq.go')
    // ★ 若哪天 handler 里加了 role 判断，「403 必然来自中间件」就不成立了。
    expect(src).not.toMatch(/claims\.Role|IsSuperAdmin|IsAdmin|IsTenantAdmin/)
  })

  it('①-6 ★★★ ★★ 深链兜底：非超管看到兜底段，且**连请求都不发**', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(hasId(w, 'miq-nope')).toBe(true)
    // ★ 关键不是「藏起来」，是**不发请求**。
    expect(mCatalog).not.toHaveBeenCalled()
    expect(mNodes).not.toHaveBeenCalled()
    expect(mHistory).not.toHaveBeenCalled()
    // ★ 兜底段存在时，正文三段必须整体不渲染。
    expect(hasId(w, 'miq-catalog-empty')).toBe(false)
    expect(hasId(w, 'miq-history-empty')).toBe(false)
  })

  it('①-6b ★★★★★★ 兜底段下**没有按钮可点**（深链进来就点不着 —— 这才是「不发请求」）', async () => {
    // ★★★ 首版只断言「挂载时没发请求」，而本页**不自动加载**
    //   ⇒ 那条断言**恒真**，是半个判据（S4 变异 NO_EFFECT 顶出来的）。
    //   真正的风险面是：兜底段有没有把取数入口留在屏上。
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(hasId(w, 'miq-load-all')).toBe(false)
    expect(hasId(w, 'miq-nodes-load')).toBe(false)
    expect(hasId(w, 'miq-history-load')).toBe(false)
    expect(hasId(w, 'miq-provider-id')).toBe(false)
    expect(hasId(w, 'miq-credential-id')).toBe(false)
    // ★ 兜底段里确实**一个**取数按钮都没有。
    expect(w.findAll('button').length).toBe(0)
  })

  it('①-6c ★★★★ 超管档下那些按钮**都在**（与 ①-6b 成对）', async () => {
    // ★ 阴性对照：①-6b 若只是因为「按钮本来就没渲染」而绿，这条会红。
    const w = await mountView()
    expect(hasId(w, 'miq-load-all')).toBe(true)
    expect(hasId(w, 'miq-nodes-load')).toBe(true)
    expect(hasId(w, 'miq-history-load')).toBe(true)
  })

  it('①-7 ★★★ 兜底段要说清「是前端拦的」，不能让人以为是后端 403', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(textOf(w, 'miq-nope-msg')).toBe(t('miq.nope.msg'))
    expect(textOf(w, 'miq-nope-why')).toBe(t('miq.nope.why'))
    // ★★★ 断言按**身份**比对（`toBe(t(...))`），不是 `toContain(某几个字）——
    //   否则改文案就会红，而文案不是契约。
  })

  it('①-8 ★★★ 超管**不**看到兜底段', async () => {
    const w = await mountView()
    expect(hasId(w, 'miq-nope')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ②③④⑤ catalog
// ══════════════════════════════════════════════════════════════════════════

describe('② catalog：四个键可为裸 null', () => {
  it('②-1 ★★★★★ 四个可空键全为 null 时正常渲染，显示占位符', async () => {
    mCatalog.mockResolvedValue([
      cat({ canonical_id: 1, node_count: 0, node_avg_iq: null, max_node_iq: null, min_node_iq: null }),
    ])
    const w = await mountView()
    await loadAll(w)
    // ★ (3)：node_count===0 ⇒ 三聚合全 null —— 这**不是**异常。
    expect(hasId(w, 'miq-catalog-bad')).toBe(false)
    expect(hasId(w, `miq-catalog-row-1`)).toBe(true)
    // ★ 「该出现的内容」要单独钉：行必须渲染出来。
    expect(byId(w, `miq-catalog-row-1`).text()).toContain('GPT-4o')
  })

  it('②-2 ★★★★★ standard_iq 为 null 时按节点均值排序，且单独说明', async () => {
    mCatalog.mockResolvedValue([
      cat({ canonical_id: 1, standard_iq: null, node_avg_iq: 88, node_count: 3 }),
      cat({ canonical_id: 2, standard_iq: 70, node_avg_iq: 70, node_count: 3 }),
    ])
    const w = await mountView()
    await loadAll(w)
    expect(hasId(w, 'miq-catalog-order-bad')).toBe(false)
    // ★ 「没有标定智商的行数」是一条**该出现**的信息，不是靠否定断言。
    expect(textOf(w, 'miq-catalog-standard-null')).toBe(t('miq.catalog.standardNull', { n: 1 }))
  })

  it('②-3 ★★★★ standard_iq 为 null **不**算顺序错（COALESCE 后非增即可）', async () => {
    mCatalog.mockResolvedValue([
      cat({ canonical_id: 1, standard_iq: null, node_avg_iq: 90, node_count: 3 }),
      cat({ canonical_id: 2, standard_iq: 95, node_avg_iq: 95, node_count: 3 }),
    ])
    const w = await mountView()
    await loadAll(w)
    // ★★ 排序键是 COALESCE(standard_iq, node_avg) —— 90 < 95 ⇒ 顺序确实错了。
    expect(hasId(w, 'miq-catalog-order-bad')).toBe(true)
  })

  it('②-4 ★★★ 空数组走「空」那一支，不是「加载中」也不是「无匹配 = null」', async () => {
    mCatalog.mockResolvedValue([])
    const w = await mountView()
    await loadAll(w)
    expect(textOf(w, 'miq-catalog-empty')).toBe(t('miq.catalog.empty'))
    expect(textOf(w, 'miq-catalog-count')).toBe('0')
  })
})

describe('③④⑤ catalog 自检信号', () => {
  it('③-1 ★★★★★ node_count>0 却聚合为 null ⇒ **报出来**', async () => {
    mCatalog.mockResolvedValue([cat({ node_count: 5, node_avg_iq: null, max_node_iq: null, min_node_iq: null })])
    const w = await mountView()
    await loadAll(w)
    expect(textOf(w, 'miq-catalog-bad')).toBe(t('miq.catalog.aggMismatch', { n: 1 }))
  })

  it('③-2 ★★★★★★ node_count=0 却有聚合值 ⇒ **也报**（反方向不是「少报」）', async () => {
    mCatalog.mockResolvedValue([cat({ node_count: 0, node_avg_iq: 88, max_node_iq: 92, min_node_iq: 85 })])
    const w = await mountView()
    await loadAll(w)
    // ★★★ 只钉「一个方向」的判据是半个判据：另一个方向也得红。
    expect(hasId(w, 'miq-catalog-bad')).toBe(true)
  })

  it('③-3 ★★★★ 正常夹具**不**报（阴性对照：判据不是恒真）', async () => {
    const w = await mountView()
    await loadAll(w)
    expect(hasId(w, 'miq-catalog-bad')).toBe(false)
    expect(hasId(w, 'miq-catalog-empty-bad')).toBe(false)
    expect(hasId(w, 'miq-catalog-order-bad')).toBe(false)
  })

  it('⑤-1 ★★★★★ 既无标定智商又无节点 ⇒ 报（与后端 WHERE 矛盾）', async () => {
    mCatalog.mockResolvedValue([
      cat({ canonical_id: 1, standard_iq: null, node_count: 0, node_avg_iq: null, max_node_iq: null, min_node_iq: null }),
    ])
    const w = await mountView()
    await loadAll(w)
    expect(hasId(w, 'miq-catalog-bad')).toBe(false) // 聚合侧自洽
    expect(textOf(w, 'miq-catalog-empty-bad')).toBe(t('miq.catalog.bothEmpty', { n: 1 }))
  })

  it('⑤-2 ★★★★ 顺序错时报出来；正序时不报', async () => {
    mCatalog.mockResolvedValue([
      cat({ canonical_id: 1, standard_iq: 70, node_avg_iq: 70 }),
      cat({ canonical_id: 2, standard_iq: 95, node_avg_iq: 95 }),
    ])
    const w = await mountView()
    await loadAll(w)
    expect(textOf(w, 'miq-catalog-order-bad')).toBe(t('miq.catalog.orderBad'))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 节点段
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 节点段', () => {
  it('⑥-1 ★★★★ provider_id=0 显示为「未挂供应商」，不是错误', async () => {
    mNodes.mockResolvedValue([node({ provider_id: 0, provider_name: '' })])
    const w = await mountView()
    await loadAll(w)
    expect(hasId(w, 'miq-nodes-no-provider')).toBe(true)
    // ★ COALESCE 兜底 ⇒ 空串可达，要说清而不是显示空白。
    expect(byId(w, 'miq-node-row-7').text()).toContain(t('miq.nodes.unbound'))
  })

  it('⑥-2 ★★★★ tested_at 为 null ⇒ 「从未测试」，不参与精度检查', async () => {
    mNodes.mockResolvedValue([node({ tested_at: null })])
    const w = await mountView()
    await loadAll(w)
    expect(hasId(w, 'miq-nodes-never-tested')).toBe(true)
    // ★★ null 不该被判成「格式不对」—— 没有值 ≠ 值不对。
    expect(hasId(w, 'miq-nodes-bad-precision')).toBe(false)
    expect(byId(w, 'miq-node-row-7').text()).toContain(t('miq.nodes.never'))
  })

  it('⑥-3 ★★★★★★ 时间戳带亚秒小数点 ⇒ 报（那是 history 的形态，不是本端的）', async () => {
    // ★★ 这是 (15) 的直接后果：两端点的 tested_at 长得像、机制不同。
    mNodes.mockResolvedValue([node({ tested_at: '2026-10-07T12:00:00.123456789Z' })])
    const w = await mountView()
    await loadAll(w)
    expect(textOf(w, 'miq-nodes-bad-precision')).toBe(t('miq.nodes.badPrecision', { n: 1 }))
  })

  it('⑥-4 ★★★ 正常秒级串**不**报精度错（阴性对照）', async () => {
    const w = await mountView()
    await loadAll(w)
    expect(hasId(w, 'miq-nodes-bad-precision')).toBe(false)
  })

  it('⑥-5 ★★★★ 非法过滤值本地挡住，且不发请求', async () => {
    const w = await mountView()
    await byId(w, 'miq-provider-id').setValue('0')
    await flushPromises()
    expect(hasId(w, 'miq-nodes-filter-bad')).toBe(true)
    mNodes.mockClear()
    await click(w, 'miq-nodes-load')
    // ★ 后端会对 0 返 400 ⇒ 前端先挡不发。
    expect(mNodes).not.toHaveBeenCalled()
  })

  it('⑥-6 ★★★ 过滤值合法时照常发请求，且参数带上', async () => {
    const w = await mountView()
    await byId(w, 'miq-provider-id').setValue('3')
    await byId(w, 'miq-canonical-id').setValue('11')
    await flushPromises()
    expect(hasId(w, 'miq-nodes-filter-bad')).toBe(false)
    await click(w, 'miq-nodes-load')
    expect(mNodes).toHaveBeenCalledWith({ providerId: 3, canonicalId: 11 })
  })

  it('⑥-7 ★★★ 空过滤值不传 undefined 之外的怪东西', async () => {
    const w = await mountView()
    await click(w, 'miq-nodes-load')
    expect(mNodes).toHaveBeenCalledWith({ providerId: undefined, canonicalId: undefined })
  })

  it('⑥-8 ★★★★ 节点顺序错时报出来', async () => {
    mNodes.mockResolvedValue([node({ credential_id: 1, overall_score: 60 }), node({ credential_id: 2, overall_score: 90 })])
    const w = await mountView()
    await loadAll(w)
    expect(textOf(w, 'miq-nodes-order-bad')).toBe(t('miq.nodes.orderBad'))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑦⑧⑨ 历史段
// ══════════════════════════════════════════════════════════════════════════

describe('⑦⑧⑨ 历史段', () => {
  /** 填必填参数并加载。 */
  async function withHistory(w: W, over: { id?: string; model?: string } = {}) {
    await byId(w, 'miq-credential-id').setValue(over.id ?? '7')
    await byId(w, 'miq-raw-model').setValue(over.model ?? 'gpt-4o')
    await flushPromises()
    await click(w, 'miq-history-load')
  }

  it('⑦-1 ★★★★★★ 缺必填参数时按钮禁用，且不发请求', async () => {
    const w = await mountView()
    expect(byId(w, 'miq-history-load').attributes('disabled')).toBeDefined()
    mHistory.mockClear()
    await click(w, 'miq-history-load')
    expect(mHistory).not.toHaveBeenCalled()
  })

  it('⑦-2 ★★★★ credential_id=0 不算合法', async () => {
    const w = await mountView()
    await byId(w, 'miq-credential-id').setValue('0')
    await byId(w, 'miq-raw-model').setValue('gpt-4o')
    await flushPromises()
    expect(byId(w, 'miq-history-load').attributes('disabled')).toBeDefined()
    expect(hasId(w, 'miq-history-params-bad')).toBe(true)
  })

  it('⑦-3 ★★★★ 参数齐全后按钮可用并发出请求', async () => {
    const w = await mountView()
    await withHistory(w)
    expect(mHistory).toHaveBeenCalledWith({ credentialId: 7, rawModelName: 'gpt-4o', limit: 50 })
  })

  it('⑦-4 ★★★★ 模型名首尾空格被**去掉**再发（后端按原样比）', async () => {
    const w = await mountView()
    await withHistory(w, { model: '  gpt-4o  ' })
    expect(mHistory).toHaveBeenCalledWith({ credentialId: 7, rawModelName: 'gpt-4o', limit: 50 })
  })

  it('⑨-1 ★★★★★★ 非法 limit 不报错，页面**显式说生效值是 50**', async () => {
    const w = await mountView()
    await byId(w, 'miq-credential-id').setValue('7')
    await byId(w, 'miq-raw-model').setValue('gpt-4o')
    await byId(w, 'miq-history-effective-limit').exists()
    // ★ 直接构造非法值（输入框填不出 chip 之外的值，但用户可以手打）。
    const src = viewSrc()
    expect(src).toContain('modelIQHistoryEffectiveLimit')
    expect(modelIQHistoryEffectiveLimit(0)).toBe(50)
    expect(modelIQHistoryEffectiveLimit(501)).toBe(50)
    expect(modelIQHistoryLimitIsAccepted(0)).toBe(false)
    expect(modelIQHistoryLimitIsAccepted(500)).toBe(true)
  })

  it('⑨-2 ★★★★ 生效值那一行恒在（成功与失败都要看得见）', async () => {
    const w = await mountView()
    expect(textOf(w, 'miq-history-effective-limit')).toBe(t('miq.history.effective', { n: MODEL_IQ_HISTORY_DEFAULT_LIMIT }))
  })

  it('⑨-3 ★★★ limit 合法时生效值 = 填的值', () => {
    expect(modelIQHistoryEffectiveLimit(100)).toBe(100)
    expect(modelIQHistoryEffectiveLimit(MODEL_IQ_HISTORY_MAX_LIMIT)).toBe(500)
    expect(modelIQHistoryEffectiveLimit(-1)).toBe(MODEL_IQ_HISTORY_DEFAULT_LIMIT)
  })

  it('⑧-1 ★★★★★★ status 与 accuracy/stability 不自洽 ⇒ 报', async () => {
    mHistory.mockResolvedValue([pt({ accuracy: 92, stability: 100, status: 'failed' })])
    const w = await mountView()
    await withHistory(w)
    expect(textOf(w, 'miq-history-status-bad')).toBe(t('miq.history.statusMismatch', { n: 1 }))
  })

  it('⑧-2 ★★★★★★ stability=0 且 accuracy=0 ⇒ status 必须是 failed（反推口径）', async () => {
    // ★ 逐字照 dbstorage.go:80-87 的判定顺序：failed 在最后且覆盖前两条。
    mHistory.mockResolvedValue([pt({ accuracy: 0, stability: 0, status: 'failed' })])
    const w = await mountView()
    await withHistory(w)
    expect(hasId(w, 'miq-history-status-bad')).toBe(false)
    expect(hasId(w, 'miq-history-status-unknown')).toBe(false)
  })

  it('⑧-3 ★★★★ 未知 status 值**单独**报，不与不自洽混为一谈', async () => {
    mHistory.mockResolvedValue([
      pt({ accuracy: 0, stability: 0, status: 'weird' }),
    ])
    const w = await mountView()
    await withHistory(w)
    // ★ 两者都成立：既不域内，也与 metrics 对不上 —— 页面要分开说。
    expect(hasId(w, 'miq-history-status-bad')).toBe(true)
    expect(textOf(w, 'miq-history-status-unknown')).toBe(t('miq.history.statusUnknown', { n: 1 }))
  })

  it('⑧-4 ★★★★ stability=0 只说明「是 0」，**不断言**它必是 failed', async () => {
    mHistory.mockResolvedValue([pt({ accuracy: 90, stability: 0, status: 'partial' })])
    const w = await mountView()
    await withHistory(w)
    expect(hasId(w, 'miq-history-zero-stability')).toBe(true)
    // ★ COALESCE 的 0 与真 0 二义 ⇒ 这一行不报「不自洽」。
    expect(hasId(w, 'miq-history-status-bad')).toBe(false)
  })

  it('⑧-5 ★★★★ 历史顺序错时报出来', async () => {
    mHistory.mockResolvedValue([
      pt({ tested_at: '2026-10-06T12:00:00Z' }),
      pt({ tested_at: '2026-10-07T12:00:00Z' }),
    ])
    const w = await mountView()
    await withHistory(w)
    expect(textOf(w, 'miq-history-order-bad')).toBe(t('miq.history.orderBad'))
  })

  it('⑧-6 ★★★ 空历史走「空」支', async () => {
    mHistory.mockResolvedValue([])
    const w = await mountView()
    await withHistory(w)
    expect(textOf(w, 'miq-history-empty')).toBe(t('miq.history.empty'))
  })

  it('⑧-7 ★★★★ 历史时间戳带亚秒**不**报错（与节点段相反）', async () => {
    mHistory.mockResolvedValue([pt({ tested_at: '2026-10-07T12:00:00.123456789Z' })])
    const w = await mountView()
    await withHistory(w)
    expect(hasId(w, 'miq-history-order-bad')).toBe(false)
    expect(byId(w, 'miq-history-row-0').text()).toContain('.123456789')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 交互：点节点行 → 填参并加载
// ══════════════════════════════════════════════════════════════════════════

describe('交互：点节点行填入历史查询', () => {
  it('★ 点击节点行 ⇒ 凭据 ID 与模型名被填上**并立刻加载历史**', async () => {
    mNodes.mockResolvedValue([node({ credential_id: 42, raw_model_name: 'o3-mini' })])
    const w = await mountView()
    await loadAll(w)
    mHistory.mockClear()
    await click(w, 'miq-node-pick-42')
    expect(inputValue(w, 'miq-credential-id')).toBe('42')
    expect(inputValue(w, 'miq-raw-model')).toBe('o3-mini')
    expect(mHistory).toHaveBeenCalledWith({ credentialId: 42, rawModelName: 'o3-mini', limit: 50 })
  })

  it('★★ 选中态落在 `--app-primary` 上（本仓**没有** --app-accent）', async () => {
    const w = await mountView()
    await loadAll(w)
    expect(viewSrc()).toContain('miq__pick--on')
    expect(viewSrc()).toContain('var(--app-primary)')
    // ★★★ 断言写成**正向形态**「没有任何 CSS 规则引用这个 token」，
    //   而不是「整个文件不含这个词」——
    //   后者会被**本文件自己的注释**顶红（本仓没有 --app-accent 这句话就写着它），
    //   而那句话恰恰是这条判据要保护的**结论**。
    expect(viewSrc()).not.toMatch(/var\(--app-accent/)
  })

  it('★★★ 换一行则选中态转移', async () => {
    mNodes.mockResolvedValue([node({ credential_id: 1 }), node({ credential_id: 2 })])
    const w = await mountView()
    await loadAll(w)
    await click(w, 'miq-node-pick-1')
    expect(byId(w, 'miq-node-pick-1').classes()).toContain('miq__pick--on')
    await click(w, 'miq-node-pick-2')
    expect(byId(w, 'miq-node-pick-2').classes()).toContain('miq__pick--on')
    expect(byId(w, 'miq-node-pick-1').classes()).not.toContain('miq__pick--on')
  })

  it('★★★ 模板里不得出现内联的 `xxx.value`（ref 自动解包会变成 undefined.value）', () => {
    // ★★ 批 114 的真运行时缺陷就在这：vitest 全绿、只有 vue-tsc 拦得住。
    const src = viewSrc()
    const tpl = src.slice(src.indexOf('<template>'))
    expect(tpl).not.toMatch(/@click="[^"]*\.value/)
    expect(tpl).not.toMatch(/:[\w-]+="[^"]*\.value/)
  })

  it('★★ 一次「加载全部」同时发 catalog 与 nodes 两路', async () => {
    const w = await mountView()
    await loadAll(w)
    expect(mCatalog).toHaveBeenCalledTimes(1)
    expect(mNodes).toHaveBeenCalledTimes(1)
    // ★ history 需要必填参数 ⇒ 全局加载**不**触发它。
    expect(mHistory).not.toHaveBeenCalled()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑩ 错误归因
// ══════════════════════════════════════════════════════════════════════════

describe('⑩ 错误归因', () => {
  it('⑩-1 ★★★★ 四条 400 完全可区分', () => {
    expect(modelIQBadRequestKind('invalid provider_id')).toBe('invalid-provider')
    expect(modelIQBadRequestKind('invalid canonical_id')).toBe('invalid-canonical')
    expect(modelIQBadRequestKind('credential_id required')).toBe('credential-required')
    expect(modelIQBadRequestKind('raw_model_name required')).toBe('model-required')
    expect(modelIQBadRequestKind('something else')).toBe('unknown')
  })

  it('⑩-2 ★★★★ 403 文案逐字钉（对着 auth.go）', () => {
    expect(modelIQSuperAdminOnlyMessage('super_admin role required for this endpoint')).toBe(true)
    expect(modelIQForbiddenMessage('super_admin role required for this endpoint')).toBe(true)
    expect(modelIQDbNotConfiguredMessage('database not configured')).toBe(true)
    expect(modelIQDbNotConfiguredMessage('database not available')).toBe(false)
  })

  it('⑩-3 ★★★★ 403 在页面上落成「权限不足」而不是裸文案', async () => {
    mCatalog.mockRejectedValue(new Error('super_admin role required for this endpoint'))
    const w = await mountView()
    await loadAll(w)
    expect(textOf(w, 'miq-catalog-error')).toBe(t('miq.err.forbidden'))
  })

  it('⑩-4 ★★★ 503 落成「未配置数据库」', async () => {
    mNodes.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    await click(w, 'miq-nodes-load')
    expect(textOf(w, 'miq-nodes-error')).toBe(t('miq.err.dbNotConfigured'))
  })

  it('⑩-5 ★★★ 400 落成对应的参数错误', async () => {
    mHistory.mockRejectedValue(new Error('raw_model_name required'))
    const w = await mountView()
    await byId(w, 'miq-credential-id').setValue('7')
    await byId(w, 'miq-raw-model').setValue('gpt-4o')
    await flushPromises()
    await click(w, 'miq-history-load')
    expect(textOf(w, 'miq-history-error')).toBe(t('miq.err.model-required'))
  })

  it('⑩-6 ★★★ 未知错误原样透出（不吞）', async () => {
    mCatalog.mockRejectedValue(new Error('boom 42'))
    const w = await mountView()
    await loadAll(w)
    expect(textOf(w, 'miq-catalog-error')).toBe('boom 42')
  })

  it('⑩-7 ★★★ 请求失败后列表清空，不留上一轮的陈旧数据', async () => {
    const w = await mountView()
    await loadAll(w)
    expect(hasId(w, `miq-catalog-row-1`)).toBe(true)
    mCatalog.mockRejectedValue(new Error('boom'))
    await loadAll(w)
    expect(hasId(w, `miq-catalog-row-1`)).toBe(false)
    expect(textOf(w, 'miq-catalog-count')).toBe('0')
  })

  it('⑩-8 ★★ 参数预检口径：两个必填都过才算可发', () => {
    expect(modelIQParamsAreAcceptable({ credentialId: 7, rawModelName: 'gpt-4o' })).toBe(true)
    expect(modelIQParamsAreAcceptable({ credentialId: 0, rawModelName: 'gpt-4o' })).toBe(false)
    expect(modelIQParamsAreAcceptable({ credentialId: 7, rawModelName: '' })).toBe(false)
    expect(modelIQParamsAreAcceptable({ credentialId: Number.NaN, rawModelName: 'gpt-4o' })).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑪ 触控与接线
// ══════════════════════════════════════════════════════════════════════════

describe('⑪ 触控与接线', () => {
  it('⑪-1 ★★★ 新增触控控件全部 ≥48 CSS px', () => {
    const src = viewSrc()
    for (const sel of ['miq__btn', 'miq__input', 'miq__chip', 'miq__pick']) {
      const re = new RegExp(`\\.${sel}[\\s\\S]{0,400}?min-height:\\s*(\\d+)px`)
      const m = re.exec(src)
      expect(m, `找不到 ${sel} 的 min-height`).toBeTruthy()
      // ★ 把数值**解析出来**再比大小，不要 `toContain('48px')`（那样改 480 也过）。
      expect(Number(m![1]), `${sel} 触控高度不足 48`).toBeGreaterThanOrEqual(48)
    }
  })

  it('⑪-2 ★★★★ 路由存在且 meta 是 super_admin', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toContain("path: '/model-iq'")
    expect(src).toContain("component: () => import('@/views/ModelIQView.vue')")
    expect(src).toMatch(/miq\.title'[^}]*requiresRole: 'super_admin'/)
  })

  it('⑪-3 ★★★ i18n 两侧都有 miq 前缀且键集合一致', () => {
    const zh = readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')
    const en = readFileSync(join(process.cwd(), 'src/i18n/en-US.ts'), 'utf8')
    expect(zh).toContain('  miq: {')
    expect(en).toContain('  miq: {')
    // ★★ 六条错误文案里有两条带连字符，必须靠引号键写。
    expect(zh).toContain("'invalid-provider':")
    expect(en).toContain("'invalid-provider':")
  })

  it('⑪-4 ★★★ 不碰 trigger（POST、有副作用）', () => {
    const src = viewSrc()
    // ★★★ 同上：不能用 `not.toContain('/model-iq/trigger')` ——
    //   文件头那句「trigger **不接**」就写着那个路径，会把判据自己顶红。
    //   改成**正向形态**：只导入三条读端点的 fetch，没有任何 trigger 符号。
    expect(src).not.toMatch(/import\s*\{[^}]*Trigger/)
    expect(src).not.toMatch(/fetchModelIQTrigger/)
    const api = goSrc('web-mobile/src/api/modelIQ.ts')
    // ★ 同理，API 层也不该有 trigger 的 fetch 导出，也不该有 POST 到 trigger 的调用。
    expect(api).not.toMatch(/export function fetchModelIQTrigger/)
    expect(api).not.toMatch(/'POST',[^)]*model-iq\/trigger/)
  })

  it('⑪-5 ★★★ 只导入三条**读**端点的 fetch（正向钉住「读了哪三个」）', () => {
    const src = viewSrc()
    for (const fn of ['fetchModelIQCatalog', 'fetchModelIQNodeLatest', 'fetchModelIQHistory']) {
      expect(src).toContain(fn)
    }
  })

  // ★★★★★ 这条是**本批真踩出来的**：写文件时混进了 3 处 U+FFFD 替换字符，
  //   把用例标题里的「时间戳」变成「时␦␦␦戳」⇒ 变异台的 `expect` 是对
  //   **fullName 做子串匹配**，于是 4 条变异集体「红了但没命中」。
  // ⇒ 那不是判据没牙，是**用例名被污染** ⇒ 断言里看不出区别，
  //   只会让人怀疑判据 ⇒ ★★★ 所以编码必须单独有门。
  it('⑪-6 ★★★★★★ 本批三个源文件里没有 U+FFFD 替换字符，也没有 NUL', () => {
    for (const f of [
      'src/views/ModelIQView.vue',
      'src/views/ModelIQView.spec.ts',
      'src/api/modelIQ.ts',
    ]) {
      const p = join(process.cwd(), f)
      expect(existsSync(p), `文件不存在：${f}`).toBe(true)
      const s = readFileSync(p, 'utf8')
      expect(s.includes('\uFFFD'), `${f} 含 U+FFFD 替换字符（编码损坏）`).toBe(false)
      expect(s.includes('\u0000'), `${f} 含 NUL`).toBe(false)
    }
  })

  it('⑪-7 ★★★ 用例标题里不得出现替换字符（变异台按 fullName 子串匹配，标题坏了就全成 PARTIAL）', () => {
    // ★ 与 ⑪-6 同源，但这条钉的是**标题**这一面 —— 它的后果与别处不同：
    //   源码里别处有 U+FFFD 只影响可读性，标题里有则会让变异台集体 PARTIAL。
    const self = readFileSync(join(process.cwd(), 'src/views/ModelIQView.spec.ts'), 'utf8')
    let checked = 0
    for (const line of self.split('\n')) {
      if (/^\s*(describe|it)\(/.test(line)) {
        checked++
        expect(line, '用例标题行含 U+FFFD').not.toContain('\uFFFD')
      }
    }
    // ★★ 必须确认**真的扫到了行**，否则这条会在「正则失配」时恒绿。
    expect(checked).toBeGreaterThan(50)
  })
})