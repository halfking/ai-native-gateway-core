<script setup lang="ts">
/**
 * Schema-driven plugin settings form (W2.4).
 * Renders SettingsSchema / adapted ConfigSchema; i18n via title_key / description_key.
 * No plugin-specific branches — hello and ASM share this path.
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import PluginErrorBoundary from './PluginErrorBoundary.vue'

export interface SettingsProperty {
  type: 'boolean' | 'integer' | 'number' | 'string'
  title?: string
  title_key?: string
  description?: string
  description_key?: string
  enum?: Array<string | number | boolean>
  minimum?: number
  maximum?: number
  default?: unknown
}

export interface SettingsSchema {
  type: 'object'
  properties: Record<string, SettingsProperty>
  'x-order'?: string[]
}

const props = defineProps<{
  schema: SettingsSchema | null
  modelValue?: Record<string, unknown>
  pluginId: string
}>()

const emit = defineEmits<{
  'update:modelValue': [Record<string, unknown>]
  submit: [Record<string, unknown>]
}>()

const { t, te } = useI18n()
const local = ref<Record<string, unknown>>({})
const formError = ref('')

watch(
  () => [props.schema, props.modelValue] as const,
  () => {
    const next: Record<string, unknown> = {}
    const order = props.schema?.['x-order'] ?? Object.keys(props.schema?.properties ?? {})
    for (const key of order) {
      const p = props.schema?.properties?.[key]
      if (!p) continue
      next[key] = props.modelValue?.[key] ?? p.default ?? defaultFor(p.type)
    }
    local.value = next
  },
  { immediate: true, deep: true },
)

const fields = computed(() => {
  const order = props.schema?.['x-order'] ?? Object.keys(props.schema?.properties ?? {})
  return order
    .map((key) => ({ key, prop: props.schema?.properties?.[key] }))
    .filter((x): x is { key: string; prop: SettingsProperty } => !!x.prop)
})

function defaultFor(type: SettingsProperty['type']): unknown {
  switch (type) {
    case 'boolean':
      return false
    case 'integer':
    case 'number':
      return 0
    default:
      return ''
  }
}

function labelOf(p: SettingsProperty): string {
  if (p.title_key && te(p.title_key)) return t(p.title_key)
  if (p.title_key) return p.title_key
  return p.title || ''
}

function descOf(p: SettingsProperty): string {
  if (p.description_key && te(p.description_key)) return t(p.description_key)
  if (p.description_key) return p.description_key
  return p.description || ''
}

function onField(key: string, value: unknown) {
  local.value = { ...local.value, [key]: value }
  emit('update:modelValue', { ...local.value })
  formError.value = ''
}

function onSubmit() {
  formError.value = ''
  emit('submit', { ...local.value })
}
</script>

<template>
  <PluginErrorBoundary :plugin-id="pluginId">
    <form v-if="schema" class="plugin-settings-form" @submit.prevent="onSubmit">
      <p v-if="formError" class="plugin-settings-form__error" role="alert">{{ formError }}</p>
      <div v-for="f in fields" :key="f.key" class="plugin-settings-form__row">
        <label :for="`ps-${pluginId}-${f.key}`">{{ labelOf(f.prop) }}</label>
        <p v-if="descOf(f.prop)" class="plugin-settings-form__hint">{{ descOf(f.prop) }}</p>
        <select
          v-if="f.prop.enum?.length"
          :id="`ps-${pluginId}-${f.key}`"
          :value="String(local[f.key] ?? '')"
          @change="onField(f.key, ($event.target as HTMLSelectElement).value)"
        >
          <option v-for="opt in f.prop.enum" :key="String(opt)" :value="String(opt)">{{ opt }}</option>
        </select>
        <input
          v-else-if="f.prop.type === 'boolean'"
          :id="`ps-${pluginId}-${f.key}`"
          type="checkbox"
          :checked="Boolean(local[f.key])"
          @change="onField(f.key, ($event.target as HTMLInputElement).checked)"
        />
        <input
          v-else-if="f.prop.type === 'integer' || f.prop.type === 'number'"
          :id="`ps-${pluginId}-${f.key}`"
          type="number"
          :min="f.prop.minimum"
          :max="f.prop.maximum"
          :value="Number(local[f.key] ?? 0)"
          @input="onField(f.key, Number(($event.target as HTMLInputElement).value))"
        />
        <input
          v-else
          :id="`ps-${pluginId}-${f.key}`"
          type="text"
          :value="String(local[f.key] ?? '')"
          @input="onField(f.key, ($event.target as HTMLInputElement).value)"
        />
      </div>
      <button type="submit" class="btn btn-primary">{{ t('plugin.settings.save') }}</button>
    </form>
    <p v-else class="plugin-settings-form__empty">{{ t('plugin.settings.noSchema') }}</p>
  </PluginErrorBoundary>
</template>

<style scoped>
.plugin-settings-form { display: flex; flex-direction: column; gap: 12px; max-width: 28rem; }
.plugin-settings-form__row { display: flex; flex-direction: column; gap: 4px; }
.plugin-settings-form__hint { margin: 0; color: var(--text-secondary); font-size: 14px; }
.plugin-settings-form__error { color: var(--danger); }
.plugin-settings-form__empty { color: var(--text-secondary); }
</style>
