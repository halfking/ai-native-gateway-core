import { readFileSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'

/**
 * i18n 键扫描器 —— `dynamicKeys.spec.ts` 与 `orphanKeys.spec.ts` **共用**这一份。
 *
 * ★★ 为什么要抽成模块而不是两个判据各写一份：
 *   两份正则一定会漂（改了一处忘了另一处），
 *   而漂掉的后果是**静默的假孤儿** —— 某一族键成批地变成「没人用」，
 *   读数看起来仍然「有门」，实际覆盖面已经缺了一块。
 *   共用一份 ⇒ 一处修改两处同时生效。
 */

/**
 * 源码里被引用的**字面量**键：`titleKey: 'nav.home'` 这种带引号的点号路径。
 *
 * ⚠️⚠️ **本文件自己的注释里绝不能写一个不存在的点号路径当例子** ——
 *   `verify-i18n-parity.mjs` 扫描**整份源码含注释**，注释里写一句
 *   「比如 t(某个假键)」，那道门就报「视图引用了词典里不存在的键」而假红。
 *   （本批实测栽在这里。）
 *
 * ★ 危险方向不止假红：注释里提一句**真实存在**的键 ⇒ 孤儿判据认为它「有人用」
 *   ⇒ **真孤儿被藏起来**。所以孤儿判据侧已经先 strip 注释，见 stripComments。
 */
export const LITERAL_RE = /['"]([A-Za-z_$][\w$-]*(?:\.[A-Za-z_$][\w$-]*)*)['"]/g

/**
 * 拼接前缀的**三种形态**，少一种就会有一族键成批变成假孤儿：
 *
 *  ① `t('maas.dim_' + d)` —— 紧邻 `t(` 的加号拼接
 *  ② `` t(`dh.state.${state}`) `` —— 模板字面量
 *  ③ `'cache.state.' + v` —— **藏在 helper 里**，不紧邻 `t(`
 *     （`api/probeTimelineCache.ts` 的 `cacheStateKeyOf` 就是这一族；
 *       只扫 ①② 会把它整族误判成孤儿）
 *
 * 另有一条 `funnel.window` **不以 `.`/`_` 结尾**（`t('funnel.window' + '24h')`），
 * 所以形态 ① 的尾部分隔符是**可选**的。
 */
export const PREFIX_RES: readonly RegExp[] = [
  /\bt\(\s*'([A-Za-z_$][\w$-]*(?:\.[A-Za-z_$][\w$-]*)*[._]?)'\s*\+/g,
  /\bt\(\s*`([A-Za-z_$][\w$-]*(?:\.[A-Za-z_$][\w$-]*)*[._])\$\{/g,
  /'([A-Za-z_$][\w$-]*(?:\.[A-Za-z_$][\w$-]*)*[._])'\s*\+/g,
]

export const PREFIX_FORM_NAMES: readonly string[] = ['①t(引号+)', '②t(`${)', '③helper里的+']

/** 扫源码用到的文件：整个 `src/`，排除测试文件与判据自身。 */
export function sourceFiles(): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    let entries: import('node:fs').Dirent[] = []
    try {
      entries = readdirSync(dir, { withFileTypes: true })
    } catch {
      return
    }
    for (const e of entries) {
      const full = join(dir, e.name)
      if (e.isDirectory()) {
        if (e.name === 'node_modules' || e.name === 'dist') continue
        walk(full)
      } else if (/\.(vue|ts)$/.test(e.name) && !/\.(spec|test)\.ts$/.test(e.name)) {
        out.push(full)
      }
    }
  }
  walk(join(resolve(process.cwd()), 'src'))
  return out
}
/**

 * ★★ 扫描前**必须 strip 注释**。
 *
 *   注释里写一句带引号的点号路径当例子，parity 门就会把它当成**真引用** ——
 *   本批实测：`keyScan.ts` 自己的文件头写了个示例键当文档，
 *   `i18n:check` 当场假红（报「视图引用了词典里不存在的键」）。
 *
 *   危险方向不是假红，是**反向的假覆盖**：注释里提一句真实存在的键，
 *   孤儿判据就会认为它「有人用」⇒ **真孤儿被藏起来**。
 *   `verify-i18n-parity.mjs` 也有同一个洞（本批未改它，见文档 §11.177 的遗留项）。
 */
export function stripComments(src: string): string {
  // ★★ **顺序不能反**：先剥行注释，再剥块注释。
  //
  //   本仓的真实反例（`src/config/appNav.ts:231`）：
  //     `// MaaS 租户运维面。**superAdmin 档**（/api/admin/maas/tenants/** 全 superAdmin）`
  //   这一行里含有 `/**`（来自 `/tenants/**`）。若先剥块注释，
  //   正则会从那个 `/**` 一路吃到下一个 `*/` —— **中间几百行真实代码被整段吃掉**，
  //   `titleKey: 'nav.annotations'` 等键随之消失，孤儿判据会把它们全报成孤儿。
  //   （实测：appNav.ts 被吃掉 18319 字符、275 行变 242 行。）
  //
  //   ⇒ 手写的文本级工具在真实数据上会误伤，**而且误伤方向是静默的漏检**。
  //     `stripProofKeys()` 就是为这件事设的负控。
  return src
    // 行注释（保护字符串里的 `//`，如 `http://`）
    .replace(/(^|[^:'"`])\/\/[^\n]*/g, '$1 ')
    // 块注释
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
}

/**
 * ★★ 负控：这些键在**真实代码**里（不是注释里），剥注释后必须仍然抽得到。
 *   若 stripComments 误伤，`NAV_PROOF_KEYS` 里至少一条会消失 ⇒ 立刻红。
 */
export const NAV_PROOF_KEYS: readonly string[] = [
  'nav.annotations', // src/config/appNav.ts:275 titleKey
  'nav.dispatchJournal', // 同上
  'nav.maasTenantOps', // 同上（夹在两行注释之间的那一段）
]

/**
 * 全部源码拼成一个大字符串，**排除 `src/i18n/` 本身** ——
 * 词典当然引用自己每一个键，把它算进「使用者」会让孤儿恒为 0（一个恒真的门）。
 */
export function sourceBlob(): { files: string[]; blob: string } {
  const i18nDir = join(resolve(process.cwd()), 'src', 'i18n')
  const files = sourceFiles().filter((f) => !f.startsWith(i18nDir))
  return {
    files: sourceFiles(),
    blob: files.map((f) => stripComments(readFileSync(f, 'utf8'))).join('\n'),
  }
}

export function literalKeys(blob: string): Set<string> {
  const out = new Set<string>()
  for (const m of blob.matchAll(LITERAL_RE)) {
    // 同 concatPrefixes：非法的直接丢，不塞 `undefined` 进集合。
    const got = m[1]
    if (typeof got === 'string' && got.length > 0) out.add(got)
  }
  return out
}

/** 每种形态各抓到了几个（覆盖面自证的读数）。 */
export function prefixHits(blob: string): number[] {
  return PREFIX_RES.map((re) => [...blob.matchAll(re)].length)
}

export function concatPrefixes(blob: string): Set<string> {
  const out = new Set<string>()
  for (const re of PREFIX_RES) {
    for (const m of blob.matchAll(re)) {
      // ★ 每条正则都有**一个**捕获组；`noUncheckedIndexedAccess` 下 m[1] 是 `string | undefined`。
      //   写成 `m[1]!` 是断言，但更稳的是**当场丢掉非法的** ——
      //   宁可少收一个前缀（会被 ①-3 / ⑥-2 的读数看见），也不要收进 `undefined`。
      const got = m[1]
      if (typeof got === 'string' && got.length > 0) out.add(got)
    }
  }
  return out
}

/** 只有「确实是词典键前缀」的才算 i18n 动态前缀，否则与本门无关。 */
export function isRealPrefix(p: string, keys: readonly string[]): boolean {
  return keys.some((k) => k.startsWith(p))
}

/** 把嵌套词典摊平成点号键路径。★ 直接遍历真实对象，不解析源码。 */
export function flattenDict(node: unknown, prefix = ''): string[] {
  if (typeof node === 'string') return prefix ? [prefix] : []
  if (!node || typeof node !== 'object' || Array.isArray(node)) return []
  return Object.entries(node as Record<string, unknown>).flatMap(([k, v]) =>
    flattenDict(v, prefix ? `${prefix}.${k}` : k),
  )
}
