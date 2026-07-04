<script setup lang="ts">
// StatsDrawer.vue — APIKey排行和模型统计抽屉
// 2026-07-05: 将原本占用大量空间的表格移到抽屉中

import { ref, computed } from 'vue'
import type { HotApiKeyEntry, ModelUsage } from '../api'

const props = defineProps<{
  hotKeys: HotApiKeyEntry[]
  models: ModelUsage[]
  days: number
  loading: boolean
}>()

const isOpen = ref(false)
const activeTab = ref<'apikeys' | 'models'>('apikeys')

function open(tab: 'apikeys' | 'models' = 'apikeys') {
  activeTab.value = tab
  isOpen.value = true
}

function close() {
  isOpen.value = false
}

function fmt(n: number | undefined, decimals = 0) {
  if (n === undefined || n === null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toFixed(decimals)
}

function fmtCost(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return '$' + Number(v).toFixed(4)
}

function fmtDate(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

defineExpose({ open, close })
</script>

<template>
  <Teleport to="body">
    <Transition name="drawer-fade">
      <div v-if="isOpen" class="drawer-overlay" @click="close">
        <Transition name="drawer-slide">
          <div v-if="isOpen" class="drawer" @click.stop>
            <!-- 头部 -->
            <div class="drawer-header">
              <div class="drawer-tabs">
                <button
                  type="button"
                  class="drawer-tab"
                  :class="{ 'drawer-tab--active': activeTab === 'apikeys' }"
                  @click="activeTab = 'apikeys'"
                >
                  高用量 API Key 排行
                </button>
                <button
                  type="button"
                  class="drawer-tab"
                  :class="{ 'drawer-tab--active': activeTab === 'models' }"
                  @click="activeTab = 'models'"
                >
                  按模型统计
                </button>
              </div>
              <button type="button" class="drawer-close" @click="close" aria-label="关闭">
                ✕
              </button>
            </div>
            
            <!-- 内容 -->
            <div class="drawer-body">
              <!-- APIKey排行 -->
              <div v-if="activeTab === 'apikeys'" class="drawer-content">
                <div v-if="loading" class="drawer-loading">加载中…</div>
                <table v-else-if="hotKeys.length > 0" class="stats-table">
                  <thead>
                    <tr>
                      <th>Key</th>
                      <th>应用</th>
                      <th>归属用户</th>
                      <th style="text-align:right">请求数</th>
                      <th style="text-align:right">Token 用量</th>
                      <th style="text-align:right">费用 (USD)</th>
                      <th>最后使用</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="k in hotKeys" :key="k.api_key_id">
                      <td><code class="mono-sm">{{ k.key_prefix ?? '—' }}***</code></td>
                      <td>{{ k.application_code ?? '—' }}</td>
                      <td>{{ k.owner_user ?? '—' }}</td>
                      <td style="text-align:right">{{ fmt(k.request_count) }}</td>
                      <td style="text-align:right">{{ fmt(k.total_tokens) }}</td>
                      <td style="text-align:right">{{ fmtCost(k.total_cost_usd) }}</td>
                      <td>{{ fmtDate(k.last_used_at) }}</td>
                    </tr>
                  </tbody>
                </table>
                <div v-else class="drawer-empty">该时段暂无 API Key 排行数据</div>
              </div>
              
              <!-- 模型统计 -->
              <div v-if="activeTab === 'models'" class="drawer-content">
                <div v-if="loading" class="drawer-loading">加载中…</div>
                <table v-else-if="models.length > 0" class="stats-table">
                  <thead>
                    <tr>
                      <th>模型</th>
                      <th>提供商</th>
                      <th style="text-align:right">请求数</th>
                      <th style="text-align:right">Token 用量</th>
                      <th style="text-align:right">费用 (USD)</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="m in models" :key="m.model">
                      <td><code class="mono-sm">{{ m.model }}</code></td>
                      <td><span class="badge badge-blue">{{ m.provider_code }}</span></td>
                      <td style="text-align:right">{{ fmt(m.total_requests) }}</td>
                      <td style="text-align:right">{{ fmt(m.total_tokens) }}</td>
                      <td style="text-align:right">{{ fmtCost(m.total_cost_usd) }}</td>
                    </tr>
                  </tbody>
                </table>
                <div v-else class="drawer-empty">该时段暂无模型统计数据</div>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.drawer-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  z-index: 1000;
  display: flex;
  justify-content: flex-end;
  align-items: stretch;
}

.drawer {
  width: min(800px, 90vw);
  background: var(--card, #1c2128);
  display: flex;
  flex-direction: column;
  box-shadow: -4px 0 24px rgba(0, 0, 0, 0.3);
}

.drawer-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border, #30363d);
  gap: 12px;
}

.drawer-tabs {
  display: flex;
  gap: 4px;
}

.drawer-tab {
  padding: 8px 16px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
}

.drawer-tab:hover {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
}

.drawer-tab--active {
  background: rgba(99, 102, 241, 0.15);
  border-color: var(--accent, #6366f1);
  color: var(--accent, #6366f1);
}

.drawer-close {
  width: 32px;
  height: 32px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 18px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.15s ease;
  flex-shrink: 0;
}

.drawer-close:hover {
  background: var(--bg-subtle, #161b22);
  border-color: var(--danger, #f85149);
  color: var(--danger, #f85149);
}

.drawer-body {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 20px;
}

.drawer-content {
  animation: fade-in 0.2s ease;
}

@keyframes fade-in {
  from { opacity: 0; transform: translateY(10px); }
  to { opacity: 1; transform: translateY(0); }
}

.drawer-loading {
  padding: 40px 20px;
  text-align: center;
  color: var(--muted, #8b949e);
}

.drawer-empty {
  padding: 40px 20px;
  text-align: center;
  color: var(--muted, #8b949e);
  font-size: 13px;
}

.stats-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.stats-table thead {
  position: sticky;
  top: 0;
  background: var(--card, #1c2128);
  z-index: 1;
}

.stats-table th {
  padding: 10px 12px;
  text-align: left;
  font-weight: 600;
  color: var(--text-secondary, #8b949e);
  border-bottom: 2px solid var(--border, #30363d);
}

.stats-table td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border, #30363d);
  color: var(--text, #e6edf3);
}

.stats-table tbody tr:hover {
  background: var(--bg-subtle, #161b22);
}

.mono-sm {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  background: var(--bg-subtle, #161b22);
  padding: 2px 6px;
  border-radius: 3px;
}

.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 11px;
  font-weight: 500;
}

.badge-blue {
  background: rgba(59, 130, 246, 0.15);
  color: #3b82f6;
}

/* 动画 */
.drawer-fade-enter-active,
.drawer-fade-leave-active {
  transition: opacity 0.25s ease;
}

.drawer-fade-enter-from,
.drawer-fade-leave-to {
  opacity: 0;
}

.drawer-slide-enter-active {
  transition: transform 0.3s cubic-bezier(0.22, 1, 0.36, 1);
}

.drawer-slide-leave-active {
  transition: transform 0.25s cubic-bezier(0.64, 0, 0.78, 0);
}

.drawer-slide-enter-from {
  transform: translateX(100%);
}

.drawer-slide-leave-to {
  transform: translateX(100%);
}

@media (max-width: 768px) {
  .drawer {
    width: 100vw;
  }
  
  .stats-table {
    font-size: 11px;
  }
  
  .stats-table th,
  .stats-table td {
    padding: 8px 6px;
  }
}
</style>
