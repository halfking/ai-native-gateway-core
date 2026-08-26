<script setup lang="ts">
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { usePublicCatalog } from '../composables/usePublicCatalog'

const { t } = useI18n()
const { catalog, ensureCatalog } = usePublicCatalog()

onMounted(ensureCatalog)

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(t('common.copied', '已复制'))
  } catch {
    ElMessage.warning(text)
  }
}
</script>

<template>
  <el-card v-if="catalog?.git_repo_url || catalog?.contact_email" shadow="never" class="pub-card pub-git-box pub-contact-box">
    <h3>{{ t('public.contact.title') }}</h3>
    <p>{{ t('public.contact.desc') }}</p>
    <div v-if="catalog?.git_repo_url" class="pub-link-row">
      <span class="pub-contact-label">{{ t('public.contact.repo') }}</span>
      <a :href="catalog.git_repo_url" target="_blank" rel="noopener">{{ catalog.git_repo_url }}</a>
      <el-button size="small" @click="copyText(catalog.git_repo_url!)">
        {{ t('public.download.copyRepo') }}
      </el-button>
    </div>
    <div v-if="catalog?.contact_email" class="pub-link-row">
      <span class="pub-contact-label">{{ t('public.contact.email') }}</span>
      <a :href="`mailto:${catalog.contact_email}`">{{ catalog.contact_email }}</a>
      <el-button size="small" @click="copyText(catalog.contact_email!)">
        {{ t('public.contact.copyEmail') }}
      </el-button>
    </div>
  </el-card>
</template>

<style scoped>
.pub-contact-box h3 {
  margin: 0 0 8px;
  font-size: 1rem;
}

.pub-contact-box p {
  margin: 0;
  color: #94a3b8;
  font-size: 0.875rem;
}

.pub-contact-label {
  font-size: 0.8rem;
  color: #64748b;
  min-width: 4.5rem;
}
</style>
