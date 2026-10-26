import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as capabilitiesApi from '@/api/featureCapabilities'
import type { FeatureCapabilities } from '@/api/featureCapabilities'

/**
 * capabilities store — 功能能力位（2026-10-21，R72f 轮 / 属主决策项 #10 的后半）。
 *
 * ## 用途
 *
 * 让导航层能**按能力隐藏入口**：某功能在当前实例没装配时，
 * 那一席就不渲染、那条路由也进不去 ⇒ ⇒ 而不是「显示出来、点进去必然 404」。
 *
 * ## ★ 失败口径：**fail-closed**
 *
 * 能力位取不到（网络错 / 401 / 403 / 尚未加载）⇒ **一律当作「该能力没开」**。
 *
 * ★ 为什么不是 fail-open：★ fail-open 的代价是「用户点进去必然失败，
 * 而且 404 与『端点不存在』同形、没有一层会说真话」——那正是本轮要修的
 * 症状。⇒ ⇒ **宁可少显示一席，也不要显示一个死入口。**
 *
 * ★ 同理，「尚未加载」也按「没开」处理 ⇒ 抽屉可能先不显示、加载完再出现
 *   （一次可见的闪动）⇒ ⇒ 换取「绝不会点到一个死入口」，这个交换是划算的。
 */
export const useCapabilitiesStore = defineStore('capabilities', () => {
  const caps = ref<Partial<FeatureCapabilities>>({})
  const loaded = ref(false)
  const loadError = ref<string | null>(null)

  /** 已开启的能力名集合（便于 O(1) 判定）。 */
  const enabled = computed<ReadonlySet<string>>(() => {
    const out = new Set<string>()
    for (const [name, on] of Object.entries(caps.value)) {
      if (on === true) out.add(name)
    }
    return out
  })

  /**
   * 某能力是否开启。
   *
   * ★ fail-closed：未加载 / 取失败 / 键不存在 ⇒ **false**。
   */
  function isEnabled(name: string): boolean {
    return enabled.value.has(name)
  }

  /** 拉取能力位（幂等：已加载且无错时直接返回）。 */
  async function refresh(force = false): Promise<void> {
    if (loaded.value && !force) return
    try {
      const data = await capabilitiesApi.fetchCapabilities()
      caps.value = { ...data }
      loadError.value = null
    } catch (e) {
      // ★ 失败 ⇒ 保持「全关」（fail-closed），并记住错误供诊断。
      caps.value = {}
      loadError.value = e instanceof Error ? e.message : String(e)
    } finally {
      loaded.value = true
    }
  }

  /** 登出/切换身份时清空（能力位可能随身份/装配变化）。 */
  function reset(): void {
    caps.value = {}
    loaded.value = false
    loadError.value = null
  }

  return { caps, loaded, loadError, enabled, isEnabled, refresh, reset }
})
