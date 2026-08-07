// useProviderLatency — 供应商 HTTP 延时轮询 composable
//
// 2026-07-23: 子项②。仅在 provider 维度展示，5 分钟一次与 NodeProbeWorker 探测节奏对齐。
// 2026-08-06: 从 LiveRequestStreamV2.vue 抽取，封装轮询生命周期 + groupBy 联动。

import { ref, watch, onMounted, onUnmounted, type Ref } from 'vue'
import { fetchProviderLatency } from '../api/provider-probe'
import type { GroupByDimension } from '../types/swimlane'

export interface UseProviderLatencyOptions {
  /** 当前泳道分组维度（来自 useSwimLane）。仅 provider 维度才拉取。 */
  groupBy: Ref<GroupByDimension>
}

export function useProviderLatency(options: UseProviderLatencyOptions) {
  const { groupBy } = options

  // providerLatencyMap: { [providerCode(字符串)]: latency_ms }
  // key 用 provider_code（不是 provider_id），因为 lane.id = req.ProviderCode
  const providerLatencyMap = ref<Record<string, number>>({})
  let timer: ReturnType<typeof setInterval> | null = null

  async function refreshProviderLatency() {
    // 仅 provider 维度拉取，减少不必要的请求
    if (groupBy.value !== 'provider') return
    try {
      const res = await fetchProviderLatency()
      const map: Record<string, number> = {}
      for (const e of res.entries || []) {
        if (e.latency_ms <= 0) continue
        // 优先用 provider_code（与 lane.id 完全一致），回退到 name 作为容错
        if (e.provider_code) map[e.provider_code] = e.latency_ms
        if (e.provider_name) map[e.provider_name] = e.latency_ms
      }
      providerLatencyMap.value = map
    } catch {
      // 接口可能在新探测模式未启用时不存在，静默失败
    }
  }

  // 维度切换到 provider 时立即拉取一次
  watch(groupBy, (g) => {
    if (g === 'provider') void refreshProviderLatency()
  })

  onMounted(() => {
    void refreshProviderLatency()
    timer = setInterval(() => void refreshProviderLatency(), 5 * 60 * 1000)
  })

  onUnmounted(() => {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  })

  return {
    providerLatencyMap,
    refreshProviderLatency,
  }
}
