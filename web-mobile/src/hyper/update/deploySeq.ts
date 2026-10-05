// deploySeq.ts — 部署序号：定义与比较口径（docs/UI规范/18 §3/§4）。
//
// 三条禁止（18 §3 表）在本模块的对应实现：
//   - 不拿 /healthz 或任何不变端点当版本号（本模块只认 index.html 的 meta）；
//   - 不拿客户端时间戳与服务器时间戳比（只比两个 meta 字符串）；
//   - 远端读不到序号 ⇒ 「不可判定」，绝不折叠成「一致」。
//
// `/api/system/version` 与 `/version` 是**后端**版本（17 §5 状态条在用），
// 与前端构建序号是两个量，本模块不得读取它们（18 §3 本仓注）。

/** index.html 构建期写入的 meta 名（vite 插件注入，见 vite.config.ts）。 */
export const DEPLOY_SEQ_META = 'llmgw:deploy-seq'
export const DEPLOY_SEQ_SRC_META = 'llmgw:deploy-seq-src'

/** 周期轮询默认 5 分钟（18 §4 表）。 */
export const UPDATE_POLL_MS = 5 * 60_000

/** 首次检查必须晚于首屏落地、不与首屏数据抢网络（18 §4 表）。 */
export const UPDATE_FIRST_CHECK_DELAY_MS = 5_000

/**
 * 自动重载的静置阈值（18 §4：「距上次交互超过阈值」）。
 * 「页面已静置一段时间」与「距上次交互超过阈值」共用同一只时钟：
 * 交互 = pointerdown / touchstart / keydown（见 useDeploySeqUpdate）。
 */
export const UPDATE_IDLE_THRESHOLD_MS = 30_000

export interface LocalDeploySeq {
  seq: string
  src: string
}

/** 读本页 meta。缺失 ⇒ null（调用方按「不可判定」处理，不得默认一致）。 */
export function readLocalDeploySeq(doc: Document): LocalDeploySeq | null {
  const seq = doc.querySelector(`meta[name="${DEPLOY_SEQ_META}"]`)?.getAttribute('content')
  if (!seq) return null
  const src = doc.querySelector(`meta[name="${DEPLOY_SEQ_SRC_META}"]`)?.getAttribute('content')
  return { seq, src: src || 'unknown' }
}

/** 从远端 index.html 文本里抠同名 meta；缺失 ⇒ null。 */
export function parseDeploySeq(html: string): string | null {
  try {
    const parsed = new DOMParser().parseFromString(html, 'text/html')
    const v = parsed.querySelector(`meta[name="${DEPLOY_SEQ_META}"]`)?.getAttribute('content')
    return v ? v : null
  } catch {
    return null
  }
}

export type SeqRelation = 'latest' | 'available' | 'indeterminate'

/**
 * 比较本页序号与远端序号。null（任一侧读不到）一律「不可判定」——
 * 「我不知道」不得被显示成「我知道没问题」（18 §3 第三禁）。
 */
export function relateSeq(local: string | null, remote: string | null): SeqRelation {
  if (!local || !remote) return 'indeterminate'
  if (local === remote) return 'latest'
  return 'available'
}

/** 输入类控件判定：焦点在其中时禁止自动重载（18 §4 自动更新三条件之一）。 */
export function elementBlocksAutoReload(el: Element | null): boolean {
  if (!el) return false
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) {
    return true
  }
  // contenteditable 三信号取或：isContentEditable（浏览器计算值）、
  // contentEditable 属性（jsdom 只设属性不反射标记）、标记存在性（标记层同义）。
  const editable = el as HTMLElement
  if (editable.isContentEditable) return true
  if (editable.contentEditable === 'true' || editable.contentEditable === 'plaintext-only') return true
  const attr = el.getAttribute('contenteditable')
  return attr !== null && attr !== 'false'
}

export interface AutoReloadConditions {
  /** 焦点是否在输入控件上。 */
  hasInputFocus: boolean
  /** 距上次交互的毫秒数。 */
  msSinceInteraction: number
}

/**
 * 自动重载三条件（18 §4）：页面已静置 且 焦点不在输入 且 距上次交互超阈值。
 * 任一不满足 ⇒ 降级为提示条。「自动」与「不丢用户输入」之间的边界，不可省。
 */
export function shouldAutoReload(c: AutoReloadConditions, idleThresholdMs = UPDATE_IDLE_THRESHOLD_MS): boolean {
  if (c.hasInputFocus) return false
  return c.msSinceInteraction >= idleThresholdMs
}
