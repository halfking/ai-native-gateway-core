<script setup lang="ts">
import { computed, ref } from 'vue'
import { revealCredentialKey } from '../api/providers'

const props = withDefaults(defineProps<{
  providerId?: number
  credentialId: number
  masked?: string | null
  canReveal?: boolean
  revealKey?: (credentialId: number, providerId?: number) => Promise<string>
  revealLabel?: string
  revealingLabel?: string
  hideLabel?: string
  copyLabel?: string
  errorLabel?: string
}>(), {
  canReveal: false,
  revealLabel: 'Reveal',
  revealingLabel: '…',
  hideLabel: 'Hide',
  copyLabel: 'Copy',
  errorLabel: 'Unable to reveal key',
})

const emit = defineEmits<{
  revealed: [key: string]
  hidden: []
  copied: []
}>()

const revealed = ref<string | null>(null)
const loading = ref(false)
const error = ref('')
const display = computed(() => revealed.value ?? props.masked ?? '—')

async function reveal() {
  if (!props.canReveal || loading.value) return
  loading.value = true
  error.value = ''
  try {
    const key = props.revealKey
      ? await props.revealKey(props.credentialId, props.providerId)
      : (props.providerId == null
          ? ''
          : (await revealCredentialKey(props.providerId, props.credentialId)).api_key)
    if (!key) throw new Error(props.errorLabel)
    revealed.value = key
    emit('revealed', key)
  } catch (err) {
    error.value = err instanceof Error ? err.message : props.errorLabel
  } finally {
    loading.value = false
  }
}

async function copy() {
  if (!revealed.value || typeof navigator === 'undefined' || !navigator.clipboard) return
  try {
    await navigator.clipboard.writeText(revealed.value)
    emit('copied')
  } catch (err) {
    error.value = err instanceof Error ? err.message : props.errorLabel
  }
}

function hide() {
  revealed.value = null
  error.value = ''
}
</script>

<template>
  <span class="credential-key-field">
    <code>{{ display }}</code>
    <template v-if="revealed">
      <button v-if="copyLabel" type="button" @click="copy">{{ copyLabel }}</button>
      <button v-if="hideLabel" type="button" @click="hide">{{ hideLabel }}</button>
    </template>
    <button v-else-if="canReveal" type="button" :disabled="loading" @click="reveal">
      {{ loading ? revealingLabel : revealLabel }}
    </button>
    <span v-if="error" class="error">{{ error }}</span>
  </span>
</template>

<style scoped>
.credential-key-field { display: inline-flex; align-items: center; gap: .5rem; flex-wrap: wrap; }
.credential-key-field code { max-width: 22rem; overflow: hidden; text-overflow: ellipsis; }
.credential-key-field button { font-size: .75rem; }
.error { color: #c53030; font-size: .75rem; }
</style>
