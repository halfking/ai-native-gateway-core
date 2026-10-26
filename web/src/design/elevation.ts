// elevation.ts — 「浮起词汇归一」的可执行判据（2026-10-27 · 第 18 增量）
//
// ★ 要治的病（不是推演，是仓内明文规范 vs 真仓现状的偏离）：
//   `DESIGN.md` §5「阴影词汇」表写死了四级别，并且 §2 有一条硬规矩：
//     「阴影 → 只用 `--kx-shadow-sm/md`」
//   craft-floor codex 段原话：
//     "Declare elevation once, border or shadow."
//   而真仓实测：**121 处 box-shadow / 71 种取值**，其中抬升层 **69 处 / 34 种手写配方**，
//   真正用上规范 token 的只有 17 处。
//   ⇒ 34 种手写阴影配方 = 每个组件各编一个数值 = AI 生成代码最典型的痕迹。
//
// ★★★ 本门只判**一条**判据（判据越少越可证伪）：
//
//   **任何非 inset 的阴影层，只要它不是 `var(--kx-shadow-xs|sm|md|md-left)`、
//     且它的偏移不是零 ⇒ 违规。**
//
//   为什么把「零偏移」排除在外：
//   · `0 0 0 1px <color>` 是「用阴影画的圆环」⇒ 那是**描边替身**，归 V9/描边层管
//   · `0 0 5px <color>` 是**零偏移彩色光晕** ⇒ craft-floor 明令是装饰，已被 V9 判
//   ⇒ 本门只管「真的投在表面上的那一层」。
//
// ★ 与第 17 增量门（`aiSmells.ts`）的分工（口径**不重叠**，可证伪）：
//   V1 幽灵卡片 = 同一表面既有描边又有抬升（**管有没有**）
//   V7 硬偏移   = 零模糊 + 有偏移（**管几何**）
//   V9 零偏移光晕 = 零偏移 + 有模糊 + 彩色（**管零偏移**）
//   E1 配方归一 = 抬升层必须来自规范 token（**管配方**）
//   ⇒ 一处违规可能同时命中两门（例如「描边 + 省略单位的手写投影」）。
//
// ★ 本文件是**判据**（纯函数），门在 `web/scripts/design-elevation-guard.mjs`。
//   放 `src/` 是为了能被 vitest 覆盖（与 `aiSmells.ts` 同一范式）。

import { readFileSync } from 'node:fs'
import { relative } from 'node:path'
// ★ 用 `.ts` 后缀 import：项目的 tsconfig 开了 allowImportingTsExtensions，
//   而 Node 的 --experimental-strip-types 对相对 import **要求**显式扩展名
//   ⇒ 两边（vite / vue-tsc / node strip-types）都能解析同一份实现。
import {
  ELEVATION_TOKENS,
  boxShadowDeclarations,
  extractCssBlocks,
  isElevationToken,
  listScannableFiles,
  shadowLayers,
  stripCssComments,
} from './aiSmells.ts'

// ★ 规范名单只有一份 ⇒ 从 `aiSmells.ts` re-export（那里是底层实现，
//   因为「`--kx-shadow-*` 一律是抬升」这条语义同时服务于幽灵卡片门）。
export { ELEVATION_TOKENS, isElevationToken }

// ───────────────────────── 规范词汇 ─────────────────────────

/**
 * 允许的抬升配方（唯一的规范来源）。
 * ★ 与 `style.css` 里的 token 定义一一对应；改这里必须同步改那里。
 * ★ 名单本体在 `aiSmells.ts`（同一口径 ⇒ 只有一份）。
 */

export interface ElevationFinding {
  rule: string
  file: string
  line: number
  selector: string
  text: string
  reason: string
}

export interface ElevationFacts {
  /** 非规范的抬升层数（= 违规数） */
  nonTokenElevation: number
  /** 全部非 inset 阴影层里「不同取值」的个数（读数用：收敛到 4 = 收敛到规范词汇） */
  distinctElevationValues: number
  /** 抬升层总数（含合规） */
  elevationLayers: number
}

/**
 * 找出所有「手写抬升配方」的层。
 *
 * ★ 形态覆盖（按【形状】判，覆盖同一形状的所有实现写法）：
 *   ① 单层手写：`box-shadow: 0 8px 24px var(--overlay-light)`
 *   ② 多层里的投影层：`box-shadow: 0 0 0 1px <color>, 0 8px 24px <color>`
 *   ③ 换了名字的 token：`box-shadow: var(--my-shadow)` ⇒ 偏移取不到 ⇒ 仍判违规
 *   ④ 省略长度单位：`box-shadow: 0 8px 24px ...` / `-8px 0 32px ...`
 */
export function findElevationViolations(src: string, rel: string): ElevationFinding[] {
  const out: ElevationFinding[] = []
  const blocks = extractCssBlocks(src, rel)
  for (const b of blocks) {
    const body = stripCssComments(b.body)
    // ★ 与 V7 共用同一个遍历器（`boxShadowDeclarations`）⇒ 「块内所有 box-shadow 声明」只有一份口径
    for (const decl of boxShadowDeclarations(b.body)) {
      // ★ 行号要指向**声明所在行**（第 17 增量的教训：行号要能直接跳过去）
      const line = b.startLine + body.slice(0, decl.offset).split('\n').length - 1
      for (const layer of shadowLayers(decl.value)) {
        if (isElevationToken(layer.raw)) continue
        // 零偏移 ⇒ 描边替身 / 彩色光晕 ⇒ 不归本门管
        if (layer.offsetX !== null && layer.offsetY !== null && layer.offsetX === 0 && layer.offsetY === 0) {
          continue
        }
        out.push({
          rule: 'E1-non-token-elevation',
          file: rel,
          line,
          selector: b.selector,
          text: layer.raw.slice(0, 90),
          reason:
            '手写抬升配方 —— 抬升层级只能来自 ' +
            ELEVATION_TOKENS.join(' / ') +
            '（DESIGN.md §5：阴影 → 只用 --kx-shadow-*）',
        })
      }
    }
  }
  return out
}

/**
 * 单个文件的统计（**不含**「不同取值数」—— 那个数必须跨文件去重，
 * 只有全仓口径才是真的 ⇒ 不在这里提供第二份口径）。
 */
export function collectElevationFacts(src: string, rel: string): Omit<ElevationFacts, 'distinctElevationValues'> {
  const violations = findElevationViolations(src, rel)
  return {
    nonTokenElevation: violations.length,
    elevationLayers: elevationLayersOf(src, rel).length,
  }
}

// ───────────────────────── 自证 ─────────────────────────

/**
 * 自证：每条判据至少 1 个正例 + 1 个反例；
 * ★ 反例必须包含**真仓里出现过的合法形态**（否则门会带着假阳性出厂）。
 */
export function runElevationSelftest(): { passed: number; total: number; failed: string[] } {
  const cases: Array<{ id: string; desc: string; expect: number; actual: number }> = []
  const check = (id: string, desc: string, expect: number, actual: number) => {
    cases.push({ id, desc, expect, actual })
  }
  // ★ 自证与真扫描走**同一个实现**（findElevationViolations）—— 判据只有一份。
  const scan = (src: string) => findElevationViolations(src, 'x.vue')

  // E1：干净样本 ⇒ 0 命中（四支规范 token 都要能用）
  check('E1a', '四支规范 token ⇒ 0 命中', 0, scan(`
<style scoped>
.a { box-shadow: var(--kx-shadow-xs); }
.b { box-shadow: var(--kx-shadow-sm); }
.c { box-shadow: var(--kx-shadow-md); }
.d { box-shadow: var(--kx-shadow-md-left); }
</style>`).length)

  // E1：手写配方 ⇒ 命中（下投影 / 侧向投影 / 省略单位）
  check('E1b', '手写下投影 ⇒ E1', 1, scan(`
<style scoped>
.pop { box-shadow: 0 8px 24px var(--overlay-light); }
</style>`).length)
  check('E1c', '手写侧向投影 ⇒ E1', 1, scan(`
<style scoped>
.drawer { box-shadow: -8px 0 24px var(--overlay-medium); }
</style>`).length)
  check('E1d', '省略长度单位 ⇒ E1（CSS 允许 `0` 无单位）', 1, scan(`
<style scoped>
.pop { box-shadow: 0 4px 12px var(--overlay-light); }
</style>`).length)

  // ★ 反例：零偏移（描边替身 / 彩色光晕）⇒ **不归本门**（归 V9）
  check('E1e', '零偏移圆环 0 0 0 1px ⇒ 不判 E1（是描边替身）', 0, scan(`
<style scoped>
.marker { box-shadow: 0 0 0 1px var(--kx-border); }
</style>`).length)
  check('E1f', '零偏移彩色光晕 0 0 5px ⇒ 不判 E1（归 V9）', 0, scan(`
<style scoped>
.badge { box-shadow: 0 0 5px color-mix(in srgb, var(--probe-cyan) 30%, transparent); }
</style>`).length)

  // ★ 反例：inset 与 none
  check('E1g', '纯内阴影 ⇒ 不判 E1', 0, scan(`
<style scoped>
.card { box-shadow: inset 0 1px 0 rgba(0,0,0,.04); }
</style>`).length)
  check('E1h', 'box-shadow: none ⇒ 不判 E1', 0, scan(`
<style scoped>
.card { box-shadow: none; }
</style>`).length)

  // ★ 多层：只判**投影层**，圆环层不动
  check('E1i', '多层（圆环 + 手写投影）⇒ 只报投影层 1 处', 1, scan(`
<style scoped>
.tile { box-shadow: 0 0 0 1px color-mix(in srgb, var(--kx-text) 35%, transparent), 0 4px 10px color-mix(in srgb, var(--kx-text) 18%, transparent); }
</style>`).length)
  check('E1j', '多层（圆环 + 规范 token）⇒ 0 命中', 0, scan(`
<style scoped>
.tile { box-shadow: 0 0 0 1px color-mix(in srgb, var(--kx-text) 35%, transparent), var(--kx-shadow-sm); }
</style>`).length)

  // ★ 换名字的 token ⇒ 偏移取不到 ⇒ 仍判违规（不许用「自定义 token」绕过）
  //   ★ 自证样例必须**自洽**：样例里引用的令牌要在这里给出定义，
  //     否则仓库的「令牌解析」门会把它当成真声明里的无定义引用（真仓实测：--my-shadow 曾触发该门）。
  check('E1k', '自定义 token 引用 ⇒ 判 E1（配方不可知）', 1, scan(`
<style scoped>
.pop { --my-shadow: 0 8px 24px rgba(0, 0, 0, 0.2); box-shadow: var(--my-shadow); }
</style>`).length)

  // ★ 注释里的声明不算（真仓 style.css 有大量注释）
  check('E1l', '注释里的手写阴影 ⇒ 不判 E1', 0, scan(`
<style scoped>
/* 以前是 box-shadow: 0 8px 24px var(--overlay-light); */
.pop { background: var(--card); }
</style>`).length)

  // ★ token **定义处**不判（style.css 里 `--kx-shadow-sm: 0 8px 24px rgba(...)`）
  check('E1m', 'token 定义处 ⇒ 不判 E1', 0, scan(`
:root { --kx-shadow-sm: 0 8px 24px rgba(30, 55, 100, 0.06); }
.pop { box-shadow: var(--kx-shadow-sm); }
</style>`).length)

  // ★ 行号要指向**声明行**（不是块尾）
  const lineCase = scan(`<style scoped>
.a { color: red; }
.b { box-shadow: 0 8px 24px var(--overlay-light); }
</style>`)
  check('E1n', '行号指向声明所在行（第 3 行）', 3, lineCase.length ? lineCase[0].line : -1)

  // ★ 嵌套规则块里的声明也要命中（块作用域）
  check('E1o', '嵌套规则块 ⇒ 命中 1 处', 1, scan(`
<style scoped>
@media (min-width: 768px) {
  .pop { box-shadow: 0 8px 24px var(--overlay-light); }
}
</style>`).length)

  let passed = 0
  const failed: string[] = []
  for (const c of cases) {
    if (c.expect === c.actual) passed++
    else failed.push(`${c.id} ${c.desc}：期望 ${c.expect}，实际 ${c.actual}`)
  }
  return { passed, total: cases.length, failed }
}

// ───────────────────────── 全仓扫描 ─────────────────────────

export interface ElevationScanResult {
  findings: ElevationFinding[]
  facts: ElevationFacts
  files: number
}

export function scanElevation(rootDir: string, repoDir: string): ElevationScanResult {
  const findings: ElevationFinding[] = []
  let nonToken = 0
  let distinct = 0
  let layers = 0
  let files = 0
  // ★ 不同取值数必须**跨文件去重** ⇒ 全仓共用一个 Set
  const valueSet = new Set<string>()
  for (const abs of listScannableFiles(rootDir)) {
    files++
    const src = readFileSync(abs, 'utf8')
    const rel = relative(repoDir, abs)
    const f = findElevationViolations(src, rel)
    findings.push(...f)
    nonToken += f.length
    for (const v of elevationLayersOf(src, rel)) {
      valueSet.add(v)
      layers++
    }
  }
  return {
    findings,
    facts: {
      nonTokenElevation: nonToken,
      distinctElevationValues: valueSet.size,
      elevationLayers: layers,
    },
    files,
  }
}

/**
 * 单个文件里出现过的全部**抬升层**取值。
 *
 * ★ 口径必须与 E1 的判定对象**完全一致**（否则读数含义会漂）：
 *   抬升层 = 非 inset、非 none、且偏移不是零的层
 *   ⇒ 零偏移圆环 / 彩色光晕**不计入**（它们归 V9 与描边层，不是浮起语言）
 */
export function elevationLayersOf(src: string, rel: string): string[] {
  const out: string[] = []
  const blocks = extractCssBlocks(src, rel)
  for (const b of blocks) {
    for (const decl of boxShadowDeclarations(b.body)) {
      for (const layer of shadowLayers(decl.value)) {
        if (layer.offsetX !== null && layer.offsetY !== null && layer.offsetX === 0 && layer.offsetY === 0) {
          continue
        }
        out.push(layer.raw)
      }
    }
  }
  return out
}
