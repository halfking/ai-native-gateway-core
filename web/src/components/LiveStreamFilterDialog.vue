<script setup lang="ts">
// LiveStreamFilterDialog — 实时请求流多维筛选弹窗（全部 + 搜索 + 完整选项）
import { computed, ref, watch } from 'vue'
import { Close } from '@element-plus/icons-vue'

const props = withDefaults(defineProps<{
  open: boolean
  title: string
  options: string[]
  selected: string[]
  /** 选项展示文案；缺省则显示 value 本身 */
  labelOf?: (value: string) => string
  searchable?: boolean
}>(), {
  searchable: true,
  labelOf: undefined,
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  apply: [selected: string[]]
}>()

const draft = ref<Set<string>>(new Set())
const search = ref('')

watch(
  () => props.open,
  (v) => {
    if (v) {
      draft.value = new Set(props.selected)
      search.value = ''
    }
  },
)

const filteredOptions = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return props.options
  return props.options.filter((opt) => {
    const label = (props.labelOf?.(opt) || opt).toLowerCase()
    return label.includes(q) || opt.toLowerCase().includes(q)
  })
})

const selectedCount = computed(() => draft.value.size)
const isAll = computed(() => draft.value.size === 0)

function label(opt: string) {
  return props.labelOf?.(opt) || opt
}

function toggle(opt: string) {
  const next = new Set(draft.value)
  if (next.has(opt)) next.delete(opt)
  else next.add(opt)
  draft.value = next
}

function selectAll() {
  draft.value = new Set()
}

function close() {
  emit('update:open', false)
}

function apply() {
  emit('apply', Array.from(draft.value))
  emit('update:open', false)
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="lsfd-backdrop" @click.self="close">
      <div class="lsfd-dialog" role="dialog" :aria-label="title">
        <header class="lsfd-head">
          <h3 class="lsfd-title">{{ title }}</h3>
          <button type="button" class="lsfd-close" :aria-label="$t('common.close')" @click="close"><Close /></button>
        </header>

        <div class="lsfd-toolbar">
          <button
            type="button"
            class="lsfd-all"
            :class="{ 'lsfd-all--active': isAll }"
            @click="selectAll"
          >
            {{ $t('common.all') }}
          </button>
          <span class="lsfd-count">已选 {{ selectedCount }}</span>
          <input
            v-if="searchable && options.length > 8"
            v-model="search"
            class="lsfd-search"
            type="search"
            placeholder="搜索…"
          />
        </div>

        <div class="lsfd-list">
          <label
            v-for="opt in filteredOptions"
            :key="opt"
            class="lsfd-option"
          >
            <input
              type="checkbox"
              :checked="draft.has(opt)"
              @change="toggle(opt)"
            />
            <span class="lsfd-option-label" :title="label(opt)">{{ label(opt) }}</span>
          </label>
          <div v-if="filteredOptions.length === 0" class="lsfd-empty">暂无可选项</div>
        </div>

        <footer class="lsfd-foot">
          <button type="button" class="lsfd-btn lsfd-btn--ghost" @click="close">{{ $t('common.button.cancel') }}</button>
          <button type="button" class="lsfd-btn lsfd-btn--primary" @click="apply">{{ $t('common.button.ok') }}</button>
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.lsfd-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1200;
  background: color-mix(in srgb, var(--kx-text) 40%, transparent);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
}
.lsfd-dialog {
  width: min(480px, 100%);
  max-height: min(72vh, 640px);
  display: flex;
  flex-direction: column;
  background: var(--card);
  color: var(--text, var(--surface-secondary));
  border-radius: 10px;
  box-shadow: var(--kx-shadow-md);
}
.lsfd-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  border-bottom: 1px solid var(--border, var(--kx-text));
}
.lsfd-title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}
.lsfd-close {
  border: 0;
  background: transparent;
  color: var(--muted, var(--muted));
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 0 4px;
}
.lsfd-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  flex-wrap: wrap;
}
.lsfd-all {
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 4px;
  border: 1px solid var(--border, var(--muted));
  background: var(--bg, var(--kx-text));
  color: var(--text);
  cursor: pointer;
}
.lsfd-all--active {
  border-color: var(--accent, var(--accent));
  color: var(--accent, var(--accent));
  font-weight: 600;
}
.lsfd-count {
  font-size: 11px;
  color: var(--muted, var(--muted));
}
.lsfd-search {
  flex: 1;
  min-width: 120px;
  font-size: 12px;
  padding: 5px 8px;
  border-radius: 4px;
  border: 1px solid var(--border, var(--muted));
  background: var(--bg, var(--kx-text));
  color: var(--text);
}
.lsfd-list {
  flex: 1;
  overflow-y: auto;
  padding: 4px 10px 10px;
  min-height: 120px;
}
.lsfd-option {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 8px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
}
.lsfd-option:hover {
  background: var(--bg-subtle, var(--kx-text));
}
.lsfd-option-label {
  flex: 1;
  word-break: break-word;
  line-height: 1.35;
}
.lsfd-empty {
  padding: 24px;
  text-align: center;
  color: var(--muted, var(--muted));
  font-size: 12px;
}
.lsfd-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 14px;
  border-top: 1px solid var(--border, var(--kx-text));
}
.lsfd-btn {
  font-size: 13px;
  padding: 6px 14px;
  border-radius: 6px;
  border: 1px solid var(--border, var(--muted));
  cursor: pointer;
}
.lsfd-btn--ghost {
  background: transparent;
  color: var(--text);
}
.lsfd-btn--primary {
  background: var(--accent, var(--accent));
  border-color: var(--accent, var(--accent));
  color: var(--on-primary);
}

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.lsfd-close > svg { width: 14px; height: 14px; }
</style>
