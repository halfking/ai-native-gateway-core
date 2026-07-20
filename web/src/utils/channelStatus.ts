/** ConnectivityBadge — 实时通道状态显示组件（WS / HTTP 轮询 / 离线）。
 *
 * 决策基线 2=A：WS 优先，HTTP 轮询降级。本组件不直接管理 socket，
 * 只展示由调用方（通常是 useBootstrapChannel）维护的 channel 状态。
 */
import { defineComponent, type PropType } from 'vue'

export type ChannelStatus = 'ws' | 'http' | 'disconnected'

export const ConnectivityBadge = defineComponent({
  name: 'ConnectivityBadge',
  props: {
    status: {
      type: String as PropType<ChannelStatus>,
      required: true,
    },
    lastError: {
      type: String as PropType<string | null>,
      default: null,
    },
    pollSeconds: {
      type: Number,
      default: 30,
    },
  },
  computed: {
    label(): string {
      switch (this.status) {
        case 'ws':
          return '实时通道：WS'
        case 'http':
          return `实时通道：HTTP 轮询（${this.pollSeconds}s）`
        default:
          return '实时通道：离线'
      }
    },
    tone(): 'ok' | 'warn' | 'err' {
      if (this.status === 'ws') return 'ok'
      if (this.status === 'http') return 'warn'
      return 'err'
    },
  },
  template: `
    <span :class="['conn-badge', tone]" :title="lastError || label">
      <span class="dot" />
      <span class="text">{{ label }}</span>
    </span>
  `,
})

export default ConnectivityBadge