<script setup lang="ts">
// RoutingDecisionTimeline.vue — 内部路由与任务判断的形象化（2026-10-16）
//
// 需求原话：「内部的路由及任务判断也可以形象化的表达清楚」。
// 图上已经用粒子走了「客户端 → 核心 → 供应端 → 模型」，但**为什么**这么走
// （自动决策/拦截原因/回退/重试/凭据切换）只在日志里。这里把
// request_lifecycle 的 action 序列画成四段泳道时间线：
//
//   routing（鉴权/排队/候选打分）→ llm（选中凭据并发上游）
//   → retrying（回退/换凭据/换模型）→ terminal（终态）
//
// 数据来自既有 SSE 的 request_lifecycle 帧，不新增后端。

import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CaretRight } from '@element-plus/icons-vue'
import type { ActionEvent } from '../../composables/liveStreamStore'

const props = defineProps<{
  decisions: ActionEvent[]
  /** 无数据时的原因说明（区分「没选中任何请求」与「该请求确实没有判定事件」） */
  hint?: string
}>()

const { t } = useI18n()

// 折叠态：默认展开（判定时间线是排查的主视图，收起它=隐藏用户主动要看的东西）。
// 与 StatsPanel 的折叠同族：标题即按钮、CaretRight 旋转指示、aria-expanded 同步。
const collapsed = ref(false)

const STAGE_ORDER: Array<{ key: string; tone: string }> = [
  { key: 'routing', tone: 'is-routing' },
  { key: 'llm', tone: 'is-llm' },
  { key: 'retrying', tone: 'is-retrying' },
  { key: 'terminal', tone: 'is-terminal' },
]

/**
 * 未上报阶段的显式形态。
 * 早先这里 `d.stage_category ?? 'routing'` —— 把「没上报」画成「路由」，
 * 等于凭空造一个它没经历过的阶段。缺失就是缺失，渲染成中性「未上报阶段」。
 */
const UNREPORTED_STAGE = 'unreported'

const items = computed(() =>
  props.decisions.map((d) => ({
    ...d,
    stageKey: d.stage_category || UNREPORTED_STAGE,
    reason: d.blocked_reasons?.length ? d.blocked_reasons.join(' · ') : (d.reason ?? d.auto_decision ?? ''),
    transition: d.from_credential_id
      ? `${d.from_credential_id} → ${d.to_credential_id ?? d.credential_id ?? '?'}`
      : '',
  })),
)

function stageClass(stage: string): string {
  return STAGE_ORDER.find((s) => s.key === stage)?.tone ?? 'is-unreported'
}

function stageLabel(stage: string): string {
  return t(`liveTopology.decision.stage.${stage}`)
}

function actionLabel(action?: string): string {
  return action ? t(`liveTopology.decision.action.${action}`, action) : t('liveTopology.decision.action.unknown')
}
</script>

<template>
  <section class="decisions" :class="{ 'is-collapsed': collapsed }" aria-label="routing decisions">
    <header class="decisions__head">
      <button
        type="button"
        class="decisions__toggle"
        :aria-expanded="!collapsed"
        :aria-label="t(collapsed ? 'liveTopology.decision.expand' : 'liveTopology.decision.collapse')"
        data-testid="decisions-toggle"
        @click="collapsed = !collapsed"
      >
        <CaretRight class="decisions__caret" :class="{ 'is-open': !collapsed }" aria-hidden="true" />
        <span class="decisions__title">{{ t('liveTopology.decision.title') }}</span>
      </button>
      <span class="decisions__hint">{{ hint || t('liveTopology.decision.hint') }}</span>
    </header>
    <ol v-if="items.length && !collapsed" class="decisions__list">
      <li v-for="(item, index) in items" :key="`${item.seq ?? index}-${item.action ?? index}`" class="decisions__row">
        <span class="decisions__stage" :class="stageClass(item.stageKey)">{{ stageLabel(item.stageKey) }}</span>
        <span class="decisions__action">
          {{ actionLabel(item.action) }}
          <em v-if="item.retry_seq" class="decisions__retry">#{{ item.retry_seq }}</em>
        </span>
        <span v-if="item.transition" class="decisions__transition">{{ item.transition }}</span>
        <span v-if="item.queue_depth" class="decisions__depth">q{{ item.queue_depth }}</span>
        <span v-if="typeof item.ttfb_ms === 'number'" class="decisions__ttfb">ttfb {{ item.ttfb_ms }}ms</span>
        <span v-if="item.reason" class="decisions__reason">{{ item.reason }}</span>
      </li>
    </ol>
    <p v-else-if="!collapsed" class="decisions__empty">{{ t('liveTopology.decision.empty') }}</p>
  </section>
</template>

<style scoped>
.decisions {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 12px;
  background: var(--kx-surface);
  max-height: 26vh;
  overflow: hidden;
}

.decisions__head {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

/* 折叠按钮（照 StatsPanel 同族：透明底、hover 亮、标题内嵌）。 */
.decisions__toggle {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 4px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  cursor: pointer;
  color: inherit;
  flex: none;
}

.decisions__toggle:hover {
  background: var(--kx-primary-soft);
}

.decisions__title {
  font-size: var(--kx-font-md, 14px);
  color: var(--kx-text);
}

.decisions__caret {
  flex: none;
  color: var(--kx-muted);
  transform: rotate(0deg);
  transition: transform 0.15s var(--kx-motion-ease-out);
}

.decisions__caret.is-open {
  transform: rotate(90deg);
}

.decisions__caret > svg {
  width: 12px;
  height: 12px;
}

/* 折叠态：只留头部一行，高度不再吃 26vh 上限的布局空间。 */
.decisions.is-collapsed {
  max-height: none;
}

.decisions__hint {
  font-size: 11px;
  color: var(--kx-muted);
}

.decisions__list {
  margin: 0;
  padding: 0 0 0 4px;
  list-style: none;
  overflow-y: auto;
}

.decisions__row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 3px 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  border-bottom: 1px dashed color-mix(in srgb, var(--kx-border) 60%, transparent);
}

.decisions__stage {
  min-width: 62px;
  padding: 1px 4px;
  border-radius: 999px;
  font-size: 11px;
  text-align: center;
}

.decisions__stage.is-routing {
  color: var(--kx-primary);
  background: var(--kx-primary-soft);
}

.decisions__stage.is-llm {
  color: var(--kx-success);
  background: var(--kx-success-soft);
}

.decisions__stage.is-retrying {
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

.decisions__stage.is-terminal {
  color: var(--kx-danger);
  background: var(--kx-danger-soft);
}

/* 未上报阶段：中性色。不能借用任何一种「有含义」的阶段色 ——
   绿色会被读成成功、红色会被读成失败。 */
.decisions__stage.is-unreported {
  color: var(--kx-muted);
  background: var(--kx-bg-accent);
}

.decisions__action {
  flex: 1;
}

.decisions__retry {
  color: var(--kx-warning);
  font-style: normal;
}

.decisions__transition,
.decisions__depth,
.decisions__ttfb {
  font-variant-numeric: tabular-nums;
  color: var(--kx-muted);
}

.decisions__reason {
  max-width: 40%;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--kx-muted);
}

.decisions__empty {
  margin: 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-muted);
}
</style>
