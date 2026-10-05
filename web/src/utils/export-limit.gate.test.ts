// export-limit.gate.test.ts — C3/C5 源码门禁（docs/UI规范 10 §4.6.18、19 §4.3b）。
//
// 治两个病：
//   C3 生产代码不得再出现**裸** `await xxx.blob()`（无上限的整体读）。
//       本仓 3/4 的导出端点是流式 CSV、无 Content-Length，裸 blob() 即 OOM 面
//       （§4.6.18 实测：Go 在 >2048 字节后转 chunked，拿不到长度）。
//   C5 文件体积上限只允许有**一个**权威源 DEFAULT_MAX_FILE_BYTES。
//
// ★ 本门首版踩了三个坑，都记在这里防重犯：
//
//  1. **门的位置决定了 SRC 基准**。首版把门放在 `src/`，沿用 D-5 门的
//     `resolve(__dirname, '..')`，得到的是 `web/` 而不是 `web/src/`
//     ⇒ 报 `ENOENT .../web/locales/zh-CN/common.ts`。
//     **同一个表达式在 `src/` 与 `src/utils/` 两个层级给出不同基准**，
//     所以「照抄邻近门的写法」隐含了「必须在同一层级」。
//     现与 D-5 门同处 `src/utils/`，`SRC` 一致 = `web/src`。
//
//  2. **C5 扫全仓数字是错的**。首版用 `/\d+\s*(MB|MiB)|\b\d{7,}\b/` 全仓扫，
//     一次报出 **34 条**，其中 33 条是无关量：`200 MB/s`（磁盘吞吐）、
//     `40MB 轻量镜像`、`86400000`（毫秒/天）、`1000000`（token 上限）。
//     ⇒ **判据过宽与过窄一样有害**：真违规被噪声淹没后就再没人看它的输出。
//     现只扫真正表达「文件体积上限」的三处语义：导出去向 / 8 locale 文案。
//
//  3. **豁免清单用字面量**。若从别处推导名单，名单本身出错时门跟着错且无告警。
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'fs'
import { dirname, join, resolve } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SRC = resolve(__dirname, '..') // = web/src（与 exportFile.gate.test.ts 同层同写法）

/** 允许持有裸 `.blob()` 的文件（豁免清单——新增需在 PR 里说明理由）。全为测试桩。 */
const RAW_BLOB_ALLOWLIST = new Set([
  'utils/exportResponse.spec.ts',
  'utils/exportFile.gate.test.ts',
  'utils/export-limit.gate.test.ts',
])

/** 允许出现体积上限字面量的文件：exportFile.ts 的 DEFAULT_MAX_FILE_BYTES 是权威源。 */
const MAX_LITERAL_ALLOWLIST = new Set(['utils/exportFile.ts'])

const LOCALES = ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']

function* walk(dir: string): Generator<string> {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (name === 'node_modules' || name === 'dist' || name === '.git') continue
    if (statSync(full).isDirectory()) yield* walk(full)
    else if (/\.(ts|vue)$/.test(name)) yield full
  }
}

const isTest = (rel: string) => /\.(spec|test)\.ts$/.test(rel) || rel.includes('__tests__')

/** 剥掉注释，避免「注释里提到 blob() / 50MB」被判红。 */
function stripComments(text: string): string {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map(l => l.replace(/\/\/.*$/, ''))
    .join('\n')
}

/**
 * 列出**全仓**所有「文件体积上限」定义点（相对 SRC 的路径）。
 *
 * ⚠️ 首版只扫 `utils/exportFile.ts` 一个文件，于是变异把第二处定义放进
 * `utils/exportResponse.ts` 时，断言读到的仍是 1 ⇒ **判据范围小于被保护对象**，
 * 变异台账 G7 仍绿。这与「集合语义的门抓不到名单自身重复」同族：
 * **门要断言「全仓唯一」，就必须真的扫全仓。**
 */
function allMaxByteDefinitions(): string[] {
  const out: string[] = []
  for (const file of walk(SRC)) {
    const rel = file.slice(SRC.length + 1)
    if (isTest(rel)) continue
    const text = stripComments(readFileSync(file, 'utf8'))
    if (/export\s+const\s+DEFAULT_MAX_FILE_BYTES\b/.test(text)) out.push(rel)
  }
  return out.sort()
}

describe('C3 源码门禁：生产代码不得整体读入导出体', () => {
  it('裸 await xxx.blob() 只许出现在测试桩里', () => {
    const offenders: string[] = []
    for (const file of walk(SRC)) {
      const rel = file.slice(SRC.length + 1)
      if (RAW_BLOB_ALLOWLIST.has(rel)) continue
      if (isTest(rel)) continue
      stripComments(readFileSync(file, 'utf8')).split('\n').forEach((line, i) => {
        if (/\bawait\s+\w+\.blob\(\)/.test(line)) offenders.push(`${rel}:${i + 1}: ${line.trim()}`)
      })
    }
    expect(offenders).toEqual([])
  })

  it('量具自证：判据仍有对象（首版抓到过 4 处，现为 0 是收编的结果）', () => {
    // 门立起来时生产代码有 4 处裸 blob()（usage.ts ×2、reportrollup.ts、
    // PricingManagementView.vue），本轮全部收编为 readExportBlob(res)。
    // 这一条保证「上面的零」不是因为判据坏了：
    //   · 该形态仍被识别
    //   · 实现确实不含裸 blob()
    //   · 权威源**全仓**恰好一个定义点
    expect(/\bawait\s+\w+\.blob\(\)/.test('const b = await res.blob()')).toBe(true)
    const impl = stripComments(readFileSync(join(SRC, 'utils', 'exportResponse.ts'), 'utf8'))
    expect(/\bawait\s+\w+\.blob\(\)/.test(impl)).toBe(false)
    expect(allMaxByteDefinitions()).toEqual(['utils/exportFile.ts'])
  })
})

describe('C5 源码门禁：体积上限只有一个权威源', () => {
  it('导出去向必须引用 DEFAULT_MAX_FILE_BYTES，不得写死字面量', () => {
    const offenders: string[] = []
    const EXPORT_MODULES = ['utils/exportResponse.ts', 'api/reportrollup.ts', 'api/usage.ts']
    const LITERAL = /\b\d+\s*(?:MB|MiB)\b|\b\d{7,}\b|\b\d+\s*\*\s*1024\s*\*\s*1024\b/
    for (const rel of EXPORT_MODULES) {
      stripComments(readFileSync(join(SRC, rel), 'utf8')).split('\n').forEach((line, i) => {
        if (LITERAL.test(line)) offenders.push(`${rel}:${i + 1}: ${line.trim()}`)
      })
    }
    expect(offenders).toEqual([])
  })

  it('8 locale 的导出超限文案必须用 {max} 插值，不得写死数字', () => {
    const offenders: string[] = []
    for (const loc of LOCALES) {
      const text = stripComments(readFileSync(join(SRC, 'locales', loc, 'common.ts'), 'utf8'))
      const line = text.split('\n').find(l => l.includes('exportTooLarge'))
      if (!line) { offenders.push(`${loc}: 缺 exportTooLarge 键`); continue }
      if (!line.includes('{max}')) offenders.push(`${loc}: 缺 {max} 插值 → ${line.trim()}`)
      // 键名不得有多余单引号（首版手写 ar-SA 时真犯过：exportTooLarge'）
      if (/exportTooLarge'/.test(line)) offenders.push(`${loc}: 键名有多余单引号`)
    }
    expect(offenders).toEqual([])
  })

  it('量具自证：判据能识别「文案写死数字」与「键名多引号」两种形态', () => {
    expect("…{max}…".includes('{max}')).toBe(true)
    expect("…50MB…".includes('{max}')).toBe(false)
    expect(/exportTooLarge'/.test("exportTooLarge': 'x'")).toBe(true)
    expect(/exportTooLarge'/.test("exportTooLarge: 'x'")).toBe(false)
  })
})
