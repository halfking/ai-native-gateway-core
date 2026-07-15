<script setup lang="ts">
import PublicPageShell from './PublicPageShell.vue'

defineProps<{
  title?: string
  subtitle?: string
  kicker?: string
}>()
</script>

<template>
  <PublicPageShell
    :title="title || ''"
    :subtitle="subtitle"
    :kicker="kicker"
  >
    <template v-if="$slots['hero-extra']" #hero-extra>
      <slot name="hero-extra" />
    </template>
    <slot />
  </PublicPageShell>
</template>
