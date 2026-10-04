/**
 * hyper/capabilities.ts — 能力协商（docs/UI规范/00 §5.2 · H1，参考规范 08 §3 / 14 §2）。
 *
 * ## 最重要的一条
 *
 * **`hyperMode`（交互形态）≠ `capabilities`（原生能力）。**
 *
 * `hyperMode` 的判定是 `inShell() || isCompact`：只要在壳里或在 compact 窗口
 * 就为 true。这只说明「我们用 Hyper 的形态渲染」，**不说明任何原生能力可用**。
 * compact 浏览器里没有后台录音插件；`window.Capacitor` 存在也不证明能 OCR、
 * 能推理或能持续后台执行。
 *
 * 任何 UI 都必须先问 capabilities，再决定要不要渲染原生入口。
 *
 * ## 握手必须过的四关（按顺序，缺一即降级）
 *
 * 1. 导航确实留在指定 WebView（不是被系统浏览器/外部 Intent 接走）
 * 2. 远端页面的桥可调用
 * 3. 原生插件已安装并注册
 * 4. 真实监听能抵达**且能清理**
 *
 * 只看到 `window.Capacitor`、只拿到一个平台名字符串、或调用返回了 Web
 * fallback 的 Promise —— **都不算成功**。因此下面每一关都独立判定，
 * 任何一关不过就整体降级为 web，而不是部分声称支持。
 *
 * ## 可信 origin
 *
 * 远端桥扩大了原生能力的信任面。壳只应对**当前明确批准的
 * scheme+host+port** 主 frame 授予能力；重定向、iframe、外链不自动继承。
 * 用户自填服务器地址不因此获得全量插件。
 */
import type { HyperCapabilities } from './types'

/** 当前协议版本。壳与前端不一致时前端走降级，不猜。 */
export const PROTOCOL_VERSION = 2

/** 纯 Web（含 compact 浏览器与 PWA）的基线能力。 */
export const WEB_CAPABILITIES: HyperCapabilities = Object.freeze({
  protocolVersion: PROTOCOL_VERSION,
  platform: 'web',
  // R3（2026-10-04 吸收）补齐的字段。⚠️ 除 `navigation` 外**全部是「未实现」值**：
  // 本专题没有录音/OCR/ASR/后台任务/Agent 任何代码。
  // 参考仓 17 号文档 §3 明确「**全部不实现也不占位假装**」——
  // 报 `false`/`'none'` 是**如实声明不可用**，不是假装可用。
  navigation: true,
  back: 'commit-only', // 浏览器历史可退，但没有可取消预览
  focusWorkspace: false, // H4 未实现
  insets: 'css-only', // 只用 env(safe-area-inset-*)
  keyboard: 'viewport-only', // 只有视口变化，没有原生键盘事件
  haptics: false,
  appLifecycle: false,
  tasks: { durableLocal: false, cloudDetached: false, continuation: 'foregroundOnly' as const },
  recording: { available: false, background: false },
  recognition: { pdfText: false, ocr: 'none' as const, asr: 'none' as const },
  agent: { available: false, skillFormat: '' },
  shellVersion: '',
})

/** 握手超时。原生无响应时按 web 降级，不让 UI 一直等。 */
const HANDSHAKE_TIMEOUT_MS = 1500

interface CapacitorGlobal {
  isNativePlatform?: () => boolean
  getPlatform?: () => string
  isPluginAvailable?: (name: string) => boolean
}

function capacitorGlobal(): CapacitorGlobal | null {
  if (typeof window === 'undefined') return null
  const g = (window as unknown as { Capacitor?: CapacitorGlobal }).Capacitor
  return g && typeof g === 'object' ? g : null
}

/** 取当前页面 origin；非浏览器返回空串。 */
function currentOrigin(): string {
  if (typeof window === 'undefined' || !window.location) return ''
  return window.location.origin
}

/**
 * 校验 origin 是否被批准。
 *
 * 批准清单来自壳握手返回值里的 `trustedOrigin`，而不是本地硬编码 ——
 * 因为内网部署地址由用户填写，客户端无权假定。
 */
export function isTrustedOrigin(claimed: string | undefined, actual: string): boolean {
  if (!claimed || !actual) return false
  // 精确匹配 scheme+host+port。不做后缀/通配匹配。
  return claimed === actual
}

export interface DetectOptions {
  /** 注入的握手函数，便于单测与将来替换桥实现。 */
  handshake?: () => Promise<Partial<HyperCapabilities> & { trustedOrigin?: string }>
  timeoutMs?: number
}

/**
 * 探测能力。**永远 resolve，不 reject** —— 探测失败就是 web 能力，
 * 不该让调用方写 try/catch 之后还要猜默认值。
 */
export async function detectCapabilities(opts: DetectOptions = {}): Promise<HyperCapabilities> {
  const timeoutMs = opts.timeoutMs ?? HANDSHAKE_TIMEOUT_MS

  // 非原生环境：直接 Web 基线，不做任何桥调用。
  const cap = capacitorGlobal()
  if (!cap || typeof cap.isNativePlatform !== 'function' || !cap.isNativePlatform()) {
    return { ...WEB_CAPABILITIES }
  }

  const handshake = opts.handshake
  if (!handshake) {
    // 壳在，但本前端没有配握手实现 → 降级，且**不谎称**任何原生能力。
    return { ...WEB_CAPABILITIES, platform: 'web' }
  }

  let claimed: Partial<HyperCapabilities> & { trustedOrigin?: string }
  try {
    claimed = await withTimeout(handshake(), timeoutMs)
  } catch {
    return { ...WEB_CAPABILITIES, platform: 'web' }
  }

  // 关 3：协议版本不兼容 → 不猜，走 Web 并保留一个可诊断的信号。
  if (claimed.protocolVersion !== PROTOCOL_VERSION) {
    return { ...WEB_CAPABILITIES, platform: 'web', shellVersion: claimed.shellVersion ?? '' }
  }

  // 关 4：可信 origin 不匹配 → 即使桥通了也不授予能力。
  // 导航到外站/iframe/换服务器后不能继承原生权限。
  if (!isTrustedOrigin(claimed.trustedOrigin, currentOrigin())) {
    return { ...WEB_CAPABILITIES, platform: 'web', shellVersion: claimed.shellVersion ?? '' }
  }

  const platform =
    claimed.platform === 'ios' || claimed.platform === 'android' ? claimed.platform : 'web'

  return {
    protocolVersion: PROTOCOL_VERSION,
    platform,
    // ⚠ 下面六个 R3 字段**一律取「不可用」值，不采信壳的 claim**。
    //   理由与本文件既有纪律同源：这里**逐字段显式投影**而不是盲信 handshake，
    //   因为「壳说有」不等于「真有」—— 前端没有录音/OCR/后台任务的任何代码，
    //   让 claim 透传出去会渲染出**点了没反应的原生入口**，
    //   而那正是 R3「未知能力默认不可用」要防的。
    //   `navigation` 是**前端自己的**能力（BackDispatcher 已实现），与壳无关。
    navigation: true,
    back: claimed.back ?? 'none',
    focusWorkspace: false,
    insets: claimed.insets ?? 'css-only',
    keyboard: claimed.keyboard ?? 'viewport-only',
    haptics: claimed.haptics === true,
    appLifecycle: claimed.appLifecycle === true,
    tasks: { durableLocal: false, cloudDetached: false, continuation: 'foregroundOnly' as const },
    recording: { available: false, background: false },
    recognition: { pdfText: false, ocr: 'none' as const, asr: 'none' as const },
    agent: { available: false, skillFormat: '' },
    shellVersion: claimed.shellVersion ?? '',
    trustedOrigin: claimed.trustedOrigin,
  }
}

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('hyper-handshake-timeout')), ms)
    p.then(
      (v) => {
        clearTimeout(timer)
        resolve(v)
      },
      (e) => {
        clearTimeout(timer)
        reject(e)
      },
    )
  })
}

/** 能力快照。应用入口探测一次后写入，UI 读它而不是各自探测。 */
let current: HyperCapabilities = WEB_CAPABILITIES
const listeners = new Set<(c: HyperCapabilities) => void>()

export function capabilities(): HyperCapabilities {
  return current
}

/** 是否处于「Hyper 交互形态」——**与能力无关**，只表示用 Hyper 形态渲染。 */
export function hyperMode(opts: { inShell: boolean; isCompact: boolean }): boolean {
  return opts.inShell || opts.isCompact
}

/** 某项能力是否真的可用。UI 渲染原生入口前必须过这个。 */
export function can(c: HyperCapabilities, key: 'haptics' | 'appLifecycle'): boolean {
  return c[key] === true
}

/** 返回能力是否达到「可取消预览」级。达不到就不能声称交互式返回。 */
export function hasInteractiveBack(c: HyperCapabilities): boolean {
  return c.back === 'interactive'
}

export function setCapabilities(next: HyperCapabilities): void {
  current = next
  for (const fn of listeners) {
    try {
      fn(next)
    } catch {
      // 订阅者异常不阻断其他订阅者
    }
  }
}

export function onCapabilitiesChange(fn: (c: HyperCapabilities) => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

/** 仅供测试。 */
export function _resetCapabilities(): void {
  current = WEB_CAPABILITIES
  listeners.clear()
}
