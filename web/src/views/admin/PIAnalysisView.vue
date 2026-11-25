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
      <!-- ★ 2026-11-25：存量「解析失败」的占位结论 —— 一键重解析（只读已存的响应，不调用模型）。 -->
      <div class="chain-note-banner" v-if="parseFailedCount > 0" data-testid="pi-parse-failed-banner">
        <strong>{{ t('common.piAnalysis.forensics.parseFailedBannerTitle', { n: parseFailedCount }) }}</strong>
        <span>{{ t('common.piAnalysis.forensics.parseFailedBannerBody') }}</span>
        <span v-if="bulkReparseNote" data-testid="pi-parse-failed-result">{{ bulkReparseNote }}</span>
        <button class="btn-secondary" :disabled="bulkReparsing" @click="reparseAll">
          {{ bulkReparsing ? '…' : t('common.piAnalysis.forensics.reparseAllButton') }}
        </button>
      </div>
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
                <!-- 修复已应用 / 最近复测：时间线从「错误发生」一直走到「修好了没有」。 -->
                <template v-if="detailData?.applied_at">
                  <div class="timeline-bar done"></div>
                  <div class="timeline-step" data-testid="pi-timeline-fix-applied">
                    <div class="timeline-dot done"></div>
                    <div class="timeline-label">{{ t('common.piAnalysis.forensics.timelineFixApplied') }}</div>
                    <div class="timeline-time">{{ formatTime(detailData.applied_at) }}</div>
                  </div>
                </template>
                <template v-if="lastVerification">
                  <div class="timeline-bar done"></div>
                  <div class="timeline-step" data-testid="pi-timeline-last-verify">
                    <div class="timeline-dot done"></div>
                    <div class="timeline-label">{{ t('common.piAnalysis.forensics.timelineLastVerify') }}</div>
                    <div class="timeline-time">
                      <span class="status" :class="verdictClass(verdictOf(lastVerification))">{{ verdictLabel(verdictOf(lastVerification)) }}</span>
                    </div>
                  </div>
                </template>
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

            <!-- ── 取证材料（2026-11-25 取证轮）：请求账本 / 上游尝试 / 会话上下文 / 服务日志 / 手册 ── -->
            <div class="detail-section" data-testid="pi-evidence-section">
              <h3>{{ t('common.piAnalysis.forensics.evidenceTitle') }}</h3>
              <div class="actions-section">
                <button class="btn-secondary" :disabled="evidenceLoading" @click="loadEvidence">
                  {{ evidenceLoading ? t('common.piAnalysis.forensics.evidenceLoading') : t('common.piAnalysis.forensics.evidenceReload') }}
                </button>
                <div class="view-only-note" v-if="evidenceFailed" data-testid="pi-evidence-failed">{{ t('common.piAnalysis.forensics.evidenceFailed') }}</div>
              </div>

              <template v-if="evidence">
                <!-- 请求账本：出错的那一条请求（模型 / 协议 / 结果 / 失败阶段 / 耗时 / tokens） -->
                <div class="detail-sub" v-if="evidenceRequestFacts.length" data-testid="pi-evidence-request">
                  <h4>
                    {{ t('common.piAnalysis.forensics.requestTitle') }}
                    <button v-if="evidence.request_id" class="btn-link" @click="openRequest(evidence.request_id)">{{ t('common.piAnalysis.forensics.openRequest') }}</button>
                  </h4>
                  <div class="detail-grid">
                    <div class="detail-item" v-for="(f, i) in evidenceRequestFacts" :key="i">
                      <label>{{ factLabel(f.key) }}:</label>
                      <span v-if="f.lines"><span v-for="(line, j) in f.lines" :key="j">{{ line }}<br /></span></span>
                      <span v-else :class="f.cls">{{ factValue(f) }}</span>
                    </div>
                  </div>
                  <pre class="collapsible" v-if="evidence.request?.request_preview">{{ evidence.request.request_preview }}</pre>
                </div>

                <!-- 上游尝试：该请求在各上游上的失败记录 -->
                <div class="detail-sub" v-if="evidenceAttempts.length" data-testid="pi-evidence-attempts">
                  <h4>{{ t('common.piAnalysis.forensics.attemptsTitle') }}</h4>
                  <div class="rule-card" v-for="(a, i) in evidenceAttempts" :key="i">
                    <div class="rule-card-head">
                      <span class="badge badge-info">#{{ a.attempt_index }}</span>
                      <span>{{ a.provider_id }} / {{ a.model }}</span>
                      <span class="badge badge-warning">{{ a.error_kind }}</span>
                      <span v-if="a.upstream_status_code" class="status status-error">{{ t('common.piAnalysis.forensics.factUpstreamStatus') }} {{ a.upstream_status_code }}</span>
                      <span class="muted">{{ formatTime(a.ts) }} · {{ a.latency_ms }} ms</span>
                    </div>
                    <p>{{ a.error_message }}</p>
                  </div>
                </div>

                <!-- 会话上下文：同一会话里前后相邻的请求（时间正序） -->
                <div class="detail-sub" v-if="evidenceSession.length" data-testid="pi-evidence-session">
                  <h4>{{ t('common.piAnalysis.forensics.sessionTitle') }}</h4>
                  <div class="rule-card" v-for="(row, i) in evidenceSession" :key="i">
                    <div class="rule-card-head">
                      <span class="badge badge-error" v-if="row.is_target">{{ t('common.piAnalysis.forensics.targetRequest') }}</span>
                      <span class="muted">{{ formatTime(row.ts) }}</span>
                      <span class="status" :class="requestStatusClass(row.request_status)">{{ requestStatusLabel(row.request_status) }}</span>
                      <span>{{ row.client_model }}</span>
                      <span v-if="row.error_kind" class="badge badge-error">{{ row.error_kind }}</span>
                      <span class="muted" v-if="row.latency_ms">{{ row.latency_ms }} ms</span>
                    </div>
                  </div>
                </div>

                <!-- 同型错误分布：偶发还是一直在发生 -->
                <div class="detail-sub" v-if="evidenceWindow.length" data-testid="pi-evidence-window">
                  <h4>{{ t('common.piAnalysis.forensics.windowTitle') }}</h4>
                  <div class="rule-card" v-for="(w, i) in evidenceWindow" :key="i">
                    <div class="rule-card-head">
                      <span>{{ w.provider_id }} / {{ w.model }}</span>
                      <span class="badge badge-warning">{{ w.error_kind }}</span>
                      <span>{{ t('common.piAnalysis.forensics.windowCount', { n: w.count }) }}</span>
                      <span class="muted">{{ formatTime(w.last_ts) }}</span>
                    </div>
                  </div>
                </div>

                <!-- 服务日志：本实例近期日志缓冲里与该错误相关的行 -->
                <div class="detail-sub" data-testid="pi-evidence-logs">
                  <h4>{{ t('common.piAnalysis.forensics.logsTitle') }}</h4>
                  <p class="muted" v-if="!evidenceLogText" data-testid="pi-evidence-logs-empty">{{ evidenceLogNote }}</p>
                  <pre class="collapsible" v-else>{{ evidenceLogText }}</pre>
                </div>

                <!-- 错误处理手册里的同型条目 -->
                <div class="detail-sub" v-if="evidencePlaybook.length" data-testid="pi-evidence-playbook">
                  <h4>{{ t('common.piAnalysis.forensics.playbookTitle') }}</h4>
                  <div class="rule-card" v-for="(entry, i) in evidencePlaybook" :key="i">
                    <div class="rule-card-head">
                      <span class="badge badge-info">#{{ entry.id }}</span>
                      <span>{{ entry.title }}</span>
                      <span class="badge" :class="entry.enabled ? 'badge-success' : 'badge-warning'">{{ entry.enabled ? t('common.piAnalysis.forensics.enabledYes') : t('common.piAnalysis.forensics.enabledNo') }}</span>
                    </div>
                    <p>{{ entry.root_cause }}</p>
                    <div class="action-reason">{{ t('common.piAnalysis.forensics.playbookMeta', { kind: entry.plan_kind, hits: entry.hit_count, ok: entry.success_count, fail: entry.failure_count }) }}</div>
                  </div>
                </div>

                <!-- 取证说明：哪一路没取到、为什么 -->
                <div class="detail-sub" v-if="evidenceNotes.length" data-testid="pi-evidence-notes">
                  <h4>{{ t('common.piAnalysis.forensics.notesTitle') }}</h4>
                  <ul><li v-for="(note, i) in evidenceNotes" :key="i">{{ note }}</li></ul>
                </div>
              </template>
            </div>

            <!-- ── 分析 · 结论 ─────────────────────────────────────── -->
            <div class="detail-section">
              <h3>{{ t('common.piAnalysis.detail.analysisTitle') }}</h3>
              <!-- ★ 旧解析器读不出的占位结论：重新解析存量响应（不调用模型）/ 带取证重新分析。 -->
              <div class="view-only-note" v-if="parseFailed" data-testid="pi-parse-failed-note">
                <p>{{ t('common.piAnalysis.forensics.parseFailedBanner') }}</p>
                <div class="actions-section">
                  <button class="btn-secondary" :disabled="reparsing" @click="reparseThis">
                    {{ reparsing ? '…' : t('common.piAnalysis.forensics.reparseButton') }}
                  </button>
                  <div class="view-only-note" v-if="reparseFailed" data-testid="pi-reparse-failed">{{ t('common.piAnalysis.forensics.reparseFailed') }}</div>
                </div>
              </div>
              <!-- 解析诊断：模型输出不完整 / 未按结构 ⇒ 结论按保守口径处理（如实说明，不装作完整）。 -->
              <div class="view-only-note" v-else-if="diagnostics && diagnostics.mode !== 'canonical'" data-testid="pi-parse-diagnostics">
                <p v-if="diagnostics.truncated || diagnostics.finish_reason === 'length'">{{ t('common.piAnalysis.forensics.parseNoteTruncated') }}</p>
                <p v-if="diagnostics.mode === 'unstructured'">{{ t('common.piAnalysis.forensics.parseNoteUnstructured') }}</p>
                <p v-else-if="diagnostics.mode === 'salvaged'">{{ t('common.piAnalysis.forensics.parseNoteSalvaged') }}</p>
                <p v-if="diagnostics.lost_keys.length">{{ t('common.piAnalysis.forensics.parseNoteLost', { keys: diagnostics.lost_keys.join(', ') }) }}</p>
              </div>
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
                <div class="detail-sub" v-if="modelOutput" data-testid="pi-model-output">
                  <h4>{{ t('common.piAnalysis.forensics.modelOutputTitle') }}</h4>
                  <pre class="collapsible">{{ JSON.stringify(modelOutput, null, 2) }}</pre>
                </div>
              </template>
              <p v-else-if="!analyzeQueued" class="muted">{{ t('common.piAnalysis.detail.notAnalyzedYet') }}</p>
            </div>

            <!-- ── 修正建议 ────────────────────────────────────────── -->
            <div class="detail-section" v-if="detailData">
              <h3>{{ t('common.piAnalysis.forensics.planTitle') }}</h3>
              <!-- ★ 2026-11-25：结构化修正方案 —— 自动动作 / 人工步骤 / 配置 / 代码 / 部署 / 验证。
                   自动动作由下面「规则调整建议」承载（后端单点判定能不能执行）；其余全部给人落地。 -->
              <div class="view-only-note" v-if="planIsEmpty" data-testid="pi-plan-empty">{{ t('common.piAnalysis.forensics.planEmpty') }}</div>
              <template v-if="fixPlan && !planIsEmpty">
                <div class="detail-sub" v-if="fixPlan.summary" data-testid="pi-plan-summary">
                  <h4>{{ t('common.piAnalysis.forensics.planSummary') }}</h4>
                  <p>{{ fixPlan.summary }}</p>
                </div>

                <div class="detail-sub" v-if="fixPlan.manual_steps?.length" data-testid="pi-plan-steps">
                  <h4>{{ t('common.piAnalysis.forensics.planManualTitle') }}</h4>
                  <div class="rule-card" v-for="st in fixPlan.manual_steps" :key="st.step">
                    <div class="rule-card-head">
                      <span class="badge badge-info">{{ st.step }}</span>
                      <span class="badge" :class="stepKindBadge(normalizeKind(st.kind))">{{ kindLabel(st.kind) }}</span>
                      <span class="severity" :class="stepRiskClass(normalizeRisk(st.risk))">{{ riskLabel(st.risk) }}</span>
                      <span>{{ st.title }}</span>
                    </div>
                    <p v-if="st.detail">{{ st.detail }}</p>
                  </div>
                </div>

                <div class="detail-sub" v-if="fixPlan.config_changes?.length" data-testid="pi-plan-config">
                  <h4>{{ t('common.piAnalysis.forensics.planConfigTitle') }}</h4>
                  <div class="rule-card" v-for="(c, i) in fixPlan.config_changes" :key="i">
                    <div class="rule-card-head"><code>{{ c.target }}</code></div>
                    <div class="action-params">
                      {{ t('common.piAnalysis.forensics.currentValue') }}: <code>{{ c.current || '—' }}</code> → {{ t('common.piAnalysis.forensics.proposedValue') }}: <code>{{ c.proposed }}</code>
                    </div>
                  </div>
                </div>

                <div class="detail-sub" v-if="fixPlan.code_changes?.length" data-testid="pi-plan-code">
                  <h4>{{ t('common.piAnalysis.forensics.planCodeTitle') }}</h4>
                  <div class="rule-card" v-for="(c, i) in fixPlan.code_changes" :key="i">
                    <div class="rule-card-head">
                      <code v-if="c.file">{{ c.file }}</code>
                      <button v-if="c.patch" class="btn-link" @click="copyPatch(c.patch)">{{ t('common.piAnalysis.forensics.copyPatch') }}</button>
                    </div>
                    <p>{{ c.description }}</p>
                    <pre class="rule-pattern" v-if="c.patch">{{ c.patch }}</pre>
                  </div>
                </div>

                <div class="detail-sub" v-if="fixPlan.deploy" data-testid="pi-plan-deploy">
                  <h4>{{ t('common.piAnalysis.forensics.planDeployTitle') }}</h4>
                  <div class="detail-grid">
                    <div class="detail-item">
                      <span class="badge" :class="fixPlan.deploy.required ? 'badge-warning' : 'badge-success'">
                        {{ fixPlan.deploy.required ? t('common.piAnalysis.forensics.planDeployRequired') : t('common.piAnalysis.forensics.planDeployNotRequired') }}
                      </span>
                    </div>
                    <div class="detail-item" v-if="fixPlan.deploy.targets?.length">
                      <label>{{ t('common.piAnalysis.forensics.planTargets') }}:</label><span>{{ fixPlan.deploy.targets.join(', ') }}</span>
                    </div>
                  </div>
                  <div class="rule-card" v-for="(st, i) in fixPlan.deploy.steps || []" :key="i"><p>{{ st }}</p></div>
                  <div class="action-reason" v-if="fixPlan.deploy.rollback">{{ t('common.piAnalysis.forensics.planRollback') }}: {{ fixPlan.deploy.rollback }}</div>
                </div>

                <div class="detail-sub" v-if="fixPlan.verification" data-testid="pi-plan-verify">
                  <h4>{{ t('common.piAnalysis.forensics.planVerifyTitle') }}</h4>
                  <div class="detail-grid">
                    <div class="detail-item" v-if="fixPlan.verification.method">
                      <label>{{ t('common.piAnalysis.forensics.verifyMethodLabel') }}:</label><span>{{ verifyMethodLabel(fixPlan.verification.method) }}</span>
                    </div>
                    <div class="detail-item" v-if="fixPlan.verification.expect">
                      <label>{{ t('common.piAnalysis.forensics.verifyExpect') }}:</label><span>{{ fixPlan.verification.expect }}</span>
                    </div>
                  </div>
                </div>

                <div class="actions-section">
                  <button class="btn-secondary" :disabled="copying" @click="copyPlan">{{ t('common.piAnalysis.forensics.planCopy') }}</button>
                </div>
              </template>
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

            <!-- ── 复测（2026-11-25 取证轮）：规则重放 / 出错层复查 / 复发统计 → 结论 ── -->
            <div class="detail-section" data-testid="pi-verify-section">
              <h3>{{ t('common.piAnalysis.forensics.verifyTitle') }}</h3>
              <div class="actions-section">
                <div class="detail-item" v-if="showJudgeOption">
                  <label><input type="checkbox" v-model="verifyIncludeJudge" data-testid="pi-verify-include-judge" /> {{ t('common.piAnalysis.forensics.verifyIncludeJudge') }}</label>
                </div>
                <button class="btn-secondary" :disabled="verifying" @click="runVerify" data-testid="pi-verify-run">
                  {{ verifying ? t('common.piAnalysis.forensics.verifyRunning') : t('common.piAnalysis.forensics.verifyButton') }}
                </button>
                <div class="view-only-note" v-if="verifyResult?.error" data-testid="pi-verify-failed">{{ t('common.piAnalysis.forensics.verifyFailed') }}</div>
              </div>

              <template v-if="verifyResult && !verifyResult.error">
                <div class="detail-grid" data-testid="pi-verify-verdict">
                  <div class="detail-item">
                    <label>{{ t('common.piAnalysis.forensics.verdictLabel') }}:</label>
                    <span class="status" :class="verdictClass(verdictOf(verifyResult))">{{ verdictLabel(verdictOf(verifyResult)) }}</span>
                  </div>
                  <div class="detail-item"><label>{{ t('common.piAnalysis.forensics.verifyAtLabel') }}:</label><span>{{ formatTime(verifyResult.at) }}</span></div>
                  <div class="detail-item"><label>{{ t('common.piAnalysis.forensics.verifyByLabel') }}:</label><span>{{ verifyResult.by }}</span></div>
                </div>
                <div class="rule-card" v-for="(c, i) in verifyResult.checks || []" :key="i">
                  <div class="rule-card-head">
                    <span>{{ checkNameLabel(c.name) }}</span>
                    <span class="status" :class="checkClass(c.status)">{{ checkStatusLabel(c.status) }}</span>
                    <span v-if="c.decisive" class="badge badge-info">{{ t('common.piAnalysis.forensics.checkDecisive') }}</span>
                  </div>
                  <div class="detail-grid" v-if="c.detail">
                    <div class="detail-item" v-if="c.detail.originally_matched">
                      <label>{{ t('common.piAnalysis.forensics.detailOriginally') }}:</label><span>{{ noneText(c.detail.originally_matched) }}</span>
                    </div>
                    <div class="detail-item" v-if="c.detail.now_matched">
                      <label>{{ t('common.piAnalysis.forensics.detailNow') }}:</label><span>{{ noneText(c.detail.now_matched) }}</span>
                    </div>
                    <div class="detail-item" v-if="c.detail.still_matching">
                      <label>{{ t('common.piAnalysis.forensics.detailStill') }}:</label><span>{{ noneText(c.detail.still_matching) }}</span>
                    </div>
                    <div class="detail-item" v-if="typeof c.detail.rule_count === 'number'">
                      {{ t('common.piAnalysis.forensics.detailRuleCount', { n: c.detail.rule_count }) }}
                    </div>
                    <div class="detail-item" v-if="c.detail.layer"><label>{{ t('common.piAnalysis.forensics.detailLayer') }}:</label><span>{{ c.detail.detail }}</span></div>
                    <div class="detail-item" v-if="typeof c.detail.queue_rows === 'number'">
                      {{ t('common.piAnalysis.forensics.detailRecurrence', { q: c.detail.queue_rows, f: c.detail.failure_rows ?? 0 }) }}
                    </div>
                  </div>
                </div>
              </template>
              <p class="muted" v-else-if="!lastVerification" data-testid="pi-verify-none">{{ t('common.piAnalysis.forensics.verdictNone') }}</p>

              <div class="detail-sub" v-if="verifyHistory.length" data-testid="pi-verify-history">
                <h4>{{ t('common.piAnalysis.forensics.historyTitle') }}</h4>
                <div class="rule-card" v-for="(h, i) in verifyHistory.slice().reverse()" :key="i">
                  <div class="rule-card-head">
                    <span class="muted">{{ formatTime(h.at) }}</span>
                    <span class="status" :class="verdictClass(verdictOf(h))">{{ verdictLabel(verdictOf(h)) }}</span>
                    <span>{{ h.by }}</span>
                    <span class="badge badge-success" v-if="h.fix_applied">{{ t('common.piAnalysis.forensics.fixAppliedTag') }}</span>
                  </div>
                </div>
              </div>
            </div>

            <!-- ── 操作区 ──────────────────────────────────────────── -->
            <div class="detail-section actions-section" v-if="!detailError">
              <template v-if="!detailData">
                <div class="view-only-note" v-if="analyzeQueued" data-testid="pi-analyze-queued">{{ t('common.piAnalysis.forensics.analyzeQueued') }}</div>
                <!-- 定向分析：只分析这一条（批量触发按优先级领取，不保证轮到它）。 -->
                <button class="btn-primary" :disabled="analyzing || analyzeQueued" @click="analyzeThis" data-testid="pi-analyze-this">
                  {{ analyzing ? '…' : t('common.piAnalysis.detail.triggerForThis') }}
                </button>
              </template>
              <template v-else-if="!detailData.auto_applied">
                <!-- 为什么不能自动修正：逐条列出后端判定的阻断原因（按钮状态与接口结论同源）。 -->
                <template v-if="!canApply">
                  <div class="view-only-note" v-for="b in (planBlockers.length ? planBlockers : ['low_confidence'])" :key="b" data-testid="pi-apply-blocker">
                    {{ blockerLabel(b, detailData.confidence_score ?? 0) }}
                  </div>
                </template>
                <button class="btn-primary" :disabled="!canApply || applying" @click="applyFix">
                  {{ applying ? '…' : t('common.piAnalysis.detail.applyButton') }}
                </button>
                <button class="btn-secondary" :disabled="analyzing || analyzeQueued" @click="analyzeThis" data-testid="pi-reanalyze">
                  {{ t('common.piAnalysis.forensics.reanalyzeButton') }}
                </button>
                <div class="view-only-note" v-if="analyzeQueued">{{ t('common.piAnalysis.forensics.analyzeQueued') }}</div>
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
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { summarizePiAnalysisStats } from '@/lib/piAnalysisStats'
import {
  BLOCKER_KEYS,
  asArray,
  autoActionDetail,
  buildPlanText,
  checkClass,
  countParseFailed,
  diagnosticsOf,
  fixPlanOf,
  isParseFailed,
  isPlanEmpty,
  modelOutputOf,
  normalizeKind,
  normalizeRisk,
  requestFacts,
  requestStatusClass,
  serviceLogText,
  stepKindBadge,
  stepRiskClass,
  supportsJudgeOption,
  verdictClass,
  verdictOf,
} from '@/lib/piAnalysisForensics'
import { createLatestRequestGuard } from '@/utils/latestRequestGuard'
import { openRequestDetailPage } from '@/utils/openRequestDetailPage'

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

// ★ 2026-11-25 取证轮：详情弹窗的取证 / 定向分析 / 重解析 / 复测状态。
const evidence = ref<any>(null)
const evidenceLoading = ref(false)
const evidenceFailed = ref(false)
const analyzing = ref(false)
const analyzeQueued = ref(false)
const reparsing = ref(false)
const reparseFailed = ref(false)
const bulkReparsing = ref(false)
const bulkReparseNote = ref('')
const verifying = ref(false)
const verifyIncludeJudge = ref(false)
const verifyResult = ref<any>(null)
const copying = ref(false)

// ★ 按钮状态与接口结论同源：后端在结果详情里附 fix_plan_view（能不能自动修正、
//   为什么不能）。旧版后端没有这个字段时退回前端的本地判定。
const planView = computed<any>(() => detailData.value?.fix_plan_view ?? null)
const planBlockers = computed<string[]>(() => (planView.value ? asArray<string>(planView.value.blockers) : []))

const canApply = computed(() =>
  !!detailData.value &&
  !detailData.value.auto_applied &&
  (planView.value
    ? planView.value.auto_fixable === true
    : normalizedActions.value.length > 0 && (detailData.value.confidence_score ?? 0) >= CONFIDENCE_FLOOR),
)

// ── 取证 / 诊断 / 方案 / 复测 的读数（纯函数在 lib/piAnalysisForensics.ts）────
const parseFailedCount = computed(() => countParseFailed(resultsData.value))
const diagnostics = computed(() => diagnosticsOf(detailData.value))
const fixPlan = computed(() => fixPlanOf(detailData.value))
const planIsEmpty = computed(() => isPlanEmpty(fixPlan.value))
const modelOutput = computed(() => modelOutputOf(detailData.value))
const parseFailed = computed(() => isParseFailed(detailData.value))

const queueMetadata = computed<Record<string, any>>(() => {
  const m = queueDetail.value?.metadata
  return m && typeof m === 'object' ? m : {}
})
const verifyHistory = computed<any[]>(() => asArray(queueMetadata.value.verifications))
const lastVerification = computed<any>(
  () => queueMetadata.value.last_verification ?? verifyHistory.value[verifyHistory.value.length - 1] ?? null,
)

const evidenceRequestFacts = computed(() => requestFacts(evidence.value?.request))
const evidenceAttempts = computed<any[]>(() => asArray(evidence.value?.upstream_attempts))
const evidenceSession = computed<any[]>(() => asArray(evidence.value?.session_context))
const evidenceWindow = computed<any[]>(() => asArray(evidence.value?.error_window))
const evidencePlaybook = computed<any[]>(() => asArray(evidence.value?.playbook_entries))
const evidenceNotes = computed<string[]>(() => asArray<string>(evidence.value?.notes))
const evidenceLogText = computed(() => serviceLogText(asArray<any>(evidence.value?.service_logs)))
// 「没搜到日志」的原因：缓冲不覆盖 / 没有请求号 / 窗内确实没有。
const evidenceLogNote = computed(() => {
  const meta = evidence.value?.service_log_meta
  if (!meta || meta.available === false) return t('common.piAnalysis.forensics.logsUnavailable')
  if (asArray(evidence.value?.service_logs).length > 0) return ''
  if (meta.covers_window === false) {
    return t('common.piAnalysis.forensics.logsNotCovered', { at: formatTime(String(meta.oldest_at ?? '')) })
  }
  if (!activeContext.value?.request_id) return t('common.piAnalysis.forensics.logsNoRequest')
  return t('common.piAnalysis.forensics.logsEmptyCovered')
})

const showJudgeOption = computed(() => supportsJudgeOption(String(activeContext.value?.error_type ?? '')))

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

// 详情请求的序号守卫：用户连点两行时，先发出的慢响应不能覆盖后一行的内容。
const detailGuard = createLatestRequestGuard()

// ★ 详情打开：结果详情与队列详情**并行**取。
//   results（有分析 → 结论 + 建议 + 应用状态）；queue（前因上下文 + 复测留痕——
//   复测记录落在队列行上，未分析的行也要能看）。结果 404 ⇒ 未分析，走队列详情。
async function openDetail(queueId: number) {
  const seq = detailGuard.next()
  activeDetailId.value = queueId
  showDetailDialog.value = true
  detailLoading.value = true
  detailError.value = ''
  detailData.value = null
  queueDetail.value = null
  evidence.value = null
  evidenceFailed.value = false
  analyzeQueued.value = false
  reparseFailed.value = false
  verifyResult.value = null
  verifyIncludeJudge.value = false
  stopPolling()
  try {
    const [res, qres] = await Promise.all([
      fetch(`/api/admin/pi-analysis/results/${queueId}`),
      fetch(`/api/admin/pi-analysis/queue/${queueId}`).catch(() => null),
    ])
    if (!detailGuard.isCurrent(seq)) return
    if (qres && qres.ok) {
      queueDetail.value = await qres.json()
    }
    if (res.status === 404) {
      if (!qres || !qres.ok) {
        throw Object.assign(new Error(`queue detail: HTTP ${qres?.status}`), { status: qres?.status ?? 0 })
      }
    } else if (!res.ok) {
      throw Object.assign(new Error(`result detail: HTTP ${res.status}`), { status: res.status })
    } else {
      detailData.value = await res.json()
    }
  } catch (error) {
    if (!detailGuard.isCurrent(seq)) return
    // ★ 用户看到的是「怎么办」；原始错误只进控制台（内部报文不进界面）。
    console.error('Failed to load detail:', error)
    detailError.value = t('common.piAnalysis.forensics.detailLoadFailed')
  } finally {
    if (detailGuard.isCurrent(seq)) {
      detailLoading.value = false
      // 有请求号或会话号的行：打开即取证（取证是独立的只读查询，不阻塞详情）。
      const ctx = detailData.value || queueDetail.value
      if (ctx?.request_id || ctx?.session_id) loadEvidence()
    }
  }
}

function viewDetail(queueId: number) {
  openDetail(queueId)
}

// ── 取证 ────────────────────────────────────────────────────────────────────

async function loadEvidence() {
  const id = activeDetailId.value
  const seq = detailGuard.next()
  evidenceLoading.value = true
  evidenceFailed.value = false
  try {
    const res = await fetch(`/api/admin/pi-analysis/queue/${id}/evidence`)
    if (!res.ok) {
      throw Object.assign(new Error(`evidence: HTTP ${res.status}`), { status: res.status })
    }
    const data = await res.json()
    if (!detailGuard.isCurrent(seq) || id !== activeDetailId.value) return
    evidence.value = data
    // 复测留痕随取证响应一起回来 ⇒ 同步进队列详情（时间线与历史立即可见）。
    if (queueDetail.value && Array.isArray(data.verifications)) {
      queueDetail.value = {
        ...queueDetail.value,
        metadata: {
          ...(queueDetail.value.metadata || {}),
          verifications: data.verifications,
          last_verification: data.last_verification,
        },
      }
    }
  } catch (error) {
    console.error('Failed to load evidence:', error)
    if (detailGuard.isCurrent(seq)) evidenceFailed.value = true
  } finally {
    if (detailGuard.isCurrent(seq)) evidenceLoading.value = false
  }
}

// ── 定向分析（这一条）：分析 / 重新分析 ────────────────────────────────────

const POLL_INTERVAL_MS = 4000
const POLL_MAX_ATTEMPTS = 45 // 3 分钟：一次分析通常一分钟内出结果
let pollTimer: ReturnType<typeof setTimeout> | null = null
let pollAttempts = 0

function stopPolling() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
  pollAttempts = 0
}

// 轮询队列状态：离开「分析中/待处理」即刷新详情并停。
async function pollQueueStatus(queueId: number) {
  pollTimer = null
  pollAttempts += 1
  if (queueId !== activeDetailId.value) return
  try {
    const res = await fetch(`/api/admin/pi-analysis/queue/${queueId}`)
    if (!res.ok) throw Object.assign(new Error(`HTTP ${res.status}`), { status: res.status })
    const item = await res.json()
    if (queueId !== activeDetailId.value) return
    if (item.status === 'analyzed' || item.status === 'failed' || pollAttempts >= POLL_MAX_ATTEMPTS) {
      analyzeQueued.value = false
      await openDetail(queueId)
      return
    }
  } catch (error) {
    console.error('Failed to poll analysis status:', error)
  }
  pollTimer = setTimeout(() => pollQueueStatus(queueId), POLL_INTERVAL_MS)
}

async function analyzeThis() {
  const id = activeDetailId.value
  analyzing.value = true
  reparseFailed.value = false
  try {
    const res = await fetch(`/api/admin/pi-analysis/queue/${id}/analyze`, { method: 'POST' })
    // 409 = 正被别的流程分析中：对用户而言同样是「已在分析」。
    if (!res.ok && res.status !== 409) {
      throw Object.assign(new Error(`HTTP ${res.status}`), { status: res.status })
    }
    if (id !== activeDetailId.value) return
    analyzeQueued.value = true
    stopPolling()
    pollTimer = setTimeout(() => pollQueueStatus(id), POLL_INTERVAL_MS)
  } catch (error) {
    console.error('Failed to start analysis:', error)
    alert(t('common.piAnalysis.forensics.analyzeFailed'))
  } finally {
    analyzing.value = false
  }
}

// ── 重解析：读已存的模型响应，不调用模型 ──────────────────────────────────

async function reparseThis() {
  const id = activeDetailId.value
  reparsing.value = true
  reparseFailed.value = false
  try {
    const res = await fetch(`/api/admin/pi-analysis/results/${id}/reparse`, { method: 'POST' })
    if (res.status === 422) {
      // 存量响应仍读不出 ⇒ 如实说，并指向下一步（带取证重新分析）。
      if (id === activeDetailId.value) reparseFailed.value = true
      return
    }
    if (!res.ok) {
      throw Object.assign(new Error(`HTTP ${res.status}`), { status: res.status })
    }
    if (id !== activeDetailId.value) return
    await openDetail(id)
  } catch (error) {
    console.error('Failed to re-parse:', error)
    if (id === activeDetailId.value) reparseFailed.value = true
  } finally {
    reparsing.value = false
  }
}

async function reparseAll() {
  bulkReparsing.value = true
  bulkReparseNote.value = ''
  try {
    const res = await fetch('/api/admin/pi-analysis/results/reparse-failed', { method: 'POST' })
    if (!res.ok) {
      throw Object.assign(new Error(`HTTP ${res.status}`), { status: res.status })
    }
    const data = await res.json()
    bulkReparseNote.value = t('common.piAnalysis.forensics.reparseAllDone', {
      ok: data.reparsed ?? 0, total: data.scanned ?? 0, left: (data.still_unparsable ?? 0) + (data.failed ?? 0),
    })
    await loadResults()
    loadStats()
  } catch (error) {
    console.error('Failed to bulk re-parse:', error)
    bulkReparseNote.value = t('common.piAnalysis.forensics.reparseAllFailed')
  } finally {
    bulkReparsing.value = false
  }
}

// ── 复测 ────────────────────────────────────────────────────────────────────

async function runVerify() {
  const id = activeDetailId.value
  verifying.value = true
  try {
    const res = await fetch(`/api/admin/pi-analysis/queue/${id}/verify`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ include_llm: verifyIncludeJudge.value }),
    })
    if (!res.ok) {
      throw Object.assign(new Error(`HTTP ${res.status}`), { status: res.status })
    }
    const data = await res.json()
    if (id !== activeDetailId.value) return
    verifyResult.value = data
    // 留痕已写进队列行 ⇒ 重取取证（它顺带带回最新的复测历史与时间线）。
    await loadEvidence()
  } catch (error) {
    console.error('Failed to run verification:', error)
    if (id === activeDetailId.value) verifyResult.value = { error: true }
  } finally {
    verifying.value = false
  }
}

// ── 复制修正方案 ────────────────────────────────────────────────────────────

function planText(): string {
  const plan = fixPlan.value
  if (!plan) return ''
  const F = 'common.piAnalysis.forensics.'
  return buildPlanText(plan, {
    title: `#${activeDetailId.value} ${formatErrorType(activeContext.value?.error_type || '')}`,
    labels: {
      planAutoTitle: t(F + 'planAutoTitle'),
      planManualTitle: t(F + 'planManualTitle'),
      planConfigTitle: t(F + 'planConfigTitle'),
      planCodeTitle: t(F + 'planCodeTitle'),
      planDeployTitle: t(F + 'planDeployTitle'),
      planDeployRequired: t(F + 'planDeployRequired'),
      planDeployNotRequired: t(F + 'planDeployNotRequired'),
      planTargets: t(F + 'planTargets'),
      planRollback: t(F + 'planRollback'),
      planVerifyTitle: t(F + 'planVerifyTitle'),
      verifyExpect: t(F + 'verifyExpect'),
      verifyMethod_rules_replay: t(F + 'verifyMethod_rules_replay'),
      verifyMethod_layer_recheck: t(F + 'verifyMethod_layer_recheck'),
      verifyMethod_recurrence: t(F + 'verifyMethod_recurrence'),
      verifyMethod_request_replay: t(F + 'verifyMethod_request_replay'),
      verifyMethod_manual: t(F + 'verifyMethod_manual'),
    },
    kinds: {
      config: t(F + 'kindConfig'), code: t(F + 'kindCode'), ops: t(F + 'kindOps'),
      upstream: t(F + 'kindUpstream'), client: t(F + 'kindClient'),
    },
    risks: { low: t(F + 'riskLow'), medium: t(F + 'riskMedium'), high: t(F + 'riskHigh') },
    actions: normalizedActions.value.map((a) => ({
      label: actionLabel(a.action), target: a.rule_name, detail: autoActionDetail(a),
    })),
  })
}

async function copyPlan() {
  const text = planText()
  if (!text) return
  copying.value = true
  try {
    await navigator.clipboard.writeText(text)
    alert(t('common.piAnalysis.forensics.planCopied'))
  } catch (error) {
    console.error('Failed to copy plan:', error)
    alert(t('common.piAnalysis.forensics.planCopyFailed'))
  } finally {
    copying.value = false
  }
}

async function copyPatch(patch: string) {
  try {
    await navigator.clipboard.writeText(patch)
    alert(t('common.piAnalysis.forensics.planCopied'))
  } catch (error) {
    console.error('Failed to copy patch:', error)
    alert(t('common.piAnalysis.forensics.planCopyFailed'))
  }
}

// ── 展示用的小映射 ──────────────────────────────────────────────────────────

function openRequest(requestId: string) {
  // 复用全仓同一入口打开请求详情页（新标签页）；弹窗的 loading 不受影响。
  openRequestDetailPage(requestId)
}

function factLabel(key: string): string {
  const keyMap: Record<string, string> = {
    time: 'factTime', model: 'factModel', protocol: 'factProtocol', status: 'factStatus',
    errorKind: 'factErrorKind', failureStage: 'factFailureStage', upstreamStatus: 'factUpstreamStatus',
    latency: 'factLatency', tokens: 'factTokens', provider: 'factProvider', credential: 'factCredential',
    agent: 'factAgent', routing: 'factRouting', interrupted: 'factInterrupted', routingOutcomes: 'factRouting',
  }
  const k = keyMap[key]
  return k ? t(`common.piAnalysis.forensics.${k}`) : key
}

function factValue(f: { key: string; value: string; raw?: Record<string, number> }): string {
  if (f.key === 'tokens' && f.raw) {
    return t('common.piAnalysis.forensics.tokensValue', f.raw)
  }
  if (f.key === 'interrupted') return t('common.piAnalysis.forensics.interruptedYes')
  if (f.key === 'time') return formatTime(f.value)
  return f.value
}

function requestStatusLabel(status: string): string {
  const keyMap: Record<string, string> = {
    success: 'requestStatusSuccess', failure: 'requestStatusFailure',
    rate_limited: 'requestStatusRateLimited', in_progress: 'requestStatusInProgress',
  }
  const k = keyMap[status]
  return k ? t(`common.piAnalysis.forensics.${k}`) : status
}

function kindLabel(kind: string): string {
  return t(`common.piAnalysis.forensics.kind${capitalize(normalizeKind(kind))}`)
}

function riskLabel(risk: string): string {
  return t(`common.piAnalysis.forensics.risk${capitalize(normalizeRisk(risk))}`)
}

function capitalize(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s
}

function checkStatusLabel(status: string): string {
  const keyMap: Record<string, string> = {
    pass: 'checkPass', fail: 'checkFail', skipped: 'checkSkipped',
    not_applicable: 'checkNotApplicable', info: 'checkInfo', error: 'checkError',
  }
  const k = keyMap[status]
  return k ? t(`common.piAnalysis.forensics.${k}`) : status
}

function checkNameLabel(name: string): string {
  const keyMap: Record<string, string> = {
    rules_replay: 'checkRulesReplay', layer_recheck: 'checkLayerRecheck', recurrence: 'checkRecurrence',
  }
  const k = keyMap[name]
  return k ? t(`common.piAnalysis.forensics.${k}`) : name
}

function verdictLabel(verdict: string | null | undefined): string {
  const keyMap: Record<string, string> = {
    not_reproducing: 'verdictNotReproducing', reproducing: 'verdictReproducing', inconclusive: 'verdictInconclusive',
  }
  const k = verdict ? keyMap[verdict] : ''
  return k ? t(`common.piAnalysis.forensics.${k}`) : t('common.piAnalysis.forensics.verdictNone')
}

function blockerLabel(blocker: string, conf: number): string {
  const k = BLOCKER_KEYS[blocker]
  if (!k) return blocker
  if (blocker === 'low_confidence') {
    return t(`common.piAnalysis.forensics.${k}`, { conf: (conf * 100).toFixed(0) + '%' })
  }
  return t(`common.piAnalysis.forensics.${k}`)
}

function verifyMethodLabel(method: string): string {
  const k = `verifyMethod_${method}`
  const text = t(`common.piAnalysis.forensics.${k}`)
  return text === `common.piAnalysis.forensics.${k}` ? method : text
}

function noneText(rules: string[]): string {
  return rules.length > 0 ? rules.join(', ') : t('common.piAnalysis.forensics.detailNone')
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
  evidence.value = null
  verifyResult.value = null
  stopPolling()
}

onBeforeUnmount(() => {
  stopPolling()
})

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
    'performance_issue': t('common.piAnalysis.page.errorTypePerformanceIssue'),
    'output_guard_blocked': t('common.piAnalysis.page.errorTypeOutputGuardBlocked'),
    'request_failed': t('common.piAnalysis.forensics.errorTypeRequestFailed')
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
