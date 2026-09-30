<script setup lang="ts">
// BoardHeroRow.vue — 看板英雄区 4 大卡（2026-09-30 重构轮，对齐效果图 A 节）。
// 总请求（成功率/错误数入口+迷你趋势线）· 总 Token（输入/输出拆分）· 总费用 · 总积分（特有计费口径，高亮）。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BoardPayload } from '../../api/board'

const props = defineProps<{
  board: BoardPayload | null | undefined
  loading?: boolean
}>()

const emit = defineEmits<{
  openErrors: []
}>()

const { t } = useI18n()

const summary = computed(() => props.board?.summary)

const totalTokens = computed(() => {
  const s = summary.value
  if (!s) return undefined
  return s.total_tokens ?? (s.total_prompt_tokens ?? 0) + (s.total_completion_tokens ?? 0)
})

const errorCount = computed(() => {
  const pies = props.board?.pies?.errors
  if (pies?.length) return pies.reduce((acc, p) => acc + (p.requests ?? 0), 0)
  const s = summary.value
  if (!s || s.total_requests == null || s.success_rate == null) return undefined
  return Math.max(0, Math.round(s.total_requests * (1 - s.success_rate)))
})

/** 迷你趋势线：最近 16 桶请求数归一化后的 SVG 折线点。 */
const sparkPoints = computed(() => {
  const trends = props.board?.trends ?? []
  const recent = trends.slice(-16)
  if (recent.length < 2) return ''
  const max = Math.max(...recent.map((p) => p.requests ?? 0), 1)
  const w = 72
  const h = 26
  return recent
    .map((p, i) => {
      const x = (i / (recent.length - 1)) * w
      const y = h - ((p.requests ?? 0) / max) * (h - 4) - 2
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
})

function fmt(n: number | undefined) {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toLocaleString()
}

function fmtCost(v: number | undefined) {
  if (v == null) return '—'
  const n = Number(v)
  // 英雄卡：≥ $1 显示两位（对齐效果图 $137.13），不足 $1 保留四位以免被舍成 0。
  return '$' + n.toFixed(Math.abs(n) >= 1 ? 2 : 4)
}

function fmtPct(v: number | undefined) {
  if (v == null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}

function fmtTokensCompact(n: number | undefined) {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toLocaleString()
}
</script>

<template>
  <div v-if="loading && !summary" class="hero-row hero-row--skeleton">
    <div v-for="i in 4" :key="i" class="hero-card hero-card--skeleton" />
  </div>
  <div v-else-if="summary" class="hero-row">
    <div class="hero-card">
      <div class="hero-card__label">{{ t('dashboard.stat.totalRequests') }}</div>
      <div class="hero-card__value">{{ fmt(summary.total_requests) }}</div>
      <div class="hero-card__sub">
        <span>{{ t('dashboard.stat.successRate') }}
          <b :style="{ color: (summary.success_rate ?? 1) > 0.95 ? 'var(--success)' : 'var(--warning)' }">{{ fmtPct(summary.success_rate) }}</b>
        </span>
        <span>{{ t('dashboard.board.heroErrors') }}
          <b style="color: var(--danger)">{{ fmt(errorCount) }}</b>
          <button type="button" class="hero-card__drill" @click="emit('openErrors')">{{ t('dashboard.board.drillDown') }} →</button>
        </span>
      </div>
      <svg v-if="sparkPoints" class="hero-card__spark" width="72" height="26" viewBox="0 0 72 26" aria-hidden="true">
        <polyline :points="sparkPoints" fill="none" stroke="var(--accent)" stroke-width="2" stroke-linecap="round" />
      </svg>
    </div>
    <div class="hero-card">
      <div class="hero-card__label">{{ t('dashboard.v2.totalTokensShort') }}</div>
      <div class="hero-card__value">{{ fmtTokensCompact(totalTokens) }}</div>
      <div class="hero-card__sub">
        <span>{{ t('dashboard.board.heroInput') }} <b>{{ fmtTokensCompact(summary.total_prompt_tokens) }}</b></span>
        <span>{{ t('dashboard.board.heroOutput') }} <b>{{ fmtTokensCompact(summary.total_completion_tokens) }}</b></span>
      </div>
    </div>
    <div class="hero-card">
      <div class="hero-card__label">{{ t('dashboard.stat.totalCost') }}</div>
      <div class="hero-card__value">{{ fmtCost(summary.total_cost_usd) }}</div>
      <div class="hero-card__sub">
        <span>{{ t('dashboard.board.heroCreditsEq', { n: fmt(summary.total_credits_charged) }) }}</span>
      </div>
    </div>
    <div class="hero-card hero-card--highlight">
      <div class="hero-card__label">{{ t('dashboard.v2.totalCredits') }}</div>
      <div class="hero-card__value">{{ fmt(summary.total_credits_charged) }}</div>
      <div class="hero-card__sub">{{ t('dashboard.v2.creditsSub') }}</div>
    </div>
  </div>
</template>

<style scoped>
.hero-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.hero-card {
  position: relative;
  overflow: hidden;
  background: linear-gradient(135deg, color-mix(in srgb, var(--accent) 8%, var(--card)), var(--card) 65%);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 14px 16px 12px;
}
.hero-card--highlight {
  border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--accent) 22%, transparent) inset;
}
.hero-card__label {
  font-size: 11.5px;
  color: var(--text-muted);
  font-weight: 600;
}
.hero-card__value {
  font-size: 24px;
  font-weight: 700;
  margin-top: 4px;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.01em;
}
.hero-card__sub {
  margin-top: 6px;
  font-size: 11.5px;
  color: var(--text-muted);
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.hero-card__sub b {
  color: var(--text);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.hero-card__drill {
  border: 0;
  background: transparent;
  color: var(--accent);
  font-size: 11.5px;
  font-weight: 600;
  cursor: pointer;
  padding: 0;
}
.hero-card__drill:hover {
  text-decoration: underline;
}
.hero-card__spark {
  position: absolute;
  right: 12px;
  top: 12px;
  opacity: 0.9;
}
.hero-row--skeleton {
  min-height: 96px;
}
.hero-card--skeleton {
  background: linear-gradient(90deg, var(--border) 25%, transparent 37%, var(--border) 63%);
  background-size: 400% 100%;
  animation: hero-shimmer 1.2s ease infinite;
}
@keyframes hero-shimmer {
  0% { background-position: 100% 0; }
  100% { background-position: 0 0; }
}
@media (max-width: 1024px) {
  .hero-row {
    grid-template-columns: repeat(2, 1fr);
  }
}
@media (max-width: 480px) {
  .hero-row {
    grid-template-columns: 1fr;
  }
}
</style>
