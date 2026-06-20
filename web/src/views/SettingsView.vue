<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  listSettings,
  getSetting,
  updateSetting,
  rollbackSetting,
  type SettingItem,
  type SettingSpec,
} from '../api'

const items = ref<SettingItem[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
const selectedKey = ref<string>('')
const selected = ref<SettingSpec | null>(null)
const currentValue = ref<any>(null)
const currentSource = ref<string>('')
const editBuffer = ref<string>('') // JSON text editor
const filterCategory = ref<string>('')

const categories = [
  { key: '',                   label: '全部',  icon: '📋' },
  { key: 'compression',        label: '压缩',  icon: '🗜'  },
  { key: 'rate_limit',         label: '限流',  icon: '🚦'  },
  { key: 'timeout',            label: '超时',  icon: '⏱'  },
  { key: 'routing',            label: '路由',  icon: '🔀'  },
  { key: 'session',            label: '会话',  icon: '💬'  },
  { key: 'security',           label: '安全',  icon: '🔐'  },
  { key: 'circuit_breaker',    label: '熔断',  icon: '⚡'  },
  { key: 'general',            label: '其他',  icon: '⚙️' },
]

function dangerLabel(level: number): string {
  return ['', '🟡 注意', '🟠 警告', '🔴 危险'][level] || ''
}

async function loadList() {
  loading.value = true
  error.value = null
  try {
    const r = await listSettings({ category: filterCategory.value || undefined })
    items.value = r.items
  } catch (e: any) {
    error.value = e.message || '加载失败'
  } finally {
    loading.value = false
  }
}

async function selectKey(key: string) {
  selectedKey.value = key
  try {
    const r = await getSetting(key)
    selected.value = r.spec
    currentValue.value = r.value
    currentSource.value = r.source
    editBuffer.value = JSON.stringify(r.value, null, 2)
  } catch (e: any) {
    error.value = e.message || '加载详情失败'
  }
}

async function save() {
  if (!selectedKey.value || !selected.value) return
  saving.value = true
  error.value = null
  try {
    const parsed = JSON.parse(editBuffer.value)
    await updateSetting(selectedKey.value, { value: parsed })
    await loadList()
    await selectKey(selectedKey.value)
  } catch (e: any) {
    error.value = e.message || '保存失败'
  } finally {
    saving.value = false
  }
}

async function rollback() {
  if (!selectedKey.value) return
  if (!confirm(`确认回滚 ${selectedKey.value} 到上次的值？`)) return
  try {
    await rollbackSetting(selectedKey.value)
    await loadList()
    await selectKey(selectedKey.value)
  } catch (e: any) {
    error.value = e.message || '回滚失败'
  }
}

function switchCategory(key: string) {
  filterCategory.value = key
  loadList()
}

const filteredCount = computed(() => items.value.length)
</script>

<template>
  <div class="settings-view">
    <div v-if="error" class="error-banner">{{ error }}</div>
    <div class="layout">
      <aside class="category-bar">
        <button
          v-for="c in categories" :key="c.key"
          class="cat-btn" :class="{ active: filterCategory === c.key }"
          @click="switchCategory(c.key)"
        >
          <span class="cat-icon">{{ c.icon }}</span>
          <span class="cat-label">{{ c.label }}</span>
        </button>
      </aside>

      <section class="list-pane">
        <div class="list-header">
          <span>共 {{ filteredCount }} 个设置</span>
        </div>
        <div v-if="loading" class="loading">加载中…</div>
        <div v-else-if="!items.length" class="empty">该类别暂无设置</div>
        <table v-else class="settings-table">
          <thead>
            <tr>
              <th>设置</th>
              <th>当前值</th>
              <th>来源</th>
              <th>危险</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="it in items" :key="it.key"
              :class="{ active: selectedKey === it.key }"
              @click="selectKey(it.key)"
            >
              <td class="cell-key">
                <code>{{ it.key }}</code>
                <div class="cell-desc">{{ it.description }}</div>
              </td>
              <td class="cell-value">
                <code>{{ JSON.stringify(it.value) }}</code>
              </td>
              <td>
                <span class="src-badge" :class="'src-' + it.source">{{ it.source || '—' }}</span>
              </td>
              <td>{{ dangerLabel(it.danger_level) }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <aside class="detail-pane" v-if="selected">
        <h3 class="detail-title">
          <code>{{ selected.key }}</code>
        </h3>
        <p class="detail-desc">{{ selected.description }}</p>

        <dl class="meta">
          <dt>类型</dt><dd>{{ selected.type }}</dd>
          <dt>当前值</dt>
          <dd>
            <code class="current-value">{{ JSON.stringify(currentValue) }}</code>
            <span class="src-badge" :class="'src-' + currentSource">{{ currentSource }}</span>
          </dd>
          <dt>默认值</dt><dd><code>{{ JSON.stringify(selected.default) }}</code></dd>
          <dt v-if="selected.options">选项</dt>
          <dd v-if="selected.options">
            <code v-for="o in selected.options" :key="o" class="opt-chip">{{ o }}</code>
          </dd>
          <dt>危险级别</dt><dd>{{ dangerLabel(selected.danger_level) }}</dd>
          <dt>热重载</dt>
          <dd>
            <span v-if="selected.hot_reload" class="src-badge src-db">是</span>
            <span v-else class="src-badge src-default">否（需重启）</span>
          </dd>
          <dt v-if="selected.observability">观察点</dt>
          <dd v-if="selected.observability">
            <a :href="selected.observability" target="_blank" rel="noopener">
              <code>{{ selected.observability }}</code>
            </a>
          </dd>
        </dl>

        <div class="editor">
          <label class="editor-label">新值 (JSON 格式)</label>
          <textarea
            v-model="editBuffer"
            rows="4"
            class="editor-textarea"
            spellcheck="false"
          />
          <div class="editor-actions">
            <button class="btn btn-primary" :disabled="saving" @click="save">
              {{ saving ? '保存中…' : '保存' }}
            </button>
            <button class="btn btn-ghost" @click="rollback">回滚</button>
          </div>
        </div>
      </aside>

      <div v-else class="detail-pane empty-detail">
        <p>← 从左侧选择一个设置查看详情</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* === Dark theme — consistent with rest of app === */
.settings-view {
  padding: 16px;
  max-width: 1400px;
  margin: 0 auto;
  color: var(--text-primary, #e6edf3);
  font-size: 13px;
}
.error-banner {
  padding: 10px;
  background: rgba(248, 113, 113, 0.1);
  border: 1px solid rgba(248, 113, 113, 0.3);
  color: #f87171;
  border-radius: 6px;
  margin-bottom: 12px;
}

.layout {
  display: grid;
  grid-template-columns: 160px 1fr 360px;
  gap: 12px;
  min-height: 70vh;
}

/* === Category bar === */
.category-bar {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.cat-btn {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid transparent;
  background: transparent;
  color: var(--text-secondary, #8b949e);
  border-radius: 6px;
  font-size: 13px;
  cursor: pointer;
  text-align: left;
}
.cat-btn:hover {
  background: var(--bg-hover, #21262d);
}
.cat-btn.active {
  background: var(--bg-card, #161b22);
  color: var(--text-primary, #e6edf3);
  border-color: var(--accent, #6366f1);
}
.cat-icon { font-size: 16px; }

/* === List pane === */
.list-pane {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  overflow: auto;
}
.list-header {
  padding: 8px 12px;
  border-bottom: 1px solid var(--border, #30363d);
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
}
.loading, .empty {
  text-align: center;
  padding: 32px;
  color: var(--text-secondary, #8b949e);
}

.settings-table {
  width: 100%;
  border-collapse: collapse;
}
.settings-table th,
.settings-table td {
  padding: 10px 12px;
  text-align: left;
  border-bottom: 1px solid var(--border, #30363d);
  vertical-align: top;
}
.settings-table th {
  color: var(--text-secondary, #8b949e);
  font-weight: 500;
  background: var(--bg, #0f1117);
  font-size: 12px;
}
.settings-table tr {
  cursor: pointer;
}
.settings-table tr.active td {
  background: rgba(99, 102, 241, 0.1);
}
.settings-table tr:hover:not(.active) td {
  background: var(--bg-hover, #21262d);
}
.cell-key code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  color: var(--accent-h, #818cf8);
}
.cell-desc {
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
}
.cell-value code {
  padding: 1px 6px;
  background: var(--bg, #0f1117);
  border-radius: 3px;
  font-size: 11px;
  font-family: ui-monospace, SFMono-Regular, monospace;
}

/* === Source badges === */
.src-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 500;
  margin-left: 4px;
}
.src-badge.src-db {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}
.src-badge.src-env {
  background: rgba(99, 102, 241, 0.15);
  color: #818cf8;
}
.src-badge.src-default {
  background: rgba(139, 148, 158, 0.15);
  color: #8b949e;
}

/* === Detail pane === */
.detail-pane {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  padding: 16px;
  overflow: auto;
}
.empty-detail {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-secondary, #8b949e);
  font-size: 14px;
}
.detail-title {
  margin: 0 0 6px;
  font-size: 14px;
}
.detail-title code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  color: var(--accent-h, #818cf8);
}
.detail-desc {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  margin: 0 0 12px;
}
.meta {
  display: grid;
  grid-template-columns: 80px 1fr;
  gap: 6px 12px;
  font-size: 12px;
  margin: 0 0 16px;
}
.meta dt {
  color: var(--text-secondary, #8b949e);
}
.meta dd {
  margin: 0;
  color: var(--text-primary, #e6edf3);
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
}
.meta code {
  padding: 1px 6px;
  background: var(--bg, #0f1117);
  border-radius: 3px;
  font-size: 11px;
  font-family: ui-monospace, SFMono-Regular, monospace;
}
.meta a {
  color: var(--accent-h, #818cf8);
  text-decoration: none;
}
.meta a:hover {
  text-decoration: underline;
}
.opt-chip {
  background: var(--bg, #0f1117);
  color: var(--text-secondary, #8b949e);
}
.current-value {
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}

/* === Editor === */
.editor {
  border-top: 1px solid var(--border, #30363d);
  padding-top: 12px;
}
.editor-label {
  display: block;
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  margin-bottom: 6px;
}
.editor-textarea {
  width: 100%;
  padding: 8px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  resize: vertical;
  margin-bottom: 8px;
}
.editor-textarea:focus {
  outline: none;
  border-color: var(--accent, #6366f1);
}
.editor-actions {
  display: flex;
  gap: 8px;
}

/* === Buttons === */
.btn {
  padding: 6px 14px;
  border-radius: 6px;
  font-size: 13px;
  cursor: pointer;
  border: 1px solid transparent;
}
.btn-primary {
  background: var(--accent, #6366f1);
  color: #fff;
}
.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-ghost {
  background: transparent;
  color: var(--text-primary, #e6edf3);
  border: 1px solid var(--border, #30363d);
}
.btn-ghost:hover {
  background: var(--bg-hover, #21262d);
}

/* === Responsive === */
@media (max-width: 1100px) {
  .layout {
    grid-template-columns: 1fr;
  }
  .category-bar {
    flex-direction: row;
    overflow-x: auto;
  }
}
</style>
