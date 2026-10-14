import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import V1DataHorizonView from './V1DataHorizonView.vue'
import { setLocale, t } from '@/i18n'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchV1DataHorizon,
  v1HorizonIsNotFrozen,
  v1HorizonIsFrozen,
  v1HorizonIsUnknown,
  v1HorizonExactlyOneFlag,
  V1_HORIZON_READ_SOURCE,
  V1_HORIZON_GATE_KEY,
  V1_HORIZON_AFFECTS,
  V1_HORIZON_NOTICE_KEYS,
  type V1DataHorizonResponse,
  type V1DataHorizonNotice,
} from '@/api/v1DataHorizon'

/**
 * v1 数据地平线面回归护栏（2026-10-14，第一百一十九批）。
 *
 * ★★★★★★ **权限档与前三批相反，而前三批并没错。**
 *   批 115 / 117 / 118 都归**第三类**（服务端没有租户维度）⇒ 过度限制到 `super_admin`。
 *   本批**同样没有租户维度**，但载荷是**平台级全局事实**（`_request_logs` 写门是否停更），
 *   七个键里**没有一丁点租户相关** ⇒ **不需要**过度限制 ⇒ **admin 档，不设 requiresRole**。
 *   ⇒ ★★★★ 这把第三类的判据钉死了：**过度限制的正当性来自「载荷里有别人的东西」，
 *     不是来自「有没有 tenant 条件」本身。**
 *   ⇒ 而且这里**必须**让 tenant_admin 看到 —— 告示自己写着
 *     「接口照常 200、结果集非空、字段齐全，接口没报错不代表数据是新的」，
 *     挡掉它等于让租户把停更数据读成新鲜数据。
 *
 * ★★★★★★ **本批的另一个核心：一条恒真的互证判据被删掉，换成有判别力的形态。**
 *   旧判据 `expect(V1_HORIZON_READ_SOURCE).toBe('request_logs')` **一直是绿的**，
 *   而 Go 侧两处字面量是 `"_request_logs"`（表名带下划线前缀）
 *   ⇒ 常量与判据**从同一个错误来源写出** ⇒ 错得「有据可依」。
 *   ⇒ 处置：删掉它，换成**从 Go 源码抽取**字面量再比（API 层那三条）。
 *
 * 判据钉九组：
 *  ① ★★★★★★ admin 档：不设 requiresRole，且**不得**进 superAdmin 白名单。
 *  ② ★★★★★ 三态互斥：null ⇒ live；对象+frozen ⇒ frozen；对象+unknown ⇒ unknown。
 *  ③ ★★★★★ 有对象 ⇒ 两个标志**恰好一个**为 true（后端不产出第四种形态）。
 *  ④ ★★★★★ `source` 是表名带 `_` 前缀（本批修掉的真契约错）。
 *  ⑤ ★★★★ `gate_key` 恒为那个控制键（运维要知道改哪里）。
 *  ⑥ ★★★★ `affects` 恒三值且都在后端列表内。
 *  ⑦ ★★★★ `effect` / `silence` 只展示、不参与判状态。
 *  ⑧ ★★★ 错误与陈旧数据；live 档也要有「该出现」的一面。
 *  ⑨ ★★★ 触控 ≥48 CSS px、路由/抽屉/i18n 接线、编码门。
 */

// ── 夹具：逐字照抄 Go 侧两个分支 ────────────────────────────────────────────

/** `:217-224` 的 **frozen 分支**（`Frozen: true`，`Unknown` 未设 ⇒ 零值 false）。 */
function frozenNotice(over: Partial<V1DataHorizonNotice> = {}): V1DataHorizonNotice {
  return {
    frozen: true,
    unknown: false,
    source: V1_HORIZON_READ_SOURCE,
    gate_key: V1_HORIZON_GATE_KEY,
    effect: '本页及基于本页数据的判断只反映停写之前的流量；新请求不再进入这张表。',
    silence: '这一类读点没有任何错误信号：接口照常 200、结果集非空、字段齐全。接口没报错不代表数据是新的。',
    affects: [...V1_HORIZON_AFFECTS],
    ...over,
  }
}

/** `:207-215` 的 **unknown 分支**（`Frozen: false`，`Unknown: true`）。 */
function unknownNotice(over: Partial<V1DataHorizonNotice> = {}): V1DataHorizonNotice {
  return {
    frozen: false,
    unknown: true,
    source: V1_HORIZON_READ_SOURCE,
    gate_key: V1_HORIZON_GATE_KEY,
    effect: '无法确认 v1 读源族的停写状态：该 gate 键在配置里没有显式取值，当前读到的值来自默认值回落。',
    silence: '两种可能都未经验证：写入可能仍在继续，也可能已经停写。',
    affects: [...V1_HORIZON_AFFECTS],
    ...over,
  }
}

/** 主键恒在；值是「七键对象」或**裸 `null`**。见 (1)。 */
function body(notice: V1DataHorizonNotice | null): V1DataHorizonResponse {
  return { v1_data_horizon: notice, '//': 'frozen=true 表示 v1 读源族当前已停更；本对象不存在即表示未停更。' }
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/v1DataHorizon', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/v1DataHorizon')>()
  return { ...actual, fetchV1DataHorizon: vi.fn() }
})

const mFetch = fetchV1DataHorizon as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(V1DataHorizonView)
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
  return readFileSync(join(process.cwd(), 'src/views/V1DataHorizonView.vue'), 'utf8')
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
  mFetch.mockResolvedValue(body(frozenNotice()))
})

async function load(w: W) {
  await byId(w, 'v1h-load').trigger('click').then(() => flushPromises())
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档：admin 档，不设 requiresRole
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档 admin（第三类的边界：没有租户维度，但载荷是全局平台事实）', () => {
  it('①-1 ★★★★★★ 抽屉席存在且**不设** requiresRole', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'v1-data-horizon')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/v1-data-horizon')
    // ★★ 与批 115/117/118 三席**相反**：那三席必须设 super_admin，本席**不许**设。
    expect(seat!.requiresRole).toBeUndefined()
  })

  it('①-2 ★★★★★★ 读 Go 源码：注册处是 **admin(...)**（AdminMiddleware 只认证不检 role）', () => {
    const handler = goSrc('admin/handler.go')
    const m = handler.match(
      /mux\.HandleFunc\("\/api\/admin\/v1-data-horizon", (admin|h\.superAdmin)\(h\.handleV1DataHorizon\)\)/,
    )
    expect(m, '没找到注册行（正则失配会让这条恒绿）').toBeTruthy()
    expect(m![1]).toBe('admin')
  })

  it('①-3 ★★★★★★ 读 Go 源码：payload 里**没有租户维度**（不是忘了做，是它压根不谈租户）', () => {
    const src = goSrc('admin/v1_freeze_notice.go')
    expect(src).not.toMatch(/tenant|Tenant/)
    // ★ 正向钉法：七个键里没有一个是租户相关的。
    const keys = [...src.matchAll(/json:"([a-z_]+)"/g)].map((m) => m[1]!)
    expect(keys).toEqual([...V1_HORIZON_NOTICE_KEYS])
    expect(keys.some((k) => /tenant/i.test(k))).toBe(false)
  })

  it('①-4 ★★★★★★ 页面要说明「为什么 tenant_admin 也能看」', async () => {
    // ★★ 这一句是本族权限结论的**用户可见面**：不写清楚，后来者会照抄前三批
    //   把它改成 super_admin，而那会让租户把停更数据读成新鲜数据。
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-scope-note')).toBe(t('v1h.scope.note'))
    expect(t('v1h.scope.note')).toContain('租户')
  })

  it('①-5 ★★★ 不设 requiresRole ⇒ **不得**进 superAdmin 白名单（反向护栏）', () => {
    // ★ 与 probe-queue / connection-registry / dispatch-journal 同一条纪律：
    //   一旦有人给它加上 super_admin，这条立刻红。
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    expect(src).not.toMatch(/^\s*'v1-data-horizon',$/m)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ②③ 三态
// ══════════════════════════════════════════════════════════════════════════

describe('②③ 三态与强不变量', () => {
  it('②-1 ★★★★★ 值为 `null` ⇒ live（**不是**「键缺失」）', async () => {
    mFetch.mockResolvedValue(body(null))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-state')).toBe(t('v1h.state.live'))
    expect(v1HorizonIsNotFrozen(body(null))).toBe(true)
    // ★ 「该出现」的一面：live 档也要说清为什么没有告示。
    expect(hasId(w, 'v1h-envelope')).toBe(true)
  })

  it('②-2 ★★★★★ frozen 对象 ⇒ frozen 档，且不报任何红线', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-state')).toBe(t('v1h.state.frozen'))
    expect(hasId(w, 'v1h-flag-bad')).toBe(false)
    expect(hasId(w, 'v1h-source-bad')).toBe(false)
    expect(hasId(w, 'v1h-affects-bad')).toBe(false)
    expect(v1HorizonIsFrozen(body(frozenNotice()))).toBe(true)
  })

  it('②-3 ★★★★★ unknown 对象 ⇒ unknown 档（**不是** frozen）', async () => {
    mFetch.mockResolvedValue(body(unknownNotice()))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-state')).toBe(t('v1h.state.unknown'))
    expect(v1HorizonIsUnknown(body(unknownNotice()))).toBe(true)
    expect(v1HorizonIsFrozen(body(unknownNotice()))).toBe(false)
  })

  it('②-4 ★★★★★ unknown 那档的文案**不说**「已停更」也不说「仍在写入」', async () => {
    mFetch.mockResolvedValue(body(unknownNotice()))
    const w = await mountView()
    await load(w)
    const note = textOf(w, 'v1h-state-note')
    expect(note).toBe(t('v1h.state.note.unknown'))
    // ★★★ 后端 `unknown` 分支的文案刻意两边都不说（`v1_freeze_notice.go:207-215`），
    //   前端文案照抄同一口径 ⇒ 断言按**身份**比对，不是 `not.toContain('停更')`
    //   （那两个字在 live 档的说明里合法地出现）。
  })

  it('②-5 ★★★★★ 三态各有独立文案（不是同一句话改数字）', () => {
    const notes = ['live', 'frozen', 'unknown'].map((s) => t(`v1h.state.note.${s}`))
    expect(new Set(notes).size).toBe(3)
  })

  it('③-1 ★★★★★ 两个标志**都**为 false ⇒ 报形状异常（后端不产出这种）', async () => {
    mFetch.mockResolvedValue(body(frozenNotice({ frozen: false, unknown: false })))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-shape-bad')).toBe(true)
    expect(hasId(w, 'v1h-flag-bad')).toBe(true)
    expect(textOf(w, 'v1h-state')).toBe(t('v1h.state.unknown-shape'))
  })

  it('③-2 ★★★★★ 两个标志**都**为 true ⇒ 也报形状异常', async () => {
    // ★★★ 首版这条只钉了 `flag-bad`，而变异台 S6 把 `v1HorizonIsFrozen` 的
    //   `&& n.unknown === false` 去掉后**它没红** —— 因为 state 机先判 IsFrozen，
    //   两标志皆真时它直接返回 'frozen'，于是「形状异常」那一支**根本走不到**，
    //   而 `flag-bad` 是另一条独立的判据，仍然照常报红。
    //   ⇒ 处置是**补断言**（不是改判据）：把「两标志皆真 ⇒ 走形状异常那一支」直接钉住。
    mFetch.mockResolvedValue(body(unknownNotice({ frozen: true })))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-flag-bad')).toBe(true)
    expect(v1HorizonExactlyOneFlag(body(unknownNotice({ frozen: true })))).toBe(false)
    // ★★★ 关键补断言：两个标志皆真时，**不能**被判成 frozen 档。
    expect(textOf(w, 'v1h-state')).toBe(t('v1h.state.unknown-shape'))
    expect(hasId(w, 'v1h-shape-bad')).toBe(true)
  })

  it('③-3 ★★★★ 恰好一个为 true 时**不**报（阴性对照）', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-flag-bad')).toBe(false)
    expect(hasId(w, 'v1h-shape-bad')).toBe(false)
    expect(v1HorizonExactlyOneFlag(body(frozenNotice()))).toBe(true)
    expect(v1HorizonExactlyOneFlag(body(unknownNotice()))).toBe(true)
  })

  it('③-4 ★★★★ 值为 null 时**不**报标志错（那是 live 档，不是违规）', async () => {
    mFetch.mockResolvedValue(body(null))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-flag-bad')).toBe(false)
    expect(v1HorizonExactlyOneFlag(body(null))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④⑤⑥ payload 的三个固定值
// ══════════════════════════════════════════════════════════════════════════

describe('④⑤⑥ source / gate_key / affects', () => {
  it('④-1 ★★★★★★ `source` 是表名，带 `_` 前缀（本批修掉的真契约错）', async () => {
    // ★★★★ 这是本批的核心：`request_logs` ≠ `_request_logs`。
    //   漏前缀的形态正是旧判据锁住的那个（且它是绿的）。
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-source')).toBe('_request_logs')
    expect(hasId(w, 'v1h-source-bad')).toBe(false)
  })

  it('④-2 ★★★★★★ 少了下划线 ⇒ 报错（反向护栏钉住本批那个 bug）', async () => {
    mFetch.mockResolvedValue(body(frozenNotice({ source: 'request_logs' })))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-source-bad')).toBe(true)
    expect(textOf(w, 'v1h-source')).toBe('request_logs')
  })

  it('④-3 ★★★ 源码里**不**许把 source 写成配置来源（命名陷阱 (7)）', () => {
    const src = viewSrc()
    // ★ `notice.source` 是表名；gate 的 source 是 {db,env,default}，**不出现**在响应里。
    expect(src).toContain('V1_HORIZON_READ_SOURCE')
    expect(src).not.toMatch(/V1_GATE_SOURCES/)
  })

  it('⑤-1 ★★★★ `gate_key` 恒为那个控制键（运维要知道改哪里）', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-gate-key')).toBe('storage.request_logs_write_enabled')
    expect(hasId(w, 'v1h-gate-key-bad')).toBe(false)
  })

  it('⑤-2 ★★★★ gate_key 不符 ⇒ 报错', async () => {
    mFetch.mockResolvedValue(body(frozenNotice({ gate_key: 'storage.something_else' })))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-gate-key-bad')).toBe(true)
  })

  it('⑥-1 ★★★★ affects 恒是那三值且都在后端列表内', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-affects-bad')).toBe(false)
    expect(hasId(w, 'v1h-affects-count-bad')).toBe(false)
  })

  it('⑥-2 ★★★★ affects 少一项 ⇒ 报错', async () => {
    mFetch.mockResolvedValue(body(frozenNotice({ affects: ['silently_frozen'] })))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-affects-count-bad')).toBe(true)
  })

  it('⑥-3 ★★★★ affects 多一个未知取值 ⇒ 报错', async () => {
    mFetch.mockResolvedValue(body(frozenNotice({ affects: [...V1_HORIZON_AFFECTS, 'brand_new'] })))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-affects-bad')).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑦⑧ 文案只展示 / 错误与陈旧
// ══════════════════════════════════════════════════════════════════════════

describe('⑦⑧ 文案只展示、错误与陈旧数据', () => {
  it('⑦-1 ★★★★★ effect / silence **原样展示**，不参与判状态', async () => {
    const n = frozenNotice()
    mFetch.mockResolvedValue(body(n))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-effect')).toBe(n.effect)
    expect(textOf(w, 'v1h-silence')).toBe(n.silence)
    // ★★ 把文案换成**别的**内容，状态判据一条都不该变（后端两分支各一套文案）。
    mFetch.mockResolvedValue(body(frozenNotice({ effect: '完全不同的文案', silence: '也是' })))
    const w2 = await mountView()
    await load(w2)
    expect(textOf(w2, 'v1h-state')).toBe(t('v1h.state.frozen'))
    expect(hasId(w2, 'v1h-shape-bad')).toBe(false)
  })

  it('⑦-2 ★★★ silence 那句要出现在屏上（它是最要紧的一句）', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-silence')).toContain('200')
  })

  it('⑧-1 ★★★ 未知错误原样透出（不吞）', async () => {
    mFetch.mockRejectedValue(new Error('boom 42'))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'v1h-error')).toBe('boom 42')
  })

  it('⑧-2 ★★★★ 请求失败后清空上一轮数据，不留陈旧', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'v1h-detail')).toBe(true)
    mFetch.mockRejectedValue(new Error('boom'))
    await load(w)
    expect(hasId(w, 'v1h-detail')).toBe(false)
    expect(hasId(w, 'v1h-state')).toBe(false)
    expect(hasId(w, 'v1h-error')).toBe(true)
  })

  it('⑧-3 ★★★ 未读取时显示「尚未读取」', async () => {
    const w = await mountView()
    expect(mFetch).not.toHaveBeenCalled()
    expect(hasId(w, 'v1h-empty')).toBe(true)
    expect(hasId(w, 'v1h-state')).toBe(false)
  })

  it('⑧-4 ★★★ 页面要说明「响应头存在但移动端只用响应体」', async () => {
    const w = await mountView()
    expect(textOf(w, 'v1h-header-note')).toBe(t('v1h.scope.headerNote'))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑨ 触控与接线
// ══════════════════════════════════════════════════════════════════════════

describe('⑨ 触控与接线', () => {
  it('⑨-1 ★★★ 新增触控控件 ≥48 CSS px', () => {
    const re = /\.v1h__btn\s*\{[\s\S]{0,200}?min-height:\s*(\d+)px/.exec(viewSrc())
    expect(re, '找不到 v1h__btn 的 min-height').toBeTruthy()
    // ★ 把数值**解析出来**再比大小，不要 `toContain('48px')`（写 480 也过）。
    expect(Number(re![1]), '触控高度不足 48').toBeGreaterThanOrEqual(48)
  })

  it('⑨-2 ★★★★ 路由存在且 meta **没有** requiresRole', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toContain("path: '/v1-data-horizon'")
    expect(src).toContain("component: () => import('@/views/V1DataHorizonView.vue')")
    // ★★ 本族的权限结论就在这一行上：多了 requiresRole 立刻红。
    expect(src).toContain("meta: { titleKey: 'v1h.title', requiresAuth: true }")
    expect(src).not.toMatch(/v1h\.title'[^\n]*requiresRole/)
  })

  it('⑨-3 ★★★ i18n 两侧都有 v1h 前缀与 nav 标题键', () => {
    const zh = readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')
    const en = readFileSync(join(process.cwd(), 'src/i18n/en-US.ts'), 'utf8')
    expect(zh).toContain('  v1h: {')
    expect(en).toContain('  v1h: {')
    expect(zh).toContain("v1DataHorizon: 'v1 数据地平线',")
    expect(en).toContain("v1DataHorizon: 'v1 data horizon',")
  })

  it('⑨-4 ★★★ 本批源文件里没有 U+FFFD，也没有 NUL', () => {
    for (const f of ['src/views/V1DataHorizonView.vue', 'src/views/V1DataHorizonView.spec.ts', 'src/api/v1DataHorizon.ts']) {
      const p = join(process.cwd(), f)
      expect(existsSync(p), `文件不存在：${f}`).toBe(true)
      const s = readFileSync(p, 'utf8')
      expect(s.includes('\uFFFD'), `${f} 含 U+FFFD 替换字符（编码损坏）`).toBe(false)
      expect(s.includes('\u0000'), `${f} 含 NUL`).toBe(false)
    }
  })

  it('⑨-5 ★★★ 用例标题里不得出现替换字符（变异台按 fullName 子串匹配）', () => {
    const self = readFileSync(join(process.cwd(), 'src/views/V1DataHorizonView.spec.ts'), 'utf8')
    let checked = 0
    for (const line of self.split('\n')) {
      if (/^\s*(describe|it)\(/.test(line)) {
        checked++
        expect(line, '用例标题行含 U+FFFD').not.toContain('\uFFFD')
      }
    }
    // ★ 必须确认**真的扫到了行**，否则这条会在「正则失配」时恒绿。
    // ★★ 首版这里写的是 40，而本文件实际只有 38 行 ⇒ **这条自己把自己顶红了**。
    //   它是自检门按设计工作的例子：阈值应当**低于**真实行数，否则它只会恒红或恒绿，
    //   不再是「真的扫到了吗」的探针。⇒ 取一个明显低于真实值的下限。
    expect(checked).toBeGreaterThan(30)
  })
})