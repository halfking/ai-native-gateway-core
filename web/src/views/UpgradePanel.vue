<script setup lang="ts">
// UpgradePanel.vue — Customer-facing upgrade status & check trigger (2026-07-13)
//
// Read-only display + manual refresh of available updates. Actual upgrade
// execution is performed by the admin via /api/admin/releases/*.

import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getUpgradeStatus,
  checkForUpgrade,
  type UpgradeStatus,
} from '../api/customer'

const status = ref<UpgradeStatus | null>(null)
const loading = ref(false)
const checking = ref(false)
const lastChecked = ref<string | null>(null)
let pollTimer: ReturnType<typeof setInterval> | null = null

const channelLabel = computed(() => {
  switch (status.value?.channel) {
    case 'stable': return '稳定版'
    case 'beta': return '公测版'
    case 'canary': return '预览版'
    default: return '稳定版'
  }
})

async function refresh() {
  loading.value = true
  try {
    status.value = await getUpgradeStatus()
  } catch (err) {
    ElMessage.error(`查询升级状态失败: ${(err as Error).message}`)
  } finally {
    loading.value = false
  }
}

async function manualCheck() {
  checking.value = true
  try {
    const result = await checkForUpgrade()
    status.value = result
    lastChecked.value = result.checked_at
    if (result.has_update) {
      ElMessage.success(`检测到新版本 ${result.latest_version}`)
    } else {
      ElMessage.info('当前已是最新版本')
    }
  } catch (err) {
    ElMessage.error(`升级检查失败: ${(err as Error).message}`)
  } finally {
    checking.value = false
  }
}

function notifyAdmin() {
  ElMessageBox.confirm(
    '请联系管理员通过 /admin/autoupdate 应用此升级。客户门户无法直接执行升级。',
    '需要管理员介入',
    { confirmButtonText: '我知道了', cancelButtonText: '复制版本号' },
  ).catch(() => {
    if (status.value?.latest_version) {
      navigator.clipboard.writeText(status.value.latest_version)
    }
  })
}

onMounted(() => {
  refresh()
  pollTimer = setInterval(refresh, 30 * 60 * 1000)
})

onBeforeUnmount(() => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})
</script>

<template>
  <div class="upgrade-panel" v-loading="loading">
    <el-card>
      <template #header>
        <div class="header-row">
          <span class="card-title">软件升级</span>
          <div class="header-actions">
            <el-button :loading="checking" @click="manualCheck" type="primary" size="small">
              立即检查
            </el-button>
          </div>
        </div>
      </template>

      <el-descriptions :column="2" border>
        <el-descriptions-item label="当前版本">
          <code>{{ status?.current_version || '—' }}</code>
        </el-descriptions-item>
        <el-descriptions-item label="Build Seq">
          {{ status?.current_build_seq ?? '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="升级频道">
          <el-tag size="small">{{ channelLabel }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="最新版本">
          <code v-if="status?.latest_version">{{ status.latest_version }}</code>
          <span v-else class="muted">—</span>
        </el-descriptions-item>
        <el-descriptions-item label="发布时间">
          {{ status?.published_at || '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="兼容性">
          <el-tag :type="status?.is_compatible ? 'success' : 'danger'" size="small">
            {{ status?.is_compatible ? '兼容' : '需要先升级到 ' + (status?.min_version || '—') }}
          </el-tag>
        </el-descriptions-item>
      </el-descriptions>

      <el-alert
        v-if="status?.has_update"
        :type="status.update_mandatory ? 'error' : 'warning'"
        :closable="false"
        show-icon
        class="update-alert"
      >
        <template #title>
          {{ status.update_mandatory ? '强制升级可用' : '有可用的新版本' }}
        </template>
        <template #default>
          <div class="release-info">
            <div><strong>标题：</strong>{{ status.release_title || '—' }}</div>
            <div class="release-notes">{{ status.release_notes || '无详细说明' }}</div>
          </div>
          <div class="cta-row">
            <el-button type="primary" @click="notifyAdmin">
              联系管理员应用升级
            </el-button>
          </div>
        </template>
      </el-alert>

      <el-alert
        v-else-if="status"
        type="success"
        :closable="false"
        show-icon
        title="已是最新版本"
        :description="`最后检查时间：${lastChecked || '尚未手动检查'}`"
        class="update-alert"
      />

      <div class="polling-note muted">
        系统每 30 分钟自动检查一次更新；点击"立即检查"立即触发。
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.upgrade-panel {
  max-width: 960px;
  margin: 0 auto;
  padding: 24px;
}
.header-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.card-title { font-weight: 600; }
.update-alert { margin-top: 16px; }
.release-info { line-height: 1.6; }
.release-notes {
  margin-top: 6px;
  padding: 8px 12px;
  background: rgba(0, 0, 0, 0.03);
  border-radius: 4px;
  white-space: pre-wrap;
}
.cta-row { margin-top: 12px; }
.polling-note {
  margin-top: 12px;
  font-size: 12px;
}
.muted { color: #909399; }
</style>