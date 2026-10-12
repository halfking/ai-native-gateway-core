// `scripts/audit-entry-url.mjs` 的类型声明。
//
// 为什么需要它：该模块被 `src/composables/viewport-matrix.spec.ts` 以 ESM 导入做
// 行为断言，而 `vue-tsc` 对无声明的 `.mjs` 会报 TS7016（implicitly has an 'any' type）。
//
// ⚠️ **声明必须与实现同步**：这里手写的签名一旦漂移，守卫就会在「断言的规则」
// 和「实际跑的规则」之间产生偏差 —— 而这正是本轮已经踩过一次的那类错
// （源码里有断言 ≠ 产物里有结果，见 UI规范 10 §4.6.75 §四）。
// 下面的 4 条 `auditEntryUrl` 断言会把形状钉住。
export declare const LARGE_HANDOFF_PX: number
export declare function isRootEntry(route: string): boolean
export declare function auditEntryUrl(route: string, w: number): {
  url: string
  entryMode: 'auto' | 'mobile-forced'
  forceMobile: boolean
}