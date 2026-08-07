// useEmergencyDiagnostic — 应急诊断弹窗状态 composable
//
// 2026-07-13: 转发泳道诊断事件，承载 EmergencyDiagnosticModal 弹窗状态。
// 2026-08-06: 从 LiveRequestStreamV2.vue 抽取，纯 UI 状态（4 ref + 3 事件函数），零依赖。

import { ref } from 'vue'

export interface EmergencyDiagnosePayload {
  credentialId: number
  model: string
  laneName: string
}

export function useEmergencyDiagnostic() {
  const showEmergencyDiagnostic = ref(false)
  const emergencyCredentialId = ref(0)
  const emergencyModel = ref('')
  const emergencyLaneName = ref('')

  function handleEmergencyDiagnose(data: EmergencyDiagnosePayload) {
    emergencyCredentialId.value = data.credentialId
    emergencyModel.value = data.model
    emergencyLaneName.value = data.laneName
    showEmergencyDiagnostic.value = true
  }

  function handleEmergencyClose() {
    showEmergencyDiagnostic.value = false
  }

  function handleEmergencyRecovered() {
    // 恢复成功后，可以选择刷新泳道或显示通知
    console.log('Credential recovered successfully')
  }

  return {
    showEmergencyDiagnostic,
    emergencyCredentialId,
    emergencyModel,
    emergencyLaneName,
    handleEmergencyDiagnose,
    handleEmergencyClose,
    handleEmergencyRecovered,
  }
}
