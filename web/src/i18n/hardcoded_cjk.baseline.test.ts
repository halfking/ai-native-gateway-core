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
  // ★★ 2026-11-09（r1050 轮复核）：本文件的两条用例各自**全量扫一遍 `src/`**，
  // ★★ 而 `vite.config.ts` 的全局 `testTimeout: 20_000` 是**按视图测试的实耗**定的
  // ★★ （★ 注释里写明「这仍是缓解不是根治」，★ 根因是 187 个 jsdom 环境并发放大
  // ★★ + 共享宿主 CPU 争抢）⇒ ★★★ **本文件不在那个口径里**：★ 它的工作量随
  // ★★★ **`src/` 规模线性增长**，★ 不随测试框架的并发策略变化。
  // ★★★ **实测证据**：★ 2026-11-09 在 **load average 207~371** 的宿主上跑
  // ★★★ `web-gates` 总入口 ⇒ ★★★ 本文件第一条 it 报
  // ★★★ `Error: Test timed out in 20000ms` ⇒ ★★★★ **门总入口因此 EXIT=1**
  // ★★★★ （★ 隔离单独跑同一条 ⇒ **PASS / 3.63s~5.70s** ⇒ ⇒ ★★★★ **是预算与
  // ★★★★ 工作量失配，不是判据红** ——★ 与同轮 Go 侧 `-timeout` 失配**同族**）。
  // ★★★ ⇒ 处置：★ ① **显式给 60_000**（★ 对隔离实测 5.70s 留 ~10x、★ 对已观测到的
  // ★★★ 「>20s」留 3x）；★ ② 顺带**去掉失败路径里的重复全量扫描**（★ 原实现调了
  // ★★★ 两次 `countAll()`，★ 而它一次就同时返回 count 与 files ⇒ ★★ 失败路径成本
  // ★★★ **直接减半**）；★ ③ 全局 20s **不动**（★ 32 处既有 `{ timeout: 20_000 }`
  // ★★★ 与全局同口径，★ 抬高全局会造成「两套口径」⇒ ★★ **只给「工作量随仓增长」
  // ★★★ 的这一条开口**）。
  it('current hardcoded CJK count does not exceed the baseline (ratchet)', () => {
    const baseline = readBaseline()
    expect(baseline, 'scripts/i18n-cjk-baseline.json missing or invalid — run `node scripts/i18n-cjk-count.mjs --update-baseline`').not.toBeNull()
    // ★ 一次扫描同时拿 count 与 files（★ `countAll()` 一次就返回两者）⇒ ★
    // ★ 失败路径不再重扫一遍全仓。
    const { count, files } = countAll()
    if (count > baseline!.count) {
      // 方便定位新增来源：打印 top 20 文件
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
  }, 60_000)

  it('counter is functional (non-trivial source still has legacy entries)', () => {
    // 存量尚未清零时的合理性检查：计数器必须真的在数东西。
    // 存量清零后可把本断言改为 toBe(0) 并删除 baseline 机制。
    const { count } = countAll()
    expect(count).toBeGreaterThan(0)
  }, 60_000)

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
