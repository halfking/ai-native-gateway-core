<script setup lang="ts">
import type { ModuleCatalogItem } from '../../api/updateActivate'

defineProps<{
  items: ModuleCatalogItem[]
  loading: boolean
  error: string
}>()
</script>

<template>
  <el-card shadow="never" class="ua-card">
    <template #header>
      <div class="ua-card__head">
        <span class="ua-card__title">开通的模块服务</span>
        <a class="hint" href="https://llm.kxpms.cn/admin/modules" target="_blank" rel="noopener">
          在中心申请开通
        </a>
      </div>
    </template>
    <el-alert v-if="error" type="warning" :title="error" show-icon :closable="false" class="mb" />
    <el-skeleton v-if="loading" :rows="3" animated />
    <el-table v-else-if="items.length" :data="items" size="small" stripe>
      <el-table-column prop="display_name" label="模块" min-width="140">
        <template #default="{ row }">
          {{ row.display_name || row.module_id }}
        </template>
      </el-table-column>
      <el-table-column prop="version" label="版本" width="100" />
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag v-if="row.installed" size="small" type="success">已安装</el-tag>
          <el-tag v-else-if="row.licensed" size="small" type="info">已授权</el-tag>
          <el-tag v-else size="small">未开通</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="开通时间" min-width="160">
        <template #default="{ row }">
          {{ row.opened_at || row.activated_at || '—' }}
        </template>
      </el-table-column>
      <el-table-column prop="description" label="说明" min-width="180" show-overflow-tooltip />
    </el-table>
    <el-empty v-else description="暂无模块清单（请确认中心 modules/catalog 可用）" />
    <p class="muted">
      模块清单由中心发版目录下发；开通申请在
      <a href="https://llm.kxpms.cn/admin/modules" target="_blank" rel="noopener">llm.kxpms.cn/admin/modules</a>
      完成。
    </p>
  </el-card>
</template>

<style scoped>
.ua-card__head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.ua-card__title { font-weight: 600; }
.hint { font-size: 13px; }
.mb { margin-bottom: 12px; }
.muted { margin: 12px 0 0; color: var(--muted); font-size: 12px; line-height: 1.5; }
</style>
