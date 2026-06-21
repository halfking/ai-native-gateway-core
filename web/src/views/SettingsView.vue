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
    const resp = await getSetting(key)
    selected.value = resp.spec
    currentValue.value = resp.value
    currentSource.value = resp.source
    
    // Smart initialization based on type
    if (resp.spec.type === 'bool') {
      editBuffer.value = String(resp.value ?? resp.spec.default)
    } else if (resp.spec.type === 'int' || resp.spec.type === 'float' || resp.spec.type === 'string') {
      editBuffer.value = String(resp.value ?? resp.spec.default)
    } else {
      editBuffer.value = JSON.stringify(resp.value ?? resp.spec.default, null, 2)
    }
  } catch (e: any) {
    error.value = e.message || '加载详情失败'
  }
}

async function save() {
  if (!selectedKey.value || !selected.value) return
  saving.value = true
  error.value = null
  try {
    let parsed: any
    // Smart parsing based on type
    if (selected.value.type === 'bool') {
      parsed = editBuffer.value === 'true' || editBuffer.value === true
    } else if (selected.value.type === 'int' || selected.value.type === 'float') {
      parsed = Number(editBuffer.value)
      if (isNaN(parsed)) {
        throw new Error('请输入有效的数字')
      }
    } else if (selected.value.type === 'string') {
      parsed = String(editBuffer.value)
    } else {
      // Fallback to JSON parsing
      parsed = JSON.parse(editBuffer.value)
    }
    
    await updateSetting(selectedKey.value, { value: parsed })
    await loadList()
    await selectKey(selectedKey.value)
  } catch (e: any) {
    error.value = e.message || '保存失败'
  } finally {
    saving.value = false
  }
}

// Helper to get friendly label for enum values
function getEnumLabel(key: string, value: any): string {
  const labels: Record<string, Record<string, string>> = {
    'compression.mode': {
      '0': '关闭 (off)',
      '1': '自动阈值 (auto_threshold)',
      '2': '4xx时压缩 (on_4xx)',
    },
    'compression.strategy': {
      'naive': '朴素压缩',
      'smart': '智能压缩',
      'adaptive': '自适应压缩',
    },
  }
  return labels[key]?.[String(value)] || String(value)
}

// Get description for enum options
function getEnumDescription(key: string, value: string): string {
  const descriptions: Record<string, Record<string, string>> = {
    'compression.mode': {
      '0': '完全关闭消息压缩功能',
      '1': '当消息长度超过context window阈值时自动压缩',
      '2': '收到4xx错误（如context_length_exceeded）时触发压缩',
    },
  }
  return descriptions[key]?.[value] || ''
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
          <code>{{ selectedKey }}</code>
        </h3>
        <p class="detail-desc">{{ selected.description }}</p>
        
        <!-- Additional documentation for specific settings -->
        <div v-if="selectedKey === 'compression.mode'" class="detail-docs">
          <div class="docs-title">📖 详细说明</div>
          <div class="docs-content">
            <p><strong>压缩模式</strong>控制系统如何处理超长对话上下文：</p>
            <ul>
              <li><code>0 (off)</code> - 关闭压缩，当上下文超限时直接返回错误</li>
              <li><code>1 (auto_threshold)</code> - 预判模式，当消息长度接近模型的context window时主动压缩</li>
              <li><code>2 (on_4xx)</code> - 响应式模式，收到4xx错误后压缩并重试【推荐】</li>
            </ul>
            <p class="docs-note">💡 <strong>推荐使用模式2</strong>：仅在必要时压缩，避免不必要的性能开销</p>
          </div>
        </div>
        
        <div v-else-if="selectedKey === 'cache.enabled'" class="detail-docs">
          <div class="docs-title">📖 详细说明</div>
          <div class="docs-content">
            <p><strong>会话缓存</strong>控制是否启用L1/L2/L3三级缓存：</p>
            <ul>
              <li><strong>L1</strong> - 内存缓存（最快）</li>
              <li><strong>L2</strong> - Redis缓存（中等）</li>
              <li><strong>L3</strong> - 数据库缓存（最慢）</li>
            </ul>
            <p class="docs-note">⚠️ 关闭后所有会话状态将不被保存，影响上下文连续性</p>
          </div>
        </div>
        
        <div v-else-if="selectedKey === 'format_conversion.enabled'" class="detail-docs">
          <div class="docs-title">📖 详细说明</div>
          <div class="docs-content">
            <p><strong>格式转换</strong>允许不同协议之间的请求格式自动转换：</p>
            <ul>
              <li><strong>Q2路径</strong>：OpenAI格式 → Anthropic模型</li>
              <li><strong>Q3路径</strong>：Anthropic格式 → OpenAI模型</li>
            </ul>
            <p class="docs-note">💡 支持Provider级别覆盖，可针对特定供应商禁用转换</p>
          </div>
        </div>

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
          <label class="editor-label">新值</label>
          
          <!-- Boolean type: Switch -->
          <div v-if="selected.type === 'bool'" class="editor-boolean">
            <label class="switch-label">
              <input 
                type="checkbox" 
                v-model="editBuffer"
                :true-value="'true'"
                :false-value="'false'"
                class="switch-input"
              />
              <span class="switch-track"></span>
              <span class="switch-text">{{ editBuffer === 'true' ? '启用' : '禁用' }}</span>
            </label>
          </div>
          
          <!-- Enum type with known options: Select -->
          <div v-else-if="selectedKey === 'compression.mode'" class="editor-select">
            <select v-model="editBuffer" class="select-input">
              <option value="0">0 - 关闭 (off)</option>
              <option value="1">1 - 自动阈值 (auto_threshold)</option>
              <option value="2">2 - 4xx时压缩 (on_4xx) 【推荐】</option>
            </select>
            <div class="select-hint">
              <div v-if="editBuffer === '0'" class="hint-item">完全关闭消息压缩功能</div>
              <div v-else-if="editBuffer === '1'" class="hint-item">当消息长度超过context window阈值时自动压缩</div>
              <div v-else-if="editBuffer === '2'" class="hint-item">收到4xx错误（如context_length_exceeded）时触发压缩并重试</div>
            </div>
          </div>
          
          <!-- Number type: Number input -->
          <div v-else-if="selected.type === 'int' || selected.type === 'float'" class="editor-number">
            <input 
              type="number"
              v-model="editBuffer"
              class="number-input"
              :step="selected.type === 'float' ? '0.01' : '1'"
            />
          </div>
          
          <!-- String type: Text input -->
          <div v-else-if="selected.type === 'string'" class="editor-string">
            <input 
              type="text"
              v-model="editBuffer"
              class="text-input"
              placeholder="输入字符串值"
            />
          </div>
          
          <!-- Fallback: JSON textarea -->
          <div v-else class="editor-json">
            <textarea
              v-model="editBuffer"
              rows="4"
              class="editor-textarea"
              spellcheck="false"
              placeholder="输入JSON格式的值"
            />
            <div class="json-hint">复杂类型请使用JSON格式</div>
          </div>
          
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

/* === Smart Editor Styles === */
.editor-boolean {
  padding: 16px 0;
}

.switch-label {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  cursor: pointer;
  user-select: none;
}

.switch-input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}

.switch-track {
  position: relative;
  width: 44px;
  height: 24px;
  background: var(--border);
  border-radius: 12px;
  transition: background 0.2s;
}

.switch-track::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 20px;
  height: 20px;
  background: white;
  border-radius: 50%;
  transition: transform 0.2s;
}

.switch-input:checked + .switch-track {
  background: var(--primary, #3b82f6);
}

.switch-input:checked + .switch-track::after {
  transform: translateX(20px);
}

.switch-text {
  font-size: 14px;
  font-weight: 500;
  color: var(--text);
}

.editor-select, .editor-number, .editor-string, .editor-json {
  margin: 12px 0;
}

.select-input, .number-input, .text-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-secondary);
  color: var(--text);
  font-size: 14px;
  font-family: inherit;
}

.select-input:focus, .number-input:focus, .text-input:focus {
  outline: none;
  border-color: var(--primary, #3b82f6);
  box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.1);
}

.select-hint {
  margin-top: 8px;
  padding: 8px 12px;
  background: rgba(59, 130, 246, 0.05);
  border-left: 3px solid var(--primary, #3b82f6);
  border-radius: 4px;
}

.hint-item {
  font-size: 13px;
  line-height: 1.5;
  color: var(--muted);
}

.json-hint {
  margin-top: 6px;
  font-size: 12px;
  color: var(--muted);
}

/* === Documentation Section === */
.detail-docs {
  margin: 16px 0;
  padding: 16px;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: 8px;
}

.docs-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 12px;
}

.docs-content {
  font-size: 13px;
  line-height: 1.6;
  color: var(--text);
}

.docs-content p {
  margin: 8px 0;
}

.docs-content ul {
  margin: 8px 0;
  padding-left: 20px;
}

.docs-content li {
  margin: 4px 0;
}

.docs-content code {
  padding: 2px 6px;
  background: rgba(99, 102, 241, 0.1);
  border-radius: 3px;
  font-size: 12px;
  font-family: 'Menlo', 'Monaco', 'Courier New', monospace;
}

.docs-note {
  margin-top: 12px;
  padding: 8px 12px;
  background: rgba(59, 130, 246, 0.08);
  border-left: 3px solid var(--primary, #3b82f6);
  border-radius: 4px;
  font-size: 13px;
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
