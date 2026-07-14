#!/usr/bin/env node
/**
 * sync-sessions-locale-leaks.mjs — patch Chinese leaks in sessions.ts locale files.
 */
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { auditFlatByLocale, rootConfigByLocale } from './sync-sessions-locale-leaks-data.mjs'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')
const CHINESE_RE = /[\u4e00-\u9fff]/
const TARGET_LOCALES = ['en-US', 'de-DE', 'fr-FR', 'es-ES', 'ja-JP', 'ar-SA', 'zh-TW']
const FOREIGN_LOCALES = TARGET_LOCALES.filter((l) => l !== 'en-US' && l !== 'zh-TW')

const S2T_PAIRS = [
  ['会话', '會話'], ['加载', '載入'], ['压缩', '壓縮'], ['审批', '審批'], ['设置', '設定'],
  ['统计', '統計'], ['导出', '匯出'], ['审计', '稽核'], ['记录', '記錄'], ['规则', '規則'],
  ['启用', '啟用'], ['禁用', '停用'], ['保存', '儲存'], ['风险', '風險'], ['检测', '偵測'],
  ['严格', '嚴格'], ['拦截', '攔截'], ['建议', '建議'], ['仅', '僅'], ['升级', '升級'],
  ['意图', '意圖'], ['纳入', '納入'], ['天数', '天數'], ['脱敏', '脫敏'], ['数据', '資料'],
  ['小时', '小時'], ['总', '總'], ['活跃', '活躍'], ['趋势', '趨勢'], ['任务', '任務'],
  ['客户端', '客戶端'], ['失败', '失敗'], ['配置', '設定'], ['基本', '基本'], ['超时', '逾時'],
  ['自动', '自動'], ['拒绝', '拒絕'], ['通过', '通過'], ['错误', '錯誤'], ['放弃', '放棄'],
  ['性能', '效能'], ['延迟', '延遲'], ['阈值', '閾值'], ['切换', '切換'], ['频繁', '頻繁'],
  ['提示', '提示'], ['注入', '注入'], ['有毒', '有毒'], ['输出', '輸出'], ['敏感', '敏感'],
  ['内容', '內容'], ['即将', '即將'], ['上线', '上線'], ['位于', '位於'], ['页面', '頁面'],
  ['此处', '此處'], ['预览', '預覽'], ['策略', '策略'], ['名称', '名稱'], ['描述', '描述'],
  ['状态', '狀態'], ['查看', '檢視'], ['详情', '詳情'], ['滑动', '滑動'], ['窗口', '視窗'],
  ['总结', '總結'], ['恢复', '恢復'], ['默认', '預設'], ['请求', '請求'], ['暂无', '暫無'],
  ['平台', '平台'], ['租户', '租戶'], ['分区', '分區'], ['交接', '交接'], ['总结', '總結'],
  ['引擎', '引擎'], ['模型', '模型'], ['厂商', '廠商'], ['移除', '移除'], ['只读', '唯讀'],
  ['能力', '能力'], ['健康', '健康'], ['检查', '檢查'], ['监控', '監控'], ['上下文', '上下文'],
  ['警告', '警告'], ['行为', '行為'], ['识别', '識別'], ['不活跃', '不活躍'], ['生命周期', '生命週期'],
  ['合规', '合規'], ['安全', '安全'], ['联动', '聯動'], ['结果', '結果'], ['分数', '分數'],
  ['写入', '寫入'], ['分析', '分析'], ['中心', '中心'], ['打开', '打開'], ['数组', '陣列'],
  ['格式', '格式'], ['角色', '角色'], ['名称', '名稱'], ['列表', '清單'], ['渠道', '渠道'],
  ['通知', '通知'], ['泄漏', '洩漏'], ['尝试', '嘗試'], ['越狱', '越獄'], ['词', '詞'],
  ['最少', '最少'], ['人数', '人數'], ['延迟', '延遲'], ['日志', '日誌'], ['中', '中'],
]

function toTraditional(text) {
  if (!text) return text
  let out = text
  for (const [from, to] of S2T_PAIRS) out = out.split(from).join(to)
  return out
}

const SIMPLIFIED_MARKERS = /加载|会话|压缩|审批|设置|统计|导出|审计|记录|配置中|失败|启用|禁用|保存|风险|检测|严格|拦截|建议|仅审计|升级|意图|纳入|保留天数|脱敏|总成本|活跃会话|任务ID|客户端ID/

function buildZhTwMaps(content) {
  const zhcnPath = path.join(localesDir, 'zh-CN/sessions.ts')
  const zhcnCfg = extractSection(
    fs.readFileSync(zhcnPath, 'utf8'),
    /\n  config: \{([\s\S]*?\n  \},\n  management:)/,
  )
  const auditCfg = extractSection(content, /    config: \{([\s\S]*?\n    \},\n  promptInjectionFull:)/)
  const statsBlock = extractSection(content, /\n  stats: \{([\s\S]*?\n  \},)/)

  const rootCfg = {}
  for (const k of CONFIG_NESTED_KEYS) {
    let v = readKey(auditCfg, k)
    if (!v || SIMPLIFIED_MARKERS.test(v)) {
      const fromCn = readKey(zhcnCfg, k)
      v = fromCn ? toTraditional(fromCn) : v ? toTraditional(v) : v
    }
    if (v) rootCfg[k] = v
  }

  const auditFlat = {
    audit_tabs_audit: '稽核記錄',
    audit_tabs_config: '審批設定',
    audit_export: '匯出 CSV',
  }
  for (const k of STATS_NESTED_KEYS) {
    const v = readKey(statsBlock, k)
    if (v) auditFlat[`stats_${k}`] = v
  }
  for (const [, flatKey] of AUDIT_CONFIG_TO_FLAT) {
    if (!flatKey.startsWith('audit_config_')) continue
    const key = flatKey.slice('audit_config_'.length)
    let v = readKey(auditCfg, key)
    if (v && CHINESE_RE.test(v)) v = toTraditional(v)
    if (v) auditFlat[flatKey] = v
  }

  const auditNested = {
    export: auditFlat.audit_export,
    tabs: { audit: auditFlat.audit_tabs_audit, config: auditFlat.audit_tabs_config },
    config: {},
  }
  for (const [nested, flatKey] of AUDIT_CONFIG_TO_FLAT) {
    if (!nested.startsWith('config.')) continue
    const key = nested.slice('config.'.length)
    if (auditFlat[flatKey]) auditNested.config[key] = auditFlat[flatKey]
  }

  return { rootCfg, auditFlat, auditNested }
}

function escapeRe(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function fmtVal(v) {
  if (v.includes("'") && !v.includes('"')) return `"${v}"`
  return `'${v.replace(/'/g, "\\'")}'`
}

function countChineseLines(text) {
  return text.split('\n').filter((l) => CHINESE_RE.test(l)).length
}

function formatObjBlock(name, obj) {
  const lines = Object.entries(obj).map(([k, v]) => `    ${k}: ${fmtVal(v)},`)
  return `${name}: {\n${lines.join('\n')}\n  },`
}

function readKey(block, key) {
  const sq = new RegExp(`${escapeRe(key)}:\\s*'((?:\\\\'|[^'])*)'`)
  const dq = new RegExp(`${escapeRe(key)}:\\s*"((?:\\\\"|[^"])*)"`)
  let m = block.match(sq)
  if (m) return m[1].replace(/\\'/g, "'")
  m = block.match(dq)
  return m ? m[1].replace(/\\"/g, '"') : null
}

function extractSection(content, re) {
  const m = content.match(re)
  return m ? m[1] : ''
}

const PROMPT_INJECTION_CATEGORIES = {
  promptInjectionCategoryInstructionOverride: 'Instruction override',
  promptInjectionCategoryInstructionLeak: 'Instruction leak',
  promptInjectionCategoryJailbreak: 'Jailbreak',
  promptInjectionCategoryEncodingBypass: 'Encoding bypass',
  promptInjectionCategoryInjectionMarker: 'Injection marker',
  promptInjectionCategoryMultiTurnAttack: 'Multi-turn attack',
  promptInjectionCategoryResourceExhaustion: 'Resource exhaustion',
  promptInjectionCategoryDataExfiltration: 'Data exfiltration',
  promptInjectionCategorySocialEngineering: 'Social engineering',
  promptInjectionCategoryPromptLeaking: 'Prompt leaking',
  promptInjectionCategoryPayloadSmuggling: 'Payload smuggling',
  promptInjectionCategoryUnicodeObfuscation: 'Unicode obfuscation',
  promptInjectionCategoryContextManipulation: 'Context manipulation',
  promptInjectionCategoryToolAbuse: 'Tool abuse',
  promptInjectionCategoryLegacy: 'Legacy',
  promptInjectionCategoryUnknown: 'Other',
}

const PROMPT_INJECTION_FULL_EN = {
  title: 'Prompt Injection Detection',
  subtitle: 'Multi-layer defense: rule engine + LLM detection + vector similarity + canary tokens',
  loading: 'Loading…',
  tabPolicy: 'Policy',
  tabEngines: 'LLM Engines',
  tabSeverity: 'Severity Matrix',
  tabRules: 'Detection Rules',
  tabCanary: 'Canary Tokens',
  tabApprovals: 'Approvals',
  tabStats: 'Statistics',
  enginesTitle: 'LLM Engines',
  addEngine: 'Add Engine',
  enginesEmpty: 'No engines configured',
  enginesEmptyHint: 'Click "Add Engine" in the top-right to create the first engine',
  colName: 'Name',
  colModel: 'Model',
  colPriority: 'Priority',
  colTemperature: 'Temperature',
  colTimeoutMs: 'Timeout (ms)',
  colCalls: 'Calls',
  colDetections: 'Detections',
  colAvgLatency: 'Avg Latency',
  colEnabled: 'Enabled',
  colActions: 'Actions',
  colDescription: 'Description',
  colType: 'Type',
  colSeverity: 'Severity',
  colSeverityLevel: 'Severity Level',
  colObserveAction: 'Observe Action',
  colEnforceAction: 'Enforce Action',
  colRequireApproval: 'Require Approval',
  colApprovalTimeout: 'Approval Timeout (min)',
  colNotify: 'Notify',
  colHealthPenalty: 'Health Penalty',
  colRepeatTerminate: 'Repeat → Terminate',
  colRuleName: 'Rule Name',
  colCategory: 'Risk Category',
  colTokenValue: 'Token Value',
  colLeakAction: 'Leak Action',
  colInjected: 'Injected',
  colLeaked: 'Leaked',
  colCaseSensitive: 'Case Sensitive',
  colPattern: 'Regex',
  colMaxTokens: 'Max Tokens',
  colTime: 'Time',
  colRequestId: 'Request ID',
  colScore: 'Score',
  colRiskLevel: 'Risk Level',
  colLLMConf: 'LLM Confidence',
  colEvidence: 'Evidence',
  notConfigured: 'Not configured',
  notAvailable: '—',
  engineCreated: 'Engine created',
  engineUpdated: 'Engine updated',
  engineDeleted: 'Engine deleted',
  ruleCreated: 'Rule created',
  ruleDeleted: 'Rule deleted',
  ruleUpdated: 'Rule updated',
  tokenCreated: 'Token created',
  tokenDeleted: 'Token deleted',
  tokenUpdated: 'Token updated',
  matrixSaved: 'Matrix saved',
  loadEnginesFailed: 'Failed to load engines: {msg}',
  loadMatrixFailed: 'Failed to load matrix: {msg}',
  loadRulesFailed: 'Failed to load rules: {msg}',
  loadCanaryFailed: 'Failed to load tokens: {msg}',
  loadStatsFailed: 'Failed to load stats: {msg}',
  loadDetectionsFailed: 'Failed to load detection logs: {msg}',
  createFailed: 'Create failed: {msg}',
  updateFailed: 'Update failed: {msg}',
  saveFailed: 'Save failed: {msg}',
  deleteFailed: 'Delete failed: {msg}',
  promptTemplateTitle: 'LLM Detection Prompt Template',
  editingEngine: 'Editing: {name}',
  promptTemplateHint: 'Click an engine in the list to edit system and detection prompts',
  promptVars: 'Available variables:',
  varUserInput: 'User input',
  varSystemPrompt: 'System prompt (if any)',
  varCategories: 'Detection categories',
  systemPromptLabel: 'System prompt',
  systemPromptPlaceholder: 'System prompt',
  detectionPromptLabel: 'Detection prompt',
  detectionPromptPlaceholder: 'Detection prompt',
  priorityHelp: 'Higher = preferred',
  addEngineTitle: 'Add LLM Engine',
  engineNamePlaceholder: 'e.g. gpt4o-detection',
  engineDescPlaceholder: 'Engine purpose',
  selectModel: 'Select model',
  severityTitle: 'Severity Action Matrix',
  save: 'Save',
  severityHint: 'Configure how each severity level is handled in observe vs enforce mode. Approval timeout 0 = unlimited.',
  severityEmpty: 'Severity matrix not configured yet',
  cvcCritical: 'Critical',
  cvcHigh: 'High',
  cvcMedium: 'Medium',
  cvcLow: 'Low',
  flowTitle: 'Processing Flow',
  flowPassDesc: 'Pass — no risk, process normally',
  flowLogDesc: 'Log — record only, no impact on request',
  flowWarnDesc: 'Warn — log + X-Security-Warning response header',
  flowReplaceDesc: 'Replace — rewrite with LLM and continue',
  flowRedactDesc: 'Redact — replace sensitive info with [REDACTED]',
  flowRemoveDesc: 'Remove — strip malicious fragment from input',
  flowRejectDesc: 'Reject — return HTTP 403',
  flowTerminateDesc: 'Terminate — end session',
  flowApproveDesc: 'Approve — pause for human review',
  flowBlockDesc: 'Block — block immediately and log',
  rulesTitle: 'Detection Rules',
  searchPlaceholder: 'Search rule name or description',
  addRule: 'Add Rule',
  addRuleTitle: 'Add Detection Rule',
  ruleNamePlaceholder: 'e.g. custom_injection_1',
  patternPlaceholder: 'Regular expression',
  ruleDescPlaceholder: 'Rule description',
  basicRule: 'Basic rule',
  advancedRule: 'Advanced rule',
  systemRule: 'System',
  customRule: 'Custom',
  lockedHint: 'System rule',
  rulesEmpty: 'No rules configured yet',
  all: 'All',
  canaryTitle: 'Canary Token Management',
  createToken: 'Create Token',
  canaryHint: 'Canary tokens are markers placed in the system prompt. Detection indicates prompt leakage.',
  canaryEmpty: 'No canary tokens configured yet',
  createTokenTitle: 'Create Canary Token',
  tokenNamePlaceholder: 'e.g. main-prompt-canary',
  tokenTypeUuid: 'UUID',
  tokenTypeCustom: 'Custom',
  tokenValuePlaceholder: 'Custom token value',
  tokenDescPlaceholder: 'Token purpose',
  actionLog: 'Log',
  actionWarn: 'Warn',
  actionReplace: 'Replace',
  actionRedact: 'Redact',
  actionRemove: 'Remove',
  actionSanitize: 'Sanitize',
  actionQuarantine: 'Quarantine',
  actionReject: 'Reject',
  actionTerminate: 'Terminate',
  actionApprove: 'Approve',
  actionBlock: 'Block',
  actionPass: 'Pass',
  approvalsTitle: 'Approval Management',
  goToApprovalCenter: 'Go to Approval Center',
  approvalsIntro: 'High-risk prompt-injection requests are automatically queued for approval.',
  queueTitle: 'Queue',
  queueDesc: 'View and process pending approval requests',
  approvalCfgTitle: 'Configuration',
  approvalCfgDesc: 'Configure approval rules and approvers',
  matrixTitle: 'Severity Matrix',
  matrixDesc: 'Actions per severity level',
  triggerTitle: 'Triggers',
  highRisk: 'High Risk',
  highRiskDesc: 'Approval required',
  criticalRisk: 'Critical Risk',
  criticalRiskDesc: 'Approval or block',
  matrixTip: 'Configure severity levels requiring approval and timeout behavior in the matrix tab.',
  statsTitle: "Today's Statistics",
  refresh: 'Refresh',
  totalDetections: 'Total',
  blocked: 'Blocked',
  approvals: 'Approvals',
  replaced: 'Replaced',
  terminated: 'Terminated',
  canaryLeaks: 'Canary Leaks',
  times: '',
  avgScore: 'Avg Score',
  maxScore: 'Max Score',
  avgLLMConf: 'Avg LLM Confidence',
  affectedSessions: 'Affected Sessions',
  riskDistribution: 'Risk Distribution',
  critical: 'Critical',
  high: 'High',
  medium: 'Medium',
  low: 'Low',
  detectionLogsTitle: 'Detection Logs',
  riskLevel: 'Risk Level',
  session: 'Session',
  sessionPlaceholder: 'Session key',
  detectionsEmpty: 'No detections',
  timeoutZeroHint: '0 = unlimited',
  repeatThresholdLabel: 'Threshold:',
  edit: 'Edit',
  delete: 'Delete',
  create: 'Create',
  cancel: 'Cancel',
  confirmTitle: 'Confirm',
  confirmDelete: 'Delete this item?',
}

const STATS_NESTED_KEYS = [
  'totalSessions', 'activeSessions', 'healthDistribution', 'costTrend', 'trendChart',
  'last7Days', 'last30Days', 'cost', 'sessionCount', 'topClients', 'topTasks',
  'clientId', 'taskId', 'totalCost', 'avgHealth', 'loadFailed',
]

const CONFIG_NESTED_KEYS = [
  'title', 'subtitle', 'loading', 'approvalTab', 'compressionTab', 'healthTab', 'basicSettings',
  'approvalMode', 'timeout', 'timeoutAction', 'modeDisabled', 'modeDisabledDesc', 'modeAutomatic',
  'modeAutomaticDesc', 'modeManual', 'modeManualDesc', 'timeoutApprove', 'timeoutApproveDesc',
  'timeoutReject', 'timeoutRejectDesc', 'loadError', 'saveError', 'saveSuccess', 'healthInfo',
  'outcomeRules', 'errorEndedPenalty', 'errorEndedHint', 'abandonedPenalty', 'abandonedHint',
  'errorRules', 'perErrorPenalty', 'perErrorHint', 'perErrorCap', 'performanceRules',
  'highLatencyThreshold', 'highLatencyPenalty', 'modelSwitchThreshold', 'modelSwitchPenalty',
  'securityRules', 'promptInjectionPenalty', 'piiPenalty', 'toxicOutputPenalty',
  'sensitivePenaltyCap', 'sensitivePenaltyCapHint', 'comingSoon', 'compressionInfo',
  'compressionStrategies', 'strategyName', 'strategyDescription', 'status', 'enabled', 'disabled',
  'viewDetails', 'strategyToken', 'strategySummary', 'strategyMemora', 'compressionTip',
  'compressionEnabledLabel', 'compressionEnabledHint', 'compressionModeLabel', 'compressionModeHint',
  'compressionWindowLabel', 'compressionWindowHint', 'compressionModelLabel', 'compressionModelHint',
  'compressionModelPlaceholder', 'advancedTitle', 'handoffEnabledLabel', 'handoffEnabledHint',
  'handoffThresholdLabel', 'handoffThresholdHint', 'saving', 'revertToDefault', 'statsTitle',
  'statsTotalRequests', 'statsCompressed', 'statsRate', 'statsSaved', 'statsViewFull',
  'statsNoData', 'statsLoadError', 'platformScope', 'tenantScope', 'tabsLabel',
  'compressionSection', 'compressionSectionHint', 'handoffSection', 'handoffSectionHint',
  'summaryEngineLabel', 'summaryEngineHint', 'summaryModelLabel', 'summaryModelHint',
  'summaryModelFallback', 'summaryKeepLabel', 'summaryKeepHint', 'modelContextUnknown',
  'modelVendorUnknown', 'modelNone', 'removeModel', 'healthReadonly', 'healthTitle',
  'healthReadonlyHint', 'moduleEnabled', 'healthMetricToken', 'healthMetricTokenHint',
  'healthMetricLatency', 'healthMetricLatencyHint', 'healthMetricCompliance',
  'healthMetricComplianceHint', 'healthMetricOutcome', 'healthMetricOutcomeHint', 'healthSource',
  'healthViewAnalytics',
]

const AUDIT_CONFIG_TO_FLAT = [
  ['tabs.audit', 'audit_tabs_audit'],
  ['tabs.config', 'audit_tabs_config'],
  ['export', 'audit_export'],
  ['config.loading', 'audit_config_loading'],
  ['config.saveSuccess', 'audit_config_saveSuccess'],
  ['config.saving', 'audit_config_saving'],
  ['config.save', 'audit_config_save'],
  ['config.detection', 'audit_config_detection'],
  ['config.detectorModels', 'audit_config_detectorModels'],
  ['config.detectorModelsHint', 'audit_config_detectorModelsHint'],
  ['config.detectPromptInjection', 'audit_config_detectPromptInjection'],
  ['config.detectPII', 'audit_config_detectPII'],
  ['config.detectJailbreak', 'audit_config_detectJailbreak'],
  ['config.thresholds', 'audit_config_thresholds'],
  ['config.enforcementLevel', 'audit_config_enforcementLevel'],
  ['config.enforcementStrict', 'audit_config_enforcementStrict'],
  ['config.enforcementAdvisory', 'audit_config_enforcementAdvisory'],
  ['config.enforcementAudit', 'audit_config_enforcementAudit'],
  ['config.approvalThreshold', 'audit_config_approvalThreshold'],
  ['config.autoBlockThreshold', 'audit_config_autoBlockThreshold'],
  ['config.approvalFlow', 'audit_config_approvalFlow'],
  ['config.approvalTimeout', 'audit_config_approvalTimeout'],
  ['config.timeoutAction', 'audit_config_timeoutAction'],
  ['config.timeoutDeny', 'audit_config_timeoutDeny'],
  ['config.timeoutEscalate', 'audit_config_timeoutEscalate'],
  ['config.timeoutAutoApprove', 'audit_config_timeoutAutoApprove'],
  ['config.minApprovals', 'audit_config_minApprovals'],
  ['config.approverRoles', 'audit_config_approverRoles'],
  ['config.approverRolesHint', 'audit_config_approverRolesHint'],
  ['config.escalation', 'audit_config_escalation'],
  ['config.escalationEnabled', 'audit_config_escalationEnabled'],
  ['config.escalationAfter', 'audit_config_escalationAfter'],
  ['config.escalationApprovers', 'audit_config_escalationApprovers'],
  ['config.notification', 'audit_config_notification'],
  ['config.notifyChannels', 'audit_config_notifyChannels'],
  ['config.notifyChannelsHint', 'audit_config_notifyChannelsHint'],
  ['config.intentAnalysis', 'audit_config_intentAnalysis'],
  ['config.requireIntentAnalysis', 'audit_config_requireIntentAnalysis'],
  ['config.intentWeight', 'audit_config_intentWeight'],
  ['config.auditSettings', 'audit_config_auditSettings'],
  ['config.retentionDays', 'audit_config_retentionDays'],
  ['config.maskSensitiveData', 'audit_config_maskSensitiveData'],
  ['config.hours', 'audit_config_hours'],
]

function readNested(content, dotted) {
  const parts = dotted.split('.')
  let block = extractSection(content, /audit: \{([\s\S]*?\n  \}),/)
  if (!block) return null
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i]
    const re = new RegExp(`${escapeRe(part)}: \\{([\\s\\S]*?)\\n\\s{${4 + i * 2}}\\},`)
    const m = block.match(re)
    if (m) {
      block = m[1]
      continue
    }
    return readKey(block, part)
  }
  return null
}

function buildFlatMapsFromNested(content) {
  const statsBlock = extractSection(content, /\n  stats: \{([\s\S]*?\n  \},)/)
  const configBlock = extractSection(content, /\n  config: \{([\s\S]*?\n  \},\n(?:  promptInjectionFull|  management):)/)
  const auditBlock = extractSection(content, /audit: \{([\s\S]*?\n  \},)/)

  const flat = {}

  for (const k of STATS_NESTED_KEYS) {
    const v = readKey(statsBlock, k)
    if (v) flat[`stats_${k}`] = v
  }

  for (const k of CONFIG_NESTED_KEYS) {
    const v = readKey(configBlock, k)
    if (v) flat[`config_${k}`] = v
  }

  for (const [nested, flatKey] of AUDIT_CONFIG_TO_FLAT) {
    const v = readNested(`audit: {${auditBlock}`, nested) ?? readNested(`audit: {${auditBlock}`, nested)
    if (!v) {
      const [a, b] = nested.split('.')
      if (b) {
        const cfg = extractSection(`audit: {${auditBlock}`, /config: \{([\s\S]*?\n    \},)/)
        const tabs = extractSection(`audit: {${auditBlock}`, /tabs: \{([\s\S]*?\n    \},)/)
        if (a === 'tabs') flat[flatKey] = readKey(tabs, b)
        else if (a === 'config') flat[flatKey] = readKey(cfg, b)
        else flat[flatKey] = readKey(auditBlock, nested)
      } else {
        flat[flatKey] = readKey(auditBlock, nested)
      }
    } else {
      flat[flatKey] = v
    }
  }

  // audit export + tabs
  flat.audit_export = readKey(auditBlock, 'export') ?? flat.audit_export
  const tabs = extractSection(`audit: {${auditBlock}`, /tabs: \{([\s\S]*?\n    \},)/)
  flat.audit_tabs_audit = readKey(tabs, 'audit') ?? flat.audit_tabs_audit
  flat.audit_tabs_config = readKey(tabs, 'config') ?? flat.audit_tabs_config
  const auditCfg = extractSection(`audit: {${auditBlock}`, /config: \{([\s\S]*?\n    \},)/)
  for (const [, flatKey] of AUDIT_CONFIG_TO_FLAT.filter(([, k]) => k.startsWith('audit_config_'))) {
    const nestedKey = flatKey.replace('audit_config_', '')
    const v = readKey(auditCfg, nestedKey)
    if (v) flat[flatKey] = v
  }

  return { flat, configBlock, statsBlock, auditCfg }
}

const CATEGORIES_BLOCK_RE = /promptInjectionCategories: \{[\s\S]*?\n  \},\n/
const PROMPT_FULL_BLOCK_RE = /promptInjectionFull: \{[\s\S]*?confirmDelete:[^\n]+\n  \},\n/

function replaceBlock(content, blockRe, newBlock, label) {
  if (!blockRe.test(content)) return { content, patched: [] }
  const next = content.replace(blockRe, newBlock)
  return { content: next, patched: next !== content ? [label] : [] }
}

function patchKeyValues(content, patches, indent, onlyChinese = true) {
  const patched = []
  let next = content
  for (const [key, val] of Object.entries(patches)) {
    if (!val) continue
    const pad = ' '.repeat(indent)
    const sqRe = new RegExp(`(^${pad}${escapeRe(key)}:\\s*)'((?:\\\\'|[^'])*)'`, 'm')
    const dqRe = new RegExp(`(^${pad}${escapeRe(key)}:\\s*)"((?:\\\\"|[^"])*)"`, 'm')
    let m = next.match(sqRe) ?? next.match(dqRe)
    if (!m) continue
    const currentValue = m[2].replace(/\\'/g, "'").replace(/\\"/g, '"')
    if (onlyChinese && !CHINESE_RE.test(currentValue)) continue
    if (currentValue === val) continue
    next = next.replace(m[0], `${m[1]}${fmtVal(val)}`)
    patched.push(key)
  }
  return { content: next, patched }
}

function auditNestedPatches(flat) {
  const config = {}
  for (const [k, v] of Object.entries(flat)) {
    if (k.startsWith('audit_config_')) config[k.slice('audit_config_'.length)] = v
  }
  return {
    export: flat.audit_export,
    tabs: { audit: flat.audit_tabs_audit, config: flat.audit_tabs_config },
    config,
  }
}

function patchWithinAuditBlock(content, patches) {
  const m = content.match(/(  audit: \{)([\s\S]*?)(\n  \},)/)
  if (!m) return { content, patched: [] }
  let inner = m[2]
  const patched = []

  if (patches.export) {
    const r = patchKeyValues(inner, { export: patches.export }, 4)
    inner = r.content
    patched.push(...r.patched.map((k) => `audit.${k}`))
  }

  if (patches.tabs) {
    const tabsM = inner.match(/(    tabs: \{)([\s\S]*?)(    \},)/)
    if (tabsM) {
      const r = patchKeyValues(tabsM[2], patches.tabs, 6)
      inner = inner.replace(tabsM[0], `${tabsM[1]}${r.content}${tabsM[3]}`)
      patched.push(...r.patched.map((k) => `audit.tabs.${k}`))
    }
  }

  if (patches.config) {
    const cfgM = inner.match(/(    config: \{)([\s\S]*?)(    \},)/)
    if (cfgM) {
      const r = patchKeyValues(cfgM[2], patches.config, 6)
      inner = inner.replace(cfgM[0], `${cfgM[1]}${r.content}${cfgM[3]}`)
      patched.push(...r.patched.map((k) => `audit.config.${k}`))
    }
  }

  if (!patched.length) return { content, patched: [] }
  return { content: content.replace(m[0], `${m[1]}${inner}${m[3]}`), patched }
}

function patchRootConfigBlock(content, configPatches) {
  const re = /(\n  config: \{[\s\S]*?)(\n  \},\n  management: )/
  const m = content.match(re)
  if (!m) return { content, patched: [] }
  const inner = m[1].replace(/^\n  config: \{\n/, '')
  const { content: newInner, patched } = patchKeyValues(inner, configPatches, 4)
  if (!patched.length) return { content, patched: [] }
  return {
    content: content.replace(re, `\n  config: {\n${newInner}${m[2]}`),
    patched: patched.map((k) => `config.${k}`),
  }
}

function validateSyntax(filePath, text) {
  const open = (text.match(/\{/g) ?? []).length
  const close = (text.match(/\}/g) ?? []).length
  if (open !== close) throw new Error(`${filePath}: unbalanced braces (${open} vs ${close})`)
  if (!text.includes('export default')) throw new Error(`${filePath}: missing export default`)
}

function processLocale(locale) {
  const filePath = path.join(localesDir, locale, 'sessions.ts')
  const before = fs.readFileSync(filePath, 'utf8')
  let content = before
  const patched = []

  if (locale === 'en-US') {
    const fullBlock = formatObjBlock('promptInjectionFull', PROMPT_INJECTION_FULL_EN) + '\n'
    const r = replaceBlock(content, PROMPT_FULL_BLOCK_RE, fullBlock, 'promptInjectionFull')
    content = r.content
    patched.push(...r.patched)
  }

  const catBlock = formatObjBlock('promptInjectionCategories', PROMPT_INJECTION_CATEGORIES) + '\n'
  const rCat = replaceBlock(content, CATEGORIES_BLOCK_RE, catBlock, 'promptInjectionCategories')
  content = rCat.content
  patched.push(...rCat.patched)

  const { flat, configBlock } = buildFlatMapsFromNested(content)

  if (locale === 'en-US') {
    const enFlat = {}
    for (const k of STATS_NESTED_KEYS) {
      const v = readKey(extractSection(content, /\n  stats: \{([\s\S]*?\n  \},)/), k)
      if (v) enFlat[`stats_${k}`] = v
    }
    const enCfg = extractSection(content, /\n  config: \{([\s\S]*?\n  \},\npromptInjectionFull:)/)
    for (const k of CONFIG_NESTED_KEYS) {
      const v = readKey(enCfg, k)
      if (v) enFlat[`config_${k}`] = v
    }
    const rFlat = patchKeyValues(content, enFlat, 2)
    content = rFlat.content
    patched.push(...rFlat.patched)
  }

  if (locale === 'zh-TW') {
    const { rootCfg, auditFlat, auditNested } = buildZhTwMaps(content)
    const rAudit = patchKeyValues(content, auditFlat, 4)
    content = rAudit.content
    patched.push(...rAudit.patched)

    const rNested = patchWithinAuditBlock(content, auditNested)
    content = rNested.content
    patched.push(...rNested.patched)

    const rRoot = patchRootConfigBlock(content, rootCfg)
    content = rRoot.content
    patched.push(...rRoot.patched)
  } else if (FOREIGN_LOCALES.includes(locale)) {
    const auditFlat = auditFlatByLocale[locale] ?? {}
    const rAudit = patchKeyValues(content, auditFlat, 4)
    content = rAudit.content
    patched.push(...rAudit.patched)

    const rNested = patchWithinAuditBlock(content, auditNestedPatches(auditFlat))
    content = rNested.content
    patched.push(...rNested.patched)

    const rootCfg = rootConfigByLocale[locale] ?? {}
    const rRoot = patchRootConfigBlock(content, rootCfg)
    content = rRoot.content
    patched.push(...rRoot.patched)
  }

  if (content !== before) {
    validateSyntax(filePath, content)
    fs.writeFileSync(filePath, content, 'utf8')
  }

  return {
    locale,
    changed: content !== before,
    patched,
    chineseBefore: countChineseLines(before),
    chineseAfter: countChineseLines(content),
  }
}

function main() {
  console.log('sync-sessions-locale-leaks\n')
  const results = TARGET_LOCALES.map(processLocale)
  const changed = results.filter((r) => r.changed)

  for (const r of results) {
    const delta = r.chineseBefore - r.chineseAfter
    console.log(`${r.locale}: chinese ${r.chineseBefore} → ${r.chineseAfter} (${delta >= 0 ? '-' : '+'}${Math.abs(delta)})${r.changed ? ' [patched]' : ''}`)
    if (r.patched.length) console.log(`  ${r.patched.join(', ')}`)
  }

  console.log(`\nFiles changed: ${changed.length}`)
  changed.forEach((r) => console.log(`  - ${r.locale}/sessions.ts`))
}

main()
