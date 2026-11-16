<script setup lang="ts">
// StageTreeBand.vue —— 请求阶段树的**单条请求带**（2026-11-16，live2）
//
// 形态：**左右向**（owner 2026-10-16 原话「左右节点，最左是每个请求」）。
//   发送轨（上行）：客户端 → 鉴权/限流 → 脱敏 → 审批 → 注入检测 → 路由
//                 → 压缩 → 还原 → 供应商/令牌池
//   接收轨（下行）：供应商 → 首字节 → 计量 → 回复 → 回到客户端
//
// ★ 阶段顺序**以代码真实管线为准**（2026-11-16 对中间件链与发射点逐条核过）：
//   · 脱敏在**审批之前**（脱敏是中间件，包住 serveHTTPInner）；
//   · 压缩在**路由之后**（会话压缩器 Prepare 明确 runs AFTER candidate resolution）；
//   · 还原在**转发之前**（tools 还原与压缩同块，上游转发在其后）。
//   与 owner 初始列举顺序的差异写在 types/liveStageTree.ts 的头注释里。
//
// ★ 七态不可合并：
//   · `unmeasured`（未测量）＝ 这一段**没有实时信号**；
//   · `pending`（未到达）  ＝ 这一段**还没跑到**。
//   两者都灰，但**图标与文案不同** —— 合并成一个灰态，评审者会把「没数据」
//   读成「系统没做这一步」，那是两种相反的结论。
//
// ★ 失败定位：失败段自己变红，**下游一律显示「未到达」**（不是「也失败」）。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Back, Monitor } from '@element-plus/icons-vue'
import { RECV_STAGES, SEND_STAGES, type StageNode } from '../../types/liveStageTree'

const props = defineProps<{
  /** 请求 id（用于 data-testid 定位单条带）。 */
  requestId: string
  /** 该带的阶段状态（按 ALL_STAGES 顺序）。 */
  stages: StageNode[]
  /** 客户端显示名。 */
  clientName: string
  /** 客户端类型。 */
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
  /** 模型解析链说明（auto 任务类型 / 别名解析）。 */
  modelChain: string
  /** 指纹可信度 chip 文案 / tooltip / 形态。 */
  credibilityLabel: string
  credibilityTip: string
  credibilityTone: 'verified' | 'suspected' | 'diluted' | 'unknown' | 'na'
  /** 供应商 / 凭据显示名。 */
  providerLabel: string
  /** 状态徽标文案键后缀（success/in_progress/failure/…）。 */
  status: string
  /** 失败原因码（error_kind）。 */
  errorKind: string
  /** 总耗时（毫秒）。未测量 = null。 */
  latencyMs: number | null
  /** token 合计。未测量 = null。 */
  totalTokens: number | null
  /** 成本（USD）。未测量 = null。 */
  costUsd: number | null
  /** 缓存命中率文案（已由纯函数层算好口径）。 */
  cacheLabel: string
  /** 缓存命中率的 tooltip（口径说明）。 */
  cacheTip: string
  /** 缓存命中率的 chip 形态：hit / na / bad。 */
  cacheTone: 'hit' | 'na' | 'bad'
}>()

const { t } = useI18n()

/** 发送轨节点（列 2..9）。 */
const sendStages = computed(() =>
  props.stages.filter(s => SEND_STAGES.some(d => d.key === s.key)),
)
/** 接收轨节点（列 9..7，从右往左）。 */
const recvStages = computed(() =>
  props.stages.filter(s => RECV_STAGES.some(d => d.key === s.key)),
)

/** 阶段 → 网格列（客户端占第 1 列，发送轨 2..9）。 */
function sendColumn(key: string): number {
  return SEND_STAGES.findIndex(s => s.key === key) + 2
}

/** 接收轨列位：**从右往左**排（首字节贴着供应商，回复最靠左）——
 *  回程线的物理方向是「供应商 → 客户端」。 */
function recvColumn(key: string): number {
  return SEND_STAGES.length + 1 - RECV_STAGES.findIndex(s => s.key === key)
}

/** 阶段值的中文化（单位随阶段而变；空值不渲染 —— 状态图标已经说明了）。 */
function valueText(s: StageNode): string {
  if (!s.value) return ''
  switch (s.key) {
    case 'sanitize':
      return t('liveStageTree.value.sanitize', { n: s.value })
    case 'route':
      return t('liveStageTree.value.route', { n: s.value })
    case 'first_byte':
      // ★ 单位「ms」是技术单位，不是可翻译文案 ⇒ 直接内联，
      //   不占一个 i18n 键（占键会逼出「各语种逐字相同」的假缺陷）。
      return `${s.value}ms`
    case 'meter':
      return t('liveStageTree.value.meter', { n: s.value })
    case 'compress':
      return s.value === 'yes'
        ? t('liveStageTree.value.compress.yes')
        : t('liveStageTree.value.compress.no')
    case 'approval':
      // 判定词逐字本地化（★ 用字面量 t()：动态键无法被引用扫描器解析，
      // 会被 i18n 门记成「未决调用点」）。
      if (s.value === 'pass') return t('liveStageTree.decision.pass')
      if (s.value === 'need_approval') return t('liveStageTree.decision.need_approval')
      if (s.value === 'block') return t('liveStageTree.decision.block')
      return s.value
    case 'injection':
      // v1 是 observe-only ⇒ 命中的判定词是「仅告警」，不是「拒绝」。
      const d =
        s.detail === 'safe'
          ? t('liveStageTree.decision.safe')
          : s.detail === 'warn' || s.detail === 'block'
            ? t('liveStageTree.decision.warn')
            : t('liveStageTree.decision.unknown')
      return t('liveStageTree.value.injection', { n: s.value, d })
    default:
      return s.value
  }
}

/** 阶段节点的 tooltip：状态 · 值 · 补充 · 耗时。 */
function stageTitle(s: StageNode): string {
  const parts = [t(`liveStageTree.state.${s.state}`)]
  const v = valueText(s)
  if (v) parts.push(v)
  const detail = s.key === 'compress' ? compressDetail(s) : s.detail
  if (detail && s.key !== 'injection') parts.push(detail)
  if (s.durMs != null) parts.push(`${s.durMs}ms`)
  return parts.join(' · ')
}

/** 压缩节点的补充说明：tools 还原徽标本地化（「tools 回填 n」）。 */
function compressDetail(s: StageNode): string {
  return s.detail
    .split(' · ')
    .filter(Boolean)
    .map(part => {
      if (part.startsWith('tools:')) {
        return t('liveStageTree.band.toolsRestore', { n: part.slice('tools:'.length) })
      }
      if (part === 'restored') return t('liveStageTree.band.toolsRestored')
      return part
    })
    .join(' · ')
}

/** 阶段节点的展示图标（七态各有各的形，灰态之间靠这个区分）。 */
function stageGlyph(s: StageNode): string {
  switch (s.state) {
    case 'done':
      return '✓'
    case 'active':
      return '◐'
    case 'failed':
      return '✕'
    case 'warned':
      return '⚠'
    case 'unmeasured':
      return '⋯'
    case 'skipped':
      return '–'
    default:
      return '·'
  }
}
</script>

<template>
  <article class="band" :data-testid="`stage-band-${props.requestId}`">
    <div class="band__grid">
      <!-- 客户端节点（固定最左，跨两行） -->
      <div class="band__client" :style="{ gridColumn: '1', gridRow: '1 / span 2' }">
        <div class="band__client-top">
          <span class="band__glyph"><Monitor /></span>
          <span class="band__client-type">{{ props.clientType }}</span>
          <span v-if="props.isAuto" class="band__auto" data-testid="stage-band-auto">AUTO</span>
        </div>
        <div class="band__client-name">{{ props.clientName }}</div>
        <div class="band__client-model" :title="props.modelEntry">{{ props.modelEntry }}</div>
        <div class="band__client-proto">{{ props.clientProtocol || '—' }}</div>
      </div>

      <!-- 发送轨（上行） -->
      <div
        v-for="s in sendStages"
        :key="s.key"
        class="band__node band__node--send"
        :class="`is-${s.state}`"
        :style="{ gridColumn: String(sendColumn(s.key)) }"
        :data-testid="`stage-send-${s.key}`"
        :title="stageTitle(s)"
      >
        <span class="band__node-glyph">{{ stageGlyph(s) }}</span>
        <span class="band__node-name">{{ t(`liveStageTree.stage.${s.key}`) }}</span>
        <span v-if="valueText(s)" class="band__node-value">{{ valueText(s) }}</span>
        <span v-if="s.durMs != null" class="band__node-dur">{{ s.durMs }}ms</span>
      </div>

      <!-- 回程线（视觉：从回复段指回客户端） -->
      <div
        class="band__return"
        :style="{ gridColumn: '2 / 5', gridRow: '2' }"
        :title="t('liveStageTree.return.lane')"
      >
        <span class="band__return-arrow"><Back /></span>
        <span class="band__return-text">{{ t('liveStageTree.return.label') }}</span>
      </div>

      <!-- 接收轨（下行，从右往左） -->
      <div
        v-for="s in recvStages"
        :key="s.key"
        class="band__node band__node--recv"
        :class="`is-${s.state}`"
        :style="{ gridColumn: String(recvColumn(s.key)), gridRow: '2' }"
        :data-testid="`stage-recv-${s.key}`"
        :title="stageTitle(s)"
      >
        <span class="band__node-glyph">{{ stageGlyph(s) }}</span>
        <span class="band__node-name">{{ t(`liveStageTree.stage.${s.key}`) }}</span>
        <span v-if="valueText(s)" class="band__node-value">{{ valueText(s) }}</span>
        <span v-if="s.durMs != null" class="band__node-dur">{{ s.durMs }}ms</span>
      </div>

      <!-- 摘要行 -->
      <div class="band__summary" :style="{ gridColumn: '1 / -1', gridRow: '3' }">
        <span class="band__badge" :class="`is-${props.status}`" data-testid="stage-band-status">
          {{ t(`liveStageTree.band.status.${props.status}`) }}
        </span>
        <span v-if="props.errorKind" class="band__error">{{ props.errorKind }}</span>

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

        <span class="band__provider">{{ props.providerLabel }}</span>

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
        <span v-if="props.latencyMs != null" class="band__metric">⏱ {{ props.latencyMs }}ms</span>
        <span v-if="props.totalTokens != null" class="band__metric">⇅ {{ props.totalTokens }} tok</span>
        <span v-if="props.costUsd != null" class="band__metric">¥{{ props.costUsd.toFixed(4) }}</span>
      </div>
    </div>
  </article>
</template>

<style scoped>
.band {
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
}

.band__grid {
  display: grid;
  /* 客户端列 + 发送轨 7 列；行：发送轨 / 接收轨 / 摘要行。 */
  grid-template-columns: 168px repeat(7, minmax(96px, 1fr));
  gap: var(--kx-space-1) var(--kx-space-2);
  padding: var(--kx-space-2) var(--kx-space-3);
  align-items: start;
}

/* ── 客户端节点（最左，跨两行） ─────────────────────────────── */
.band__client {
  grid-row: 1 / span 2;
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  padding: var(--kx-space-2);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg-container);
}

.band__client-top {
  display: flex;
  align-items: center;
  gap: var(--kx-space-1);
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
  padding: 0 var(--kx-space-1);
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

.band__client-proto {
  font-size: var(--kx-text-2xs);
  color: var(--kx-muted);
}

/* ── 阶段节点 ───────────────────────────────────────────────── */
.band__node {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: var(--kx-space-1) var(--kx-space-2);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg-container);
  min-width: 0;
}

.band__node-glyph {
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

.band__node-dur {
  font-size: var(--kx-text-2xs);
  color: var(--kx-muted);
}

/* 七态配色：done 绿 / active 蓝 / failed 红 / warned 黄；
   pending 与 unmeasured 都是灰，但**形态不同**（未测量用虚线 + ⋯，
   未到达用实线 + ·；skipped 用短横线表示「不适用」）。 */
.band__node.is-done {
  border-color: var(--kx-success);
}
.band__node.is-done .band__node-glyph {
  color: var(--kx-success);
}
.band__node.is-active {
  border-color: var(--kx-primary);
}
.band__node.is-active .band__node-glyph {
  color: var(--kx-primary);
}
.band__node.is-failed {
  border-color: var(--kx-danger);
  background: var(--kx-danger-soft);
}
.band__node.is-failed .band__node-glyph {
  color: var(--kx-danger);
}
.band__node.is-warned {
  border-color: var(--kx-warning);
  background: var(--kx-warning-soft);
}
.band__node.is-warned .band__node-glyph {
  color: var(--kx-warning);
}
.band__node.is-skipped {
  border-style: dashed;
  opacity: 0.6;
}
.band__node.is-unmeasured {
  border-style: dashed;
  opacity: 0.75;
}
.band__node.is-unmeasured .band__node-glyph {
  color: var(--kx-muted);
}

/* 接收轨节点：比发送轨矮一档（视觉上「回来的一路」）。 */
.band__node--recv {
  padding-top: 1px;
  padding-bottom: 1px;
}

/* ── 回程线 ─────────────────────────────────────────────────── */
.band__return {
  display: flex;
  align-items: center;
  gap: var(--kx-space-1);
  padding: 0 var(--kx-space-2);
  color: var(--kx-muted);
  font-size: var(--kx-text-2xs);
  border-top: 1px dashed var(--kx-border);
  align-self: center;
}

.band__return-arrow {
  color: var(--kx-primary);
  display: inline-flex;
  align-items: center;
}

/* element-plus 图标是裸 <svg viewBox>，必须显式定尺寸。 */
.band__return-arrow > svg {
  width: 12px;
  height: 12px;
}

/* ── 摘要行 ─────────────────────────────────────────────────── */
.band__summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--kx-space-2);
  font-size: var(--kx-text-2xs);
  color: var(--kx-muted);
}

.band__badge {
  padding: 0 var(--kx-space-1);
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
  color: var(--kx-danger);
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
  padding: 0 var(--kx-space-1);
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
  padding: 0 var(--kx-space-1);
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
</style>
