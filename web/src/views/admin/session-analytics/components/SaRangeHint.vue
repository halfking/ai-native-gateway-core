<script setup lang="ts">
// SaRangeHint.vue — 当前统计区间的文字提示（2026-11-20）
//
// 为什么需要：页面上只显示日期选择器的话，用户不知道**当前生效的区间**
// 是哪一段（改了但没点「应用」时尤其容易误读）。把区间起止显式写出来，
// 页面上的每个数字才有可追溯的口径。
//
// ★ 这两个键（sessionAnalytics.range.start / .end）只在这里引用 ——
//   语言包里的键必须真的被 t() 取用，否则会被「共有死键」门判红。

import { useI18n } from 'vue-i18n'

const props = defineProps<{
  /** YYYY-MM-DD */
  start: string
  /** YYYY-MM-DD */
  end: string
}>()

const { t } = useI18n()
</script>

<template>
  <span class="sa-range-hint">
    {{ t('sessionAnalytics.range.start') }} {{ props.start }} ·
    {{ t('sessionAnalytics.range.end') }} {{ props.end }}
  </span>
</template>

<style scoped>
.sa-range-hint {
  color: var(--muted);
  font-size: 12px;
  font-family: var(--font-mono, monospace);
}
</style>
