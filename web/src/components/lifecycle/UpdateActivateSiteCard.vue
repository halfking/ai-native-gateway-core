<script setup lang="ts">
import type { BootstrapStatus } from '../../api/bootstrap'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElCard, ElDescriptions, ElDescriptionsItem, ElEmpty, ElSkeleton, ElTag } from 'element-plus'
defineProps<{
  status: BootstrapStatus | null
  loading: boolean
  instanceId: string
  deviceName: string
}>()
</script>

<template>
  <el-card shadow="never" class="ua-card">
    <template #header>
      <span class="ua-card__title">站点信息</span>
    </template>
    <el-skeleton v-if="loading" :rows="5" animated />
    <el-descriptions v-else-if="status" :column="1" border size="small">
      <el-descriptions-item label="当前 IP">
        <code>{{ status.primary_ip || '—' }}</code>
      </el-descriptions-item>
      <el-descriptions-item label="服务版本">
        <code>{{ status.service_version || '—' }}</code>
      </el-descriptions-item>
      <el-descriptions-item label="注册名称">
        {{ status.device_name || deviceName || '—' }}
      </el-descriptions-item>
      <el-descriptions-item label="实例 ID">
        <code>{{ status.instance_id || instanceId }}</code>
      </el-descriptions-item>
      <el-descriptions-item label="激活状态">
        <el-tag :type="status.activated ? 'success' : 'warning'" size="small">
          {{ status.activated ? '已激活' : '未激活' }}
        </el-tag>
        <span v-if="status.center_online" class="muted"> · 中心可达</span>
        <span v-else class="muted"> · 中心不可达</span>
      </el-descriptions-item>
      <el-descriptions-item label="激活时间">
        {{ status.activated_at || '—' }}
      </el-descriptions-item>
      <el-descriptions-item v-if="status.license_key" label="License">
        <code>{{ status.license_key }}</code>
      </el-descriptions-item>
      <el-descriptions-item v-if="status.message" label="说明">
        <span class="muted">{{ status.message }}</span>
      </el-descriptions-item>
    </el-descriptions>
    <el-empty v-else description="无法读取站点状态" />
  </el-card>
</template>

<style scoped>
.ua-card__title { font-weight: 600; }
.muted { color: var(--muted); font-size: 13px; }
code { font-size: 12px; word-break: break-all; }
</style>
