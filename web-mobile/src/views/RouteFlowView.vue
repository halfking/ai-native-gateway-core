<script setup lang="ts">
// RouteFlowView — 任务 → 模型 → 供应商 分层流量（/auto-route/analytics/flow，**superAdmin 档**）。
//
// 数据源：GET /api/admin/auto-route/analytics/flow?window=
//
// 它与已上移的 /funnel、/matrix 构成 auto-route 分析的三个视角：
//   /funnel  = 单个模型**纵深**（这个模型被筛掉多少、可不可信）
//   /matrix  = 模型 × 任务**横向对比**（谁擅长什么）
//   本页     = 请求**从哪来、落到哪个供应商**（全链路的去向）
//
// ⚠️★ UI 形态的选择（这一条本身就是设计决定，写下来免得下一个人「优化」掉）：
//
// 后端给的是一张三层桑基图的数据（nodes + links）。移动端**不画桑基图**，
// 改成分层列表 + 链路明细。理由：
//   · 三层桑基在 390px 宽的屏上，每个节点标签要压到 8px 以下才排得下；
//   · 桑基的「带宽」编码在小屏上分辨率不足，肉眼分不出 3% 和 7%；
//   · 而**「哪个供应商承接了最多流量」是运维真正要答的问题**，
//     它用「按链路量排序的列表 + 占比」表达得比图形更直接，也更好点。
// ⇒ 列表是**主动选择**（带宽 → 排序 + 占比数字），不是「做不了图」的降级。
//
// ⚠️ 后端语义：
//
// (1) ★ **node.id 带层前缀**：`task:` / `model:` / `prov:`（analytics.go:504-520）。
//     label 才是可读名。两个都要留着：id 用来连边，label 用来显示。
//
// (2) ★ **`links[].task_type` 必须带上。**
//     L2→L3 的链路是按 (任务, 模型) 分组聚合的，每条边要回带原始任务，
//     否则「code_generation → gpt-4o → openai」和「翻译 → gpt-4o → openai」
//     会合成一条，图上分不清流量来自哪类请求。
//
// (3) **本端点没有降级字段**（meta 只有 window，且是 `map[string]string`
//     —— 全字符串，本仓库第四次同款）。⇒ 不编「估算」字样。
//
// (4) **24h 与 7d 走不同的数据源**（analytics_materialized.go:53-58），
//     但 flow 没有 p95，所以两条路的口径差异在本页**不产生可见偏差**。
//     ⇒ 与 /matrix 不同，本页不需要「口径」提示。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt } from '@/utils/format'
import {
  fetchRouteFlow,
  nodesByLayer,
  linksFrom,
  taskLabel,
  FLOW_LAYER_COUNT,
  type FlowLink,
  type FlowNode,
  type FlowResponse,
} from '@/api/autoRouteMatrix'
import { ANALYTICS_WINDOWS, type AnalyticsWindow } from '@/api/autoRouteInsights'

useHyperPage({ title: () => t('flow.title') })

const windowSel = ref<AnalyticsWindow>('7d')
const data = ref<FlowResponse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchRouteFlow({ window: windowSel.value })
  } catch (err) {
    data.value = null
    error.value = describeError(err)
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('flow.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

function pickWindow(w: AnalyticsWindow): void {
  if (windowSel.value === w) return
  windowSel.value = w
  void load()
}

const layers = computed(() => nodesByLayer(data.value?.nodes))
const links = computed(() => data.value?.links ?? [])

/** 全图总量。所有占比的分母 —— 不能拿某层的和当分母，那会让每层都是 100%。 */
const totalValue = computed(() => links.value.reduce((a, l) => a + (Number.isFinite(l.value) ? l.value : 0), 0))

const LAYER_LABEL_KEYS = ['layer.task', 'layer.model', 'layer.provider'] as const

function layerLabel(i: number): string {
  return t(LAYER_LABEL_KEYS[i] ?? 'layer.other')
}

/** 节点出边按流量降序 —— 见文件头「UI 形态」：带宽编码换成排序。 */
function outLinksOf(n: FlowNode): FlowLink[] {
  return linksFrom(links.value, n.id).sort((a, b) => b.value - a.value)
}

function share(v: number): number {
  if (totalValue.value <= 0) return 0
  return v / totalValue.value
}

/** ★ 占比文案。分母为 0 ⇒ 「—」，不是 0%（见 autoRouteInsights.stageRate 同纪律）。 */
function shareText(v: number): string {
  if (totalValue.value <= 0) return t('common.unknown')
  const p = share(v) * 100
  // 极小的链路直接写 <0.1%，比「0.0%」诚实
  if (p > 0 && p < 0.1) return '<0.1%'
  return `${p.toFixed(1)}%`
}

/** 边上的目标节点名。找不到就是形状不符，显示原始 id 而不是空。 */
function targetLabel(l: FlowLink): string {
  const n = (data.value?.nodes ?? []).find((x) => x.id === l.target)
  return n?.label || l.target.replace(/^(task|model|prov):/, '')
}

/** ★ 边上必须回显来源任务（见文件头 (2)）。 */
function taskTextOf(l: FlowLink): string {
  return taskLabel(l.task_type, t)
}

const isEmpty = computed(() => loaded.value && !loading.value && links.value.length === 0)

onBeforeUnmount(() => {
  data.value = null
})
</script>

<template>
  <div class="view-root fl">
    <div class="mx-chips" role="group" :aria-label="t('flow.window')">
      <button
        v-for="w in ANALYTICS_WINDOWS"
        :key="w"
        type="button"
        class="fl__chip"
        :class="{ 'fl__chip--on': windowSel === w }"
        :aria-pressed="windowSel === w"
        @click="pickWindow(w)"
      >
        {{ t('funnel.window' + w) }}
      </button>
    </div>

    <p v-if="error" class="fl__msg fl__msg--err">{{ error }}</p>
    <p v-if="loading" class="fl__msg">{{ t('common.loading') }}</p>
    <p v-else-if="isEmpty" class="fl__msg">{{ t('flow.empty') }}</p>

    <p v-else class="fl__total">{{ t('flow.total', { n: fmtInt(totalValue) }) }}</p>

    <div v-for="(group, i) in layers" :key="i" class="fl__layer">
      <h2 class="fl__layer-title">
        <span class="fl__layer-index">{{ i + 1 }}/{{ FLOW_LAYER_COUNT }}</span>
        <span>{{ layerLabel(i) }}</span>
      </h2>

      <p v-if="group.length === 0" class="fl__layer-empty">{{ t('flow.layerEmpty') }}</p>

      <ul v-else class="fl__list">
        <li v-for="n in group" :key="n.id" class="fl__item">
          <p class="fl__item-name">{{ n.label }}</p>
          <ul v-if="outLinksOf(n).length" class="fl__edges">
            <li v-for="l in outLinksOf(n)" :key="l.source + '>' + l.target" class="fl__edge">
              <span class="fl__edge-arrow" aria-hidden="true">→</span>
              <span class="fl__edge-target">{{ targetLabel(l) }}</span>
              <!-- ★ 边的来源任务：去掉它，两条不同来源的边会看起来一样 -->
              <span class="fl__edge-task">{{ taskTextOf(l) }}</span>
              <span class="fl__edge-num">
                <span class="fl__edge-value">{{ fmtInt(l.value) }}</span>
                <span class="fl__edge-share">{{ shareText(l.value) }}</span>
              </span>
            </li>
          </ul>
          <p v-else class="fl__item-leaf">{{ t('flow.leaf') }}</p>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.fl {
  padding: var(--app-space-3);
}
.mx-chips {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: var(--app-space-3);
}
.fl__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.fl__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.fl__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.fl__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.fl__total {
  margin: 0 0 var(--app-space-3);
  color: var(--app-text-secondary);
  font-size: 12px;
}
.fl__layer {
  margin-bottom: var(--app-space-3);
}
.fl__layer-title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.fl__layer-index {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-primary-soft);
  color: var(--app-primary);
  font-size: 10px;
  font-weight: 600;
}
.fl__layer-empty {
  margin: 0;
  padding: 8px 10px;
  border: 1px dashed var(--app-border);
  border-radius: var(--app-radius-sm);
  color: var(--app-text-muted);
  font-size: 11px;
}
.fl__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.fl__item {
  padding: var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.fl__item-name {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-word;
}
.fl__item-leaf {
  margin: 4px 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
}
.fl__edges {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0 0 0 var(--app-space-2);
  border-left: 1px solid var(--app-border);
}
.fl__edge {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  /* ★ 48px 是 R1 对新增触控控件的下限。链路行虽然只读，
     但它在手机上是要被逐条读/被点开的，24px 一行在手指下太密。
     为了不把列表撑太长，上方 .fl__item 的 padding 同时收紧。 */
  min-height: 48px;
}
.fl__edge-arrow {
  color: var(--app-text-muted);
  font-size: 11px;
}
.fl__edge-target {
  font-size: 12px;
  color: var(--app-text-secondary);
  word-break: break-word;
  min-width: 0;
}
.fl__edge-task {
  padding: 0 5px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 10px;
  white-space: nowrap;
}
.fl__edge-num {
  margin-left: auto;
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
  font-variant-numeric: tabular-nums;
}
.fl__edge-value {
  font-size: 12px;
  font-weight: 600;
  color: var(--app-text);
}
.fl__edge-share {
  font-size: 10px;
  color: var(--app-text-muted);
}
</style>
