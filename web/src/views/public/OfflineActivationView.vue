<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import PublicPortalLayout from '../../components/PublicPortalLayout.vue'
import {
  submitOfflineRequest,
  getOfflineStatus,
  getOfflineResponse,
} from '../../api/public'

const { t } = useI18n()
const router = useRouter()

const content = ref('')
const requestId = ref('')
const status = ref('')
const rejectReason = ref('')
const responseJson = ref('')
const loading = ref(false)
const polling = ref(false)

async function handleSubmit() {
  if (!content.value.trim()) return
  loading.value = true
  try {
    const res = await submitOfflineRequest(content.value.trim())
    requestId.value = res.request_id
    status.value = res.status
    ElMessage.success(t('public.offline.submitOk'))
  } catch {
    ElMessage.error(t('public.offline.submitFailed'))
  } finally {
    loading.value = false
  }
}

async function refreshStatus() {
  if (!requestId.value) return
  polling.value = true
  try {
    const res = await getOfflineStatus(requestId.value)
    status.value = res.status
    rejectReason.value = res.reject_reason || ''
  } catch {
    ElMessage.error(t('public.offline.notFound'))
  } finally {
    polling.value = false
  }
}

async function fetchResponse() {
  if (!requestId.value) return
  loading.value = true
  try {
    const res = await getOfflineResponse(requestId.value)
    if (res.signed_license) {
      responseJson.value = JSON.stringify(res, null, 2)
      status.value = 'approved'
      ElMessage.success(t('public.offline.approved'))
    } else {
      status.value = res.status || status.value
      ElMessage.info(res.message || t('public.offline.pending'))
    }
  } catch {
    ElMessage.error(t('public.offline.notFound'))
  } finally {
    loading.value = false
  }
}

function handleFileUpload(file: File) {
  const reader = new FileReader()
  reader.onload = () => {
    content.value = String(reader.result || '').trim()
  }
  reader.readAsText(file)
  return false
}

async function copyResponse() {
  if (!responseJson.value) return
  try {
    await navigator.clipboard.writeText(responseJson.value)
    ElMessage.success(t('common.copied', '已复制'))
  } catch {
    ElMessage.warning('copy failed')
  }
}
</script>

<template>
  <PublicPortalLayout>
    <div class="off-page">
      <h1>{{ t('public.offline.title') }}</h1>
      <p class="off-sub">{{ t('public.offline.subtitle') }}</p>

      <el-steps :active="requestId ? 2 : 1" finish-status="success" simple class="off-steps">
        <el-step :title="t('public.offline.step1')" />
        <el-step :title="t('public.offline.step2')" />
        <el-step :title="t('public.offline.step3')" />
      </el-steps>

      <p class="off-hint">{{ t('public.offline.step1Hint') }}</p>

      <el-form label-position="top">
        <el-form-item :label="t('public.offline.uploadLabel')">
          <el-input
            v-model="content"
            type="textarea"
            :rows="8"
            :placeholder="t('public.offline.uploadPlaceholder')"
          />
          <el-upload
            :show-file-list="false"
            :before-upload="handleFileUpload"
            accept=".req,.txt,.json"
            class="off-upload"
          >
            <el-button link type="primary">{{ t('public.offline.uploadFile') }}</el-button>
          </el-upload>
        </el-form-item>
        <el-button type="primary" :loading="loading" @click="handleSubmit">
          {{ t('public.offline.submit') }}
        </el-button>
      </el-form>

      <el-card v-if="requestId" shadow="never" class="off-status">
        <p><strong>{{ t('public.offline.requestId') }}:</strong> <code>{{ requestId }}</code></p>
        <p><strong>{{ t('public.offline.status') }}:</strong> {{ status }}</p>
        <p v-if="rejectReason" class="off-reject">{{ rejectReason }}</p>
        <div class="off-actions">
          <el-button :loading="polling" @click="refreshStatus">{{ t('public.offline.poll') }}</el-button>
          <el-button type="primary" :loading="loading" @click="fetchResponse">
            {{ t('public.offline.downloadResp') }}
          </el-button>
          <el-button link @click="router.push('/activate')">{{ t('public.offline.goActivate') }}</el-button>
        </div>
      </el-card>

      <el-card v-if="responseJson" shadow="never" class="off-resp">
        <pre>{{ responseJson }}</pre>
        <el-button @click="copyResponse">{{ t('public.offline.copyResponse') }}</el-button>
      </el-card>
    </div>
  </PublicPortalLayout>
</template>

<style scoped>
.off-page h1 { margin: 0 0 0.5rem; }
.off-sub, .off-hint { color: #64748b; }
.off-steps { margin: 1.5rem 0; }
.off-upload { margin-top: 0.5rem; }
.off-status, .off-resp { margin-top: 1.5rem; }
.off-reject { color: var(--el-color-danger); }
.off-actions { display: flex; gap: 0.5rem; flex-wrap: wrap; margin-top: 0.75rem; }
.off-resp pre {
  max-height: 240px;
  overflow: auto;
  background: #f1f5f9;
  padding: 1rem;
  border-radius: 6px;
  font-size: 0.8rem;
}
</style>
