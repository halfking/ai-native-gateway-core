<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { bootstrapApi, type BootstrapStatus } from '../../api/bootstrap'
import { ensureInstanceId, readStoredHardwareHash } from '../../utils/deviceFingerprint'
import { SITE_TITLE } from '../../config/brand'

const instanceId = ref(ensureInstanceId())
const status = ref<BootstrapStatus | null>(null)
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    status.value = await bootstrapApi.status(instanceId.value.trim() || undefined)
  } catch (e) {
    error.value = (e as Error).message
    status.value = null
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="lifecycle-page">
    <header class="lifecycle-page__header">
      <div>
        <p class="eyebrow">SITE INFO</p>
        <h1>站点信息</h1>
        <p>{{ SITE_TITLE }} — 本机实例、激活与中心连接状态。</p>
      </div>
      <div class="lifecycle-page__actions">
        <RouterLink class="btn btn-ghost" to="/bootstrap">首启向导</RouterLink>
        <button type="button" class="btn btn-primary btn-sm" :disabled="loading" @click="load">刷新</button>
      </div>
    </header>

    <el-alert v-if="error" type="error" :title="error" show-icon class="mb" closable @close="error = ''" />

    <div class="lifecycle-grid">
      <el-card shadow="never">
        <template #header>实例</template>
        <p><strong>实例 ID</strong></p>
        <p class="mono">{{ instanceId }}</p>
        <p class="muted">硬件指纹（摘要）</p>
        <p class="mono">{{ readStoredHardwareHash() || status?.hardware_hash || '—' }}</p>
      </el-card>

      <el-card shadow="never">
        <template #header>运行状态</template>
        <el-skeleton v-if="loading" :rows="4" animated />
        <template v-else-if="status">
          <p>激活：<strong>{{ status.activated ? '已激活' : '未激活' }}</strong></p>
          <p>中心可达：<strong>{{ status.center_online ? '是' : '否' }}</strong></p>
          <p>中心注册：<strong>{{ status.registered ? '已注册' : '未注册/待补' }}</strong></p>
          <p v-if="status.license_key" class="mono">License：{{ status.license_key }}</p>
          <p v-if="status.message" class="muted">{{ status.message }}</p>
        </template>
        <el-empty v-else description="无法读取状态" />
      </el-card>

      <el-card shadow="never">
        <template #header>快捷操作</template>
        <div class="action-links">
          <RouterLink to="/customer/activate">许可激活</RouterLink>
          <RouterLink to="/customer/license">许可状态</RouterLink>
          <RouterLink to="/customer/agreement">用户协议</RouterLink>
          <RouterLink to="/customer/offline-activation">离线激活</RouterLink>
          <a href="/maintain/download" target="_self">云端下载</a>
        </div>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.lifecycle-page { padding: 24px clamp(16px, 3vw, 32px) 40px; max-width: 1100px; }
.lifecycle-page__header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  margin-bottom: 20px;
}
.eyebrow {
  margin: 0 0 6px;
  color: var(--accent-h, #1e4fd6);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.12em;
}
.lifecycle-page__header h1 { margin: 0 0 8px; font-size: 24px; }
.lifecycle-page__header p { margin: 0; color: var(--muted, #5b6b82); font-size: 14px; line-height: 1.6; }
.lifecycle-page__actions { display: flex; gap: 8px; flex-wrap: wrap; }
.lifecycle-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 16px;
}
.mono { font-family: ui-monospace, monospace; font-size: 13px; word-break: break-all; }
.muted { color: var(--muted, #5b6b82); font-size: 13px; }
.action-links { display: flex; flex-direction: column; gap: 10px; }
.action-links a { color: var(--accent-h, #1e4fd6); text-decoration: none; font-weight: 600; }
.mb { margin-bottom: 16px; }
</style>
