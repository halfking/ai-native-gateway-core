<script setup lang="ts">
withDefaults(
  defineProps<{
    credits?: number | null
    costUsd?: number | null
    showCost?: boolean
    inline?: boolean
  }>(),
  {
    credits: null,
    costUsd: null,
    showCost: false,
    inline: false,
  },
)

function fmtCredits(n?: number | null) {
  if (n == null) return '—'
  return n.toLocaleString('zh-CN') + ' 积分'
}

function fmtCost(n?: number | null) {
  if (n == null) return '—'
  return '$' + n.toFixed(2)
}
</script>

<template>
  <div class="fee-cost-cell" :class="{ 'fee-cost-cell--inline': inline }">
    <span class="fee-main">{{ fmtCredits(credits) }}</span>
    <span v-if="showCost && costUsd != null" class="fee-cost-sub">成本 {{ fmtCost(costUsd) }}</span>
  </div>
</template>

<style scoped>
.fee-cost-cell {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 2px;
  line-height: 1.3;
}
.fee-cost-cell--inline {
  display: inline-flex;
  flex-direction: column;
  align-items: flex-start;
}
.fee-main {
  font-size: inherit;
  color: var(--text);
}
.fee-cost-sub {
  font-size: 11px;
  color: var(--muted);
  white-space: nowrap;
}
</style>
