// useIncidentDiagnosis — 诊断工作台（RouteIncidentDrawer）状态 composable
//
// 2026-07-13: 转发泳道诊断事件，承载 RouteIncidentDrawer（Phase 1 只读）。
// 2026-08-06: 从 LiveRequestStreamV2.vue 抽取，唯一副作用通过注入的 onOpenRequest 回调。

import { ref } from 'vue'
import type { RouteIncident } from '../types/routeIncident'

export interface UseIncidentDiagnosisOptions {
  /** 从诊断工作台跳转到指定请求（组件 emit('openDetail') 的包装） */
  onOpenRequest: (requestId: string) => void
}

export function useIncidentDiagnosis(options: UseIncidentDiagnosisOptions) {
  const { onOpenRequest } = options

  const activeIncidentId = ref<string | null>(null)
  const activeIncidentPreview = ref<RouteIncident | null>(null)

  function handleDiagnose(incidentId: string, preview: RouteIncident) {
    activeIncidentId.value = incidentId
    activeIncidentPreview.value = preview
  }

  function closeDiagnose() {
    activeIncidentId.value = null
    activeIncidentPreview.value = null
  }

  function handleRequestFromDrawer(requestId: string) {
    closeDiagnose()
    onOpenRequest(requestId)
  }

  return {
    activeIncidentId,
    activeIncidentPreview,
    handleDiagnose,
    closeDiagnose,
    handleRequestFromDrawer,
  }
}
