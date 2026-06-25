<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import FpSlotVisualizer from './FpSlotVisualizer.vue'
import { getCredentialSlots, type SlotInfoResponse } from '../api/providers'

interface Props {
  credentialId: number
}

const props = defineProps<Props>()

const loading = ref(true)
const error = ref<string | null>(null)
const data = ref<SlotInfoResponse | null>(null)

// 转换 SlotInfoV3 为 FpSlotVisualizer 需要的格式
const slotDetails = computed(() => {
  if (!data.value?.slots) return []
  return data.value.slots.map(s => ({
    index: s.index,
    holder: s.holder,
    ttl_seconds: s.ttl_seconds,
    expired: s.expired,
    // V3.1 新增字段（暂不展示在旧组件中）
    inflight: s.inflight,
    pin_holder: s.pin_holder,
    pin_ttl_seconds: s.pin_ttl_seconds,
  }))
})

async function fetchSlotInfo() {
  loading.value = true
  error.value = null
  try {
    data.value = await getCredentialSlots(props.credentialId)
  } catch (e: any) {
    error.value = e?.message || 'Failed to load slot info'
  } finally {
    loading.value = false
  }
}

async function handleReleaseSlot(slotIndex: number) {
  // Phase 8 TODO: 实现 slot 释放功能（需要新增后端 endpoint）
  console.log('Release slot', slotIndex, 'for credential', props.credentialId)
  alert('释放槽位功能暂未实现（Phase 8 TODO）')
}

onMounted(() => {
  fetchSlotInfo()
})

defineExpose({
  refresh: fetchSlotInfo,
})
</script>

<template>
  <div class="slot-info-card">
    <!-- Loading State -->
    <div v-if="loading" class="slot-loading">
      <div class="spinner"></div>
      <span>加载槽位信息...</span>
    </div>
    
    <!-- Error State -->
    <div v-else-if="error" class="slot-error">
      <span class="error-icon">⚠️</span>
      <span>{{ error }}</span>
      <button @click="fetchSlotInfo" class="retry-btn">重试</button>
    </div>
    
    <!-- Disabled State -->
    <div v-else-if="!data?.enabled" class="slot-disabled">
      <span class="disabled-icon">ℹ️</span>
      <span>该凭据未启用指纹槽（FpSlot 未启用）</span>
    </div>
    
    <!-- Main Content -->
    <div v-else class="slot-content">
      <!-- V3.1 Stats Header -->
      <div class="v3-stats-header">
        <div class="v3-stat">
          <div class="v3-stat-value">{{ data?.total_slots || 0 }}</div>
          <div class="v3-stat-label">总槽位</div>
        </div>
        <div class="v3-stat">
          <div class="v3-stat-value">{{ data?.active_slots || 0 }}</div>
          <div class="v3-stat-label">已占用</div>
        </div>
        <div class="v3-stat v3-stat--highlight">
          <div class="v3-stat-value">{{ data?.total_inflight || 0 }}</div>
          <div class="v3-stat-label">并发请求</div>
        </div>
        <div class="v3-stat">
          <div class="v3-stat-value">{{ data?.fp_slot_limit || '-' }}</div>
          <div class="v3-stat-label">槽位上限</div>
        </div>
      </div>
      
      <!-- Layer 1: Fingerprint Slots (reuse FpSlotVisualizer) -->
      <div class="layer-section">
        <h4 class="layer-title">Layer 1: 指纹槽</h4>
        <FpSlotVisualizer
          v-if="data?.slots"
          :details="slotDetails"
          :slot-limit="data?.total_slots || 0"
          @release="handleReleaseSlot"
        />
      </div>
      
      <!-- Layer 2: Inflight Details -->
      <div class="layer-section" v-if="data?.slots && data.slots.some(s => s.inflight > 0)">
        <h4 class="layer-title">Layer 2: 并发详情</h4>
        <div class="inflight-grid">
          <div
            v-for="slot in data.slots.filter(s => s.inflight > 0)"
            :key="slot.index"
            class="inflight-card"
          >
            <div class="inflight-header">
              <span class="inflight-slot-num">#{{ slot.index }}</span>
              <span class="inflight-count">{{ slot.inflight }} 个并发</span>
            </div>
            <div class="inflight-meta">
              <div class="inflight-holder">
                <span class="label">持有者:</span>
                <span class="value">{{ slot.holder || '-' }}</span>
              </div>
              <div class="inflight-ttl">
                <span class="label">TTL:</span>
                <span class="value">{{ slot.ttl_seconds > 0 ? Math.floor(slot.ttl_seconds / 60) + 'm' : '已过期' }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
      
      <!-- Empty State -->
      <div v-else-if="data?.active_slots === 0" class="empty-state">
        <span class="empty-icon">📭</span>
        <span>当前没有活跃的槽位</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.slot-info-card {
  background: var(--bg, #0d1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 12px;
  padding: 20px;
  margin-top: 16px;
}

.slot-loading,
.slot-error,
.slot-disabled {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  padding: 40px 20px;
  color: var(--muted, #8b949e);
}

.spinner {
  width: 20px;
  height: 20px;
  border: 2px solid var(--border, #30363d);
  border-top-color: var(--accent, #4f46e5);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.error-icon,
.disabled-icon {
  font-size: 20px;
}

.retry-btn {
  padding: 6px 12px;
  background: var(--accent, #4f46e5);
  color: white;
  border: none;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
  transition: background 0.15s;
}

.retry-btn:hover {
  background: var(--accent-hover, #4338ca);
}

.slot-content {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.v3-stats-header {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
  padding: 16px;
  background: linear-gradient(135deg, rgba(99, 102, 241, 0.1) 0%, rgba(79, 70, 229, 0.05) 100%);
  border-radius: 8px;
  border: 1px solid rgba(99, 102, 241, 0.2);
}

.v3-stat {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
}

.v3-stat-value {
  font-size: 24px;
  font-weight: 700;
  color: var(--text, #f0f6fc);
  font-family: ui-monospace, monospace;
}

.v3-stat-label {
  font-size: 11px;
  color: var(--muted, #8b949e);
  margin-top: 4px;
}

.v3-stat--highlight .v3-stat-value {
  color: #10b981;
}

.layer-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.layer-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text, #f0f6fc);
  margin: 0;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border, #30363d);
}

.inflight-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
}

.inflight-card {
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  padding: 12px;
}

.inflight-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.inflight-slot-num {
  font-family: ui-monospace, monospace;
  font-size: 12px;
  font-weight: 600;
  color: var(--accent, #4f46e5);
}

.inflight-count {
  font-size: 14px;
  font-weight: 700;
  color: #10b981;
}

.inflight-meta {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
  color: var(--muted, #8b949e);
}

.inflight-meta .label {
  margin-right: 4px;
}

.inflight-meta .value {
  font-family: ui-monospace, monospace;
  color: var(--text, #f0f6fc);
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 40px 20px;
  color: var(--muted, #8b949e);
  text-align: center;
}

.empty-icon {
  font-size: 32px;
  opacity: 0.6;
}

/* Responsive */
@media (max-width: 768px) {
  .v3-stats-header {
    grid-template-columns: repeat(2, 1fr);
  }
  
  .inflight-grid {
    grid-template-columns: 1fr;
  }
}
</style>
