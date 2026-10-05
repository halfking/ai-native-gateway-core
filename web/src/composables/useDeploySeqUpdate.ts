/**
 * useDeploySeqUpdate.ts — 部署序号检查与更新提示（docs/UI规范/18 §3/§4，
 * §10.3 步骤 4：桌面侧只接 deploy-seq 检查，不接 SW）。
 *
 * 与 web-mobile/src/hyper/update/useDeploySeqUpdate.ts 同形（两项目独立
 * npm 包，无共享包目标；改造须双侧同步）。
 *
 * 三条禁止（18 §3 表）的实现落点：
 *   - 只认 index.html 的 llmgw:deploy-seq meta，不拿 /version 等后端端点当
 *     前端序号（那是另一个量，18 §3 本仓注）；
 *   - 不比时间戳，只比两个 meta 字符串；
 *   - 远端读不到 ⇒ 「不可判定」+原因，绝不折叠成「已是最新」。
 *
 * 时机契约（18 §4 表）：
 *   - 首查晚于首屏落地；周期 5 分钟；不可见暂停、回前台补查；
 *   - 序号变化且满足自动重载三条件（静置 + 无输入焦点 + 距上次交互超阈值）
 *     ⇒ reload；否则非阻塞提示条（DeploySeqUpdateBanner）。
 *   - 检查失败绝不打断页面。
 */
import { ref, readonly } from 'vue'

export const DEPLOY_SEQ_META = 'llmgw:deploy-seq'
export const DEPLOY_SEQ_SRC_META = 'llmgw:deploy-seq-src'

/** 周期轮询 5 分钟（18 §4 表）。 */
export const UPDATE_POLL_MS = 5 * 60_000
/** 首查延迟：晚于首屏落地，不与首屏数据抢网络。 */
export const UPDATE_FIRST_CHECK_DELAY_MS = 5_000
/** 自动重载静置阈值：距上次交互超过该值才允许 reload。 */
export const UPDATE_IDLE_THRESHOLD_MS = 30_000

export type UpdateCheckStatus = 'idle' | 'checking' | 'latest' | 'available' | 'indeterminate'
export type UpdateIndeterminateReason = '' | 'no-local' | 'no-remote' | 'network'

export interface LocalDeploySeq {
  seq: string
  src: string
}

export function readLocalDeploySeq(doc: Document): LocalDeploySeq | null {
  const seq = doc.querySelector(`meta[name="${DEPLOY_SEQ_META}"]`)?.getAttribute('content')
  if (!seq) return null
  const src = doc.querySelector(`meta[name="${DEPLOY_SEQ_SRC_META}"]`)?.getAttribute('content')
  return { seq, src: src || 'unknown' }
}

export function parseDeploySeq(html: string): string | null {
  try {
    const parsed = new DOMParser().parseFromString(html, 'text/html')
    const v = parsed.querySelector(`meta[name="${DEPLOY_SEQ_META}"]`)?.getAttribute('content')
    return v ? v : null
  } catch {
    return null
  }
}

export function relateSeq(local: string | null, remote: string | null): 'latest' | 'available' | 'indeterminate' {
  if (!local || !remote) return 'indeterminate'
  return local === remote ? 'latest' : 'available'
}

/** 输入类控件判定（含 contenteditable 三信号：计算值 / 属性 / 标记存在性）。 */
export function elementBlocksAutoReload(el: Element | null): boolean {
  if (!el) return false
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) {
    return true
  }
  const editable = el as HTMLElement
  if (editable.isContentEditable) return true
  if (editable.contentEditable === 'true' || editable.contentEditable === 'plaintext-only') return true
  const attr = el.getAttribute('contenteditable')
  return attr !== null && attr !== 'false'
}

export interface AutoReloadConditions {
  hasInputFocus: boolean
  msSinceInteraction: number
}

/** 自动重载三条件（18 §4）：任一不满足 ⇒ 降级为提示条。 */
export function shouldAutoReload(c: AutoReloadConditions, idleThresholdMs = UPDATE_IDLE_THRESHOLD_MS): boolean {
  if (c.hasInputFocus) return false
  return c.msSinceInteraction >= idleThresholdMs
}

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>

export interface UpdateRuntimeOptions {
  fetchImpl?: FetchLike
  doc?: Document
  now?: () => number
  reloadImpl?: () => void
  activeElementProvider?: () => Element | null
}

const INTERACTION_EVENTS: Array<keyof DocumentEventMap> = ['pointerdown', 'touchstart', 'keydown']

// 模块级单例：App.vue 挂载时 start() 一次，Banner / AccountSheet 共享状态。
const status = ref<UpdateCheckStatus>('idle')
const reason = ref<UpdateIndeterminateReason>('')
const bannerVisible = ref(false)
const checkCount = ref(0)
const lastCheckedAt = ref<number | null>(null)
const local = ref<LocalDeploySeq | null>(null)

let started = false
let pollTimer: ReturnType<typeof setInterval> | null = null
let firstTimer: ReturnType<typeof setTimeout> | null = null
let lastInteractionAt = 0
let fetchImpl: FetchLike = (u, i) => fetch(u, i)
let nowImpl: () => number = () => Date.now()
let reloadImpl: () => void = () => window.location.reload()
let getDoc: () => Document = () => document
let getActiveElement: () => Element | null = () => getDoc().activeElement

function ensureLocal(): void {
  if (local.value) return
  local.value = readLocalDeploySeq(getDoc())
  if (!local.value) {
    status.value = 'indeterminate'
    reason.value = 'no-local'
  }
}

async function runCheck(): Promise<void> {
  ensureLocal()
  if (!local.value) return
  status.value = 'checking'
  let remote: string | null = null
  try {
    // 绕过 HTTP 缓存（18 §3 末段），否则读到的是旧序号。
    // 桌面 SPA 入口文档是 /index.html（同源、零后端改动）。
    const res = await fetchImpl('/index.html', { cache: 'no-store' })
    if (!res.ok) throw new Error(`http ${res.status}`)
    remote = parseDeploySeq(await res.text())
  } catch {
    status.value = 'indeterminate'
    reason.value = 'network'
    checkCount.value += 1
    lastCheckedAt.value = nowImpl()
    return
  }
  checkCount.value += 1
  lastCheckedAt.value = nowImpl()
  const relation = relateSeq(local.value.seq, remote)
  if (relation === 'indeterminate') {
    status.value = 'indeterminate'
    reason.value = 'no-remote'
    return
  }
  reason.value = ''
  status.value = relation
  if (relation === 'available') {
    const cond = {
      hasInputFocus: elementBlocksAutoReload(getActiveElement()),
      msSinceInteraction: lastInteractionAt === 0 ? Number.POSITIVE_INFINITY : nowImpl() - lastInteractionAt,
    }
    if (shouldAutoReload(cond)) {
      reloadImpl()
      return
    }
    bannerVisible.value = true
  }
}

function onVisibility(): void {
  if (document.hidden) {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  } else {
    void runCheck()
    if (!pollTimer) pollTimer = setInterval(() => void runCheck(), UPDATE_POLL_MS)
  }
}

function onInteraction(): void {
  lastInteractionAt = nowImpl()
}

/** 幂等：App.vue 挂载时调用一次（登录/游客/已登录三态都检查）。 */
function startDeploySeqUpdate(options?: UpdateRuntimeOptions): void {
  if (started) return
  started = true
  if (options?.fetchImpl) fetchImpl = options.fetchImpl
  if (options?.now) nowImpl = options.now
  if (options?.reloadImpl) reloadImpl = options.reloadImpl
  if (options?.activeElementProvider) getActiveElement = options.activeElementProvider
  if (options?.doc) {
    const d = options.doc
    getDoc = () => d
  }
  for (const ev of INTERACTION_EVENTS) {
    document.addEventListener(ev, onInteraction, { passive: true, capture: true })
  }
  document.addEventListener('visibilitychange', onVisibility)
  firstTimer = setTimeout(() => {
    firstTimer = null
    if (!document.hidden) void runCheck()
  }, UPDATE_FIRST_CHECK_DELAY_MS)
  pollTimer = setInterval(() => void runCheck(), UPDATE_POLL_MS)
}

/** 测试专用：还原模块单例。 */
function resetDeploySeqUpdateForTest(): void {
  started = false
  if (pollTimer) clearInterval(pollTimer)
  if (firstTimer) clearTimeout(firstTimer)
  pollTimer = null
  firstTimer = null
  status.value = 'idle'
  reason.value = ''
  bannerVisible.value = false
  checkCount.value = 0
  lastCheckedAt.value = null
  local.value = null
  lastInteractionAt = 0
  fetchImpl = (u, i) => fetch(u, i)
  nowImpl = () => Date.now()
  reloadImpl = () => window.location.reload()
  getDoc = () => document
  getActiveElement = () => getDoc().activeElement
  for (const ev of INTERACTION_EVENTS) {
    document.removeEventListener(ev, onInteraction, { capture: true } as EventListenerOptions)
  }
  document.removeEventListener('visibilitychange', onVisibility)
}

export function useDeploySeqUpdate() {
  return {
    status: readonly(status),
    reason: readonly(reason),
    bannerVisible: readonly(bannerVisible),
    checkCount: readonly(checkCount),
    lastCheckedAt: readonly(lastCheckedAt),
    localSeq: readonly(local),
    start: startDeploySeqUpdate,
    checkNow: runCheck,
    dismissBanner(): void {
      bannerVisible.value = false
    },
    applyUpdate(): void {
      reloadImpl()
    },
    resetDeploySeqUpdateForTest,
  }
}
