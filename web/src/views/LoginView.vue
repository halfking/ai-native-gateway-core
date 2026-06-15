<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useLoginModal } from '../composables/useLoginModal'

const router = useRouter()
const { openLogin } = useLoginModal()

onMounted(async () => {
  openLogin()
  await router.replace({ path: '/', query: {} })
})
</script>

<template>
  <div />
</template>
