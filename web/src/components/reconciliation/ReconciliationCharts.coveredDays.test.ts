/**
 * 趋势图「未聚合日」裁剪规则的回归门。
 *
 * 这条规则决定生产趋势图末端会不会出现一条假断崖：后端为了让「每天明细」
 * 表格连续，会给区间内还没跑 rollup 的日期补一行 0；页面默认 end=今天，所以
 * 生产上每天都会命中。锁住它。
 */
import { describe, expect, it } from 'vitest'
import { pickCoveredDays } from './coveredDays'

const day = (date: string, n: number) => ({ date, totals: { request_count: n } })

describe('pickCoveredDays', () => {
  it('剔除区间末端还没聚合的补零日', () => {
    const days = [
      day('2026-09-27', 1229),
      day('2026-09-28', 1404),
      day('2026-09-29', 0), // rollup 未跑，后端补的 0 行
    ]
    const out = pickCoveredDays(days, ['2026-09-27', '2026-09-28'])
    expect(out.map((d) => d.date)).toEqual(['2026-09-27', '2026-09-28'])
  })

  it('真实存在的零请求日（有快照）必须保留——不能按 request_count 过滤', () => {
    // 防回归：修「假断崖」时最容易犯的错就是顺手把 0 值天也删掉，
    // 那会把「这天真的没流量」和「这天没聚合」混为一谈。
    const days = [day('2026-09-28', 1404), day('2026-09-29', 0)]
    const out = pickCoveredDays(days, ['2026-09-28', '2026-09-29'])
    expect(out.map((d) => d.date)).toEqual(['2026-09-28', '2026-09-29'])
  })

  it('covered 为空或未传时不裁剪（无 coverage 的调用方保持旧行为）', () => {
    const days = [day('2026-09-28', 1), day('2026-09-29', 0)]
    expect(pickCoveredDays(days, undefined)).toHaveLength(2)
    expect(pickCoveredDays(days, [])).toHaveLength(2)
  })

  it('日期顺序跟随 days 原序，不被 covered 的顺序带偏', () => {
    const days = [day('2026-09-27', 2), day('2026-09-28', 1), day('2026-09-29', 0)]
    // covered 故意倒序给出：结果必须仍是 days 的原序，否则趋势图 x 轴会乱序
    const out = pickCoveredDays(days, ['2026-09-29', '2026-09-28', '2026-09-27'])
    expect(out.map((d) => d.date)).toEqual(['2026-09-27', '2026-09-28', '2026-09-29'])
  })
})
