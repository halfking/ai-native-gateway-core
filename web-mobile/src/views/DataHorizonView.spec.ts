import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import DataHorizonView from './DataHorizonView.vue'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  fetchDataHorizon,
  DATA_HORIZON_SOURCE,
  DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES,
  DATA_HORIZON_LAG_TOLERANCE_MINUTES,
  type DataHorizonNotice,
  type DataHorizonResponse,
} from '@/api/dataHorizon'

/**
 * 流量数据地平线面回归护栏（2026-10-15，第一百二十九批）。
 *
 * ★★★★★ 本族**最要紧的一条纪律**：这一族**不设** `requiresRole`，
 *   而这是**有理由的**，不是遗漏 —— 判据必须把这个理由钉住，
 *   否则下一个人看到「admin 档却没设门」就会"顺手补上"，把告示挡在
 *   tenant_admin 之外，而 `silence` 自己写着
 *   「接口没报错不代表数据是新的」。
 *
 * ★★★ 与前一代（`V1DataHorizonView`）的差异不是改名，判据逐条钉死：
 *   · 三态 `live/frozen/unavailable` → **`live/stale/unknown`**
 *   · 删掉 `affects` / `gate_key` 两行展示与两条自检红线，新增 `hint`
 *   · 新增两个时间窗的展示（判据参数，前端**不参与**判定）
 *
 * 判据钉五组：
 *  ① ★★★★★★ 权限：**不设** requiresRole，且理由钉住（三态对所有角色可见）。
 *  ② ★★★★★★ 三态渲染：live 时**没有** notice 对象、stale/unknown 各一套文案。
 *  ③ ★★★★★ 自检红线三条都能被变异打掉。
 *  ④ ★★★★ 已下线的展示行不得复活（affects / gate_key）。
 *  ⑤ ★★★★ 接线、触控尺寸、编码门、两语种键。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function staleNotice(): DataHorizonNotice {
  return {
    stale: true,
    unknown: false,
    source: DATA_HORIZON_SOURCE,
    effect: '网关仍在接收请求，但 session_turns 未记录到新数据。',
    silence: '这类断流没有任何错误信号：统计接口照常返回、数字齐全。',
    hint: '排查方向：session 镜像写入路径与 sessions_v2 写门。',
  }
}
function unknownNotice(): DataHorizonNotice {
  return {
    stale: false,
    unknown: true,
    source: DATA_HORIZON_SOURCE,
    effect: '无法确认流量数据状态：session_turns 读取失败。',
    silence: '两种可能都未经验证。',
    hint: '排查方向：数据库连通性 / session_turns_hot 表是否存在。',
  }
}
function envelope(n: DataHorizonNotice | null): DataHorizonResponse {
  return { data_horizon: n, '//': 'stale=true 表示…' }
}

// ── 装配 ────────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})
vi.mock('@/api/dataHorizon', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dataHorizon')>()
  return { ...actual, fetchDataHorizon: vi.fn() }
})

const mFetch = fetchDataHorizon as unknown as ReturnType<typeof vi.fn>

const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}

/** ★★★ 剥掉注释，只留代码。否定断言必须否**可执行代码**而非词。 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/DataHorizonView.vue'), 'utf8')
}
/** ★ 本 spec 自己的源码 —— 用于「钉判据自身」的负控之负控。 */
function thisFileSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/DataHorizonView.spec.ts'), 'utf8')
}
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}

async function mountView() {
  const w = mount(DataHorizonView)
  await flushPromises()
  return w
}
async function loadAndSettle(w: Awaited<ReturnType<typeof mountView>>) {
  await w.find('[data-testid="dh-load"]').trigger('click')
  await flushPromises()
}

// ── ① 权限 ──────────────────────────────────────────────────────────────────

describe('① ★★★★★★ 权限：不设 requiresRole，且理由钉住', () => {
  it('①-1 ★★★★★★ 抽屉席**没有** requiresRole 字段（★ 缺省即"对所有角色可见"）', () => {
    const seat = DRAWER_NAV.find((n) => n.key === 'data-horizon')
    expect(seat, '抽屉里没有这一席 ⇒ 页面不可达').toBeTruthy()
    // ★★ 注意是「字段不存在」而不是「值为 undefined」——
    //   `navItemsFor` 对缺省会命中默认分支 `return true`，两种写法行为相同，
    //   但**显式 undefined 会被读成「作者想过这件事」，而事实是「没想过」**。
    expect(seat!.requiresRole).toBeUndefined()
    expect(Object.prototype.hasOwnProperty.call(seat!, 'requiresRole')).toBe(false)
  })

  it('①-2 ★★★★★★ 三个角色都能看到这一席（★ 这就是「不设门」的可见后果）', () => {
    for (const r of ['super_admin', 'tenant_admin', 'admin_key', 'user']) {
      expect(
        navItemsFor(DRAWER_NAV, r).map((n) => n.key),
        `${r} 看不到这一席 ⇒ 门被收窄了，而 tenant_admin 必须看得见`,
      ).toContain('data-horizon')
    }
  })

  it('①-3 ★★★★★★ 理由钉住：载荷是平台级全局事实（六键无一带租户）', () => {
    // ★ 逐键断言「不含租户」—— 用常量派生，不是写死 6
    const keys = ['stale', 'unknown', 'source', 'effect', 'silence', 'hint']
    for (const k of keys) {
      expect(keys.some((x) => x.toLowerCase().includes('tenant')), `${k} 像租户字段`).toBe(false)
      expect(keys.some((x) => x.toLowerCase().includes('tenant_id')), `${k} 像租户字段`).toBe(false)
    }
    // ★★ 服务端那一侧：admin 档 + 载荷无租户维度 ⇒ 不过度限制
    const handler = goSrc('admin/handler.go')
    expect(handler).toMatch(
      /mux\.HandleFunc\("\/api\/admin\/data-horizon", admin\(h\.handleDataHorizon\)\)/,
    )
    const h = goSrc('admin/data_horizon.go')
    // 六个字段，无一带 tenant
    const fields = [...h.matchAll(/json:"([a-z_]+)"/g)].map((m) => m[1])
    expect(fields).toEqual(['stale', 'unknown', 'source', 'effect', 'silence', 'hint'])
    for (const f of fields) expect(f!.includes('tenant'), `${f} 带租户`).toBe(false)
  })

  it('①-4 ★★★★ 路由不设 requiresRole，但仍要 requiresAuth', () => {
    const router = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(router).toMatch(/titleKey: 'dh\.title', requiresAuth: true \}/)
  })

  it('①-5 ★★★★ 「不得进 superAdmin 白名单」这条纪律仍然成立', () => {
    const drawer = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    const at = drawer.indexOf('superAdmin')
    expect(at, 'AppDrawer.spec.ts 里找不到 superAdmin 白名单').toBeGreaterThan(-1)
    expect(drawer.slice(at, at + 1400)).not.toContain("'data-horizon'")
  })
})

// ── ② 三态渲染 ──────────────────────────────────────────────────────────────

describe('② ★★★★★★ 三态：live 无对象 / stale / unknown 各一套', () => {
  it('②-1 ★★★★★★ live（主键为 null）时**不得**渲染任何 notice 行', async () => {
    mFetch.mockResolvedValue(envelope(null))
    const w = await mountView()
    await loadAndSettle(w)
    // ⚠️ `t()` 运行时默认语言不是 zh-CN ⇒ 不与中文字面量比对，
    //   只断言「状态徽章不是畸形态」且 notice 行没渲染（批 127 的同一条纪律）。
    expect(w.find('[data-testid="dh-state"]').text()).not.toContain('malformed')
    expect(w.find('[data-testid="dh-detail"]').exists(), 'live 时渲染了 notice 行').toBe(false)
    expect(w.find('[data-testid="dh-silence"]').exists()).toBe(false)
    expect(w.find('[data-testid="dh-flag-bad"]').exists(), 'live 时报了自检红线').toBe(false)
  })

  it('②-2 ★★★★★★ stale 与 unknown 各有独立文案（★ 不得共用一套）', async () => {
    mFetch.mockResolvedValue(envelope(staleNotice()))
    const wS = await mountView()
    await loadAndSettle(wS)
    expect(wS.find('[data-testid="dh-state"]').text()).toContain('stalled')
    const staleEffect = wS.find('[data-testid="dh-effect"]').text()
    // ★★★ 必须连 **`dh-state-note`（页面自己的三态说明）** 一起比：
    //   变异台 M30 把 `t(\`dh.state.note.${state}\`)` 硬编码成 `live` 全绿，
    //   因为②-2 只断言了 `dh-effect`（那是**后端**的文案，页面改不了）。
    const staleNote = wS.find('[data-testid="dh-state-note"]').text()

    mFetch.mockResolvedValue(envelope(unknownNotice()))
    const wU = await mountView()
    await loadAndSettle(wU)
    expect(wU.find('[data-testid="dh-state"]').text()).toContain('unreadable')
    const unkEffect = wU.find('[data-testid="dh-effect"]').text()

    const unkNote = wU.find('[data-testid="dh-state-note"]').text()
    // ★★★ 两套文案必须不同，且都不该用对方的关键词
    expect(staleEffect).not.toBe(unkEffect)
    expect(staleEffect).not.toContain('cannot be read')
    expect(unkEffect).not.toContain('not recorded')
    // ★★★ 页面自己的三态说明也必须**逐态不同**（M30 的打靶处）
    expect(staleNote, 'stale 的 state-note 与 unknown 相同').not.toBe(unkNote)
    expect(staleNote).not.toBe(unkEffect)
    expect(unkNote).not.toBe(staleEffect)
  })

  it('②-3 ★★★★★★ unknown 档也要显示 source 与两个时间窗', async () => {
    mFetch.mockResolvedValue(envelope(unknownNotice()))
    const w = await mountView()
    await loadAndSettle(w)
    expect(w.find('[data-testid="dh-source"]').text()).toBe(DATA_HORIZON_SOURCE)
    expect(w.find('[data-testid="dh-recent-window"]').text()).toContain(
      String(DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES),
    )
    expect(w.find('[data-testid="dh-lag-tolerance"]').text()).toContain(
      String(DATA_HORIZON_LAG_TOLERANCE_MINUTES),
    )
    // ★★ hint 是本代**新增**的展示行
    expect(w.find('[data-testid="dh-hint"]').text().length).toBeGreaterThan(0)
  })

  it('②-4 ★★★★ 「没取到」与「取到了但值为 null」是两件事', async () => {
    const w = await mountView()
    await flushPromises()
    expect(w.find('[data-testid="dh-empty"]').exists(), '还没点就显示「尚未读取」').toBe(true)
    mFetch.mockResolvedValue(envelope(null))
    await loadAndSettle(w)
    expect(w.find('[data-testid="dh-empty"]').exists()).toBe(false)
    expect(w.find('[data-testid="dh-state"]').exists()).toBe(true)
  })

  it('②-5 ★★★★ 失败时清空数据并显示错误', async () => {
    mFetch.mockResolvedValueOnce(envelope(staleNotice()))
    const w = await mountView()
    await loadAndSettle(w)
    expect(w.find('[data-testid="dh-state"]').exists()).toBe(true)
    mFetch.mockRejectedValueOnce(new Error('boom'))
    await loadAndSettle(w)
    expect(w.find('[data-testid="dh-error"]').text()).toContain('boom')
    expect(w.find('[data-testid="dh-state"]').exists()).toBe(false)
  })
})

// ── ③ 自检红线 ──────────────────────────────────────────────────────────────

describe('③ ★★★★★ 三条自检红线都能被变异打掉', () => {
  it('③-1 ★★★★★ 畸形 notice（有对象但两个 flag 都 false）必须被标出来', async () => {
    mFetch.mockResolvedValue(envelope({ ...staleNotice(), stale: false, unknown: false }))
    const w = await mountView()
    await loadAndSettle(w)
    expect(w.find('[data-testid="dh-flag-bad"]').exists(), 'flag 不变量破了却没报').toBe(true)
    expect(w.find('[data-testid="dh-shape-bad"]').exists()).toBe(true)
    expect(w.find('[data-testid="dh-state"]').text()).toContain('malformed')
  })

  it('③-2 ★★★★★ source 不是 session_turns_hot 时必须被标出来', async () => {
    mFetch.mockResolvedValue(envelope({ ...staleNotice(), source: 'request_logs' }))
    const w = await mountView()
    await loadAndSettle(w)
    expect(w.find('[data-testid="dh-source-bad"]').exists(), 'source 错了却没报').toBe(true)
  })

  it('③-3 ★★★★★ 正常响应时三条红线都**不该**出现（★ 否则它们是恒红噪声）', async () => {
    for (const n of [staleNotice(), unknownNotice()]) {
      mFetch.mockResolvedValue(envelope(n))
      const w = await mountView()
      await loadAndSettle(w)
      expect(w.find('[data-testid="dh-flag-bad"]').exists()).toBe(false)
      expect(w.find('[data-testid="dh-shape-bad"]').exists()).toBe(false)
      expect(w.find('[data-testid="dh-source-bad"]').exists()).toBe(false)
    }
  })

  it('③-4 ★★★★ 「本页不参与判定」这句话必须在页面上', async () => {
    mFetch.mockResolvedValue(envelope(staleNotice()))
    const w = await mountView()
    await loadAndSettle(w)
    const note = w.find('[data-testid="dh-source-note"]').text()
    expect(note).toContain(DATA_HORIZON_SOURCE)
    expect(note).not.toContain('{source}')
  })
})

// ── ④ 已下线概念 ────────────────────────────────────────────────────────────

describe('④ ★★★★ 已下线的概念不得复活', () => {
  it('④-1 ★★★★★ 视图里不得再有 affects / gate_key 的展示行与红线', () => {
    const code = stripComments(viewSrc())
    for (const dead of ['affects', 'gate_key', 'gateKey', 'Affects']) {
      expect(code, `视图里还有已下线的 ${dead}`).not.toContain(dead)
    }
    // ★ 旧 testid 也不得残留
    for (const dead of ['dh-affects-bad', 'dh-affects-count-bad', 'dh-gate-key', 'dh-gate-key-bad']) {
      expect(code, `残留旧 testid ${dead}`).not.toContain(dead)
    }
    // ★★★ 新 testid 必须都在
    for (const alive of ['dh-source', 'dh-hint', 'dh-flag-bad', 'dh-shape-bad', 'dh-source-bad']) {
      expect(code, `缺 ${alive}`).toContain(alive)
    }
  })

  it('④-2 ★★★★ 不得重算后端的三态（★ 那是后端纯函数的镜像）', () => {
    const code = stripComments(viewSrc())
    // ★ 前端只 import 判据函数，不得自己拼 stale/unknown 条件
    expect(code).not.toMatch(/notice\.value\s*&&\s*notice\.value\.stale\s*===/)
    expect(code).not.toMatch(/deriveDataHorizonState|computeDataHorizonState/)
  })

  it('④-3 ★★★★ 旧视图文件不得存在', () => {
    expect(existsSync(join(process.cwd(), 'src/views/V1DataHorizonView.vue'))).toBe(false)
    expect(existsSync(join(process.cwd(), 'src/views/V1DataHorizonView.spec.ts'))).toBe(false)
  })
})

// ── ⑤ 接线 / 尺寸 / 编码 / i18n ──────────────────────────────────────────────

describe('⑤ ★★★★ 接线、尺寸、编码与 i18n', () => {
  it('⑤-1 ★★★★ 触控目标 ≥48 CSS px（把数值解析出来再比大小）', () => {
    const m = viewSrc().match(/\.dh__btn\s*\{[^}]*min-height:\s*(\d+)px/)
    expect(m, '没找到 .dh__btn 的 min-height（正则失配会让这条恒绿）').toBeTruthy()
    expect(Number(m![1])).toBeGreaterThanOrEqual(48)
  })

  it('⑤-2 ★★★★ 样式只用已定义的 token（★ 不许自造）', () => {
    const style = viewSrc()
    const used = new Set([...style.matchAll(/var\((--[a-z0-9-]+)/g)].map((m) => m[1]!))
    const theme = readFileSync(join(process.cwd(), 'src/styles/theme.css'), 'utf8')
    for (const t of used) {
      expect(theme.includes(`${t}:`), `样式引用了未定义的 token ${t}`).toBe(true)
    }
  })

  it('⑤-3 ★★★★ 编码门：源文件不得含 U+FFFD / NUL', () => {
    // ⚠️ 必须按**完整三字节序列**匹配 EF BF BD ——
    //   写成 includes(0xef) || includes(0xbf) 会把每一行正常中文都判成污染。
    const FFFD = Buffer.from([0xef, 0xbf, 0xbd])
    for (const p of [
      'src/views/DataHorizonView.vue',
      'src/views/DataHorizonView.spec.ts',
      'src/api/dataHorizon.ts',
      'src/api/dataHorizon.test.ts',
    ]) {
      const buf = readFileSync(join(process.cwd(), p))
      expect(buf.includes(FFFD), `${p} 含 U+FFFD 字节`).toBe(false)
      expect(buf.includes(0), `${p} 含 NUL 字节`).toBe(false)
    }
  })

  it('⑤-4 ★★★★ i18n 两语种键都在，且**未翻译**（值 ≠ 键名）', () => {
    const code = stripComments(viewSrc())
    const used = new Set<string>()
    for (const m of code.matchAll(/t\('(dh\.[a-zA-Z.-]+)'/g)) used.add(m[1]!)
    // ★ 模板字面量那一处要补上插值值：`t(\`dh.state.${state}\`)`
    //   ⚠️ 正则只取到 `dh.state.` 就是**把插值部分截掉了**（本批真踩到），
    //   而那正是四个状态文案的键 ⇒ 必须显式补全，不能让它变成半个键。
    for (const m of code.matchAll(/t\(`(dh\.[a-zA-Z.]+)\$\{/g)) {
      used.add(`${m[1]}live`)
      used.add(`${m[1]}stale`)
      used.add(`${m[1]}unknown`)
      used.add(`${m[1]}unexpected-shape`)
    }
    expect(used.size, '没从视图里取到任何 dh.* 键').toBeGreaterThan(5)
    for (const key of used) {
      for (const loc of ['zh-CN', 'en-US'] as const) {
        const v = dictLookup(DICT[loc], key)
        expect(v, `${loc} 缺 ${key}`).toBeDefined()
        expect(typeof v, `${loc} 的 ${key} 不是字符串`).toBe('string')
        expect(v, `${loc} 的 ${key} 未翻译`).not.toBe(key)
      }
    }
  })

  it('⑤-4a ★★★★★★ 三条「钉判据自身」的判据**不得被削弱**（★ 负控的负控·第二层）', () => {
    // ★★★★★ 变异台 M41 / M44 / M45 三条**无红**，而它们是同一个机制：
    //   它们改的是**判据自己**（把 `toBe(2)` 放宽成 `toBeGreaterThanOrEqual(2)`、
    //   把「已下线概念」那个循环放宽、把「不得重算三态」那条正则删掉）。
    //   ⇒ 一条判据被削弱时，**它在逻辑上无法察觉自己被削弱了** ——
    //     削弱「断言调用者是 2 次」的那条，不会让任何别的判据变红。
    //
    // ⇒ ★★★ 这是第 128 批「负控的负控」的**第二层**：
    //     第一层是「判据被换成恒真表达式」（那已经能被抓）；
    //     **第二层是「判据被换成更弱的表达式」，而弱的那版仍然是真命题。**
    //     ⇒ 治法只有一个：**从另一个文件读它源码**并要求关键子串逐字存在。
    const apiTest = readFileSync(join(process.cwd(), 'src/api/dataHorizon.test.ts'), 'utf8')
    const selfCode = stripComments(thisFileSrc())
    // ① callers 计数必须是 **恰好 2**（`toBe(2)`，不是 `>=`）
    expect(apiTest, 'callers 计数被放宽了 ⇒ 中枢出口那件事就没人守了').toMatch(
      /expect\(callers\.length,[^)]*\)\.toBe\(2\)/,
    )
    expect(apiTest, '出现了 toBeGreaterThanOrEqual 版的 callers 断言').not.toMatch(
      /callers\.length\)\s*\.toBeGreaterThanOrEqual/,
    )
    // ② 「已下线概念」那个 for 循环**与它的断言本体**都不得消失
    // ⚠️★★ 只盯数组字面量是不够的（变异台 M44 的教训）：
    //   把 `expect(code, ...).not.toContain(dead)` 整行换成注释，
    //   **`for (const dead of [...])` 那一行仍在** ⇒ 只钉数组的判据照样通过。
    //   ⇒ 必须**同时**钉住断言本体。★★ 这是与 M35/M36 完全同族的
    //   「注释掉断言」型变异，而那条只能靠**从另一个文件读源码**来抓。
    expect(selfCode, '④-1 的死值数组被删了').toMatch(
      /for \(const dead of \['affects', 'gate_key', 'gateKey', 'Affects'\]\)/,
    )
    expect(selfCode, '④-1 的断言本体被注释掉了 ⇒ 「已下线概念」就没人守了').toMatch(
      /expect\(code,[^)]*\)\.not\.toContain\(dead\)/,
    )
    // ⚠️⚠️ 第二版踩的坑：**不能**只钉 `not.toContain(dead)` ——
    //   `④-1` 里有**两个**这样的断言（死值数组 + 旧 testid），
    //   把其中一个注释掉，另一个仍满足正则 ⇒ 照样全绿（本批真踩到）。
    // ⇒ 必须连**模板字符串的插值名**一起钉 —— 那个是两条断言**唯一不同**的地方：
    //   `视图里还有已下线的 ${dead}` vs `残留旧 testid ${dead}`。
    expect(selfCode, '④-1「死值数组」那条断言被注释掉了').toMatch(
      /expect\(code, `视图里还有已下线的 \$\{dead\}`\)\.not\.toContain\(dead\)/,
    )
    expect(selfCode, '④-1「旧 testid」那条断言被注释掉了').toMatch(
      /expect\(code, `残留旧 testid \$\{dead\}`\)\.not\.toContain\(dead\)/,
    )
    // ③ 「不得重算三态」那条正则不得被删
    expect(selfCode, '④-2 的「不得重算」正则被删了').toMatch(
      /expect\(code\)\.not\.toMatch\(\/notice\\.value/,
    )
  })

  it('⑤-4b ★★★★★ 孤儿哨兵的两条断言**不得被改成注释**（★ 反僵尸断言）', () => {
    // ★★★ 变异台 M35/M36 把孤儿棘轮里那两条 `expect` 整行注释掉，**全仓无红** ——
    //   因为没有任何别的判据覆盖它们 ⇒ 那两条是**僵尸断言**：
    //   只在「自己被改」时才红，等于平时不测任何东西。
    // ⇒ 治法（与第 128 批「负控的负控」同形）：**钉它们的源码形态**。
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const code = stripComments(src)
    expect(code).toMatch(/expect\(currentOrphans\(\)\)\.not\.toContain\('dataHorizon'\)/)
    expect(code).toMatch(/expect\(apiModules\(\)\)\.not\.toContain\('v1DataHorizon'\)/)
    // ★ 剥掉注释后仍能匹配 ⇒ 证明它们**不是**注释里的字面量（否则 stripComments 会把它们删掉）
    expect(stripComments(src).length).toBeGreaterThan(0)
  })

  it('⑤-5 ★★★★ 四个状态各自的文案两语种都在，且互不相同', () => {
    const states = ['live', 'stale', 'unknown', 'unexpected-shape']
    for (const st of states) {
      const seen = new Set<string>()
      for (const loc of ['zh-CN', 'en-US'] as const) {
        const v = dictLookup(DICT[loc], `dh.state.${st}`)
        expect(typeof v === 'string' && v.length > 0, `${loc} 缺 dh.state.${st}`).toBe(true)
        seen.add(v as string)
        const note = dictLookup(DICT[loc], `dh.state.note.${st}`)
        expect(typeof note === 'string' && note.length > 0, `${loc} 缺 dh.state.note.${st}`).toBe(
          true,
        )
      }
      expect(seen.size, `两个语种的 dh.state.${st} 相同`).toBe(2)
    }
    // ★★ 旧键不得残留
    for (const loc of ['zh-CN', 'en-US'] as const) {
      expect(dictLookup(DICT[loc], 'v1h.title'), `${loc} 还留着 v1h 块`).toBeUndefined()
      expect(dictLookup(DICT[loc], 'nav.v1DataHorizon')).toBeUndefined()
      const nav = dictLookup(DICT[loc], 'nav.dataHorizon')
      expect(nav, `${loc} 缺 nav 席名`).toBeDefined()
      // ★★★ M37 的打靶处：断言「存在」不够，**必须查「值 ≠ 键名」** ——
      //   i18n 门拦的就是「值 === 键名」，而这里只判了 toBeDefined ⇒ 漏网。
      expect(nav, `${loc} 的 nav 席名未翻译`).not.toBe('nav.dataHorizon')
    }
    // ★★★ M39 的打靶处：同一语种内，三态 + 畸形态的**徽章**文案必须互不相同。
    //   原判据只查「中英不同」⇒ 把 zh 的 `stale` 改成 `'unknown'`（与 en 的
    //   `'stalled'` 仍不同）就溜过去了 —— ★ **跨语种比较证明不了语种内的唯一性。**
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const badges = states.map((st) => dictLookup(DICT[loc], `dh.state.${st}`))
      expect(new Set(badges).size, `${loc} 的 dh.state.* 有重复`).toBe(states.length)
      const notes = states.map((st) => dictLookup(DICT[loc], `dh.state.note.${st}`))
      expect(new Set(notes).size, `${loc} 的 dh.state.note.* 有重复`).toBe(states.length)
    }
  })
})
