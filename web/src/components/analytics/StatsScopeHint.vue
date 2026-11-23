<script setup lang="ts">
// StatsScopeHint.vue — 「统计说明」提示符（2026-11-24 紧凑轮）。
//
// ★ 为什么从「常驻横幅」改成「点击才展开的提示符」：
//   统计说明（口径 + 注意事项）是**解释性**内容，不是状态；常驻横幅把它顶成
//   页面最显眼的一块，反而挤掉了真正需要扫读的数字区。
//   ⇒ 收成一枚提示符，挂在**刷新按钮旁边**（用户正要读数字、正要判断数据新不新
//     的那个位置），点击才展开全文。
//
// ★ 两处统计页共用同一枚提示符：
//   · 仪表盘「会话与统计」页签（DashboardViewV2 的刷新按钮旁）
//   · 会话分析·用户页（SessionAnalyticsUsersView 的刷新按钮旁）
//   ⇒ 两页的说明文字、措辞、口径完全一致（同一份 i18n 键）。
//
// ★ days 传的是**当前生效窗口**的日历日跨度（含首尾）：
//   正文那句「首次请求在近{days}天内」必须与实际取数窗口一致，
//   否则提示符自己就在说假话。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElIcon, ElPopover } from 'element-plus'
import { InfoFilled } from '@element-plus/icons-vue'

const props = withDefaults(
  defineProps<{
    /** 当前生效窗口的日历日跨度（含首尾） */
    days?: number
  }>(),
  { days: 1 },
)

const { t } = useI18n()
const visible = ref(false)

const body = computed(() => t('sessions.stats.scopeNoticeBody', { days: props.days }))
</script>

<template>
  <ElPopover
    v-model:visible="visible"
    trigger="click"
    placement="bottom-start"
    :width="460"
    popper-class="stats-scope-hint-popper"
  >
    <template #reference>
      <button
        type="button"
        class="stats-scope-hint"
        data-testid="stats-scope-hint"
        :aria-label="t('sessions.stats.scopeHintLabel')"
        :title="t('sessions.stats.scopeHintLabel')"
      >
        <el-icon :size="14"><InfoFilled /></el-icon>
        <span>{{ t('sessions.stats.scopeHintLabel') }}</span>
      </button>
    </template>

    <div class="stats-scope-hint__body">
      <div class="stats-scope-hint__title">{{ t('sessions.stats.scopeNoticeTitle') }}</div>
      <p class="stats-scope-hint__line">{{ body }}</p>
      <p class="stats-scope-hint__line stats-scope-hint__line--muted">
        {{ t('sessions.stats.scopeNoticeHint') }}
      </p>
    </div>
  </ElPopover>
</template>

<style scoped>
.stats-scope-hint {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--border);
  background: var(--card);
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 600;
  border-radius: 9px;
  padding: 4px 10px;
  cursor: pointer;
  transition: color 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
}
.stats-scope-hint:hover {
  color: var(--accent);
  border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
}
.stats-scope-hint__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.stats-scope-hint__title {
  font-size: 12px;
  font-weight: 700;
  color: var(--text);
}
.stats-scope-hint__line {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text);
}
.stats-scope-hint__line--muted {
  color: var(--text-muted);
}
</style>

<style>
/* popper 挂 body ⇒ 全局样式（仍全令牌）。 */
.stats-scope-hint-popper.stats-scope-hint-popper {
  --el-popper-border-radius: 14px;
}
</style>
