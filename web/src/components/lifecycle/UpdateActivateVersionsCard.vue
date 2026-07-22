<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { UpgradeStatus } from '../../api/updateActivate'

const props = defineProps<{
  status: UpgradeStatus | null
  loading: boolean
  checking: boolean
  activated?: boolean
}>()

const emit = defineEmits<{
  (e: 'check'): void
  (e: 'upgrade'): void
}>()

const canUpgrade = computed(() => {
  const s = props.status
  if (!s?.has_update || !s.latest_version) return false
  return s.latest_version !== s.current_version
})

// 模拟版本列表（实际应从后端获取）
const versionList = computed(() => {
  if (!props.status) return []
  const versions = [
    {
      version: props.status.latest_version || 'v2.4.7',
      releaseDate: '2026-07-22',
      description: '修复激活流程、优化性能',
      installed: !canUpgrade.value,
      downloaded: false,
    },
    {
      version: 'v2.4.6',
      releaseDate: '2026-07-20',
      description: '增强安全性、修复若干 bug',
      installed: false,
      downloaded: false,
    },
    {
      version: 'v2.4.5',
      releaseDate: '2026-07-18',
      description: '新增批量操作、UI 优化',
      installed: false,
      downloaded: false,
    },
  ]
  return versions.slice(0, 3)
})

const downloading = ref<string | null>(null)
const upgrading = ref<string | null>(null)

async function handleDownload(version: string) {
  downloading.value = version
  try {
    // 模拟下载
    await new Promise(resolve => setTimeout(resolve, 2000))
    ElMessage.success(`版本 ${version} 下载完成`)
    // 实际应更新版本列表状态
  } catch (e) {
    ElMessage.error(`下载失败: ${(e as Error).message}`)
  } finally {
    downloading.value = null
  }
}

async function handleUpgrade(version: string) {
  try {
    await ElMessageBox.confirm(
      `确认升级到版本 ${version}？升级过程中服务会短暂中断，请确保没有重要任务正在运行。`,
      '确认升级',
      {
        confirmButtonText: '确认升级',
        cancelButtonText: '取消',
        type: 'warning',
      }
    )

    upgrading.value = version
    ElMessage.info('开始升级，请稍候...')

    // 模拟升级流程
    await new Promise(resolve => setTimeout(resolve, 1000))
    ElMessage.success('环境检查完成')

    await new Promise(resolve => setTimeout(resolve, 2000))
    ElMessage.success('升级包解压完成')

    await new Promise(resolve => setTimeout(resolve, 1500))
    ElMessage.success('服务重启中...')

    // 实际应调用后端 API
    emit('upgrade')

  } catch (e) {
    if (e !== 'cancel') {
      ElMessage.error(`升级失败: ${(e as Error).message}`)
    }
  } finally {
    upgrading.value = null
  }
}
</script>

<template>
  <el-card shadow="never" class="ua-card">
    <template #header>
      <div class="ua-card__head">
        <span class="ua-card__title">版本与升级</span>
        <el-button size="small" :loading="checking" @click="emit('check')">检查更新</el-button>
      </div>
    </template>
    <el-skeleton v-if="loading" :rows="4" animated />
    <template v-else-if="status">
      <el-descriptions :column="2" border size="small" class="mb">
        <el-descriptions-item label="当前版本">
          <code>{{ status.current_version || '—' }}</code>
          <el-tag size="small" type="success" class="ml">已安装</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="频道">{{ status.channel || 'stable' }}</el-descriptions-item>
        <el-descriptions-item label="最新版本">
          <code>{{ status.latest_version || status.current_version || '—' }}</code>
          <el-tag v-if="canUpgrade" size="small" type="warning" class="ml">未安装</el-tag>
          <el-tag v-else size="small" class="ml">已是最新</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="强制升级">
          {{ status.update_mandatory ? '是' : '否' }}
        </el-descriptions-item>
      </el-descriptions>
      <div v-if="status.release_title || status.release_notes" class="notes">
        <h4>{{ status.release_title || '发布说明' }}</h4>
        <pre>{{ status.release_notes || '暂无发布说明' }}</pre>
      </div>
      <div v-if="canUpgrade" class="actions">
        <el-button type="primary" @click="emit('upgrade')">升级到 {{ status.latest_version }}</el-button>
        <span class="muted">将跳转下载/升级入口；管理员也可在运维中心执行灰度发布。</span>
      </div>
    </template>
    <el-empty v-else description="暂无版本信息（中心或本地升级服务不可用）" />
  </el-card>
</template>

<style scoped>
.ua-card__head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.ua-card__title { font-weight: 600; }
.mb { margin-bottom: 12px; }
.ml { margin-left: 8px; }
.notes h4 { margin: 0 0 8px; font-size: 14px; }
.notes pre {
  margin: 0;
  white-space: pre-wrap;
  font-size: 12px;
  line-height: 1.5;
  color: var(--muted);
  max-height: 180px;
  overflow: auto;
}
.actions { display: flex; flex-wrap: wrap; gap: 12px; align-items: center; margin-top: 12px; }
.muted { color: var(--muted); font-size: 12px; }
code { font-size: 12px; }
</style>
