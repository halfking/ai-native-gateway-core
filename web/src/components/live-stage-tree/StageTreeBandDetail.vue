<script setup lang="ts">
// StageTreeBandDetail.vue —— 请求阶段的**详情面板**（2026-11-18）
//
// ★ 为什么有它：设计原型（docs/audit/2026-11-16-live阶段树原型*.html）里
//   **点击请求行会展开「请求详情」**（原型函数 `toggleBandDetail`，手风琴：
//   同时只展开一条），而**线上实现缺了这一段** —— 点行没有任何反应。
//   ⇒ 本组件把原型的详情内容按**当前实现真实有的数据**移植过来。
//
// ★ 三条纪律（沿用本仓既有）：
//   ① **诚实**：拿不到的值一律显示「—」，**不拿 0 冒充**（未测量 ≠ 0）。
//   ② **不编**：原型里有「模拟 liveactions」「打开右侧抽屉」两块，
//      线上实现**没有**这两份数据/那个抽屉 ⇒ 本组件**不做**它们
//      （做不了就承认做不了，别画一个假的）。
//   ③ **字形随模式**：阶段字形取自唯一真源 `lib/stageTreeGlyphs.ts`
//      ⇒ 紧凑模式下**不出现 ✓**（设计规则 15：紧凑模式全页 ✓ 计数 = 0）。
//
// ★ 文案映射**不另写一份**：阶段名/状态名/错误码/客户端类型/模型来源
//   全部取自 `lib/liveStageTreeLabels.ts`（请求带与本面板共用一份）。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { StageBand } from '../../types/liveStageTree'
import { glyphFor } from '../../lib/stageTreeGlyphs'
import { requestDetailPath } from '../../utils/openRequestDetailPage'
import {
  bandStatus,
  bandStatusLabel,
  clientTypeLabel,
  errorKindLabel,
  errorSourceLabel,
  modelSourceLabel,
  stageDetailLabel,
  stageLabel,
  stageStateLabel,
  stageValueLabel,
} from '../../lib/liveStageTreeLabels'

const props = defineProps<{
  /** 该请求带的全部推导结果（纯函数产物）。 */
  band: StageBand
  /** 显示模式（完整 / 紧凑）—— 决定阶段字形（紧凑模式无 ✓）。 */
  mode: 'full' | 'compact'
  /** 会话内的轮次（1 = 首轮）。未测量 = null。 */
  sessionRound: number | null
  /** 指纹可信度 chip 文案（由页面层算好，避免这里再拉一次清单）。 */
  credibilityLabel: string
  /** 指纹可信度 tooltip。 */
  credibilityTip: string
}>()

const { t } = useI18n()

/** 紧凑模式 = 阶段字形表切换（✓ 不出现）。 */
const compact = computed(() => props.mode === 'compact')

/** 未测量的统一表示（诚实性纪律：不用 0 冒充）。 */
const NA = '—'

/**
 * ★ 「打开请求详情页」链接的目标地址。
 *
 * 路径**不另写一份**：取自仓内既有的单一真源
 * `utils/openRequestDetailPage.ts` 的 `requestDetailPath()`
 * （与 `openRequestDetailPage()` 同模块 ⇒ 同一套编码 / base / 回退逻辑）。
 *
 * ★ 为什么用**真实 `<a>`** 而不是 `<button>` + `window.open`：
 *   ① `window.open` 可能被**弹窗拦截器**挡掉（返回 false 时用户什么也没发生）；
 *   ② 真实链接**地址可见、可复制、可右键新标签打开**（中键/⌘点击天然可用）；
 *   ③ 语义正确 ⇒ 这是「跳到另一个页面」，不是「触发一个动作」。
 *   （仓内 `openRequestDetailPage()` 的 `window.open` 形态保留给**行**级入口，
 *     那里没有「链接」的位置。）
 */
const detailHref = computed(() => requestDetailPath(props.band.requestId))

function num(v: number | null | undefined): string {
  return v == null ? NA : String(v)
}

function ms(v: number | null | undefined): string {
  return v == null ? NA : `${v} ms`
}

function usd(v: number | null | undefined): string {
  return v == null ? NA : `$${v.toFixed(6)}`
}

/** 标题行：请求 id · 客户端 · 类型 · 状态。 */
const headLine = computed(() => {
  const b = props.band
  return [
    b.requestId,
    b.clientName,
    clientTypeLabel(t, b.clientType),
    bandStatusLabel(t, bandStatus(b)),
  ].join(' · ')
})

/** 模型解析链的字段（★ auto 选型的置信度未上报 = null ⇒ 显示「—」）。 */
const resolveRows = computed(() => {
  const m = props.band.model
  const autoText = m.isAuto
    ? `${t('liveStageTree.detail.field.taskType')} ${m.taskType || NA} · ${
        t('liveStageTree.detail.field.confidence')
      } ${m.autoConfidence == null ? NA : m.autoConfidence.toFixed(2)}`
    : t('liveStageTree.detail.field.no')
  return [
    { k: t('liveStageTree.detail.field.clientModel'), v: m.requested || NA },
    { k: t('liveStageTree.detail.field.autoSelect'), v: autoText },
    { k: t('liveStageTree.detail.field.resolveMethod'), v: m.reason || NA },
    { k: t('liveStageTree.detail.field.finalModel'), v: m.final || NA },
    { k: t('liveStageTree.band.finalModel'), v: modelSourceLabel(t, m.source) },
    {
      k: t('liveStageTree.detail.field.modelSwitch'),
      v: m.switched ? `${m.switched.from} → ${m.switched.to}` : NA,
    },
    {
      k: t('liveStageTree.detail.field.upstreamOutbound'),
      v: m.upstream
        ? `${m.upstream} @ ${props.band.providerLabel || props.band.providerCode || NA}`
        : NA,
    },
  ]
})

/** 回复侧指标（★ 全部按「未上报 = —」口径，不拿 0 冒充）。 */
const replyRows = computed(() => {
  const b = props.band
  const cache = b.cache
  const cacheText = cache && cache.pct != null ? `${cache.pct}%` : NA
  return [
    { k: t('liveStageTree.stats.kpi.inputTokens'), v: num(b.promptTokens) },
    { k: t('liveStageTree.stats.kpi.outputTokens'), v: num(b.completionTokens) },
    { k: t('liveStageTree.stats.kpi.totalTokens'), v: num(b.totalTokens) },
    { k: t('liveStageTree.detail.field.cacheRead'), v: num(b.cacheReadTokens) },
    {
      k: t('liveStageTree.stats.kpi.cacheRate'),
      v: cacheText,
      tip: cache ? cache.why : undefined,
    },
    { k: t('liveStageTree.detail.field.chunks'), v: num(b.chunks) },
    { k: t('liveStageTree.detail.field.ttfb'), v: ms(b.ttfbMs) },
    { k: t('liveStageTree.detail.field.latency'), v: ms(b.latencyMs) },
    { k: t('liveStageTree.detail.field.queue'), v: ms(b.queueMs) },
    { k: t('liveStageTree.detail.field.attempts'), v: String(b.attempts) },
    { k: t('liveStageTree.stats.kpi.cost'), v: usd(b.costUsd) },
  ]
})

/** 模型身份与可信度（★ 上游自报模型没上报 = —，不拿声称模型顶替）。 */
const identityRows = computed(() => {
  const b = props.band
  return [
    { k: t('liveStageTree.detail.field.claimedModel'), v: b.model.final || NA },
    { k: t('liveStageTree.detail.field.upstreamReported'), v: b.rawModel || NA },
    { k: t('liveStageTree.detail.field.credential'), v: b.credentialLabel || NA },
    { k: t('liveStageTree.detail.field.provider'), v: b.providerLabel || b.providerCode || NA },
    {
      k: t('liveStageTree.detail.field.credibility'),
      v: props.credibilityLabel || NA,
      tip: props.credibilityTip,
      credibility: true,
    },
  ]
})

/** 阶段明细（发送轨 + 接收轨，按 ALL_STAGES 顺序）。 */
const stageRows = computed(() =>
  props.band.stages.map(s => ({
    key: s.key,
    glyph: glyphFor(s.state, compact.value),
    name: stageLabel(t, s.key),
    state: stageStateLabel(t, s.state),
    value: stageValueLabel(t, s) || NA,
    detail: stageDetailLabel(t, s),
    dur: s.durMs == null ? NA : `${s.durMs} ms`,
  })),
)

/** 会话与错误（★ 没有错误时整块显示「无失败」，不列空行）。 */
const sessionRows = computed(() => {
  const b = props.band
  const rows = [
    { k: t('liveStageTree.detail.field.sessionId'), v: b.sessionId || NA },
    {
      k: t('liveStageTree.detail.field.sessionRound'),
      v:
        props.sessionRound == null
          ? NA
          : props.sessionRound <= 1
            ? t('liveStageTree.session.first')
            : t('liveStageTree.session.round', { n: props.sessionRound }),
    },
    {
      k: t('liveStageTree.detail.field.probe'),
      v: b.isProbe ? t('liveStageTree.detail.field.yes') : t('liveStageTree.detail.field.no'),
    },
  ]
  if (b.errorKind || b.errorSource) {
    rows.push({
      k: t('liveStageTree.detail.field.errorSource'),
      v: errorSourceLabel(t, b.errorSource) || NA,
    })
    rows.push({
      k: t('liveStageTree.detail.field.errorKind'),
      v: b.errorKind ? errorKindLabel(t, b.errorKind) : NA,
    })
    rows.push({
      k: t('liveStageTree.detail.field.failedStage'),
      v: b.failedStage ? stageLabel(t, b.failedStage) : NA,
    })
  }
  return rows
})
</script>

<template>
  <!-- ★ @click.stop：点详情内部**不得**冒泡到请求行 ⇒ 否则点一下就收起，看不清。 -->
  <section
    class="detail"
    :data-testid="`stage-detail-${props.band.requestId}`"
    data-testid-detail="stage-detail"
    @click.stop
  >
    <header class="detail__head">
      <span class="detail__title" data-testid="stage-detail-title">
        {{ t('liveStageTree.detail.title') }}
      </span>
      <span class="detail__sub">{{ headLine }}</span>
      <!-- ★ 「打开这一条请求的单页详情」（RequestDetailFullscreenView）。
           真实链接：地址可见、可复制、可中键/⌘点击打开；
           target=_blank ⇒ 新标签打开，/live2 的现场不被顶掉；
           rel=noopener noreferrer ⇒ 新标签拿不到 opener（安全惯例）。 -->
      <a
        v-if="detailHref"
        class="detail__open"
        data-testid="stage-detail-open"
        :href="detailHref"
        target="_blank"
        rel="noopener noreferrer"
        :aria-label="t('liveStageTree.detail.openDetailFor', { requestId: props.band.requestId })"
      >
        {{ t('liveStageTree.detail.openDetail') }}
        <span class="detail__open-icon" aria-hidden="true">↗</span>
      </a>
    </header>

    <div class="detail__cols">
      <div class="detail__col">
        <h4 class="detail__h" data-testid="stage-detail-section-resolve">
          {{ t('liveStageTree.detail.sections.resolve') }}
        </h4>
        <dl class="detail__kv">
          <template v-for="r in resolveRows" :key="r.k">
            <dt>{{ r.k }}</dt>
            <dd>{{ r.v }}</dd>
          </template>
        </dl>

        <h4 class="detail__h" data-testid="stage-detail-section-reply">
          {{ t('liveStageTree.detail.sections.reply') }}
        </h4>
        <dl class="detail__kv">
          <template v-for="r in replyRows" :key="r.k">
            <dt>{{ r.k }}</dt>
            <dd :title="r.tip">{{ r.v }}</dd>
          </template>
        </dl>

        <h4 class="detail__h" data-testid="stage-detail-section-identity">
          {{ t('liveStageTree.detail.sections.identity') }}
        </h4>
        <dl class="detail__kv">
          <template v-for="r in identityRows" :key="r.k">
            <dt>{{ r.k }}</dt>
            <dd :title="r.tip" :data-credibility="r.credibility ? '1' : undefined">{{ r.v }}</dd>
          </template>
        </dl>
      </div>

      <div class="detail__col">
        <h4 class="detail__h" data-testid="stage-detail-section-stages">
          {{ t('liveStageTree.detail.sections.stages') }}
        </h4>
        <dl class="detail__kv detail__kv--stage" data-testid="stage-detail-stages">
          <template v-for="s in stageRows" :key="s.key">
            <dt>
              <span class="detail__glyph">{{ s.glyph }}</span>
              {{ s.name }}
            </dt>
            <dd>
              {{ s.state }} · {{ s.value }}
              <span v-if="s.detail" class="detail__extra">· {{ s.detail }}</span>
              <span class="detail__extra">· {{ t('liveStageTree.detail.field.duration') }} {{ s.dur }}</span>
            </dd>
          </template>
        </dl>

        <h4 class="detail__h" data-testid="stage-detail-section-session">
          {{ t('liveStageTree.detail.sections.session') }}
        </h4>
        <dl class="detail__kv">
          <template v-for="r in sessionRows" :key="r.k">
            <dt>{{ r.k }}</dt>
            <dd>{{ r.v }}</dd>
          </template>
        </dl>
      </div>
    </div>
  </section>
</template>

<style scoped>
/* ★ 详情底色**不透明**（设计第 7 轮规则 18：透明底色会让详情与下一条带重叠，
     读起来像两层叠在一起）。 */
.detail {
  background: var(--kx-surface);
  border-top: 1px dashed var(--kx-border);
  padding: 8px 12px 12px;
  color: var(--kx-text);
  font-size: var(--kx-text-xs);
}

.detail__head {
  display: flex;
  align-items: baseline;
  gap: var(--kx-space-2);
  margin-bottom: 6px;
}

.detail__title {
  font-weight: 650;
  font-size: var(--kx-text-sm);
}

.detail__sub {
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

/* ★ 「打开请求详情页」链接：贴右、弱化但可点（不抢详情正文的视线）。 */
.detail__open {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 1px 7px;
  border: 1px solid var(--kx-border);
  border-radius: 999px;
  color: var(--kx-text);
  background: transparent;
  font-size: var(--kx-text-2xs);
  line-height: 1.6;
  text-decoration: none;
  white-space: nowrap;
  cursor: pointer;
}

.detail__open:hover {
  border-color: var(--kx-text);
}

.detail__open:focus-visible {
  outline: 2px solid var(--kx-primary, currentColor);
  outline-offset: 1px;
}

.detail__open-icon {
  font-size: var(--kx-text-xs);
  line-height: 1;
}

.detail__cols {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 4px 24px;
}

.detail__col {
  min-width: 0;
}

.detail__h {
  margin: 6px 0 3px;
  font-size: var(--kx-text-2xs);
  font-weight: 650;
  color: var(--kx-muted);
  text-transform: none;
}

.detail__kv {
  display: grid;
  grid-template-columns: minmax(96px, 168px) minmax(0, 1fr);
  gap: 1px 10px;
  margin: 0;
}

.detail__kv dt {
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.detail__kv dd {
  margin: 0;
  word-break: break-word;
}

.detail__glyph {
  display: inline-block;
  min-width: 12px;
  text-align: center;
}

.detail__extra {
  color: var(--kx-muted);
}

@media (max-width: 1024px) {
  .detail__cols {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
