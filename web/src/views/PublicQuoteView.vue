<script setup lang="ts">
// PublicQuoteView.vue — 客户版报价落地页（2026-11-08 W2 客户版轮，风暴档 §3.5
// 「内外隔离：渲染隔离，不是显示隐藏」的落地半边）。
//
// 数据面 = 分享 token 钉住的版本快照经 /api/public/quote/{token}（客户安全
// 投影：成本/毛利/供应商身份物理不进响应体，编译期锁 + 响应体断言双锁）。
//
// 呈现纪律（风暴档 §3.5）：
//  - 水印：报价链接自带时效（valid_until）+ 页面浅色水印（客户名 + 有效期）
//    ——外泄可溯源；真正的逐访问水印需要服务端渲染，MVP 以时效 + 审计兜底；
//  - 打印样式（打印样式兜底 PDF，风暴档 §4 件4）：window.print() + @media
//    print 隐藏按钮/水印加重 —— 浏览器「打印为 PDF」即客户可存档形态；
//    服务端 PDF 生成（gofpdf 依赖裁决）登记为遗留，不做第二把渲染尺；
//  - 过期/撤销/无链接一律同一「不可用」态（不区分，不泄露状态语义）。
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../components/ui/CardList.vue'
import { fetchPublicQuote, type PublicQuote } from '../api/publicQuote'

const { t } = useI18n()
const route = useRoute()

const quote = ref<PublicQuote | null>(null)
const unavailable = ref(false)
const loading = ref(true)

const quoteItemFields = computed<CardField[]>(() => [
  { key: 'est_tokens_in', label: t('publicQuote.colTokensIn'), format: value => fmtTokens(Number(value)) },
  { key: 'est_tokens_out', label: t('publicQuote.colTokensOut'), format: value => fmtTokens(Number(value)) },
  { key: 'rate_in', label: t('publicQuote.colRateIn') },
  { key: 'rate_out', label: t('publicQuote.colRateOut') },
  { key: 'discount_pct', label: t('publicQuote.colDiscount'), format: value => value ? `${value}%` : '—' },
])
const subtotalField = computed<CardField>(() => ({
  key: 'subtotal_usd', label: t('publicQuote.colSubtotal'), type: 'metric', format: value => fmtUsd(Number(value)),
}))

const token = computed(() => String(route.params.token || ''))

onMounted(async () => {
  if (!token.value) {
    unavailable.value = true
    loading.value = false
    return
  }
  try {
    quote.value = await fetchPublicQuote(token.value)
  } catch {
    // 统一「不可用」态：不区分 404/429/5xx（后端对一切失败路径不区分，
    // 前端对齐；instanceof 判别在统一态下无行为差异——保留 import 以钉
    // 错误类型契约，分支省略）。
    unavailable.value = true
  } finally {
    loading.value = false
  }
})

function fmtTokens(n: number): string {
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)}B`
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(0)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}K`
  return String(n)
}

function fmtUsd(v: number): string {
  return `$${v.toFixed(2)}`
}

function fmtDate(iso: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString()
}

function printQuote() {
  window.print()
}
</script>

<template>
  <div class="pq-page">
    <div v-if="loading" class="pq-state">{{ t('publicQuote.loading') }}</div>

    <div v-else-if="unavailable" class="pq-state pq-unavailable">
      <svg class="pq-unavailable-icon" width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
        <path d="M12 3 2 21h20L12 3Z" stroke-linejoin="round" />
        <path d="M12 9v5m0 3v.1" stroke-linecap="round" />
      </svg>
      <p class="pq-unavailable-title">{{ t('publicQuote.unavailableTitle') }}</p>
      <p class="pq-unavailable-hint">{{ t('publicQuote.unavailableHint') }}</p>
    </div>

    <div v-else-if="quote" class="pq-card">
      <header class="pq-header">
        <div>
          <h1 class="pq-title">{{ quote.title || t('publicQuote.defaultTitle') }}</h1>
          <p class="pq-sub">{{ t('publicQuote.forCustomer', { name: quote.customer_name }) }}</p>
        </div>
        <div class="pq-meta">
          <span class="pq-tier">{{ t('publicQuote.tierLabel', { tier: quote.tier }) }}</span>
          <span class="pq-valid">{{ t('publicQuote.validUntil', { date: fmtDate(quote.valid_until) }) }}</span>
        </div>
      </header>

      <div v-if="quote.expired" class="pq-expired pq-expired-banner">
        {{ t('publicQuote.expiredBanner') }}
      </div>

      <ResponsiveDataView
        :rows="quote.items"
        title-key="canonical_name"
        :title-format="row => String(row.display_name || row.canonical_name)"
        :subtitle-keys="['volume_note']"
        :fields="quoteItemFields"
        :primary-field="subtotalField"
        table-min-width="720px"
      >
        <template #table>
      <table class="pq-table">
        <thead>
          <tr>
            <th class="pq-col-model">{{ t('publicQuote.colModel') }}</th>
            <th class="pq-col-tokens">{{ t('publicQuote.colTokensIn') }}</th>
            <th class="pq-col-tokens">{{ t('publicQuote.colTokensOut') }}</th>
            <th class="pq-col-rate">{{ t('publicQuote.colRateIn') }}</th>
            <th class="pq-col-rate">{{ t('publicQuote.colRateOut') }}</th>
            <th class="pq-col-disc">{{ t('publicQuote.colDiscount') }}</th>
            <th class="pq-col-sub">{{ t('publicQuote.colSubtotal') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="it in quote.items" :key="it.canonical_name">
            <td class="pq-col-model">
              <div class="pq-model-name">{{ it.display_name || it.canonical_name }}</div>
              <div v-if="it.volume_note" class="pq-volume-note">{{ it.volume_note }}</div>
            </td>
            <td class="pq-col-tokens">{{ fmtTokens(it.est_tokens_in) }}</td>
            <td class="pq-col-tokens">{{ fmtTokens(it.est_tokens_out) }}</td>
            <td class="pq-col-rate">{{ it.rate_in != null ? it.rate_in : '—' }}</td>
            <td class="pq-col-rate">{{ it.rate_out != null ? it.rate_out : '—' }}</td>
            <td class="pq-col-disc">{{ it.discount_pct ? `${it.discount_pct}%` : '—' }}</td>
            <td class="pq-col-sub">{{ fmtUsd(it.subtotal_usd) }}</td>
          </tr>
        </tbody>
      </table>
        </template>
      </ResponsiveDataView>
      <div class="pq-total-row">
        <span class="pq-total-label">{{ t('publicQuote.total') }}</span>
        <span class="pq-total">{{ fmtUsd(quote.sale_total_usd) }}</span>
      </div>

      <p class="pq-note">{{ t('publicQuote.pricingNote') }}</p>

      <div class="pq-actions pq-no-print">
        <button type="button" class="pq-print-btn" @click="printQuote">{{ t('publicQuote.print') }}</button>
      </div>

      <div class="pq-watermark" aria-hidden="true">
        <span>{{ quote.customer_name }}</span>
        <span>{{ fmtDate(quote.valid_until) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 间距走 4/8/12/16… 阶（design-spacing 尺是观测尺，新页面按阶起笔） */
.pq-page {
  min-height: 60vh;
  padding: 24px 16px;
  display: flex;
  justify-content: center;
  align-items: flex-start;
}
.pq-state {
  margin-top: 48px;
  color: var(--el-text-color-secondary);
  text-align: center;
}
.pq-unavailable-icon { display: block; margin: 0 auto; }
.pq-unavailable-title { font-size: 16px; margin: 12px 0 4px; }
.pq-unavailable-hint { font-size: 13px; }
.pq-card {
  position: relative;
  width: 100%;
  max-width: 880px;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 24px;
  overflow: hidden;
}
.pq-header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  border-bottom: 1px solid var(--el-border-color-lighter);
  padding-bottom: 16px;
}
.pq-title { font-size: 20px; margin: 0 0 4px; }
.pq-sub { color: var(--el-text-color-secondary); margin: 0; }
.pq-meta { text-align: right; color: var(--el-text-color-secondary); font-size: 13px; display: flex; flex-direction: column; gap: 4px; }
.pq-expired { margin-top: 16px; }
.pq-expired-banner {
  border: 1px solid var(--warning-bd);
  background: var(--warning-bg);
  color: var(--warning);
  border-radius: 4px;
  padding: 8px 12px;
  font-size: 13px;
}
.pq-table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 16px;
  font-size: 14px;
}
.pq-table th, .pq-table td {
  border-bottom: 1px solid var(--el-border-color-lighter);
  padding: 8px 8px;
  text-align: left;
}
.pq-table th { color: var(--el-text-color-secondary); font-weight: 500; }
.pq-col-tokens, .pq-col-rate, .pq-col-disc, .pq-col-sub { text-align: right; white-space: nowrap; }
.pq-model-name { font-weight: 500; }
.pq-volume-note { color: var(--el-text-color-secondary); font-size: 12px; }
.pq-total-row { display: flex; justify-content: flex-end; gap: 16px; padding-top: 12px; }
.pq-total-label { text-align: right; font-weight: 600; }
.pq-total { font-weight: 600; }
.pq-note { color: var(--el-text-color-secondary); font-size: 12px; margin-top: 12px; }
.pq-actions { margin-top: 16px; display: flex; justify-content: flex-end; }
.pq-print-btn {
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  border-radius: 4px;
  padding: 8px 16px;
  cursor: pointer;
  font-size: 14px;
}
.pq-print-btn:hover { border-color: var(--el-color-primary); color: var(--el-color-primary); }
.pq-watermark {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 48px;
  pointer-events: none;
  font-size: 40px;
  font-weight: 700;
  color: color-mix(in srgb, var(--kx-text) 4%, transparent);
  transform: rotate(-18deg);
  user-select: none;
}
/* 打印样式兜底 PDF：按钮隐藏、水印加重、容器去壳 */
@media print {
  :deep(.responsive-data-view__switch) { display: none !important; }
  .pq-no-print { display: none !important; }
  .pq-page { padding: 0; }
  .pq-card { border: none; max-width: none; }
  .pq-watermark { color: color-mix(in srgb, var(--kx-text) 8%, transparent); }
}
</style>
