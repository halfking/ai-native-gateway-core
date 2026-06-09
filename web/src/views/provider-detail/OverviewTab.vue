<script setup lang="ts">
import type { ProviderDetail, ModelOffer } from '../../api'

const props = defineProps<{
  provider: ProviderDetail
  models: ModelOffer[]
}>()

function fmtPct(v: number): string {
  return (v * 100).toFixed(1) + '%'
}

function fmtMoney(v: number | null | undefined): string {
  if (v == null) return '-'
  return '$' + Number(v).toFixed(2)
}

const availableModels = computed(() => props.models.filter(m => m.available))
const unavailableModels = computed(() => props.models.filter(m => !m.available))

import { computed } from 'vue'
</script>

<template>
  <div class="overview-grid">
    <div class="metric-card">
      <div class="metric-label">活跃凭据</div>
      <div class="metric-value">{{ provider.active_cred_count }}</div>
      <div class="metric-sub">
        <span class="dot dot-green"></span> 健康 {{ provider.healthy_cred_count }}
        <span class="dot dot-amber" style="margin-left:8px"></span> 警示 {{ provider.warning_cred_count }}
      </div>
    </div>
    <div class="metric-card">
      <div class="metric-label">熔断/不可达</div>
      <div class="metric-value">{{ provider.cooling_cred_count }} / {{ provider.unreachable_cred_count }}</div>
      <div class="metric-sub">凭据健康状态</div>
    </div>
    <div class="metric-card">
      <div class="metric-label">可用模型</div>
      <div class="metric-value">{{ provider.available_model_count }} / {{ provider.available_model_count + provider.unavailable_model_count }}</div>
      <div class="metric-sub">覆盖率 {{ provider.available_model_count + provider.unavailable_model_count > 0 ? ((provider.available_model_count / (provider.available_model_count + provider.unavailable_model_count)) * 100).toFixed(1) + '%' : '-' }}</div>
    </div>
    <div class="metric-card">
      <div class="metric-label">24h 错误率</div>
      <div class="metric-value" :class="provider.error_rate_24h > 0.1 ? 'text-danger' : ''">
        {{ fmtPct(provider.error_rate_24h) }}
      </div>
      <div class="metric-sub">最近 24 小时</div>
    </div>

    <div class="info-section" style="grid-column: 1 / -1; margin-top: 12px">
      <h4>基本信息</h4>
      <div class="info-grid">
        <div><span class="info-label">目录代码</span><code>{{ provider.catalog_code || provider.code }}</code></div>
        <div><span class="info-label">协议</span><code>{{ provider.protocol }}</code></div>
        <div><span class="info-label">Base URL</span><code class="url">{{ provider.base_url || '-' }}</code></div>
        <div><span class="info-label">类型</span>{{ provider.kind }} / {{ provider.category }}</div>
        <div><span class="info-label">折扣率</span>{{ provider.discount_rate || '-' }}</div>
        <div><span class="info-label">状态</span>
          <span class="badge" :class="provider.enabled ? 'badge-green' : 'badge-red'">
            {{ provider.enabled ? '已启用' : '已禁用' }}
          </span>
        </div>
        <div v-if="provider.notes"><span class="info-label">备注</span>{{ provider.notes }}</div>
        <div><span class="info-label">创建时间</span>{{ provider.created_at ? new Date(provider.created_at).toLocaleString('zh-CN') : '-' }}</div>
      </div>
    </div>

    <div class="model-matrix" style="grid-column: 1 / -1" v-if="models.length > 0">
      <h4>模型可用性矩阵 <span class="muted">({{ availableModels.length }} 可用 / {{ unavailableModels.length }} 不可用)</span></h4>
      <div class="matrix-chips">
        <span
          v-for="m in models"
          :key="m.id"
          class="model-chip"
          :class="m.available ? 'chip-green' : 'chip-red'"
          :title="m.available ? '可用' : (m.unavailable_reason || '不可用')"
        >
          {{ m.raw_model_name }}
          <span v-if="m.unavailable_reason" class="chip-reason">({{ m.unavailable_reason }})</span>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overview-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
}
.metric-card {
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
}
.metric-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 4px;
}
.metric-value {
  font-size: 22px;
  font-weight: 600;
  color: var(--text);
}
.metric-sub {
  font-size: 11px;
  color: var(--muted);
  margin-top: 4px;
}
.text-danger { color: #f44336; }
.dot {
  width: 8px; height: 8px; border-radius: 50%;
  display: inline-block; vertical-align: middle; margin-right: 2px;
}
.dot-green { background: #4caf50; }
.dot-amber { background: #f0b429; }
.info-section { margin-top: 8px; }
.info-section h4 { margin: 0 0 8px; font-size: 14px; color: var(--text); }
.info-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 6px 24px;
  font-size: 13px;
  color: var(--text);
}
.info-label {
  color: var(--muted);
  margin-right: 8px;
  font-size: 12px;
}
code { font-size: 12px; }
code.url { word-break: break-all; }
.model-matrix { margin-top: 12px; }
.model-matrix h4 { margin: 0 0 8px; font-size: 14px; color: var(--text); }
.muted { color: var(--muted); font-size: 11px; }
.matrix-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.model-chip {
  font-size: 11px;
  padding: 3px 8px;
  border-radius: 4px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.chip-green { background: rgba(76,175,80,.15); color: #66bb6a; }
.chip-red { background: rgba(244,67,54,.12); color: #ef5350; }
.chip-reason { font-size: 10px; opacity: .7; }
</style>
