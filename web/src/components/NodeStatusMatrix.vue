<script setup lang="ts">
/**
 * NodeStatusMatrix — 节点状态矩阵（V3.2 FE-A2）
 *
 * KEEP: 等待 V3.2 BE-A4 wire SetNodeStatusProvider + liveStreamStore 加 nodesRef state 后启用。
 * 当前为 WIP（`.v32wip` 后缀）— 引用了尚未实现的 nodesRef / LiveNodeStatus type，
 * 重命名为 .vue 会编译失败。@v3-team 2026-Q3 review.
 *
 * 展示所有可用节点的四态（circuit/availability/quota/health）+ 在途请求数，
 * 支持点击节点 → 抽屉：立即测试（test-now）+ 启用/禁用切换（enable）。
 *
 * 数据源：liveStreamStore 的 node_update SSE 消息（BE-A4）；
 * 操作：POST /api/admin/providers/{id}/test-now、PATCH .../enable（BE-A2）。
 *
 * 设计约束：var(--kx-*) token；三态；操作有 loading/成功/失败反馈；禁用需 confirm。
 */
import { ref, computed } from 'vue'
import { nodesRef, type LiveNodeStatus } from '../composables/liveStreamStore'
import { authBearer } from '../store'

const nodes = nodesRef

// 选中节点的抽屉
const selectedNode = ref<LiveNodeStatus | null>(null)
const drawerVisible = ref(false)

// 操作状态
const testing = ref(false)
const testResult = ref<{ latency_ms: number; status: string; error?: string } | null>(null)
const toggling = ref(false)
const opError = ref('')

// 节点状态汇总（用于矩阵卡片颜色）
function nodeHealth(n: LiveNodeStatus): 'ok' | 'warn' | 'danger' | 'disabled' {
  if (n.manual_disabled) return 'disabled'
  if (n.circuit_state === 'open' || n.health_status === 'unreachable') return 'danger'
  if (n.circuit_state === 'half_open' || n.availability_state === 'cooling' ||
      n.quota_state?.includes('exhausted')) return 'warn'
  return 'ok'
}

const sortedNodes = computed(() => {
  return [...nodes.value].sort((a, b) => {
    // 异常节点排前面
    const order = { danger: 0, warn: 1, ok: 2, disabled: 3 }
    return order[nodeHealth(a)] - order[nodeHealth(b)]
  })
})

function openNode(n: LiveNodeStatus) {
  selectedNode.value = n
  testResult.value = null
  opError.value = ''
  drawerVisible.value = true
}

// 立即测试（同步 probe，5s 超时）
async function testNow() {
  if (!selectedNode.value) return
  testing.value = true
  testResult.value = null
  opError.value = ''
  try {
    const resp = await fetch(`/api/admin/providers/${selectedNode.value.provider_id}/test-now`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${authBearer()}` },
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    testResult.value = await resp.json()
  } catch (e: any) {
    opError.value = e?.message || '测试失败'
  } finally {
    testing.value = false
  }
}

// 启用/禁用切换（二次确认）
async function toggleEnable() {
  if (!selectedNode.value) return
  const target = !selectedNode.value.manual_disabled
  if (target === false && !confirm(`确认禁用节点 ${selectedNode.value.credential_id}？禁用后将立即从路由候选中摘除。`)) {
    return
  }
  toggling.value = true
  opError.value = ''
  try {
    const resp = await fetch(`/api/admin/providers/${selectedNode.value.provider_id}/enable`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${authBearer()}` },
      body: JSON.stringify({ enabled: target }),
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    // 乐观更新（SSE node_update 会在 2s 内同步真实状态）
    selectedNode.value.manual_disabled = !target
  } catch (e: any) {
    opError.value = e?.message || '操作失败'
  } finally {
    toggling.value = false
  }
}

const hasNodes = computed(() => nodes.value.length > 0)
</script>

<template>
  <div class="node-matrix">
    <div class="nm-header">
      <span class="nm-title">节点状态矩阵</span>
      <span class="nm-count">{{ nodes.length }} 个节点</span>
    </div>

    <!-- 空态 -->
    <div v-if="!hasNodes" class="nm-empty">
      <span class="nm-empty-text">暂无节点数据（等待 node_update 推送）</span>
    </div>

    <!-- 节点矩阵 -->
    <div v-else class="nm-grid">
      <div
        v-for="n in sortedNodes"
        :key="n.credential_id"
        class="nm-card"
        :class="`nm-card--${nodeHealth(n)}`"
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
          <span v-if="n.manual_disabled" class="nm-disabled-tag">已禁用</span>
        </div>
      </div>
    </div>

    <!-- 节点详情抽屉 -->
    <Teleport to="body">
      <div v-if="drawerVisible" class="nm-drawer-mask" @click="drawerVisible = false" />
      <div v-if="drawerVisible" class="nm-drawer">
        <div class="nm-drawer-header">
          <span>节点 {{ selectedNode?.credential_id }} 详情</span>
          <button class="nm-close" @click="drawerVisible = false">×</button>
        </div>
        <div v-if="selectedNode" class="nm-drawer-body">
          <div class="nm-detail-row"><span>供应商</span><span>{{ selectedNode.provider_code || selectedNode.provider_id }}</span></div>
          <div class="nm-detail-row"><span>熔断状态</span><span>{{ selectedNode.circuit_state }}</span></div>
          <div class="nm-detail-row"><span>可用性</span><span>{{ selectedNode.availability_state }}</span></div>
          <div class="nm-detail-row"><span>配额</span><span>{{ selectedNode.quota_state }}</span></div>
          <div class="nm-detail-row"><span>健康</span><span>{{ selectedNode.health_status }}</span></div>
          <div class="nm-detail-row"><span>手动禁用</span><span>{{ selectedNode.manual_disabled ? '是' : '否' }}</span></div>
          <div v-if="selectedNode.last_error" class="nm-detail-row nm-detail-row--error">
            <span>最近错误</span><span>{{ selectedNode.last_error }}</span>
          </div>

          <!-- 操作区 -->
          <div class="nm-actions">
            <button class="nm-btn nm-btn--primary" :disabled="testing" @click="testNow">
              {{ testing ? '测试中…' : '立即测试' }}
            </button>
            <button
              class="nm-btn"
              :class="selectedNode.manual_disabled ? 'nm-btn--success' : 'nm-btn--danger'"
              :disabled="toggling"
              @click="toggleEnable"
            >
              {{ toggling ? '处理中…' : selectedNode.manual_disabled ? '启用节点' : '禁用节点' }}
            </button>
          </div>

          <!-- 测试结果 -->
          <div v-if="testResult" class="nm-test-result" :class="`nm-test-result--${testResult.status === 'healthy' ? 'ok' : 'error'}`">
            <div>状态：{{ testResult.status }}</div>
            <div>延迟：{{ testResult.latency_ms }}ms</div>
            <div v-if="testResult.error">错误：{{ testResult.error }}</div>
          </div>
          <div v-if="opError" class="nm-op-error">{{ opError }}</div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.node-matrix {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-md, 8px);
  padding: 12px 16px;
  margin-bottom: 12px;
}
.nm-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 10px;
}
.nm-title { font-weight: 600; color: var(--kx-text); }
.nm-count { font-size: 12px; color: var(--kx-text-secondary); }
.nm-empty { padding: 16px; text-align: center; }
.nm-empty-text { color: var(--kx-text-secondary); font-size: 13px; }
.nm-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 10px;
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

/* 抽屉 */
.nm-drawer-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  z-index: 1000;
}
.nm-drawer {
  position: fixed;
  top: 0;
  right: 0;
  width: 360px;
  height: 100vh;
  background: var(--kx-surface);
  border-left: 1px solid var(--kx-border);
  z-index: 1001;
  overflow-y: auto;
  box-shadow: var(--kx-shadow-lg);
}
.nm-drawer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px;
  border-bottom: 1px solid var(--kx-border);
  font-weight: 600;
  color: var(--kx-text);
}
.nm-close {
  background: none;
  border: none;
  font-size: 24px;
  cursor: pointer;
  color: var(--kx-text-secondary);
}
.nm-drawer-body { padding: 16px; }
.nm-detail-row {
  display: flex;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--kx-border-light);
  font-size: 13px;
  color: var(--kx-text);
}
.nm-detail-row--error { color: var(--kx-danger); }
.nm-actions {
  display: flex;
  gap: 8px;
  margin-top: 16px;
}
.nm-btn {
  flex: 1;
  padding: 8px 12px;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm, 6px);
  background: var(--kx-surface);
  color: var(--kx-text);
  cursor: pointer;
  font-size: 13px;
  transition: opacity 0.15s ease;
}
.nm-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.nm-btn--primary { background: var(--kx-primary); color: var(--kx-text-on-primary, #fff); border-color: var(--kx-primary); }
.nm-btn--success { background: var(--kx-success); color: var(--kx-text-on-primary, #fff); border-color: var(--kx-success); }
.nm-btn--danger { background: var(--kx-danger); color: var(--kx-text-on-primary, #fff); border-color: var(--kx-danger); }
.nm-test-result {
  margin-top: 12px;
  padding: 10px;
  border-radius: var(--kx-radius-sm, 6px);
  font-size: 13px;
}
.nm-test-result--ok { background: var(--kx-success-bg, rgba(103, 194, 58, 0.12)); color: var(--kx-success); }
.nm-test-result--error { background: var(--kx-danger-bg, rgba(245, 108, 108, 0.12)); color: var(--kx-danger); }
.nm-op-error {
  margin-top: 12px;
  padding: 10px;
  border-radius: var(--kx-radius-sm, 6px);
  background: var(--kx-danger-bg, rgba(245, 108, 108, 0.12));
  color: var(--kx-danger);
  font-size: 13px;
}
</style>
