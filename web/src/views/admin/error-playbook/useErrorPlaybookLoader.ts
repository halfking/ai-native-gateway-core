// useErrorPlaybookLoader.ts — 错误处理手册页面的共享加载状态机（2026-11-21）
//
// 与 session-analytics 的三态机同形，但**没有 stale 态**：手册 API 查的是
// 真表 + SQL 聚合，没有物化视图新鲜度闸 ⇒ 服务端不会主动以
// `analytics_view_stale` 拒答。两种结局就够了：在飞 / 失败。
//
// 错误文案走 `surfaceError`（仓内用户可见错误文案的唯一出口）；
// 业务性 422 的原文（后端写成用户可读的门控文案）原样透出。

import { ref, type Ref } from 'vue'
import { surfaceError } from '../../../i18n/errorSurface'

export interface ErrorPlaybookLoader {
  loading: Ref<boolean>
  error: Ref<string>
  /**
   * 跑一次加载。成功返回数据；失败返回 null 并把状态写进 error。
   * @param fn 实际的数据获取函数
   * @param errKey 主体专属错误文案键（已翻译，含恢复动作）
   */
  run<T>(fn: () => Promise<T>, errKey: string): Promise<T | null>
}

export function useErrorPlaybookLoader(): ErrorPlaybookLoader {
  const loading = ref(false)
  const error = ref('')

  async function run<T>(fn: () => Promise<T>, errKey: string): Promise<T | null> {
    loading.value = true
    error.value = ''
    try {
      return await fn()
    } catch (e) {
      error.value = surfaceError(e, { key: errKey })
      return null
    } finally {
      loading.value = false
    }
  }

  return { loading, error, run }
}
