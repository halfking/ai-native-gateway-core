<script setup lang="ts">
// AppAccountSheet — 账户全屏 Sheet（UI规范 02 §5 结构：header 用户元信息 /
// body 分组（用户、外观=语言+主题）/ footer 登出）。行 48px、:active 高亮。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import QRCode from 'qrcode'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { LOCALE_OPTIONS, getLocale, setLocale, t, locale, type Locale } from '@/i18n'
import { appBase } from '@/utils/base'
import { useDeploySeqUpdate } from '@/hyper/update/useDeploySeqUpdate'
import AppIcon from '@/components/common/AppIcon.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import AppConfirm from '@/components/common/AppConfirm.vue'
import { fetchMobileNodeDescriptor } from '@/api/mobileNode'
import { GATEWAY_SITES, currentSite, isCurrentSite, siteLoginUrl, type GatewaySite } from '@/api/gatewaySites'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

/** 当前所在站点；不在预置目录里（自建环境）时为 null ⇒ 不渲染该组。 */
const site = currentSite()
/** 除当前站点外的可切换站点。当前站点自己列在上面作为「你在哪」。 */
const otherSites = computed(() => GATEWAY_SITES.filter(option => !isCurrentSite(option)))
const switchingSite = ref<string | null>(null)

/**
 * 切到另一站点：整页跳到该站点的登录页。
 *
 * ⚠️ 为什么不先 logout：当前站点的 `llmgw_session` 是 cookie，跨 origin 不共享，
 *   跳走后新站点读不到它；反过来调用 logout 会顺手清掉主题/语言/节点配置
 *   这些**跨站点也该保留**的东西。换站点 ≠ 注销。
 */
function switchSite(target: GatewaySite): void {
  if (switchingSite.value) return
  switchingSite.value = target.id
  window.location.replace(siteLoginUrl(target))
}

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
const nodeQrCanvas = ref<HTMLCanvasElement | null>(null)
const nodeQrLoading = ref(false)
const nodeQrError = ref('')
const nodeQrExpiresAt = ref('')
const nodeFingerprint = ref('')
let qrGeneration = 0
let qrAborter: AbortController | null = null
let qrExpiryTimer: ReturnType<typeof setTimeout> | undefined
function clearQr(): void {
  ++qrGeneration
  qrAborter?.abort()
  clearTimeout(qrExpiryTimer)
  nodeQrExpiresAt.value = ''
  nodeFingerprint.value = ''
  nodeQrLoading.value = false
}
watch(() => [props.modelValue, auth.role], () => { clearQr(); nodeQrError.value = '' })
onBeforeUnmount(clearQr)

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
    : new Date(updateLastCheckedAt.value).toLocaleTimeString(getLocale()),
)

async function doLogout(): Promise<void> {
  confirmLogout.value = false
  emit('update:modelValue', false)
  await auth.logout()
  window.location.assign(`${appBase()}login`)
}

function pickLocale(event: Event): void {
  setLocale((event.target as HTMLSelectElement).value as Locale)
}

async function showNodeQr(): Promise<void> {
  clearQr()
  const generation = qrGeneration
  qrAborter = new AbortController()
  const signal = qrAborter.signal
  nodeQrLoading.value = true
  nodeQrError.value = ''
  try {
    const response = await fetchMobileNodeDescriptor({ signal })
    if (generation !== qrGeneration) return
    const remaining = Date.parse(response.expires_at) - Date.now()
    if (!Number.isFinite(remaining) || remaining <= 0) throw new Error('Expired QR')
    const rendered = document.createElement('canvas')
    await QRCode.toCanvas(rendered, response.payload, {
      width: 260, margin: 2, errorCorrectionLevel: 'Q',
      color: { dark: '#07112c', light: '#ffffff' },
    })
    if (generation !== qrGeneration) return
    nodeQrExpiresAt.value = response.expires_at
    nodeFingerprint.value = response.signing_key_fingerprint ?? ''
    await nextTick()
    if (generation !== qrGeneration) return
    const canvas = nodeQrCanvas.value
    if (!canvas) throw new Error('QR canvas unavailable')
    canvas.width = rendered.width
    canvas.height = rendered.height
    canvas.getContext('2d')?.drawImage(rendered, 0, 0)
    qrExpiryTimer = setTimeout(() => { clearQr(); nodeQrError.value = t('account.nodeQrExpired') }, Math.max(0, Date.parse(response.expires_at) - Date.now()))
  } catch {
    if (generation !== qrGeneration) return
    clearQr()
    nodeQrError.value = t('account.nodeQrUnavailable')
  } finally {
    if (generation === qrGeneration) nodeQrLoading.value = false
  }
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

      <!-- 站点切换（2026-10-13）：与登录页同一份目录（api/gatewaySites.ts）。
           切换 = 整页跳到目标站点的 /m/login，需重新登录该站点的账号。 -->
      <div v-if="site" class="account__group">
        <div class="account__group-label">{{ t('account.gatewaySite') }}</div>
        <div class="account__current-site">
          <span class="account__site-env" :data-env="site.env">{{ t(`login.siteEnv.${site.env}`) }}</span>
          <span class="account__row-label">{{ site.name }}</span>
          <code class="account__site-origin">{{ site.origin }}</code>
        </div>
        <button
          v-for="option in otherSites"
          :key="option.id"
          type="button"
          class="account__row"
          :data-site-id="option.id"
          :disabled="switchingSite !== null"
          @click="switchSite(option)"
        >
          <span class="account__site-env" :data-env="option.env">{{ t(`login.siteEnv.${option.env}`) }}</span>
          <span class="account__row-label">{{ option.name }}</span>
          <span class="account__row-value">{{ switchingSite === option.id ? t('common.loading') : '' }}</span>
          <AppIcon name="chevron" :size="16" class="account__chev" />
        </button>
      </div>

      <div class="account__group">
        <div class="account__group-label">{{ t('account.appearance') }}</div>

        <button type="button" class="account__row" @click="theme.toggleTheme()">
          <AppIcon :name="theme.theme === 'dark' ? 'moon' : 'sun'" :size="20" />
          <span class="account__row-label">{{ t('account.theme') }}</span>
          <span class="account__row-value">{{ theme.theme === 'dark' ? t('account.themeDark') : t('account.themeLight') }}</span>
          <AppIcon name="chevron" :size="16" class="account__chev" />
        </button>

        <label class="account__row account__language">
          <AppIcon name="globe" :size="20" />
          <span class="account__row-label">{{ t('account.language') }}</span>
          <select class="account__language-select" :value="locale" :aria-label="t('account.language')" @change="pickLocale">
            <option v-for="option in LOCALE_OPTIONS" :key="option.code" :value="option.code">{{ option.label }}</option>
          </select>
          <AppIcon name="chevron" :size="16" class="account__chev" />
        </label>
      </div>

      <div class="account__group">
        <div class="account__group-label">{{ t('account.gatewayNode') }}</div>
        <a href="llmgw://nodes" class="account__row">{{ t('account.openNodeManager') }}</a>
        <button v-if="auth.role === 'super_admin'" type="button" class="btn btn--secondary btn--block" :disabled="nodeQrLoading" @click="showNodeQr">
          {{ nodeQrLoading ? t('common.loading') : t('account.showNodeQr') }}
        </button>
        <div v-if="nodeQrExpiresAt" class="account__node-qr">
          <canvas ref="nodeQrCanvas" :aria-label="t('account.nodeQrAlt')" />
          <p>{{ t('account.nodeQrExpires', { time: new Date(nodeQrExpiresAt).toLocaleTimeString(getLocale()) }) }}</p>
<template v-if="nodeFingerprint"><p>{{ t('account.nodeFingerprint') }}</p><code>{{ nodeFingerprint }}</code><p>{{ t('account.nodeFingerprintHint') }}</p></template>
        </div>
        <p v-if="nodeQrError" class="account__reason" role="alert">{{ nodeQrError }}</p>
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

    <button id="account-logout" type="button" class="btn btn--danger btn--block account__logout" @click="confirmLogout = true">
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

/* 站点切换（2026-10-13） */
.account__current-site {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  min-height: 48px;
  padding: 6px 0;
  flex-wrap: wrap;
}

.account__site-origin {
  flex: 1 0 100%;
  color: var(--app-text-muted);
  font-size: 11px;
  overflow-wrap: anywhere;
}

.account__site-env {
  flex: 0 0 auto;
  padding: 2px 7px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  color: var(--app-text-secondary);
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
}

/* 生产站点用 danger 醒目标记，避免在生产上误操作。 */
.account__site-env[data-env='production'] {
  border-color: var(--app-danger);
  color: var(--app-danger);
}

.account__language-select {
  max-width: 45%;
  min-width: 8rem;
  padding: 0.4rem 0.5rem;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font: inherit;
  text-align: end;
}

.account__node-qr {
  display: flex;
  align-items: center;
  flex-direction: column;
  gap: var(--app-space-2);
  padding: var(--app-space-3);
  margin-top: var(--app-space-3);
  border: 1px solid var(--app-border-subtle);
  border-radius: var(--app-radius);
  background: var(--app-surface-muted);
  text-align: center;
}

.account__node-qr canvas {
  width: min(260px, 72vw);
  height: auto;
  border-radius: var(--app-radius-sm);
  background: #fff;
}

.account__node-qr p { margin: 0; color: var(--app-text-secondary); font-size: 0.75rem; }

.account__chev {
  color: var(--app-text-muted);
}

.account__logout {
  margin-top: var(--app-space-5);
}
</style>
