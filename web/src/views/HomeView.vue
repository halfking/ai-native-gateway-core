<script setup lang="ts">
import { computed } from 'vue'
import { store } from '../store'
import DashboardView from './DashboardView.vue'
import LandingView from './LandingView.vue'

const isLoggedIn = computed(() => !!(store.jwtToken || store.apiKey))
</script>

<template>
  <DashboardView v-if="isLoggedIn" />
  <LandingView v-else />
</template>
