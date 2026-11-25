// useSessionAnalytics.ts — 会话分析页面的共享加载状态机（2026-11-20）
//
// 为什么单独抽一层：5 个页面都要处理同一种**三态**结果，而这三态的口径必须
// 一致，否则同一个页面上会出现「一个卡片报陈旧、另一个卡片报失败」。
//
//   · loading —— 请求在飞
//   · stale   —— 服务端 503 + code=analytics_view_stale
//     （聚合视图超出新鲜度预算，服务端**主动拒绝**返回过期数字）
//   · error   —— 其它失败（403 / 500 / 网络）
//
// ★ 陈旧态**不是**错误态：服务端已经明确说了「这是过期数字，我不给」。
//   前端把它渲染成通用错误，等于把「正在刷新」说成「坏了」—— 用户会去叫运维，
//   而实际上只要再等一轮刷新就好。
//
// 错误文案走 `surfaceError`（仓内用户可见错误文案的唯一出口）。

import { ref, type Ref } from 'vue'
import { surfaceError } from '../../../i18n/errorSurface'
import { isViewStaleError } from '../../../api/sessionAnalytics'

export interface SessionAnalyticsLoader {
  loading: Ref<boolean>
  error: Ref<string>
  stale: Ref<boolean>
  reset(): void
  /**
   * 跑一次加载。成功返回数据；失败返回 null 并把状态写进 error/stale。
   * @param fn 实际的数据获取函数
   * @param errKey 主体专属错误文案键（已翻译，含恢复动作）
   */
  run<T>(fn: () => Promise<T>, errKey: string): Promise<T | null>
}

export function useSessionAnalyticsLoader(): SessionAnalyticsLoader {
  const loading = ref(false)
  const error = ref('')
  const stale = ref(false)
  let generation = 0

  function reset(): void {
    generation++
    loading.value = false
    error.value = ''
    stale.value = false
  }

  async function run<T>(fn: () => Promise<T>, errKey: string): Promise<T | null> {
    const request = ++generation
    loading.value = true
    error.value = ''
    stale.value = false
    try {
      const result = await fn()
      return request === generation ? result : null
    } catch (e) {
      if (request !== generation) return null
      if (isViewStaleError(e)) {
        // 陈旧 ⇒ 不写 error：交给 stale 横幅说「正在刷新」
        stale.value = true
      } else {
        error.value = surfaceError(e, { key: errKey })
      }
      return null
    } finally {
      if (request === generation) loading.value = false
    }
  }

  return { loading, error, stale, reset, run }
}
