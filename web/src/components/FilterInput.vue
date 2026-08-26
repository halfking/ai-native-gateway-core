<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'

const props = defineProps<{
  modelValue: string
  options: string[]
  placeholder?: string
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
}>()

const open = ref(false)
const highlighted = ref(0)
const root = ref<HTMLElement | null>(null)

const filtered = computed(() => {
  const q = props.modelValue.trim().toLowerCase()
  if (!q) return props.options
  return props.options.filter((o) => o.toLowerCase().includes(q))
})

function onInput(e: Event) {
  const t = e.target as HTMLInputElement
  emit('update:modelValue', t.value)
  open.value = true
  highlighted.value = 0
}

function pick(value: string) {
  emit('update:modelValue', value)
  open.value = false
}

function onFocus() {
  open.value = true
  highlighted.value = 0
}

function onKeyDown(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') {
    open.value = true
    highlighted.value = Math.min(highlighted.value + 1, filtered.value.length - 1)
    e.preventDefault()
  } else if (e.key === 'ArrowUp') {
    highlighted.value = Math.max(highlighted.value - 1, 0)
    e.preventDefault()
  } else if (e.key === 'Enter') {
    if (open.value && filtered.value[highlighted.value]) {
      pick(filtered.value[highlighted.value])
      e.preventDefault()
    }
  } else if (e.key === 'Escape') {
    open.value = false
  }
}

function onClickDoc(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) {
    open.value = false
  }
}

if (typeof window !== 'undefined') {
  document.addEventListener('mousedown', onClickDoc)
  onBeforeUnmount(() => document.removeEventListener('mousedown', onClickDoc))
}
</script>

<template>
  <div ref="root" class="filter-input">
    <input
      class="input"
      :placeholder="placeholder"
      :value="modelValue"
      @input="onInput"
      @focus="onFocus"
      @keydown="onKeyDown"
    />
    <ul v-if="open && filtered.length" class="filter-suggest">
      <li
        v-for="(opt, idx) in filtered"
        :key="opt"
        :class="{ active: idx === highlighted }"
        @mousedown.prevent="pick(opt)"
        @mouseenter="highlighted = idx"
      >{{ opt }}</li>
    </ul>
    <ul v-else-if="open && !filtered.length" class="filter-suggest">
      <li class="empty">无匹配项</li>
    </ul>
  </div>
</template>

<style scoped>
.filter-input {
  position: relative;
  display: inline-block;
}
.filter-suggest {
  position: absolute;
  top: 100%;
  left: 0;
  min-width: 100%;
  max-height: 240px;
  overflow-y: auto;
  margin: 2px 0 0;
  padding: 4px 0;
  list-style: none;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  z-index: 50;
}
.filter-suggest li {
  padding: 6px 12px;
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
}
.filter-suggest li:hover,
.filter-suggest li.active {
  background: var(--border);
}
.filter-suggest li.empty {
  color: var(--muted);
  cursor: default;
}
.filter-suggest li.empty:hover {
  background: transparent;
}
</style>
