<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { detectTheme, setTheme, type ThemeMode } from '../theme'

const theme = ref<ThemeMode>(detectTheme())

onMounted(() => {
  theme.value = detectTheme()
})

function select(next: ThemeMode) {
  theme.value = next
  setTheme(next)
}
</script>

<template>
  <div class="theme-toggle" role="group" aria-label="Theme">
    <button
      type="button"
      class="theme-btn"
      :class="{ active: theme === 'light' }"
      :aria-pressed="theme === 'light'"
      title="Light"
      @click="select('light')"
    >
      <span aria-hidden="true">☀</span>
    </button>
    <button
      type="button"
      class="theme-btn"
      :class="{ active: theme === 'dark' }"
      :aria-pressed="theme === 'dark'"
      title="Dark"
      @click="select('dark')"
    >
      <span aria-hidden="true">☾</span>
    </button>
  </div>
</template>

<style scoped>
.theme-toggle {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--card);
}
.theme-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 28px;
  border: 0;
  border-radius: calc(var(--radius) - 2px);
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  font-size: 13px;
}
.theme-btn:hover { color: var(--text); background: var(--bg-subtle); }
.theme-btn.active {
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 14%, transparent);
}
</style>
