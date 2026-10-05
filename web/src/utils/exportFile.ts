/**
 * exportFile — 移动优先的文件导出降级链（docs/UI规范/19 §3.1）。
 *
 * 链条顺序固定、触发时判定（不在构建时判定）——同一按钮桌面走 ③、移动
 * 浏览器走 ①、壳里走 ②，代码路径唯一：
 *
 *   ① navigator.canShare({files}) → navigator.share({files}) 唤起系统分享面
 *      （含「存储到文件」）；用户取消（AbortError）= 'cancelled'，不算失败。
 *   ② 壳下载桥（setFileExportBridge 注册，capabilities 判定宿主——禁止 UA
 *      嗅探，19 §6 规则 1）。⚠ 当前无人注册：04 §7.1 桥注入决策未落地前，
 *      壳内桥能力一律按不可用降级（19 §6 规则 3）。本臂保留接线位，
 *      决策落地后在壳装配处注册。
 *   ③ 兜底 blob + a[download] 四步（append→click→remove→revoke，仓内既有
 *      正确形态）。桌面 canShare 为 false 天然回落本臂——九处调用点改造后
 *      桌面行为零回归（19 §3.1 规则 2）。
 *   ④ 全部失败 ⇒ 抛 ExportChannelError，由调用方如实展示——禁止静默
 *      （「按钮点了没反应」与死按钮同罪，19 §3.1 规则 4）。
 *
 * D-5 源码门禁：新增下载工具不得只写 a[download] 单通道——见
 * exportFile.gate.test.ts（createObjectURL 只允许出现在本文件）。
 */

export interface ExportFileInput {
  filename: string
  blob: Blob
}

export type ExportOutcome = 'shared' | 'downloaded' | 'cancelled'

/** 链条走到头仍失败（④）。message 为技术性英文，展示层走 i18n 文案。 */
export class ExportChannelError extends Error {
  constructor(message = 'export failed: no usable channel in this host') {
    super(message)
    this.name = 'ExportChannelError'
  }
}

/** 壳下载桥：返回 true 表示已由桥消费。注册权在壳装配层（当前未接线）。 */
export type FileExportBridge = (input: ExportFileInput) => Promise<boolean>

let bridge: FileExportBridge | null = null

export function setFileExportBridge(next: FileExportBridge | null): void {
  bridge = next
}

function buildShareFile(input: ExportFileInput): File | null {
  try {
    return new File([input.blob], input.filename, { type: input.blob.type || 'application/octet-stream' })
  } catch {
    // File 构造器不可用（极旧宿主）——①直接判不可用
    return null
  }
}

/** ①系统分享面。返回 null 表示该臂不可用/被跳过。 */
async function tryShare(input: ExportFileInput): Promise<ExportOutcome | null> {
  if (typeof navigator === 'undefined' || typeof navigator.canShare !== 'function') return null
  const file = buildShareFile(input)
  if (!file) return null
  let shareable = false
  try {
    shareable = navigator.canShare({ files: [file] })
  } catch {
    return null
  }
  if (!shareable) return null
  try {
    await navigator.share({ files: [file] })
    return 'shared'
  } catch (e: unknown) {
    // 用户取消不是通道失败——链条到此为止，不再兜底（避免取消后又弹下载）。
    if (e instanceof DOMException && (e.name === 'AbortError' || e.name === 'NotAllowedError')) {
      return e.name === 'AbortError' ? 'cancelled' : null
    }
    // NotAllowedError 之外还有手势过期等形态——按通道失败降级 ③。
    return null
  }
}

/** ③兜底 blob + a[download] 四步（仓内既有正确形态，唯一实现点）。 */
function downloadViaAnchor(input: ExportFileInput): void {
  const url = URL.createObjectURL(input.blob)
  const a = document.createElement('a')
  a.href = url
  a.download = input.filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

export async function exportFile(input: ExportFileInput): Promise<ExportOutcome> {
  // ① 分享面（移动浏览器/iOS 15+/Android Chrome）
  const shared = await tryShare(input)
  if (shared !== null) return shared

  // ② 壳下载桥（当前未注册——见文件头说明，按不可用降级）
  if (bridge) {
    const handled = await bridge(input).catch(() => false)
    if (handled) return 'shared'
  }

  // ③ blob 兜底（桌面与新版移动浏览器）
  if (typeof document === 'undefined' || typeof URL?.createObjectURL !== 'function') {
    throw new ExportChannelError()
  }
  downloadViaAnchor(input)
  return 'downloaded'
}
