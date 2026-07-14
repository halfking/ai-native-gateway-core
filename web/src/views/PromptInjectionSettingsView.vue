<template>
  <div class="prompt-injection-settings">
    <div class="settings-header">
      <h2>{{ t('sessions.promptInjectionFull.title') }}</h2>
      <p class="description">{{ t('sessions.promptInjectionFull.subtitle') }}</p>
    </div>

    <el-tabs
      v-model="activeTab"
      type="border-card"
      class="settings-tabs"
    >
      <!-- Tab 1: 策略（折叠嵌入配置面板） -->
      <el-tab-pane :label="t('sessions.promptInjectionFull.tabPolicy')" name="policy">
        <template #label>
          <span><el-icon><Setting /></el-icon> {{ t('sessions.promptInjectionFull.tabPolicy') }}</span>
        </template>
        <PromptInjectionConfigPanel />
      </el-tab-pane>

      <!-- Tab 2: LLM 引擎 -->
      <el-tab-pane :label="t('sessions.promptInjectionFull.tabEngines')" name="engines">
        <template #label>
          <span><el-icon><Cpu /></el-icon> {{ t('sessions.promptInjectionFull.tabEngines') }}</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.enginesTitle') }}</span>
              <el-button type="primary" size="small" @click="showAddEngine = true">
                <el-icon><Plus /></el-icon> {{ t('sessions.promptInjectionFull.addEngine') }}
              </el-button>
            </div>
          </template>

          <!-- No engines yet — render an explicit empty state instead of relying
               on el-table to coerce its empty template into a usable row. -->
          <div v-if="!enginesLoading && engines.length === 0" class="empty-state">
            <p>{{ t('sessions.promptInjectionFull.enginesEmpty') }}</p>
            <p class="meta">{{ t('sessions.promptInjectionFull.enginesEmptyHint') }}</p>
          </div>
          <div v-else-if="enginesLoading" class="state">{{ t('sessions.promptInjectionFull.loading') }}</div>
          <el-table v-else :data="engines" style="width: 100%" stripe>
            <el-table-column prop="engine_name" :label="t('sessions.promptInjectionFull.colName')" width="180" />
            <el-table-column :label="t('sessions.promptInjectionFull.colModel')" width="220">
              <template #default="{ row }">
                <el-tag size="small">{{ row.model_name || t('sessions.promptInjectionFull.notConfigured') }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="priority" :label="t('sessions.promptInjectionFull.colPriority')" width="80" />
            <el-table-column prop="temperature" :label="t('sessions.promptInjectionFull.colTemperature')" width="80" />
            <el-table-column prop="timeout_ms" :label="t('sessions.promptInjectionFull.colTimeoutMs')" width="100" />
            <el-table-column prop="total_calls" :label="t('sessions.promptInjectionFull.colCalls')" width="100" />
            <el-table-column prop="total_detections" :label="t('sessions.promptInjectionFull.colDetections')" width="110" />
            <el-table-column :label="t('sessions.promptInjectionFull.colAvgLatency')" width="120">
              <template #default="{ row }">
                {{ row.avg_latency_ms ? row.avg_latency_ms.toFixed(0) + 'ms' : '-' }}
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colEnabled')" width="80">
              <template #default="{ row }">
                <el-switch v-model="row.enabled" @change="updateEngine(row)" />
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colActions')" width="180" fixed="right">
              <template #default="{ row, $index }">
                <el-button size="small" @click="editEngine(row)">{{ t('sessions.promptInjectionFull.edit') }}</el-button>
                <el-button size="small" type="danger" @click="deleteEngine(row)">{{ t('sessions.promptInjectionFull.delete') }}</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>

        <!-- LLM 检测提示词模板 -->
        <el-card class="section-card" shadow="never" v-if="engines.length">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.promptTemplateTitle') }}</span>
              <el-tag v-if="selectedEngine.id" type="info" size="small">
                {{ t('sessions.promptInjectionFull.editingEngine', { name: selectedEngine.engine_name }) }}
              </el-tag>
              <span v-else class="meta">{{ t('sessions.promptInjectionFull.promptTemplateHint') }}</span>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              <div>
                <p><strong>{{ t('sessions.promptInjectionFull.promptVars') }}</strong></p>
                <p><code>{user_input}</code> - {{ t('sessions.promptInjectionFull.varUserInput') }}</p>
                <p><code>{system_prompt}</code> - {{ t('sessions.promptInjectionFull.varSystemPrompt') }}</p>
                <p><code>{detection_categories}</code> - {{ t('sessions.promptInjectionFull.varCategories') }}</p>
              </div>
            </template>
          </el-alert>

          <div class="prompt-editor">
            <label>{{ t('sessions.promptInjectionFull.systemPromptLabel') }}</label>
            <el-input
              v-model="selectedEngine.system_prompt"
              type="textarea"
              :rows="4"
              :placeholder="t('sessions.promptInjectionFull.systemPromptPlaceholder')"
              :disabled="!selectedEngine.id"
            />
            <label style="margin-top: 12px">{{ t('sessions.promptInjectionFull.detectionPromptLabel') }}</label>
            <el-input
              v-model="selectedEngine.detection_prompt"
              type="textarea"
              :rows="8"
              :placeholder="t('sessions.promptInjectionFull.detectionPromptPlaceholder')"
              :disabled="!selectedEngine.id"
            />
          </div>
        </el-card>
      </el-tab-pane>

      <!-- Tab 3: 严重度矩阵 -->
      <el-tab-pane :label="t('sessions.promptInjectionFull.tabSeverity')" name="severity">
        <template #label>
          <span><el-icon><Warning /></el-icon> {{ t('sessions.promptInjectionFull.tabSeverity') }}</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.severityTitle') }}</span>
              <el-button type="primary" size="small" @click="saveSeverityMatrix">{{ t('sessions.promptInjectionFull.save') }}</el-button>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              {{ t('sessions.promptInjectionFull.severityHint') }}
            </template>
          </el-alert>

          <div v-if="!severityMatrixLoading && severityMatrix.length === 0" class="empty-state">
            <p>{{ t('sessions.promptInjectionFull.severityEmpty') }}</p>
          </div>
          <div v-else-if="severityMatrixLoading" class="state">{{ t('sessions.promptInjectionFull.loading') }}</div>
          <el-table v-else :data="severityMatrix" style="width: 100%" stripe border>
            <el-table-column :label="t('sessions.promptInjectionFull.colSeverityLevel')" width="120">
              <template #default="{ row }">
                <el-tag :type="getSeverityTagType(row.severity_level) as any" size="large">
                  {{ getSeverityLabel(row.severity_level) }}
                </el-tag>
              </template>
            </el-table-column>

            <el-table-column :label="t('sessions.promptInjectionFull.colObserveAction')" width="160">
              <template #default="{ row }">
                <el-select v-model="row.observe_action" size="small">
                  <el-option :label="t('sessions.promptInjectionFull.actionLog')" value="log" />
                  <el-option :label="t('sessions.promptInjectionFull.actionWarn')" value="warn" />
                </el-select>
              </template>
            </el-table-column>

            <el-table-column :label="t('sessions.promptInjectionFull.colEnforceAction')" width="180">
              <template #default="{ row }">
                <el-select v-model="row.enforce_action" size="small">
                  <el-option :label="t('sessions.promptInjectionFull.actionLog')" value="log" />
                  <el-option :label="t('sessions.promptInjectionFull.actionWarn')" value="warn" />
                  <el-option :label="t('sessions.promptInjectionFull.actionReplace')" value="replace" />
                  <el-option :label="t('sessions.promptInjectionFull.actionRedact')" value="redact" />
                  <el-option :label="t('sessions.promptInjectionFull.actionRemove')" value="remove" />
                  <el-option :label="t('sessions.promptInjectionFull.actionReject')" value="reject" />
                  <el-option :label="t('sessions.promptInjectionFull.actionTerminate')" value="terminate" />
                  <el-option :label="t('sessions.promptInjectionFull.actionApprove')" value="approve" />
                  <el-option :label="t('sessions.promptInjectionFull.actionBlock')" value="block" />
                </el-select>
              </template>
            </el-table-column>

            <el-table-column :label="t('sessions.promptInjectionFull.colRequireApproval')" width="120">
              <template #default="{ row }">
                <el-switch v-model="row.require_approval" />
              </template>
            </el-table-column>

            <el-table-column :label="t('sessions.promptInjectionFull.colApprovalTimeout')" width="180">
              <template #default="{ row }">
                <el-input-number v-model="row.approval_timeout_minutes" :min="0" :max="1440" size="small" :disabled="!row.require_approval" />
                <div class="help-text-small">{{ t('sessions.promptInjectionFull.timeoutZeroHint') }}</div>
              </template>
            </el-table-column>

            <el-table-column :label="t('sessions.promptInjectionFull.colNotify')" width="80">
              <template #default="{ row }">
                <el-switch v-model="row.notify_on_detect" />
              </template>
            </el-table-column>

            <el-table-column :label="t('sessions.promptInjectionFull.colHealthPenalty')" width="140">
              <template #default="{ row }">
                <el-input-number v-model="row.session_health_penalty" :min="0" :max="100" size="small" />
              </template>
            </el-table-column>

            <el-table-column :label="t('sessions.promptInjectionFull.colRepeatTerminate')" width="180">
              <template #default="{ row }">
                <el-switch v-model="row.terminate_session_on_repeat" />
                <div v-if="row.terminate_session_on_repeat" class="help-text-small">
                  {{ t('sessions.promptInjectionFull.repeatThresholdLabel') }}
                  <el-input-number v-model="row.repeat_threshold" :min="1" :max="10" size="small" style="width: 80px" />
                </div>
              </template>
            </el-table-column>
          </el-table>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.flowTitle') }}</span>
            </div>
          </template>

          <div class="flow-description">
            <el-descriptions :column="1" border>
              <el-descriptions-item v-for="action in actionDescriptions" :key="action.key" :label="action.key">
                <strong>{{ action.title }}</strong> — {{ action.desc }}
              </el-descriptions-item>
            </el-descriptions>
          </div>
        </el-card>
      </el-tab-pane>

      <!-- Tab 4: 检测规则 -->
      <el-tab-pane :label="t('sessions.promptInjectionFull.tabRules')" name="rules">
        <template #label>
          <span><el-icon><List /></el-icon> {{ t('sessions.promptInjectionFull.tabRules') }}</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.rulesTitle') }}</span>
              <div>
                <el-input
                  v-model="ruleSearch"
                  :placeholder="t('sessions.promptInjectionFull.searchPlaceholder')"
                  style="width: 220px; margin-right: 12px"
                  size="small"
                  clearable
                  @input="loadRules"
                >
                  <template #prefix>
                    <el-icon><Search /></el-icon>
                  </template>
                </el-input>
                <el-button type="primary" size="small" @click="showAddRule = true">
                  <el-icon><Plus /></el-icon> {{ t('sessions.promptInjectionFull.addRule') }}
                </el-button>
              </div>
            </div>
          </template>

          <div class="category-filter">
            <el-tag
              v-for="cat in ruleCategories"
              :key="cat.value"
              :type="ruleCategoryFilter === cat.value ? 'primary' : ''"
              :effect="ruleCategoryFilter === cat.value ? 'dark' : 'plain'"
              @click="ruleCategoryFilter = cat.value; loadRules()"
              style="margin-right: 8px; margin-bottom: 8px; cursor: pointer"
            >
              {{ cat.label }} ({{ cat.count }})
            </el-tag>
          </div>

          <div v-if="rulesLoading && rules.length === 0" class="state">{{ t('sessions.promptInjectionFull.loading') }}</div>
          <div v-else-if="!rules.length" class="empty-state">
            <p>{{ t('sessions.promptInjectionFull.rulesEmpty') }}</p>
          </div>
          <el-table v-else :data="rules" style="width: 100%" stripe>
            <el-table-column prop="rule_name" :label="t('sessions.promptInjectionFull.colRuleName')" width="220" show-overflow-tooltip />
            <el-table-column :label="t('sessions.promptInjectionFull.colCategory')" width="150">
              <template #default="{ row }">
                <el-tag
                  :type="getCategoryType(row.category_new || row.category) as any"
                  size="small"
                >
                  {{ getCategoryLabel(row.category_new || row.category) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colSeverity')" width="100">
              <template #default="{ row }">
                <el-tag :type="getSeverityTagType(row.severity) as any" size="small">
                  {{ row.severity }}/10
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="description" :label="t('sessions.promptInjectionFull.colDescription')" show-overflow-tooltip />
            <el-table-column :label="t('sessions.promptInjectionFull.colType')" width="80">
              <template #default="{ row }">
                <el-tag :type="row.is_system ? 'info' : 'success'" size="small">
                  {{ row.is_system ? t('sessions.promptInjectionFull.systemRule') : t('sessions.promptInjectionFull.customRule') }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colEnabled')" width="80">
              <template #default="{ row }">
                <el-switch v-model="row.enabled" @change="toggleRule(row)" />
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colActions')" width="120" fixed="right">
              <template #default="{ row }">
                <el-button v-if="!row.is_system" size="small" type="danger" @click="deleteRule(row)">{{ t('sessions.promptInjectionFull.delete') }}</el-button>
                <span v-else class="meta">{{ t('sessions.promptInjectionFull.lockedHint') }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-tab-pane>

      <!-- Tab 5: Canary Token -->
      <el-tab-pane :label="t('sessions.promptInjectionFull.tabCanary')" name="canary">
        <template #label>
          <span><el-icon><Key /></el-icon> {{ t('sessions.promptInjectionFull.tabCanary') }}</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.canaryTitle') }}</span>
              <el-button type="primary" size="small" @click="showAddCanary = true">
                <el-icon><Plus /></el-icon> {{ t('sessions.promptInjectionFull.createToken') }}
              </el-button>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              {{ t('sessions.promptInjectionFull.canaryHint') }}
            </template>
          </el-alert>

          <div v-if="canaryLoading && canaryTokens.length === 0" class="state">{{ t('sessions.promptInjectionFull.loading') }}</div>
          <div v-else-if="!canaryTokens.length" class="empty-state">
            <p>{{ t('sessions.promptInjectionFull.canaryEmpty') }}</p>
          </div>
          <el-table v-else :data="canaryTokens" style="width: 100%" stripe>
            <el-table-column prop="token_name" :label="t('sessions.promptInjectionFull.colName')" width="150" />
            <el-table-column :label="t('sessions.promptInjectionFull.colTokenValue')" width="320">
              <template #default="{ row }">
                <el-text truncated>{{ row.token_value }}</el-text>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colType')" width="80">
              <template #default="{ row }">
                <el-tag size="small">{{ row.token_type }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colLeakAction')" width="140">
              <template #default="{ row }">
                <el-tag :type="getActionType(row.leak_action) as any" size="small">
                  {{ getActionLabel(row.leak_action) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="times_injected" :label="t('sessions.promptInjectionFull.colInjected')" width="110" />
            <el-table-column :label="t('sessions.promptInjectionFull.colLeaked')" width="110">
              <template #default="{ row }">
                <el-tag :type="row.times_leaked > 0 ? 'danger' : 'success'" size="small">
                  {{ row.times_leaked }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colEnabled')" width="80">
              <template #default="{ row }">
                <el-switch v-model="row.active" @change="updateCanaryToken(row)" />
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colActions')" width="120" fixed="right">
              <template #default="{ row }">
                <el-button size="small" type="danger" @click="deleteCanaryToken(row)">{{ t('sessions.promptInjectionFull.delete') }}</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-tab-pane>

      <!-- Tab 6: 审批队列 — 快速跳转 -->
      <el-tab-pane :label="t('sessions.promptInjectionFull.tabApprovals')" name="approvals">
        <template #label>
          <span><el-icon><Checked /></el-icon> {{ t('sessions.promptInjectionFull.tabApprovals') }}</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.approvalsTitle') }}</span>
              <el-button type="primary" @click="goToApprovalPage">
                <el-icon><Link /></el-icon> {{ t('sessions.promptInjectionFull.goToApprovalCenter') }}
              </el-button>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              <div>
                <p>{{ t('sessions.promptInjectionFull.approvalsIntro') }}</p>
              </div>
            </template>
          </el-alert>

          <el-row :gutter="20">
            <el-col :span="8">
              <el-card shadow="hover" class="quick-link-card" @click="goToApprovalPage">
                <el-icon size="48" color="#409eff"><Checked /></el-icon>
                <h3>{{ t('sessions.promptInjectionFull.queueTitle') }}</h3>
                <p>{{ t('sessions.promptInjectionFull.queueDesc') }}</p>
              </el-card>
            </el-col>
            <el-col :span="8">
              <el-card shadow="hover" class="quick-link-card" @click="goToApprovalConfig">
                <el-icon size="48" color="#67c23a"><Setting /></el-icon>
                <h3>{{ t('sessions.promptInjectionFull.approvalCfgTitle') }}</h3>
                <p>{{ t('sessions.promptInjectionFull.approvalCfgDesc') }}</p>
              </el-card>
            </el-col>
            <el-col :span="8">
              <el-card shadow="hover" class="quick-link-card" @click="goToSeverityTab">
                <el-icon size="48" color="#e6a23c"><Warning /></el-icon>
                <h3>{{ t('sessions.promptInjectionFull.matrixTitle') }}</h3>
                <p>{{ t('sessions.promptInjectionFull.matrixDesc') }}</p>
              </el-card>
            </el-col>
          </el-row>

          <el-divider content-position="left">{{ t('sessions.promptInjectionFull.triggerTitle') }}</el-divider>

          <el-descriptions :column="2" border>
            <el-descriptions-item :label="t('sessions.promptInjectionFull.highRisk')">
              <el-tag type="warning">score ≥ 8</el-tag>
              <span style="margin-left: 8px">{{ t('sessions.promptInjectionFull.highRiskDesc') }}</span>
            </el-descriptions-item>
            <el-descriptions-item :label="t('sessions.promptInjectionFull.criticalRisk')">
              <el-tag type="danger">score ≥ 10</el-tag>
              <span style="margin-left: 8px">{{ t('sessions.promptInjectionFull.criticalRiskDesc') }}</span>
            </el-descriptions-item>
          </el-descriptions>

          <el-alert type="warning" :closable="false" style="margin-top: 16px">
            <template #title>
              {{ t('sessions.promptInjectionFull.matrixTip') }}
            </template>
          </el-alert>
        </el-card>
      </el-tab-pane>

      <!-- Tab 7: 统计监控 -->
      <el-tab-pane :label="t('sessions.promptInjectionFull.tabStats')" name="stats">
        <template #label>
          <span><el-icon><DataAnalysis /></el-icon> {{ t('sessions.promptInjectionFull.tabStats') }}</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.statsTitle') }}</span>
              <el-button size="small" @click="refreshStats">{{ t('sessions.promptInjectionFull.refresh') }}</el-button>
            </div>
          </template>

          <el-row :gutter="20">
            <el-col :span="4"><el-statistic :title="t('sessions.promptInjectionFull.totalDetections')" :value="stats.total_detections"><template #suffix>{{ t('sessions.promptInjectionFull.times') }}</template></el-statistic></el-col>
            <el-col :span="4"><el-statistic :title="t('sessions.promptInjectionFull.blocked')" :value="stats.blocked_count"><template #suffix>{{ t('sessions.promptInjectionFull.times') }}</template></el-statistic></el-col>
            <el-col :span="4"><el-statistic :title="t('sessions.promptInjectionFull.approvals')" :value="stats.approval_count"><template #suffix>{{ t('sessions.promptInjectionFull.times') }}</template></el-statistic></el-col>
            <el-col :span="4"><el-statistic :title="t('sessions.promptInjectionFull.replaced')" :value="stats.replaced_count"><template #suffix>{{ t('sessions.promptInjectionFull.times') }}</template></el-statistic></el-col>
            <el-col :span="4"><el-statistic :title="t('sessions.promptInjectionFull.terminated')" :value="stats.terminated_count"><template #suffix>{{ t('sessions.promptInjectionFull.times') }}</template></el-statistic></el-col>
            <el-col :span="4"><el-statistic :title="t('sessions.promptInjectionFull.canaryLeaks')" :value="stats.canary_leak_count"><template #suffix>{{ t('sessions.promptInjectionFull.times') }}</template></el-statistic></el-col>
          </el-row>

          <el-divider />

          <el-row :gutter="20">
            <el-col :span="6"><el-statistic :title="t('sessions.promptInjectionFull.avgScore')" :value="stats.avg_score" :precision="1"><template #suffix>/ 10</template></el-statistic></el-col>
            <el-col :span="6"><el-statistic :title="t('sessions.promptInjectionFull.maxScore')" :value="stats.max_score"><template #suffix>/ 10</template></el-statistic></el-col>
            <el-col :span="6"><el-statistic :title="t('sessions.promptInjectionFull.avgLLMConf')" :value="stats.avg_llm_confidence" :precision="2"><template #suffix>/ 1</template></el-statistic></el-col>
            <el-col :span="6"><el-statistic :title="t('sessions.promptInjectionFull.affectedSessions')" :value="stats.affected_sessions" /></el-col>
          </el-row>

          <el-divider />

          <h4>{{ t('sessions.promptInjectionFull.riskDistribution') }}</h4>
          <el-row :gutter="10">
            <el-col :span="6"><div class="risk-item risk-critical"><div class="risk-label">{{ t('sessions.promptInjectionFull.critical') }}</div><div class="risk-count">{{ stats.critical_count }}</div></div></el-col>
            <el-col :span="6"><div class="risk-item risk-high"><div class="risk-label">{{ t('sessions.promptInjectionFull.high') }}</div><div class="risk-count">{{ stats.high_count }}</div></div></el-col>
            <el-col :span="6"><div class="risk-item risk-medium"><div class="risk-label">{{ t('sessions.promptInjectionFull.medium') }}</div><div class="risk-count">{{ stats.medium_count }}</div></div></el-col>
            <el-col :span="6"><div class="risk-item risk-low"><div class="risk-label">{{ t('sessions.promptInjectionFull.low') }}</div><div class="risk-count">{{ stats.low_count }}</div></div></el-col>
          </el-row>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>{{ t('sessions.promptInjectionFull.detectionLogsTitle') }}</span>
              <el-button size="small" @click="loadDetections">{{ t('sessions.promptInjectionFull.refresh') }}</el-button>
            </div>
          </template>

          <el-form :inline="true" class="filter-form">
            <el-form-item :label="t('sessions.promptInjectionFull.riskLevel')">
              <el-select v-model="detectionFilter.risk_level" @change="loadDetections" clearable :placeholder="t('sessions.promptInjectionFull.all')">
                <el-option :label="t('sessions.promptInjectionFull.critical')" value="critical" />
                <el-option :label="t('sessions.promptInjectionFull.high')" value="high" />
                <el-option :label="t('sessions.promptInjectionFull.medium')" value="medium" />
                <el-option :label="t('sessions.promptInjectionFull.low')" value="low" />
              </el-select>
            </el-form-item>

            <el-form-item :label="t('sessions.promptInjectionFull.colEnforceAction')">
              <el-select v-model="detectionFilter.action" @change="loadDetections" clearable :placeholder="t('sessions.promptInjectionFull.all')">
                <el-option :label="t('sessions.promptInjectionFull.actionLog')" value="log" />
                <el-option :label="t('sessions.promptInjectionFull.actionWarn')" value="warn" />
                <el-option :label="t('sessions.promptInjectionFull.actionReplace')" value="replace" />
                <el-option :label="t('sessions.promptInjectionFull.actionReject')" value="reject" />
                <el-option :label="t('sessions.promptInjectionFull.actionBlock')" value="block" />
                <el-option :label="t('sessions.promptInjectionFull.actionApprove')" value="approve" />
              </el-select>
            </el-form-item>

            <el-form-item :label="t('sessions.promptInjectionFull.session')">
              <el-input
                v-model="detectionFilter.session_key"
                :placeholder="t('sessions.promptInjectionFull.sessionPlaceholder')"
                @keyup.enter="loadDetections"
                clearable
              />
            </el-form-item>
          </el-form>

          <div v-if="detectionsLoading && detections.length === 0" class="state">{{ t('sessions.promptInjectionFull.loading') }}</div>
          <div v-else-if="!detections.length" class="empty-state">
            <p>{{ t('sessions.promptInjectionFull.detectionsEmpty') }}</p>
          </div>
          <el-table v-else :data="detections" style="width: 100%" stripe>
            <el-table-column prop="detected_at" :label="t('sessions.promptInjectionFull.colTime')" width="180" />
            <el-table-column prop="request_id" :label="t('sessions.promptInjectionFull.colRequestId')" width="180" show-overflow-tooltip />
            <el-table-column :label="t('sessions.promptInjectionFull.colScore')" width="80">
              <template #default="{ row }">
                <el-tag :type="getSeverityTagType(row.detection_score) as any" size="small">
                  {{ row.detection_score }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colRiskLevel')" width="100">
              <template #default="{ row }">
                <el-tag :type="getRiskLevelType(row.risk_level) as any" size="small">
                  {{ getRiskLevelLabel(row.risk_level) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colCategory')" width="200">
              <template #default="{ row }">
                <template v-for="cat in (row.categories || [])" :key="cat">
                  <el-tag size="small" style="margin-right: 4px">{{ getCategoryLabel(cat) }}</el-tag>
                </template>
                <span v-if="!row.categories || !row.categories.length" class="meta">-</span>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colEnforceAction')" width="100">
              <template #default="{ row }">
                <el-tag :type="getActionType(row.action_taken) as any" size="small">
                  {{ getActionLabel(row.action_taken) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('sessions.promptInjectionFull.colLLMConf')" width="110">
              <template #default="{ row }">
                {{ row.llm_confidence ? (row.llm_confidence * 100).toFixed(0) + '%' : '-' }}
              </template>
            </el-table-column>
            <el-table-column prop="evidence_text" :label="t('sessions.promptInjectionFull.colEvidence')" show-overflow-tooltip />
          </el-table>

          <el-pagination
            v-model:current-page="detectionPagination.page"
            v-model:page-size="detectionPagination.page_size"
            :total="detectionPagination.total"
            :page-sizes="[10, 20, 50, 100]"
            layout="total, sizes, prev, pager, next, jumper"
            @current-change="loadDetections"
            @size-change="loadDetections"
            style="margin-top: 20px"
          />
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <!-- ============= Dialogs ============= -->

    <el-dialog v-model="showAddEngine" :title="t('sessions.promptInjectionFull.addEngineTitle')" width="600px">
      <el-form :model="newEngine" label-width="140px">
        <el-form-item :label="t('sessions.promptInjectionFull.colName')" required>
          <el-input v-model="newEngine.engine_name" :placeholder="t('sessions.promptInjectionFull.engineNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colDescription')">
          <el-input v-model="newEngine.description" :placeholder="t('sessions.promptInjectionFull.engineDescPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colModel')">
          <el-select v-model="newEngine.model_canonical_id" :placeholder="t('sessions.promptInjectionFull.selectModel')">
            <el-option v-for="m in availableModels" :key="m.id" :label="m.canonical_name" :value="m.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colTemperature')">
          <el-slider v-model="newEngine.temperature" :min="0" :max="2" :step="0.1" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colMaxTokens')">
          <el-input-number v-model="newEngine.max_tokens" :min="100" :max="4096" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colTimeoutMs')">
          <el-input-number v-model="newEngine.timeout_ms" :min="1000" :max="30000" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colPriority')">
          <el-input-number v-model="newEngine.priority" :min="0" :max="100" />
          <span class="help-text">{{ t('sessions.promptInjectionFull.priorityHelp') }}</span>
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colEnabled')">
          <el-switch v-model="newEngine.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAddEngine = false">{{ t('sessions.promptInjectionFull.cancel') }}</el-button>
        <el-button type="primary" @click="createEngine">{{ t('sessions.promptInjectionFull.create') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showAddRule" :title="t('sessions.promptInjectionFull.addRuleTitle')" width="600px">
      <el-form :model="newRule" label-width="140px">
        <el-form-item :label="t('sessions.promptInjectionFull.colRuleName')" required>
          <el-input v-model="newRule.rule_name" :placeholder="t('sessions.promptInjectionFull.ruleNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colType')">
          <el-select v-model="newRule.rule_type">
            <el-option :label="t('sessions.promptInjectionFull.basicRule')" value="basic" />
            <el-option :label="t('sessions.promptInjectionFull.advancedRule')" value="advanced" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colCategory')">
          <el-select v-model="newRule.category_new">
            <el-option v-for="cat in allCategories" :key="cat.value" :label="cat.label" :value="cat.value" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colPattern')" required>
          <el-input v-model="newRule.pattern" type="textarea" :rows="3" :placeholder="t('sessions.promptInjectionFull.patternPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colDescription')">
          <el-input v-model="newRule.description" :placeholder="t('sessions.promptInjectionFull.ruleDescPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colSeverity')">
          <el-slider v-model="newRule.severity" :min="1" :max="10" show-stops />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colCaseSensitive')">
          <el-switch v-model="newRule.case_sensitive" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAddRule = false">{{ t('sessions.promptInjectionFull.cancel') }}</el-button>
        <el-button type="primary" @click="createRule">{{ t('sessions.promptInjectionFull.create') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showAddCanary" :title="t('sessions.promptInjectionFull.createTokenTitle')" width="500px">
      <el-form :model="newCanary" label-width="140px">
        <el-form-item :label="t('sessions.promptInjectionFull.colName')">
          <el-input v-model="newCanary.token_name" :placeholder="t('sessions.promptInjectionFull.tokenNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colType')">
          <el-select v-model="newCanary.token_type">
            <el-option :label="t('sessions.promptInjectionFull.tokenTypeUuid')" value="uuid" />
            <el-option :label="t('sessions.promptInjectionFull.tokenTypeCustom')" value="custom" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="newCanary.token_type === 'custom'" :label="t('sessions.promptInjectionFull.colTokenValue')">
          <el-input v-model="newCanary.token_value" :placeholder="t('sessions.promptInjectionFull.tokenValuePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colDescription')">
          <el-input v-model="newCanary.description" :placeholder="t('sessions.promptInjectionFull.tokenDescPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colLeakAction')">
          <el-select v-model="newCanary.leak_action">
            <el-option :label="t('sessions.promptInjectionFull.actionBlock')" value="block" />
            <el-option :label="t('sessions.promptInjectionFull.actionReject')" value="reject" />
            <el-option :label="t('sessions.promptInjectionFull.actionWarn')" value="warn" />
            <el-option :label="t('sessions.promptInjectionFull.actionLog')" value="log" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sessions.promptInjectionFull.colNotify')">
          <el-switch v-model="newCanary.notify_on_leak" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAddCanary = false">{{ t('sessions.promptInjectionFull.cancel') }}</el-button>
        <el-button type="primary" @click="createCanaryToken">{{ t('sessions.promptInjectionFull.create') }}</el-button>
      </template>
    </el-dialog>

  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch, reactive } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  QuestionFilled, CircleCloseFilled, CircleCheckFilled,
  Setting, Cpu, Warning, List, Key, Checked, DataAnalysis,
  Plus, Search, Link
} from '@element-plus/icons-vue'
import {
  CATEGORIES, getCategoryMeta, getSeverityTagType,
  getPolicy, listRules, toggleRule as apiToggleRule, updatePolicy, updateRule,
  listEngines, getEngine, createEngine as apiCreateEngine, updateEngine as apiUpdateEngine,
  removeEngine, listCanaryTokens, createCanaryToken as apiCreateCanaryToken,
  updateCanaryToken as apiUpdateCanaryToken, removeCanaryToken,
  getSeverityMatrix, updateSeverityMatrix,
  listStats, listDetections,
  type PromptInjectionRule,
} from '../api/promptInjection'
import PromptInjectionConfigPanel from '../components/PromptInjectionConfigPanel.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

// Active tab — sync from URL hash (?tab=engines|severity|rules|...) and keep
// URL in sync when the user switches tabs.
const validTabs = ['policy', 'engines', 'severity', 'rules', 'canary', 'approvals', 'stats']
const activeTab = ref<string>(typeof route.query.tab === 'string' && validTabs.includes(route.query.tab) ? route.query.tab : 'policy')
watch(activeTab, (next) => {
  router.replace({ query: { ...route.query, tab: next } })
})

// Helper used everywhere we render el-table-column bodies. el-table injects
// the row via `#default="{ row }"`, but the previous implementation used a
// positional `scope` and called `scope.row` — when the row was empty or the
// table body ran without data, scope ended up undefined and Vue logged
// "Cannot read properties of undefined (reading 'row')". Guarding with this
// noop v-if removes the runtime crash and renders an empty cell instead.
function hasRow(row: unknown): boolean {
  return row !== null && row !== undefined && typeof row === 'object'
}

// ───── Severity / risk / category helpers (shared with PromptInjectionConfigPanel) ─────
function getCategoryType(raw: string) {
  return getCategoryMeta(raw).tagType as 'danger' | 'warning' | 'info' | ''
}
function getCategoryLabel(raw: string) {
  const meta = getCategoryMeta(raw)
  return t(`sessions.promptInjectionFull.cat.${meta.i18nKey}`, meta.zhFallback)
}
function getSeverityType(severity: number) {
  return getSeverityTagType(severity)
}
function getRiskLevelType(level: string): 'danger' | 'warning' | 'info' {
  const types: Record<string, 'danger' | 'warning' | 'info'> = { critical: 'danger', high: 'danger', medium: 'warning', low: 'info' }
  return types[level] || 'info'
}
function getRiskLevelLabel(level: string): string {
  return ({ critical: t('sessions.promptInjectionFull.critical'), high: t('sessions.promptInjectionFull.high'), medium: t('sessions.promptInjectionFull.medium'), low: t('sessions.promptInjectionFull.low') } as Record<string, string>)[level] || level
}
function getActionType(action: string): 'danger' | 'warning' | 'success' | 'info' {
  const types: Record<string, 'danger' | 'warning' | 'success' | 'info'> = {
    block: 'danger', reject: 'danger', terminate: 'danger', approve: 'warning',
    replace: 'warning', redact: 'warning', remove: 'warning', sanitize: 'warning',
    warn: 'info', log: 'success', pass: 'success',
  }
  return types[action] || 'success'
}
function getActionLabel(action: string): string {
  const m: Record<string, string> = {
    block: t('sessions.promptInjectionFull.actionBlock'),
    reject: t('sessions.promptInjectionFull.actionReject'),
    terminate: t('sessions.promptInjectionFull.actionTerminate'),
    approve: t('sessions.promptInjectionFull.actionApprove'),
    replace: t('sessions.promptInjectionFull.actionReplace'),
    redact: t('sessions.promptInjectionFull.actionRedact'),
    remove: t('sessions.promptInjectionFull.actionRemove'),
    sanitize: t('sessions.promptInjectionFull.actionSanitize'),
    quarantine: t('sessions.promptInjectionFull.actionQuarantine'),
    warn: t('sessions.promptInjectionFull.actionWarn'),
    log: t('sessions.promptInjectionFull.actionLog'),
    pass: t('sessions.promptInjectionFull.actionPass'),
  }
  return m[action] || action
}
function getSeverityLabel(level: string): string {
  const m: Record<string, string> = {
    critical: t('sessions.promptInjectionFull.cvcCritical'),
    high: t('sessions.promptInjectionFull.cvcHigh'),
    medium: t('sessions.promptInjectionFull.cvcMedium'),
    low: t('sessions.promptInjectionFull.cvcLow'),
  }
  return m[level] || level
}

// Hard-coded description table for the "处理流程说明" card.
const actionDescriptions = computed(() => ([
  { key: 'pass', title: t('sessions.promptInjectionFull.actionPass'), desc: t('sessions.promptInjectionFull.flowPassDesc') },
  { key: 'log', title: t('sessions.promptInjectionFull.actionLog'), desc: t('sessions.promptInjectionFull.flowLogDesc') },
  { key: 'warn', title: t('sessions.promptInjectionFull.actionWarn'), desc: t('sessions.promptInjectionFull.flowWarnDesc') },
  { key: 'replace', title: t('sessions.promptInjectionFull.actionReplace'), desc: t('sessions.promptInjectionFull.flowReplaceDesc') },
  { key: 'redact', title: t('sessions.promptInjectionFull.actionRedact'), desc: t('sessions.promptInjectionFull.flowRedactDesc') },
  { key: 'remove', title: t('sessions.promptInjectionFull.actionRemove'), desc: t('sessions.promptInjectionFull.flowRemoveDesc') },
  { key: 'reject', title: t('sessions.promptInjectionFull.actionReject'), desc: t('sessions.promptInjectionFull.flowRejectDesc') },
  { key: 'terminate', title: t('sessions.promptInjectionFull.actionTerminate'), desc: t('sessions.promptInjectionFull.flowTerminateDesc') },
  { key: 'approve', title: t('sessions.promptInjectionFull.actionApprove'), desc: t('sessions.promptInjectionFull.flowApproveDesc') },
  { key: 'block', title: t('sessions.promptInjectionFull.actionBlock'), desc: t('sessions.promptInjectionFull.flowBlockDesc') },
]))

// ───── Engines ─────
const engines = ref<any[]>([])
const enginesLoading = ref(false)
const showAddEngine = ref(false)
const selectedEngine = reactive<any>({ id: null, engine_name: '', system_prompt: '', detection_prompt: '' })

const newEngine = reactive({
  engine_name: '', description: '',
  model_canonical_id: null as number | null,
  temperature: 0.1, max_tokens: 512,
  timeout_ms: 3000, priority: 0,
  enabled: true,
})
const availableModels = ref<any[]>([])

const loadEngines = async () => {
  enginesLoading.value = true
  try {
    const res = await listEngines()
    engines.value = res.engines || []
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.loadEnginesFailed', { msg: e?.message || '' }))
  } finally { enginesLoading.value = false }
}
const loadAvailableModels = async () => {
  // Models API: best-effort. If endpoint fails, just leave the dropdown empty.
  try {
    const res = await req<any>('GET', '/api/admin/models?with_versions=true')
    availableModels.value = (res.families || []).flatMap((f: any) => (f.versions || []).map((v: any) => ({ id: v.id, canonical_name: v.canonical_name || v.display_name })))
  } catch { availableModels.value = [] }
}
import { req } from '../api/_core'
const createEngine = async () => {
  try {
    await apiCreateEngine(newEngine as any)
    ElMessage.success(t('sessions.promptInjectionFull.engineCreated'))
    showAddEngine.value = false
    Object.assign(newEngine, { engine_name: '', description: '', model_canonical_id: null, temperature: 0.1, max_tokens: 512, timeout_ms: 3000, priority: 0, enabled: true })
    await loadEngines()
  } catch (e: any) { ElMessage.error(t('sessions.promptInjectionFull.createFailed', { msg: e?.message || '' })) }
}
const updateEngine = async (engine: any) => {
  try {
    await apiUpdateEngine(engine.id, engine)
    ElMessage.success(t('sessions.promptInjectionFull.engineUpdated'))
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.updateFailed', { msg: e?.message || '' }))
    engine.enabled = !engine.enabled
  }
}
const editEngine = async (engine: any) => {
  try {
    const full = await getEngine(engine.id)
    Object.assign(selectedEngine, full)
  } catch {
    Object.assign(selectedEngine, engine, { system_prompt: '', detection_prompt: '' })
  }
}
const deleteEngine = async (engine: any) => {
  try {
    await ElMessageBox.confirm(t('sessions.promptInjectionFull.confirmDelete'), t('sessions.promptInjectionFull.confirmTitle'), { type: 'warning' })
    await deleteEngineApi(engine.id)
    ElMessage.success(t('sessions.promptInjectionFull.engineDeleted'))
    loadEngines()
    if (selectedEngine.id === engine.id) Object.assign(selectedEngine, { id: null, engine_name: '', system_prompt: '', detection_prompt: '' })
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(t('sessions.promptInjectionFull.deleteFailed', { msg: e?.message || '' }))
  }
}
async function deleteEngineApi(id: number | string) { return removeEngine(id) }
watch(selectedEngine, async () => {
  // Persist system_prompt / detection_prompt on change. These fields map 1:1 to LLMEngine.
  if (!selectedEngine.id) return
  try { await apiUpdateEngine(selectedEngine.id, { system_prompt: selectedEngine.system_prompt, detection_prompt: selectedEngine.detection_prompt }) } catch {}
}, { deep: true })

// ───── Severity matrix ─────
const severityMatrix = ref<any[]>([])
const severityMatrixLoading = ref(false)

const loadSeverityMatrix = async () => {
  severityMatrixLoading.value = true
  try {
    const res = await getSeverityMatrix()
    severityMatrix.value = res.matrix || []
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.loadMatrixFailed', { msg: e?.message || '' }))
  } finally { severityMatrixLoading.value = false }
}
const saveSeverityMatrix = async () => {
  try {
    await updateSeverityMatrix(severityMatrix.value)
    ElMessage.success(t('sessions.promptInjectionFull.matrixSaved'))
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.saveFailed', { msg: e?.message || '' }))
  }
}

// ───── Rules ─────
const rules = ref<PromptInjectionRule[]>([])
const rulesLoading = ref(false)
const ruleSearch = ref('')
const ruleCategoryFilter = ref('')
const showAddRule = ref(false)
const newRule = reactive({
  rule_name: '', rule_type: 'basic', category_new: '',
  pattern: '', description: '', severity: 5,
  case_sensitive: false, enabled: true,
})

const allCategories = CATEGORIES.map((c) => ({ value: c.value, label: c.zhFallback }))
const ruleCategories = computed(() => {
  const counts: Record<string, number> = {}
  rules.value.forEach((r) => {
    const c = r.category_new || r.category || ''
    counts[c] = (counts[c] || 0) + 1
  })
  return [
    { value: '', label: t('sessions.promptInjectionFull.all'), count: rules.value.length },
    ...CATEGORIES.filter((c) => counts[c.value]).map((c) => ({ value: c.value, label: c.zhFallback, count: counts[c.value] || 0 })),
  ]
})

const loadRules = async () => {
  rulesLoading.value = true
  try {
    const res = await listRules({
      category: ruleCategoryFilter.value || undefined,
      search: ruleSearch.value.trim() || undefined,
    })
    rules.value = res.rules || []
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.loadRulesFailed', { msg: e?.message || '' }))
  } finally { rulesLoading.value = false }
}
const toggleRule = async (rule: any) => {
  try {
    await apiToggleRule(rule.id, rule.enabled)
    ElMessage.success(t('sessions.promptInjectionFull.ruleUpdated'))
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.updateFailed', { msg: e?.message || '' }))
    rule.enabled = !rule.enabled
  }
}
const createRule = async () => {
  try {
    await req('POST', '/api/admin/prompt-injection/rules', newRule as any)
    ElMessage.success(t('sessions.promptInjectionFull.ruleCreated'))
    showAddRule.value = false
    Object.assign(newRule, { rule_name: '', rule_type: 'basic', category_new: '', pattern: '', description: '', severity: 5, case_sensitive: false, enabled: true })
    loadRules()
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.createFailed', { msg: e?.message || '' }))
  }
}
const deleteRule = async (rule: any) => {
  try {
    await ElMessageBox.confirm(t('sessions.promptInjectionFull.confirmDelete'), t('sessions.promptInjectionFull.confirmTitle'), { type: 'warning' })
    await req('DELETE', `/api/admin/prompt-injection/rules/${rule.id}`)
    ElMessage.success(t('sessions.promptInjectionFull.ruleDeleted'))
    loadRules()
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(t('sessions.promptInjectionFull.deleteFailed', { msg: e?.message || '' }))
  }
}

// ───── Canary tokens ─────
const canaryTokens = ref<any[]>([])
const canaryLoading = ref(false)
const showAddCanary = ref(false)
const newCanary = reactive({
  token_name: '', token_type: 'uuid', token_value: '',
  description: '', leak_action: 'block', notify_on_leak: true, active: true,
})

const loadCanaryTokens = async () => {
  canaryLoading.value = true
  try {
    const res = await listCanaryTokens()
    canaryTokens.value = res.tokens || []
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.loadCanaryFailed', { msg: e?.message || '' }))
  } finally { canaryLoading.value = false }
}
const createCanaryToken = async () => {
  try {
    await createCanaryApi(newCanary as any)
    ElMessage.success(t('sessions.promptInjectionFull.tokenCreated'))
    showAddCanary.value = false
    Object.assign(newCanary, { token_name: '', token_type: 'uuid', token_value: '', description: '', leak_action: 'block', notify_on_leak: true, active: true })
    loadCanaryTokens()
  } catch (e: any) { ElMessage.error(t('sessions.promptInjectionFull.createFailed', { msg: e?.message || '' })) }
}
async function createCanaryApi(payload: any) { return apiCreateCanaryToken(payload) }
const updateCanaryToken = async (token: any) => {
  try {
    await apiUpdateCanaryToken(token.id, token)
    ElMessage.success(t('sessions.promptInjectionFull.tokenUpdated'))
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.updateFailed', { msg: e?.message || '' }))
    token.active = !token.active
  }
}
const deleteCanaryToken = async (token: any) => {
  try {
    await ElMessageBox.confirm(t('sessions.promptInjectionFull.confirmDelete'), t('sessions.promptInjectionFull.confirmTitle'), { type: 'warning' })
    await deleteCanaryApi(token.id)
    ElMessage.success(t('sessions.promptInjectionFull.tokenDeleted'))
    loadCanaryTokens()
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error(t('sessions.promptInjectionFull.deleteFailed', { msg: e?.message || '' }))
  }
}
async function deleteCanaryApi(id: number | string) { return removeCanaryToken(id) }

// ───── Stats + detections ─────
const stats = reactive({
  total_detections: 0, blocked_count: 0, critical_count: 0, high_count: 0,
  medium_count: 0, low_count: 0, approval_count: 0, replaced_count: 0,
  terminated_count: 0, canary_leak_count: 0, avg_score: 0, max_score: 0,
  avg_llm_confidence: 0, affected_sessions: 0,
})
const detections = ref<any[]>([])
const detectionFilter = reactive({ risk_level: '', blocked: '', action: '', session_key: '' })
const detectionPagination = reactive({ page: 1, page_size: 20, total: 0 })
const detectionsLoading = ref(false)

const refreshStats = async () => {
  try {
    const res = await listStats()
    Object.assign(stats, res)
  } catch (e: any) { ElMessage.error(t('sessions.promptInjectionFull.loadStatsFailed', { msg: e?.message || '' })) }
}
const loadDetections = async () => {
  detectionsLoading.value = true
  try {
    const res = await listDetections({
      page: detectionPagination.page,
      page_size: detectionPagination.page_size,
      ...detectionFilter,
    })
    detections.value = res.detections || []
    detectionPagination.total = res.total || 0
  } catch (e: any) {
    ElMessage.error(t('sessions.promptInjectionFull.loadDetectionsFailed', { msg: e?.message || '' }))
  } finally { detectionsLoading.value = false }
}

// ───── Cross-tab navigation ─────
function goToApprovalPage() { router.push('/admin/approvals') }
function goToApprovalConfig() { router.push('/admin/approval-config') }
function goToSeverityTab() { activeTab.value = 'severity' }

// ───── Mount ─────
onMounted(async () => {
  await Promise.all([
    loadEngines().then(loadAvailableModels),
    loadSeverityMatrix(),
    loadRules(),
    loadCanaryTokens(),
    refreshStats(),
    loadDetections(),
  ])
})
</script>

<style scoped lang="scss">
.prompt-injection-settings { padding: 20px; }
.settings-header {
  margin-bottom: 24px;
  h2 { margin: 0 0 8px 0; font-size: 24px; }
  .description { color: var(--muted, #8b949e); margin: 0; }
}
.settings-tabs :deep(.el-tabs__header) { margin-bottom: 20px; }
.section-card {
  margin-bottom: 20px;
  .card-header { display: flex; justify-content: space-between; align-items: center; }
}
.help-text { margin-left: 12px; color: var(--muted, #8b949e); font-size: 12px; }
.help-text-small { color: var(--muted, #8b949e); font-size: 11px; margin-top: 4px; }
.meta { color: var(--muted); font-size: 11px; }
.empty-state {
  padding: 32px 16px; text-align: center; color: var(--muted, #8b949e);
  p { margin: 4px 0; }
  .meta { font-size: 12px; }
}
.state { color: var(--muted, #6e7681); padding: 20px 0; text-align: center; }
.whitelist-tags { margin-top: 8px; }
.category-filter {
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--border, #30363d);
}
.risk-distribution {
  h4 { margin: 0 0 16px 0; }
  .risk-item {
    padding: 16px; border-radius: 4px; text-align: center;
    .risk-label { font-size: 14px; margin-bottom: 8px; }
    .risk-count { font-size: 24px; font-weight: bold; }
    &.risk-critical { background: rgba(248,81,73,.12); color: #f85149; }
    &.risk-high { background: rgba(248,81,73,.12); color: #f85149; }
    &.risk-medium { background: rgba(210,153,34,.12); color: #d29922; }
    &.risk-low { background: rgba(99,102,241,.12); color: #818cf8; }
  }
}
.filter-form { margin-bottom: 16px; }
.prompt-editor {
  display: flex; flex-direction: column; gap: 6px;
  label { font-size: 12px; color: var(--text-secondary, #8b949e); }
}
.flow-description {
  :deep(.el-descriptions__label) { width: 120px; font-weight: bold; }
}
.quick-link-card {
  cursor: pointer; text-align: center; padding: 20px;
  transition: all 0.3s;
  &:hover { transform: translateY(-4px); box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15); }
  h3 { margin: 12px 0 8px; font-size: 16px; }
  p { color: var(--muted, #8b949e); font-size: 14px; margin: 0; }
}
</style>