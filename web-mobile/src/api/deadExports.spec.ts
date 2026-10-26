import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'

/**
 * ★★★★★ api 模块「**零引用导出**」普查 —— 孤儿问题的**第三面**。
 *
 * ## 三个面
 *
 *   ① 孤儿 **模块**：没人 import（批 133 的 `orphanLedger.spec.ts`，已清零）
 *   ② 孤儿 **i18n 键**：词典里有、没人用（批 140 的 `orphanKeys.spec.ts`，72 条已登记）
 *   ③ **导出无人调** ← 本文件：模块有人 import，函数也实现了，**但没有任何调用点**
 *
 * 第 ③ 面才是「功能做了但用户点不到」的形态，而且它**不会**被前两面发现：
 * 模块在 import 图里好好的，函数也在，只是没人调。
 *
 * ## ★ 为什么分母是「含测试的严格口径」而不是「只看生产」
 *
 * 先量过「只看生产调用方」：**540 个**导出函数没有生产调用方。
 * 但逐条打开看，那里面 **531 个是导出给 `*.test.ts` 用的断言判定函数**
 * （`workTypeStatsShareL1Proxy` / `trendSourceRealTable` / `approvalTimeValid` …）
 * —— 它们不是死代码，**它们是那些模块的判据**。
 *
 * ⇒ 用它们当分母会得到 540 这个吓人的数，而它**大部分不是债**。
 * ⇒ 换成**含测试的严格口径**：连测试都没引用的，才是真的没人要。**9 个。**
 *
 * ## 「被引用」的判定口径
 *
 * 逐文件抽出全部标识符（单遍），再看该符号是否出现在**定义文件之外**任一文件里。
 * ⚠️ 只做词边界匹配，不解析 import 图 —— 见 ①-3 的覆盖面自证。
 */

const SRC = join(resolve(process.cwd()), 'src')
const SELF = 'deadExports.spec.ts'

function walk(dir: string): string[] {
  const out: string[] = []
  let entries: import('node:fs').Dirent[] = []
  try {
    entries = readdirSync(dir, { withFileTypes: true })
  } catch {
    return out
  }
  for (const e of entries) {
    const full = join(dir, e.name)
    if (e.isDirectory()) {
      if (e.name === 'node_modules' || e.name === 'dist') continue
      out.push(...walk(full))
    } else if (/\.(vue|ts)$/.test(e.name)) {
      out.push(full)
    }
  }
  return out
}

const ALL_FILES = walk(SRC).filter((f) => !f.endsWith(SELF))
const BLOB = new Map(ALL_FILES.map((f) => [f, readFileSync(f, 'utf8')]))

const IDENT = /[A-Za-z_$][\w$]*/g
const IDENTS = new Map([...BLOB].map(([f, s]) => [f, new Set(s.match(IDENT) ?? [])]))

/** 被测对象：api 模块（非测试）。★ 判据自身不在其中（它在 src/api/ 下）。 */
const API_MODULES = [...BLOB.keys()]
  .filter((f) => f.startsWith(join(SRC, 'api') + '/') && !/\.(test|spec)\.ts$/.test(f))
  .sort()

const EXPORT_FN =
  /^export\s+(?:async\s+)?function\s+([A-Za-z_$][\w$]*)/gm

type Exported = { file: string; name: string }
const EXPORTS: Exported[] = []
for (const f of API_MODULES) {
  for (const m of BLOB.get(f)!.matchAll(EXPORT_FN)) {
    // ★ matchAll 的捕获组在 noUncheckedIndexedAccess 下是 `string | undefined`。
    //   宁可丢掉非法的，也不要把 `undefined` 塞进名单（会让后面所有比较失真）。
    const name = m[1]
    if (typeof name === 'string' && name.length > 0) EXPORTS.push({ file: f, name })
  }
}

/**
 * ★ 纯函数：某符号在「定义文件之外」是否被引用过。
 * 抽成纯函数，好让 ①-2 的自证**走同一段逻辑**（批 140 的 K03 教训：
 * 负控走的是另一段表达式时，负控就是装饰）。
 */
function isReferenced(name: string, defFile: string, idents = IDENTS): boolean {
  for (const [f, ids] of idents) {
    if (f !== defFile && ids.has(name)) return true
  }
  return false
}

/** 含测试的严格口径：连测试都没引用 ⇒ 真的没人要。 */
const DEAD: Exported[] = EXPORTS.filter((e) => !isReferenced(e.name, e.file))
/** 只看生产的口径：**只**是读数，不是债（见文件头）。 */
const PROD_DEAD: Exported[] = EXPORTS.filter(
  (e) => !isReferenced(e.name, e.file, new Map([...IDENTS].filter(([f]) => !/\.(test|spec)\.ts$/.test(f)))),
)
/** 只被测试引用（= 导出给判据用的判定函数），**不是债**。 */
const TEST_ONLY: Exported[] = PROD_DEAD.filter((e) => !DEAD.includes(e))

const DEAD_KEYS: string[] = DEAD.map((e) => `${basename(e.file)}::${e.name}`)

/**
 * ★★★ 两条棘轮判定各自抽成**具名纯函数**。
 *   批 142 变异台首轮实测：把 ② 的 `unknown` / ③ 的 `used` **整个掏空**，
 *   用例**照样全绿**（`[] === []`）—— 棘轮被关掉没有任何症状。
 *   ⇒ 负控必须**调用同一个函数**，只是传一个「被打破」的输入。
 */
const unregisteredDead = (deadKeys: string[], registered: string[]): string[] =>
  deadKeys.filter((k) => !registered.includes(k))

const noLongerDead = (registered: string[], deadKeys: string[]): string[] =>
  registered.filter((k) => !deadKeys.includes(k))

/**
 * ★★★ 已知零引用导出 —— 逐条注明**定性**，不是豁免。
 *
 * 定性只有三类（本批逐条查过源码与 Go 注册处）：
 *   A. **export 多余**：函数在本文件里有调用方，只是没必要的 `export`
 *   B. **端点存在但 UI 无入口**：客户端函数已实现，后端注册处也确认存在，
 *      但移动端没有任何页面调它 ⇒ 功能做了、用户点不到
 *   C. **知识未落地**：它承载的是一句已写进注释的正确性知识，但 UI 没有体现
 */
const KNOWN_DEAD_EXPORTS: { key: string; class: 'A' | 'B' | 'C'; note: string }[] = [
  {
    key: 'logsAdmin.ts::logsAdminPath',
    class: 'A',
    note: '本文件 :318/:334/:350 三处在用（拼 body-cache-stats / files / stats），只是没必要的 export',
  },
  {
    key: 'modules.ts::modulesPath',
    class: 'A',
    note: '本文件 :187/:203/:230 三处在用（列表 / 单模块 / config），只是没必要的 export',
  },
  {
    key: 'pendingResponses.ts::unwrapPendingStats',
    class: 'A',
    note: '本文件 :203 的 `.then(unwrapPendingStats)` 在用 —— 定义写在 :207，调用在它上面',
  },
  {
    key: 'credentialsOps.ts::fetchScoreDetails',
    class: 'B',
    note: '★ 打 `GET /api/routing/score-details`（`admin/handler.go:1283`）—— 后端在、客户端实现了、`ScoreDetailsResponse` 也定义了，**但没有任何页面展示打分明细**',
  },
  {
    key: 'models.ts::fetchFeaturedModelSuggestions',
    class: 'B',
    note: '★ 打 `GET /api/routing/featured-models`（`admin/handler.go:1290`）—— 同族的 fetchFeaturedModels / saveFeaturedModels 都已在 `ModelsView.vue` 接线，**唯独它没有**',
  },
  {
    key: 'requestLogs.ts::fetchRequestLogDetail',
    class: 'B',
    note: '★ 打 `GET /api/logs/<id>` —— 注册 `admin/handler.go:1344`，**取 id 的那行**是 `admin/logs.go:862`（逐字是 /api/logs/ 的尾段解析；注册行只有前缀，所以引用必须落在 :862 才可核）。而 UI 实际用的是**另一个端点** `admin/handler.go:1346` 的 /api/admin/request-detail/（走 `requestDetail.fetchRequestDetail`）⇒ 两端点并存，只接了一个',
  },
  {
    key: 'system.ts::fetchVersion',
    class: 'B',
    note: '★ 打 `GET /version`（`cmd/gateway/main.go:6324`）；Go 侧注释写明它供 `scripts/lifecycle/preflight.sh` 三段检查用 ⇒ 移动端无入口',
  },
  {
    key: 'tenants.ts::tenantAggSourceNote',
    class: 'C',
    note: '承载「列表与详情的聚合可靠性不同：详情是 `_ =` 吞错」这句知识，但它返回的 `list`/`detail` 标签在 UI 上无处体现。★ 降级提示本身**已落地**：姊妹函数 tenantUsageMayBeDegraded 有 2 个生产调用方（`TenantsView.vue:44/:201` · `TenantDetailView.vue:95`）',
  },
  {
    key: 'turnsSessions.ts::turnIsProblem',
    class: 'C',
    note: '判定「失败 / 限流 / 注入拦截都算」已实现，但没有任何视图用它在轮次列表上着色',
  },
]

const REGISTERED: string[] = KNOWN_DEAD_EXPORTS.map((d) => d.key)

describe('① 读数：分母必须写在这里', () => {
  it('①-1 ★★★★★ 三个口径各是多少，不许只报「死的有几条」', () => {
    console.log(
      `[①-1] api 模块 ${API_MODULES.length} 个 · 导出函数 ${EXPORTS.length} 个\n` +
        `       ★ 严格口径（含测试）：零引用 **${DEAD.length}**\n` +
        `       只看生产：零调用 ${PROD_DEAD.length}，其中**只被测试引用** ${TEST_ONLY.length}` +
        `（导出给判据用的判定函数，**不是债**）`,
    )
    expect(EXPORTS.length).toBeGreaterThan(1000)
  })

  it('①-2 ★★★★★ 自证：同一个符号，被引用⇒非死；移出引用集⇒立刻判死', () => {
    // ★ 双向各验一次，且**走同一个函数**（批 140 的 K03 教训：负控走另一段表达式就是装饰）
    const sample = EXPORTS.find((e) => isReferenced(e.name, e.file))
    expect(sample, '一个导出都没找到引用方？扫描器坏了').toBeTruthy()
    expect(isReferenced(sample!.name, sample!.file)).toBe(true)

    // ★ 把这个符号从**除定义文件外**的所有文件里摘掉 —— 只清定义文件是不够的，
    //   其他文件照样能命中（第一版就栽在这里，①-2 恒红）。
    const stripped = new Map<string, Set<string>>()
    for (const [f, ids] of IDENTS) {
      stripped.set(f, f === sample!.file ? ids : new Set([...ids].filter((x) => x !== sample!.name)))
    }
    expect(isReferenced(sample!.name, sample!.file, stripped)).toBe(false)
  })

  it('①-3 ★★★★ 覆盖面自证：导出正则必须抓到 4 种声明形态', () => {
    // 覆盖面退化的形态是「某类导出整个抓不到」—— 那样死函数读数会**偏小**。
    const forms: Record<string, number> = {
      'export function': (API_MODULES.map((f) => BLOB.get(f)!.match(/^export\s+function\s/gm)?.length ?? 0)
        .reduce((a, b) => a + b, 0)) as number,
      'export async function': API_MODULES.map((f) => BLOB.get(f)!.match(/^export\s+async\s+function\s/gm)?.length ?? 0)
        .reduce((a, b) => a + b, 0),
      'export const': API_MODULES.map((f) => BLOB.get(f)!.match(/^export\s+(?:const|let|var)\s/gm)?.length ?? 0)
        .reduce((a, b) => a + b, 0),
      'export interface|type': API_MODULES.map((f) => BLOB.get(f)!.match(/^export\s+(?:interface|type)\s/gm)?.length ?? 0)
        .reduce((a, b) => a + b, 0),
    }
    console.log(`[①-3] 导出形态计数：${JSON.stringify(forms)}`)
    for (const [k, v] of Object.entries(forms)) expect(v, `${k} 一个都没抓到 ⇒ 覆盖面退化了`).toBeGreaterThan(0)
  })
})

// ── ② 棘轮：未登记的零引用导出一律红 ─────────────────────────────────────────

describe('② ★★★ 未登记的零引用导出一律红', () => {
  it('②-1 ★★★★★ 判为零引用的导出必须全部已登记并注明定性', () => {
    // ★ 负控**先跑**（同批 140 的 ②-1）：证明这个函数还会报出未登记项，
    //   否则下面那句 `toEqual([])` 是恒真的。
    const probe = DEAD_KEYS[0]
    expect(
      probe === undefined ? [] : unregisteredDead([probe], []),
      '★ 负控失效：未登记的零引用导出会被报出来吗',
    ).toEqual([probe])
    const unknown = unregisteredDead(DEAD_KEYS, REGISTERED)
    expect(
      unknown,
      '这些导出连测试都没引用 ⇒ 要么删掉，要么在 KNOWN_DEAD_EXPORTS 里登记并注明定性：\n' +
        unknown.join('\n'),
    ).toEqual([])
  })
})

// ── ③ 棘轮反向：登记项必须仍然是零引用 ───────────────────────────────────────

describe('③ ★★★ 登记项必须仍然是零引用（有人接了就删登记）', () => {
  it('③-1 ★★★★★ KNOWN_DEAD_EXPORTS 里每一条都仍然判为零引用', () => {
    // ★ 负控**先跑**
    const probeKey = REGISTERED[0]
    expect(
      probeKey === undefined ? [] : noLongerDead([probeKey], []),
      '★ 负控失效：不再零引用的登记项会被报出来吗',
    ).toEqual([probeKey])
    const used = noLongerDead(REGISTERED, DEAD_KEYS)
    expect(
      used,
      '这些登记项已经有人调用了 ⇒ 把登记删掉，别让名单变成免死金牌：\n' + used.join('\n'),
    ).toEqual([])
  })

  it('③-2 ★★★ 名单里不许有重名项', () => {
    expect(REGISTERED.filter((k, i) => REGISTERED.indexOf(k) !== i)).toEqual([])
  })
})

// ── ④ 名单质量 ──────────────────────────────────────────────────────────────

describe('④ 名单规模与质量', () => {
  it('④-1 ★★★★ KNOWN_DEAD_EXPORTS 恰好 9 条（严格口径，逐条查过源码与 Go 注册处）', () => {
    expect(KNOWN_DEAD_EXPORTS).toHaveLength(9)
  })

  it('④-2 ★★★★ 每一条都必须有定性分类与非空理由', () => {
    const blank = KNOWN_DEAD_EXPORTS.filter((d) => !d.note || d.note.trim().length < 10)
    expect(blank.map((b) => b.key), '这些登记项没写理由 ⇒ 不许登记').toEqual([])
    for (const d of KNOWN_DEAD_EXPORTS) expect(['A', 'B', 'C']).toContain(d.class)
  })

  it('④-3 ★★★ 名单里每个符号都必须真实存在于它标注的那个文件里', () => {
    const ghost = KNOWN_DEAD_EXPORTS.filter((d) => {
      const mod = d.key.split('::')[0]
      const name = d.key.split('::')[1]
      if (mod === undefined || name === undefined) return true
      const f = API_MODULES.find((p) => basename(p) === mod)
      return !f || !BLOB.get(f)!.includes(name)
    })
    expect(ghost.map((g) => g.key), `名单里有不存在的符号：${ghost.map((g) => g.key).join(', ')}`).toEqual([])
  })

  it('④-4 ★★ 三个定性分类都必须在用（不许把「不是债」的塞进债名单）', () => {
    const byClass = (c: string) => KNOWN_DEAD_EXPORTS.filter((d) => d.class === c).length
    console.log(`[④-4] A export 多余 ${byClass('A')} · B 端点存在但 UI 无入口 ${byClass('B')} · C 知识未落地 ${byClass('C')}`)
    expect(byClass('A')).toBeGreaterThan(0)
    expect(byClass('B')).toBeGreaterThan(0)
    expect(byClass('C')).toBeGreaterThan(0)
  })
})
