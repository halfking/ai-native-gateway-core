<script setup lang="ts">
/**
 * ThemeToggle — 单图标按钮（☼/☾），同一位置渲染。
 * 与 ai-native-maintain 视觉一致（text glyph）。
 * 2026-07-21: 替换原 SVG 双图标方案，统一为单图标按钮。
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { detectTheme, setTheme } from '../theme'
import { Moon, Sunny } from '@element-plus/icons-vue'

const { t } = useI18n()
const theme = ref<'light' | 'dark'>('light')

onMounted(() => {
  theme.value = detectTheme()
})

const isDark = computed(() => theme.value === 'dark')

function toggle() {
  const next = isDark.value ? 'light' : 'dark'
  theme.value = next
  setTheme(next)
}
</script>

<template>
  <button
    type="button"
    class="theme-toggle"
    :aria-label="isDark ? t('app.theme.switchToLight') : t('app.theme.switchToDark')"
    :title="isDark ? t('app.theme.lightTitle') : t('app.theme.darkTitle')"
    :aria-pressed="isDark"
    @click="toggle"
  >
    <span class="theme-toggle-glyph" aria-hidden="true"><Moon v-if="isDark" /><Sunny v-else /></span>
  </button>
</template>

<style scoped>
.theme-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: 8px;
  border: 1px solid var(--kx-border, var(--border));
  background: var(--kx-surface, var(--card));
  color: var(--kx-muted, var(--muted));
  cursor: pointer;
  transition: background 0.18s ease, color 0.18s ease, border-color 0.18s ease;
}
.theme-toggle:hover {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
  border-color: var(--kx-primary, var(--accent));
}
.theme-toggle-glyph {
  font-size: 16px;
  line-height: 1;
  font-weight: 600;
}

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3 · 插值形态）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。 */
.theme-toggle-glyph > svg { width: 16px; height: 16px; }
</style>