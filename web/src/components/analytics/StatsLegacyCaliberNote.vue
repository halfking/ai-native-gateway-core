<script setup lang="ts">
/**
 * StatsLegacyCaliberNote —— 「旧口径」披露标记（N1 修复轮，2026-11-24）。
 *
 * ★ 要解决的缺陷：会话统计面板一次拉 7 个端点，前端把 include_probes 发给
 *   全部，但后端**只在 session-overview 上认它** ⇒ session-trend /
 *   session-health / errors / performance 仍是旧宇宙 ⇒ 面板顶部的「含探测」
 *   徽标亮了（徽标读的是 session-overview 的 probe_scope），而趋势 / 健康度 /
 *   错误 / 性能那几块的数字还是另一把尺 ⇒ **一个面板里两把尺，且没有任何提示
 *   告诉读者哪个是哪个**。
 *
 * ★ 判定只看**响应**的 probe_scope（不看本地开关）——与面板徽标同一真源，
 *   详见同目录 statsLegacyCaliber.ts 的说明。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { isProbeScopeIncluded } from './statsLegacyCaliber'

const props = defineProps<{
  /** 会话总览响应里的 probe_scope（唯一判据；缺省 = 未知 ⇒ 不挂标记）。 */
  probeScope?: string | null
}>()

const { t } = useI18n()

const legacy = computed(() => isProbeScopeIncluded(props.probeScope))
</script>

<template>
  <span
    v-if="legacy"
    class="legacy-caliber-note"
    :title="t('dashboard.stats.legacyCaliberTitle')"
    data-testid="stats-legacy-caliber-note"
  >{{ t('dashboard.stats.legacyCaliberLabel') }}</span>
</template>

<style scoped>
.legacy-caliber-note {
  display: inline-block;
  font-size: 11px;
  line-height: 1.6;
  padding: 1px 6px;
  border-radius: 8px;
  border: 1px solid var(--border, var(--surface-secondary));
  background: var(--surface-secondary, rgba(0, 0, 0, 0.03));
  color: var(--muted, var(--text-secondary));
  cursor: help;
}
</style>
