// useProviderLatency — 供应商 HTTP 延时轮询 composable
//
// 2026-07-23: 子项②。仅在 provider 维度展示，5 分钟一次与 NodeProbeWorker 探测节奏对齐。
// 2026-08-06: 从 LiveRequestStreamV2.vue 抽取，封装轮询生命周期 + groupBy 联动。
// 2026-09-01: P2-6 内存泄漏修复——visibilitychange 时 clearInterval / setInterval，
//             页面隐藏期间不再触发 5 分钟轮询的累积，避免 idle tab 内存累积。

import { ref, watch, onMounted, onUnmounted, type Ref } from 'vue'
import { fetchProviderLatency } from '../api/provider-probe'
import type { GroupByDimension } from '../types/swimlane'

export interface UseProviderLatencyOptions {
  /** 当前泳道分组维度（来自 useSwimLane）。仅 provider 维度才拉取。 */
  groupBy: Ref<GroupByDimension>
}

const POLL_INTERVAL_MS = 5 * 60 * 1000

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

  function startTimer() {
    if (timer != null) return
    timer = setInterval(() => void refreshProviderLatency(), POLL_INTERVAL_MS)
  }

  function stopTimer() {
    if (timer == null) return
    clearInterval(timer)
    timer = null
  }

  // 页面隐藏时暂停轮询：idle / 切到后台 tab 时不再叠加 5 分钟 timer 链，
  // 避免长时间挂起后唤醒瞬间触发一连串过期请求造成内存峰值。
  function onVisibilityChange() {
    if (typeof document === 'undefined') return
    if (document.hidden) {
      stopTimer()
    } else {
      startTimer()
      // 重新可见时立即拉一次，让 UI 尽快反映当前实际延时
      if (groupBy.value === 'provider') void refreshProviderLatency()
    }
  }

  // 维度切换到 provider 时立即拉取一次
  watch(groupBy, (g) => {
    if (g === 'provider') void refreshProviderLatency()
  })

  onMounted(() => {
    void refreshProviderLatency()
    startTimer()
    document.addEventListener('visibilitychange', onVisibilityChange)
  })

  onUnmounted(() => {
    stopTimer()
    document.removeEventListener('visibilitychange', onVisibilityChange)
  })

  return {
    providerLatencyMap,
    refreshProviderLatency,
  }
}
