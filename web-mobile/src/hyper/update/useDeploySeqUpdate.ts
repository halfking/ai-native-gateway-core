// useDeploySeqUpdate.ts — 部署序号检查与更新提示（docs/UI规范/18 §4）。
//
// 时机契约：
//   - 启动后空闲做第一次检查（UPDATE_FIRST_CHECK_DELAY_MS，晚于首屏落地）；
//   - 周期轮询 UPDATE_POLL_MS；页面不可见时暂停，回前台补查一次；
//   - 序号变化且满足自动重载三条件 ⇒ location.reload()；
//     否则出非阻塞提示条（AppUpdateBanner），用户可「立即更新 / 稍后」；
//   - 检查失败绝不打断页面：任何异常降级为「不可判定」，由 reason 承载原因。
import { ref, readonly } from 'vue'
import { appBase } from '@/utils/base'
import {
  UPDATE_FIRST_CHECK_DELAY_MS,
  UPDATE_POLL_MS,
  elementBlocksAutoReload,
  parseDeploySeq,
  readLocalDeploySeq,
  relateSeq,
  shouldAutoReload,
  type SeqRelation,
} from './deploySeq'

export type UpdateCheckStatus = 'idle' | 'checking' | SeqRelation

/** 不可判定原因（i18n 键尾段；空 = 非不可判定态）。 */
export type UpdateIndeterminateReason = '' | 'no-local' | 'no-remote' | 'network'

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>

interface UpdateRuntimeOptions {
  fetchImpl?: FetchLike
  /** 测试注入；缺省 document。 */
  doc?: Document
  /** 测试注入时钟（epoch ms）。 */
  now?: () => number
  /** 测试注入重载（jsdom 的 location.reload 不可 spy）。 */
  reloadImpl?: () => void
  /** 测试注入焦点元素（DOMParser 文档没有焦点语义，生产缺省 getDoc().activeElement）。 */
  activeElementProvider?: () => Element | null
}

/** 交互监听（自动重载三条件的「距上次交互」时钟）。 */
const INTERACTION_EVENTS: Array<keyof DocumentEventMap> = ['pointerdown', 'touchstart', 'keydown']

// 模块级单例：HyperApp 只 start() 一次，AccountSheet / Banner 共享同一份状态。
const status = ref<UpdateCheckStatus>('idle')
const reason = ref<UpdateIndeterminateReason>('')
const bannerVisible = ref(false)
const checkCount = ref(0)
const lastCheckedAt = ref<number | null>(null)
const local = ref<{ seq: string; src: string } | null>(null)

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
    // 本页没有序号 meta（构建期未注入）——如实进入不可判定，不得默认一致。
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
    // 检查请求必须绕过 HTTP 缓存（18 §3 末段），否则读到的是旧序号。
    const res = await fetchImpl(`${appBase()}index.html`, { cache: 'no-store' })
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
    // 回前台补查一次，再恢复轮询（18 §4 表「页面不可见时暂停」）。
    void runCheck()
    if (!pollTimer) pollTimer = setInterval(() => void runCheck(), UPDATE_POLL_MS)
  }
}

function onInteraction(): void {
  lastInteractionAt = nowImpl()
}

/** 幂等：HyperApp 挂载时调用一次。 */
function startUpdateCheck(options?: UpdateRuntimeOptions): void {
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

/** 测试专用：还原模块单例，避免用例间串扰。 */
function resetUpdateCheckForTest(): void {
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
    start: startUpdateCheck,
    checkNow: runCheck,
    dismissBanner(): void {
      bannerVisible.value = false
    },
    applyUpdate(): void {
      reloadImpl()
    },
    resetUpdateCheckForTest,
  }
}
