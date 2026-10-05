<script setup lang="ts">
// AppAccountSheet — 账户全屏 Sheet（UI规范 02 §5 结构：header 用户元信息 /
// body 分组（用户、外观=语言+主题）/ footer 登出）。行 48px、:active 高亮。
import { computed, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { setLocale, t, locale } from '@/i18n'
import { appBase } from '@/utils/base'
import { useDeploySeqUpdate } from '@/hyper/update/useDeploySeqUpdate'
import AppIcon from '@/components/common/AppIcon.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import AppConfirm from '@/components/common/AppConfirm.vue'

defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const auth = useAuthStore()
const theme = useThemeStore()
const {
  status: updateStatus,
  reason: updateReason,
  checkCount: updateChecks,
  lastCheckedAt: updateLastCheckedAt,
  localSeq: updateLocalSeq,
  checkNow: runUpdateCheck,
} = useDeploySeqUpdate()
const confirmLogout = ref(false)

const roleLabel = computed(() => {
  if (auth.role === 'super_admin') return t('account.superAdmin')
  if (auth.role === 'tenant_admin') return t('account.tenantAdmin')
  return t('account.userRole')
})

// ── 版本与更新（UI规范 18 §4 手动检查面）────────────────────────
// 「不可判定」必须带原因展示，不得折叠成绿色徽标（18 §5 硬规则）。
const updateStateLabel = computed(() => {
  if (updateStatus.value === 'checking') return t('update.stateChecking')
  if (updateStatus.value === 'latest') return t('update.stateLatest')
  if (updateStatus.value === 'available') return t('update.stateAvailable')
  if (updateStatus.value === 'indeterminate') return t('update.stateIndeterminate')
  return '—'
})

const updateSeqLabel = computed(() => updateLocalSeq.value?.seq ?? t('update.sourceUnknown'))

const updateSourceLabel = computed(() => {
  const src = updateLocalSeq.value?.src
  if (src === 'build-seq') return t('update.sourceBuildSeq')
  if (src === 'git-sha') return t('update.sourceGitSha')
  return t('update.sourceUnknown')
})

const updateReasonLabel = computed(() => {
  if (updateStatus.value !== 'indeterminate') return ''
  if (updateReason.value === 'no-local') return t('update.reasonNoLocal')
  if (updateReason.value === 'no-remote') return t('update.reasonNoRemote')
  if (updateReason.value === 'network') return t('update.reasonNetwork')
  return ''
})

const lastCheckLabel = computed(() =>
  updateLastCheckedAt.value === null
    ? t('update.never')
    : new Date(updateLastCheckedAt.value).toLocaleTimeString(),
)

async function doLogout(): Promise<void> {
  confirmLogout.value = false
  emit('update:modelValue', false)
  await auth.logout()
  window.location.assign(`${appBase()}login`)
}

function pickLocale(): void {
  setLocale(locale.value === 'zh-CN' ? 'en-US' : 'zh-CN')
}
</script>

<template>
  <AppSheet
    :model-value="modelValue"
    presentation="sheet"
    :title="t('account.title')"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
  >
    <div class="account">
      <div class="account__group">
        <div class="account__group-label">{{ t('account.user') }}</div>
        <div class="account__user">
          <div class="account__name">{{ auth.userInfo?.display_name || auth.userInfo?.username || '—' }}</div>
          <div class="account__meta">{{ auth.userInfo?.email }}</div>
          <span class="badge badge--info">{{ roleLabel }}</span>
        </div>
      </div>

      <div class="account__group">
        <div class="account__group-label">{{ t('account.appearance') }}</div>

        <button type="button" class="account__row" @click="theme.toggleTheme()">
          <AppIcon :name="theme.theme === 'dark' ? 'moon' : 'sun'" :size="20" />
          <span class="account__row-label">{{ t('account.theme') }}</span>
          <span class="account__row-value">{{ theme.theme === 'dark' ? t('account.themeDark') : t('account.themeLight') }}</span>
          <AppIcon name="chevron" :size="16" class="account__chev" />
        </button>

        <button type="button" class="account__row" @click="pickLocale">
          <AppIcon name="globe" :size="20" />
          <span class="account__row-label">{{ t('account.language') }}</span>
          <span class="account__row-value">{{ locale === 'zh-CN' ? '简体中文' : 'English' }}</span>
          <AppIcon name="chevron" :size="16" class="account__chev" />
        </button>
      </div>

      <!-- 版本与更新（UI规范 18 §4）：当前序号、来源、已检查次数 + 手动检查 -->
      <div class="account__group">
        <div class="account__group-label">{{ t('update.section') }}</div>

        <div class="account__row account__row--static">
          <span class="account__row-label">{{ t('update.currentSeq') }}</span>
          <span class="account__row-value account__seq">{{ updateSeqLabel }}</span>
        </div>

        <div class="account__row account__row--static">
          <span class="account__row-label">{{ t('update.seqSource') }}</span>
          <span class="account__row-value">{{ updateSourceLabel }}</span>
        </div>

        <div class="account__row account__row--static">
          <span class="account__row-label">{{ t('update.state') }}</span>
          <span
            class="account__row-value"
            :class="{
              'account__seq--ok': updateStatus === 'latest',
              'account__seq--warn': updateStatus === 'available',
            }"
          >
            {{ updateStateLabel }}
          </span>
        </div>
        <p v-if="updateReasonLabel" class="account__reason">{{ updateReasonLabel }}</p>

        <div class="account__row account__row--static">
          <span class="account__row-label">{{ t('update.checks') }}</span>
          <span class="account__row-value">{{ updateChecks }}</span>
        </div>

        <div class="account__row account__row--static">
          <span class="account__row-label">{{ t('update.lastCheck') }}</span>
          <span class="account__row-value">{{ lastCheckLabel }}</span>
        </div>

        <button type="button" class="account__row" @click="runUpdateCheck()">
          <AppIcon name="globe" :size="20" />
          <span class="account__row-label">{{ t('update.checkNow') }}</span>
          <AppIcon name="chevron" :size="16" class="account__chev" />
        </button>
      </div>
    </div>

    <button type="button" class="btn btn--danger btn--block account__logout" @click="confirmLogout = true">
      {{ t('common.logout') }}
    </button>

    <AppConfirm
      v-model="confirmLogout"
      :title="t('account.logoutConfirm')"
      :confirm-label="t('common.logout')"
      :cancel-label="t('common.cancel')"
      danger
      @confirm="doLogout"
    />
  </AppSheet>
</template>

<style scoped>
.account {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.account__group-label {
  font-size: 0.72rem;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--app-text-muted);
  margin-bottom: var(--app-space-1);
}

.account__user {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--app-space-1);
  padding: var(--app-space-2) 0;
}

.account__name {
  font-size: 1rem;
  font-weight: 600;
}

.account__meta {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.account__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-3);
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: none;
  background: transparent;
  border-radius: var(--app-radius);
  color: var(--app-text);
  font-size: 0.875rem;
  cursor: pointer;
  text-align: left;
}

.account__row:active {
  background: var(--app-primary-soft);
}

/* 静态信息行：不可点，仅展示（48px 高度与可点行一致，视觉节奏不变） */
.account__row--static {
  cursor: default;
}

.account__row--static:active {
  background: transparent;
}

.account__seq {
  font-family: var(--app-font-mono, monospace);
  font-size: 0.75rem;
  word-break: break-all;
}

.account__seq--ok {
  color: var(--app-success);
}

.account__seq--warn {
  color: var(--app-info);
  font-weight: 600;
}

.account__reason {
  margin: 0;
  padding: 0 var(--app-space-2);
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.account__row-label {
  flex: 1;
}

.account__row-value {
  color: var(--app-text-secondary);
  font-size: 0.8125rem;
}

.account__chev {
  color: var(--app-text-muted);
}

.account__logout {
  margin-top: var(--app-space-5);
}
</style>
