// exportFile.gate.test.ts — D-5 源码门禁（docs/UI规范/19 §3.2 用例 D-5）。
//
// 规则：新增导出/下载工具**不得**只写 a[download] 单通道——凡新建下载工具
// 函数必须含 canShare 分支，或在文件头登记「桌面专用」理由。
// 判据形态：blob 下载的唯一合法实现点是 utils/exportFile.ts（exportFile）；
// 任何其他文件再出现 URL.createObjectURL 即红——那意味着绕过了降级链。
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'fs'
import { dirname, join, resolve } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SRC = resolve(__dirname, '..')

/** 允许持有 createObjectURL 的文件（豁免清单——新增需在 PR 里说明理由）。 */
const CREATE_OBJECT_URL_ALLOWLIST = new Set([
  // 降级链唯一实现点（含 canShare 分支）
  'utils/exportFile.ts',
  // 其自身的单测
  'utils/exportFile.spec.ts',
  // 本门禁（allowlist 常量里含字面量）
  'utils/exportFile.gate.test.ts',
])

function* walk(dir: string): Generator<string> {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (name === 'node_modules' || name === 'dist') continue
    if (statSync(full).isDirectory()) yield* walk(full)
    else if (/\.(ts|vue)$/.test(name)) yield full
  }
}

describe('D-5 源码门禁：下载不得绕过 exportFile 降级链', () => {
  it('createObjectURL 只允许出现在 exportFile 及其单测（其余全走降级链）', () => {
    const offenders: string[] = []
    for (const file of walk(SRC)) {
      const rel = file.slice(SRC.length + 1)
      if (CREATE_OBJECT_URL_ALLOWLIST.has(rel)) continue
      const text = readFileSync(file, 'utf8')
      if (text.includes('createObjectURL')) offenders.push(rel)
    }
    expect(
      offenders,
      `以下文件绕过了 exportFile 降级链（docs/UI规范/19 §3.2 D-5）：\n${offenders.join('\n')}`,
    ).toEqual([])
  })

  it('exportFile.ts 必须保留 canShare 分支（①分享面是链条第一臂）', () => {
    const src = readFileSync(join(SRC, 'utils/exportFile.ts'), 'utf8')
    expect(src).toContain('navigator.canShare')
    expect(src).toContain('navigator.share')
  })
})
