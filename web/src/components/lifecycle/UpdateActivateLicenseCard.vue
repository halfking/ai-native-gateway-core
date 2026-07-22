<script setup lang="ts">
import { computed } from 'vue'
import type { LicenseStatus } from '../../api/updateActivate'
import { isLicenseActive, licenseStateLabel } from '../../utils/labels'

/** UpdateActivateLicenseCard — 复用 /maintain/activate 的 status-panel 样式，
 *  展示激活效果：状态点 + 状态文案 + 订阅 tier + license key + 有效期 + 设备名。
 *  数据来源 maintain-api /license/status（由父组件注入）。 */

const props = defineProps<{
  status: LicenseStatus | null
  loading: boolean
  activated: boolean
  instanceId: string
  deviceName: string
}>()

const emit = defineEmits<{
  (e: 'refresh'): void
}>()

const dotVariant = computed(() => {
  if (!props.status) return 'idle'
  return isLicenseActive(props.status.state) ? 'success' : 'warning'
})

const isActive = computed(() => isLicenseActive(props.status?.state))

const expiresDisplay = computed(() => {
  const ts = props.status?.expires_at
  if (!ts) return ''
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  return d.toLocaleString()
})

const heartbeatDisplay = computed(() => {
  const ts = props.status?.last_heartbeat
  if (!ts) return ''
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  return d.toLocaleString()
})
</script>

<template>
  <el-card shadow="never" class="ua-card license-card">
    <template #header>
      <div class="ua-card__head">
        <span class="ua-card__title">激活效果</span>
        <el-button
          size="small"
          text
          :loading="loading"
          class="btn-no-arrow"
          @click="emit('refresh')"
        >
          刷新
        </el-button>
      </div>
    </template>

    <div v-if="loading && !status" class="skeleton-row">
      <el-skeleton :rows="4" animated />
    </div>

    <template v-else-if="status && status.state && status.state !== 'none'">
      <div class="status-line">
        <span class="status-dot" :class="`status-dot--${dotVariant}`" aria-hidden="true" />
        <strong class="status-label">{{ licenseStateLabel(status.state) }}</strong>
        <span v-if="status.subscription_tier" class="chip chip--tier">
          {{ status.subscription_tier }}
        </span>
        <el-tag v-if="isActive" size="small" type="success" class="ml">已激活</el-tag>
        <el-tag v-else size="small" type="warning" class="ml">需要处理</el-tag>
      </div>

      <dl class="license-meta">
        <div v-if="status.license_key" class="meta-row">
          <dt>License Key</dt>
          <dd><code>{{ status.license_key }}</code></dd>
        </div>
        <div v-if="expiresDisplay" class="meta-row">
          <dt>有效期至</dt>
          <dd>{{ expiresDisplay }}</dd>
        </div>
        <div class="meta-row">
          <dt>设备名称</dt>
          <dd>{{ status.device_name || deviceName || '—' }}</dd>
        </div>
        <div class="meta-row">
          <dt>实例 ID</dt>
          <dd><code>{{ instanceId || '—' }}</code></dd>
        </div>
        <div v-if="status.customer_name" class="meta-row">
          <dt>客户名称</dt>
          <dd>{{ status.customer_name }}</dd>
        </div>
        <div v-if="heartbeatDisplay" class="meta-row">
          <dt>最近心跳</dt>
          <dd class="muted">{{ heartbeatDisplay }}</dd>
        </div>
      </dl>

      <p v-if="!isActive && !props.activated" class="hint">
        当前 License 状态非「激活」，可能已过期或被吊销。请联系运维或使用「
        <RouterLink to="/customer/offline-activation">离线激活</RouterLink>」重新签发。
      </p>
    </template>

    <el-empty
      v-else
      description="尚未从中心读取 License 状态（中心可能不可达，或尚未激活）"
      :image-size="72"
    />
  </el-card>
</template>

<style scoped>
.license-card { height: 100%; }
.ua-card__head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.ua-card__title { font-weight: 600; }
.skeleton-row { padding: 4px 0; }

.status-line {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 14px;
}
.status-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  display: inline-block;
  box-shadow: 0 0 0 4px color-mix(in srgb, var(--kx-border) 35%, transparent);
}
.status-dot--success { background: var(--kx-success); }
.status-dot--warning { background: var(--kx-warning); }
.status-dot--idle { background: var(--muted); }
.status-label { font-size: 15px; }

.chip {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  background: var(--kx-primary-soft);
  color: var(--kx-primary);
}
.chip--tier { font-weight: 500; }

.ml { margin-left: 4px; }

.license-meta {
  margin: 0;
  display: grid;
  grid-template-columns: 1fr;
  gap: 10px;
}
.meta-row {
  display: grid;
  grid-template-columns: 110px 1fr;
  gap: 12px;
  align-items: baseline;
  padding: 6px 0;
  border-bottom: 1px dashed color-mix(in srgb, var(--kx-border) 60%, transparent);
}
.meta-row:last-child { border-bottom: none; }
.meta-row dt {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
  letter-spacing: 0.02em;
}
.meta-row dd {
  margin: 0;
  font-size: 13px;
  word-break: break-all;
}
.meta-row code {
  font-size: 12px;
  padding: 2px 6px;
  background: var(--kx-surface-soft);
  border-radius: 4px;
}
.muted { color: var(--muted); }

.hint {
  margin: 14px 0 0;
  padding: 10px 12px;
  background: var(--kx-warning-soft);
  border-left: 3px solid var(--kx-warning);
  border-radius: 4px;
  font-size: 13px;
  color: var(--kx-warning);
}
</style>
