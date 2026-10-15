import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { zhCN } from './zh-CN'
import { enUS } from './en-US'
import { parseDictSource } from '../../scripts/i18nDictScanner.mjs'
import { flattenDict } from './keyScan'

/**
 * ★★★★★ **手写扫描器 ↔ 真实对象 的逐键对账**。
 *
 * ## 这道门在防什么
 *
 * `verify-i18n-parity.mjs` 跑在 build 前置里，没有编译链 ⇒ 只能手写扫描词典。
 * 一份没人验证的手写解析器有多危险，本仓有**实测数据**：
 * 旧版（按行 + 几个正则）连续漏掉 **19** 个键
 * （键名带引号 7 个 + 一行多键 12 个），漏掉的后果不是误报，
 * 而是**判据 1/2/3（键集一致 / 无未翻译 / 无重复）对这些键根本不生效** ——
 * zh/en 在那儿写得不一致、值等于键名，门全程绿。
 *
 * ## ★ 为什么对账能**永久地**关掉这个风险
 *
 * 与其把扫描器调得更可靠，不如**让它不必可靠**：
 * 本文件拿它的输出与 `import { zhCN }` 的**真实对象**逐键比。
 * 两侧键集必须**完全相等**（双向），任何一侧漂移都会红。
 * ⇒ 扫描器从「一份没人验证的手写代码」变成「一份被钉住的实现」。
 *
 * 这也回应了第一百四十批那条结论：
 * **能 import 时就别写解析器；不能 import 时，让解析器与 import 的结果对账。**
 */

const WEB = join(process.cwd())

/** 合成样本：五种最容易把正则解析器打穿的形态。 */
const SYNTHETIC = [
  'export const zhCN = {',
  '  a: {',
  "    month: {",
  "      jan: '1 月', feb: '2 月', mar: '3 月',",
  "      'apr': '4 月',",
  "    },",
  "    note: 'x, y: z',",
  "    plain: 'ok',",
  "    nested: { deep: '更深' },",
  '  },',
  '} as const',
  '',
].join('\n')

describe('① 读数：两个口径各是多少，差多少', () => {
  it('①-1 ★★★★★ 扫描器与真实对象的键数必须相等，差值必须打印出来', () => {
    const scanned = parseDictSource(readFileSync(join(WEB, 'src/i18n/zh-CN.ts'), 'utf8'))
    const real = new Set(flattenDict(zhCN))
    const got = new Set(scanned.out.keys())
    const onlyScanned = [...got].filter((k) => !real.has(k))
    const onlyReal = [...real].filter((k) => !got.has(k))
    console.log(
      `[①-1] 真实对象 ${real.size} 键 · 扫描器 ${got.size} 键\n` +
        `       只在扫描器里（凭空造的键）${onlyScanned.length} 个` +
        (onlyScanned.length ? `：${onlyScanned.slice(0, 5).join(', ')}` : '') +
        `\n       只在真实对象里（被吞掉的键）${onlyReal.length} 个` +
        (onlyReal.length ? `：${onlyReal.slice(0, 5).join(', ')}` : ''),
    )
    expect(got.size).toBeGreaterThan(3000)
  })
})

describe('★★ 双向对账：两个口径必须逐键完全相等', () => {
  /** 抽成函数，好让负控与正向**走同一段代码**。 */
  const diffBothWays = (real: Set<string>, got: Set<string>) => ({
    onlyScanned: [...got].filter((k) => !real.has(k)),
    onlyReal: [...real].filter((k) => !got.has(k)),
  })

  const files: [label: string, dict: unknown, path: string][] = [
    ['zh-CN', zhCN, 'src/i18n/zh-CN.ts'],
    ['en-US', enUS, 'src/i18n/en-US.ts'],
  ]

  for (const [label, dict, path] of files) {
    it(`② ${label}：扫描器与真实对象逐键相等`, () => {
      const scanned = parseDictSource(readFileSync(join(WEB, path), 'utf8'))
      const d = diffBothWays(new Set(flattenDict(dict)), new Set(scanned.out.keys()))
      expect(d.onlyScanned, `${label} 扫描器**凭空造出**了这些键`).toEqual([])
      expect(d.onlyReal, `${label} 扫描器**吞掉**了这些键`).toEqual([])
    })

    it(`②b ${label}：扫描器必须零重复键`, () => {
      const scanned = parseDictSource(readFileSync(join(WEB, path), 'utf8'))
      expect(scanned.duplicates, `${label} 报出了重复键：${scanned.duplicates.join(', ')}`).toEqual([])
    })
  }

  it('②-c ★★★ 负控：两个口径不一致时，这段比较**必须**报出来', () => {
    // ★ 没有这条，上面那两条就可能是「恒真的空门」——
    //   两边都空时 `every` / `filter` 同样返回空数组。
    //   ⇒ 先证这段比较**还能报出差异**，再拿它去比真实数据。
    const d = diffBothWays(new Set(['x', 'y']), new Set(['y', 'z']))
    expect(d.onlyReal).toEqual(['x'])
    expect(d.onlyScanned).toEqual(['z'])
  })
})

describe('③ 扫描器自身的形态自证（合成样本）', () => {
  const scanned = parseDictSource(SYNTHETIC).out

  it('③-1 ★★★★★ 一行多键的每个键都要抽到', () => {
    for (const k of ['a.month.jan', 'a.month.feb', 'a.month.mar']) {
      expect(scanned.has(k), `漏了 ${k}`).toBe(true)
    }
  })

  it('③-2 ★★★★★ 带引号的键名要抽到', () => {
    expect(scanned.has('a.month.apr')).toBe(true)
  })

  it('③-3 ★★★★★ 值里的逗号与冒号**不得**被切成新键', () => {
    expect(scanned.get('a.note')).toBe('x, y: z')
    // ★ 关键断言：`b` 这个键**根本不该存在** —— 旧正则会在逗号处切开，凭空造出它。
    expect([...scanned.keys()].filter((k) => k === 'a.b' || k === 'b')).toEqual([])
  })

  it('③-4 ★★★★ 嵌套对象与尾行 `as const` 都要处理对', () => {
    expect(scanned.get('a.nested.deep')).toBe('更深')
    // 栈若没被正确弹空，`a.plain` 会被算成 `a.nested.plain` 之类
    expect(scanned.get('a.plain')).toBe('ok')
    expect(scanned.has('a.month.jan')).toBe(true)
  })

  it('③-5 ★★ 顶层 `export const X = {` 不产生路径段', () => {
    expect([...scanned.keys()].every((k) => !k.startsWith('zhCN'))).toBe(true)
  })
})
