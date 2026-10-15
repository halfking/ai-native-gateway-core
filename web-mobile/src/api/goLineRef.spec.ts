import { describe, it, expect } from 'vitest'
import { readFileSync, existsSync, readdirSync } from 'node:fs'
import { join, relative, sep } from 'node:path'

/**
 * api 模块注释里「Go 行号引用」的普查与棘轮（2026-10-16，第一百三十六批）。
 *
 * ★★★★★★ 本仓的权限决策大量压在**注释里的 Go 行号引用**上 ——
 *   例如批 130 的「服务端是真门」整条依据是 `admin/handler.go:936-937` 挂
 *   `h.superAdmin(...)` 与 `admin/auth.go:383-384` 逐字比 `claims.Role`。
 *   那些**手工逐字核过**的引用今天仍然成立（本批复验过）；
 *   但**批量写**的那些没人核过。⇒ 本批把它们**量一遍**。
 *
 * ★★★★★ **读数（本批实测）**：
 *   api 模块里 `.go:行号` 形式的引用共 **1015 处**，
 *   其中路径能唯一对上仓库里真实存在的 Go 文件、且同一行注释里
 *   带一个**可核 token**（端点路径或反引号标识符）的有 **68 处** ——
 *   这 69 条是**本判据能机器判定**的子集；剩下 946 处分两类：
 *   **611 处**写的是裸文件名（如 `handler.go`），仓库里有多个同名文件，
 *   **无法确定指的是哪一个** ⇒ 不判；**335 处**同一行没有可核 token ⇒ 不判。
 *   另有 **1 处**是区间反向的畸形引用（`settings/spec.go:284-283`）。
 *   ⇒ ★ **不判 ≠ 对**。本判据只对那 68 条负责，分母写在这里以免读数被误读成
 *     「全仓只有 69 条引用」。
 *
 * ★★★★★ **阴性结论带宽度与演进**：
 *   判据建成时（批 136）那 68 条里 **32 条对不上（46%）**；
 *   批 137 **逐条定性** —— **12 条是真错**（含 1 处区间反向、1 处指向空行、
 *   1 处只差 1 行），已全部订正并**复核结论仍然成立**（光移行号不算修好）；
 *   剩下 **15 条经逐字核对，引用本身是对的**，只是本判据抓 token 的方式不适用
 *   ⇒ 登记进 `KNOWN_FALSE_POSITIVE`，**它们不是债**。
 *   ⇒ 现状：`KNOWN_DRIFT`（真错）**0 条**，`KNOWN_FALSE_POSITIVE` **15 条**。
 *
 * ★★★★ **批 137 的一个额外发现，值得单独记住**：
 *   那 12 处真错修完之后，在**普查扫不到的范围**里又找到 **16 处同源残留** ——
 *   散落在 `views/*.vue`、`*.test.ts`、判据文件里，引用的是**同一批过期行号**。
 *   ⇒ 普查的「不判」不等于「干净」：946 处判不了的地方藏着同类问题。
 *   ⇒ 本批把那 16 处一并修了，但**普查范围本身没扩**（扩到 views/ 是另一件事）。
 *
 * ★★ **本判据自身的宽度**（也是它为什么只覆盖 69 条）：
 *   · 端点 token 必须写成 `/api/...` 的**字面量**；写常量名（如 `dispatchJournalAPIPath`）
 *     的引用判不出来 ⇒ 落进「无可核 token」或 `KNOWN_FALSE_POSITIVE`。
 *   · 标识符 token 要求**小写字母开头**（`queryInt`/`writeError`/`wrapAdmin`），
 *     大写开头的（`SuperAdminMiddleware`）同样判不出来。
 *   · token 与被引区间必须**逐字包含**；「引用的是函数定义、token 在函数体内」
 *     这类**语义上正确**的引用判不出来 ⇒ 这是 15 条假阳性的全部来源。
 *   ⇒ 这些是**已知的漏检/误检面**，不是「那些引用是对的」。
 */

// ── 仓库里的真实 Go 文件集合 ────────────────────────────────────────────────

const ROOT = process.cwd().replace(new RegExp(`${sep}web-mobile$`), '')
const WEB = join(process.cwd(), 'src')

/**
 * ★ 用 `withFileTypes`（lstat 语义）而不是 `statSync`。
 *   实测本仓 `docs/archive/process/incidents-collection/` 下有**一个悬空符号链接**
 *   （指向不存在的 `docs/fixes/2026-08-31-154-blue-green-deploy-recovery.md`），
 *   `statSync` 跟进去直接 ENOENT ⇒ 整个判据在收集阶段就崩。
 *   Dirent 对符号链接的 `isDirectory()` 恒为 false，于是它被安静跳过。
 */
function collectGoFiles(dir: string, acc: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name)
    if (e.isDirectory()) {
      if (e.name === 'node_modules' || e.name === '.git' || e.name === 'dist') continue
      collectGoFiles(p, acc)
    } else if (e.isFile() && e.name.endsWith('.go')) {
      acc.push(relative(ROOT, p).split(sep).join('/'))
    }
  }
  return acc
}

const GO_FILES = new Set(collectGoFiles(ROOT))
const goCache = new Map<string, string[]>()

function goLines(rel: string): string[] | null {
  if (!goCache.has(rel)) goCache.set(rel, readFileSync(join(ROOT, rel), 'utf8').split('\n'))
  return goCache.get(rel)!
}

/** api 模块清单：排除测试与判据文件（那些不是「文档债」的载体）。 */
function apiModules(): string[] {
  const out: string[] = []
  const walk = (d: string) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.isFile() && e.name.endsWith('.ts') && !e.name.endsWith('.test.ts') && !e.name.endsWith('.spec.ts')) {
        out.push(p)
      }
    }
  }
  walk(join(WEB, 'api'))
  return out.sort()
}

const REF = /((?:[A-Za-z0-9_]+\/)*[A-Za-z0-9_]+\.go):(\d+)(?:-(\d+))?/g
const TOKEN = /(\/api\/[A-Za-z0-9_/{}.:-]+)|(`[a-z][A-Za-z0-9_]{3,}`)/g

export interface Ref {
  /** 引用所在的文件（相对 web-mobile/src）。 */
  file: string
  /** 引用所在行。 */
  line: number
  /** 原始引用文本，如 `admin/handler.go:936-937`。 */
  raw: string
  goFile: string
  from: number
  to: number
  tokens: string[]
}

/**
 * ★ 扫描器写成**纯函数**（吃文本，不吃文件系统状态）——
 *   这样 ① 的召回自证才能拿一段合成文本直接喂进来，
 *   而不必真的去改产品文件（本仓的判据不该改产品文件）。
 */
export function scanRefs(text: string, fileLabel: string): Ref[] {
  const out: Ref[] = []
  text.split('\n').forEach((line, i) => {
    for (const m of line.matchAll(REF)) {
      const tokens = [...line.matchAll(TOKEN)].map((t) => (t[1] ?? t[2] ?? '').replace(/`/g, ''))
      out.push({
        file: fileLabel,
        line: i + 1,
        raw: m[0],
        goFile: m[1] ?? '',
        from: Number(m[2]),
        to: m[3] ? Number(m[3]) : Number(m[2]),
        tokens,
      })
    }
  })
  return out
}

// ★ 第四个桶是本批实测**必需**的：第一版只有三类，对账断言当场抓到
//   「三类之和 = 1014 ≠ 总数 1015」—— 漏掉的是 `settings/spec.go:284-283`
//   这种**区间反向**的畸形引用（切片为空，天然判不出 token）。
type Kind = '路径不唯一' | '无可核 token' | '行号越界或区间反向' | '可判定' | '可判定但对不上'

function classify(refs: Ref[]): { counts: Record<Kind, number>; bad: Ref[] } {
  const counts: Record<Kind, number> = {
    路径不唯一: 0,
    '无可核 token': 0,
    '行号越界或区间反向': 0,
    可判定: 0,
    可判定但对不上: 0,
  }
  const bad: Ref[] = []
  for (const r of refs) {
    if (!GO_FILES.has(r.goFile)) {
      counts['路径不唯一']++
      continue
    }
    const src = goLines(r.goFile)
    if (!src || r.from < 1 || r.from > src.length || r.to < r.from) {
      // 越界 / 区间反向：区间反向时切片为空，天然判不出 token。
      counts['行号越界或区间反向']++
      bad.push(r)
      continue
    }
    if (r.tokens.length === 0) {
      counts['无可核 token']++
      continue
    }
    counts['可判定']++
    const seg = src.slice(r.from - 1, r.to).join('\n')
    if (!r.tokens.some((t) => seg.includes(t))) {
      counts['可判定但对不上']++
      bad.push(r)
    }
  }
  return { counts, bad }
}

function allRefs(): Ref[] {
  const out: Ref[] = []
  for (const p of apiModules()) {
    const rel = relative(WEB, p).split(sep).join('/')
    out.push(...scanRefs(readFileSync(p, 'utf8'), rel))
  }
  return out
}

const key = (r: Ref) => `${r.file}:${r.line} ${r.raw}`

/**
 * ★★ 棘轮名单：本批**只登记**，不假装已修。
 *   ⚠️ 每一条都必须在 ② 里仍然判为「对不上」——
 *     谁把它修好了，③ 会逼他把这一条从名单里删掉。
 */
/**
 * ★★ 真错名单：**本批清零**。
 *   批 136 登记 25 条，批 137 逐条定性 —— 其中 **12 条是真错**（含 1 处区间反向、
 *   1 处指向空行、1 处差 1 行），已全部订正并把结论一并复核过。
 *   ⇒ 名单留成空数组而不是删掉：**空名单本身是一条断言**（不许悄悄新增），
 *     与 `KNOWN_ORPHANS` 同一处理方式。
 */
const KNOWN_DRIFT: string[] = []

/**
 * ★★★ 「检查器假阳性」名单 —— **不是债，是判据分辨率不足**。
 *   下面每一条的引用**逐字核对过、被引处确实是它声称的东西**，
 *   只是本判据抓 token 的方式不适用（token 不是字面出现、或与引用不在同一段）。
 *   ★★ 之所以要显式列出来而不是直接删：删了之后它们会变成
 *     **「未登记的漂移」** ⇒ ② 会红，而红的原因与人无关。
 *   每条写清「为什么判不出来」，将来改判据时才知道该动哪儿。
 */
const KNOWN_FALSE_POSITIVE: { key: string; note: string }[] = [
  { key: 'api/connectionRegistry.ts:7 admin/handler.go:1044', note: '注释写 `{id}`，Go 里是 `{request_id}` —— 字面不同，指的同一条注册' },
  { key: 'api/credentialsOps.ts:140 admin/routing_reset.go:52', note: '52 行是 `handleResetCredentialState` 的函数定义；token `reason` 在函数体内（注释自己就写了 `:69-72`）' },
  { key: 'api/dashboard.ts:14 admin/dashboardapi/types.go:187', note: '187 行是 `writeSuccessJSON` 产出结构里的一行（`Success: true`），不是该函数本身' },
  { key: 'api/dispatchJournal.ts:18 admin/journal_handlers.go:30-35', note: '被引处写的是**常量名** `dispatchJournalAPIPath`，字面量在 :13' },
  { key: 'api/errorsTrend.ts:38 admin/users.go:406', note: '行里是大写 `Role`，token 是小写 `role`（大小写敏感）' },
  { key: 'api/outputCompliance.ts:131 admin/output_compliance_handler.go:712', note: '712 行是 `writeJSON(...{"feedback": items...})`，token 是函数名 `listFeedback`（定义在 :673）' },
  { key: 'api/requestActions.ts:94 middleware/requestid_mw.go:83-89', note: '83 行是 `generateRequestID()` 的定义；token `request_id` 在函数体内' },
  { key: 'api/sessions.ts:9 cmd/gateway/main.go:7268-7275', note: '7268-7275 是路由清单 `slog.Info`，token `wrapAdmin` 出现在同一段里但不在被引区间' },
  { key: 'api/sessions.ts:11 admin/session_summary_v2.go:89', note: '89 行正是 `if r.Method != http.MethodPost` 那句；token 是端点名 `summary`（不在该行）' },
  { key: 'api/slidingWindow.ts:18 credentialhealth/recorder.go:16-22', note: '同一行是一串链式引用；`slidingWindowQuery` 配的是同行的 `:815` 那条，`recorder.go:16-22` 配的是 `CallEntry`' },
  { key: 'api/taskProfile.ts:27 taskprofile/suggest.go:45-92', note: '45 行是 `Suggest` 的定义；token `ensurePool` 配的是同行另一条引用 `:435-441`' },
  { key: 'api/taskTypeCorrectionStats.ts:20 admin/handler.go:1413', note: '1413 行确实是 `admin(...)` 注册（结论成立）；token `requiresRole` 是**前端概念**，Go 里没有这个词' },
  { key: 'api/turnsFilterOptions.ts:317 admin/turns_filter_options.go:61-73', note: '61-73 就是注释里**逐字引用的那段**租户判断；token 是函数名（定义在 :49）' },
  { key: 'api/usageEnhanced.ts:27 pkg/identity/verifier.go:111', note: '111 行是 `Verify` 的定义；token `supportedRole` 在它函数体内（:120 附近）' },
  { key: 'api/usageTrendSeries.ts:265 admin/usage_trend_series.go:407', note: '407 行是 SQL 读面的 FROM 子句；token `source` 是**对外字段名**' },
]

const REGISTERED: string[] = [...KNOWN_DRIFT, ...KNOWN_FALSE_POSITIVE.map((f) => f.key)]

// ── ① 普查与召回自证 ────────────────────────────────────────────────────────

describe('① Go 行号引用普查：读数 + 召回自证 + 阳性对照', () => {
  it('①-1 ★★★★★ 读数：分母必须写在这里，不许只报「对不上的几条」', () => {
    const refs = allRefs()
    const { counts, bad } = classify(refs)
    console.log(
      `[①-1] api 模块 ${apiModules().length} 个；引用 ${refs.length} 处；` +
        `路径不唯一 ${counts['路径不唯一']} · 无可核 token ${counts['无可核 token']} · ` +
        `行号越界/区间反向 ${counts['行号越界或区间反向']} · ` +
        `可判定 ${counts['可判定']}（其中对不上 ${counts['可判定但对不上']}）`,
    )
    expect(refs.length).toBeGreaterThan(900)
    expect(counts['可判定']).toBeGreaterThan(50)
    // 三类之和 == 总数：普查自己不许漏项（对账）。
    expect(
      counts['路径不唯一'] +
        counts['无可核 token'] +
        counts['行号越界或区间反向'] +
        counts['可判定'],
      '三类之和与总数对不上，分类器漏了项',
    ).toBe(refs.length)
    const unlisted = bad.map((r) => `${r.file}:${r.line} ${r.raw}`).filter((k) => !REGISTERED.includes(k))
    const listed = REGISTERED.filter((k) => !bad.map((r) => `${r.file}:${r.line} ${r.raw}`).includes(k))
    if (unlisted.length || listed.length) console.log('[①-1] 未登记:', unlisted, ' 已修好:', listed)
    expect(bad.length).toBe(REGISTERED.length)
  })

  it('①-2 ★★★★★★ 召回自证：合成一条错引用，扫描器必须抓到并判为对不上', () => {
    // ★ 这条是本判据**承重**的自证：只报「抓到 N 条对不上」而没有正控，
    //   那么 N 可能是 0（扫描器压根没在工作）—— 两种情况输出一样。
    //   合成一条**必然错**的引用：`admin/auth.go:1` 配一个该文件里没有的 token。
    const fake = '// 见 admin/auth.go:1 的 `superAdminMiddlewareAbcDefXyz`\n'
    const refs = scanRefs(fake, 'api/__synthetic__.ts')
    expect(refs, '扫描器没抓到合成引用 ⇒ 它对新写的引用同样会漏').toHaveLength(1)
    const { counts, bad } = classify(refs)
    expect(counts['可判定']).toBe(1)
    expect(bad, '抓到了引用却判成对得上').toHaveLength(1)
  })

  it('①-3 ★★★★ 阳性对照：一条真实**正确**的引用必须判为对得上', () => {
    // 批 136 实测：`api/connectionRegistry.ts` 的列表端点引用
    //   `admin/handler.go:1043`，而那一行逐字是
    //   `mux.HandleFunc("/api/admin/connection-registry", admin(...))`。
    // ⇒ 反向读数：判据不能只会喊「对不上」。
    const src = readFileSync(join(WEB, 'api', 'connectionRegistry.ts'), 'utf8')
    const refs = scanRefs(src, 'api/connectionRegistry.ts')
    const { counts, bad } = classify(refs)
    expect(counts['可判定']).toBe(2)
    expect(bad).toHaveLength(1)
    expect(bad[0]?.from, '唯一判为对不上的应该是 {id} 那条（Go 里是 {request_id}）').toBe(1044)
  })
})

// ── ② 棘轮：不许新欠债 ───────────────────────────────────────────────────────

describe('② ★★ 未登记的漂移一律红', () => {
  it('②-1 ★★★ 对不上的引用必须全部在 KNOWN_DRIFT 里', () => {
    const { bad } = classify(allRefs())
    const unknown = bad.map(key).filter((k) => !REGISTERED.includes(k))
    expect(
      unknown,
      '这些引用判为对不上但没登记 ⇒ 债又欠下了（订正，或在 KNOWN_DRIFT / KNOWN_FALSE_POSITIVE 里显式登记）：\n' +
        unknown.join('\n'),
    ).toEqual([])
  })
})

// ── ③ 棘轮反向：登记项不许悄悄变好 ──────────────────────────────────────────

describe('③ ★★★ 登记项必须仍然对不上（修好了就要删登记）', () => {
  it('③-1 ★★★★ KNOWN_DRIFT 里每一条都仍然判为对不上', () => {
    const { bad } = classify(allRefs())
    const present = new Set(bad.map(key))
    const fixed = REGISTERED.filter((k) => !present.has(k))
    expect(
      fixed,
      '这些登记项已经不判为对不上了 ⇒ 把它们从 KNOWN_DRIFT 里删掉，别让名单变成免死金牌：\n' +
        fixed.join('\n'),
    ).toEqual([])
  })

  it('③-2 ★★★ 名单里不许有重名项（重名会让 ③-1 的比对失去意义）', () => {
    const dup = REGISTERED.filter((k, i) => REGISTERED.indexOf(k) !== i)
    expect(dup).toEqual([])
  })
})

// ── ④ 名单规模钉死 ───────────────────────────────────────────────────────────

describe('④ 名单规模', () => {
  it('④-1 ★★★★ KNOWN_DRIFT 真错名单**恰好 0 条**（本批清零）', () => {
    expect(KNOWN_DRIFT).toHaveLength(0)
  })

  it('④-1b ★★★ KNOWN_FALSE_POSITIVE 恰好 15 条（逐条定性过，不是债）', () => {
    expect(KNOWN_FALSE_POSITIVE).toHaveLength(15)
  })

  it('④-2 ★★ 名单里每一条的 Go 文件都得真实存在（否则是笔误不是债）', () => {
    const ghost = REGISTERED.map((k) => k.split(' ')[1]?.split(':')[0] ?? '').filter((g) => !GO_FILES.has(g))
    expect(ghost, `名单里有不存在的 Go 文件：${ghost.join(', ')}`).toEqual([])
  })

  it('④-3 ★★ 判据自身不得依赖未纳入仓库的路径', () => {
    expect(existsSync(join(ROOT, 'admin', 'handler.go')), 'admin/handler.go 不在预期位置，ROOT 推断错了').toBe(true)
    expect(existsSync(join(WEB, 'api', 'requestAnomalies.ts'))).toBe(true)
  })
})
