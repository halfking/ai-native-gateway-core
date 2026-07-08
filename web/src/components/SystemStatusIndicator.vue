<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { getHealth, getBackgroundTasksStatus, type HealthResponse, type BackgroundTasksStatus } from '../api/system'

const health = ref<HealthResponse | null>(null)
const bgTasks = ref<BackgroundTasksResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const showPopover = ref(false)
const lastChecked = ref<Date | null>(null)
const timeSinceCheck = ref('')
const dropdownStyle = ref<Record<string, string>>({})

const triggerRef = ref<HTMLElement | null>(null)
const dropdownRef = ref<HTMLElement | null>(null)

let refreshTimer: number | null = null
let timeUpdateTimer: number | null = null

// Update dropdown position to be below the trigger
async function updateDropdownPosition() {
  if (!triggerRef.value) return
  await nextTick()
  const rect = triggerRef.value.getBoundingClientRect()
  dropdownStyle.value = {
    top: `${rect.bottom + 8}px`,
    left: `${rect.left}px`,
  }
}

// Toggle popover
async function togglePopover() {
  showPopover.value = !showPopover.value
  if (showPopover.value) {
    await updateDropdownPosition()
    loadStatus() // Refresh data on open
  }
}

// Close popover when clicking outside
function handleClickOutside(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target.closest('.status-wrapper') && !target.closest('.status-dropdown')) {
    showPopover.value = false
  }
}

const overallStatus = computed(() => {
  if (!health.value) return 'unknown'
  
  const dbOk = !health.value.database || health.value.database.connected
  const redisOk = !health.value.redis || health.value.redis.connected
  
  if (health.value.status === 'ok' && dbOk && redisOk) {
    return 'healthy'
  }
  
  return 'unhealthy'
})

const statusColor = computed(() => {
  switch (overallStatus.value) {
    case 'healthy': return '#34d399'
    case 'unhealthy': return '#f87171'
    default: return '#9ca3af'
  }
})

const statusIcon = computed(() => {
  switch (overallStatus.value) {
    case 'healthy': return '✓'
    case 'unhealthy': return '✗'
    default: return '?'
  }
})

const aliveTasksCount = computed(() => {
  if (!bgTasks.value) return null
  
  const tasks = [
    bgTasks.value.discovery,
    bgTasks.value.probe_loop,
    bgTasks.value.cycler,
    bgTasks.value.recovery,
    bgTasks.value.telemetry
  ]
  
  const alive = tasks.filter(t => t?.alive).length
  return { alive, total: tasks.length }
})

function formatLatency(latency?: string): string {
  if (!latency) return ''
  // Convert "1.234567ms" to "1.2ms"
  const match = latency.match(/^(\d+\.?\d*)(.+)$/)
  if (match) {
    const num = parseFloat(match[1])
    const unit = match[2]
    return `${num.toFixed(1)}${unit}`
  }
  return latency
}

function updateTimeSinceCheck() {
  if (!lastChecked.value) {
    timeSinceCheck.value = ''
    return
  }
  
  const seconds = Math.floor((Date.now() - lastChecked.value.getTime()) / 1000)
  if (seconds < 60) {
    timeSinceCheck.value = `${seconds}秒前`
  } else if (seconds < 3600) {
    timeSinceCheck.value = `${Math.floor(seconds / 60)}分钟前`
  } else {
    timeSinceCheck.value = `${Math.floor(seconds / 3600)}小时前`
  }
}

async function loadStatus() {
  loading.value = true
  error.value = null
  
  try {
    // Load health with full details
    health.value = await getHealth(true)
    
    // Load background tasks
    try {
      bgTasks.value = await getBackgroundTasksStatus()
    } catch (e) {
      // Background tasks might not be available, ignore
      console.warn('Background tasks not available:', e)
    }
    
    lastChecked.value = new Date()
    updateTimeSinceCheck()
  } catch (e: any) {
    error.value = e.message || '加载状态失败'
    console.error('Failed to load system status:', e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadStatus()
  // Refresh every 30 seconds
  refreshTimer = window.setInterval(loadStatus, 30000)
  // Update time display every 5 seconds
  timeUpdateTimer = window.setInterval(updateTimeSinceCheck, 5000)
  // Listen for outside clicks to close popover
  document.addEventListener('click', handleClickOutside)
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  if (timeUpdateTimer) clearInterval(timeUpdateTimer)
  document.removeEventListener('click', handleClickOutside)
})
</script>

<template>
  <div class="status-wrapper">
    <div ref="triggerRef" class="status-inline" @click="togglePopover">
      <span class="compact-indicator" :class="{ ok: health?.status === 'ok' }" title="Gateway">G</span>
      <span class="compact-indicator" :class="{ ok: health?.database?.connected }" title="Database">D</span>
      <span class="compact-indicator" :class="{ ok: health?.redis?.connected }" title="Redis">R</span>
      <span class="compact-indicator" :class="{ ok: aliveTasksCount && aliveTasksCount.alive === aliveTasksCount.total }" title="Tasks">T</span>
    </div>

    <Teleport to="body">
      <Transition name="fade">
        <div
          v-if="showPopover"
          ref="dropdownRef"
          class="status-dropdown"
          :style="dropdownStyle"
          @click.stop
        >
          <div class="status-popover">
            <div v-if="loading && !health" class="status-loading">加载中...</div>

            <div v-else-if="error" class="status-error">
              <div class="error-icon">⚠️</div>
              <div>{{ error }}</div>
            </div>

            <div v-else-if="health" class="status-details">
              <div class="status-item">
                <span class="status-dot" :style="{ backgroundColor: statusColor }"></span>
                <span class="status-name">Gateway</span>
                <span class="status-value">
                  v{{ health.version.split('-')[0] }}
                  <template v-if="health.version.split('-').length > 2">
                    <span class="version-build">#{{ health.version.split('-')[2] }}</span>
                  </template>
                </span>
              </div>

              <div v-if="health.database" class="status-item">
                <span
                  class="status-dot"
                  :style="{ backgroundColor: health.database.connected ? '#34d399' : '#f87171' }"
                ></span>
                <span class="status-name">Database</span>
                <span class="status-value">
                  {{ health.database.connected ? 'Connected' : 'Disconnected' }}
                  <span v-if="health.database.connected && health.database.latency" class="latency">
                    {{ formatLatency(health.database.latency) }}
                  </span>
                </span>
              </div>

              <div v-if="health.redis" class="status-item">
                <span
                  class="status-dot"
                  :style="{ backgroundColor: health.redis.connected ? '#34d399' : '#f87171' }"
                ></span>
                <span class="status-name">Redis</span>
                <span class="status-value">
                  {{ health.redis.connected ? 'Connected' : 'Disconnected' }}
                  <span v-if="health.redis.connected && health.redis.latency" class="latency">
                    {{ formatLatency(health.redis.latency) }}
                  </span>
                </span>
              </div>

              <div v-if="aliveTasksCount" class="status-item">
                <span
                  class="status-dot"
                  :style="{ backgroundColor: aliveTasksCount.alive === aliveTasksCount.total ? '#34d399' : '#fbbf24' }"
                ></span>
                <span class="status-name">Tasks</span>
                <span class="status-value">{{ aliveTasksCount.alive }}/{{ aliveTasksCount.total }} alive</span>
              </div>

              <div class="status-footer">
                <span class="last-checked">{{ timeSinceCheck || '刚刚' }}</span>
                <button class="refresh-btn" @click="loadStatus" :disabled="loading">
                  <span :class="{ rotating: loading }">↻</span>
                </button>
              </div>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.status-wrapper {
  display: inline-flex;
  position: relative;
}

.status-inline {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 6px;
  cursor: pointer;
  transition: opacity 0.2s;
}

.status-inline:hover {
  opacity: 0.8;
}

.compact-indicator {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 3px;
  font-size: 10px;
  font-weight: bold;
  background: var(--bg-hover, #21262d);
  color: #8b949e;
  border: 1px solid var(--border, #30363d);
  cursor: help;
  flex-shrink: 0;
}

.compact-indicator.ok {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
  border-color: #34d399;
}

.status-dropdown {
  position: fixed;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  z-index: 2000;
  min-width: 240px;
  /* Position below the trigger element - will be set via JS */
}

.status-popover {
  padding: 4px 0;
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.15s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

.status-loading {
  padding: 16px;
  text-align: center;
  color: var(--text-secondary, #8b949e);
}

.status-error {
  padding: 12px;
  display: flex;
  align-items: center;
  gap: 8px;
  color: #f87171;
}

.error-icon {
  font-size: 18px;
}

.status-details {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.status-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  transition: background 0.2s;
}

.status-item:hover {
  background: var(--bg-hover, rgba(255, 255, 255, 0.05));
}

.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.status-name {
  font-weight: 500;
  color: var(--text-primary, #e6edf3);
  min-width: 70px;
}

.status-value {
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
  flex: 1;
  text-align: right;
}

.latency {
  color: var(--text-tertiary, #6e7681);
  margin-left: 4px;
}

.version-build {
  color: var(--text-tertiary, #6e7681);
  margin-left: 4px;
}

.status-summary {
  padding: 12px;
  cursor: pointer;
  transition: background 0.2s;
}

.status-summary:hover {
  background: var(--bg-hover, rgba(255, 255, 255, 0.05));
}

.summary-line {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}

.summary-label {
  font-weight: 500;
  color: var(--text-primary, #e6edf3);
  font-size: 13px;
}

.expand-btn {
  background: transparent;
  border: none;
  color: var(--text-secondary, #8b949e);
  cursor: pointer;
  font-size: 10px;
  padding: 2px;
}

.summary-indicators {
  display: flex;
  gap: 8px;
  align-items: center;
}

.indicator {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: bold;
  background: var(--bg-hover, #21262d);
  color: #8b949e;
  border: 1px solid var(--border, #30363d);
  cursor: help;
}

.indicator.ok {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
  border-color: #34d399;
}

.detail-view {
  border-top: 1px solid var(--border, #30363d);
  padding-top: 4px;
  margin-top: 4px;
}

.status-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px 4px;
  margin-top: 4px;
  border-top: 1px solid var(--border, #30363d);
}

.last-checked {
  font-size: 11px;
  color: var(--text-tertiary, #6e7681);
}

.refresh-btn {
  background: transparent;
  border: none;
  color: var(--text-secondary, #8b949e);
  cursor: pointer;
  font-size: 16px;
  padding: 2px;
  transition: color 0.2s;
}

.refresh-btn:hover:not(:disabled) {
  color: var(--text-primary, #e6edf3);
}

.refresh-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.rotating {
  display: inline-block;
  animation: rotate 1s linear infinite;
}

@keyframes rotate {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}
</style>
