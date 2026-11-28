<script setup lang="ts">
// SelfUpdateView.vue — 自更新管理页（自动检索最新版本 → 提醒 → 点击安装升级）。
//
// 后端契约见 api/selfUpdate.ts（/api/admin/releases 族，super admin）。
//
// ★★★ 本页的第一纪律：**检查失败 ≠ 已是最新**
//
//   本仓有过血案（web/src/views/lifecycle/upgradeLatestVsUnknown.test.ts）：
//   旧实现在检查失败时把「已是最新」和「当前已是最新版本」显示出来，
//   并且把「最新版本」那一栏回退成当前版本号 —— 两栏同数看着像「确认过是最新的」，
//   用户据此跳过了补丁。
//
//   ⇒ 本页把检索结论收敛成**四态**（见 `state` 计算属性）：
//       notConfigured —— 本节点没配远端源 ⇒ 结论不可得
//       unknown      —— 没查过 / 查失败 / 结论不完整 ⇒ 结论不可得（并给出原因）
//       upToDate     —— **查成了**，且远端确认没有更新
//       available    —— 查成了，且远端确认有更新
//     只有后两态才允许出现「已是最新」这类结论；
//     前两态的「最新版本」栏一律显示占位符，**绝不回退成当前版本号**。
//
// ★ 第二纪律：**装机会替换二进制并重启服务** ⇒ 一律二次确认（ElMessageBox.confirm），
//   确认文案必须说清后果（下载 / 替换 / 重启 / 期间请求中断）。
//
// ★ 第三纪律：**面向用户的文案一律走 i18n**（t('selfUpdateView.*')），
//   本文件里不写中文（仓库有「硬编码中文只许降不许升」的棘轮门）。

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ElButton, ElInput, ElTag } from 'element-plus'
import PageHeader from '../../components/ui/PageHeader.vue'
import { formatDateTime } from '../../utils/datetime'
import {
  getCheckStatus,
  getInstallStatus,
  getNodeUpgradeStatus,
  getNotifications,
  installVersion,
  runCheck,
  sourceBuildVersion,
  type CheckResult,
  type CheckStatusResponse,
  type InstallJob,
  type InstallMode,
  type ReleaseNotification,
} from '../../api/selfUpdate'

const { t, locale } = useI18n()

/**
 * 空值占位符。
 *
 * ★ 为什么放在代码里而不是语言包：8 个语种共用同一个 '—' 会让
 *   「值与 en-US 逐字相同」这条 i18n 质量棘轮计数上涨 ⇒ 棘轮只许降不许升。
 *   仓内既有事实源 utils/datetime.ts 的空值哨兵也是 '—'（同口径）。
 */
const UNKNOWN_VALUE = '—'

/** 安装任务轮询间隔（毫秒）。 */
const POLL_INTERVAL_MS = 2000

// ── 状态 ────────────────────────────────────────────────────────────────────
const loading = ref(true)
const checking = ref(false)
const checkStatus = ref<CheckStatusResponse | null>(null)
const loadError = ref('')
const job = ref<InstallJob | null>(null)
const notifications = ref<ReleaseNotification[]>([])
const sourceRef = ref('')
const sourceBuilding = ref(false)

/**
 * 本节点当前运行的版本（公开端点兜底）。
 *
 * ★ admin 族的 check-status 在「未配置远端版本源」时只回 {configured:false}，
 *   不带任何版本号 ⇒ 没有这个兜底，状态卡的「当前版本 / 构建序号」会是占位符
 *   （而节点明明在跑某个版本）。
 */
const nodeVersion = ref<{ version: string; build_seq?: number } | null>(null)

/** 最近一次「立即检查」的失败原因（空串 = 没有失败）。 */
const checkError = ref('')

// ── 检索结论的四态 ──────────────────────────────────────────────────────────
type UpdateState =
  | { kind: 'notConfigured' }
  | { kind: 'unknown'; reason: string }
  | { kind: 'upToDate' }
  | { kind: 'available' }

const state = computed<UpdateState>(() => {
  // 最近一次「立即检查」失败 ⇒ 结论不可得（这是最直接的失败原因，优先于快照错误）。
  if (checkError.value) return { kind: 'unknown', reason: checkError.value }
  // 读取本身失败 ⇒ 结论不可得（不是「已是最新」）。
  if (loadError.value) return { kind: 'unknown', reason: loadError.value }
  const st = checkStatus.value
  if (!st) return { kind: 'unknown', reason: '' }
  // 未配置远端版本源 ⇒ 自动检索/提醒未启用 ⇒ 无从判断有没有新版。
  if (!st.configured) return { kind: 'notConfigured' }
  const r = st.result
  // 配了但没查过 ⇒ 结论不可得。
  if (!r) return { kind: 'unknown', reason: '' }
  // 查了但失败 ⇒ 结论不可得，且要给出原因。
  if (r.error) return { kind: 'unknown', reason: r.error }
  if (r.up_to_date) return { kind: 'upToDate' }
  if (r.latest) return { kind: 'available' }
  // 结论不完整（既没有 latest 也没有 up_to_date）⇒ 宁可说未知。
  return { kind: 'unknown', reason: '' }
})

const stateLabel = computed(() => {
  switch (state.value.kind) {
    case 'notConfigured':
      return t('selfUpdateView.stateNotConfiguredLabel')
    case 'unknown':
      return t('selfUpdateView.stateUnknownLabel')
    case 'upToDate':
      return t('selfUpdateView.stateUpToDateLabel')
    case 'available':
      return t('selfUpdateView.stateAvailableLabel')
  }
})

const stateDetail = computed(() => {
  switch (state.value.kind) {
    case 'notConfigured':
      return t('selfUpdateView.stateNotConfiguredDetail')
    case 'unknown':
      return state.value.reason
        ? t('selfUpdateView.stateUnknownDetail', { reason: state.value.reason })
        : t('selfUpdateView.stateNeverCheckedDetail')
    case 'upToDate':
      return t('selfUpdateView.stateUpToDateDetail')
    case 'available':
      return t('selfUpdateView.stateAvailableDetail', { version: latestVersion.value })
  }
})

const stateClass = computed(() => {
  switch (state.value.kind) {
    case 'upToDate':
      return 'state-line--ok'
    case 'available':
      return 'state-line--update'
    default:
      return 'state-line--unknown'
  }
})

// ── 派生读数 ────────────────────────────────────────────────────────────────
const currentVersion = computed(() => {
  const st = checkStatus.value
  return (
    st?.current_version || st?.result?.current_version || nodeVersion.value?.version || ''
  )
})

const latestRelease = computed<CheckResult['latest']>(() => {
  const r = checkStatus.value?.result
  if (state.value.kind === 'available' && r?.latest) return r.latest
  return undefined
})

/**
 * 「最新版本」栏的取值。
 *
 * ★ 关键纪律：结论不可得（unknown / notConfigured）时返回空串，
 *   界面上显示占位符 —— **绝不回退成当前版本号**（两栏同数 = 假话）。
 */
const latestVersion = computed(() => {
  if (state.value.kind === 'available') return latestRelease.value?.version || ''
  if (state.value.kind === 'upToDate') return currentVersion.value
  return ''
})

const buildSequence = computed(() => {
  const seq =
    checkStatus.value?.result?.current_build_seq ?? nodeVersion.value?.build_seq
  return typeof seq === 'number' ? String(seq) : UNKNOWN_VALUE
})

const lastCheckedAt = computed(() =>
  formatDateTime(checkStatus.value?.result?.checked_at, { locale: locale.value }),
)

const checkIntervalText = computed(() => {
  const secs = checkStatus.value?.interval_secs
  if (typeof secs !== 'number' || secs <= 0) return UNKNOWN_VALUE
  if (secs % 3600 === 0) return t('selfUpdateView.checkIntervalHours', { hours: secs / 3600 })
  if (secs % 60 === 0) return t('selfUpdateView.checkIntervalMinutes', { minutes: secs / 60 })
  return t('selfUpdateView.checkIntervalSeconds', { seconds: secs })
})

const releaseNotes = computed(() => latestRelease.value?.release_notes || '')

const mandatory = computed(() => latestRelease.value?.mandatory === true)

// ── 任务状态文案 ────────────────────────────────────────────────────────────
/** 安装任务状态 → 本地化文案（用字面量 t()，让引用扫描能看见每一个键）。 */
function jobStatusLabel(status: string): string {
  switch (status) {
    case 'pending':
      return t('selfUpdateView.jobStatusPending')
    case 'downloading':
      return t('selfUpdateView.jobStatusDownloading')
    case 'ready_to_restart':
      return t('selfUpdateView.jobStatusReadyToRestart')
    case 'upgrading':
      return t('selfUpdateView.jobStatusUpgrading')
    case 'success':
      return t('selfUpdateView.jobStatusSuccess')
    case 'failed':
      return t('selfUpdateView.jobStatusFailed')
    case 'rolled_back':
      return t('selfUpdateView.jobStatusRolledBack')
    default:
      return t('selfUpdateView.jobStatusUnknown')
  }
}

/** 安装模式 → 本地化文案。 */
function jobModeLabel(mode: string): string {
  if (mode === 'source') return t('selfUpdateView.modeSource')
  if (mode === 'artifact') return t('selfUpdateView.modeArtifact')
  return t('selfUpdateView.jobModeUnknown')
}

/** 提醒状态 → 本地化文案。 */
function notificationStatusLabel(status: string): string {
  if (status === 'pending') return t('selfUpdateView.notificationPending')
  return t('selfUpdateView.notificationHandled')
}

// ── 动作 ────────────────────────────────────────────────────────────────────
function errText(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}

async function loadCheckStatus(): Promise<void> {
  try {
    checkStatus.value = await getCheckStatus()
    loadError.value = ''
  } catch (err) {
    checkStatus.value = null
    loadError.value = errText(err)
  }
}

async function loadNotifications(): Promise<void> {
  try {
    const res = await getNotifications()
    notifications.value = res.items || []
  } catch (err) {
    notifications.value = []
    ElMessage.error(t('selfUpdateView.notificationsLoadFailed', { reason: errText(err) }))
  }
}

async function loadInstallStatus(): Promise<void> {
  try {
    const res = await getInstallStatus()
    if (res.job) job.value = res.job
  } catch {
    /* 读不到任务快照就保持现状（下一轮轮询会再试） */
  }
}

/** 本节点当前运行的版本（兜底；取不到就显示占位符，不报错）。 */
async function loadNodeVersion(): Promise<void> {
  try {
    const st = await getNodeUpgradeStatus()
    nodeVersion.value = {
      version: st.current_version || '',
      build_seq: typeof st.current_build_seq === 'number' ? st.current_build_seq : undefined,
    }
  } catch {
    nodeVersion.value = null
  }
}

/** 「立即检查更新」。 */
async function onCheckNow(): Promise<void> {
  checking.value = true
  checkError.value = ''
  try {
    const outcome = await runCheck()
    if (!outcome.ok) {
      // ★ 失败就是失败：记下原因，状态落到 unknown，**不写任何「已是最新」**。
      checkError.value = outcome.detail || outcome.error
      // 后端把失败结论也记进 lastResult ⇒ 拉一次快照，让「最近检查时间」等读数跟上。
      await loadCheckStatus()
      return
    }
    // 查成了 ⇒ 直接采信本次结论（不必再拉一次快照：那次拉取失败反而会把已得到的
    // 结论降级成「未知」）。
    checkStatus.value = {
      ...(checkStatus.value ?? { configured: true }),
      configured: true,
      checked: true,
      current_version: outcome.result.current_version,
      result: outcome.result,
    }
    await loadNotifications()
  } finally {
    checking.value = false
  }
}

// ── 安装任务轮询 ────────────────────────────────────────────────────────────
let pollTimer: number | null = null

function stopPolling(): void {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

function startPolling(): void {
  stopPolling()
  pollTimer = window.setInterval(() => {
    void pollInstallStatus()
  }, POLL_INTERVAL_MS)
}

async function pollInstallStatus(): Promise<void> {
  try {
    const res = await getInstallStatus()
    if (res.job) job.value = res.job
    if (!res.running) stopPolling()
  } catch {
    /* 本轮读失败就下一轮再试，不打断正在跑的安装 */
  }
}

/** 「立即升级」。 */
async function onInstallNow(): Promise<void> {
  const version = latestVersion.value
  if (!version) return
  try {
    await ElMessageBox.confirm(
      t('selfUpdateView.confirmInstallBody'),
      t('selfUpdateView.confirmInstallTitle', { version }),
      {
        confirmButtonText: t('selfUpdateView.confirmInstallButton'),
        cancelButtonText: t('common.cancel'),
        type: 'warning',
      },
    )
  } catch {
    return // 用户取消
  }
  const latest = latestRelease.value
  try {
    const created = await installVersion(version, {
      mode: 'artifact',
      restart: true,
      ...(latest?.binary_url ? { binary_url: latest.binary_url } : {}),
      ...(latest?.binary_sha256 ? { sha256: latest.binary_sha256 } : {}),
      // 升级配置合同：把 manifest 下发的主机配置变更（新增/强制更新）随安装
      // 请求透传给执行器——安装器写前先备份原配置，失败自动还原。
      ...(latest?.config_updates?.length ? { config_updates: latest.config_updates } : {}),
    })
    job.value = created
    startPolling()
  } catch (err) {
    ElMessage.error(t('selfUpdateView.installStartFailed', { reason: errText(err) }))
  }
}

/** 「从源码编译并升级」。 */
async function onSourceBuild(): Promise<void> {
  const version = latestVersion.value || currentVersion.value
  if (!version) {
    ElMessage.error(t('selfUpdateView.sourceBuildNoVersion'))
    return
  }
  try {
    await ElMessageBox.confirm(
      t('selfUpdateView.confirmSourceBuildBody'),
      t('selfUpdateView.confirmSourceBuildTitle'),
      {
        confirmButtonText: t('selfUpdateView.confirmSourceBuildButton'),
        cancelButtonText: t('common.cancel'),
        type: 'warning',
      },
    )
  } catch {
    return // 用户取消
  }
  const ref = sourceRef.value.trim()
  sourceBuilding.value = true
  try {
    const created = await sourceBuildVersion(version, {
      restart: true,
      ...(ref ? { source: { ref } } : {}),
    })
    job.value = created
    startPolling()
  } catch (err) {
    ElMessage.error(t('selfUpdateView.sourceBuildFailed', { reason: errText(err) }))
  } finally {
    sourceBuilding.value = false
  }
}

onMounted(async () => {
  await Promise.all([
    loadCheckStatus(),
    loadNotifications(),
    loadInstallStatus(),
    loadNodeVersion(),
  ])
  loading.value = false
})

onBeforeUnmount(stopPolling)
</script>

<template>
  <div class="self-update-page">
    <PageHeader
      :title="t('selfUpdateView.pageTitle')"
      :subtitle="t('selfUpdateView.pageSubtitle')"
    />

    <!-- ① 当前状态卡 -->
    <section class="panel">
      <div class="panel__head">
        <h3 class="panel__title">{{ t('selfUpdateView.statusCardTitle') }}</h3>
        <p class="panel__hint">{{ t('selfUpdateView.statusCardHint') }}</p>
      </div>

      <div class="stat-grid">
        <div class="stat stat--current">
          <span class="stat__label">{{ t('selfUpdateView.currentVersion') }}</span>
          <span class="stat__value">{{ currentVersion || UNKNOWN_VALUE }}</span>
        </div>
        <div class="stat stat--latest">
          <span class="stat__label">{{ t('selfUpdateView.latestVersion') }}</span>
          <span class="stat__value">{{ latestVersion || UNKNOWN_VALUE }}</span>
        </div>
        <div class="stat">
          <span class="stat__label">{{ t('selfUpdateView.buildSequence') }}</span>
          <span class="stat__value">{{ buildSequence }}</span>
        </div>
        <div class="stat">
          <span class="stat__label">{{ t('selfUpdateView.lastCheckedAt') }}</span>
          <span class="stat__value">{{ lastCheckedAt }}</span>
        </div>
        <div class="stat">
          <span class="stat__label">{{ t('selfUpdateView.checkInterval') }}</span>
          <span class="stat__value">{{ checkIntervalText }}</span>
        </div>
      </div>

      <!-- 检索结论：只有「查成了」才允许出现「已是最新」。 -->
      <p
        v-if="!loading"
        class="state-line"
        :class="stateClass"
        data-test="state-line"
      >
        <span class="state-line__label">{{ stateLabel }}</span>
        <span class="state-line__detail">{{ stateDetail }}</span>
      </p>

      <div class="panel__actions">
        <el-button
          type="primary"
          :loading="checking"
          :disabled="checking"
          data-test="check-now"
          @click="onCheckNow"
        >
          {{ checking ? t('selfUpdateView.checking') : t('selfUpdateView.checkNow') }}
        </el-button>
      </div>
    </section>

    <!-- ② 更新横幅（有新版才出现） -->
    <section v-if="state.kind === 'available'" class="update-banner" role="alert">
      <div class="update-banner__head">
        <span class="update-banner__title">{{ t('selfUpdateView.bannerTitle') }}</span>
        <el-tag :type="mandatory ? 'danger' : 'warning'" size="small">
          {{ mandatory ? t('selfUpdateView.bannerMandatory') : t('selfUpdateView.bannerOptional') }}
        </el-tag>
      </div>
      <p class="update-banner__version" data-test="latest-version">{{ latestVersion }}</p>
      <div class="update-banner__notes">
        <span class="update-banner__notes-title">{{ t('selfUpdateView.bannerNotesTitle') }}</span>
        <p class="update-banner__notes-body">
          {{ releaseNotes || t('selfUpdateView.bannerNoNotes') }}
        </p>
      </div>
      <div class="update-banner__actions">
        <el-button type="danger" data-test="install-now" @click="onInstallNow">
          {{ t('selfUpdateView.installNow') }}
        </el-button>
      </div>
    </section>

    <!-- ③ 安装进度卡 -->
    <section class="panel">
      <div class="panel__head">
        <h3 class="panel__title">{{ t('selfUpdateView.installCardTitle') }}</h3>
        <p class="panel__hint">{{ t('selfUpdateView.installCardHint') }}</p>
      </div>

      <p v-if="!job" class="empty-line">{{ t('selfUpdateView.installCardEmpty') }}</p>
      <div v-else class="job">
        <div class="stat-grid">
          <div class="stat">
            <span class="stat__label">{{ t('selfUpdateView.jobVersion') }}</span>
            <span class="stat__value">{{ job.version }}</span>
          </div>
          <div class="stat">
            <span class="stat__label">{{ t('selfUpdateView.jobMode') }}</span>
            <span class="stat__value">{{ jobModeLabel(job.mode) }}</span>
          </div>
          <div class="stat">
            <span class="stat__label">{{ t('selfUpdateView.jobStatus') }}</span>
            <span class="stat__value" data-test="job-status">{{ jobStatusLabel(job.status) }}</span>
          </div>
          <div class="stat">
            <span class="stat__label">{{ t('selfUpdateView.jobStartedAt') }}</span>
            <span class="stat__value">
              {{ formatDateTime(job.started_at, { locale: locale }) }}
            </span>
          </div>
        </div>

        <h4 class="job__steps-title">{{ t('selfUpdateView.jobStepsTitle') }}</h4>
        <ol v-if="job.steps && job.steps.length" class="job__steps" data-test="job-steps">
          <li v-for="(step, idx) in job.steps" :key="idx" class="job__step">{{ step }}</li>
        </ol>
        <p v-else class="empty-line">{{ t('selfUpdateView.jobStepsEmpty') }}</p>

        <p v-if="job.error" class="job__error" data-test="job-error">
          {{ t('selfUpdateView.jobErrorTitle') }}：{{ job.error }}
        </p>
        <p v-if="job.status === 'success'" class="job__notice" data-test="job-success">
          {{ t('selfUpdateView.jobSuccessNotice') }}
        </p>
        <p v-if="job.restart_scheduled" class="job__notice" data-test="job-restart">
          {{ t('selfUpdateView.jobRestartScheduled') }}
        </p>
      </div>
    </section>

    <!-- ④ 提醒列表卡 -->
    <section class="panel">
      <div class="panel__head">
        <h3 class="panel__title">{{ t('selfUpdateView.notificationsTitle') }}</h3>
        <p class="panel__hint">{{ t('selfUpdateView.notificationsHint') }}</p>
      </div>

      <p v-if="!notifications.length" class="empty-line">
        {{ t('selfUpdateView.notificationsEmpty') }}
      </p>
      <table v-else class="notify-table">
        <thead>
          <tr>
            <th scope="col">{{ t('selfUpdateView.notificationVersion') }}</th>
            <th scope="col">{{ t('selfUpdateView.notificationStatus') }}</th>
            <th scope="col">{{ t('selfUpdateView.notificationStartedAt') }}</th>
            <th scope="col">{{ t('selfUpdateView.notificationCompletedAt') }}</th>
            <th scope="col">{{ t('selfUpdateView.notificationRetryCount') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(n, idx) in notifications" :key="idx" class="notify-table__row">
            <td>{{ n.version }}</td>
            <td>{{ notificationStatusLabel(n.status) }}</td>
            <td>{{ formatDateTime(n.started_at, { locale: locale }) }}</td>
            <td>{{ formatDateTime(n.completed_at, { locale: locale }) }}</td>
            <td>{{ n.retry_count }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- ⑤ 本地源码编译卡 -->
    <section class="panel">
      <div class="panel__head">
        <h3 class="panel__title">{{ t('selfUpdateView.sourceCardTitle') }}</h3>
        <p class="panel__hint">{{ t('selfUpdateView.sourceCardHint') }}</p>
      </div>

      <div class="source-form">
        <label class="source-form__label" for="self-update-source-ref">
          {{ t('selfUpdateView.sourceRefLabel') }}
        </label>
        <el-input
          id="self-update-source-ref"
          v-model="sourceRef"
          class="source-form__input"
          :placeholder="t('selfUpdateView.sourceRefPlaceholder')"
        />
        <el-button
          type="primary"
          :loading="sourceBuilding"
          :disabled="sourceBuilding"
          data-test="source-build"
          @click="onSourceBuild"
        >
          {{ t('selfUpdateView.sourceBuildNow') }}
        </el-button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.self-update-page {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-4);
}

.panel {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-3);
  padding: var(--kx-space-4);
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-md);
}

.panel__head {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
}

.panel__title {
  margin: 0;
  font-size: var(--kx-text-lg);
  font-weight: 600;
}

.panel__hint {
  margin: 0;
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.panel__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--kx-space-2);
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: var(--kx-space-3);
}

.stat {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  padding: var(--kx-space-3);
  background: var(--kx-surface-secondary);
  border-radius: var(--kx-radius-sm);
}

.stat__label {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.stat__value {
  font-size: var(--kx-text-md);
  font-weight: 600;
  overflow-wrap: anywhere;
}

.state-line {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  margin: 0;
  padding: var(--kx-space-3);
  font-size: var(--kx-text-sm);
  border-radius: var(--kx-radius-sm);
  border: 1px solid var(--kx-border);
  background: var(--kx-surface-secondary);
}

.state-line--ok {
  border-color: var(--kx-success);
}

.state-line--update {
  border-color: var(--kx-warning);
}

.state-line--unknown {
  border-color: var(--kx-warning);
}

.state-line__label {
  font-weight: 600;
}

.state-line__detail {
  color: var(--kx-muted);
}

.update-banner {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-3);
  padding: var(--kx-space-4);
  border: 1px solid var(--kx-warning);
  border-radius: var(--kx-radius-md);
  background: var(--kx-warning-soft);
}

.update-banner__head {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  flex-wrap: wrap;
}

.update-banner__title {
  font-size: var(--kx-text-lg);
  font-weight: 600;
}

.update-banner__version {
  margin: 0;
  font-size: var(--kx-text-xl);
  font-weight: 700;
}

.update-banner__notes {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
}

.update-banner__notes-title {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.update-banner__notes-body {
  margin: 0;
  font-size: var(--kx-text-sm);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.update-banner__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--kx-space-2);
}

.job {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-3);
}

.job__steps-title {
  margin: 0;
  font-size: var(--kx-text-sm);
  font-weight: 600;
}

.job__steps {
  margin: 0;
  padding-left: var(--kx-space-5);
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  font-size: var(--kx-text-sm);
}

.job__step {
  overflow-wrap: anywhere;
}

.job__error {
  margin: 0;
  padding: var(--kx-space-3);
  font-size: var(--kx-text-sm);
  color: var(--kx-danger);
  border: 1px solid var(--kx-danger);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-danger-soft);
}

.job__notice {
  margin: 0;
  padding: var(--kx-space-3);
  font-size: var(--kx-text-sm);
  color: var(--kx-success);
  border: 1px solid var(--kx-success);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-success-soft);
}

.empty-line {
  margin: 0;
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
}

.notify-table {
  width: 100%;
  border-collapse: collapse;
}

.notify-table th,
.notify-table td {
  text-align: left;
  padding: var(--kx-space-2) var(--kx-space-3);
  border-bottom: 1px solid var(--kx-border);
  overflow-wrap: anywhere;
}

.notify-table__row td {
  font-size: var(--kx-text-sm);
}

.source-form {
  display: flex;
  align-items: center;
  gap: var(--kx-space-3);
  flex-wrap: wrap;
}

.source-form__label {
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
}

.source-form__input {
  flex: 1 1 240px;
  min-width: 0;
}

@media (max-width: 768px) {
  .source-form {
    flex-direction: column;
    align-items: stretch;
  }

  .stat-grid {
    grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  }
}
</style>
