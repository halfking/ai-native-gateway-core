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
import { ref, computed } from 'vue'
import { nodesRef, type LiveNodeStatus } from '../composables/liveStreamStore'
import NodeDetailDrawer from './NodeDetailDrawer.vue'

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

// 触发按钮上的健康摘要（异常+警告），让全景入口本身可扫读
const abnormalCount = computed(() => groupedNodes.value.danger.length + groupedNodes.value.warn.length)

// 折叠状态（默认只展开异常和警告）
const collapsedGroups = ref({
  danger: false,
  warn: false,
  ok: true,
  disabled: true
})

function toggleGroup(group: keyof typeof collapsedGroups.value) {
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

const hasNodes = computed(() => nodes.value.length > 0)
</script>

<template>
  <div class="node-matrix">
    <button type="button" class="nm-trigger" @click="showDialog = true">
      <span class="nm-trigger-title">节点状态矩阵</span>
      <span v-if="hasNodes" class="nm-trigger-meta" :class="{ 'nm-trigger-meta--warn': abnormalCount > 0 }">
        {{ nodes.length }} 个节点<template v-if="abnormalCount > 0"> · 异常 {{ abnormalCount }}</template>
      </span>
      <span v-else class="nm-trigger-meta">暂无节点数据</span>
      <span class="nm-trigger-hint">查看全景</span>
    </button>

    <Teleport to="body">
      <div v-if="showDialog" class="nm-modal-mask" @click.self="closeDialog">
        <section class="nm-modal" role="dialog" aria-modal="true" aria-label="节点状态矩阵">
          <div class="nm-modal-header">
            <div>
              <h3>节点状态矩阵</h3>
              <p class="nm-modal-sub">全量节点按健康分组的全景；点击卡片查看模型×节点详情与维护</p>
            </div>
            <button type="button" class="nm-modal-close" aria-label="关闭" @click="closeDialog">×</button>
          </div>

          <!-- 空态 -->
          <div v-if="!hasNodes" class="nm-empty">
            <span class="nm-empty-text">暂无节点数据（等待 node_update 推送）</span>
          </div>

          <!-- 节点矩阵（分组显示） -->
          <div v-else class="nm-groups">
      <!-- 异常节点组 -->
      <div v-if="groupedNodes.danger.length > 0" class="nm-group">
        <div class="nm-group-header" @click="toggleGroup('danger')">
          <span class="nm-group-title nm-group-title--danger">
            ⚠️ 异常节点 ({{ groupedNodes.danger.length }})
          </span>
          <span class="nm-collapse-icon">{{ collapsedGroups.danger ? '▶' : '▼' }}</span>
        </div>
        <div v-if="!collapsedGroups.danger" class="nm-grid">
          <div
            v-for="n in groupedNodes.danger"
            :key="n.credential_id"
            class="nm-card nm-card--danger"
            @click="openNode(n)"
          >
            <div class="nm-card-header">
              <span class="nm-card-id">节点 {{ n.credential_id }}</span>
              <span class="nm-card-provider">{{ n.provider_code || `P${n.provider_id}` }}</span>
            </div>
            <div class="nm-card-states">
              <span class="nm-state" :title="`熔断: ${n.circuit_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.circuit_state === 'closed' ? 'ok' : n.circuit_state === 'open' ? 'danger' : 'warn'}`" />
                熔断
              </span>
              <span class="nm-state" :title="`可用性: ${n.availability_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.availability_state === 'ready' || n.availability_state === 'active' ? 'ok' : 'warn'}`" />
                可用
              </span>
              <span class="nm-state" :title="`配额: ${n.quota_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.quota_state === 'ok' ? 'ok' : 'danger'}`" />
                配额
              </span>
              <span class="nm-state" :title="`健康: ${n.health_status}`">
                <i class="nm-dot" :class="`nm-dot--${n.health_status === 'healthy' ? 'ok' : n.health_status === 'unreachable' ? 'danger' : 'warn'}`" />
                健康
              </span>
            </div>
            <div class="nm-card-footer">
              <span v-if="n.in_flight" class="nm-inflight">在途 {{ n.in_flight }}</span>
              <span v-if="n.last_latency_ms" class="nm-latency">{{ n.last_latency_ms }}ms</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 警告节点组 -->
      <div v-if="groupedNodes.warn.length > 0" class="nm-group">
        <div class="nm-group-header" @click="toggleGroup('warn')">
          <span class="nm-group-title nm-group-title--warn">
            ⚡ 警告节点 ({{ groupedNodes.warn.length }})
          </span>
          <span class="nm-collapse-icon">{{ collapsedGroups.warn ? '▶' : '▼' }}</span>
        </div>
        <div v-if="!collapsedGroups.warn" class="nm-grid">
          <div
            v-for="n in groupedNodes.warn"
            :key="n.credential_id"
            class="nm-card nm-card--warn"
            @click="openNode(n)"
          >
            <div class="nm-card-header">
              <span class="nm-card-id">节点 {{ n.credential_id }}</span>
              <span class="nm-card-provider">{{ n.provider_code || `P${n.provider_id}` }}</span>
            </div>
            <div class="nm-card-states">
              <span class="nm-state" :title="`熔断: ${n.circuit_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.circuit_state === 'closed' ? 'ok' : n.circuit_state === 'open' ? 'danger' : 'warn'}`" />
                熔断
              </span>
              <span class="nm-state" :title="`可用性: ${n.availability_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.availability_state === 'ready' || n.availability_state === 'active' ? 'ok' : 'warn'}`" />
                可用
              </span>
              <span class="nm-state" :title="`配额: ${n.quota_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.quota_state === 'ok' ? 'ok' : 'danger'}`" />
                配额
              </span>
              <span class="nm-state" :title="`健康: ${n.health_status}`">
                <i class="nm-dot" :class="`nm-dot--${n.health_status === 'healthy' ? 'ok' : n.health_status === 'unreachable' ? 'danger' : 'warn'}`" />
                健康
              </span>
            </div>
            <div class="nm-card-footer">
              <span v-if="n.in_flight" class="nm-inflight">在途 {{ n.in_flight }}</span>
              <span v-if="n.last_latency_ms" class="nm-latency">{{ n.last_latency_ms }}ms</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 正常节点组 -->
      <div v-if="groupedNodes.ok.length > 0" class="nm-group">
        <div class="nm-group-header" @click="toggleGroup('ok')">
          <span class="nm-group-title nm-group-title--ok">
            ✅ 正常节点 ({{ groupedNodes.ok.length }})
          </span>
          <span class="nm-collapse-icon">{{ collapsedGroups.ok ? '▶' : '▼' }}</span>
        </div>
        <div v-if="!collapsedGroups.ok" class="nm-grid">
          <div
            v-for="n in groupedNodes.ok"
            :key="n.credential_id"
            class="nm-card nm-card--ok"
            @click="openNode(n)"
          >
            <div class="nm-card-header">
              <span class="nm-card-id">节点 {{ n.credential_id }}</span>
              <span class="nm-card-provider">{{ n.provider_code || `P${n.provider_id}` }}</span>
            </div>
            <div class="nm-card-states">
              <span class="nm-state" :title="`熔断: ${n.circuit_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.circuit_state === 'closed' ? 'ok' : n.circuit_state === 'open' ? 'danger' : 'warn'}`" />
                熔断
              </span>
              <span class="nm-state" :title="`可用性: ${n.availability_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.availability_state === 'ready' || n.availability_state === 'active' ? 'ok' : 'warn'}`" />
                可用
              </span>
              <span class="nm-state" :title="`配额: ${n.quota_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.quota_state === 'ok' ? 'ok' : 'danger'}`" />
                配额
              </span>
              <span class="nm-state" :title="`健康: ${n.health_status}`">
                <i class="nm-dot" :class="`nm-dot--${n.health_status === 'healthy' ? 'ok' : n.health_status === 'unreachable' ? 'danger' : 'warn'}`" />
                健康
              </span>
            </div>
            <div class="nm-card-footer">
              <span v-if="n.in_flight" class="nm-inflight">在途 {{ n.in_flight }}</span>
              <span v-if="n.last_latency_ms" class="nm-latency">{{ n.last_latency_ms }}ms</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 已禁用节点组 -->
      <div v-if="groupedNodes.disabled.length > 0" class="nm-group">
        <div class="nm-group-header" @click="toggleGroup('disabled')">
          <span class="nm-group-title nm-group-title--disabled">
            🚫 已禁用节点 ({{ groupedNodes.disabled.length }})
          </span>
          <span class="nm-collapse-icon">{{ collapsedGroups.disabled ? '▶' : '▼' }}</span>
        </div>
        <div v-if="!collapsedGroups.disabled" class="nm-grid">
          <div
            v-for="n in groupedNodes.disabled"
            :key="n.credential_id"
            class="nm-card nm-card--disabled"
            @click="openNode(n)"
          >
            <div class="nm-card-header">
              <span class="nm-card-id">节点 {{ n.credential_id }}</span>
              <span class="nm-card-provider">{{ n.provider_code || `P${n.provider_id}` }}</span>
            </div>
            <div class="nm-card-states">
              <span class="nm-state" :title="`熔断: ${n.circuit_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.circuit_state === 'closed' ? 'ok' : n.circuit_state === 'open' ? 'danger' : 'warn'}`" />
                熔断
              </span>
              <span class="nm-state" :title="`可用性: ${n.availability_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.availability_state === 'ready' || n.availability_state === 'active' ? 'ok' : 'warn'}`" />
                可用
              </span>
              <span class="nm-state" :title="`配额: ${n.quota_state}`">
                <i class="nm-dot" :class="`nm-dot--${n.quota_state === 'ok' ? 'ok' : 'danger'}`" />
                配额
              </span>
              <span class="nm-state" :title="`健康: ${n.health_status}`">
                <i class="nm-dot" :class="`nm-dot--${n.health_status === 'healthy' ? 'ok' : n.health_status === 'unreachable' ? 'danger' : 'warn'}`" />
                健康
              </span>
            </div>
            <div class="nm-card-footer">
              <span class="nm-disabled-tag">已禁用</span>
            </div>
          </div>
        </div>
      </div>
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
  justify-content: space-between;
  align-items: center;
  padding: 10px 12px;
  background: var(--kx-bg-elevated);
  cursor: pointer;
  user-select: none;
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
