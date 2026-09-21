// useConfirmDialog.ts — 全站统一确认交互（2026-09-04）
//
// 背景：确认交互曾有 4 种实现并存：
//   1. 原生 confirm() / window.confirm()（阻塞式、不可样式化、按钮文案不可翻译）
//   2. ElMessageBox.confirm(...) 直调（各处自带 title / 按钮文案）
//   3. NodeDetailEmergencyPanel.vue 内联自制 confirm（pending 状态 + 模板按钮）
//   4. RouteIncidentDrawer.vue 的 action modal（reason + token 表单，属操作表单，
//      非纯确认，保留原实现）
// 本 composable 收敛 1/2/3 为唯一入口：基于 ElMessageBox 的 Promise<boolean>
// 确认框，默认 title / 按钮文案走 i18n（common.confirmTitle / common.confirm /
// common.cancel）。
//
// 用法：
//   const { confirmDialog } = useConfirmDialog()
//   if (!(await confirmDialog(`确认删除 ${name}？`))) return
//   // 或带选项：await confirmDialog(msg, { title, type: 'error', confirmButtonText })

import { ElMessageBox } from 'element-plus'
import { i18n } from '../i18n'

export interface ConfirmDialogOptions {
  /** 弹窗标题；默认 t('common.confirmTitle') */
  title?: string
  /** 确认按钮文案；默认 t('common.confirm') */
  confirmButtonText?: string
  /** 取消按钮文案；默认 t('common.cancel') */
  cancelButtonText?: string
  /** 图标类型；默认 'warning' */
  type?: 'success' | 'warning' | 'info' | 'error'
}

/**
 * 弹出统一确认框。resolve true=确认，false=取消/关闭/Esc。
 * 不会 throw（ElMessageBox 的 reject 被吞掉并归一化为 false）。
 */
export async function confirmDialog(
  message: string,
  options: ConfirmDialogOptions = {},
): Promise<boolean> {
  const t = i18n.global.t.bind(i18n.global)
  try {
    await ElMessageBox.confirm(message, options.title ?? t('common.confirmTitle'), {
      confirmButtonText: options.confirmButtonText ?? t('common.confirm'),
      cancelButtonText: options.cancelButtonText ?? t('common.cancel'),
      type: options.type ?? 'warning',
    })
    return true
  } catch {
    // cancel / close / Esc 都视为「不确认」
    return false
  }
}

/** 组合式入口（与项目 use* 命名约定对齐；返回的函数可在组件任意位置调用）。 */
export function useConfirmDialog() {
  return { confirmDialog }
}
