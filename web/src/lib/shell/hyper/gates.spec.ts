/**
 * hyper/gates.spec.ts — Hyper 壳层的跨文件门禁（docs/UI规范/00 §5.4 · H2）。
 *
 * 存在理由：一个真实踩过的坑。
 * `styles/hyper.css` 第一版写的是
 *   `.app-topbar__lang` / `__theme` / `__user`
 * 这三个类名在 `AppTopbar.vue` 里**根本不存在**（它直接渲染 ThemeToggle /
 * LanguageSelector / UserMenuDropdown 子组件，根类名分别是 `.theme-toggle` /
 * `.language-selector` / `.user-menu`）。规则完整、语法正确、构建通过、
 * 门禁全绿 —— 但**完全没生效**，compact 顶栏照样堆着三个系统设置控件。
 *
 * 「CSS 规则打空」是单文件 spec 抓不到的：被测文件自己不会说自己没用上。
 * 所以这里做**跨文件**校验：hyper.css 里出现的每个类选择器，必须能在
 * src/ 下真实存在。
 */
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import vm from 'node:vm'
import { describe, expect, it } from 'vitest'
import { MEDIA_QUERY_WHITELIST } from '../../../config/breakpoints'
import { WEB_CAPABILITIES } from './capabilities'

const SRC = resolve(process.cwd(), 'src')

/** 递归收集 .vue/.ts/.css 源文件。 */
function collectFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === 'dist') continue
    const p = join(dir, entry)
    const st = statSync(p)
    if (st.isDirectory()) collectFiles(p, out)
    else if (/\.(vue|ts|css)$/.test(entry)) out.push(p)
  }
  return out
}

const ALL_FILES = collectFiles(SRC)
const ALL_TEXT = ALL_FILES.map((f) => readFileSync(f, 'utf8')).join('\n')

/** 剥掉注释，避免把说明文字里的类名当成真实选择器。 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1')
}

const hyperCss = stripComments(readFileSync(resolve(SRC, 'styles/hyper.css'), 'utf8'))

/** 取出 hyper.css 里所有 class 选择器片段。 */
function classSelectors(css: string): string[] {
  const out = new Set<string>()
  // 形如 `.foo` 或 `.foo,` 的类名 token
  for (const m of css.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)) out.add(m[1])
  return [...out]
}

describe('hyper.css：不存在死选择器', () => {
  const selectors = classSelectors(hyperCss)

  it('至少解析出若干选择器（防止正则失效导致空集恒真）', () => {
    expect(selectors.length).toBeGreaterThan(3)
  })

  it.each(classSelectors(hyperCss))('选择器 .%s 在 src/ 下真实存在', (cls) => {
    // 用 `class="... cls ..."` 或 CSS/TS 里的 `.cls` 两种形态找。
    // 只搜类名 token 会出现同名子串误判，所以要求出现在类属性里或作为选择器。
    const inClassAttr = new RegExp(`class="[^"]*\\b${cls.replace(/[-/\\^$*+?.()|[\]{}]/g, '\\$&')}\\b`).test(ALL_TEXT)
    const asSelector = new RegExp(`\\.${cls.replace(/[-/\\^$*+?.()|[\]{}]/g, '\\$&')}\\b`).test(
      ALL_FILES.filter((f) => f !== resolve(SRC, 'styles/hyper.css'))
        .map((f) => stripComments(readFileSync(f, 'utf8')))
        .join('\n'),
    )
    expect(inClassAttr || asSelector, `选择器 .${cls} 在 src/ 里找不到对应定义 —— 规则会打空`).toBe(true)
  })

  it('不引入白名单外的断点（768 由 config 派生，脚本会归到 768）', () => {
    const widths = [...hyperCss.matchAll(/max-width:\s*([\d.]+)px/g)].map((m) => parseFloat(m[1]))
    for (const w of widths) {
      // 只允许 767.98（compact 边界）这类由 window-class 派生的分数边界
      expect(Number.isInteger(w) ? [480, 640, 768, 1024, 1440] : true).toBeTruthy()
    }
  })

  it('不用范围语法（width <= / width >=）', () => {
    expect(hyperCss).not.toMatch(/width\s*[<>]/)
  })
})

describe('compact 顶栏红线（规范 02 §4）', () => {
  const topbar = readFileSync(resolve(SRC, 'components/shell/AppTopbar.vue'), 'utf8')
  const topbarCss = stripComments(
    readFileSync(resolve(SRC, 'styles/hyper.css'), 'utf8'),
  )

  it('hyper.css 必须同时隐藏主题/语言/用户三个系统设置控件', () => {
    for (const cls of ['.theme-toggle', '.language-selector', '.user-menu']) {
      expect(topbarCss, `缺少 ${cls} 的 compact 隐藏规则`).toContain(cls)
    }
  })

  it('这些类名确实来自 AppTopbar 渲染的子组件（防止规则指向错对象）', () => {
    expect(topbar).toContain('<ThemeToggle')
    expect(topbar).toContain('<LanguageSelector')
    expect(topbar).toContain('<UserMenuDropdown')
  })

  it('隐藏规则限定在 compact 媒体查询内（桌面不受影响）', () => {
    const idx = topbarCss.indexOf('.theme-toggle')
    const before = topbarCss.lastIndexOf('@media', idx)
    expect(before).toBeGreaterThan(-1)
    expect(topbarCss.slice(before, idx)).toContain('max-width: 767.98px')
  })
})

/**
 * ## 单列收拢断点：900 / 960 已并入 768（2026-10-04）
 *
 * 背景：`config/breakpoints.ts` 规定 `tablet: 768 = >= 768 是 2 列栅格`，
 * 而这 3 个文件原先用 900 / 900 / 960 把两列收成单列 —— 落在 768 与 1024
 * 之间的「无主区」，与 `tablet` 语义直接矛盾：768~959px 的平板宽度下，
 * 这三处仍按两列排布，而同仓其它地方早已按单列收。
 *
 * ### 这道门钉的是什么（**不是**「不许出现白名单外断点」）
 *
 * `responsive:check --strict` 已经能抓住「退回 900 / 960」—— 那会让白名单外
 * 断点数从 1 变 2。**它抓不到的是换成另一个白名单内的合法值**：把 768 改成
 * 1024 或 480，`responsive:check` 全绿，而这三处的收拢时机被静默挪动。
 * ⇒ 所以这里用**独立字面量**钉死期望值，而不是复用聚合阈值。
 *
 * ### 两个刻意的设计（都踩过坑才这么写）
 *
 * 1. **名单是字面量，不从文件系统推导。** 若写成
 *    `collectFiles(...).filter(f => PINNED.includes(f))` 之类的自指形态，
 *    「哪些文件该被钉住」就由被测对象自己决定 —— 与 R1 门里
 *    `PINNED = [...TOUCH_FILES]` 那种恒真的假门同型，删掉名单即等于关掉门。
 * 2. **先 `stripComments` 再提取。** 生产脚本 `responsive-audit.mjs:80`
 *    直接吃原文（不剥注释）。实测：往 `UserDetailDrawer.vue` 塞一行
 *    `/* @media (max-width: 1100px) 历史值，已停用 *\/`，生产脚本把它报成
 *    「1100px x1 ✗ 白名单外（存量）」，而同一次注入下本门提取器只看到 `[768]`。
 *    也就是说生产脚本会把注释掉的断点当成真实欠账，把人引去改一个不存在的缺陷。
 *    该差异已登记在 docs/UI规范/10 §4.6.9；**本门刻意不跟随** ——
 *    钉值门宁可看不见注释里的假断点，而生产脚本保留注释是为了顺带报出
 *    「注释里写着的待清理值」，那是另一种用途。
 *    prelude 与宽度取值两条正则与生产脚本逐字相同（见下方 `extractWidthHits`）。
 *
 * ### 本门与 `responsive:check` 的分工（实测，2026-10-04）
 *
 * 生产脚本**在任何配置下都无法为「新违规」变红而不同时在干净基线就红**：
 *   - `--strict` 单独用 ⇒ 因 1080 存量单挂而**恒红**（基线 rc=1）；
 *   - `--strict --allow-legacy` ⇒ `--allow-legacy` 对白名单外命中是**一刀切**豁免，
 *     不分新旧。实测注入全新 950px 违规后仍 rc=0，只从「1 处」变成「2 处 WARN」。
 * 变异台账 M1–M5 全部落在 `本门 rc=1 / 生产脚本 rc=0`。
 * ⇒ 生产门禁的饱和与本门的覆盖互补，不是重复。生产脚本本身的修法见 §4.6.9。
 *
 * ### 2026-10-05 增补：1080 清偿后上述「恒红」前提已消失
 *
 * `TenantDetailView` 1080 → 1024 后，`responsive:check`（= `--strict` 单独用）
 * 干净基线 rc=0、新违规 rc=1，生产脚本不再饱和。本门保留的价值：
 * ① 钉的是**登记集合**（`LEGACY_OUTSIDE_WHITELIST`，现为空）而非仅
 *   「有没有白名单外」，白名单内换值挪动收拢时机仍只有本门看得见
 *   （见上方 768 例的报错语义）；② `--allow-legacy` 的一刀切豁免缺陷仍在，
 *   CI 若有人图省事加回该旗，本门是唯一不受影响的钉值门。
 */
describe('单列收拢断点：900 / 960 已并入 768（2026-10-04）', () => {
  /** 期望收敛到 768 的三个文件（原值逐个记在注释里，改动时不要抹掉）。 */
  const COLLAPSE_TO_768: readonly { file: string; was: number; why: string }[] = [
    { file: 'components/UserDetailDrawer.vue', was: 900, why: '详情抽屉两列网格，与 tablet 768 冲突' },
    { file: 'views/admin/UsageTrendExplorer.vue', was: 900, why: '用量趋势筛选区收单列' },
    { file: 'views/admin/ReconciliationReport.vue', was: 960, why: '对账报表列收单列' },
  ]

  /**
   * 全仓允许的白名单外断点登记表（存量，2026-10-04 单挂 → 2026-10-05 清偿）。
   * 登记/清偿它是为了让「欠账既不增长也不被顺手抹掉」：改了要连同本表一起改。
   * 2026-10-05：`TenantDetailView.vue:1179` 1080 → 1024（Step 8 收敛，见
   * §4.6.7.1），表清空 ⇒ 本门升格为「恰好 0 处」的更强不变量。
   */
  const LEGACY_OUTSIDE_WHITELIST: readonly { file: string; value: number }[] = []

  /**
   * 与 `scripts/responsive-audit.mjs:80-92` 逐字相同的提取器。
   * 刻意复制而非 import：那个脚本是 CLI（读 argv、走 process.exit），
   * 在 spec 里 import 只会把门禁和命令行入口焊死在一起。
   */
  function extractWidthHits(content: string): { value: number; line: number }[] {
    const hits: { value: number; line: number }[] = []
    const preludeRe = /@(?:media|container)([^{]*){/g
    let m
    while ((m = preludeRe.exec(content)) !== null) {
      const prelude = m[1]
      const line = content.slice(0, m.index).split('\n').length
      const valueRe = /(?:min-width|max-width|width)\s*:\s*(\d+)px/g
      let v
      while ((v = valueRe.exec(prelude)) !== null) hits.push({ value: parseInt(v[1], 10), line })
    }
    return hits
  }

  const hitsOf = (rel: string) => extractWidthHits(stripComments(readFileSync(resolve(SRC, rel), 'utf8')))

  it('量具自证：三个被钉文件都真的解析出了宽度断点（防空集恒真）', () => {
    for (const { file } of COLLAPSE_TO_768) {
      expect(hitsOf(file).length, `${file} 一条宽度断点都没解析出来，提取器已腐化`).toBeGreaterThan(0)
    }
  })

  it('量具自证：全仓断点规模与生产审计同量级（防全仓提取整体失效）', () => {
    const total = ALL_FILES.reduce((n, f) => n + extractWidthHits(stripComments(readFileSync(f, 'utf8'))).length, 0)
    // 生产脚本实测 112 处（2026-10-04）。此处取下界而非等值：
    // 真实增减是正常的，取等值只会让门在某次正常改动后变成噪音。
    expect(total, '全仓断点数远低于生产审计规模，提取器多半没生效').toBeGreaterThan(80)
  })

  it.each(COLLAPSE_TO_768)('$file 的收拢断点恰好是 768px（原 $waspx）', ({ file, was }) => {
    const hits = hitsOf(file)
    expect(
      hits.map((h) => h.value),
      `${file} 期望只有一条宽度断点且值为 768（原为 ${was}px）。` +
        `实测 ${JSON.stringify(hits)}。改成 480/640/1024/1440 同样是错的：` +
        `白名单内换值 responsive:check 抓不到，但收拢时机被静默挪动。`,
    ).toEqual([768])
  })

  it('白名单外断点恰好 0 处（1080 存量已于 2026-10-05 清偿为 1024）', () => {
    const whitelist = new Set(MEDIA_QUERY_WHITELIST)
    // 行号只进报错信息、不进断言：无关改动会让行号移位，
    // 把行号钉进期望值等于给门配一个必然过期的前提。
    const outside: { loc: string; line: number }[] = []
    for (const f of ALL_FILES) {
      const rel = f.slice(SRC.length + 1)
      for (const h of extractWidthHits(stripComments(readFileSync(f, 'utf8')))) {
        if (!whitelist.has(h.value)) outside.push({ loc: `${rel}:${h.value}px`, line: h.line })
      }
    }
    expect(
      outside.map((o) => o.loc),
      '白名单外断点集合与登记不符。新增 = 欠账增长；清零 = 有人顺手改了却没更新本表。\n' +
        '实测（含行号）：\n' + outside.map((o) => `  ${o.loc}@L${o.line}`).join('\n') +
        '\n当前登记（改动时请一并更新 LEGACY_OUTSIDE_WHITELIST）：\n' +
        LEGACY_OUTSIDE_WHITELIST.map((l) => `  ${l.file}: ${l.value}px`).join('\n'),
    ).toEqual(LEGACY_OUTSIDE_WHITELIST.map((l) => `${l.file}:${l.value}px`))
  })
})

/**
 * ## 跨语言契约：插件 manifest 的 `label_key` 必须在 8 个 locale 里都存在
 *
 * 存在理由：一个**当前没有任何门覆盖**的方向。
 *
 * `plugin-runtime/nav_validate.go:94-101` 强制每个插件导航页声明一个合法点分
 * i18n key（`nav.label_key required` + 正则），前端 `remoteNavToNavItems`
 * 把 `label_key` 交给 `t()` 运行期解析。
 * ⇒ **契约两端各有一道门，唯独中间那句「这个 key 在 locale 里存在吗」没人管**：
 * 插件作者改一个 key 名，Go 侧校验照过、`manifest_test.go` 照过，
 * UI 侧静默渲染成裸 key 或回落英文，**全链路无一处变红**。
 *
 * `i18n/parity.test.ts` 覆盖的是「locale 之间互为超集」与「src/ 里引用的 key 存在」，
 * **不含 manifest 来源的 key**——静态抽取器在原理上看不到它们（§4.6.8）。
 *
 * ### 真源从哪来（这一条决定了门是否恒真）
 *
 * 键集从 **`plugin-runtime/testdata/*.json` 的 `label_key`** 推导，
 * **不是**从 Go 源码里 grep `nav.*` 字面量。
 * 差别是实测过的：Go 侧还有 `registry_test.go:10` 的 `nav.item.x`、
 * `nav_validate_test.go:17` 的 `nav.plugin.title` —— 那是**单测脚手架**，
 * 本来就不该在 locale 里存在，按字面量推导会得到一道**永久红**的门。
 * manifest JSON 才是插件真正发布的那份契约。
 *
 * ### 与 `i18n/parity.test.ts` 的重叠（实测，别当成完全互补）
 *
 * 逐条变异对照（每条单独施加、单独跑两道门、单独还原并验字节一致）：
 *
 * | 变异 | parity | 本门 | 判定 |
 * | --- | --- | --- | --- |
 * | 只把 `de-DE` 的 `sessionPlugin` 改名 | rc=1 | rc=1 | **两门都抓**（parity 断言每语种 ⊇ zh-CN） |
 * | 只把 `de-DE` 的 `sessionPluginSettings` 改名 | rc=1 | rc=1 | **两门都抓** |
 * | manifest 指向一个**所有语种都没有**的键 | **rc=0** | rc=1 | **只有本门抓到** |
 * | manifest 新增页面 + 全新 `label_key` | **rc=0** | rc=1 | **只有本门抓到** |
 *
 * ⇒ **本门唯一不可替代的覆盖是「键来自 manifest 侧」这个方向**。
 * 前两行是冗余的（冗余不是坏事：parity 的口径若变，这一段仍有人接），
 * 但**不能拿它们当本门的立功证据**。后两行才是：parity 比的是语种之间、
 * 扫的是 `src/` 里的字面量，而一个**所有语种都没有**的键对它天然不可见。
 *
 * ### 为什么不 import `parity.test.ts` 的 `collectLeafKeys`
 *
 * 它确实 `export` 了。但 import 一个 `.test.ts` 会让它的 `describe` 块在**本文件
 * 的上下文里二次注册** ⇒ 那些用例在一次运行里被数两遍，测试总数虚高、失败信息
 * 归属错乱。宁可复制一份 loader 技术，也不去动那道成熟门禁的注册语义。
 * ⚠️ 复制的只是**手法**（`vm` 求值 + 递归收集叶子键），不是真源：
 * 两边读的仍是同一批 locale 文件。
 */
describe('跨语言契约：manifest label_key → 8 locale', () => {
  const REPO_ROOT = resolve(SRC, '..', '..')
  const TESTDATA = resolve(REPO_ROOT, 'plugin-runtime', 'testdata')
  const LOCALES_DIR = resolve(SRC, 'locales')
  const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']

  // ---- locale 侧：vm 求值 + 递归收集叶子键（手法同 i18n/parity.test.ts） ----
  function evalAsCjs(code: string, absPath: string): Record<string, unknown> {
    const moduleObj: { exports: Record<string, unknown> } = { exports: {} }
    const sandbox = { module: moduleObj }
    vm.createContext(sandbox)
    vm.runInContext(code, sandbox, { filename: absPath })
    return moduleObj.exports
  }
  function loadModuleFile(locale: string, moduleName: string): Record<string, unknown> {
    const absPath = join(LOCALES_DIR, locale, `${moduleName}.ts`)
    const code = readFileSync(absPath, 'utf8').replace(/^export default /m, 'module.exports = ')
    return evalAsCjs(code, absPath)
  }
  function loadLocale(locale: string): Record<string, unknown> {
    const src = readFileSync(join(LOCALES_DIR, locale, 'index.ts'), 'utf8')
    const importRe = /^\s*import\s+(\w+)\s+from\s+['"]\.\/(\w+)['"]\s*$/gm
    const merged: Record<string, unknown> = {}
    let m: RegExpExecArray | null
    while ((m = importRe.exec(src)) !== null) merged[m[1]] = loadModuleFile(locale, m[2])
    return merged
  }
  const isPlainObject = (v: unknown): v is Record<string, unknown> =>
    typeof v === 'object' && v !== null && !Array.isArray(v)
  function collectLeafKeys(obj: unknown, prefix = ''): Set<string> {
    const keys = new Set<string>()
    if (!isPlainObject(obj)) return keys
    for (const [k, v] of Object.entries(obj)) {
      const path = prefix ? `${prefix}.${k}` : k
      if (Array.isArray(v)) continue
      if (isPlainObject(v)) collectLeafKeys(v, path).forEach((nk) => keys.add(nk))
      else if (v !== null && v !== undefined) keys.add(path)
    }
    return keys
  }

  // ---- manifest 侧：递归捞出任何 `label_key` 字符串 ----
  const manifestFiles = existsSync(TESTDATA)
    ? readdirSync(TESTDATA).filter((f) => f.endsWith('.json')).sort()
    : []
  const manifestLabelKeys = ((): string[] => {
    const out: string[] = []
    const walk = (node: unknown) => {
      if (Array.isArray(node)) return node.forEach(walk)
      if (!isPlainObject(node)) return
      for (const [k, v] of Object.entries(node)) {
        if (k === 'label_key' && typeof v === 'string') out.push(v)
        else walk(v)
      }
    }
    for (const f of manifestFiles) walk(JSON.parse(readFileSync(join(TESTDATA, f), 'utf8')))
    return [...new Set(out)].sort()
  })()

  /** 每个 locale 的全部叶子键（含 namespace 前缀，如 `nav.item.sessionPlugin`）。 */
  const localeKeys = new Map<string, Set<string>>(
    LOCALES.map((l) => [l, collectLeafKeys(loadLocale(l))]),
  )

  it('量具自证：testdata 目录存在且有 manifest（防目录改名后判据恒真）', () => {
    expect(manifestFiles.length, `${TESTDATA} 下没有 *.json manifest`).toBeGreaterThan(0)
  })

  it('量具自证：确实从 manifest 里捞出了 label_key（防 JSON 形状变化后空集恒真）', () => {
    expect(
      manifestLabelKeys.length,
      '一份 manifest 都没解析出 label_key —— 要么 manifest 形状变了，' +
        '要么键名不再叫 label_key。两种都得改这道门，而不是让它悄悄通过。',
    ).toBeGreaterThanOrEqual(2)
  })

  it('量具自证：每个 locale 都真的求值出了键集，且都含 nav 命名空间', () => {
    for (const [locale, keys] of localeKeys) {
      expect(keys.size, `${locale} 叶子键为空，vm loader 或 index.ts 装配坏了`).toBeGreaterThan(100)
      const navLeaves = [...keys].filter((k) => k.startsWith('nav.')).length
      expect(navLeaves, `${locale} 没有任何 nav.* 叶子键，nav 模块没被装配进来`).toBeGreaterThan(0)
    }
  })

  it('每个 manifest label_key 在 8 个 locale 里都存在', () => {
    const missing: string[] = []
    for (const key of manifestLabelKeys) {
      for (const locale of LOCALES) {
        if (!localeKeys.get(locale)!.has(key)) missing.push(`${key} 缺于 ${locale}`)
      }
    }
    expect(
      missing,
      '插件 manifest 声明的 label_key 在 locale 里不存在 ⇒ UI 会静默渲染成裸 key。\n' +
        `缺失明细（${missing.length} 条）：\n` + missing.map((m) => `  ${m}`).join('\n') +
        '\n修法：在 src/locales/<8 个语种>/ 对应模块里补上该键。' +
        '若该键确实该废弃，请改 manifest 让两端一致，不要单边删 locale。',
    ).toEqual([])
  })

  it('manifest label_key 全部落在 nav.* 命名空间内（与 nav_validate.go 的形状要求一致）', () => {
    const strays = manifestLabelKeys.filter((k) => !k.startsWith('nav.'))
    expect(
      strays,
      '这些 label_key 不在 nav.* 下。可能是合法的其他命名空间，' +
        '但请先确认前端 t() 能解析到——本门只覆盖 nav.* 以外的形状不报错。',
    ).toEqual([])
  })
})

/**
 * ## 手势仲裁必须单点（规范 12 §1 / §6 的可执行版）
 *
 * 规范 12 §6 结尾写着一句前瞻规则：
 * > 「§1 与 §2 不完成前，不要在 compact 上加任何滑动交互。」
 *
 * 那是给人读的规矩，**没有门就会烂掉**。本门把它变成可执行的：
 * 全仓的 `pointerdown` / `pointermove` / `pointerup` / `pointercancel` /
 * `touchstart` 绑定**只允许出现在一个文件里** —— `components/ui/AppDrawer.vue`，
 * 也就是把决策交给 `lib/shell/hyper/dragDismiss` 的那个仲裁点。
 *
 * ### 为什么必须是「单点」而不是「每个组件各管各的」
 *
 * 规范 12 §1 要求「必须显式仲裁，不得各自监听」。一旦允许第二个组件自己挂
 * `pointerdown`，就会重演它自己列的失败模式：弹层拖拽与内容滚动各监听各的，
 * 谁也不让谁，斜着划既不滚动也不关闭。**所以这不是风格偏好，是把 §1 钉住。**
 *
 * ### 这道门**不**管的事
 *
 * 桌面端与存量页面里已有的手势代码不在本门范围内 —— 全仓当前只有
 * `components/detail/SessionTurnsSyncPane.vue` 用过 touch，与本专题无关。
 * 本门盯的是**新增**的滑动交互别绕过仲裁点。
 */
describe('手势仲裁单点（规范 12 §1 / §6）', () => {
  /**
   * 已登记的**其它**指针手势消费者。沿用 `navCoverage.test.ts` 的 `KNOWN_ORPHANS`
   * 形态：每条带具体理由，因为「白名单里写同上」等于没有约束。
   */
  const REGISTERED_OTHERS: Record<string, string> = {
    'components/detail/SessionTurnsSyncPane.vue':
      '面板 resize 分隔线的 pointer 拖拽（onDividerDown/Move/Up，挂在 window 上）。' +
      '它不是 §1 仲裁表里的任何一格：不是弹层关闭、不是内容滚动、不是下拉刷新、' +
      '也不是边缘侧滑返回。属桌面面板布局，与 compact 表面无关。',
  }

  /** 允许**直接**绑定指针事件的唯一仲裁点。 */
  const ARBITRATION_FILES: readonly string[] = ['components/ui/AppDrawer.vue']

  const POINTER_BINDINGS =
    /@(?:pointerdown|pointermove|pointerup|pointercancel|touchstart|touchmove|touchend)(?:\.[\w.]+)?\s*=/
  /** matchAll 必须用全局正则；另开一份而不是给 POINTER_BINDINGS 加 g
   *  —— 带 g 的正则做 .test() 是**有状态**的（lastIndex 残留），会漏判。 */
  const POINTER_BINDINGS_G = new RegExp(POINTER_BINDINGS.source, 'g')

  const offenders = ALL_FILES.filter((f) => {
    const rel = f.slice(SRC.length + 1)
    if (ARBITRATION_FILES.includes(rel) || rel in REGISTERED_OTHERS) return false
    return POINTER_BINDINGS.test(stripComments(readFileSync(f, 'utf8')))
  }).map((f) => f.slice(SRC.length + 1))

  it('量具自证：扫描器确实找到了仲裁点自己（防正则失效导致空集恒真）', () => {
    const src = readFileSync(resolve(SRC, ARBITRATION_FILES[0]), 'utf8')
    const hits = [...src.matchAll(POINTER_BINDINGS_G)].map((m) => m[0])
    expect(
      hits.length,
      `${ARBITRATION_FILES[0]} 上一个指针事件绑定都没有 —— 扫描器多半已腐化，` +
        '或仲裁点被误删。这道门会因为空集而假装通过。',
    ).toBeGreaterThanOrEqual(3)
  })

  it('仲裁点必须真的走 dragDismiss，而不是自己手写阈值', () => {
    const src = readFileSync(resolve(SRC, ARBITRATION_FILES[0]), 'utf8')
    // ⚠️ 必须钉 **import 语句**，不能只匹配 `DragDismiss` 这个词。
    // 变异 G2 实测：删掉 import 后，`let drag: DragDismiss | null` 与
    // `new DragDismiss(` 仍在，裸词匹配照样通过 ⇒ 判据形同虚设。
    expect(
      src,
      '仲裁点没 import DragDismiss ⇒ 它可能在别处就地手写了阈值，' +
        '方向锁与速度判定就不再由唯一真源裁决。',
    ).toMatch(/import\s*\{[^}]*\bDragDismiss\b[^}]*\}\s*from\s*['"][^'"]*dragDismiss/)
    // 单一真源：阈值只能在 dragDismiss.ts 里定义一次
    const core = readFileSync(resolve(SRC, 'lib/shell/hyper/dragDismiss.ts'), 'utf8')
    expect(core, '阈值常量必须住在纯状态机里').toMatch(/thresholdRatio/)
  })

  it('仲裁点名单不得被悄悄扩容（新增仲裁点必须重新论证 §1 的争用顺序）', () => {
    expect(
      ARBITRATION_FILES.length,
      '新增了第二个仲裁点。请先回答：它与既有仲裁点的优先级顺序是什么？' +
        '规范 12 §1 要求「显式仲裁，不得各自监听」。',
    ).toBe(1)
  })

  it('已登记条目必须仍然有效：文件还在、且理由仍然具体（防白名单腐烂）', () => {
    const stale = Object.keys(REGISTERED_OTHERS).filter((rel) => !existsSync(resolve(SRC, rel)))
    expect(stale, `登记的文件已不存在，应移除登记：${stale.join(', ')}`).toEqual([])
    const weak = Object.entries(REGISTERED_OTHERS)
      .filter(([, reason]) => reason.trim().length < 20)
      .map(([rel, r]) => `${rel} → "${r}"（${r.trim().length} 字符）`)
    expect(weak, `登记理由过短等于没有约束：\n${weak.join('\n')}`).toEqual([])
  })

  it('登记条目不得凭空登记：未被门覆盖的文件进白名单就等于关掉门', () => {
    const notActuallyOffending = Object.keys(REGISTERED_OTHERS).filter(
      (rel) => !POINTER_BINDINGS.test(stripComments(readFileSync(resolve(SRC, rel), 'utf8'))),
    )
    expect(
      notActuallyOffending,
      '这些文件已经不绑指针事件了，登记应移除（否则白名单会变成一张什么都豁免的废纸）：\n' +
        notActuallyOffending.map((f) => `  ${f}`).join('\n'),
    ).toEqual([])
  })

  it('全仓不得有第二个组件直接监听指针事件', () => {
    expect(
      offenders,
      `这些文件直接绑了指针事件，绕过 ${ARBITRATION_FILES[0]} 的仲裁：\n` +
        offenders.map((f) => `  ${f}`).join('\n') +
        '\n请改为经由 lib/shell/hyper/dragDismiss，或按 §1 登记为具名消费者。',
    ).toEqual([])
  })
})

/**
 * ## 令牌存在性门禁（限本专题拥有的文件）
 *
 * 存在理由：真实踩过的坑，而且 `color:check` **结构上看不见**它。
 *
 * 本轮 5 个新组件里用了 6 个 `style.css` 里**不存在**的令牌名
 * （`--kx-fg` / `--kx-fg-muted` / `--kx-accent` / `--kx-bg-elevated` /
 * `--kx-ok` / `--kx-warn`），写法是 `var(--kx-fg, #e6e8ee)`。后果有两层：
 *   1. 字面兜底被 color 门禁拦下（这层 color:check 看得见）；
 *   2. **令牌名本身不存在** —— 按 color 门禁要求去掉兜底后，
 *      `var(--kx-fg)` 解析为空，颜色回落到继承值或初始值。
 *      而这层 color:check 完全看不见：没有字面色，就没有违规。
 *
 * ★ 为什么只扫本专题的文件：全仓实测有 **74 处**跨文件未定义令牌
 * （`--kx-text-secondary` 7 个文件、`--card-bg` 7 个、`--primary` 6 个……），
 * 其中 `--kx-bg-elevated` / `--kx-accent` 在 `NodeStatusMatrix`、
 * `QueuePerspectivePanel`、`RoutingAttemptsTimeline` 等**存量**文件里也在用 ——
 * 也就是说这不是我独创的名字，而是仓库里既有的坏惯例。
 * 把门开在整个 src/ 上会立刻撞上 74 处存量欠账，那属于另一件事。
 * 这里只保证**本专题新增/修改的代码不再制造这一类**。
 *
 * 判据：`var(--x)` 有效 ⟺ `--x` 在 `src/style.css` 定义过，
 * **或**在同一个文件里定义过（组件自定义局部令牌是合法实践）。
 */
describe('CSS 令牌存在性：var(--x) 不得引用未定义的令牌', () => {
  const styleCss = readFileSync(resolve(SRC, 'style.css'), 'utf8')

  /** 全局令牌（style.css，含 [data-theme] 覆盖块）。 */
  const globalTokens = new Set([...styleCss.matchAll(/(--[a-zA-Z0-9-]+)\s*:/g)].map((m) => m[1]))

  /** 本专题拥有的文件。刻意不写成通配：范围要能被人一眼核对。 */
  const OWNED = [
    'styles/hyper.css',
    'config/window-class.ts',
    'composables/useWindowClass.ts',
    'composables/useDataViewMode.ts',
    'components/shell/AppBottomNav.vue',
    'components/shell/AppAccountSheet.vue',
    'components/shell/V1DataFrozenBanner.vue',
    'components/ui/CardList.vue',
    'components/ui/ResponsiveDataView.vue',
    'components/ui/HyperLoadMore.vue',
  ]
  // D21（2026-10-04）：横幅原先不在 OWNED 里，于是下面两条门都够不着它 ——
  //   ①「不得引用未定义令牌」：它写的 `var(--kx-warning-surface, …)` 那四个令牌全仓不存在；
  //   ②「不得出现 var(--x, #字面色)」：形态一模一样，也没报。
  //   代价是暗色主题下横幅对比度 1.10:1（失败态 1.02:1）＝ 隐形。
  // ⇒ 这两条门**本来就是对的**，只是漏了这个文件。补进 OWNED 即可，不要另写一套。
  // ⚠️ 但 ① 仍然**看不见**「只在浅色块定义、深色块没定义」：它的 globalTokens 是
  //    两个主题块的并集。分主题的那一维由 `V1DataFrozenBanner.test.ts` 的 D21 钉承担，
  //    两者互补，不是重复。

  /** 剥掉全部注释（块/模板/行）。只剥块注释不够：行注释里也可能有令牌名。 */
  const OWNED_TEXT = OWNED.map((rel) => stripComments(readFileSync(resolve(SRC, rel), 'utf8')))

  /**
   * 局部令牌定义。★ 必须容忍**引号**：组件里最常见的写法是
   * `:style="{ '--rdv-table-min-width': w }"` —— 键名带引号，
   * 不容忍的话这个正则匹配不到，会把合法定义误报成「未定义」。
   * 这是这道门禁被 H6 切片第一次真实使用时打出来的。
   */
  function localTokensOf(index: number): Set<string> {
    return new Set(
      [...OWNED_TEXT[index].matchAll(/['"]?(--[a-zA-Z0-9-]+)['"]?\s*:/g)].map((m) => m[1]),
    )
  }

  /** 找出「用了但没定义」的令牌。局部定义算已定义。 */
  function undefinedTokens(): Map<string, string[]> {
    const bad = new Map<string, string[]>()
    for (let i = 0; i < OWNED.length; i++) {
      const local = localTokensOf(i)
      for (const m of OWNED_TEXT[i].matchAll(/var\(\s*(--[a-zA-Z0-9-]+)/g)) {
        if (globalTokens.has(m[1]) || local.has(m[1])) continue
        const list = bad.get(m[1]) ?? []
        list.push(OWNED[i])
        bad.set(m[1], list)
      }
    }
    return bad
  }

  it('style.css 至少定义了 50 个全局令牌（防止正则失效导致空集恒真）', () => {
    expect(globalTokens.size).toBeGreaterThan(50)
  })

  it('被扫文件里确实用到了 var()（防止扫描失效导致断言恒真）', () => {
    expect(OWNED_TEXT.some((t) => /var\(\s*--[a-zA-Z0-9-]+/.test(t))).toBe(true)
    expect(OWNED_TEXT.join('').match(/var\(\s*--[a-zA-Z0-9-]+/g)?.length ?? 0).toBeGreaterThan(20)
  })

  const bad = undefinedTokens()

  it('本专题文件没有引用未定义的令牌', () => {
    const detail = [...bad.entries()].map(([t, fs]) => `  ${t} ← ${[...new Set(fs)].join(', ')}`).join('\n')
    expect(
      bad.size,
      `以下 var() 引用了 style.css 与本文件都没定义的令牌（去掉字面兜底后会解析为空）：\n${detail}`,
    ).toBe(0)
  })

  it.each([...bad.keys()])('%s 未定义（逐条定位）', (token) => {
    const rel = bad.get(token)![0]
    const local = localTokensOf(OWNED.indexOf(rel))
    expect(globalTokens.has(token) || local.has(token), `${rel} 里的 var(${token}) 无处定义`).toBe(true)
  })

  it('这些文件不再出现「var(--x, #字面色)」形态（暗色漏白通道）', () => {
    // ★ 只拦**颜色**兜底，不拦所有兜底。
    // 上一版写成 `var(--x, 任意)`，结果把 `var(--rdv-table-min-width, 720px)`
    // 也报了 —— 宽度/间距的兜底是无害的渐进增强，拦它是误判。
    // 拦颜色的理由是：字面色在暗色主题下漏白，且会掩盖「令牌根本不存在」。
    const COLOR_FALLBACK = /var\(\s*--[a-zA-Z0-9-]+\s*,\s*(?:#[0-9a-fA-F]{3,8}|rgba?\(|hsla?\(|color-mix\()/g
    for (let i = 0; i < OWNED.length; i++) {
      const offenders = [...OWNED_TEXT[i].matchAll(COLOR_FALLBACK)].map((m) => m[0])
      expect(offenders, `${OWNED[i]} 里仍有带字面**颜色**兜底的 var()：${offenders.join(' ')}`).toEqual([])
    }
  })
})

/**
 * ## R1 门禁：新触控控件 ≥48 CSS px
 *
 * 依据：参考仓 17 号文档的跨文档裁决 R1（2026-10-04 吸收）——
 * **新增**触控控件一律 ≥48 CSS px；**44px 是存量控件的下限，不是新标准**。
 *
 * 存在理由：这条不设门就一定会漂。2026-10-04 吸收 R1 时实测到
 * `AppBottomNav` 席位 44px、`AppAccountSheet` 关闭钮 44px、
 * `HyperLoadMore` 容器与按钮 44px —— 而**同一专题**的 `CardList`
 * 触控目标早就全是 48px。同一批代码里两种尺寸，不是设计，是没人守。
 * 尤其 `AppAccountSheet` 自相矛盾：行与动作是 48，唯独关闭钮 44。
 *
 * 口径：**只看 `min-height`，只看 1–47**。
 * - 不看 `min-width`：`AppTopbar__item-badge` / `AppNavDrawer__badge` 这类徽标
 *   的 `min-width: 20px` 是**装饰尺寸不是触控区域**，纳入即误报。
 *   而本专题实际修掉的三个控件（`AppBottomNav` 席位 / `AppAccountSheet` 关闭钮 /
 *   `HyperLoadMore`）**全都同时有 min-height**，所以这一刀不漏。
 * - 只看 1–47：无单位声明（`min-height: 0`，滚动容器）天然不匹配。
 *
 * 名单为什么手写而不按目录自动发现：全扫 `components/shell` + `components/ui`
 * 会翻出 **6 处存量违规**（`AppTopbar` / `AppDrawer` / `AppModal` / `PageHeader` /
 *   `AppNavDrawer`），而 R1 原文写的是「**新增**控件 ≥48，44px 是**存量**下限」
 *   ⇒ 存量不该被这条门拦。与本文件另一道门 `OWNED` 同一哲学：范围要能一眼核对。
 * ⚠️ 但手写名单的代价是「有人悄悄把它改小」—— 所以下面用**名单钉住断言**堵这个口
 *   （变异 M4 证过：只靠「总数 ≥ N」的下界，删掉一个文件后门仍然 rc=0）。
 */
describe('R1：本专题新建的触控控件不得低于 48px', () => {
  /** 扫描名单。删除/新增任何一项都会被下面「名单钉住」那条断言拦下。 */
  const TOUCH_FILES = [
    'components/shell/AppBottomNav.vue',
    'components/shell/AppAccountSheet.vue',
    'components/ui/HyperLoadMore.vue',
    'components/ui/CardList.vue',
    'components/ui/ResponsiveDataView.vue',
  ]

  /**
   * 名单钉住：防止扫描范围被悄悄缩小（这是本门唯一的结构性风险）。
   * ⚠️ 第一版写成 `const PINNED = [...TOUCH_FILES]` —— 那是**声明时的快照**，
   *   与被测对象同源：从 `TOUCH_FILES` 删一个文件，PINNED 跟着一起变，
   *   断言恒真（变异 M4 实测 rc=0）。⇒ 必须是**独立字面量**。
   *   代价是新增文件要改两处 —— 这正是我们要的：改两处 = 有一次可见的评审。
   */
  const PINNED = [
    'components/shell/AppBottomNav.vue',
    'components/shell/AppAccountSheet.vue',
    'components/ui/HyperLoadMore.vue',
    'components/ui/CardList.vue',
    'components/ui/ResponsiveDataView.vue',
  ]

  /** 合法但非触控目标的声明（选择器 → 理由）。当前为空；新增必须写理由。 */
  const NOT_TOUCH = new Map<string, string>()

  const findings = (rel: string) => {
    const src = readFileSync(resolve(SRC, rel), 'utf8')
    const at = src.indexOf('<style')
    if (at === -1) return []
    const out: string[] = []
    for (const m of stripComments(src.slice(at)).matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const selector = m[1].trim().split('\n').pop()!.trim()
      if (NOT_TOUCH.has(selector)) continue
      for (const d of m[2].matchAll(/min-height\s*:\s*(\d+)px/g)) {
        const v = Number(d[1])
        if (v >= 1 && v < 48) out.push(`${rel} · ${selector} · min-height=${v}px`)
      }
    }
    return out
  }

  const bad = TOUCH_FILES.flatMap(findings)

  it('★ 名单钉住：扫描范围不得被悄悄缩小', () => {
    expect(TOUCH_FILES).toEqual(PINNED)
  })

  // 量具自证：规则解析不能空转，否则下面那条「一处都没有」是恒真的。
  it('量具自证：每个文件都解析出了 min-height 声明，且看得见 ≥48px 的那些', () => {
    // ⚠ 第一版这里用 `flatMap(...)` 直接当数字用 —— flatMap 返回**数组**不是和，
    //   `expect(数组).toBeGreaterThanOrEqual(8)` 抛的是 TypeError（脚手架错误），不是门红。
    const parsed = TOUCH_FILES.map((rel) => {
      const text = stripComments(readFileSync(resolve(SRC, rel), 'utf8'))
      const all = [...text.matchAll(/min-height\s*:\s*(\d+)px/g)]
      return { rel, total: all.length, bigEnough: all.filter((m) => Number(m[1]) >= 48).length }
    })
    expect(
      parsed.filter((p) => p.total === 0).map((p) => p.rel),
      '以下文件在扫描名单里，但一条 min-height 都没解析出来（路径写错了？还是量具的正则坏了？）',
    ).toEqual([])
    // 第二重自证：量具必须也看得到 R1 修完之后的 48px ——
    // 否则它的正则可能恰好只匹配违规项，这条门就成了「专门数违规」的筛子。
    expect(parsed.reduce((a, b) => a + b.bigEnough, 0), JSON.stringify(parsed))
      .toBeGreaterThanOrEqual(5)
  })

  it('没有低于 48px 的触控目标', () => {
    expect(bad, `R1：新触控控件必须 ≥48px（44px 是存量下限）\n  ${bad.join('\n  ')}`).toEqual([])
  })
})

/**
 * ## R3 门禁：capabilities v2 的字段集不得被删
 *
 * 依据：参考仓 17 号文档的 R3 合并 schema（2026-10-04 吸收）。
 * 吸收之前本专题的 `HyperCapabilities` 只有 9 个字段，R3 要求 15 个 ——
 * 缺的 6 个（`navigation` / `focusWorkspace` / `tasks` / `recording` /
 * `recognition` / `agent`）对应的能力本专题**确实没实现**，
 * 但 R3 的意图是「**报出不可用**，而不是让消费方靠字段缺失去猜」。
 * 缺字段时消费方只能写 `'recording' in c`，一旦有人后来加了半截实现，
 * 就会读到既非 true 也非 undefined 的中间态。
 *
 * 关键：本门**不**断言这些能力为真，只断言**字段在**、且
 * `WEB_CAPABILITIES` 里给的是「不可用」值 —— 与参考文档
 * 「全部不实现也不占位假装」一致。
 */
describe('R3：HyperCapabilities 的字段集不得被删', () => {
  /** R3 合并 schema 的 15 个字段。必填 13 + 可选 2。 */
  const R3_FIELDS = [
    'protocolVersion', 'platform', 'navigation', 'back', 'focusWorkspace',
    'insets', 'keyboard', 'haptics', 'appLifecycle', 'tasks', 'recording',
    'recognition', 'agent', 'shellVersion', 'trustedOrigin',
  ] as const

  /** R3 里的必填部分（`shellVersion` / `trustedOrigin` 是可选的补充）。 */
  const REQUIRED = R3_FIELDS.filter((f) => f !== 'shellVersion' && f !== 'trustedOrigin')

  const typesSrc = readFileSync(resolve(SRC, 'lib/shell/hyper/types.ts'), 'utf8')
  const capsSrc = readFileSync(resolve(SRC, 'lib/shell/hyper/capabilities.ts'), 'utf8')

  function interfaceBody(src: string, name: string): string {
    const at = src.indexOf(`export interface ${name} {`)
    if (at === -1) throw new Error(`types.ts 里找不到 interface ${name}`)
    let depth = 0
    for (let i = src.indexOf('{', at); i < src.length; i++) {
      if (src[i] === '{') depth++
      else if (src[i] === '}' && --depth === 0) return src.slice(at, i + 1)
    }
    throw new Error(`interface ${name} 花括号没配平`)
  }

  // 接口体字段缩进 2 空格，嵌套字段 4 空格 ⇒ 只取顶层。
  // 靠缩进而不是靠括号计数，是因为嵌套结构（tasks/recognition/agent）里
  // 也可能出现同行闭合的写法，按缩进更贴合本文件的排版约定。
  const declared = new Set(
    [...interfaceBody(typesSrc, 'HyperCapabilities').matchAll(/^ {2}(\w+)\??:/gm)].map((m) => m[1]),
  )

  function webCapsKeys(): string[] {
    const at = capsSrc.indexOf('export const WEB_CAPABILITIES')
    if (at === -1) throw new Error('capabilities.ts 里找不到 WEB_CAPABILITIES')
    const from = capsSrc.indexOf('{', at)
    return [...capsSrc.slice(from).matchAll(/^ {2}(\w+):/gm)].map((m) => m[1])
  }
  const inWebCaps = new Set(webCapsKeys())

  // 量具自证：两处抽取都不能空转，否则下面两条「都没缺」是恒真的。
  it('量具自证：确实从源码里抽出了字段名', () => {
    expect(declared.size, `types.ts 抽出 ${declared.size} 个字段：${[...declared].join(',')}`)
      .toBeGreaterThanOrEqual(13)
    expect(inWebCaps.size, `capabilities.ts 抽出 ${inWebCaps.size} 个键：${[...inWebCaps].join(',')}`)
      .toBeGreaterThanOrEqual(13)
  })

  it('interface 声明了 R3 的全部必填字段', () => {
    expect(REQUIRED.filter((f) => !declared.has(f)), 'R3 必填字段未在 HyperCapabilities 里声明').toEqual([])
  })

  it('WEB_CAPABILITIES 为每个必填字段都给了值（不允许靠字段缺失表达不可用）', () => {
    expect(REQUIRED.filter((f) => !inWebCaps.has(f)), 'WEB_CAPABILITIES 缺键').toEqual([])
  })

  it('未实现的能力一律报「不可用」而不是「可用」（R3「不占位假装」）', () => {
    // 这五条能力本专题**没有代码**，所以基线里必须全为不可用。
    // 若将来真实现了，这个断言会红 —— 那正是要人更新本门的时候。
    expect(WEB_CAPABILITIES.focusWorkspace).toBe(false)
    expect(WEB_CAPABILITIES.tasks).toEqual({
      durableLocal: false, cloudDetached: false, continuation: 'foregroundOnly',
    })
    expect(WEB_CAPABILITIES.recording).toEqual({ available: false, background: false })
    expect(WEB_CAPABILITIES.recognition).toEqual({ pdfText: false, ocr: 'none', asr: 'none' })
    expect(WEB_CAPABILITIES.agent).toEqual({ available: false, skillFormat: '' })
  })
})

/**
 * ## 弹层层级门禁
 *
 * 存在理由：`AppBottomNav.vue` 的注释曾写「z-index 低于抽屉（40）」，
 * 而 `AppDrawer.vue` 实际是 **100**。结论（底栏不能压过抽屉）是对的，
 * 写下来的数字是错的 —— 而且**没有任何门禁会发现它**，因为注释不参与计算。
 *
 * 这正是 D6（死选择器）的同族：文档/注释与实现漂移。
 * 层级顺序是可计算的事实，就该由门禁算，而不是靠注释提醒。
 */
describe('弹层层级：底栏 < 账户 Sheet < 抽屉', () => {
  function zIndexOf(rel: string): number {
    const css = readFileSync(resolve(SRC, rel), 'utf8')
    const m = css.match(/z-index:\s*(\d+)/)
    expect(m, `${rel} 里找不到 z-index 声明 —— 本门禁失效`).not.toBeNull()
    return Number(m![1])
  }

  const bottomNav = zIndexOf('components/shell/AppBottomNav.vue')
  const accountSheet = zIndexOf('components/shell/AppAccountSheet.vue')
  const drawer = zIndexOf('components/ui/AppDrawer.vue')

  it('三个层级都解析出了数值（防止正则失效导致恒真）', () => {
    for (const v of [bottomNav, accountSheet, drawer]) expect(Number.isInteger(v)).toBe(true)
  })

  it('底栏低于账户 Sheet，账户 Sheet 低于抽屉', () => {
    expect(bottomNav, `底栏 ${bottomNav} 必须 < 账户 Sheet ${accountSheet}`).toBeLessThan(accountSheet)
    expect(accountSheet, `账户 Sheet ${accountSheet} 必须 < 抽屉 ${drawer}`).toBeLessThan(drawer)
  })

  it('「更多」打开的抽屉能压在底栏之上（否则点不到抽屉里的菜单）', () => {
    expect(bottomNav, `底栏 ${bottomNav} 不得 ≥ 抽屉 ${drawer}`).toBeLessThan(drawer)
  })

  it('注释里写下的层级数字与实际一致（防注释漂移）', () => {
    const comment = readFileSync(resolve(SRC, 'components/shell/AppBottomNav.vue'), 'utf8')
    const line = comment.split('\n').find((l) => l.includes('底栏 30'))
    expect(line, '底栏注释未写明「底栏 30 < 账户 Sheet 50 < 抽屉 100」').toBeDefined()
    expect(line).toContain(`账户 Sheet ${accountSheet}`)
    expect(line).toContain(`抽屉 ${drawer}`)
  })
})

/**
 * ## 规范文档编码门禁
 *
 * 存在理由：写 02–16 那批正文时，**同一批里踩了 3 次** U+FFFD
 * （「未在**本**仓复核」写成了「未在���仓复核」等）。每次都要靠写完再扫一遍才发现。
 * 症状很隐蔽：文件能渲染、Markdown 正常、门禁全绿，但那一行**永久不可解码** ——
 * 归档后每个读它的人都得自己猜哪个字是原文、哪个字是损坏。
 *
 * 判据按**文件**逐个报，不按行数报（一次只暴露一处时容易以为修完了）。
 */
describe('docs/UI规范：无编码截断', () => {
  const DOCS = resolve(process.cwd(), '../docs/UI规范')

  const files = readdirSync(DOCS).filter((f) => f.endsWith('.md')).sort()
  const broken = files
    .map((f) => ({ f, text: readFileSync(join(DOCS, f), 'utf8') }))
    .map(({ f, text }) => ({ f, text, bad: (text.match(/�/g) ?? []).length }))
    .filter((x) => x.bad > 0)

  it('规范目录存在且有文档（防止目录改名后判据恒真）', () => {
    expect(files.length).toBeGreaterThanOrEqual(10)
  })

  it('没有文件包含 U+FFFD 替换字符', () => {
    const detail = broken.map((x) => `  ${x.f}: ${x.bad} 处`).join('\n')
    expect(broken.length, `以下文档含编码截断（U+FFFD），该行永久不可解码：\n${detail}`).toBe(0)
  })

  it.each(broken.map((x) => x.f))('%s 含编码截断（逐文件定位）', (f) => {
    const text = readFileSync(join(DOCS, f), 'utf8')
    const lineNo = text.split('\n').findIndex((l) => l.includes('�')) + 1
    expect(text.includes('�'), `${f}:${lineNo} 仍含 U+FFFD`).toBe(false)
  })

  it('每篇规范正文（01–16）都必须有状态标记，避免"写了就当做了"', () => {
    for (const f of files) {
      // 00 是方案文档，用 §9.3「已知未解决」表表达状态，机制不同故豁免
      if (f === 'README.md' || f.startsWith('00-')) continue
      const full = readFileSync(join(DOCS, f), 'utf8')
      // ★ 必须限定在**头部区块**：全文匹配会被正文表格里的零散 [未落地] 顶替，
      //   删掉头部标记后门照样绿 —— 判据检查点位置错了，等于没检。
      const header = full.split('\n').slice(0, 12).join('\n')
      expect(
        /\*\*\[(已落地|部分落地|未落地)/.test(header),
        `${f} 头部（前 12 行）缺少状态标记 —— 规范必须区分「已实现」与「只有契约」`,
      ).toBe(true)
    }
  })

  it('方案文档 00 用「已知未解决」表表达未完成项（等价机制）', () => {
    const text = readFileSync(join(DOCS, '00-需求与优化方案.md'), 'utf8')
    expect(text, '00 缺少 §9.3 已知未解决表').toContain('已知未解决')
    expect(text, '00 缺少 §9.4 尚未开工').toContain('尚未开工')
  })
})
