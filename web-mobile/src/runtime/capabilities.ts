// capabilities.ts — HyperCapabilities v2 合并 schema（UI 规范 17 §4-R3）。
// Web 端基线：无原生能力。不以 window.Shell 存在与否推能力；未知字段忽略、
// 未知能力默认不可用（14 §2）。原生壳轮接入时由壳注入覆盖本基线。

export interface HyperCapabilities {
  protocolVersion: 2
  platform: 'web' | 'ios' | 'android'
  navigation: boolean
  back: 'none' | 'commit-only' | 'interactive'
  focusWorkspace: boolean
  insets: 'css-only' | 'native-css-px'
  keyboard: 'viewport-only' | 'native'
  haptics: boolean
  appLifecycle: boolean
  tasks: {
    durableLocal: boolean
    cloudDetached: boolean
    continuation:
      | 'foregroundOnly'
      | 'bestEffort'
      | 'osScheduled'
      | 'activeAudio'
      | 'serverDurable'
  }
  recording: { available: boolean; background: boolean }
  recognition: { pdfText: boolean; ocr: 'none' | 'fast' | 'accurate'; asr: 'none' | 'fast' | 'accurate' }
  agent: { available: boolean; skillFormat: string }
}

export function webBaselineCapabilities(): HyperCapabilities {
  return {
    protocolVersion: 2,
    platform: 'web',
    // Web 端 BackDispatcher 可用（history popstate 仲裁）。
    navigation: true,
    // Web 容器拿不到预测返回 start/progress/cancel（11 §4），只有 commit。
    back: 'commit-only',
    // 专注工作区纯 DOM 覆盖层实现，不依赖 Fullscreen API。
    focusWorkspace: true,
    insets: 'css-only',
    keyboard: 'viewport-only',
    haptics: false,
    appLifecycle: false,
    tasks: { durableLocal: false, cloudDetached: false, continuation: 'foregroundOnly' },
    recording: { available: false, background: false },
    recognition: { pdfText: false, ocr: 'none', asr: 'none' },
    agent: { available: false, skillFormat: '' },
  }
}

const CAP_STATE_KEY = 'llmgw_mobile_capabilities'

let cached: HyperCapabilities | null = null

export function getCapabilities(): HyperCapabilities {
  if (cached) return cached
  const base = webBaselineCapabilities()
  // 宿主（原生壳轮）可注入 window.__HYPER_CAPABILITIES__ 覆盖基线；
  // 逐字段白名单合并，未知字段忽略，未知能力默认不可用。
  const injected = (globalThis as Record<string, unknown>)['__HYPER_CAPABILITIES__'] as
    | Partial<HyperCapabilities>
    | undefined
  cached = injected ? mergeCapabilities(base, injected) : base
  return cached
}

function mergeCapabilities(base: HyperCapabilities, patch: Partial<HyperCapabilities>): HyperCapabilities {
  const out: HyperCapabilities = { ...base }
  for (const key of Object.keys(base) as (keyof HyperCapabilities)[]) {
    const v = patch[key]
    if (v !== undefined && typeof v === typeof base[key]) {
      // 嵌套对象浅合并（tasks/recording/recognition/agent）。
      if (v !== null && typeof v === 'object' && !Array.isArray(v)) {
        // @ts-expect-error —— 白名单逐键浅合并，形状由 typeof 守卫约束
        out[key] = { ...(base[key] as object), ...(v as object) }
      } else {
        // @ts-expect-error 同上
        out[key] = v
      }
    }
  }
  return out
}

/** 测试钩子：重置缓存（生产不调用）。 */
export function resetCapabilitiesCache(): void {
  cached = null
  try {
    sessionStorage.removeItem(CAP_STATE_KEY)
  } catch {
    /* ignore */
  }
}
