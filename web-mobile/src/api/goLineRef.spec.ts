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
 * ★★★★★ **阴性结论带宽度**：修复前那 68 条里初次实测 **32 条对不上（46%）**。
 *   本批订正了其中 **13 条**（逐条打开源码核过真实行，且**确认注释里的结论
 *   仍然成立** —— 光移行号不改结论不算修好），剩 **25 条**（可判定里 24 条 +
 *   区间反向 1 条）登记进 `KNOWN_DRIFT`。
 *   ★ 那 25 条里**混着两类**，本判据分不出来，也不假装分得出来：
 *     · **真错**（token 在所引文件里根本不出现 / 区间反向等畸形）
 *     · **检查器假阳性**（引用其实正确，但 token 不是字面出现 ——
 *       例如注释写 `/api/admin/connection-registry/{id}` 而 Go 里是
 *       `{request_id}`；或注释指的是个常量名而非字面量）
 *   ⇒ 棘轮只管「不许变多」与「登记项不许悄悄变好」，**分类留给人**。
 *
 * ★★ **本判据自身的宽度**（也是它为什么只覆盖 68 条）：
 *   · 端点 token 必须写成 `/api/...` 的字面量；写常量名（如 `dispatchJournalAPIPath`）
 *     的引用判不出来 ⇒ 会落进「无可核 token」那一类，不算错也不算对。
 *   · 标识符 token 要求**小写字母开头**（`queryInt`/`writeError`/`wrapAdmin`），
 *     大写开头的（`SuperAdminMiddleware`）同样落进「无可核 token」。
 *     ⇒ 这是**已知的漏检面**，不是「那些引用是对的」。
 *
 * 判据四组：
 *  ① 普查读数 + **召回自证**（合成一条错引用，扫描器必须抓到）
 *     + **阳性对照**（一条真实正确的引用必须判为 OK）。
 *  ② ★★ 未登记的漂移 ⇒ 红（这是「不许新欠债」的那道门）。
 *  ③ ★★★ 登记项必须**仍然漂移** —— 修好了就把它从名单里删，
 *     否则这条判据会退化成「名单即免死金牌」。
 *  ④ 名单规模钉死。
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
const KNOWN_DRIFT = [
  'api/connectionRegistry.ts:7 admin/handler.go:1044',
  'api/credentialsOps.ts:18 admin/credential_monitor.go:1785-1792',
  'api/credentialsOps.ts:140 admin/routing_reset.go:52',
  'api/dashboard.ts:14 admin/dashboardapi/types.go:187',
  'api/dataLifecycleStats.ts:89 admin/data_lifecycle.go:33-38',
  'api/dispatchJournal.ts:18 admin/journal_handlers.go:30-35',
  'api/dispatchWaterfall.ts:8 cmd/gateway/main.go:7228-7230',
  'api/errorsTrend.ts:38 admin/users.go:406',
  'api/modelHistory.ts:137 admin/handler.go:1534-1544',
  'api/modelPolicies.ts:10 admin/tenants.go:188-190',
  'api/modules.ts:106 settings/spec.go:284-283',
  'api/outputCompliance.ts:131 admin/output_compliance_handler.go:711',
  'api/requestActions.ts:94 middleware/requestid_mw.go:83-89',
  'api/requestAnomalies.ts:70 admin/handler.go:1494-1498',
  'api/requestAnomalies.ts:166 admin/handler.go:1546-1549',
  'api/sessionAnalyticsFilterOptions.ts:78 cmd/gateway/main.go:7085-7089',
  'api/sessions.ts:9 cmd/gateway/main.go:7268-7275',
  'api/sessions.ts:11 admin/session_summary_v2.go:89',
  'api/slidingWindow.ts:18 credentialhealth/recorder.go:16-22',
  'api/taskProfile.ts:27 taskprofile/suggest.go:45-92',
  'api/taskTypeCorrectionStats.ts:20 admin/handler.go:1413',
  'api/turnsFilterOptions.ts:317 admin/turns_filter_options.go:61-73',
  'api/usageEnhanced.ts:27 pkg/identity/verifier.go:111',
  'api/usageTrendSeries.ts:143 admin/usage_trend_series.go:22-24',
  'api/usageTrendSeries.ts:265 admin/usage_trend_series.go:407',
]

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
    const unlisted = bad.map((r) => `${r.file}:${r.line} ${r.raw}`).filter((k) => !KNOWN_DRIFT.includes(k))
    const listed = KNOWN_DRIFT.filter((k) => !bad.map((r) => `${r.file}:${r.line} ${r.raw}`).includes(k))
    if (unlisted.length || listed.length) console.log('[①-1] 未登记:', unlisted, ' 已修好:', listed)
    expect(bad.length).toBe(KNOWN_DRIFT.length)
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
    const unknown = bad.map(key).filter((k) => !KNOWN_DRIFT.includes(k))
    expect(
      unknown,
      '这些引用判为对不上但没登记 ⇒ 债又欠下了（要么订正，要么显式登记）：\n' + unknown.join('\n'),
    ).toEqual([])
  })
})

// ── ③ 棘轮反向：登记项不许悄悄变好 ──────────────────────────────────────────

describe('③ ★★★ 登记项必须仍然对不上（修好了就要删登记）', () => {
  it('③-1 ★★★★ KNOWN_DRIFT 里每一条都仍然判为对不上', () => {
    const { bad } = classify(allRefs())
    const present = new Set(bad.map(key))
    const fixed = KNOWN_DRIFT.filter((k) => !present.has(k))
    expect(
      fixed,
      '这些登记项已经不判为对不上了 ⇒ 把它们从 KNOWN_DRIFT 里删掉，别让名单变成免死金牌：\n' +
        fixed.join('\n'),
    ).toEqual([])
  })

  it('③-2 ★★★ 名单里不许有重名项（重名会让 ③-1 的比对失去意义）', () => {
    const dup = KNOWN_DRIFT.filter((k, i) => KNOWN_DRIFT.indexOf(k) !== i)
    expect(dup).toEqual([])
  })
})

// ── ④ 名单规模钉死 ───────────────────────────────────────────────────────────

describe('④ 名单规模', () => {
  it('④-1 ★★★ KNOWN_DRIFT 恰好 25 条（订正一条就要同步改这里）', () => {
    expect(KNOWN_DRIFT).toHaveLength(25)
  })

  it('④-2 ★★ 名单里每一条的 Go 文件都得真实存在（否则是笔误不是债）', () => {
    const ghost = KNOWN_DRIFT.map((k) => k.split(' ')[1]?.split(':')[0] ?? '').filter((g) => !GO_FILES.has(g))
    expect(ghost, `名单里有不存在的 Go 文件：${ghost.join(', ')}`).toEqual([])
  })

  it('④-3 ★★ 判据自身不得依赖未纳入仓库的路径', () => {
    expect(existsSync(join(ROOT, 'admin', 'handler.go')), 'admin/handler.go 不在预期位置，ROOT 推断错了').toBe(true)
    expect(existsSync(join(WEB, 'api', 'requestAnomalies.ts'))).toBe(true)
  })
})
