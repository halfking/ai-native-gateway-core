<script setup lang="ts">
// TurnsFilterOptionsView — 批 201（2026-10-17）
// 🟢 加 router/nav：turnsFilterOptions 独立深链详细页
//
// 端点：GET /api/admin/turns/sessions/filter-options
// 类型：TurnsFilterOptions（api/turns.ts:147）9 字段：
//   projects / tasks / owners / clients / tags / models / providers / status_codes
//   + api_keys（TurnsAPIKeyOption[]）
//
// 真 orphan 性：listTurnsFilterOptions() + TurnsFilterOptions 类型已 export（api/turns.ts:204），
//   TurnsListView 已用（line 54/128/323），但缺独立深链详细页让用户能直接打开 filter options。
//
// 调研：/tmp/co153-wt-evidence.txt 17 跳（line 1066 「已有隔离 / 可直接接前端」）
import { onMounted, ref } from 'vue'
import { listTurnsFilterOptions, type TurnsFilterOptions, type TurnsAPIKeyOption } from '../api/turns'

const loading = ref(false)
const error = ref('')
const options = ref<TurnsFilterOptions | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    options.value = await listTurnsFilterOptions()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

interface StringSection { key: string; label: string; values: string[] }
const stringSections = (): StringSection[] => {
  const o = options.value
  if (!o) return []
  return [
    { key: 'projects', label: '项目', values: o.projects },
    { key: 'tasks', label: '任务', values: o.tasks },
    { key: 'owners', label: '所有者', values: o.owners },
    { key: 'clients', label: '客户端', values: o.clients },
    { key: 'tags', label: '标签', values: o.tags },
    { key: 'models', label: '模型', values: o.models },
    { key: 'providers', label: '供应商', values: o.providers },
    { key: 'status_codes', label: '状态码', values: o.status_codes },
  ].filter((s) => s.values.length > 0)
}

const apiKeyList = (): TurnsAPIKeyOption[] => {
  return options.value?.api_keys ?? []
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="turns-filter-options-view">
    <header class="tfo-header">
      <h1 class="tfo-title">轮次筛选选项</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
      </button>
    </header>

    <p v-if="error" class="tfo-error">{{ error }}</p>

    <div v-if="loading && !options" class="tfo-loading">{{ $t('common.feedback.loading') }}</div>

    <template v-else-if="options">
      <section v-for="s in stringSections()" :key="s.key" class="tfo-section">
        <header class="tfo-section-header">
          <h2 class="tfo-section-title">{{ s.label }}</h2>
          <span class="tfo-section-count">{{ s.values.length }} 项</span>
        </header>
        <ul class="tfo-chips">
          <li v-for="v in s.values" :key="s.key + v" class="tfo-chip"><code>{{ v }}</code></li>
        </ul>
      </section>

      <section v-if="apiKeyList().length" class="tfo-section">
        <header class="tfo-section-header">
          <h2 class="tfo-section-title">API {{ $t('common.terms.apiKey') }}</h2>
          <span class="tfo-section-count">{{ apiKeyList().length }} 项</span>
        </header>
        <table class="tfo-table">
          <thead>
            <tr><th>key_id</th><th>名称</th></tr>
          </thead>
          <tbody>
            <tr v-for="k in apiKeyList()" :key="k.id">
              <td><code>{{ k.id }}</code></td>
              <td>{{ k.label }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <p v-if="stringSections().length === 0 && apiKeyList().length === 0" class="tfo-empty">
        暂无筛选项
      </p>
    </template>
  </div>
</template>

<style scoped>
.turns-filter-options-view { padding: var(--space-4); }
.tfo-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-3); }
.tfo-title { font-size: var(--text-xl); font-weight: var(--font-semibold); }
.tfo-error { color: var(--danger); padding: var(--space-2); background: var(--danger-bg); border-radius: var(--radius); margin-bottom: var(--space-3); }
.tfo-loading { color: var(--text-muted); padding: var(--space-3); }
.tfo-empty { color: var(--text-muted); padding: var(--space-3); font-style: italic; }
.tfo-section { background: var(--bg-surface); border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-3); margin-bottom: var(--space-3); }
.tfo-section-header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: var(--space-2); }
.tfo-section-title { font-size: var(--text-md); font-weight: var(--font-semibold); }
.tfo-section-count { color: var(--text-muted); font-size: var(--text-sm); }
.tfo-chips { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: var(--space-1); }
.tfo-chip code { font-size: 12px; padding: 2px 8px; background: var(--bg-subtle); border: 1px solid var(--border-muted); border-radius: 4px; }
.tfo-table { width: 100%; border-collapse: collapse; }
.tfo-table th, .tfo-table td { text-align: left; padding: var(--space-1) var(--space-2); border-bottom: 1px solid var(--border-muted); font-size: var(--text-sm); }
.tfo-table th { color: var(--text-muted); font-weight: var(--font-semibold); }
</style>
