<script setup lang="ts">
// ProbeView — 探测面（/probe）：现在有什么在探测、谁探测得慢。
//
// 数据源：
//   GET /api/admin/probe/queue-tasks       探测队列当前任务（integrity 源）
//   GET /api/admin/probe/provider-latency  供应商最近一次成功直连探测延时
//   GET /api/admin/probe/node-tasks        节点探测队列（node_probe 源，2026-10-07 加入）
// 三者都 adminWrap = AdminMiddleware ⇒ tenant_admin 可用。
//
// ★ queue-tasks 与 node-tasks 是**两个不同的队列**，不是同一个的两种视图：
//   · queue-tasks 来源 credential_probe_queue（完整性探测规划器），source="integrity"
//   · node-tasks 来源 node_probe_state（错误触发的 NodeProbeWorker），source="node_probe"
//   后端 NodeProbeTaskRow.Source 的注释明说是为了「让前端能一致地 badge/合并两队列的行」。
//   ⇒ 两段分开渲染，各自带自己的空态与错误。
//
// 它补的是 NodesView 的**结论**页缺的那一半：NodesView 展示 node_probe_state
// 的判定（健康/故障/可疑），本页展示**得出该判定的过程**——
// 排到第几次、下次什么时候重试、上次结果是什么、供应商直连一次要多久。
// 「这一条为什么被判成可疑」经常只能从这里看出来。
//
// ⚠️ 两条端点都不支持分页，也不接受过滤参数 ⇒ 不套 ContinuousListController，
//   一次拉完。provider-latency 更是**不接受任何参数**（写死 1h 窗口 + LIMIT 500）。
//
// ⚠️ 两个「没出现 ≠ 不存在」必须显式说：
//   · provider-latency 只统计 `direct_ok AND direct_latency_ms > 0` 且在 1h 内
//     ⇒ 某供应商不在列表里 = 最近一小时没有成功的直连探测，不等于供应商没了
//   · taskStatusTone 对词表外的状态给 muted 而不是 success

import { onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchProbeQueueTasks,
  fetchProviderLatency,
  taskStatusTone,
  taskLabel,
  PROBE_TASKS_DEFAULT_LIMIT,
  type ProbeQueueTask,
  type ProviderLatencyEntry,
} from '@/api/probeOps'
import {
  fetchProbeNodeTasks,
  nodeStatusTone,
  nodeStatusKeyOf,
  latencyOf,
  needsManualAction,
  type NodeProbeTaskRow,
} from '@/api/probeModelHealth'

useHyperPage({ title: () => t('probe.title') })

const tasks = ref<ProbeQueueTask[]>([])
const taskTotal = ref(0)
const entries = ref<ProviderLatencyEntry[]>([])
const latencyTotal = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)
/**
 * ★ 两个端点**分别**记错误。合并成一个 error 会让「队列挂了」显示成
 *   「整页都挂了」，而 provider-latency 失败并不影响队列那一段 ——
 *   桌面端这里就是 `.catch(() => null)` 静默吞掉的（fetchDispatchWaterfall
 *   旁边的 Promise.all），移动端不照抄。
 */
const taskError = ref<string | null>(null)
const latencyError = ref<string | null>(null)
/** ★ 第三个端点同样**独立**记错误（同上纪律）。 */
const nodeError = ref<string | null>(null)
const nodeTasks = ref<NodeProbeTaskRow[]>([])
const nodeTotal = ref(0)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  const [a, b, c] = await Promise.allSettled([
    fetchProbeQueueTasks({ limit: PROBE_TASKS_DEFAULT_LIMIT }),
    fetchProviderLatency(),
    // ★ 不发 limit ⇒ 用后端自己的默认 120（node-tasks 的默认与 queue-tasks
    //   的 100 **不同**，见 probeModelHealth.ts 文件头 (3)）。
    fetchProbeNodeTasks(),
  ])
  if (a.status === 'fulfilled') {
    tasks.value = a.value.tasks ?? []
    taskTotal.value = a.value.total ?? tasks.value.length
    taskError.value = null
  } else {
    tasks.value = []
    taskTotal.value = 0
    taskError.value = (a.reason as Error)?.message ?? null
  }
  if (b.status === 'fulfilled') {
    entries.value = b.value.entries ?? []
    latencyTotal.value = b.value.total ?? entries.value.length
    latencyError.value = null
  } else {
    entries.value = []
    latencyTotal.value = 0
    latencyError.value = (b.reason as Error)?.message ?? null
  }
  if (c.status === 'fulfilled') {
    nodeTasks.value = c.value.tasks ?? []
    nodeTotal.value = c.value.total ?? nodeTasks.value.length
    nodeError.value = null
  } else {
    nodeTasks.value = []
    nodeTotal.value = 0
    nodeError.value = (c.reason as Error)?.message ?? null
  }
  if (a.status === 'rejected' && b.status === 'rejected' && c.status === 'rejected') {
    error.value = taskError.value ?? latencyError.value ?? nodeError.value
  }
  loading.value = false
}
void load()

onBeforeUnmount(() => {
  // 没有定时器/监听器要清；置空避免闭包持有已卸载组件引用
  tasks.value = []
  entries.value = []
  nodeTasks.value = []
})

/**
 * ★ 延时文案。`last_latency_ms` 是 `*int` + omitempty ⇒ 缺失 ≠ 0ms。
 * 写成模板里两个 `latencyOf(tk)` 调用收不窄类型（两次调用互不关联），
 * 所以提取成函数一次取值。
 */
function nodeLatencyText(tk: NodeProbeTaskRow): string {
  const ms = latencyOf(tk)
  return ms === null ? t('probe.noLatency') : t('probe.latencyMs', { ms })
}

function resultText(tk: ProbeQueueTask): string | null {
  const code = tk.result_http_status
  const ms = tk.result_latency_ms
  if (code == null && ms == null) return null
  return t('probe.lastResult', { code: code ?? '—', ms: ms ?? '—' })
}
</script>

<template>
  <div class="view-root pb">
    <p v-if="loading" class="pb__msg">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="pb__msg pb__msg--err">{{ error }}</p>

    <!-- 探测队列 -->
    <section class="pb__section">
      <h2 class="pb__title">
        {{ t('probe.queueSection') }}
        <span class="pb__count">{{ t('probe.taskCount', { n: taskTotal }) }}</span>
      </h2>
      <p v-if="taskError" class="pb__sec-err">{{ taskError }}</p>
      <p v-else-if="tasks.length === 0" class="pb__sec-empty">{{ t('probe.emptyQueue') }}</p>
      <ul v-else class="pb__list">
        <li v-for="tk in tasks" :key="tk.id" class="pb__item">
          <div class="pb__item-head">
            <span class="pb__item-name">
              <StatusDot :tone="taskStatusTone(tk.status)" />
              {{ taskLabel(tk) }}
            </span>
            <span class="badge" :class="`badge--${taskStatusTone(tk.status)}`">
              {{ tk.status || t('probe.unknownStatus') }}
            </span>
          </div>
          <div class="pb__fields">
            <span v-if="tk.provider_name" class="pb__field">{{ tk.provider_name }}</span>
            <span class="pb__field">{{ t('probe.attempt', { n: tk.attempt }) }}</span>
            <span class="pb__field">{{ t('probe.priority', { n: tk.priority }) }}</span>
          </div>
          <p v-if="tk.reason_code" class="pb__item-reason">
            {{ t('probe.reason', { c: tk.reason_code }) }}
          </p>
          <p v-if="resultText(tk)" class="pb__item-result">{{ resultText(tk) }}</p>
          <p v-if="tk.next_run_at" class="pb__item-next">
            {{ t('probe.nextRun', { t: relativeTime(tk.next_run_at) }) }}
          </p>
        </li>
      </ul>
    </section>

    <!-- 供应商探测延时 -->
    <section class="pb__section">
      <h2 class="pb__title">
        {{ t('probe.latencySection') }}
        <span class="pb__count">{{ latencyTotal }}</span>
      </h2>
      <!-- ★ 口径说明必须在空态旁边，不然「没出现」会被读成「不存在」 -->
      <p class="pb__hint">
        <AppIcon name="alert" :size="14" />
        <span>{{ t('probe.latencyWindowHint') }}</span>
      </p>
      <p v-if="latencyError" class="pb__sec-err">{{ latencyError }}</p>
      <p v-else-if="entries.length === 0" class="pb__sec-empty">{{ t('probe.emptyLatency') }}</p>
      <ul v-else class="pb__list">
        <li v-for="e in entries" :key="e.provider_id" class="pb__item pb__item--row">
          <span class="pb__item-name">{{ e.provider_name || e.provider_code || `#${e.provider_id}` }}</span>
          <span class="pb__latency">{{ e.latency_ms }}ms</span>
          <span class="pb__field">{{ relativeTime(e.probed_at) }}</span>
        </li>
      </ul>
    </section>

    <!-- ★ 节点探测队列（node_probe 源）。与上面的完整性队列是两个队列。 -->
    <section class="pb__section">
      <h2 class="pb__title">
        {{ t('probe.nodeSection') }}
        <span class="pb__count">{{ t('probe.taskCount', { n: nodeTotal }) }}</span>
      </h2>
      <p v-if="nodeError" class="pb__sec-err">{{ nodeError }}</p>
      <p v-else-if="nodeTasks.length === 0" class="pb__sec-empty">{{ t('probe.emptyNode') }}</p>
      <ul v-else class="pb__list">
        <li
          v-for="tk in nodeTasks"
          :key="tk.credential_id + '|' + tk.standardized_name"
          class="pb__item"
          :class="{ 'pb__item--manual': needsManualAction(tk) }"
        >
          <div class="pb__item-head">
            <span class="pb__item-name">
              <StatusDot :tone="nodeStatusTone(tk.status)" />
              {{ tk.standardized_name || tk.raw_model }}
            </span>
            <span class="badge" :class="`badge--${nodeStatusTone(tk.status)}`">
              {{ t(nodeStatusKeyOf(tk.status)) }}
            </span>
          </div>
          <div class="pb__fields">
            <span v-if="tk.provider_name" class="pb__field">{{ tk.provider_name }}</span>
            <span class="pb__field">{{ t('probe.attempt', { n: tk.attempt }) }}</span>
            <!-- ★ 延时缺失 ⇒ 「—」。0ms 是真实值，两者不能混（见 latencyOf）。 -->
            <span class="pb__field">{{ nodeLatencyText(tk) }}</span>
          </div>
          <p v-if="tk.last_err_code" class="pb__item-reason">
            {{ t('probe.reason', { c: tk.last_err_code }) }}
          </p>
          <!-- ★ 「等一等就好」与「等再久也不会好」必须分得开。 -->
          <p v-if="needsManualAction(tk)" class="pb__item-manual">{{ t('probe.needsManual') }}</p>
          <p v-if="tk.next_retry_at" class="pb__item-next">
            {{ t('probe.nextRun', { t: relativeTime(tk.next_retry_at) }) }}
          </p>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.pb {
  padding: var(--app-space-3);
}
.pb__item--manual {
  border-left: 3px solid var(--app-danger);
}
.pb__item-manual {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-danger);
}
.pb__msg {
  padding: var(--app-space-4) 0;
  color: var(--app-text-muted);
  font-size: 14px;
  text-align: center;
}
.pb__msg--err {
  color: var(--app-danger);
}
.pb__section {
  margin-bottom: var(--app-space-4);
}
.pb__title {
  display: flex;
  align-items: baseline;
  gap: var(--app-space-2);
  margin: 0 0 var(--app-space-2);
  font-size: 14px;
  font-weight: 600;
  color: var(--app-text);
}
.pb__count {
  font-size: 12px;
  font-weight: 400;
  color: var(--app-text-muted);
}
.pb__hint {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.pb__sec-empty {
  margin: 0;
  padding: var(--app-space-3) 0;
  color: var(--app-text-muted);
  font-size: 13px;
  text-align: center;
}
.pb__sec-err {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 12px;
  word-break: break-word;
}
.pb__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.pb__item {
  padding: var(--app-space-2) var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.pb__item--row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  min-height: 48px;
}
.pb__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.pb__item-name {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pb__fields {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: 4px;
}
.pb__field {
  font-size: 12px;
  color: var(--app-text-muted);
  white-space: nowrap;
}
.pb__latency {
  margin-left: auto;
  font-size: 14px;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.pb__item-reason {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.pb__item-result {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--app-text-muted);
}
.pb__item-next {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}
</style>
