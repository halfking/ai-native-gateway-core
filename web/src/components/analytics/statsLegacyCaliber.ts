// statsLegacyCaliber.ts —— 「旧口径」披露的判定口径（N1 修复轮，2026-11-24）。
//
// ★ 判定**只看响应**里的 probe_scope，不看本地开关：
//   面板顶部的「含探测」徽标本来就是这么算的（见 SessionStatsPanel.vue 的
//   includeProbesActive 计算属性）⇒ 披露标记若改看本地开关，就会出现
//   「徽标没亮、标记却亮了」（或反过来）的自相矛盾 ⇒ 读者没法信任何一个。
//   ⇒ 唯一真源 = 会话总览响应里的 probe_scope。
//
// ★ 什么算「旧口径」：
//   前端把 include_probes 发给全部 7 个端点，但后端只在 session-overview 上
//   认它；session-trend / session-health / errors / performance 仍按旧宇宙
//   计算 ⇒ 当总览说「含探测」而那四腿仍是旧宇宙时，同一个面板里就并存
//   两把尺。⇒ 此时必须给那几块挂上可见标记。

/** 会话宇宙口径：响应里的 probe_scope 是否表示「含探测」。 */
export function isProbeScopeIncluded(probeScope: string | null | undefined): boolean {
  return probeScope === 'included' || probeScope === 'degraded_included'
}
