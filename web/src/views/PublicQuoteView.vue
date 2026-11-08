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
import { fetchPublicQuote, PublicQuoteUnavailableError, type PublicQuote } from '../api/publicQuote'

const { t } = useI18n()
const route = useRoute()

const quote = ref<PublicQuote | null>(null)
const unavailable = ref(false)
const loading = ref(true)

const token = computed(() => String(route.params.token || ''))

onMounted(async () => {
  if (!token.value) {
    unavailable.value = true
    loading.value = false
    return
  }
  try {
    quote.value = await fetchPublicQuote(token.value)
  } catch (e) {
    if (e instanceof PublicQuoteUnavailableError) unavailable.value = true
    else unavailable.value = true
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
      <div class="pq-unavailable-icon">⚠</div>
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
        <tfoot>
          <tr>
            <td colspan="6" class="pq-total-label">{{ t('publicQuote.total') }}</td>
            <td class="pq-col-sub pq-total">{{ fmtUsd(quote.sale_total_usd) }}</td>
          </tr>
        </tfoot>
      </table>

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
  color: var(--el-text-color-secondary, #909399);
  text-align: center;
}
.pq-unavailable-icon { font-size: 32px; }
.pq-unavailable-title { font-size: 16px; margin: 12px 0 4px; }
.pq-unavailable-hint { font-size: 13px; }
.pq-card {
  position: relative;
  width: 100%;
  max-width: 880px;
  background: var(--el-bg-color, #fff);
  border: 1px solid var(--el-border-color-lighter, #ebeef5);
  border-radius: 8px;
  padding: 24px;
  overflow: hidden;
}
.pq-header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  border-bottom: 1px solid var(--el-border-color-lighter, #ebeef5);
  padding-bottom: 16px;
}
.pq-title { font-size: 20px; margin: 0 0 4px; }
.pq-sub { color: var(--el-text-color-secondary, #909399); margin: 0; }
.pq-meta { text-align: right; color: var(--el-text-color-secondary, #909399); font-size: 13px; display: flex; flex-direction: column; gap: 4px; }
.pq-expired { margin-top: 16px; }
.pq-expired-banner {
  border: 1px solid var(--el-color-warning-light-7, #f3d19e);
  background: var(--el-color-warning-light-9, #fdf6ec);
  color: var(--el-color-warning, #e6a23c);
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
  border-bottom: 1px solid var(--el-border-color-lighter, #ebeef5);
  padding: 8px 8px;
  text-align: left;
}
.pq-table th { color: var(--el-text-color-secondary, #909399); font-weight: 500; }
.pq-col-tokens, .pq-col-rate, .pq-col-disc, .pq-col-sub { text-align: right; white-space: nowrap; }
.pq-model-name { font-weight: 500; }
.pq-volume-note { color: var(--el-text-color-secondary, #909399); font-size: 12px; }
.pq-total-label { text-align: right; font-weight: 600; }
.pq-total { font-weight: 600; }
.pq-note { color: var(--el-text-color-secondary, #909399); font-size: 12px; margin-top: 12px; }
.pq-actions { margin-top: 16px; display: flex; justify-content: flex-end; }
.pq-print-btn {
  border: 1px solid var(--el-border-color, #dcdfe6);
  background: var(--el-bg-color, #fff);
  border-radius: 4px;
  padding: 8px 16px;
  cursor: pointer;
  font-size: 14px;
}
.pq-print-btn:hover { border-color: var(--el-color-primary, #409eff); color: var(--el-color-primary, #409eff); }
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
  color: rgba(0, 0, 0, 0.04);
  transform: rotate(-18deg);
  user-select: none;
}
/* 打印样式兜底 PDF：按钮隐藏、水印加重、容器去壳 */
@media print {
  .pq-no-print { display: none !important; }
  .pq-page { padding: 0; }
  .pq-card { border: none; max-width: none; }
  .pq-watermark { color: rgba(0, 0, 0, 0.08); }
}
</style>
