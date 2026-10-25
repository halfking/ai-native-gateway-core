<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '../utils/datetime'
import { localeRef } from '../i18n'
import {
  listKeyApplications,
  approveKeyApplication,
  rejectKeyApplication,
  revealKey,
  type KeyApplication,
  type ApproveApplicationResponse,
} from '../api'

const { t } = useI18n()

const applications = ref<KeyApplication[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const filterStatus = ref<string>('pending')

// Review modal
const reviewing = ref<KeyApplication | null>(null)
const reviewAction = ref<'approve' | 'reject' | null>(null)
const reviewNotes = ref('')
const reviewSaving = ref(false)
const reviewError = ref<string | null>(null)

// Approved key reveal
const revealedKeys = ref<Record<string, string>>({})
const revealLoading = ref<string | null>(null)

const filtered = computed(() => {
  if (!filterStatus.value) return applications.value
  return applications.value.filter((a) => a.status === filterStatus.value)
})

const pendingCount = computed(() => applications.value.filter((a) => a.status === 'pending').length)

async function load() {
  loading.value = true
  error.value = null
  try {
    applications.value = await listKeyApplications()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function openReview(app: KeyApplication, action: 'approve' | 'reject') {
  reviewing.value = app
  reviewAction.value = action
  reviewNotes.value = ''
  reviewError.value = null
}

function closeReview() {
  reviewing.value = null
  reviewAction.value = null
  reviewNotes.value = ''
  reviewError.value = null
}

async function submitReview() {
  if (!reviewing.value || !reviewAction.value) return
  reviewSaving.value = true
  reviewError.value = null
  try {
    if (reviewAction.value === 'approve') {
      const result: ApproveApplicationResponse = await approveKeyApplication(
        reviewing.value.id,
        reviewNotes.value.trim() || undefined,
      )
      // Update local state
      const idx = applications.value.findIndex((a) => a.id === reviewing.value!.id)
      if (idx >= 0) {
        applications.value[idx].status = 'approved'
        applications.value[idx].issued_key_id = result.key_id
        applications.value[idx].admin_notes = reviewNotes.value.trim() || null
      }
    } else {
      await rejectKeyApplication(
        reviewing.value.id,
        reviewNotes.value.trim() || undefined,
      )
      const idx = applications.value.findIndex((a) => a.id === reviewing.value!.id)
      if (idx >= 0) {
        applications.value[idx].status = 'rejected'
        applications.value[idx].admin_notes = reviewNotes.value.trim() || null
      }
    }
    closeReview()
  } catch (e: unknown) {
    reviewError.value = e instanceof Error ? e.message : String(e)
  } finally {
    reviewSaving.value = false
  }
}

async function handleReveal(app: KeyApplication) {
  if (!app.issued_key_id) return
  revealLoading.value = app.id
  try {
    const result = await revealKey(app.issued_key_id)
    revealedKeys.value[app.id] = result.api_key
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    revealLoading.value = null
  }
}

function fmtTs(ts: string | null) {
  if (!ts) return '—'
  return formatDateTime(ts, { locale: localeRef.value, options: { hour12: false } })
}

function statusBadge(status: string) {
  if (status === 'pending') return 'badge-yellow'
  if (status === 'approved') return 'badge-green'
  if (status === 'rejected') return 'badge-red'
  return 'badge-gray'
}

function statusLabel(status: string) {
  if (status === 'pending') return t('keys.applications.status.pending')
  if (status === 'approved') return t('keys.applications.status.approved')
  if (status === 'rejected') return t('keys.applications.status.rejected')
  if (status === 'expired') return t('keys.applications.status.expired')
  return status
}

onMounted(load)
</script>

<template>
  <div>
    <div
      class="page-header"
      style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px"
    >
      <h2 style="margin:0">
        {{ t('keys.applications.title') }}
        <span v-if="pendingCount" class="badge badge-yellow" style="margin-left:8px;font-size:13px">
          {{ pendingCount }}{{ t('keys.applications.pendingBadge') }}
        </span>
      </h2>
      <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">{{ t('keys.applications.refresh') }}</button>
    </div>

    <p v-if="error" style="color:var(--danger);margin-bottom:12px">{{ error }}</p>

    <!-- Filter tabs -->
    <div style="display:flex;gap:8px;margin-bottom:16px">
      <button
        v-for="tab in [
          { value: 'pending', label: t('keys.applications.tab.pending') },
          { value: 'approved', label: t('keys.applications.tab.approved') },
          { value: 'rejected', label: t('keys.applications.tab.rejected') },
          { value: '', label: t('keys.applications.tab.all') },
        ]"
        :key="tab.value"
        class="btn btn-sm"
        :class="filterStatus === tab.value ? 'btn-primary' : 'btn-ghost'"
        @click="filterStatus = tab.value"
      >
        {{ tab.label }}
      </button>
    </div>

    <div class="card" style="overflow-x:auto">
      <table class="data-table" style="width:100%;font-size:13px">
        <thead>
          <tr>
            <th>{{ t('keys.applications.table.appliedAt') }}</th>
            <th>{{ t('keys.applications.table.contact') }}</th>
            <th>{{ t('keys.applications.table.purpose') }}</th>
            <th>{{ t('keys.applications.table.sourceIp') }}</th>
            <th>{{ t('keys.common.statusLabel') }}</th>
            <th>{{ t('keys.applications.table.reviewRemark') }}</th>
            <th>{{ t('keys.applications.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" style="text-align:center;padding:24px">{{ t('keys.applications.loading') }}</td>
          </tr>
          <tr v-else-if="!filtered.length">
            <td colspan="7" style="text-align:center;padding:24px;color:var(--muted)">{{ t('keys.applications.noRecord') }}</td>
          </tr>
          <tr v-for="app in filtered" :key="app.id">
            <td style="white-space:nowrap">{{ fmtTs(app.created_at) }}</td>
            <td>
              <div style="font-weight:500">{{ app.contact }}</div>
              <div v-if="app.expires_at && app.status === 'pending'" style="font-size:11px;color:var(--muted)">
                {{ t('keys.applications.expiredCell', { ts: fmtTs(app.expires_at) }) }}
              </div>
            </td>
            <td style="max-width:240px;word-break:break-word">{{ app.purpose || '—' }}</td>
            <td style="font-family:monospace;font-size:12px">{{ app.client_ip }}</td>
            <td>
              <span class="badge" :class="statusBadge(app.status)">{{ statusLabel(app.status) }}</span>
            </td>
            <td style="max-width:200px;word-break:break-word;color:var(--muted);font-size:12px">
              {{ app.admin_notes || '—' }}
            </td>
            <td style="white-space:nowrap">
              <!-- Pending: approve / reject -->
              <template v-if="app.status === 'pending'">
                <button
                  class="btn btn-sm btn-primary"
                  style="margin-right:6px"
                  @click="openReview(app, 'approve')"
                >{{ t('keys.applications.action.approve') }}</button>
                <button
                  class="btn btn-sm btn-danger"
                  @click="openReview(app, 'reject')"
                >{{ t('keys.applications.action.reject') }}</button>
              </template>
              <!-- Approved: show key prefix + reveal button -->
              <template v-else-if="app.status === 'approved' && app.issued_key_id">
                <span
                  v-if="revealedKeys[app.id]"
                  style="font-family:monospace;font-size:11px;background:var(--surface2);padding:2px 6px;border-radius:4px;user-select:all"
                >{{ revealedKeys[app.id] }}</span>
                <button
                  v-else
                  class="btn btn-sm btn-ghost"
                  :disabled="revealLoading === app.id"
                  @click="handleReveal(app)"
                >
                  {{ revealLoading === app.id ? t('keys.applications.action.revealing') : t('keys.applications.action.reveal') }}
                </button>
              </template>
              <span v-else style="color:var(--muted)">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Review modal -->
    <div
      v-if="reviewing"
      class="modal-overlay"
      @click.self="closeReview"
    >
      <div class="card" style="width:480px;padding:24px" @click.stop>
        <h3 style="margin:0 0 16px">
          {{ reviewAction === 'approve' ? t('keys.applications.modal.approveTitle') : t('keys.applications.modal.rejectTitle') }}
        </h3>
        <div style="margin-bottom:12px">
          <div style="font-size:13px;color:var(--muted)">{{ t('keys.applications.modal.contact') }}</div>
          <div style="font-weight:500">{{ reviewing.contact }}</div>
        </div>
        <div v-if="reviewing.purpose" style="margin-bottom:12px">
          <div style="font-size:13px;color:var(--muted)">{{ t('keys.applications.modal.purpose') }}</div>
          <div>{{ reviewing.purpose }}</div>
        </div>
        <div style="margin-bottom:16px">
          <label style="font-size:13px;display:block;margin-bottom:4px">{{ t('keys.applications.modal.remark') }}</label>
          <textarea
            v-model="reviewNotes"
            rows="3"
            :placeholder="t('keys.applications.modal.remarkPlaceholder')"
            style="width:100%;box-sizing:border-box"
          />
        </div>
        <p v-if="reviewError" style="color:var(--danger);margin-bottom:12px">{{ reviewError }}</p>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost btn-sm" @click="closeReview">{{ t('keys.applications.modal.cancel') }}</button>
          <button
            class="btn btn-sm"
            :class="reviewAction === 'approve' ? 'btn-primary' : 'btn-danger'"
            :disabled="reviewSaving"
            @click="submitReview"
          >
            {{ reviewSaving ? t('keys.applications.modal.processing') : (reviewAction === 'approve' ? t('keys.applications.modal.confirmApprove') : t('keys.applications.modal.confirmReject')) }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
