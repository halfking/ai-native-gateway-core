<script setup lang="ts">
// WorkModeSwitch.vue — 工作模式切换器（简易 / 专家）
//
// 2026-11-17：简易模式落地轮。
//
// ★ 它切的是**界面投影**，不是后端行为：切过去只改导航与首页渲染，
//   数据、接口语义、设置全部不动 ⇒ 随时可以切回来。
//
// ★ 本机选择 vs 平台默认：本按钮写的是**本机选择**（localStorage），
//   平台默认是 settings 里的 `gateway.mode`（super_admin 在设置页改，影响所有人）。
//   本按钮**不改平台默认** —— 不替别人做决定。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkMode } from '../../composables/useWorkMode'

const { t } = useI18n()
const { mode, isSimple, setMode } = useWorkMode()

const label = computed(() => (isSimple.value ? t('simpleMode.mode.simple') : t('simpleMode.mode.expert')))
const hint = computed(() => (isSimple.value ? t('simpleMode.mode.simpleHint') : t('simpleMode.mode.expertHint')))
const toggleLabel = computed(() =>
  isSimple.value ? t('simpleMode.mode.switchToExpert') : t('simpleMode.mode.switchToSimple'),
)

function toggle(): void {
  setMode(isSimple.value ? 'expert' : 'simple')
}
</script>

<template>
  <button
    type="button"
    class="work-mode-switch"
    :class="{ 'work-mode-switch--simple': isSimple }"
    :title="hint"
    :aria-label="toggleLabel"
    :data-work-mode="mode"
    @click="toggle"
  >
    <span class="work-mode-switch__dot" aria-hidden="true"></span>
    <span class="work-mode-switch__label">{{ label }}</span>
  </button>
</template>

<style scoped>
.work-mode-switch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 8px;
  background: transparent;
  color: var(--kx-muted, var(--muted));
  font-size: 12px;
  font-weight: 600;
  line-height: 1.2;
  white-space: nowrap;
  cursor: pointer;
}

.work-mode-switch:hover {
  color: var(--kx-text, var(--text));
  background: var(--bg-hover);
}

.work-mode-switch__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--kx-muted, var(--muted));
}

.work-mode-switch--simple {
  color: var(--kx-primary, var(--primary));
  border-color: var(--kx-primary, var(--primary));
}

.work-mode-switch--simple .work-mode-switch__dot {
  background: var(--kx-primary, var(--primary));
}
</style>
