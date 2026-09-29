/**
 * 趋势图「未聚合日」裁剪规则。
 *
 * 独立成模块（而不是塞在 .vue 里）：`<script setup>` 不允许 ES export，而这个
 * 规则必须能被单测直接覆盖——它决定生产趋势图末端会不会画出一条假断崖。
 *
 * 背景：后端为了让「每天明细」表格日期连续，会给区间内还没跑 rollup 的日期补
 * 一行 0。趋势图若直接画这行 0，就会在区间末日出现一条垂直断崖，看着像流量崩
 * 了，实际那天只是没聚合。页面默认 end=今天，所以生产上每天都会命中。
 */
export function pickCoveredDays<T extends { date: string }>(days: T[], covered?: string[]): T[] {
  if (!covered || covered.length === 0) return days
  const set = new Set(covered)
  return days.filter((d) => set.has(d.date))
}
