// 审计入口 URL 规则（UI规范 10 §4.6.75）。
//
// 背景：`public/entry-switch.js` 在 large（≥1280px）访问 `/m` **根入口**时
// 会 `location.replace('/')` 主动交接给桌面端（深链不动）。这是**设计内**行为，
// 不是缺陷 —— 15 档矩阵里 `/m/` 的 `1280x800` / `1440x900` 两行报 `no-content`，
// 是判据照着「自动入口」去量一个**按设计不会留在移动端**的地址。
//
// 但 `?mobile` 是**受支持的用户路径**（平板显式选移动端就落到它），
// 而它此前一档读数都没有 ⇒ 平板档的移动端版式处于无人验证状态。
// 所以判据在这两档显式补 `?mobile`，而不是把它们记成「无读数」。
//
// ⚠️ 断点 1280 有三份镜像，必须同改（entry-switch.js 自己的要求）：
//   ① public/entry-switch.js 的 `(min-width: 1280px)` 字面量
//   ② src/composables/useWindowClass.ts 的 BREAKPOINT_LARGE_PX
//   ③ 本文件的 LARGE_HANDOFF_PX
// 由 viewport-matrix.spec.ts 断言三者一致。

export const LARGE_HANDOFF_PX = 1280

/**
 * 根入口的判定，与 entry-switch.js 的 `isRootEntry` 同口径，但**不写死 `/m`**：
 * driver 的 BASE 由 `--base` 决定（默认 `/m`），所以判据必须对任意 base 成立。
 * 规则 = 「路径以 `/` 结尾，且去掉这一层后最多只剩一段」。
 *   /m/        → /m   → 1 段 → 根入口 ✓（与 entry-switch 一致）
 *   /m/index.html → /m/ → 根入口 ✓（entry-switch 也算根入口）
 *   /          → ''   → 0 段 → 根入口 ✓（BASE 为空时）
 *   /m/keys    → 不以 / 结尾 → 深链 ✗
 *   /m/keys/   → /m/keys → 2 段 → 深链 ✗
 * ⚠️ 第一版写成 `/^\/$/`，只匹配裸 `/`，而 driver 的路由是 `/m/` ⇒ 规则静默失效、
 *   两档重新变回 no-content。是 viewport-matrix.spec.ts 的行为断言当场抓到的
 *   （守卫第一次运行就抓到了它自己守护的那段代码的错，这正是要有行为断言的原因）。
 */
export function isRootEntry(route) {
  const p = route.split('?')[0].replace(/\/index\.html$/, '/')
  if (!p.endsWith('/')) return false
  return p.slice(0, -1).split('/').filter(Boolean).length <= 1
}

/**
 * 返回这一档该导航到的地址与入口模式。
 * `route` 是 driver 的路由（BASE 已拼好，如 `/m/`、`/m/keys`）。
 */
export function auditEntryUrl(route, w) {
  const forceMobile = isRootEntry(route) && w >= LARGE_HANDOFF_PX
  return {
    url: route + (forceMobile ? '?mobile' : ''),
    entryMode: forceMobile ? 'mobile-forced' : 'auto',
    forceMobile,
  }
}