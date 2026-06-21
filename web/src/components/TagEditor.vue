<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { listTags, type TagInfo } from '../api'

const props = defineProps<{
  modelValue: string[]
  locked?: boolean
  suggestions?: string[]   // optional pre-supplied suggestions
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', tags: string[]): void
  (e: 'reset'): void
}>()

const draft = ref('')
const allTags = ref<TagInfo[]>([])
const showSuggest = ref(false)

const NAMESPACES = ['family', 'version', 'series', 'generation', 'variant', 'modality', 'cap', 'user']

async function loadTags() {
  if (props.suggestions && props.suggestions.length) return
  try {
    const r = await listTags()
    allTags.value = r.namespaces.flatMap((g) => g.tags)
  } catch { /* silent */ }
}

const suggestList = computed<string[]>(() => {
  if (props.suggestions && props.suggestions.length) return props.suggestions
  return allTags.value.map((t) => t.tag)
})

const filtered = computed(() => {
  const q = draft.value.trim().toLowerCase()
  if (!q) return suggestList.value.slice(0, 12)
  return suggestList.value
    .filter((t) => t.toLowerCase().includes(q) && !props.modelValue.includes(t))
    .slice(0, 12)
})

function addTag(raw: string) {
  const t = raw.trim()
  if (!t) return
  // basic namespace check; require <ns>:<value>
  if (!/^[\w]+:[\w\-\.:]+$/.test(t)) {
    return
  }
  if (props.modelValue.includes(t)) return
  emit('update:modelValue', [...props.modelValue, t])
  draft.value = ''
}

function removeTag(t: string) {
  emit('update:modelValue', props.modelValue.filter((x) => x !== t))
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' || e.key === ',') {
    e.preventDefault()
    addTag(draft.value)
  } else if (e.key === 'Backspace' && !draft.value && props.modelValue.length) {
    removeTag(props.modelValue[props.modelValue.length - 1])
  }
}

function onBlur() {
  // Vue template inline expressions don't resolve the global setTimeout,
  // so the blur handler is wrapped in a script-defined function. 150ms
  // delay lets the user click a suggestion before the dropdown closes.
  setTimeout(() => (showSuggest.value = false), 150)
}

function nsHint(ns: string) {
  draft.value = `${ns}:`
}

watch(() => props.suggestions, () => { loadTags() })
onMounted(loadTags)
</script>

<template>
  <div class="tag-editor">
    <div class="tag-row">
      <span v-for="t in modelValue" :key="t" class="chip">
        {{ t }}
        <button type="button" class="chip-x" @click="removeTag(t)">×</button>
      </span>
      <input
        v-model="draft"
        class="tag-input"
        placeholder="输入 <ns>:<值>，Enter 添加"
        @keydown="onKey"
        @focus="showSuggest = true"
        @blur="onBlur"
      />
    </div>
    <div class="ns-hints">
      <button
        v-for="ns in NAMESPACES"
        :key="ns"
        type="button"
        class="btn btn-ghost btn-sm"
        @click="nsHint(ns)"
      >{{ ns }}:</button>
      <span v-if="locked" class="badge badge-yellow" style="margin-left:8px">已锁定</span>
      <button
        v-if="locked"
        type="button"
        class="btn btn-ghost btn-sm"
        @click="emit('reset')"
      >重置为自动</button>
    </div>
    <ul v-if="showSuggest && filtered.length" class="suggest">
      <li v-for="s in filtered" :key="s" @mousedown.prevent="addTag(s)">{{ s }}</li>
    </ul>
  </div>
</template>

<style scoped>
.tag-editor { display: flex; flex-direction: column; gap: 6px; position: relative; }
.tag-row {
  display: flex; flex-wrap: wrap; gap: 6px; align-items: center;
  padding: 6px; border: 1px solid var(--border); border-radius: 6px; min-height: 36px;
  background: var(--bg);
}
.chip {
  background: var(--card); border: 1px solid var(--border); border-radius: 12px;
  padding: 2px 8px; font-size: 12px; display: inline-flex; align-items: center; gap: 4px;
}
.chip-x { background: none; border: none; cursor: pointer; color: var(--text-muted); font-size: 14px; line-height: 1; }
.tag-input { flex: 1; min-width: 160px; border: none; outline: none; background: transparent; font-size: 13px; color: var(--text); }
.ns-hints { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
.suggest {
  position: absolute; top: 100%; left: 0; right: 0; z-index: 10;
  margin-top: 2px; background: var(--card); border: 1px solid var(--border); border-radius: 6px;
  list-style: none; padding: 4px 0; max-height: 200px; overflow-y: auto;
  box-shadow: 0 4px 12px rgba(0,0,0,0.1);
}
.suggest li { padding: 4px 10px; cursor: pointer; font-size: 12px; }
.suggest li:hover { background: var(--bg); }
</style>
