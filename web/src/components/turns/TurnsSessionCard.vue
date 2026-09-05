<script setup lang="ts">
import type { SessionChildRequest, SessionTurnTreeItem } from '../../api/sessionTurnsTree'
import type { TurnGroupItem, TurnsSessionGroup } from '../../api/turns'
import {
  childOpsLabel,
  formatMs,
  formatTokens,
  relationLabel,
  sessionClient,
  sessionTitle,
  sessionTopic,
  shortSessionId,
  statusLabel,
  summaryMeta,
  taskLabel,
} from './turnsListHelpers'

const props = defineProps<{
  session: TurnsSessionGroup
  expanded: boolean
  selected: boolean
  summaryBusy?: boolean
  assetBusy?: boolean
  assetMsg?: string
  childOps?: SessionTurnTreeItem[]
  childOpsLoading?: boolean
  summaryExpanded?: boolean
}>()

const emit = defineEmits<{
  toggle: []
  select: [boolean]
  openSession: []
  openTurn: [TurnGroupItem]
  openParent: [string]
  resummarize: []
  extractAsset: []
  toggleSummary: []
}>()

function visibleModels(session: TurnsSessionGroup) {
  return (session.models_used || []).slice(0, 3)
}
function hiddenModelCount(session: TurnsSessionGroup) {
  return Math.max(0, (session.models_used || []).length - 3)
}
function opsForTurn(turnNo: number): SessionChildRequest[] {
  const hit = (props.childOps || []).find(t => t.turn_number === turnNo)
  return hit?.child_requests || []
}
</script>

<template>
  <div class="session-card" :class="{ selected }">
    <div class="session-header" role="button" tabindex="0"
      :aria-expanded="expanded"
      @click="emit('toggle')"
      @keydown.enter.prevent="emit('toggle')"
      @keydown.space.prevent="emit('toggle')"
    >
      <div class="session-head-line">
        <input type="checkbox" class="sel" :checked="selected" @click.stop @change="emit('select', ($event.target as HTMLInputElement).checked)" />
        <span class="caret" :class="{ open: expanded }" aria-hidden="true">▸</span>
        <span class="status-badge" :data-state="session.status">{{ statusLabel(session.status) }}</span>
        <span class="session-title" :title="sessionTitle(session)">{{ sessionTitle(session) }}</span>
        <span v-if="sessionTopic(session)" class="session-topic" :title="sessionTopic(session)">主题：{{ sessionTopic(session) }}</span>
        <button v-if="session.parent_session_id" class="ctx-badge parent-link" type="button" @click.stop="emit('openParent', session.parent_session_id!)">
          ↳ {{ relationLabel(session.parent_relation) }} {{ shortSessionId(session.parent_session_id) }}
        </button>
      </div>

      <div v-if="session.summary" class="session-summary" :class="{ clamped: !summaryExpanded }" :title="session.summary">
        {{ session.summary }}
        <span v-if="summaryMeta(session)" class="summary-meta">AI 总结 · {{ summaryMeta(session) }}</span>
        <button v-if="session.summary.length > 120" class="link tiny" type="button" @click.stop="emit('toggleSummary')">
          {{ summaryExpanded ? '收起' : '展开' }}
        </button>
      </div>
      <div v-else class="session-summary muted">暂无会话摘要（未生成总结）</div>

      <div class="session-context">
        <span v-if="session.project_id" class="ctx-badge project">项目 {{ session.project_id }}</span>
        <span v-if="session.task_id" class="ctx-badge task">任务 {{ taskLabel(session.task_id) }}</span>
        <span v-if="session.owner_user" class="ctx-badge owner">用户 {{ session.owner_user }}</span>
        <span v-if="sessionClient(session)" class="ctx-badge client">{{ session.application_code ? '智能体' : '客户端' }} {{ sessionClient(session) }}</span>
        <span v-if="session.api_key_label" class="ctx-badge apikey">API Key {{ session.api_key_label }}</span>
        <span v-if="session.start_time" class="ts">开始 {{ new Date(session.start_time).toLocaleString() }}</span>
        <span v-for="tag in session.user_tags" :key="tag" class="badge tag">#{{ tag }}</span>
      </div>

      <div class="session-meta">
        <span class="badge">{{ session.total_turns }} 轮</span>
        <span class="badge">{{ formatTokens(session.total_tokens) }} tok</span>
        <span class="badge cost">${{ session.total_cost_usd.toFixed(4) }}</span>
        <span class="badge">时长 {{ formatMs(session.duration_ms) }}</span>
        <span v-if="session.failover_count" class="badge warn">failover ×{{ session.failover_count }}</span>
        <span v-if="session.error_count" class="badge error">错误 ×{{ session.error_count }}</span>
        <span v-if="session.compression.applied_count" class="badge compression">压缩 {{ session.compression.applied_count }} 次 · 省 {{ formatTokens(session.compression.tokens_saved) }}</span>
        <span v-for="model in visibleModels(session)" :key="model" class="badge model">{{ model }}</span>
        <span v-if="hiddenModelCount(session)" class="badge model">+{{ hiddenModelCount(session) }}</span>
      </div>

      <div class="ops-bar" @click.stop>
        <span class="ops-label">关联操作</span>
        <span class="ops-item" :class="{ on: !!(session.topic || session.intent) }">主题抽取 {{ session.topic || session.intent ? '已有' : '未做' }}</span>
        <span class="ops-item" :class="{ on: !!session.summary }">会话总结 {{ session.summary ? '已有' : '未做' }}</span>
        <span class="ops-item" :class="{ on: !!session.compression.applied_count }">压缩 {{ session.compression.applied_count || 0 }} 次</span>
        <button class="btn-mini" type="button" :disabled="summaryBusy" @click="emit('resummarize')">{{ summaryBusy ? '归纳中…' : '重新归纳' }}</button>
        <button class="btn-mini accent" type="button" :disabled="assetBusy" @click="emit('extractAsset')">{{ assetBusy ? '沉淀中…' : '沉淀为资产' }}</button>
        <span v-if="assetMsg" class="ops-msg">{{ assetMsg }}</span>
      </div>

      <div class="session-foot">
        <span class="ts">更新 {{ new Date(session.updated_at).toLocaleString() }}</span>
        <button class="link" type="button" @click.stop="emit('openSession')">进入会话详情 →</button>
      </div>
    </div>

    <div v-if="expanded" class="turns-block">
      <div v-if="childOpsLoading" class="ops-hint">正在加载关联子操作…</div>
      <div v-if="!session.turns.length" class="empty">该会话暂无匹配轮次</div>
      <div
        v-for="turn in session.turns"
        :key="`${session.session_id}-${turn.turn_no}`"
        class="turn-row"
        role="button"
        tabindex="0"
        @click="emit('openTurn', turn)"
        @keydown.enter.prevent="emit('openTurn', turn)"
      >
        <div class="col col-req">
          <div class="meta">
            <span class="turn-no">#{{ turn.turn_no }}</span>
            <span class="ts">{{ new Date(turn.ts).toLocaleString() }}</span>
            <span v-if="turn.attempt_no" class="mini-badge warn">failover#{{ turn.attempt_no }}</span>
            <span :class="['verdict', `tag-${turn.injection_verdict || 'skip'}`]">inj: {{ turn.injection_verdict || 'skip' }}</span>
            <span v-if="turn.latency_ms !== undefined" class="ts">⏱ {{ formatMs(turn.latency_ms) }}</span>
          </div>
          <div class="preview" :title="turn.title || '(无请求摘要)'">{{ turn.title || '(无请求摘要)' }}</div>
          <div class="badges">
            <span class="badge">req {{ formatTokens(turn.request_tokens) }}</span>
            <span v-if="turn.cache_read_tokens" class="badge cache">读缓存 {{ formatTokens(turn.cache_read_tokens) }}</span>
            <span v-if="turn.cache_write_tokens" class="badge cache">写缓存 {{ formatTokens(turn.cache_write_tokens) }}</span>
            <span v-if="turn.attachment_count" class="badge">附件 {{ turn.attachment_count }}</span>
            <span v-if="turn.submit_mode === 'delta'" class="badge submit">delta</span>
            <span class="badge model">{{ turn.model || '未指定模型' }}</span>
            <span v-if="turn.provider" class="badge">{{ turn.provider }}</span>
          </div>
        </div>
        <div class="col col-resp">
          <div class="meta">
            <span :class="['verdict', `tag-${turn.output_verdict || 'skip'}`]">out: {{ turn.output_verdict || 'skip' }}</span>
            <span v-if="turn.compression_applied" class="badge compression">
              已压缩{{ turn.compression_strategy ? `·${turn.compression_strategy}` : '' }}
              <span v-if="turn.compression_tokens_saved !== undefined"> 省{{ formatTokens(turn.compression_tokens_saved) }}</span>
            </span>
          </div>
          <div class="preview" :title="turn.summary || '(无回复摘要)'">{{ turn.summary || '(无回复摘要)' }}</div>
          <div class="badges">
            <span class="badge">resp {{ formatTokens(turn.response_tokens) }}</span>
            <span class="badge cost">${{ turn.cost_usd.toFixed(4) }}</span>
            <span class="badge status" :data-ok="turn.status_code < 400 ? 'true' : 'false'">{{ turn.status_code }}</span>
            <span v-if="!turn.success && turn.error_kind" class="badge error">{{ turn.error_kind.slice(0, 24) }}</span>
          </div>
          <div v-if="opsForTurn(turn.turn_no).length" class="child-ops">
            <span v-for="op in opsForTurn(turn.turn_no)" :key="op.request_id" class="ops-chip">
              {{ childOpsLabel(op.request_type) }} · {{ op.status }}{{ op.latency != null ? ` · ${formatMs(op.latency)}` : '' }}
            </span>
          </div>
        </div>
      </div>
      <div v-if="session.turns.length > 50" class="ops-hint">轮次较多，完整时间线请进入会话详情查看。</div>
    </div>
  </div>
</template>

<style scoped>
.session-card { border: 1px solid var(--border); border-radius: 8px; background: var(--surface-primary); overflow: hidden; }
.session-card.selected { border-color: var(--accent); }
.session-header { padding: 12px 16px; cursor: pointer; outline: none; }
.session-header:hover, .turn-row:hover { background: var(--bg-hover); }
.session-header:focus-visible, .turn-row:focus-visible, .link:focus-visible, .btn-mini:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.session-head-line, .session-context, .session-meta, .badges, .meta { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.sel { margin: 0; }
.caret { display: inline-block; transition: transform .15s; color: var(--text-muted); }
.caret.open { transform: rotate(90deg); }
.status-badge, .verdict, .badge, .ctx-badge, .mini-badge, .ops-chip { padding: 2px 6px; border-radius: 4px; font-size: 11px; }
.status-badge { font-weight: 600; }
.status-badge[data-state="active"] { background: var(--success-soft); color: var(--success); }
.status-badge[data-state="closed"] { background: var(--primary-soft); color: var(--accent); }
.status-badge[data-state="deleted"] { background: var(--danger-soft); color: var(--danger); }
.session-title { font-size: 15px; font-weight: 600; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 42%; }
.session-topic { color: var(--accent); background: var(--primary-soft); }
.session-summary { margin: 8px 0; color: var(--text-secondary); line-height: 1.5; }
.session-summary.clamped { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-summary.muted, .ts { color: var(--text-muted); }
.summary-meta { margin-left: 8px; color: var(--text-muted); font-size: 11px; }
.session-context { margin-bottom: 8px; font-size: 12px; }
.ctx-badge { color: var(--text-secondary); background: var(--surface-secondary); }
.ctx-badge.project { background: var(--warning-soft); color: var(--warning); }
.ctx-badge.task, .ctx-badge.owner, .ctx-badge.apikey, .badge.model, .badge.cache, .badge.compression, .badge.tag { background: var(--primary-soft); color: var(--accent); }
.ctx-badge.client, .badge.submit { background: var(--success-soft); color: var(--success); }
.parent-link, .link { cursor: pointer; }
.session-foot { display: flex; justify-content: space-between; align-items: center; margin-top: 8px; }
.link { color: var(--accent); background: transparent; border: 0; padding: 0; }
.link.tiny { font-size: 12px; margin-left: 8px; }
.ops-bar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: 8px; padding: 8px 10px; border-radius: 6px; background: var(--surface-secondary); }
.ops-label { font-size: 12px; color: var(--text-muted); font-weight: 600; }
.ops-item { font-size: 12px; color: var(--text-muted); }
.ops-item.on { color: var(--success); }
.ops-msg { font-size: 12px; color: var(--accent); }
.btn-mini { height: 26px; padding: 0 10px; border-radius: 4px; border: 1px solid var(--border); background: var(--surface-primary); color: var(--text-primary); cursor: pointer; font-size: 12px; }
.btn-mini.accent { background: var(--accent); border-color: var(--accent); color: white; }
.btn-mini:disabled { opacity: .55; cursor: not-allowed; }
.turns-block { border-top: 1px solid var(--border); background: var(--surface-secondary); }
.turn-row { display: grid; grid-template-columns: 1fr 1fr; padding: 10px 16px; cursor: pointer; background: var(--surface-primary); border-bottom: 1px solid var(--border); }
.col { padding: 0 8px; }
.col + .col { border-left: 1px dashed var(--border); }
.preview { margin-bottom: 6px; color: var(--text-secondary); overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; line-height: 1.4; }
.badge { background: var(--surface-secondary); color: var(--text-secondary); }
.badge.warn, .mini-badge.warn { background: var(--warning-soft); color: var(--warning); }
.badge.error { background: var(--danger-soft); color: var(--danger); }
.badge.cost { background: var(--warning-soft); color: var(--warning); }
.badge.status[data-ok="true"] { background: var(--success-soft); color: var(--success); }
.badge.status[data-ok="false"] { background: var(--danger-soft); color: var(--danger); }
.tag-pass { background: var(--success-soft); color: var(--success); }
.tag-warn { background: var(--warning-soft); color: var(--warning); }
.tag-block { background: var(--danger-soft); color: var(--danger); }
.tag-skip { background: var(--surface-secondary); color: var(--text-muted); }
.child-ops { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 6px; }
.ops-chip { background: var(--primary-soft); color: var(--accent); }
.ops-hint, .empty { text-align: center; color: var(--text-secondary); padding: 12px 16px; font-size: 13px; }
@media (max-width: 760px) {
  .turn-row { grid-template-columns: 1fr; gap: 10px; }
  .col + .col { padding-top: 10px; border-left: 0; border-top: 1px dashed var(--border); }
  .session-title { max-width: 100%; }
  .session-foot { flex-direction: column; align-items: flex-start; gap: 6px; }
}
</style>
