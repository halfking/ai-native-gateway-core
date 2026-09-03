// hardcoded_cjk.baseline.test.ts — 硬编码中文棘轮门（2026-09-04）
//
// 阻止 src/ 新增硬编码中文：当前计数必须 <= scripts/i18n-cjk-baseline.json
// 中的 baseline。清完一批存量后运行
//   node scripts/i18n-cjk-count.mjs --update-baseline
// 把棘轮降下来（计数只许降不许升）。
//
// 计数口径见 src/i18n/hardcodedCjk.ts 头注释（排除 locales/i18n/测试文件、
// 跳过整行注释）。CLI 与本测试共用同一实现，保证口径一致。

import { describe, expect, it } from 'vitest'
import { countAll, readBaseline } from './hardcodedCjk'

describe('hardcoded CJK baseline gate', () => {
  it('current hardcoded CJK count does not exceed the baseline (ratchet)', () => {
    const baseline = readBaseline()
    expect(baseline, 'scripts/i18n-cjk-baseline.json missing or invalid — run `node scripts/i18n-cjk-count.mjs --update-baseline`').not.toBeNull()
    const { count } = countAll()
    if (count > baseline!.count) {
      // 方便定位新增来源：打印 top 20 文件
      const { files } = countAll()
      const top = files
        .slice(0, 20)
        .map((f) => `  ${f.file}: ${f.count}`)
        .join('\n')
      throw new Error(
        `Hardcoded CJK count increased: ${count} > baseline ${baseline!.count} (${baseline!.updated}).\n` +
          'Replace new hardcoded Chinese with t(\'key\') + locale entries.\n' +
          'Top files:\n' + top,
      )
    }
  })

  it('counter is functional (non-trivial source still has legacy entries)', () => {
    // 存量尚未清零时的合理性检查：计数器必须真的在数东西。
    // 存量清零后可把本断言改为 toBe(0) 并删除 baseline 机制。
    const { count } = countAll()
    expect(count).toBeGreaterThan(0)
  })
})
