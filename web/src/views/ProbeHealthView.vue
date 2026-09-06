<script setup lang="ts">
import ProbeHealthPanel from './probe/ProbeHealthPanel.vue'

// /probe-health now redirects to /routing-v2/credentials?tab=probe-health
// (probe-health integration, 2026-09-07). This wrapper keeps the route
// definition valid for any stale bookmark that bypasses the redirect.
</script>

<template>
  <ProbeHealthPanel />
</template>
