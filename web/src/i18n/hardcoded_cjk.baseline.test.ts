// hardcoded_cjk.baseline.test.ts — 硬编码中文棘轮门（2026-09-04，口径 2026-10-06 重铸）
//
// 阻止 src/ 新增硬编码中文：当前计数必须 <= scripts/i18n-cjk-baseline.json
// 中的 baseline。清完一批存量后运行
//   node scripts/i18n-cjk-count.mjs --update-baseline
// 把棘轮降下来（计数只许降不许升）。
//
// 计数口径见 src/i18n/hardcodedCjk.ts 头注释（排除 locales/i18n/测试文件；
// 只剥离解析器认得的真注释）。CLI 与本测试共用同一实现，保证口径一致。

import { describe, expect, it } from 'vitest'
import { countAll, readBaseline, selfCheckCounting } from './hardcodedCjk'

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

  // ⚠️ 口径自证必须**每次跑门禁都执行**。2026-10-06 的教训：口径从「只跳整行注释」
  //   换成「只跳真注释」时，先后有三个错误实现（独立扫描器 / 只在节点上收 / 节点+NodeArray）
  //   全部**只表现为计数偏大**——方向正常、不报错、不破坏单调性，
  //   而当时的 4 组自证一条都没拦住，因为探针里没有一个含 `${}` 的模板串。
  //   ⇒ 自证覆盖不到的形状等于没验；换实现时必须同时补上覆盖新形状的探针（D9/D13/D14）。
  it('counter self-check: comments stripped in both directions', () => {
    const cases = selfCheckCounting()
    // ⚠️ 先断「探针真的跑了」：一个空的用例数组会让下面那条断言恒绿。
    expect(cases.length, 'counter self-check produced no cases — it did not run').toBeGreaterThanOrEqual(16)
    const failed = cases.filter((c) => !c.ok)
    expect(
      failed.map((c) => `${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`),
      'hardcoded CJK counter self-check failed',
    ).toEqual([])
  })
})
