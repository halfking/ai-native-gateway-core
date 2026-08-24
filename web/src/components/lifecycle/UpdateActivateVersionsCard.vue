<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { CatalogResponse, Release, CatalogItem, UpgradeStatus } from '../../api/updateActivate'

/** UpdateActivateVersionsCard — 系统版本列表（最新 5 个）+ 升级→下载→安装→启动切换流程。
 *  复用 maintain /maintain/download + /maintain/upgrade 的视觉规范。 */

export type UpgradeStepId = 'download' | 'install' | 'switch'

export type UpgradeStep = {
  id: UpgradeStepId
  title: string
  description: string
  status: 'idle' | 'in_progress' | 'done' | 'failed'
}

const props = defineProps<{
  catalog: CatalogResponse | null
  loading: boolean
  checking: boolean
  upgradeStatus: UpgradeStatus | null
  currentVersion: string
  activated: boolean
}>()

const emit = defineEmits<{
  (e: 'check'): void
  (e: 'download', payload: { version: string; item: CatalogItem }): void
  (e: 'install', payload: { version: string }): void
  (e: 'switch', payload: { version: string }): void
  (e: 'refresh'): void
}>()

const latestVersion = computed(() => props.upgradeStatus?.latest_version || props.currentVersion)
const installedSet = computed(() => new Set([props.currentVersion, props.upgradeStatus?.current_version].filter(Boolean) as string[]))

const topVersions = computed(() => {
  const versions = props.catalog?.versions || []
  return versions.slice(0, 5)
})

const selected = ref<Release | null>(null)
const selectedItem = ref<CatalogItem | null>(null)

function pickVersion(release: Release) {
  selected.value = release
  selectedItem.value = release.items[0] || null
}

function isInstalled(version: string): boolean {
  return installedSet.value.has(version)
}

const canUpgrade = computed(() => {
  const s = props.upgradeStatus
  if (!s?.has_update || !s.latest_version) return false
  return s.latest_version !== s.current_version
})

// ===== 升级流程状态机 =====
const STEPS_TEMPLATE: Omit<UpgradeStep, 'status'>[] = [
  { id: 'download', title: '下载新版本', description: '从中心获取安装包下载链接并落盘。' },
  { id: 'install', title: '安装新版本', description: '解压安装包并替换当前服务进程。' },
  { id: 'switch', title: '启动切换', description: '重启服务并切换流量到新版本。' },
]

const steps = ref<UpgradeStep[]>(STEPS_TEMPLATE.map((s) => ({ ...s, status: 'idle' })))
const upgrading = ref<string | null>(null)

function resetSteps() {
  steps.value = STEPS_TEMPLATE.map((s) => ({ ...s, status: 'idle' }))
}

function setStep(id: UpgradeStepId, status: UpgradeStep['status']) {
  const step = steps.value.find((s) => s.id === id)
  if (step) step.status = status
}

function pickFirstItem(items: CatalogItem[]): CatalogItem | null {
  // 优先 linux/amd64（与服务运行环境一致），其次任意
  const preferred = items.find((i) => i.platform === 'linux' && i.arch === 'amd64')
  return preferred || items[0] || null
}

async function startUpgrade() {
  if (!canUpgrade.value || !props.upgradeStatus?.latest_version) return
  if (!props.activated) {
    ElMessage.warning('请先完成激活再升级')
    return
  }
  const targetVersion = props.upgradeStatus.latest_version
  const release = topVersions.value.find((v) => v.version === targetVersion)
  const item = release ? pickFirstItem(release.items) : null
  if (!release || !item) {
    ElMessage.error('未在版本目录中找到可下载的安装包')
    return
  }
  try {
    await ElMessageBox.confirm(
      `即将把服务升级到 ${targetVersion}（${item.label}）。过程中服务会短暂不可用，请确认无重要任务正在进行。`,
      '确认升级',
      { confirmButtonText: '开始升级', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  upgrading.value = targetVersion
  resetSteps()
  await runUpgradeFlow(targetVersion, item)
}

async function runUpgradeFlow(version: string, item: CatalogItem) {
  // 步骤 1：下载
  setStep('download', 'in_progress')
  try {
    emit('download', { version, item })
    // 等待父组件完成下载事件上报后切到 done。父组件 emit 不阻塞，但保留 in_progress 让 UI 自然过渡。
    await new Promise((r) => setTimeout(r, 800))
    setStep('download', 'done')
  } catch (e) {
    setStep('download', 'failed')
    ElMessage.error(`下载失败：${(e as Error).message}`)
    upgrading.value = null
    return
  }

  // 步骤 2：安装
  setStep('install', 'in_progress')
  try {
    emit('install', { version })
    await new Promise((r) => setTimeout(r, 1200))
    setStep('install', 'done')
  } catch (e) {
    setStep('install', 'failed')
    ElMessage.error(`安装失败：${(e as Error).message}`)
    upgrading.value = null
    return
  }

  // 步骤 3：启动切换
  setStep('switch', 'in_progress')
  try {
    emit('switch', { version })
    await new Promise((r) => setTimeout(r, 800))
    setStep('switch', 'done')
    ElMessage.success(`已切换到 ${version}`)
    emit('refresh')
  } catch (e) {
    setStep('switch', 'failed')
    ElMessage.error(`启动切换失败：${(e as Error).message}`)
  } finally {
    upgrading.value = null
  }
}
</script>

<template>
  <el-card shadow="never" class="ua-card versions-card">
    <template #header>
      <div class="ua-card__head">
        <span class="ua-card__title">系统版本与升级</span>
        <div class="head-actions">
          <el-button size="small" :loading="checking" @click="emit('check')">检查更新</el-button>
          <el-button size="small" text :loading="loading" @click="emit('refresh')">刷新目录</el-button>
        </div>
      </div>
    </template>

    <el-skeleton v-if="loading && !catalog" :rows="5" animated />

    <template v-else-if="catalog && topVersions.length">
      <!-- 当前版本 + 最新版本条幅 -->
      <div class="version-banner">
        <div class="banner-cell">
          <span class="banner-label">当前版本</span>
          <strong class="banner-value">
            {{ upgradeStatus?.current_version || currentVersion || '—' }}
          </strong>
          <el-tag v-if="upgradeStatus?.current_build_seq" size="small" type="info" class="ml">
            build {{ upgradeStatus.current_build_seq }}
          </el-tag>
        </div>
        <div class="banner-arrow" aria-hidden="true">→</div>
        <div class="banner-cell">
          <span class="banner-label">最新版本</span>
          <strong class="banner-value">{{ latestVersion || '—' }}</strong>
          <el-tag v-if="canUpgrade" size="small" type="warning" class="ml">未安装</el-tag>
          <el-tag v-else size="small" type="success" class="ml">已是最新</el-tag>
        </div>
      </div>

      <div v-if="upgradeStatus?.update_mandatory" class="mandatory-banner">
        <strong>本次升级为强制更新</strong>
        <span>最低要求 {{ upgradeStatus.min_version || '—' }}，请尽快升级。</span>
      </div>

      <!-- 5 个版本列表 -->
      <div class="version-list">
        <div
          v-for="release in topVersions"
          :key="release.version"
          class="version-row"
          :class="{ 'is-selected': selected?.version === release.version, 'is-installed': isInstalled(release.version) }"
          @click="pickVersion(release)"
        >
          <div class="version-row__main">
            <div class="version-row__title">
              <code class="version-row__name">{{ release.version }}</code>
              <el-tag v-if="isInstalled(release.version)" size="small" type="success">已安装</el-tag>
              <el-tag v-else-if="release.version === latestVersion" size="small" type="warning">未安装</el-tag>
              <el-tag v-else size="small" type="info">历史版本</el-tag>
            </div>
            <div class="version-row__meta">
              <span>{{ release.release_date }}</span>
              <span>·</span>
              <span>build {{ release.build_seq }}</span>
              <span>·</span>
              <span>{{ release.channel }}</span>
            </div>
          </div>
          <div class="version-row__artifacts">
            <span v-for="item in release.items.slice(0, 3)" :key="item.artifact_name" class="artifact-chip">
              {{ item.label }}
            </span>
            <span v-if="release.items.length > 3" class="artifact-chip artifact-chip--more">
              +{{ release.items.length - 3 }}
            </span>
          </div>
        </div>
      </div>

      <!-- 升级流程：升级→下载→安装→启动切换 -->
      <div class="upgrade-flow">
        <div class="upgrade-flow__head">
          <h4 class="flow-title">升级流程</h4>
          <span class="flow-hint">检查更新 → 下载 → 安装 → 启动切换</span>
        </div>
        <div class="flow-steps" role="list">
          <div
            v-for="(step, idx) in steps"
            :key="step.id"
            class="flow-step"
            :class="`flow-step--${step.status}`"
            role="listitem"
          >
            <div class="flow-step__index">{{ idx + 1 }}</div>
            <div class="flow-step__body">
              <div class="flow-step__title">{{ step.title }}</div>
              <div class="flow-step__desc">{{ step.description }}</div>
            </div>
            <div class="flow-step__status">
              <el-tag v-if="step.status === 'idle'" size="small" type="info">待执行</el-tag>
              <el-tag v-else-if="step.status === 'in_progress'" size="small" type="warning">进行中</el-tag>
              <el-tag v-else-if="step.status === 'done'" size="small" type="success">已完成</el-tag>
              <el-tag v-else size="small" type="danger">失败</el-tag>
            </div>
          </div>
        </div>
        <div class="flow-actions">
          <el-button
            type="primary"
            :disabled="!canUpgrade || upgrading !== null"
            :loading="!!upgrading"
            @click="startUpgrade"
          >
            {{ upgrading ? `正在切换 ${upgrading}…` : `升级到 ${latestVersion}` }}
          </el-button>
          <span class="muted">
            <template v-if="!props.activated">需先完成激活才能升级</template>
            <template v-else-if="!canUpgrade">当前已是最新版本</template>
            <template v-else>升级过程会短暂中断服务，请提前做好准备</template>
          </span>
        </div>
      </div>
    </template>

    <el-empty
      v-else
      description="暂无版本目录（中心可能不可达，或尚未发布版本）"
      :image-size="72"
    />
  </el-card>
</template>

<style scoped>
.versions-card { height: 100%; }
.ua-card__head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.ua-card__title { font-weight: 600; }
.head-actions { display: flex; gap: 8px; }

.version-banner {
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  gap: 16px;
  padding: 14px 16px;
  background: var(--kx-surface-soft, rgba(0, 0, 0, 0.03));
  border: 1px solid var(--kx-border, rgba(0, 0, 0, 0.08));
  border-radius: 10px;
  margin-bottom: 16px;
}
.banner-cell { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.banner-label {
  font-size: 11px;
  color: var(--muted, var(--muted));
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.banner-value {
  font-size: 18px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.banner-arrow {
  font-size: 20px;
  color: var(--muted, var(--muted));
  align-self: center;
}
.ml { margin-left: 6px; }

.mandatory-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  margin-bottom: 12px;
  background: rgba(217, 119, 6, 0.1);
  border-left: 3px solid var(--kx-warning, var(--warning));
  border-radius: 4px;
  font-size: 13px;
  color: var(--kx-warning, var(--warning-dark));
}

.version-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 20px;
}
.version-row {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 12px;
  align-items: center;
  padding: 10px 14px;
  border: 1px solid var(--kx-border, rgba(0, 0, 0, 0.08));
  border-radius: 8px;
  cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
}
.version-row:hover { border-color: var(--kx-primary, var(--accent)); }
.version-row.is-selected {
  border-color: var(--kx-primary, var(--accent));
  background: rgba(37, 99, 235, 0.06);
}
.version-row.is-installed { background: rgba(22, 163, 74, 0.04); }
.version-row__main { min-width: 0; }
.version-row__title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 4px;
}
.version-row__name {
  font-size: 14px;
  font-weight: 600;
}
.version-row__meta {
  display: flex;
  gap: 6px;
  font-size: 12px;
  color: var(--muted, var(--muted));
  flex-wrap: wrap;
}
.version-row__artifacts {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.artifact-chip {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--kx-surface-soft, rgba(0, 0, 0, 0.05));
  color: var(--muted, var(--muted));
}
.artifact-chip--more { background: rgba(0, 0, 0, 0.08); }

.upgrade-flow {
  padding: 16px;
  background: var(--kx-surface-soft, rgba(0, 0, 0, 0.02));
  border: 1px solid var(--kx-border, rgba(0, 0, 0, 0.06));
  border-radius: 10px;
}
.upgrade-flow__head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 12px;
  flex-wrap: wrap;
  gap: 8px;
}
.flow-title { margin: 0; font-size: 14px; font-weight: 600; }
.flow-hint { font-size: 12px; color: var(--muted, var(--muted)); }

.flow-steps {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 10px;
  margin-bottom: 14px;
}
.flow-step {
  display: grid;
  grid-template-columns: 28px 1fr auto;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--kx-border, rgba(0, 0, 0, 0.08));
  border-radius: 8px;
  background: var(--kx-surface, var(--on-primary));
}
.flow-step--in_progress { border-color: var(--kx-warning, var(--warning)); background: rgba(217, 119, 6, 0.05); }
.flow-step--done { border-color: var(--kx-success, var(--success)); background: rgba(22, 163, 74, 0.05); }
.flow-step--failed { border-color: var(--danger); background: rgba(220, 38, 38, 0.05); }
.flow-step__index {
  width: 28px;
  height: 28px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 600;
  font-size: 13px;
  background: var(--kx-primary-soft, rgba(37, 99, 235, 0.12));
  color: var(--kx-primary, var(--accent));
}
.flow-step--done .flow-step__index { background: rgba(22, 163, 74, 0.15); color: var(--kx-success, var(--success)); }
.flow-step--failed .flow-step__index { background: rgba(220, 38, 38, 0.15); color: var(--danger); }
.flow-step--in_progress .flow-step__index { background: rgba(217, 119, 6, 0.18); color: var(--kx-warning, var(--warning)); }
.flow-step__body { min-width: 0; }
.flow-step__title { font-size: 13px; font-weight: 600; margin-bottom: 2px; }
.flow-step__desc { font-size: 12px; color: var(--muted, var(--muted)); }

.flow-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.muted { color: var(--muted, var(--muted)); font-size: 12px; }
</style>