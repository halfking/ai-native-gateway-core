import { onUnmounted, getCurrentInstance } from 'vue'

/**
 * 代际号防竞态守卫（2026-10-01 第十八轮 ④ 从三处内联实现收敛：
 * UserDetailDrawer / SessionCatalogPanel / TenantDetailView.loadStats，
 * 原样板 useReconciliationPage.ts 的 fetchGen）。
 *
 * 快速切换参数（tab / 搜索词 / 统计窗口）时，旧响应后到不得覆盖新结果：
 *
 * ```ts
 * const race = useRaceGuard()
 * async function load() {
 *   const gen = race.begin()
 *   loading.value = true
 *   try {
 *     const r = await fetchStuff(x.value)
 *     if (race.stale(gen)) return
 *     data.value = r
 *   } finally {
 *     if (race.current(gen)) loading.value = false
 *   }
 * }
 * ```
 *
 * 组件卸载时自动失效（in-flight 响应一律视为 stale），比内联版多堵一个
 * 「卸载后迟到响应写 ref」的小口子；非组件环境（纯 composable/测试）安全
 * 退化为无卸载钩子。
 */
export interface RaceGuard {
  /** 开始一次新请求，返回本次代际号。 */
  begin(): number
  /** gen 是否已过期（有更新的请求启动，或组件已卸载）。 */
  stale(gen: number): boolean
  /** gen 是否仍是当前代（用于 finally 中只在最新代收尾 loading 等）。 */
  current(gen: number): boolean
}

export function useRaceGuard(): RaceGuard {
  let gen = 0
  let disposed = false
  if (getCurrentInstance()) {
    onUnmounted(() => {
      disposed = true
      gen++ // 失效一切 in-flight 代际
    })
  }
  return {
    begin: () => ++gen,
    stale: (g) => disposed || g !== gen,
    current: (g) => !disposed && g === gen,
  }
}
