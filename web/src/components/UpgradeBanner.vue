<script setup lang="ts">
// UpgradeBanner.vue — Persistent top-of-page banner when an update is
// available. Mounted in App.vue so it shows across all routes.

import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import {
  getUpgradeStatus,
  type UpgradeStatus,
} from '../api/customer'

const status = ref<UpgradeStatus | null>(null)
let pollTimer: ReturnType<typeof setInterval> | null = null

const visible = computed(() => !!status.value?.has_update)
const mandatory = computed(() => !!status.value?.update_mandatory)
const incompatible = computed(() => status.value?.has_update && !status.value?.is_compatible)

async function refresh() {
  try {
    status.value = await getUpgradeStatus()
  } catch {
    status.value = null
  }
}

function dismiss() {
  status.value = null
}

onMounted(() => {
  refresh()
  pollTimer = setInterval(refresh, 60 * 60 * 1000)
})

onBeforeUnmount(() => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})
</script>

<template>
  <transition name="el-fade-in">
    <el-alert
      v-if="visible"
      :type="incompatible ? 'error' : mandatory ? 'warning' : 'info'"
      :title="`新版本可用：${status?.latest_version}`"
      :description="status?.release_title"
      class="upgrade-banner"
      show-icon
      :closable="!mandatory"
      @close="dismiss"
    >
      <template #default>
        <div class="banner-content">
          <div class="banner-text">
            <strong>v{{ status?.latest_version }}</strong> 已发布
            <span v-if="mandatory" class="badge-mandatory">[强制]</span>
            <span v-if="status?.release_title"> · {{ status.release_title }}</span>
          </div>
          <router-link to="/upgrade" class="banner-link">查看详情 →</router-link>
        </div>
      </template>
    </el-alert>
  </transition>
</template>

<style scoped>
.upgrade-banner {
  margin: 0;
  border-radius: 0;
  border-left: none;
  border-right: none;
  border-top: none;
}
.banner-content {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.banner-link {
  color: inherit;
  text-decoration: underline;
  font-weight: 600;
}
.badge-mandatory {
  margin-left: 6px;
  padding: 1px 6px;
  background: color-mix(in srgb, var(--warning) 18%, transparent);
  color: var(--warning-dark);
  border-radius: 3px;
  font-size: 12px;
}
</style>