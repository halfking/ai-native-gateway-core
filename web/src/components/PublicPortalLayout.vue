<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

const { t } = useI18n()
const router = useRouter()
</script>

<template>
  <div class="pub-layout">
    <main class="pub-main">
      <slot />
    </main>
    <footer class="pub-footer">
      <span>{{ t('landing.brandTitle') }} · {{ t('landing.kicker') }}</span>
      <div class="pub-footer__links">
        <el-button link type="primary" @click="router.push('/activate')">
          {{ t('public.download.activateLink') }}
        </el-button>
        <el-button link type="primary" @click="router.push('/support')">
          {{ t('public.layout.support') }}
        </el-button>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.pub-layout {
  min-height: calc(100vh - 69px);
  display: flex;
  flex-direction: column;
  background: linear-gradient(180deg, rgba(15, 23, 42, 0.35) 0%, transparent 280px);
}

.pub-main {
  flex: 1;
  max-width: 960px;
  width: 100%;
  margin: 0 auto;
  padding: 1.5rem 1.5rem 2rem;
}

.pub-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 1rem 2rem;
  border-top: 1px solid var(--border, #2a2d3a);
  background: var(--panel, #1a1d27);
  color: var(--text-secondary, #8b949e);
  font-size: 0.875rem;
}

.pub-footer__links {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
</style>
