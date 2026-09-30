<script setup lang="ts">
// DistributionTable.vue — name + share bar + numeric columns (2026-09-30 stats UI).
import { ElTable, ElTableColumn, ElTooltip } from 'element-plus'
import BarCell from '../ui/BarCell.vue'
import type { DistRow } from '../reconciliation/distTypes'

const props = withDefaults(defineProps<{
  rows: DistRow[]
  nameHeader: string
  headers: string[]
  showBar?: boolean
  clickable?: boolean
  testId?: string
}>(), {
  showBar: true,
  clickable: true,
  testId: 'dist-table',
})

const emit = defineEmits<{ row: [key: string] }>()

function onRow(row: { key?: string }) {
  if (!props.clickable || !row?.key) return
  emit('row', row.key)
}

function reasonText(row: unknown): string {
  const reasons = (row as DistRow).reasons ?? []
  return reasons.map((item) => `${item.code} ×${item.count}`).join(' · ')
}
</script>

<template>
  <div :data-testid="testId" :class="{ clickable }">
    <el-table
      :data="rows"
      size="small"
      border
      stripe
      max-height="480"
      @row-click="onRow"
    >
      <el-table-column :label="nameHeader" min-width="180" fixed>
        <template #default="{ row }">
          <BarCell v-if="showBar" :pct="row.pct" :tone="row.tone">
            <template #name>
              <span>{{ row.name }}</span>
              <span v-if="row.sub" class="sub">{{ row.sub }}</span>
            </template>
          </BarCell>
          <span v-else class="plain-name">{{ row.name }}<span v-if="row.sub" class="sub">{{ row.sub }}</span></span>
          <el-tooltip v-if="row.reasons?.length" :content="reasonText(row)" placement="top">
            <span class="reasons">
              <span v-for="item in row.reasons.slice(0, 2)" :key="item.code" class="chip-badge">
                {{ item.code }} ×{{ item.count }}
              </span>
            </span>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column
        v-for="(header, index) in headers"
        :key="header"
        :label="header"
        min-width="96"
        align="right"
      >
        <template #default="{ row }">
          <span :class="row.cells[index]?.tone && `tone-${row.cells[index].tone}`">
            {{ row.cells[index]?.text ?? '—' }}
          </span>
          <div v-if="row.cells[index]?.sub" class="sub num">{{ row.cells[index].sub }}</div>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.sub {
  margin-left: 6px;
  color: var(--text-muted, var(--el-text-color-secondary));
  font-size: 12px;
  font-weight: 400;
}
.sub.num { margin-left: 0; }
.plain-name { font-weight: 600; }
.reasons { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 4px; }
.chip-badge {
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 0 6px;
  font-size: 11px;
  color: var(--text-muted, var(--el-text-color-secondary));
}
.tone-ok { color: var(--success); }
.tone-warn { color: var(--warning); }
.tone-bad { color: var(--danger); }
.clickable :deep(.el-table__row) { cursor: pointer; }
</style>
