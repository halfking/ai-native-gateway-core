<script setup lang="ts">
import PublicPageShell from './PublicPageShell.vue'

defineProps<{
  title?: string
  subtitle?: string
}>()
</script>

<template>
  <PublicPageShell
    :title="title || ''"
    :subtitle="subtitle"
  >
    <template v-if="$slots['hero-extra']" #hero-extra>
      <slot name="hero-extra" />
    </template>
    <slot />
  </PublicPageShell>
</template>
