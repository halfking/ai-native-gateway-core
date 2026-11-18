<template>
  <div class="pi-analysis-view">
    <div class="header">
 <h1>{{ t('common.piAnalysis.page.title') }}</h1>
      <div class="actions">
        <button @click="triggerAnalysis" class="btn-primary" :disabled="isTriggering">
          {{ isTriggering ? t('common.piAnalysis.page.triggering') : t('common.piAnalysis.page.triggerManual') }}
        </button>
        <button @click="refreshData" class="btn-secondary">
           {{ t('common.piAnalysis.page.refresh') }}
        </button>
      </div>
    </div>

    <!--
      ★ 2026-11-18 审计轮 P2：API 不可用时**如实说明**，不再是一张空表。
      后端 /api/admin/pi-analysis/* 只在「有 DB + admin 鉴权材料齐备」时
      注册（fail-closed）⇒ 无 DB / 未配 secret 的部署上这些请求全是 404，
      而页面此前只 console.error ⇒ 运维看到的是「空队列」，分不清
      「没数据」与「功能不存在」。
    -->
    <div class="unavailable-banner" v-if="unavailable">
      <strong>{{ t('common.piAnalysis.unavailableTitle') }}</strong>
      <span>{{ unavailableReason }}</span>
    </div>

    <!-- Chain-not-wired notice: the queue is empty because there is no producer. -->
    <div class="chain-note-banner" v-if="!unavailable && chainNotWired">
      <strong>{{ t('common.piAnalysis.chainNotWiredTitle') }}</strong>
      <span>{{ t('common.piAnalysis.chainNotWiredReason') }}</span>
    </div>

    <!--
      统计卡片（2026-11-19 列表↔统计一致性根修）：
      六张卡全部与列表同域——总数/待分析/已分析/失败来自队列全量（= 错误队列 tab 的域），
      误报率/平均耗时来自结果表全量（= 分析结果 tab 的域）。
      总数卡子行披露 处理中/跳过（瞬态态不在卡面单列，但 Σ 全部状态 = 总数 ⇒ 可对账）。
    -->
    <div class="stats-cards" v-if="stats">
      <div class="stat-card">
        <div class="stat-label">{{ t('common.piAnalysis.stats.total') }}</div>
        <div class="stat-value">{{ stats.total }}</div>
        <div class="stat-sub" v-if="stats.processing || stats.skipped">
          {{ t('common.piAnalysis.stats.subTransient', { processing: stats.processing, skipped: stats.skipped }) }}
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('common.piAnalysis.stats.pending') }}</div>
        <div class="stat-value">{{ stats.pending }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('common.piAnalysis.stats.analyzed') }}</div>
        <div class="stat-value">{{ stats.analyzed }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('common.piAnalysis.stats.failed') }}</div>
        <div class="stat-value">{{ stats.failed }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('common.piAnalysis.stats.falsePositiveRate') }}</div>
        <div class="stat-value">{{ stats.falsePositiveRate }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('common.piAnalysis.stats.avgDuration') }}</div>
        <div class="stat-value">{{ stats.avgDuration }}</div>
      </div>
    </div>

    <!-- Tab 切换 -->
    <div class="tabs">
      <button 
        :class="['tab', { active: activeTab === 'queue' }]"
        @click="activeTab = 'queue'"
      >
         {{ t('common.piAnalysis.page.tabQueue') }}
      </button>
      <button 
        :class="['tab', { active: activeTab === 'results' }]"
        @click="activeTab = 'results'"
      >
        {{ t('common.piAnalysis.page.tabResults') }}
      </button>
    </div>

    <!-- 错误队列表格 -->
    <div v-if="activeTab === 'queue'" class="table-container">
      <div class="filters">
        <select v-model="queueStatus" @change="loadQueue">
          <option value="">{{ t('common.piAnalysis.page.filterAllStatus') }}</option>
          <option value="pending">{{ t('common.piAnalysis.page.statusPending') }}</option>
          <option value="processing">{{ t('common.piAnalysis.page.statusProcessing') }}</option>
          <option value="analyzed">{{ t('common.piAnalysis.page.statusAnalyzed') }}</option>
          <option value="failed">{{ t('common.piAnalysis.page.statusFailed') }}</option>
        </select>
      </div>

      <table class="data-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>{{ t('common.piAnalysis.page.colErrorType') }}</th>
            <th>{{ t('common.piAnalysis.page.colPriority') }}</th>
            <th>{{ t('common.piAnalysis.page.colTenant') }}</th>
            <th>{{ t('common.piAnalysis.page.colErrorMessage') }}</th>
            <th>{{ t('common.piAnalysis.page.colStatus') }}</th>
            <th>{{ t('common.piAnalysis.page.colRetryCount') }}</th>
            <th>{{ t('common.piAnalysis.page.colCreatedAt') }}</th>
            <th>{{ t('common.piAnalysis.page.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="error in queueData" :key="error.id" class="clickable-row" @click="openDetail(error.id)">
            <td>{{ error.id }}</td>
            <td>
              <span class="badge" :class="getErrorTypeClass(error.error_type)">
                {{ formatErrorType(error.error_type) }}
              </span>
            </td>
            <td>
              <span class="priority" :class="getPriorityClass(error.priority)">
                {{ error.priority }}
              </span>
            </td>
            <td>{{ error.tenant_id }}</td>
            <td class="error-message">{{ error.error_message }}</td>
            <td>
              <span class="status" :class="getStatusClass(error.status)">
                {{ formatStatus(error.status) }}
              </span>
            </td>
            <td>{{ error.retry_count }}</td>
            <td>{{ formatTime(error.created_at) }}</td>
            <td>
              <button class="btn-link">
                {{ t('common.piAnalysis.detail.rowHint') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="pagination">
        <button @click="prevQueuePage" :disabled="queueOffset === 0">{{ t('common.piAnalysis.page.prevPage') }}</button>
        <span>{{ queueOffset / queueLimit + 1 }} / {{ Math.ceil(queueTotal / queueLimit) }}</span>
        <button @click="nextQueuePage" :disabled="queueOffset + queueLimit >= queueTotal">{{ t('common.piAnalysis.page.nextPage') }}</button>
      </div>
    </div>

    <!-- 分析结果表格 -->
    <div v-if="activeTab === 'results'" class="table-container">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('common.piAnalysis.page.colAnalysisId') }}</th>
            <th>{{ t('common.piAnalysis.page.colErrorType') }}</th>
            <th>{{ t('common.piAnalysis.page.colRootCause') }}</th>
            <th>{{ t('common.piAnalysis.page.colFalsePositive') }}</th>
            <th>{{ t('common.piAnalysis.page.colConfidence') }}</th>
            <th>{{ t('common.piAnalysis.page.colSeverity') }}</th>
            <th>{{ t('common.piAnalysis.page.colSuggestedFix') }}</th>
            <th>{{ t('common.piAnalysis.page.colCreatedAt') }}</th>
            <th>{{ t('common.piAnalysis.page.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="result in resultsData" :key="result.id">
            <td>{{ result.analysis_id.slice(0, 8) }}...</td>
            <td>
              <span class="badge" :class="getErrorTypeClass(result.error_type)">
                {{ formatErrorType(result.error_type) }}
              </span>
            </td>
            <td class="root-cause">{{ result.root_cause }}</td>
            <td>
              <span :class="result.is_false_positive ? 'badge-warning' : 'badge-success'">
                {{ result.is_false_positive ? t('common.piAnalysis.page.yes') : t('common.piAnalysis.page.no') }}
              </span>
            </td>
            <td>{{ (result.confidence_score * 100).toFixed(0) }}%</td>
            <td>
              <span class="severity" :class="getSeverityClass(result.severity_level)">
                {{ formatSeverity(result.severity_level) }}
              </span>
            </td>
            <td class="suggested-fix">{{ result.suggested_fix }}</td>
            <td>{{ formatTime(result.created_at) }}</td>
            <td>
              <button @click="viewDetail(result.queue_id)" class="btn-link">
                {{ t('common.piAnalysis.page.detailButton') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="pagination">
        <button @click="prevResultsPage" :disabled="resultsOffset === 0">{{ t('common.piAnalysis.page.prevPage') }}</button>
        <span>{{ resultsOffset / resultsLimit + 1 }} / {{ Math.ceil(resultsTotal / resultsLimit) }}</span>
        <button @click="nextResultsPage" :disabled="resultsOffset + resultsLimit >= resultsTotal">{{ t('common.piAnalysis.page.nextPage') }}</button>
      </div>
    </div>

    <!-- 详情对话框：前因 / 分析 / 建议 / 操作 四区（2026-11-18 PI 分析管理面优化轮） -->
    <div v-if="showDetailDialog" class="dialog-overlay" @click="closeDetail">
      <div class="dialog detail-dialog" @click.stop>
        <div class="dialog-header">
          <h2>#{{ activeDetailId }} · {{ formatErrorType(activeContext?.error_type || '') }}</h2>
          <button @click="closeDetail" class="btn-close" aria-label="close">×</button>
        </div>
        <div class="dialog-content">
          <div v-if="detailLoading" class="detail-empty">…</div>
          <div v-else-if="detailError" class="detail-error">
            {{ t('common.piAnalysis.detail.loadFailed') }}：{{ detailError }}
          </div>
          <template v-else>

            <!-- ── 时间线 ─────────────────────────────────────────── -->
            <div class="detail-section">
              <h3>{{ t('common.piAnalysis.detail.timelineTitle') }}</h3>
              <div class="timeline">
                <div class="timeline-step">
                  <div class="timeline-dot done"></div>
                  <div class="timeline-label">{{ t('common.piAnalysis.detail.timelineQueued') }}</div>
                  <!-- results 详情里队列入队时间叫 queued_at（created_at 属于分析侧）；queue 详情里叫 created_at -->
                  <div class="timeline-time">{{ formatTime(queuedAt) }}</div>
                </div>
                <div class="timeline-bar" :class="{ done: !!activeContext?.started_at }"></div>
                <div class="timeline-step">
                  <div class="timeline-dot" :class="{ done: !!activeContext?.started_at }"></div>
                  <div class="timeline-label">{{ t('common.piAnalysis.detail.timelineStarted') }}</div>
                  <div class="timeline-time">{{ activeContext?.started_at ? formatTime(activeContext.started_at) : '—' }}</div>
                </div>
                <div class="timeline-bar" :class="{ done: !!activeContext?.completed_at }"></div>
                <div class="timeline-step">
                  <div class="timeline-dot" :class="{ done: !!activeContext?.completed_at }"></div>
                  <div class="timeline-label">{{ t('common.piAnalysis.detail.timelineCompleted') }}</div>
                  <div class="timeline-time">{{ activeContext?.completed_at ? formatTime(activeContext.completed_at) : '—' }}</div>
                </div>
              </div>
              <div v-if="detailData?.analysis_duration_ms" class="timeline-duration">
                {{ t('common.piAnalysis.detail.timelineDuration', { ms: detailData.analysis_duration_ms }) }}
              </div>
            </div>

            <!-- ── 前因 · 错误发生时 ───────────────────────────────── -->
            <div class="detail-section">
              <h3>{{ t('common.piAnalysis.detail.contextTitle') }}</h3>
              <div class="detail-grid">
                <div class="detail-item">
                  <label>{{ t('common.piAnalysis.detail.priorityLabel') }}:</label>
                  <span class="priority" :class="getPriorityClass(activeContext?.priority || 0)">{{ activeContext?.priority }}</span>
                </div>
                <div class="detail-item">
                  <label>{{ t('common.piAnalysis.detail.statusLabel') }}:</label>
                  <span class="status" :class="getStatusClass(activeContext?.status || '')">{{ formatStatus(activeContext?.status || '') }}</span>
                </div>
                <div class="detail-item">
                  <label>{{ t('common.piAnalysis.detail.retryLabel', { cur: activeContext?.retry_count ?? 0, max: activeContext?.max_retries ?? 3 }) }}</label>
                </div>
                <div class="detail-item">
                  <label>{{ t('common.piAnalysis.detail.errorHashLabel') }}:</label>
                  <code class="hash-code">{{ activeContext?.error_hash }}</code>
                </div>
                <div class="detail-item">
                  <label>{{ t('common.piAnalysis.detail.sessionLabel') }}:</label>
                  <span>{{ activeContext?.session_id || t('common.piAnalysis.detail.noSession') }}</span>
                </div>
                <div class="detail-item">
                  <label>{{ t('common.piAnalysis.detail.requestLabel') }}:</label>
                  <span>{{ activeContext?.request_id || t('common.piAnalysis.detail.noSession') }}</span>
                </div>
              </div>

              <div class="detail-sub">
                <h4>{{ t('common.piAnalysis.detail.errorMessageTitle') }}</h4>
                <pre>{{ activeContext?.error_message }}</pre>
              </div>
              <div class="detail-sub" v-if="hasJSON(activeContext?.error_details)">
                <h4>{{ t('common.piAnalysis.detail.errorDetailsTitle') }}</h4>
                <pre>{{ prettyJSON(activeContext?.error_details) }}</pre>
              </div>
              <div class="detail-sub" v-if="activeContext?.input_text">
                <h4>{{ t('common.piAnalysis.detail.inputTextTitle') }}</h4>
                <div class="untrusted-note">{{ t('common.piAnalysis.detail.inputTextWarning') }}</div>
                <pre class="untrusted-input">{{ activeContext.input_text }}</pre>
              </div>
              <div class="detail-sub" v-if="matchedRules.length">
                <h4>{{ t('common.piAnalysis.detail.matchedRulesTitle') }}</h4>
                <div class="rule-card" v-for="mr in matchedRules" :key="mr.rule_name">
                  <div class="rule-card-head">
                    <span class="badge badge-info">{{ mr.rule_name }}</span>
                    <span class="severity severity-high">{{ t('common.piAnalysis.detail.matchedRuleSeverity', { sev: mr.severity ?? '?' }) }}</span>
                  </div>
                  <pre class="rule-pattern">{{ mr.pattern }}</pre>
                </div>
              </div>
              <div class="detail-sub" v-else>
                <h4>{{ t('common.piAnalysis.detail.matchedRulesTitle') }}</h4>
                <p class="muted">{{ t('common.piAnalysis.detail.matchedRulesEmpty') }}</p>
              </div>
              <div class="detail-sub" v-if="hasJSON(activeContext?.detection_layers)">
                <h4>{{ t('common.piAnalysis.detail.detectionLayersTitle') }}</h4>
                <pre>{{ prettyJSON(activeContext?.detection_layers) }}</pre>
              </div>
              <div class="detail-sub" v-if="hasJSON(activeContext?.policy_config)">
                <h4>{{ t('common.piAnalysis.detail.policyTitle') }}</h4>
                <pre>{{ prettyJSON(activeContext?.policy_config) }}</pre>
              </div>
            </div>

            <!-- ── 分析 · 结论 ─────────────────────────────────────── -->
            <div class="detail-section">
              <h3>{{ t('common.piAnalysis.detail.analysisTitle') }}</h3>
              <template v-if="detailData">
                <div class="detail-grid">
                  <div class="detail-item">
                    <label>{{ t('common.piAnalysis.detail.falsePositiveYes') }}:</label>
                    <span :class="detailData.is_false_positive ? 'badge-warning' : 'badge-success'">
                      {{ detailData.is_false_positive ? t('common.piAnalysis.detail.falsePositiveYes') : t('common.piAnalysis.detail.falsePositiveNo') }}
                    </span>
                  </div>
                  <div class="detail-item">
                    <label>{{ t('common.piAnalysis.detail.confidenceLabel') }}:</label>
                    <span>{{ (detailData.confidence_score * 100).toFixed(0) }}%</span>
                  </div>
                  <div class="detail-item">
                    <label>{{ t('common.piAnalysis.detail.severityLabel') }}:</label>
                    <span class="severity" :class="getSeverityClass(detailData.severity_level)">
                      {{ formatSeverity(detailData.severity_level) }}
                    </span>
                  </div>
                  <div class="detail-item">
                    <label>{{ t('common.piAnalysis.detail.impactLabel') }}:</label>
                    <span>{{ formatImpact(detailData.impact_scope) }}</span>
                  </div>
                  <div class="detail-item">
                    <label>{{ t('common.piAnalysis.detail.affectedUsersLabel') }}:</label>
                    <span>{{ detailData.affected_users_estimate }}</span>
                  </div>
                  <div class="detail-item">
                    <label>{{ t('common.piAnalysis.detail.categoryLabel') }}:</label>
                    <span>{{ detailData.error_category }}</span>
                  </div>
                  <div class="detail-item" v-if="detailData.analyzer_model">
                    <label>{{ t('common.piAnalysis.detail.analyzerLabel') }}:</label>
                    <span>{{ detailData.analyzer_model }}{{ detailData.analyzer_version ? ` (${detailData.analyzer_version})` : '' }}</span>
                  </div>
                  <div class="detail-item" v-if="detailData.pattern_frequency">
                    {{ t('common.piAnalysis.detail.patternFreqLabel', { n: detailData.pattern_frequency }) }}
                  </div>
                </div>
                <div class="detail-sub">
                  <h4>{{ t('common.piAnalysis.detail.rootCauseTitle') }}</h4>
                  <p>{{ detailData.root_cause }}</p>
                </div>
                <div class="detail-sub" v-if="hasJSON(detailData.analysis_details)">
                  <h4>{{ t('common.piAnalysis.detail.analysisDetailsTitle') }}</h4>
                  <pre>{{ prettyJSON(detailData.analysis_details) }}</pre>
                </div>
                <div class="detail-sub" v-if="hasJSON(detailData.similar_cases)">
                  <h4>{{ t('common.piAnalysis.detail.similarCasesTitle') }}</h4>
                  <pre>{{ prettyJSON(detailData.similar_cases) }}</pre>
                </div>
                <div class="detail-sub" v-if="detailData.llm_response_raw">
                  <h4>{{ t('common.piAnalysis.detail.rawResponseTitle') }}</h4>
                  <pre class="collapsible">{{ detailData.llm_response_raw }}</pre>
                </div>
              </template>
              <p v-else class="muted">{{ t('common.piAnalysis.detail.notAnalyzedYet') }}</p>
            </div>

            <!-- ── 修正建议 ────────────────────────────────────────── -->
            <div class="detail-section" v-if="detailData">
              <h3>{{ t('common.piAnalysis.detail.fixTitle') }}</h3>
              <div class="detail-sub" v-if="detailData.suggested_fix">
                <h4>{{ t('common.piAnalysis.detail.suggestedFixTitle') }}</h4>
                <p>{{ detailData.suggested_fix }}</p>
              </div>
              <div class="detail-sub">
                <h4>{{ t('common.piAnalysis.detail.ruleAdjustmentTitle') }}</h4>
                <template v-if="normalizedActions.length">
                  <div class="rule-card action-card" v-for="(a, i) in normalizedActions" :key="i">
                    <div class="rule-card-head">
                      <span class="badge badge-warning">{{ actionLabel(a.action) }}</span>
                      <span class="action-target">
                        {{ t('common.piAnalysis.detail.targetRuleLabel') }}:
                        <code>{{ a.rule_name }}</code>
                      </span>
                    </div>
                    <div class="action-params" v-if="a.action === 'adjust_severity'">
                      {{ t('common.piAnalysis.detail.newValueLabel') }}: severity = {{ a.severity }}
                    </div>
                    <pre class="rule-pattern" v-if="a.action === 'update_pattern'">{{ a.pattern }}</pre>
                    <div class="action-reason" v-if="a.reason">{{ t('common.piAnalysis.detail.reasonLabel') }}: {{ a.reason }}</div>
                  </div>
                </template>
                <p v-else class="muted">{{ t('common.piAnalysis.detail.noExecutableAction') }}</p>
              </div>
              <div class="detail-sub" v-if="hasJSON(detailData.code_changes)">
                <h4>{{ t('common.piAnalysis.detail.codeChangesTitle') }}</h4>
                <pre>{{ prettyJSON(detailData.code_changes) }}</pre>
              </div>
            </div>

            <!-- ── 操作区 ──────────────────────────────────────────── -->
            <div class="detail-section actions-section" v-if="!detailError">
              <template v-if="!detailData">
                <button class="btn-primary" :disabled="isTriggering" @click="triggerAnalysis">
                  {{ t('common.piAnalysis.detail.triggerForThis') }}
                </button>
              </template>
              <template v-else-if="!detailData.auto_applied">
                <div class="view-only-note" v-if="!canApply && lowConfidenceReason">
                  {{ lowConfidenceReason }}
                </div>
                <button class="btn-primary" :disabled="!canApply || applying" @click="applyFix">
                  {{ applying ? '…' : t('common.piAnalysis.detail.applyButton') }}
                </button>
              </template>
              <template v-else>
                <span class="badge badge-success">{{ t('common.piAnalysis.detail.appliedBadge') }}</span>
                <span class="muted">
                  {{ t('common.piAnalysis.detail.appliedAtLabel') }}: {{ detailData.applied_at ? formatTime(detailData.applied_at) : '—' }}
                  · {{ t('common.piAnalysis.detail.appliedByLabel') }}: {{ detailData.applied_by || '—' }}
                </span>
                <div class="detail-sub" v-if="appliedRecords.length">
                  <div class="rule-card" v-for="(rec, i) in appliedRecords" :key="i">
                    <div class="rule-card-head">
                      <span class="badge badge-info">{{ actionLabel(rec.action) }}</span>
                      <code>{{ rec.rule_name }}</code>
                    </div>
                    <div class="action-params">
                      {{ t('common.piAnalysis.detail.oldValueLabel') }}:
                      <code>{{ JSON.stringify(rec.old) }}</code>
                    </div>
                  </div>
                </div>
                <button class="btn-secondary danger" :disabled="reverting" @click="revertFix">
                  {{ t('common.piAnalysis.detail.revertButton') }}
                </button>
              </template>
            </div>

          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { summarizePiAnalysisStats } from '@/lib/piAnalysisStats'

const { t } = useI18n()

// 状态管理
const activeTab = ref<'queue' | 'results'>('queue')
const queueStatus = ref('')
const isTriggering = ref(false)
const showDetailDialog = ref(false)

// 数据
const stats = ref<any>(null)
const queueData = ref<any[]>([])
const resultsData = ref<any[]>([])
const detailData = ref<any>(null)

// ★ 2026-11-18 PI 分析管理面优化轮：详情四区状态。
//   detailData = /results/{id} 的全量（含分析结论 + 建议 + 应用状态）；
//   queueDetail = /queue/{id} 的全量（未分析时的「前因」上下文）。
//   activeDetailId = 当前打开的 queue id（标题/操作用）。
const queueDetail = ref<any>(null)
const activeDetailId = ref<number>(0)
const detailLoading = ref(false)
const detailError = ref('')
const applying = ref(false)
const reverting = ref(false)

// activeContext：分析结果与队列详情都带前因上下文，优先取 results（较新）。
const activeContext = computed<any>(() => detailData.value || queueDetail.value)

// queuedAt：入队时间——results 详情序列化为 queued_at，queue 详情是 created_at。
const queuedAt = computed<string>(() =>
  (detailData.value?.queued_at as string) || (queueDetail.value?.created_at as string) || '')

// matchedRules：array 或单对象都兼容（后端列是 JSONB，两种形态都见过）。
const matchedRules = computed<any[]>(() => {
  const raw = activeContext.value?.matched_rules
  if (!raw) return []
  if (Array.isArray(raw)) return raw
  if (typeof raw === 'object') return [raw]
  return []
})

// normalizedActions：与后端 NormalizeRuleAdjustment 同一形状识别（前端展示用；
// 真正的应用仍以后端归一化 + 白名单为准——前端这一份只负责「展示将要做什么」）。
const normalizedActions = computed<any[]>(() => normalizeActions(detailData.value?.rule_adjustment))

const appliedRecords = computed<any[]>(() => {
  const raw = detailData.value?.applied_actions
  if (!raw) return []
  try {
    const parsed = typeof raw === 'string' ? JSON.parse(raw) : raw
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
})

const CONFIDENCE_FLOOR = 0.8

const canApply = computed(() =>
  !!detailData.value &&
  !detailData.value.auto_applied &&
  normalizedActions.value.length > 0 &&
  (detailData.value.confidence_score ?? 0) >= CONFIDENCE_FLOOR,
)

const lowConfidenceReason = computed(() => {
  if (!detailData.value) return ''
  const conf = detailData.value.confidence_score ?? 0
  if (conf < CONFIDENCE_FLOOR) {
    return t('common.piAnalysis.detail.viewOnlyLowConfidence', { conf: (conf * 100).toFixed(0) + '%' })
  }
  return ''
})

function normalizeActions(raw: any): any[] {
  if (!raw) return []
  let top: any
  try {
    top = typeof raw === 'string' ? JSON.parse(raw) : raw
  } catch {
    return []
  }
  const out: any[] = []
  const pushOne = (m: Record<string, any>) => {
    const ruleName = strOf(m, ['rule_name', 'rule', 'name', 'ruleName'])
    const action = actionOf(strOf(m, ['action', 'type', 'operation']))
    if (!ruleName || !action) return
    out.push({
      action,
      rule_name: ruleName,
      severity: numOf(m, ['severity', 'new_severity']),
      pattern: strOf(m, ['pattern', 'new_pattern']) || undefined,
      reason: strOf(m, ['reason', 'rationale', 'why']) || undefined,
    })
  }
  if (Array.isArray(top)) {
    top.forEach((item) => item && typeof item === 'object' && pushOne(item))
    return out
  }
  if (top && typeof top === 'object') {
    if (top.action) {
      pushOne(top)
    }
    if (Array.isArray(top.disable)) {
      top.disable.forEach((name: any) => {
        if (typeof name === 'string' && name.trim()) {
          out.push({ action: 'disable_rule', rule_name: name.trim() })
        }
      })
    }
    if (Array.isArray(top.rules)) {
      top.rules.forEach((m: any) => m && typeof m === 'object' && pushOne(m))
    }
  }
  return out
}

function strOf(m: Record<string, any>, keys: string[]): string {
  for (const k of keys) {
    const v = m?.[k]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return ''
}

function numOf(m: Record<string, any>, keys: string[]): number | undefined {
  for (const k of keys) {
    const v = m?.[k]
    if (typeof v === 'number') return v
    if (typeof v === 'string' && v.trim() && !Number.isNaN(Number(v))) return Number(v)
  }
  return undefined
}

function actionOf(word: string): string {
  const w = (word || '').toLowerCase()
  if (['disable_rule', 'disable', 'disabled'].includes(w)) return 'disable_rule'
  if (['adjust_severity', 'severity', 'set_severity', 'lower_severity', 'reduce_severity'].includes(w)) return 'adjust_severity'
  if (['update_pattern', 'pattern', 'modify_pattern', 'rewrite_pattern'].includes(w)) return 'update_pattern'
  return ''
}

function actionLabel(action: string): string {
  const keyMap: Record<string, string> = {
    disable_rule: 'actionDisableRule',
    adjust_severity: 'actionAdjustSeverity',
    update_pattern: 'actionUpdatePattern',
  }
  const key = keyMap[action]
  return key ? t(`common.piAnalysis.detail.${key}`) : action
}

function hasJSON(v: any): boolean {
  if (v === null || v === undefined) return false
  if (typeof v === 'string') return v.trim() !== '' && v.trim() !== 'null'
  if (typeof v === 'object') return Object.keys(v).length > 0
  return false
}

function prettyJSON(v: any): string {
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  return JSON.stringify(v, null, 2)
}

// 分页
const queueLimit = ref(20)
const queueOffset = ref(0)
const queueTotal = ref(0)

const resultsLimit = ref(20)
const resultsOffset = ref(0)
const resultsTotal = ref(0)

// 加载统计
async function loadStats() {
  try {
    const response = await fetch('/api/admin/pi-analysis/stats')
    // ★ 2026-11-18 审计轮 P2：非 2xx 必须**如实**判为「不可用」，
    // 不能把 404 的响应体当 JSON 解析（解析失败会伪装成网络错误，
    // 运维看到的提示也会误导）。⇒ 显式带状态码抛错。
    if (!response.ok) {
      throw Object.assign(new Error(`pi-analysis stats failed: HTTP ${response.status}`), {
        status: response.status,
      })
    }
    const data = await response.json()
    unavailable.value = false
    // ★ 2026-11-18 审计轮 P1：后端把「检测链是否接线」一起返回了。
    //	没接线时队列**恒为空**（唯一的生产者是 promptinjection.Detector，
    //	而整条检测链在 LLM_GATEWAY_USE_V2_PIPELINE 门控内，默认关）
    //	⇒ 必须如实显示，否则运维会把「没有生产者」读成「没有错误发生」。
    chainNotWired.value = data?.detection_chain_wired === false

    // ★ 2026-11-19 一致性根修：顶层计数直读（后端单点保证与列表 COUNT 域一致），
    // 误报率/平均耗时来自结果表聚合——不再有死零。
    stats.value = summarizePiAnalysisStats(data)
  } catch (error) {
    console.error('Failed to load stats:', error)
    markUnavailable(error)
  }
}

// ★ 2026-11-18 审计轮 P2：把「API 不可用」与「没有数据」区分开。
// 后端在无 DB / 无 admin 鉴权材料时**不注册**这批路由（fail-closed），
// 请求会得到 404 ⇒ 页面必须如实说明，而不是显示一张空表。
const unavailable = ref(false)
const unavailableReason = ref('')
// ★ 2026-11-18 审计轮 P1：后端告知「检测链未接线」时的标记。
const chainNotWired = ref(false)

function markUnavailable(error: unknown) {
  const status = (error as { status?: number })?.status
  if (status === 404 || status === 401 || status === 403) {
    unavailable.value = true
    unavailableReason.value = t('common.piAnalysis.unavailableReason')
    return
  }
  unavailable.value = true
  unavailableReason.value = t('common.piAnalysis.unavailableNetwork')
}

// 加载队列
async function loadQueue() {
  try {
    const params = new URLSearchParams({
      limit: queueLimit.value.toString(),
      offset: queueOffset.value.toString(),
    })
    if (queueStatus.value) {
      params.set('status', queueStatus.value)
    }
    
    const response = await fetch(`/api/admin/pi-analysis/queue?${params}`)
    const data = await response.json()
    queueData.value = data.errors || []
    queueTotal.value = data.total || 0
  } catch (error) {
    console.error('Failed to load queue:', error)
  }
}

// 加载结果
async function loadResults() {
  try {
    const params = new URLSearchParams({
      limit: resultsLimit.value.toString(),
      offset: resultsOffset.value.toString(),
    })
    
    const response = await fetch(`/api/admin/pi-analysis/results?${params}`)
    const data = await response.json()
    resultsData.value = data.results || []
    resultsTotal.value = data.total || 0
  } catch (error) {
    console.error('Failed to load results:', error)
  }
}

// ★ 详情打开：先试 results（有分析 → 全量四区），404 再落 queue 详情（未分析 → 前因 + 触发分析）。
async function openDetail(queueId: number) {
  activeDetailId.value = queueId
  showDetailDialog.value = true
  detailLoading.value = true
  detailError.value = ''
  detailData.value = null
  queueDetail.value = null
  try {
    const res = await fetch(`/api/admin/pi-analysis/results/${queueId}`)
    if (res.status === 404) {
      const qres = await fetch(`/api/admin/pi-analysis/queue/${queueId}`)
      if (!qres.ok) {
        throw Object.assign(new Error(`queue detail: HTTP ${qres.status}`), { status: qres.status })
      }
      queueDetail.value = await qres.json()
    } else if (!res.ok) {
      throw Object.assign(new Error(`result detail: HTTP ${res.status}`), { status: res.status })
    } else {
      detailData.value = await res.json()
    }
  } catch (error) {
    detailError.value = error instanceof Error ? error.message : String(error)
  } finally {
    detailLoading.value = false
  }
}

function viewDetail(queueId: number) {
  openDetail(queueId)
}

// ★ 应用修复：确认弹窗逐条列出将执行的动作（与后端白名单同一口径）。
async function applyFix() {
  if (!detailData.value || !canApply.value) return
  const list = normalizedActions.value
    .map((a) => `• ${actionLabel(a.action)} → ${a.rule_name}`)
    .join('\n')
  if (!window.confirm(`${t('common.piAnalysis.detail.applyConfirmTitle')}\n${t('common.piAnalysis.detail.applyConfirmBody')}\n${list}`)) {
    return
  }
  applying.value = true
  try {
    const res = await fetch(`/api/admin/pi-analysis/results/${activeDetailId.value}/apply`, { method: 'POST' })
    if (!res.ok) {
      const body = await res.text()
      alert(`${t('common.piAnalysis.detail.applyFailed')}: HTTP ${res.status}\n${body.slice(0, 300)}`)
      return
    }
    alert(t('common.piAnalysis.detail.applySuccess'))
    await openDetail(activeDetailId.value)
    refreshData()
  } catch (error) {
    alert(`${t('common.piAnalysis.detail.applyFailed')}: ${error}`)
  } finally {
    applying.value = false
  }
}

// ★ 回滚修复：按留痕的旧值快照逐条恢复。
async function revertFix() {
  if (!detailData.value?.auto_applied) return
  const list = appliedRecords.value.map((r) => `• ${r.rule_name}`).join('\n')
  if (!window.confirm(`${t('common.piAnalysis.detail.revertConfirmTitle')}\n${t('common.piAnalysis.detail.revertConfirmBody')}\n${list}`)) {
    return
  }
  reverting.value = true
  try {
    const res = await fetch(`/api/admin/pi-analysis/results/${activeDetailId.value}/revert`, { method: 'POST' })
    if (!res.ok) {
      const body = await res.text()
      alert(`${t('common.piAnalysis.detail.revertFailed')}: HTTP ${res.status}\n${body.slice(0, 300)}`)
      return
    }
    alert(t('common.piAnalysis.detail.revertSuccess'))
    await openDetail(activeDetailId.value)
    refreshData()
  } catch (error) {
    alert(`${t('common.piAnalysis.detail.revertFailed')}: ${error}`)
  } finally {
    reverting.value = false
  }
}

function closeDetail() {
  showDetailDialog.value = false
  detailData.value = null
  queueDetail.value = null
  detailError.value = ''
}

// 触发分析
async function triggerAnalysis() {
  isTriggering.value = true
  try {
    await fetch('/api/admin/pi-analysis/trigger', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ batch_size: 10 })
    })
    alert(t('common.piAnalysis.detail.triggerQueued'))
  } catch (error) {
    console.error('Failed to trigger analysis:', error)
    alert(`${t('common.piAnalysis.detail.loadFailed')}: ${error}`)
  } finally {
    isTriggering.value = false
  }
}

// 刷新数据
function refreshData() {
  loadStats()
  if (activeTab.value === 'queue') {
    loadQueue()
  } else {
    loadResults()
  }
}

// 分页
function prevQueuePage() {
  if (queueOffset.value >= queueLimit.value) {
    queueOffset.value -= queueLimit.value
    loadQueue()
  }
}

function nextQueuePage() {
  if (queueOffset.value + queueLimit.value < queueTotal.value) {
    queueOffset.value += queueLimit.value
    loadQueue()
  }
}

function prevResultsPage() {
  if (resultsOffset.value >= resultsLimit.value) {
    resultsOffset.value -= resultsLimit.value
    loadResults()
  }
}

function nextResultsPage() {
  if (resultsOffset.value + resultsLimit.value < resultsTotal.value) {
    resultsOffset.value += resultsLimit.value
    loadResults()
  }
}

// 格式化函数
function formatErrorType(type: string): string {
  const map: Record<string, string> = {
    'llm_detection_failed': t('common.piAnalysis.page.errorTypeLLMDetectionFailed'),
    'rule_false_positive': t('common.piAnalysis.page.errorTypeRuleFalsePositive'),
    'canary_detection_failed': t('common.piAnalysis.page.errorTypeCanaryDetectionFailed'),
    'vector_match_failed': t('common.piAnalysis.page.errorTypeVectorMatchFailed'),
    'heuristic_misfire': t('common.piAnalysis.page.errorTypeHeuristicMisfire'),
    'policy_load_failed': t('common.piAnalysis.page.errorTypePolicyLoadFailed'),
    'unexpected_behavior': t('common.piAnalysis.page.errorTypeUnexpectedBehavior'),
    'performance_issue': t('common.piAnalysis.page.errorTypePerformanceIssue')
  }
  return map[type] || type
}

function formatStatus(status: string): string {
  const map: Record<string, string> = {
    'pending': t('common.piAnalysis.page.statusPending'),
    'processing': t('common.piAnalysis.page.statusProcessing'),
    'analyzed': t('common.piAnalysis.page.statusAnalyzed'),
    'failed': t('common.piAnalysis.page.statusFailed'),
    'skipped': t('common.piAnalysis.page.statusSkipped')
  }
  return map[status] || status
}

function formatSeverity(severity: string): string {
  const map: Record<string, string> = {
    'low': t('common.piAnalysis.page.severityLow'),
    'medium': t('common.piAnalysis.page.severityMedium'),
    'high': t('common.piAnalysis.page.severityHigh'),
    'critical': t('common.piAnalysis.page.severityCritical')
  }
  return map[severity] || severity
}

function formatImpact(impact: string): string {
  const map: Record<string, string> = {
    'session': t('common.piAnalysis.page.impactSession'),
    'tenant': t('common.piAnalysis.page.impactTenant'),
    'global': t('common.piAnalysis.page.impactGlobal')
  }
  return map[impact] || impact
}

function formatTime(time: string): string {
  // ★ 详情端点的时间列来自 PG timestamptz 文本形（"2026-11-18 12:34:56.789+00"，
  //   空格分隔）⇒ new Date() 解析失败显示 Invalid Date；空格换 T 再解析。
  if (!time) return '—'
  const d = new Date(time.includes(' ') && !time.includes('T') ? time.replace(' ', 'T') : time)
  return Number.isNaN(d.getTime()) ? time : d.toLocaleString('zh-CN')
}

function getErrorTypeClass(type: string): string {
  if (type.includes('failed')) return 'badge-error'
  if (type.includes('false_positive')) return 'badge-warning'
  return 'badge-info'
}

function getPriorityClass(priority: number): string {
  if (priority >= 8) return 'priority-high'
  if (priority >= 5) return 'priority-medium'
  return 'priority-low'
}

function getStatusClass(status: string): string {
  const map: Record<string, string> = {
    'pending': 'status-pending',
    'processing': 'status-processing',
    'analyzed': 'status-success',
    'failed': 'status-error',
    'skipped': 'status-skipped'
  }
  return map[status] || ''
}

function getSeverityClass(severity: string): string {
  const map: Record<string, string> = {
    'low': 'severity-low',
    'medium': 'severity-medium',
    'high': 'severity-high',
    'critical': 'severity-critical'
  }
  return map[severity] || ''
}

// 初始化
onMounted(() => {
  loadStats()
  loadQueue()
  loadResults()
})
</script>

<style scoped>
.pi-analysis-view {
  padding: 20px;
}

/* ★ 2026-11-18 audit round P2: banner shown when the PI analysis API is unavailable. */
.unavailable-banner {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 16px;
  padding: 12px 16px;
  border-radius: 4px;
  border: 1px solid #d97706;
  background: rgba(217, 119, 6, 0.08);
  color: inherit;
  font-size: 13px;
  line-height: 1.5;
}

/* ★ 2026-11-18 audit round P1: banner shown when the detection chain is not wired. */
.chain-note-banner {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 16px;
  padding: 12px 16px;
  border-radius: 4px;
  border: 1px solid #6b7280;
  background: rgba(107, 114, 128, 0.08);
  color: inherit;
  font-size: 13px;
  line-height: 1.5;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.header h1 {
  margin: 0;
  font-size: 24px;
}

.actions {
  display: flex;
  gap: 10px;
}

.btn-primary, .btn-secondary {
  padding: 8px 16px;
  border-radius: 4px;
  border: none;
  cursor: pointer;
  font-size: 14px;
}

.btn-primary {
  background: #1890ff;
  color: white;
}

.btn-primary:hover {
  background: #40a9ff;
}

.btn-primary:disabled {
  background: #d9d9d9;
  cursor: not-allowed;
}

.btn-secondary {
  background: #f0f0f0;
  color: #333;
}

.btn-secondary:hover {
  background: #e0e0e0;
}

.stats-cards {
  display: grid;
  grid-template-columns: repeat(6, 1fr);
  gap: 16px;
  margin-bottom: 24px;
}

/* 总数卡的瞬态子行（处理中/跳过） */
.stat-sub {
  font-size: 11px;
  color: #8c8c8c;
  margin-top: 6px;
}

.stat-card {
  background: white;
  padding: 20px;
  border-radius: 8px;
  box-shadow: 0 2px 4px rgba(0,0,0,0.1);
}

.stat-label {
  font-size: 14px;
  color: #666;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 28px;
  font-weight: bold;
  color: #1890ff;
}

.tabs {
  display: flex;
  gap: 0;
  margin-bottom: 20px;
  border-bottom: 2px solid #e8e8e8;
}

.tab {
  padding: 12px 24px;
  background: none;
  border: none;
  cursor: pointer;
  font-size: 14px;
  color: #666;
  position: relative;
}

.tab:hover {
  color: #1890ff;
}

.tab.active {
  color: #1890ff;
  font-weight: bold;
}

.tab.active::after {
  content: '';
  position: absolute;
  bottom: -2px;
  left: 0;
  right: 0;
  height: 2px;
  background: #1890ff;
}

.table-container {
  background: white;
  border-radius: 8px;
  padding: 20px;
  box-shadow: 0 2px 4px rgba(0,0,0,0.1);
}

.filters {
  margin-bottom: 16px;
}

.filters select {
  padding: 8px 12px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 14px;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
}

.data-table th {
  text-align: left;
  padding: 12px;
  background: #fafafa;
  font-weight: 600;
  border-bottom: 1px solid #e8e8e8;
}

.data-table td {
  padding: 12px;
  border-bottom: 1px solid #f0f0f0;
}

.data-table tr:hover {
  background: #fafafa;
}

.badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}

.badge-error {
  background: #fff1f0;
  color: #cf1322;
}

.badge-warning {
  background: #fffbe6;
  color: #d48806;
}

.badge-info {
  background: #e6f7ff;
  color: #0958d9;
}

.badge-success {
  background: #f6ffed;
  color: #52c41a;
}

.priority {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: bold;
}

.priority-high {
  background: #ff4d4f;
  color: white;
}

.priority-medium {
  background: #faad14;
  color: white;
}

.priority-low {
  background: #52c41a;
  color: white;
}

.status {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
}

.status-pending {
  background: #f0f0f0;
  color: #666;
}

.status-processing {
  background: #e6f7ff;
  color: #1890ff;
}

.status-success {
  background: #f6ffed;
  color: #52c41a;
}

.status-error {
  background: #fff1f0;
  color: #ff4d4f;
}

.severity {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}

.severity-low {
  background: #f6ffed;
  color: #52c41a;
}

.severity-medium {
  background: #fffbe6;
  color: #faad14;
}

.severity-high {
  background: #fff1f0;
  color: #ff4d4f;
}

.severity-critical {
  background: #000;
  color: #fff;
}

.error-message, .root-cause, .suggested-fix {
  max-width: 300px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.btn-link {
  background: none;
  border: none;
  color: #1890ff;
  cursor: pointer;
  text-decoration: underline;
}

.btn-link:hover {
  color: #40a9ff;
}

.pagination {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 16px;
  margin-top: 16px;
}

.pagination button {
  padding: 6px 12px;
  border: 1px solid #d9d9d9;
  background: white;
  border-radius: 4px;
  cursor: pointer;
}

.pagination button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.dialog-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0,0,0,0.5);
  display: flex;
  justify-content: center;
  align-items: center;
  z-index: 1000;
}

.dialog {
  background: white;
  border-radius: 8px;
  max-width: 800px;
  max-height: 80vh;
  overflow: auto;
  box-shadow: 0 4px 12px rgba(0,0,0,0.15);
}

/* ★ 2026-11-18 PI analysis optimization: wider detail dialog + section styles. */
.detail-dialog {
  max-width: 960px;
  width: calc(100vw - 48px);
}

.detail-empty,
.detail-error {
  color: #cf1322;
  padding: 12px 0;
}

.muted {
  color: #8c8c8c;
  font-size: 13px;
}

.hash-code {
  font-size: 12px;
  background: #f5f5f5;
  padding: 1px 6px;
  border-radius: 3px;
}

.detail-sub {
  margin-top: 14px;
}

.detail-sub h4 {
  margin: 0 0 8px;
  font-size: 13px;
  color: #595959;
}

.timeline {
  display: flex;
  align-items: flex-start;
  gap: 0;
}

.timeline-step {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  min-width: 110px;
}

.timeline-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: #d9d9d9;
}

.timeline-dot.done {
  background: #1890ff;
}

.timeline-bar {
  flex: 1;
  height: 2px;
  background: #d9d9d9;
  margin-top: 4px;
}

.timeline-bar.done {
  background: #1890ff;
}

.timeline-label {
  font-size: 12px;
  color: #595959;
}

.timeline-time {
  font-size: 11px;
  color: #8c8c8c;
}

.timeline-duration {
  margin-top: 8px;
  font-size: 12px;
  color: #595959;
}

.untrusted-note {
  font-size: 12px;
  color: #d46b08;
  background: #fff7e6;
  border: 1px solid #ffd591;
  border-radius: 4px;
  padding: 6px 10px;
  margin-bottom: 6px;
}

pre.untrusted-input {
  border: 1px dashed #ffd591;
}

pre.collapsible {
  max-height: 220px;
  overflow: auto;
}

.rule-card {
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  padding: 10px 12px;
  margin-bottom: 8px;
  background: #fafafa;
}

.rule-card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.rule-pattern {
  margin: 8px 0 0;
  background: #f0f0f0;
  padding: 8px;
  border-radius: 4px;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}

.action-card {
  border-color: #ffe58f;
}

.action-reason {
  margin-top: 6px;
  font-size: 12px;
  color: #8c8c8c;
}

.actions-section {
  display: flex;
  flex-direction: column;
  gap: 10px;
  border-top: 1px solid #f0f0f0;
  padding-top: 16px;
}

.view-only-note {
  font-size: 12px;
  color: #d48806;
  background: #fffbe6;
  border: 1px solid #ffe58f;
  border-radius: 4px;
  padding: 6px 10px;
}

.btn-secondary.danger {
  color: #cf1322;
  border-color: #ffa39e;
}

.clickable-row {
  cursor: pointer;
}

.dialog-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 24px;
  border-bottom: 1px solid #e8e8e8;
}

.dialog-header h2 {
  margin: 0;
  font-size: 18px;
}

.btn-close {
  background: none;
  border: none;
  font-size: 24px;
  cursor: pointer;
  color: #999;
}

.btn-close:hover {
  color: #666;
}

.dialog-content {
  padding: 24px;
}

.detail-section {
  margin-bottom: 24px;
}

.detail-section h3 {
  font-size: 16px;
  margin-bottom: 12px;
  color: #333;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.detail-item {
  display: flex;
  gap: 8px;
}

.detail-item label {
  font-weight: 600;
  color: #666;
}

.detail-section pre {
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
  overflow-x: auto;
  font-size: 12px;
  line-height: 1.5;
}

.detail-section p {
  line-height: 1.6;
  color: #333;
}
</style>
