<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { setLocale } from '../store'
import { languages } from '../i18n'

const { locale } = useI18n()
const isOpen = ref(false)

const currentLanguage = computed(() => {
  return languages.find(lang => lang.code === locale.value) || languages[0]
})

function toggleDropdown() {
  isOpen.value = !isOpen.value
}

function selectLanguage(code: string) {
  locale.value = code
  setLocale(code)
  isOpen.value = false
}

// 点击外部关闭下拉菜单
function handleClickOutside(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target.closest('.language-selector')) {
    isOpen.value = false
  }
}

// 监听全局点击事件
if (typeof window !== 'undefined') {
  document.addEventListener('click', handleClickOutside)
}
</script>

<template>
  <div class="language-selector">
    <button
      type="button"
      class="btn btn-ghost btn-sm language-btn"
      @click.stop="toggleDropdown"
      :aria-label="'Switch language'"
    >
      <span class="language-flag">{{ currentLanguage.flag }}</span>
      <span class="language-code">{{ currentLanguage.code }}</span>
    </button>
    
    <div v-if="isOpen" class="language-dropdown">
      <button
        v-for="lang in languages"
        :key="lang.code"
        type="button"
        class="language-option"
        :class="{ active: locale === lang.code }"
        @click.stop="selectLanguage(lang.code)"
      >
        <span class="language-flag">{{ lang.flag }}</span>
        <span class="language-name">{{ lang.name }}</span>
        <span v-if="locale === lang.code" class="check-mark">✓</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.language-selector {
  position: relative;
  display: inline-block;
}

.language-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  font-size: 14px;
  cursor: pointer;
}

.language-flag {
  font-size: 16px;
  line-height: 1;
}

.language-code {
  font-size: 12px;
  opacity: 0.9;
}

.language-dropdown {
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  min-width: 180px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
  z-index: 1000;
  overflow: hidden;
}

.language-option {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border: none;
  background: transparent;
  color: var(--text);
  font-size: 14px;
  text-align: left;
  cursor: pointer;
  transition: background 0.15s ease;
}

.language-option:hover {
  background: var(--bg-subtle);
}

.language-option.active {
  background: var(--bg-subtle);
  color: var(--accent);
}

.language-name {
  flex: 1;
}

.check-mark {
  color: var(--accent);
  font-weight: bold;
  margin-left: auto;
}

/* 响应式：小屏幕隐藏语言代码 */
@media (max-width: 768px) {
  .language-code {
    display: none;
  }
}
</style>
