<script setup lang="ts">
import { computed } from 'vue'
import { credentialDisplayState, credentialDisplayStateLabel, type CredentialDisplayState, type CredentialStatusLike } from '../utils/credentialStatus'

const props = withDefaults(defineProps<{
  credential: CredentialStatusLike
  compact?: boolean
  labels?: Partial<Record<CredentialDisplayState, string>>
  reason?: string | null
}>(), { compact: false })

const state = computed(() => credentialDisplayState(props.credential))
const label = computed(() => props.labels?.[state.value] ?? credentialDisplayStateLabel[state.value])
</script>

<template>
  <span class="credential-status-bar" :class="`state-${state}`" :title="props.reason ?? props.credential.effective_reason ?? undefined">
    <span class="dot" aria-hidden="true" />
    <span v-if="!compact">{{ label }}</span>
  </span>
</template>

<style scoped>
.credential-status-bar { display: inline-flex; align-items: center; gap: .35rem; font-size: .8rem; font-weight: 600; }
.dot { width: .5rem; height: .5rem; border-radius: 50%; background: currentColor; }
.state-active { color: var(--success); }
.state-cooling, .state-rate_limited { color: var(--warning); }
.state-degraded, .state-quota_exhausted { color: color-mix(in srgb, var(--warning) 60%, var(--danger)); }
.state-unreachable, .state-auth_failed, .state-suspended, .state-disabled, .state-deleted { color: var(--danger); }
.state-unknown { color: var(--muted); }
</style>
