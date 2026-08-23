<script setup lang="ts">
/**
 * NodeStatusMatrix — 节点状态矩阵（V3.2 FE-A2 → 2026-08-18 按需弹窗化）
 *
 * 全量节点按健康分组（异常/警告/正常/已禁用）的全景视图，点击卡片打开统一详情抽屉。
 * 队列视图日常视角已由指标条 + 模型分组节点卡片覆盖，本矩阵降级为按需全景：
 * 页面只渲染一个带健康摘要的触发按钮，弹窗内容 v-if 挂载，默认零渲染、零交互。
 *
 * 数据源：liveStreamStore 的 node_update SSE 消息（BE-A4）。
 *
 * 设计约束：var(--kx-*) token；三态；操作有 loading/成功/失败反馈；禁用需 confirm。
 */
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { nodesRef, type LiveNodeStatus } from '../composables/liveStreamStore'
import NodeDetailDrawer from './NodeDetailDrawer.vue'
import { credentialDisplayName } from '../composables/useCredentialLabels'

const { t } = useI18n()
const nodes = nodesRef

// 弹窗默认不显示：内容 v-if 挂载，关闭即销毁，不做任何默认渲染/交互
const showDialog = ref(false)

// 选中节点的统一详情抽屉
const selectedNode = ref<LiveNodeStatus | null>(null)
const drawerVisible = ref(false)

// 节点状态汇总（用于矩阵卡片颜色）
function nodeHealth(n: LiveNodeStatus): 'ok' | 'warn' | 'danger' | 'disabled' {
  if (n.manual_disabled) return 'disabled'
  if (n.circuit_state === 'open' || n.health_status === 'unreachable') return 'danger'
  if (n.circuit_state === 'half_open' || n.availability_state === 'cooling' ||
    n.quota_state?.includes('exhausted')) return 'warn'
  return 'ok'
}

// 分组节点：按健康状态分组
const groupedNodes = computed(() => {
  const groups = {
    danger: [] as LiveNodeStatus[],
    warn: [] as LiveNodeStatus[],
    ok: [] as LiveNodeStatus[],
    disabled: [] as LiveNodeStatus[]
  }
  nodes.value.forEach(n => {
    groups[nodeHealth(n)].push(n)
  })
  return groups
})

// 按渲染顺序提供 i18n key + 色调，单一模板遍历四组，移除原 4 段重复代码。
const healthGroups = computed(() => ([
  { key: 'danger' as const, tone: 'danger', titleKey: 'requestJourneys.matrix.groupDanger', nodes: groupedNodes.value.danger },
  { key: 'warn' as const, tone: 'warn', titleKey: 'requestJourneys.matrix.groupWarn', nodes: groupedNodes.value.warn },
  { key: 'ok' as const, tone: 'ok', titleKey: 'requestJourneys.matrix.groupOk', nodes: groupedNodes.value.ok },
  { key: 'disabled' as const, tone: 'disabled', titleKey: 'requestJourneys.matrix.groupDisabled', nodes: groupedNodes.value.disabled },
]))

// 触发按钮上的健康摘要（异常+警告），让全景入口本身可扫读
const abnormalCount = computed(() => groupedNodes.value.danger.length + groupedNodes.value.warn.length)

// 节点状态点工具：返回 ok/warn/danger 三态。null = 字段未上报（按未知灰点展示）。
function circuitDotClass(n: LiveNodeStatus): string {
  if (!n.circuit_state) return 'nm-dot--warn'
  return `nm-dot--${n.circuit_state === 'closed' ? 'ok' : n.circuit_state === 'open' ? 'danger' : 'warn'}`
}
function availabilityDotClass(n: LiveNodeStatus): string {
  if (!n.availability_state) return 'nm-dot--warn'
  return `nm-dot--${n.availability_state === 'ready' || n.availability_state === 'active' ? 'ok' : 'warn'}`
}
function quotaDotClass(n: LiveNodeStatus): string {
  if (!n.quota_state) return 'nm-dot--warn'
  return `nm-dot--${n.quota_state === 'ok' ? 'ok' : 'danger'}`
}
function healthDotClass(n: LiveNodeStatus): string {
  if (!n.health_status) return 'nm-dot--warn'
  return `nm-dot--${n.health_status === 'healthy' ? 'ok' : n.health_status === 'unreachable' ? 'danger' : 'warn'}`
}

// 折叠状态（默认只展开异常和警告）
const collapsedGroups = ref({
  danger: false,
  warn: false,
  ok: true,
  disabled: true
})

function toggleGroup(group: 'danger' | 'warn' | 'ok' | 'disabled') {
  collapsedGroups.value[group] = !collapsedGroups.value[group]
}

function openNode(n: LiveNodeStatus) {
  selectedNode.value = n
  drawerVisible.value = true
}

function closeDialog() {
  showDialog.value = false
  drawerVisible.value = false
  selectedNode.value = null
}

// 关闭弹窗时同步清掉详情抽屉的引用并把抽屉状态重置，
// 避免关闭后抽屉缓存的 stale 状态随再次打开泄漏。
let escapeHandler: ((event: KeyboardEvent) => void) | null = null
function attachEscapeClose() {
  if (escapeHandler) return
  escapeHandler = (event: KeyboardEvent) => {
    if (event.key === 'Escape') closeDialog()
  }
  window.addEventListener('keydown', escapeHandler)
}
function detachEscapeClose() {
  if (escapeHandler) {
    window.removeEventListener('keydown', escapeHandler)
    escapeHandler = null
  }
}
onBeforeUnmount(detachEscapeClose)
watch(showDialog, (open) => {
  if (!open) {
    drawerVisible.value = false
    selectedNode.value = null
    detachEscapeClose()
    return
  }
  // 打开弹窗后注册 Escape 关闭：键盘可达性补齐（与 A11y 增强一致）。
  attachEscapeClose()
})

const hasNodes = computed(() => nodes.value.length > 0)
</script>

<template>
  <div class="node-matrix">
    <button
      type="button"
      class="nm-trigger"
      aria-haspopup="dialog"
      :aria-expanded="showDialog"
      @click="showDialog = true"
      @keydown.enter.prevent="showDialog = true"
      @keydown.space.prevent="showDialog = true"
    >
      <span class="nm-trigger-title">{{ t('requestJourneys.matrix.triggerTitle') }}</span>
      <span v-if="hasNodes" class="nm-trigger-meta" :class="{ 'nm-trigger-meta--warn': abnormalCount > 0 }">
        {{ t('requestJourneys.matrix.triggerMeta', { total: nodes.length }) }}<template v-if="abnormalCount > 0"> · {{ t('requestJourneys.matrix.abnormalCount', { count: abnormalCount }) }}</template>
      </span>
      <span v-else class="nm-trigger-meta">{{ t('requestJourneys.matrix.empty') }}</span>
      <span class="nm-trigger-hint">{{ t('requestJourneys.matrix.viewAll') }}</span>
    </button>

    <Teleport to="body">
      <div v-if="showDialog" class="nm-modal-mask" @click.self="closeDialog">
        <section
          class="nm-modal"
          role="dialog"
          aria-modal="true"
          :aria-label="t('requestJourneys.matrix.triggerTitle')"
        >
          <div class="nm-modal-header">
            <div>
              <h3>{{ t('requestJourneys.matrix.triggerTitle') }}</h3>
              <p class="nm-modal-sub">{{ t('requestJourneys.matrix.modalSubtitle') }}</p>
            </div>
            <button
              type="button"
              class="nm-modal-close"
              :title="t('requestJourneys.matrix.closeLabel')"
              :aria-label="t('requestJourneys.matrix.closeLabel')"
              @click="closeDialog"
            >×</button>
          </div>

          <!-- 空态 -->
          <div v-if="!hasNodes" class="nm-empty">
            <span class="nm-empty-text">{{ t('requestJourneys.matrix.empty') }}（{{ t('requestJourneys.matrix.emptyHint') }}）</span>
          </div>

          <!-- 节点矩阵（分组显示） -->
          <div v-else class="nm-groups">
            <template v-for="group in healthGroups" :key="group.key">
              <div v-if="group.nodes.length > 0" class="nm-group">
                <button type="button" class="nm-group-header" :aria-expanded="!collapsedGroups[group.key]" @click="toggleGroup(group.key)" @keydown.enter.prevent="toggleGroup(group.key)" @keydown.space.prevent="toggleGroup(group.key)">
                  <span :class="['nm-group-title', `nm-group-title--${group.tone}`]">{{ t(group.titleKey, { count: group.nodes.length }) }}</span>
                  <span class="nm-collapse-icon">{{ collapsedGroups[group.key] ? '▶' : '▼' }}</span>
                </button>
                <div v-if="!collapsedGroups[group.key]" class="nm-grid">
                  <button
                    type="button"
                    v-for="n in group.nodes"
                    :key="n.credential_id"
                    :class="['nm-card', `nm-card--${group.tone}`]"
                    @click="openNode(n)"
                    @keydown.enter.prevent="openNode(n)"
                    @keydown.space.prevent="openNode(n)"
                  >
                    <div class="nm-card-header">
                      <span class="nm-card-id">{{ credentialDisplayName(n.credential_id) }}</span>
                      <span class="nm-card-provider">{{ n.provider_code || `P${n.provider_id}` }}</span>
                    </div>
                    <div class="nm-card-states">
                      <span class="nm-state" :title="`${t('requestJourneys.matrix.circuit')}: ${n.circuit_state || '—'}`">
                        <i :class="['nm-dot', circuitDotClass(n)]" />
                        {{ t('requestJourneys.matrix.circuit') }}
                      </span>
                      <span class="nm-state" :title="`${t('requestJourneys.matrix.availability')}: ${n.availability_state || '—'}`">
                        <i :class="['nm-dot', availabilityDotClass(n)]" />
                        {{ t('requestJourneys.matrix.availability') }}
                      </span>
                      <span class="nm-state" :title="`${t('requestJourneys.matrix.quota')}: ${n.quota_state || '—'}`">
                        <i :class="['nm-dot', quotaDotClass(n)]" />
                        {{ t('requestJourneys.matrix.quota') }}
                      </span>
                      <span class="nm-state" :title="`${t('requestJourneys.matrix.health')}: ${n.health_status || '—'}`">
                        <i :class="['nm-dot', healthDotClass(n)]" />
                        {{ t('requestJourneys.matrix.health') }}
                      </span>
                    </div>
                    <div class="nm-card-footer">
                      <span v-if="n.in_flight" class="nm-inflight">{{ t('requestJourneys.matrix.inFlight', { count: n.in_flight }) }}</span>
                      <span v-if="n.last_latency_ms" class="nm-latency">{{ t('requestJourneys.matrix.lastLatency', { ms: n.last_latency_ms }) }}</span>
                      <span v-if="group.key === 'disabled'" class="nm-disabled-tag">{{ t('requestJourneys.matrix.disabledTag') }}</span>
                    </div>
                  </button>
                </div>
              </div>
            </template>
          </div>

          <NodeDetailDrawer
            v-model="drawerVisible"
            :node="selectedNode"
            @applied="drawerVisible = true"
          />
        </section>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.node-matrix {
  min-width: 0;
}
/* 页面常驻的只有一个紧凑触发按钮：健康摘要可扫读，全景按需打开 */
.nm-trigger {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm, 6px);
  background: var(--kx-surface);
  color: var(--kx-text);
  cursor: pointer;
  text-align: left;
}
.nm-trigger:hover { border-color: var(--kx-primary); }
.nm-trigger-title { font-weight: 600; font-size: 13px; }
.nm-trigger-meta { color: var(--kx-text-secondary); font-size: 11px; }
.nm-trigger-meta--warn { color: var(--kx-danger); font-weight: 600; }
.nm-trigger-hint { margin-left: auto; color: var(--kx-text-secondary); font-size: 11px; }

/* 按需弹窗：内容 v-if 挂载，关闭即销毁 */
.nm-modal-mask {
  position: fixed;
  inset: 0;
  z-index: 2900;
  background: rgba(0, 0, 0, 0.38);
  display: flex;
  justify-content: flex-end;
}
.nm-modal {
  width: min(860px, 96vw);
  height: 100vh;
  overflow: auto;
  background: var(--kx-surface);
  color: var(--kx-text);
  box-shadow: -10px 0 30px rgba(0, 0, 0, 0.24);
  padding: 18px 20px 28px;
  box-sizing: border-box;
}
.nm-modal-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 12px;
}
.nm-modal-header h3 { margin: 0; font-size: 15px; }
.nm-modal-sub { margin: 4px 0 0; color: var(--kx-text-secondary); font-size: 11px; }
.nm-modal-close {
  background: none;
  border: none;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  color: var(--kx-text-secondary);
}
.nm-empty { padding: 16px; text-align: center; }
.nm-empty-text { color: var(--kx-text-secondary); font-size: 13px; }

/* 分组样式 */
.nm-groups {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.nm-group {
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm, 6px);
  overflow: hidden;
}
.nm-group-header {
  display: flex;
  width: 100%;
  justify-content: space-between;
  align-items: center;
  padding: 10px 12px;
  background: var(--kx-bg-elevated);
  cursor: pointer;
  user-select: none;
  text-align: left;
  border: none;
  transition: background 0.15s ease;
}
.nm-group-header:hover {
  background: var(--kx-border);
}
.nm-group-title {
  font-size: 13px;
  font-weight: 600;
}
.nm-group-title--danger { color: var(--kx-danger); }
.nm-group-title--warn { color: var(--kx-warning); }
.nm-group-title--ok { color: var(--kx-success); }
.nm-group-title--disabled { color: var(--kx-text-secondary); }
.nm-collapse-icon {
  font-size: 11px;
  color: var(--kx-text-secondary);
  transition: transform 0.2s ease;
}

.nm-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 10px;
  padding: 12px;
}
.nm-card {
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm, 6px);
  padding: 10px;
  cursor: pointer;
  text-align: left;
  background: var(--kx-surface);
  font: inherit;
  color: inherit;
  transition: transform 0.15s ease, box-shadow 0.15s ease;
}
.nm-card:hover { transform: translateY(-2px); box-shadow: var(--kx-shadow-sm); }
.nm-card--ok { border-left: 3px solid var(--kx-success); }
.nm-card--warn { border-left: 3px solid var(--kx-warning); }
.nm-card--danger { border-left: 3px solid var(--kx-danger); }
.nm-card--disabled { border-left: 3px solid var(--kx-text-secondary); opacity: 0.6; }
.nm-card-header {
  display: flex;
  justify-content: space-between;
  margin-bottom: 6px;
}
.nm-card-id { font-weight: 600; font-size: 13px; color: var(--kx-text); }
.nm-card-provider { font-size: 12px; color: var(--kx-text-secondary); }
.nm-card-states {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
}
.nm-state {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
  color: var(--kx-text-secondary);
}
.nm-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  display: inline-block;
}
.nm-dot--ok { background: var(--kx-success); }
.nm-dot--warn { background: var(--kx-warning); }
.nm-dot--danger { background: var(--kx-danger); }
.nm-card-footer {
  display: flex;
  gap: 8px;
  font-size: 11px;
  color: var(--kx-text-secondary);
}
.nm-inflight { color: var(--kx-primary); }
.nm-latency { color: var(--kx-text); }
.nm-disabled-tag { color: var(--kx-danger); }
</style>