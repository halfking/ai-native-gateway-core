import type { HyperCapabilities } from './types'

// capabilities 协商（UI规范 08 §3 + 14 §2 + 17 §4-R3）。
// 红线：存在 window.Shell / window.Capacitor 不证明任何能力（14 §2 四步验证）。
// Web 端基线恒定：无原生任务/录音/识别/Agent；返回能力为 commit-only
// （popstate 是通知不是可取消预览，11 §4）。

export function webCapabilities(): HyperCapabilities {
  return {
    protocolVersion: 2,
    platform: 'web',
    navigation: true,
    back: 'commit-only',
    focusWorkspace: true,
    insets: 'css-only',
    keyboard: 'viewport-only',
    haptics: false,
    appLifecycle: false,
    tasks: {
      durableLocal: false,
      cloudDetached: true,
      continuation: 'serverDurable',
    },
    recording: { available: false, background: false },
    recognition: { pdfText: false, ocr: 'none', asr: 'none' },
    agent: { available: false, skillFormat: 'declarative-v1' },
  }
}

/**
 * 能力协商入口。原生壳接入后在此追加桥探测（四步验证：导航留在指定
 * WebView → 桥可调用 → 插件已注册 → 监听真实抵达且能清理）；探测失败
 * 一律回落 webCapabilities()，不以平台名推断。
 */
export function resolveCapabilities(): HyperCapabilities {
  return webCapabilities()
}
