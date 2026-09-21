// useActionMessage.ts — 操作反馈条统一 composable（审计 R3 #10 UI 统一批次）。
//
// 背景：2026-09-09 审计确认 ≥8 处视图各自实现「成功/失败反馈条 + setTimeout
// 自动消失」：ApprovalListView / OutputComplianceView / SettingsTab /
// ApprovalConfigPanel / PromptInjectionConfigPanel / ModelsView(featured) /
// ProbeHealthPanel / SystemMonitorPanel 等，成功超时（3000/5000ms）、错误
// 是否自动消失、timer 清理（onUnmounted）各不相同。
//
// 共同形态（与 ErrorDetailTab 的 loading/error 展示一致）：
//   - `message`（成功）+ `error`（失败）两个 ref，模板按
//     `alert alert-success` / `alert alert-danger` 渲染；
//   - notifySuccess 默认 3s 自动清除（仓内主流值）；
//   - notifyError 默认驻留（绝大多数实现错误不自动消失），
//     需要自动消失的场景传 errorTimeoutMs；
//   - 成功与失败互斥：通知一侧清除另一侧，避免双条同时挂着；
//   - 组件卸载自动清 timer，消除散落的 clearTimeout 样板。
//
// 模板沿用各处现有变量名时用解构重命名即可，例如：
//   const { message: successMessage, error, notifySuccess, notifyError } = useActionMessage()

import { getCurrentScope, onScopeDispose, ref } from 'vue'

export interface UseActionMessageOptions {
  /** 成功提示自动清除毫秒数，默认 3000（仓内主流值）。0 = 驻留。 */
  successTimeoutMs?: number
  /** 错误提示自动清除毫秒数，默认 0 = 驻留（与大多数既有实现一致）。 */
  errorTimeoutMs?: number
}

export interface UseActionMessageReturn {
  /** 成功反馈文案；模板渲染为 alert alert-success。 */
  message: ReturnType<typeof ref<string>>
  /** 失败反馈文案；模板渲染为 alert alert-danger。 */
  error: ReturnType<typeof ref<string>>
  /** 展示成功反馈（清除错误反馈 + 重置自动清除计时）。 */
  notifySuccess: (text: string) => void
  /** 展示失败反馈（清除成功反馈 + 按配置计时清除）。 */
  notifyError: (text: string) => void
  /** 立即清除成功反馈（消息条 × 关闭按钮用）。 */
  clearMessage: () => void
  /** 立即清除失败反馈（消息条 × 关闭按钮用）。 */
  clearError: () => void
  /** 立即清空两侧反馈（进入操作前重置用）。 */
  clear: () => void
}

export function useActionMessage(options: UseActionMessageOptions = {}): UseActionMessageReturn {
  const successTimeoutMs = options.successTimeoutMs ?? 3000
  const errorTimeoutMs = options.errorTimeoutMs ?? 0

  const message = ref('')
  const error = ref('')
  let successTimer: ReturnType<typeof setTimeout> | null = null
  let errorTimer: ReturnType<typeof setTimeout> | null = null

  function clearSuccessTimer() {
    if (successTimer !== null) {
      clearTimeout(successTimer)
      successTimer = null
    }
  }
  function clearErrorTimer() {
    if (errorTimer !== null) {
      clearTimeout(errorTimer)
      errorTimer = null
    }
  }

  function notifySuccess(text: string) {
    clearSuccessTimer()
    clearErrorTimer()
    error.value = ''
    message.value = text
    if (successTimeoutMs > 0) {
      successTimer = setTimeout(() => {
        message.value = ''
        successTimer = null
      }, successTimeoutMs)
    }
  }

  function notifyError(text: string) {
    clearSuccessTimer()
    clearErrorTimer()
    message.value = ''
    error.value = text
    if (errorTimeoutMs > 0) {
      errorTimer = setTimeout(() => {
        error.value = ''
        errorTimer = null
      }, errorTimeoutMs)
    }
  }

  function clearMessage() {
    clearSuccessTimer()
    message.value = ''
  }

  function clearError() {
    clearErrorTimer()
    error.value = ''
  }

  function clear() {
    clearSuccessTimer()
    clearErrorTimer()
    message.value = ''
    error.value = ''
  }

  // 组件卸载（或 effect scope 销毁）时清 timer；测试等无 scope 环境跳过。
  if (getCurrentScope()) onScopeDispose(clear)

  return { message, error, notifySuccess, notifyError, clearMessage, clearError, clear }
}
