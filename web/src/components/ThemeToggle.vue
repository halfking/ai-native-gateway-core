<script setup lang="ts">
// 2026-07-21: 单图标 ☼/☾ 切换（与 ai-native-maintain 对齐）。
// 当前是浅色 → 显示 ☾ "切到深色"；当前是深色 → 显示 ☼ "切到浅色"。
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { detectTheme, setTheme, type ThemeMode } from '../theme'

const { t } = useI18n()
const theme = ref<ThemeMode>(detectTheme())

onMounted(() => {
  theme.value = detectTheme()
  // 监听跨组件 / URL 主题变化（其他 topbar 调用 setTheme 时本组件也要刷新）
  const obs = new MutationObserver(() => {
    const next = detectTheme()
    if (next !== theme.value) theme.value = next
  })
  obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})

function toggle() {
  const next: ThemeMode = theme.value === 'dark' ? 'light' : 'dark'
  theme.value = next
  setTheme(next)
}
</script>

<template>
  <button
    type="button"
    class="theme-toggle"
    :title="theme === 'dark' ? t('theme.switchToLight', '切到浅色') : t('theme.switchToDark', '切到深色')"
    :aria-label="theme === 'dark' ? t('theme.lightTitle', '浅色模式') : t('theme.darkTitle', '深色模式')"
    @click="toggle"
  >
    <span aria-hidden="true">{{ theme === 'dark' ? '☼' : '☾' }}</span>
  </button>
</template>

<style scoped>
.theme-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  padding: 0;
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 8px;
  background: var(--kx-surface-soft, var(--card));
  color: var(--kx-text, var(--text));
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
  transition: background 0.15s ease, color 0.15s ease, border-color 0.15s ease;
}
.theme-toggle:hover {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
  border-color: var(--kx-primary, var(--accent));
}
.theme-toggle:focus-visible {
  outline: 2px solid var(--kx-primary, var(--accent));
  outline-offset: 2px;
}
</style>
