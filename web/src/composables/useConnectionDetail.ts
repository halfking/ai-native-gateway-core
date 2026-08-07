// useConnectionDetail — 连接详情弹窗状态 composable
//
// 2026-07-05 v2: 管理员连接详情弹窗、空闲块机制。
// 2026-08-06: 从 LiveRequestStreamV2.vue 抽取，依赖 isAdmin / isEditingUrl 注入。

import { ref, type Ref } from 'vue'

export interface UseConnectionDetailOptions {
  /** 是否管理员（非管理员点击不弹窗） */
  isAdmin: Ref<boolean>
  /** 是否正在编辑 SSE URL（关闭弹窗时一并退出编辑态） */
  isEditingUrl: Ref<boolean>
}

export function useConnectionDetail(options: UseConnectionDetailOptions) {
  const { isAdmin, isEditingUrl } = options

  const showConnectionDetail = ref(false)

  function toggleConnectionDetail() {
    if (isAdmin.value) {
      showConnectionDetail.value = !showConnectionDetail.value
      if (!showConnectionDetail.value) {
        isEditingUrl.value = false
      }
    }
  }

  return {
    showConnectionDetail,
    toggleConnectionDetail,
  }
}
