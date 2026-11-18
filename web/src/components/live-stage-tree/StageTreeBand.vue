<script setup lang="ts">
// StageTreeBand.vue —— 请求阶段树的**单条请求带**（2026-11-17 设计对齐轮改版）
//
// 形态：**左右向**（owner 2026-10-16 原话「左右节点，最左是每个请求」）。
//   发送轨（上行）：客户端 → 鉴权/限流 → 脱敏 → 审批 → 注入检测 → 路由
//                 → 压缩 → 供应商/令牌池
//   接收轨（下行）：供应商 → 首字节 → 脱敏回填 → 计量 → 回复 → 回到客户端
//
// ★ 本轮（2026-11-17）按设计方案对齐的六处：
//   ① **时间列**（每带最左）—— 设计第 8 轮：一眼看出请求先后。
//   ② **客户端节点三行**（类型徽标 + 名称 + 模型入口）—— 设计第 5 轮：
//      客户端要什么（模型入口）与最终用了什么（最终匹配模型）分列两端。
//   ③ **阶段节点有值**（不再只有名字）—— 设计第 5 轮：每段显示关键值
//      （已通过 / 未命中 / N 处已遮蔽 / 候选数 / 模型名…）。
//   ④ **摘要行补齐**（状态 / 错误 / 会话轮次 / 模型链 / 排队 / 耗时 /
//      token / 成本 / 缓存 / 可信度 / 供应商）—— 设计第 5+8+9 轮。
//   ⑤ **紧凑模式单行**（11 段瓦片 + 最右**固定错误列**）—— 设计第 6/7/9 轮：
//      「在最右边显示错误源头及类型」⇒ 做成**列**（右缘恒定，能竖着扫），
//      不是挂在内容流末尾（每行位置都不同，扫不动）。
//   ⑥ **七态字形**（完成 ✓ / 进行中 ◐ / 未到达 ○ / 跳过 ⌀ / 失败 ✕
//      / 告警 ⚠ / 未测量 ⋯）—— 设计图例里的七枚字形。
//
// ★ 阶段顺序**以代码真实管线为准**（设计主档 §5.2）：
//   · 脱敏在**审批之前**（脱敏是中间件，包住 serveHTTPInner）；
//   · 压缩在**路由之后**（会话压缩器 Prepare 明确 runs AFTER candidate resolution）；
//   · 还原在**转发之前**（tools 还原与压缩同块，上游转发在其后）。
//
// ★ 七态不可合并：
//   · `unmeasured`（未测量）＝ 这一段**没有实时信号**；
//   · `pending`（未到达）  ＝ 这一段**还没跑到**。
//   两者都灰，但**字形与文案不同** —— 合并成一个灰态，评审者会把「没数据」
//   读成「系统没做这一步」，那是两种相反的结论。
//
// ★ 失败定位：失败段自己变红，**下游一律显示「未到达」**（不是「也失败」）。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Monitor } from '@element-plus/icons-vue'
import { RECV_STAGES, SEND_STAGES, type StageKey, type StageNode, type StageState } from '../../types/liveStageTree'
import { COMPACT_GLYPH, FULL_GLYPH } from '../../lib/stageTreeGlyphs'
// ★ 文案映射**单一真源**（请求带与请求详情面板共用一份 ⇒ 不会漂）。
import {
  clientTypeLabel as clientTypeLabelOf,
  errorKindLabel,
  stageDetailLabel,
  stageLabel,
  stageStateLabel,
  stageValueLabel,
} from '../../lib/liveStageTreeLabels'
// ★ 阶段图标与紧凑模式字形**复用仓内既有常量**（web/src/types/stageTree.ts）——
//   那份常量就是设计原型 ICON / CGLYPH 的移植 ⇒ 画面与原型一致，且不重复维护。
import { STAGE_ICONS } from '@/types/stageTree'
import {
  COMPACT_ERROR_COLUMN,
  COMPACT_GRID_COLUMNS,
  COMPACT_RIGHT_COLUMN,
  COMPACT_TILE_COLUMN,
  COMPACT_TREE_WIDTH,
  FULL_GRID_COLUMNS,
  RECV_COLUMN,
  SEND_COLUMN,
} from './stageTreeLayout'

const props = defineProps<{
  /** 显示模式（完整 / 紧凑）。 */
  mode: 'full' | 'compact'
  /** 请求 id（用于 data-testid 定位单条带）。 */
  requestId: string
  /** 该带的阶段状态（按 ALL_STAGES 顺序）。 */
  stages: StageNode[]
  /** 客户端显示名。 */
  clientName: string
  /** 客户端类型（原始值；人话标签在组件里按字面量 t() 取）。 */
  clientType: string
  /** 客户端协议。 */
  clientProtocol: string
  /** 是否 auto 请求（客户端要的是 auto）。 */
  isAuto: boolean
  /** 客户端侧显示的模型入口（auto → 最终模型 / 请求 <名>）。 */
  modelEntry: string
  /** 最终匹配模型（标准模型名）。空串 = 未测量。 */
  finalModel: string
  /** 最终匹配模型的来源说明（tooltip）。 */
  finalModelTip: string
  /** 上游实际模型串（与最终模型不同时才显示）。 */
  upstreamModel: string
  /** 模型解析链说明（auto 任务类型 / 别名解析 / 模型切换）。 */
  modelChain: string
  /** 指纹可信度 chip 文案。 */
  credibilityLabel: string
  /** 指纹可信度 tooltip。 */
  credibilityTip: string
  /** 指纹可信度 chip 形态。 */
  credibilityTone: 'verified' | 'suspected' | 'diluted' | 'unknown' | 'na'
  /** 供应商 / 凭据显示名。 */
  providerLabel: string
  /** 状态徽标文案键后缀（success/in_progress/failure/…）。 */
  status: string
  /** 失败原因码（error_kind）。 */
  errorKind: string
  /** 错误来源（网关 / 供应商 / 其它）；空串 = 无失败。 */
  errorSource: 'gateway' | 'provider' | 'other' | ''
  /** 失败发生在哪一段（无失败 = null）。 */
  failedStage: StageKey | null
  /** 总耗时（毫秒）。未测量 = null。 */
  latencyMs: number | null
  /** token 合计。未测量 = null。 */
  totalTokens: number | null
  /** 输入 token。未测量 = null。 */
  promptTokens: number | null
  /** 输出 token。未测量 = null。 */
  completionTokens: number | null
  /** 成本（USD）。未测量 = null。 */
  costUsd: number | null
  /** 缓存命中率文案（已由纯函数层算好口径）。 */
  cacheLabel: string
  /** 缓存命中率的 tooltip（口径说明）。 */
  cacheTip: string
  /** 缓存命中率的 chip 形态：hit / na / bad。 */
  cacheTone: 'hit' | 'na' | 'bad'
  /** 客户端时间戳（毫秒）—— 时间列。 */
  tsMs: number
  /** 会话内的轮次（1 = 首轮）。未测量 = null。 */
  sessionRound: number | null
  /** 排队时长（毫秒）。未测量 = null。 */
  queueMs: number | null
  /** 出站尝试次数（0 = 还没出站）。 */
  attempts: number
  /** 候选数（路由段上报的候选个数）。未上报 = null。 */
  candidates: number | null
  /** 该带是否已展开详情（点击请求行展开；手风琴：同时只展开一条）。 */
  expanded: boolean
}>()

const emit = defineEmits<{
  /** 点击请求行 ⇒ 请求展开/收起详情（带 requestId 供上层做手风琴）。 */
  toggle: [requestId: string]
}>()

const { t } = useI18n()

/** 点击请求行 ⇒ 通知上层展开/收起本条详情（手风琴由上层统一管）。 */
function onToggle(): void {
  emit('toggle', props.requestId)
}

/** 发送轨节点（列 3..9）。 */
const sendStages = computed(() =>
  props.stages.filter(s => SEND_STAGES.some(d => d.key === s.key)),
)
/** 接收轨节点（按 DOM 顺序＝流向：首字节→还原→计量→回复）。 */
const recvStages = computed(() =>
  props.stages.filter(s => RECV_STAGES.some(d => d.key === s.key)),
)

/** 网格列宽（完整 / 紧凑两套）。 */
const gridColumns = computed(() =>
  props.mode === 'compact' ? COMPACT_GRID_COLUMNS : FULL_GRID_COLUMNS,
)

/** 阶段名（★ 逐字面量 t()：动态键是引用扫描器的盲区，且写错键没有任何门会报）。 */
function stageName(key: StageKey): string {
  return stageLabel(t, key)
}

/** 阶段状态文案（逐字面量）。 */
function stateName(state: string): string {
  return stageStateLabel(t, state)
}

/** 阶段值的中文化（单位随阶段而变；空值不渲染 —— 状态字形已经说明了）。 */
function valueText(s: StageNode): string {
  return stageValueLabel(t, s)
}

/** 阶段节点的 tooltip：状态 · 值 · 补充 · 耗时。 */
function stageTitle(s: StageNode): string {
  const parts = [stateName(s.state)]
  const v = valueText(s)
  if (v) parts.push(v)
  const detail = s.key === 'compress' ? compressDetail(s) : s.detail
  if (detail && s.key !== 'injection') parts.push(detail)
  // ★ 注入检测：节点上是短判定词，tooltip 里给完整措辞 + 得分（可核对）。
  if (s.key === 'injection') {
    if (s.detail === 'warn' || s.detail === 'block') parts.push(t('liveStageTree.decision.warn'))
    if (s.value) parts.push(t('liveStageTree.value.injection', { n: s.value }))
  }
  if (s.durMs != null) parts.push(`${s.durMs}ms`)
  // ★ 路由段：候选数不在主值上（主值＝最终匹配模型）⇒ tooltip 里补出来。
  if (s.key === 'route' && s.candidates != null) {
    parts.push(t('liveStageTree.value.route', { n: s.candidates }))
  }
  return parts.join(' · ')
}

/** 压缩节点的补充说明：tools 还原徽标本地化（「tools 回填 n」）。 */
function compressDetail(s: StageNode): string {
  return stageDetailLabel(t, s)
}

/** 阶段节点的展示字形（七态各有各的形，灰态之间靠这个区分）。
 *  ★ 字形来自**唯一真源** `lib/stageTreeGlyphs.ts`（与图例共用）——
 *    完整模式用 FULL_GLYPH；紧凑模式另有 COMPACT_GLYPH（见 compactGlyph）。
 *    ★ 2026-11-18：本函数原来自己 switch 了一份字形 ⇒ 与图例形成【两份真源】
 *      ⇒ 同一条规定写两遍必然漂移（线上缺陷 F1 的根因之一）。 */
function stageGlyph(s: StageNode): string {
  return FULL_GLYPH[s.state] ?? ''
}

/** 时间列（HH:MM:SS，本地时区）。 */
const timeText = computed(() => {
  if (!props.tsMs) return '—'
  const d = new Date(props.tsMs)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
})

/** 客户端类型的人话标签（★ 逐字面量 t()；未知类型回落原始值）。 */
function clientTypeLabel(): string {
  return clientTypeLabelOf(t, props.clientType)
}

/**
 * 客户端节点的模型入口行：**协议 · 模型入口**（设计第 5 轮：
 * 「客户端要的是 auto → 最终匹配模型」与「协议」并排）。
 */
const clientModelLine = computed(() => {
  const proto = props.clientProtocol || ''
  const entry = props.modelEntry || ''
  return proto ? `${proto} · ${entry}` : entry
})

/** 会话轮次文案（首轮 / 第 N 轮）。 */
const sessionText = computed(() => {
  if (props.sessionRound == null) return ''
  return props.sessionRound <= 1
    ? t('liveStageTree.session.first')
    : t('liveStageTree.session.round', { n: props.sessionRound })
})

/** 错误来源标签（GW 网关 / UP 供应商 / 其它）。 */
const errorTag = computed(() => {
  if (props.errorSource === 'gateway') return t('liveStageTree.error.tag.gateway')
  if (props.errorSource === 'provider') return t('liveStageTree.error.tag.provider')
  if (props.errorSource === 'other') return t('liveStageTree.error.tag.other')
  return ''
})

/** 错误来源的人话名（tooltip 用）。 */
const errorSourceName = computed(() => {
  if (props.errorSource === 'gateway') return t('liveStageTree.error.source.gateway')
  if (props.errorSource === 'provider') return t('liveStageTree.error.source.provider')
  if (props.errorSource === 'other') return t('liveStageTree.error.source.other')
  return ''
})

/** 错误码的中文释义（★ 逐字面量 t()；未知错误码回落原始码，不编）。 */
function errorKindText(kind: string): string {
  return errorKindLabel(t, kind)
}

/** 错误列 / 错误徽标的 tooltip（来源 + 判据 + 失败阶段）。 */
const errorTitle = computed(() => {
  if (!props.errorKind && !props.errorSource) return ''
  const parts: string[] = []
  if (props.errorSource) parts.push(errorSourceName.value)
  if (props.errorKind) {
    parts.push(t('liveStageTree.error.kindLabel', { kind: errorKindText(props.errorKind) }))
    parts.push(t('liveStageTree.error.codeLabel', { code: props.errorKind }))
  }
  if (props.failedStage) {
    parts.push(t('liveStageTree.error.stageLabel', { stage: stageName(props.failedStage) }))
  }
  return parts.join(' · ')
})

/** 紧凑模式瓦片上的小数字（★ 只放真正有口径的数，没有就不放 —— 不编 0）。 */
function compactNumber(s: StageNode): string {
  if (s.durMs != null) return `${s.durMs}`
  if (!s.value) return ''
  if (s.key === 'route') return s.candidates != null ? String(s.candidates) : ''
  if (s.key === 'sanitize') return s.value
  if (s.key === 'injection') return s.value
  if (s.key === 'meter') return s.value
  if (s.key === 'first_byte') return s.value
  return ''
}

/* ── 紧凑模式专用 ────────────────────────────────────────────────── */

/** 紧凑模式的 11 段（发送 7 段 → 回程 4 段，单行流水）。 */
const compactSeq = computed<StageNode[]>(() => [...sendStages.value, ...recvStages.value])

/** 紧凑模式**不画 ✓**（设计第 8 轮：owner「整个紧凑模式不要勾勾」）——
 *  完成态由**颜色**承载，失败/跳过/告警/进行中仍留字形。
 *  ★ 字形表来自**唯一真源** `lib/stageTreeGlyphs.ts`（与图例共用）——
 *    本轮实测的线上缺陷 F1 就是因为「瓦片一套、图例另一套」⇒ 同一条规定写了两份
 *    ⇒ 必然漂移 ⇒ 生产页面出现「图例说 ✓、画面没 ✓」。 */
const COMPACT_STATE_GLYPH: Readonly<Record<StageState, string>> = COMPACT_GLYPH

function compactGlyph(state: StageState): string {
  return COMPACT_STATE_GLYPH[state] ?? ''
}

/** 阶段图标（设计原型 ICON 的移植；计量段在原型里叫 billing）。 */
function stageIcon(key: StageKey): string {
  const k = key === 'meter' ? 'billing' : key
  return (STAGE_ICONS as Record<string, string>)[k] ?? ''
}

/** 这一段属于哪条轨（决定轨道短线与箭头方向）。 */
function railOf(key: StageKey): 'send' | 'recv' {
  return SEND_STAGES.some(d => d.key === key) ? 'send' : 'recv'
}

/** 是否是该轨的**最后一段**（最后一段后面不画短线）。 */
function isRailTail(key: StageKey): boolean {
  return key === 'provider' || key === 'reply'
}

/** 失败落在这一段吗（出错的那一段叠来源角标）。 */
function isErrStage(key: StageKey): boolean {
  return !!props.errorSource && props.failedStage === key
}

/** 状态徽标文案（★ 逐字面量 t()：动态键是引用扫描器的盲区）。 */
function statusLabel(status: string): string {
  switch (status) {
    case 'success':
      return t('liveStageTree.band.status.success')
    case 'in_progress':
      return t('liveStageTree.band.status.in_progress')
    case 'failure':
      return t('liveStageTree.band.status.failure')
    default:
      return status
  }
}
</script>

<template>
  <article
    class="band"
    :class="[`is-${props.status}`, `mode-${props.mode}`, { 'is-expanded': props.expanded }]"
    :data-testid="`stage-band-${props.requestId}`"
    :data-expanded="props.expanded ? '1' : '0'"
    role="button"
    tabindex="0"
    :aria-expanded="props.expanded ? 'true' : 'false'"
    :aria-label="`${props.requestId} · ${props.clientName}`"
    @click="onToggle"
    @keydown.enter.prevent="onToggle"
    @keydown.space.prevent="onToggle"
  >
    <div
      class="band__grid"
      :style="{ gridTemplateColumns: gridColumns, '--st-tree-w': `${COMPACT_TREE_WIDTH}px` }"
    >
      <!-- ① 时间列（设计第 8 轮：每带最左，一眼看出先后） -->
      <div class="band__time" :data-testid="`stage-band-time-${props.requestId}`">
        {{ timeText }}
      </div>

      <!-- ② 客户端节点（固定最左；完整模式跨两行，紧凑模式只占一行） -->
      <div
        class="band__client"
        :style="{ gridColumn: '2', gridRow: props.mode === 'full' ? '1 / span 2' : '1' }"
      >
        <div class="band__client-top">
          <span class="band__glyph"><Monitor /></span>
          <span class="band__client-type">{{ clientTypeLabel() }}</span>
          <span v-if="props.isAuto" class="band__auto" data-testid="stage-band-auto">AUTO</span>
        </div>
        <div class="band__client-name">{{ props.clientName }}</div>
        <div class="band__client-model" :title="props.modelEntry">{{ clientModelLine }}</div>
      </div>

      <!-- 发送轨（上行）：节点有值 + 状态条
           ★ 只在**完整模式**渲染：紧凑模式用 11 段瓦片表达同一条链路，
             两套节点同时在场会**抢同一批网格列**（瓦片被挤到第二行、节点文字被压成省略号）。 -->
      <div
        v-for="s in props.mode === 'full' ? sendStages : []"
        :key="s.key"
        class="band__node band__node--send"
        :class="`is-${s.state}`"
        :style="{ gridColumn: String(SEND_COLUMN[s.key]) }"
        :data-testid="`stage-send-${s.key}`"
        :title="stageTitle(s)"
      >
        <div class="band__node-top">
          <span class="band__node-glyph">{{ stageGlyph(s) }}</span>
          <span class="band__node-name">{{ stageName(s.key) }}</span>
        </div>
        <div class="band__node-value">{{ valueText(s) || '—' }}</div>
        <span class="band__node-bar"></span>
      </div>

      <!-- 接收轨（下行，从右往左）：单行「字形 + 名称 + 值」（★ 同上：仅完整模式） -->
      <div
        v-for="s in props.mode === 'full' ? recvStages : []"
        :key="s.key"
        class="band__node band__node--recv"
        :class="`is-${s.state}`"
        :style="{ gridColumn: String(RECV_COLUMN[s.key]), gridRow: '2' }"
        :data-testid="`stage-recv-${s.key}`"
        :title="stageTitle(s)"
      >
        <span class="band__node-glyph">{{ stageGlyph(s) }}</span>
        <span class="band__node-name">{{ stageName(s.key) }}</span>
        <span class="band__node-value">{{ valueText(s) }}</span>
      </div>

      <!-- 接收轨方向标记（设计：回程线从右往左 ⇒ 右端写明「◀ 接收轨」） -->
      <div
        v-if="props.mode === 'full'"
        class="band__railtag"
        :style="{ gridColumn: '10', gridRow: '2' }"
        :title="t('liveStageTree.return.lane')"
      >
        {{ t('liveStageTree.legend.recvRail') }}
      </div>

      <!-- ④ 摘要行（完整模式） -->
      <div
        v-if="props.mode === 'full'"
        class="band__summary"
        :style="{ gridColumn: '1 / -1', gridRow: '3' }"
      >
        <span class="band__badge" :class="`is-${props.status}`" data-testid="stage-band-status">
          {{ statusLabel(props.status) }}
        </span>

        <!-- 错误来源徽标（★ 与紧凑模式**同口径**：来源徽标 + error_kind **原始值**；
             依据主档 §16.1「来源徽标顺带带上 error_kind（与紧凑同口径）」——
             原始值可 grep、可对生产代码；中文释义/失败阶段在 tooltip 里） -->
        <span
          v-if="props.errorSource"
          class="band__error"
          :class="`is-${props.errorSource}`"
          data-testid="stage-band-error"
          :title="errorTitle"
        >
          {{ errorTag }} {{ props.errorKind }}
        </span>

        <!-- 会话轮次（首轮 / 第 N 轮） -->
        <span v-if="sessionText" class="band__session">{{ sessionText }}</span>

        <!-- 最终匹配模型（owner 明确要求：auto 也必须显示最终匹配到的模型） -->
        <span
          v-if="props.finalModel"
          class="band__final"
          data-testid="stage-band-final-model"
          :title="props.finalModelTip"
        >
          {{ t('liveStageTree.band.finalModel') }} {{ props.finalModel }}
        </span>
        <span
          v-if="props.upstreamModel && props.upstreamModel !== props.finalModel"
          class="band__upstream"
          data-testid="stage-band-upstream-model"
        >
          {{ t('liveStageTree.band.upstreamModel') }} {{ props.upstreamModel }}
        </span>
        <span v-if="props.modelChain" class="band__chain">{{ props.modelChain }}</span>

        <span v-if="props.candidates != null" class="band__metric" data-testid="stage-band-candidates">
          {{ t('liveStageTree.value.route', { n: props.candidates }) }}
        </span>
        <span v-if="props.attempts > 1" class="band__metric">
          {{ t('liveStageTree.band.attempts', { n: props.attempts }) }}
        </span>

        <span class="band__provider">{{ props.providerLabel }}</span>

        <span v-if="props.queueMs != null" class="band__metric">
          {{ t('liveStageTree.band.queue', { ms: props.queueMs }) }}
        </span>
        <span v-if="props.latencyMs != null" class="band__metric">⏱ {{ props.latencyMs }}ms</span>
        <span v-if="props.promptTokens != null || props.completionTokens != null" class="band__metric">
          ⇡ {{ props.promptTokens ?? '—' }} → ⇣ {{ props.completionTokens ?? '—' }}
        </span>
        <span v-if="props.totalTokens != null" class="band__metric">⇅ {{ props.totalTokens }} tok</span>
        <span v-if="props.costUsd != null" class="band__metric">¥{{ props.costUsd.toFixed(4) }}</span>

        <!-- 指纹可信度（与模型名同步显示） -->
        <span
          class="band__cred"
          :class="`is-${props.credibilityTone}`"
          data-testid="stage-band-credibility"
          :title="props.credibilityTip"
        >
          {{ props.credibilityLabel }}
        </span>

        <span class="band__cache" :class="`is-${props.cacheTone}`" :title="props.cacheTip">
          {{ props.cacheLabel }}
        </span>
      </div>

      <!-- ⑤ 紧凑模式（设计第 6/7/8/9 轮）：
           单行 11 段瓦片（28×28，**无边框无底色**）＋ 段间轨道短线与方向箭头
           ＋ **右侧同一行**摘要块（状态·会话·模型·路由·候选·尝试·token·耗时）
           ＋ **最右固定错误列**（右缘恒定 ⇒ 能竖着扫）。 -->
      <template v-else>
        <div
          v-for="s in compactSeq"
          :key="`c-${s.key}`"
          class="band__tile"
          :class="`is-${s.state}`"
          :style="{ gridColumn: String(COMPACT_TILE_COLUMN[s.key]) }"
          :data-testid="`stage-tile-${s.key}`"
          :data-rail="railOf(s.key)"
          :data-tail="isRailTail(s.key) ? '1' : undefined"
          :title="stageTitle(s)"
        >
          <span class="band__tile-icon" v-html="stageIcon(s.key)"></span>
          <span class="band__tile-glyph">{{ compactGlyph(s.state) }}</span>
          <span class="band__tile-num">{{ compactNumber(s) }}</span>
          <!-- 出错的那一段：角标用**来源色圆点**（来源名与判据在 tooltip / 最右错误列） -->
          <span
            v-if="isErrStage(s.key)"
            class="band__tile-src"
            :class="`is-${props.errorSource}`"
            :title="errorTitle"
          ></span>
          <!-- 供应商瓦片左下：出站尝试次数（多跳才画） -->
          <i v-if="s.key === 'provider' && props.attempts > 1" class="band__tile-marks">
            ↻{{ props.attempts }}
          </i>
          <!-- 方向：发送末端向右、回程起点向左 -->
          <i v-if="s.key === 'provider'" class="band__arrow is-right"></i>
          <i v-if="s.key === 'first_byte'" class="band__arrow is-left"></i>
        </div>

        <!-- 右侧同一行摘要块（★ 不是另起一行的摘要行 —— 设计第 7 轮） -->
        <div
          class="band__cright"
          :style="{ gridColumn: String(COMPACT_RIGHT_COLUMN) }"
          data-testid="stage-band-cright"
        >
          <span class="band__badge" :class="`is-${props.status}`" data-testid="stage-band-status">
            {{ statusLabel(props.status) }}
          </span>
          <span v-if="sessionText" class="band__session">{{ sessionText }}</span>
          <span v-if="props.finalModel" class="band__final">{{ props.finalModel }}</span>
          <span v-if="props.modelChain" class="band__chain">{{ props.modelChain }}</span>
          <span v-if="props.candidates != null" class="band__metric">
            {{ t('liveStageTree.value.route', { n: props.candidates }) }}
          </span>
          <span v-if="props.attempts > 1" class="band__metric">
            {{ t('liveStageTree.band.attempts', { n: props.attempts }) }}
          </span>
          <span v-if="props.totalTokens != null" class="band__metric">
            ⇅ {{ props.totalTokens }} tok
          </span>
          <span v-if="props.latencyMs != null" class="band__metric">⏱ {{ props.latencyMs }}ms</span>
        </div>

        <!-- 最右错误列：所有带的 chip 右缘恒定（一条真正的列） -->
        <div
          class="band__errorcol"
          :style="{ gridColumn: String(COMPACT_ERROR_COLUMN) }"
          data-testid="stage-band-errorcol"
        >
          <span
            v-if="props.errorSource"
            class="band__error"
            :class="`is-${props.errorSource}`"
            :title="errorTitle"
          >
            {{ errorTag }} {{ props.errorKind }}
          </span>
          <span v-else class="band__error is-none">—</span>
        </div>
      </template>
    </div>

    <!-- ★ 详情面板插槽（设计原型：`toggleBandDetail` 展开的「请求详情」）。
         由上层（LiveStageTreeView）在**被点开的那一条**上填内容；
         未展开时不渲染 ⇒ 页面默认态与改造前**逐字一致**（设计钉测不受影响）。 -->
    <slot name="detail" />
  </article>
</template>

<style scoped>
.band {
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
}

.band__grid {
  display: grid;
  gap: 4px 8px;
  padding: 6px 12px;
  align-items: stretch;
}

/* ── 时间列 ───────────────────────────────────────────────────── */
.band__time {
  grid-row: 1 / span 2;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 4px;
  color: var(--kx-muted);
  font-size: var(--kx-text-2xs);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* ── 客户端节点（最左，跨两行） ─────────────────────────────── */
.band__client {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 4px 6px;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg-container);
  min-width: 0;
}

.band__client-top {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: var(--kx-text-2xs);
  color: var(--kx-muted);
}

.band__glyph {
  color: var(--kx-primary);
  display: inline-flex;
  align-items: center;
}

/* element-plus 图标是裸 <svg viewBox>，必须显式定尺寸。 */
.band__glyph > svg {
  width: 12px;
  height: 12px;
}

.band__auto {
  padding: 0 4px;
  border-radius: var(--kx-radius-sm);
  background: var(--kx-primary-soft);
  color: var(--kx-primary);
  font-weight: 600;
}

.band__client-name {
  font-size: var(--kx-text-xs);
  font-weight: 600;
  color: var(--kx-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.band__client-model {
  font-size: var(--kx-text-2xs);
  color: var(--kx-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ── 阶段节点 ───────────────────────────────────────────────── */
.band__node {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 3px 6px;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg-container);
  min-width: 0;
}

.band__node-top {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}

.band__node-glyph {
  flex: none;
  font-size: var(--kx-text-2xs);
  color: var(--kx-muted);
}

.band__node-name {
  font-size: var(--kx-text-2xs);
  color: var(--kx-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.band__node-value {
  font-size: var(--kx-text-2xs);
  color: var(--kx-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 底部状态条（设计图里的 n-bar）：一眼看出这一段的状态。 */
.band__node-bar {
  position: absolute;
  left: 0;
  bottom: 0;
  height: 2px;
  width: 100%;
  background: var(--kx-border);
  opacity: 0.9;
}

/* 七态配色：done 绿 / active 蓝 / failed 红 / warned 黄；
   pending 与 unmeasured 都是灰，但**形态不同**（未测量用 ⋯，未到达用 ○；
   skipped 用 ⌀ 表示「不适用」）。 */
.band__node.is-done {
  border-color: var(--kx-success);
}
.band__node.is-done .band__node-glyph {
  color: var(--kx-success);
}
.band__node.is-done .band__node-bar {
  background: var(--kx-success);
}
.band__node.is-active {
  border-color: var(--kx-primary);
}
.band__node.is-active .band__node-glyph {
  color: var(--kx-primary);
}
.band__node.is-active .band__node-bar {
  background: var(--kx-primary);
}
.band__node.is-failed {
  border-color: var(--kx-danger);
  background: var(--kx-danger-soft);
}
.band__node.is-failed .band__node-glyph {
  color: var(--kx-danger);
}
.band__node.is-failed .band__node-bar {
  background: var(--kx-danger);
}
.band__node.is-warned {
  border-color: var(--kx-warning);
  background: var(--kx-warning-soft);
}
.band__node.is-warned .band__node-glyph {
  color: var(--kx-warning);
}
.band__node.is-warned .band__node-bar {
  background: var(--kx-warning);
}
.band__node.is-skipped {
  border-style: dashed;
  opacity: 0.6;
}
.band__node.is-skipped .band__node-bar {
  opacity: 0.25;
}
.band__node.is-unmeasured {
  border-style: dashed;
  opacity: 0.75;
}
.band__node.is-unmeasured .band__node-glyph {
  color: var(--kx-muted);
}
.band__node.is-pending {
  opacity: 0.72;
}

/* 接收轨节点：比发送轨矮一档（视觉上「回来的一路」）。 */
.band__node--recv {
  flex-direction: row;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  min-height: 26px;
}

.band__node--recv .band__node-value {
  margin-left: auto;
}

/* 接收轨方向标记（右端）。 */
.band__railtag {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  color: var(--kx-muted);
  font-size: var(--kx-text-2xs);
  white-space: nowrap;
}

/* ── 摘要行 ─────────────────────────────────────────────────── */
.band__summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  font-size: var(--kx-text-2xs);
  color: var(--kx-muted);
}

.band__badge {
  padding: 0 4px;
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg);
  color: var(--kx-text-secondary);
}

.band__badge.is-success {
  background: var(--kx-success-soft);
  color: var(--kx-success);
}
.band__badge.is-failure {
  background: var(--kx-danger-soft);
  color: var(--kx-danger);
}
.band__badge.is-in_progress {
  background: var(--kx-primary-soft);
  color: var(--kx-primary);
}

.band__error {
  padding: 0 4px;
  border-radius: var(--kx-radius-sm);
  border: 1px solid transparent;
  color: var(--kx-danger);
  /* ★ error_kind 是**生产原始值**（可 grep、可对代码）⇒ 等宽字体，
     与普通文案区分开；太长时省略（完整值在 tooltip 里）。 */
  font-family: var(--kx-mono);
  font-size: var(--kx-text-2xs);
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.band__error.is-gateway {
  border-color: var(--kx-danger);
}
.band__error.is-provider {
  border-color: var(--kx-warning);
  color: var(--kx-warning);
}
.band__error.is-other {
  border-color: var(--kx-border);
  color: var(--kx-muted);
}
.band__error.is-none {
  border-color: transparent;
  color: var(--kx-muted);
}

.band__session {
  padding: 0 4px;
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg);
  color: var(--kx-text-secondary);
}

/* 最终匹配模型：加粗 + 主色，和其它摘要项区分开（owner 明确要求突出显示）。 */
.band__final {
  font-weight: 600;
  color: var(--kx-text);
}

.band__upstream,
.band__chain {
  color: var(--kx-text-secondary);
}

.band__provider {
  color: var(--kx-text-secondary);
}

.band__cred {
  padding: 0 4px;
  border-radius: var(--kx-radius-sm);
  border: 1px solid transparent;
}

.band__cred.is-verified {
  border-color: var(--kx-success);
  color: var(--kx-success);
}
.band__cred.is-suspected {
  border-color: var(--kx-warning);
  color: var(--kx-warning);
}
.band__cred.is-diluted {
  border-color: var(--kx-danger);
  color: var(--kx-danger);
}
.band__cred.is-unknown {
  border-color: var(--kx-border);
  color: var(--kx-muted);
}
.band__cred.is-na {
  border-style: dashed;
  border-color: var(--kx-border);
  color: var(--kx-muted);
}

.band__cache {
  padding: 0 4px;
  border-radius: var(--kx-radius-sm);
  border: 1px solid transparent;
}

.band__cache.is-hit {
  border-color: var(--kx-success);
  color: var(--kx-success);
}
.band__cache.is-na {
  border-style: dashed;
  border-color: var(--kx-border);
  color: var(--kx-muted);
}
.band__cache.is-bad {
  border-color: var(--kx-danger);
  color: var(--kx-danger);
}

.band__metric {
  font-variant-numeric: tabular-nums;
}

/* ── 紧凑模式（设计第 6/7/8/9 轮）───────────────────────────────
   ★ 几何来自设计原型：CTILE=28（瓦片边长）/ CPITCH=38（步距 ⇒ 间隙 10）
     / CTREE_W=1400（树宽）/ CERR_W=210（错误列）/ CERR_RIGHT=10（右留白）。
   ★ 瓦片**无边框无底色**（owner 第 7 轮：「图标再缩小一些，不要加边框」），
     状态由**颜色 + 角标字形**两通道承载；完成态**不画 ✓**（owner 第 8 轮）。
   ★ 错误列贴在**树的**右缘 ⇒ 所有带的 chip 右缘恒定（一条真正的列，能竖着扫）。 */

.mode-compact .band__grid {
  align-items: center;
  /* 紧凑树宽固定 1400（原型 CTREE_W）：错误列因此在**树的**右缘，
     而不是「跟着内容长度漂移」。窗口更窄时画布横向滚动（设计 §16.3）。 */
  min-width: var(--st-tree-w, 1400px);
  gap: 0 10px;
}

/* ★ 紧凑模式只有一行：时间列与客户端都不得跨行，
   否则网格被撑出第二行，11 段瓦片整体下移一格。 */
.mode-compact .band__time {
  grid-row: 1;
  align-items: center;
  padding-top: 0;
}

.mode-compact .band__client {
  grid-row: 1;
  padding: 2px 4px;
}

/* ── 瓦片 ──────────────────────────────────────────────────────── */
.band__tile {
  position: relative;
  width: 28px;
  height: 28px;
  border-radius: 7px;
  border: none;
  background: transparent;
  color: var(--kx-muted);
}

/* 图标：绝对定位（左上）⇒ 右上留给状态字形、右下留给小数字、左下留给路由标记。 */
.band__tile-icon {
  position: absolute;
  left: 6px;
  top: 4px;
  width: 15px;
  height: 15px;
}

/* ★★ 必须用 :deep() —— v-html 注入的 SVG **不会**带 scoped 属性（Vue 只给模板里的
   元素加），写成 `.band__tile-icon > svg` 永远不匹配 ⇒ 图标会退回浏览器默认
   （黑色填充、无描边）⇒ 画面上是一坨黑块，且状态色完全丢失。 */
.band__tile-icon :deep(svg) {
  width: 15px;
  height: 15px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.7;
  stroke-linecap: round;
  stroke-linejoin: round;
}

/* 状态角标：**纯字形**（不画实心圆）⇒ 图标仍是主角，字形是非颜色通道。 */
.band__tile-glyph {
  position: absolute;
  top: -5px;
  right: -4px;
  font-size: 9px;
  line-height: 10px;
  font-weight: 800;
  color: inherit;
  background: var(--kx-surface);
  padding: 0 1px;
  border-radius: 3px;
}

/* 小数字：贴在瓦片内右下角（不是另起一行）⇒ 整条带只有一行。 */
.band__tile-num {
  position: absolute;
  right: 0;
  bottom: 0;
  font-size: 7px;
  line-height: 8px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
  background: var(--kx-surface);
  padding: 0 1px;
  border-radius: 2px;
  max-width: 30px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font-weight: 600;
}

/* 出错段：错误**来源**角标。
   ★ 原型用「单字」（网/供/它）：单字在 8 语种里既不可实现、又必然与 en-US/zh-CN
     同值 ⇒ 撞 i18n 值棘轮 ⇒ 改为**来源色圆点**（颜色承载来源，tooltip 给名称与判据），
     避免整段文案压在相邻瓦片上。 */
.band__tile-src {
  position: absolute;
  left: -5px;
  top: 50%;
  margin-top: -4px;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--kx-muted);
  border: 1px solid var(--kx-border);
}

.band__tile-src.is-gateway {
  background: var(--kx-primary);
  border-color: var(--kx-primary);
}

.band__tile-src.is-provider {
  background: var(--kx-warning);
  border-color: var(--kx-warning);
}

/* 路由多层标记：供应商瓦片左下角「↻N」（跳数越多标记越多）。 */
.band__tile-marks {
  position: absolute;
  left: 1px;
  bottom: 0;
  font-size: 7px;
  line-height: 8px;
  font-style: normal;
  font-weight: 700;
  color: var(--kx-muted);
  background: var(--kx-surface);
  padding: 0 1px;
  border-radius: 2px;
}

/* ── 轨道短线与方向箭头 ─────────────────────────────────────────
   ★ 轨道**只画在段与段之间**（不横穿图标）—— 去边框后图标本身很轻，
     一条实线穿过去会像「删除线」，把图标划花。 */
.band__tile[data-rail]::after {
  content: '';
  position: absolute;
  top: 13px;
  left: 28px;
  width: 10px;
  height: 2px;
  border-radius: 2px;
}

.band__tile[data-rail='send']::after {
  background: var(--kx-primary);
  opacity: 0.38;
}

.band__tile[data-rail='recv']::after {
  background: var(--kx-warning);
  opacity: 0.38;
}

/* 该轨的最后一段：后面不画短线。 */
.band__tile[data-tail='1']::after {
  display: none;
}

/* 方向：发送末端向右、回程起点向左（用箭头表达，不用文字标签）。 */
.band__arrow {
  position: absolute;
  top: 11px;
  width: 0;
  height: 0;
  border-top: 4px solid transparent;
  border-bottom: 4px solid transparent;
}

.band__arrow.is-right {
  left: 26px;
  border-left: 6px solid var(--kx-primary);
  opacity: 0.55;
}

.band__arrow.is-left {
  left: -10px;
  border-right: 6px solid var(--kx-warning);
  opacity: 0.55;
}

/* ── 瓦片七态 ──────────────────────────────────────────────────── */
.band__tile.is-done {
  color: var(--kx-success);
}
.band__tile.is-active {
  color: var(--kx-primary);
}
.band__tile.is-failed {
  color: var(--kx-danger);
}
.band__tile.is-warned {
  color: var(--kx-warning);
}
/* 跳过：降透明 + 斜纹（不靠颜色单独承载语义）。 */
.band__tile.is-skipped {
  opacity: 0.4;
  color: var(--kx-muted);
  background: repeating-linear-gradient(
    45deg,
    rgba(139, 155, 176, 0.12) 0 3px,
    transparent 3px 6px
  );
}
/* 未到达：靛紫（与「跳过＝灰 + 斜纹」明确区分）。 */
.band__tile.is-pending {
  color: #7c5cf0;
  opacity: 0.85;
}
/* 未测量：与未到达分开（没数据 ≠ 没跑到）。 */
.band__tile.is-unmeasured {
  color: var(--kx-muted);
  opacity: 0.6;
}

/* ── 右侧同一行摘要块 ───────────────────────────────────────────
   ★ 设计第 7 轮：状态/目标模型/完整路由放在**同一行右侧**，摘要行彻底取消
     ⇒ 每条带只剩一行。 */
.band__cright {
  display: flex;
  align-items: center;
  gap: 7px;
  height: 28px;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}

.band__cright .band__final,
.band__cright .band__chain,
.band__cright .band__session {
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 错误列：带级固定右列（右缘恒定 ⇒ 能竖着扫）。 */
.band__errorcol {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  height: 28px;
  min-width: 0;
  overflow: hidden;
}
</style>
