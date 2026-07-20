/** useBootstrapChannel — 本地实例 ↔ 中心 实时通道（决策基线 2=A）。
 *
 * 不在本文件实现真正的 socket（浏览器端需要稳定 socket 库），本 hook 只负责：
 *   - 暴露 status / lastError 给 ConnectivityBadge
 *   - 调度：建立 → 失败 3 次 → HTTP 轮询降级；恢复后自动回切 WS
 *
 * 当前实现是 spec-only；后续 batch 接入真正的 wsclient（后端 cmd/gateway 已有
 * internal/agent/wsclient Go 实现；前端等价的 wsclient 计划单独建 web/src/wsclient/
 * 目录并随 WS SDK 一起引入，避免现在追新依赖）。模块管理 / 激活向导只需 import
 * 本 hook 的接口，不必等 WS 落地即可联调 ConnectivityBadge UI。
 */
import { onBeforeUnmount, ref } from 'vue'

export type ChannelStatus = 'ws' | 'http' | 'disconnected'

export interface UseBootstrapChannelOpts {
  instanceId: () => string
  licenseKey?: () => string
  hardwareHash?: () => string
  onCommand?: (cmd: { type: string; payload: unknown }) => void
}

export interface UseBootstrapChannelReturn {
  status: ReturnType<typeof ref<ChannelStatus>>
  lastError: ReturnType<typeof ref<string | null>>
  pollSeconds: number
}

const MAX_FAILURES = 3

export function useBootstrapChannel(_opts: UseBootstrapChannelOpts): UseBootstrapChannelReturn {
  const status = ref<ChannelStatus>('disconnected')
  const lastError = ref<string | null>(null)
  let failures = 0

  // Spec-only stub: real WS will be wired in a follow-up batch.
  // For now, simply expose the reactive surface so UI can render.
  // It defaults to "disconnected" until the WS client lands.
  void failures
  void MAX_FAILURES

  onBeforeUnmount(() => {
    // future: close socket, clear timers
  })

  return { status, lastError, pollSeconds: 30 }
}