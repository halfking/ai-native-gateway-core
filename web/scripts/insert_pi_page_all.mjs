//给8 语种的 common.piAnalysis 下插入 page节（en-US用英文值，其余7语种用占位——后随子代理填充；zh-TW/ja-JP 也直接给值由我写）
import fs from 'fs';
const WEB = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/locales';
//en值
const EN = {
 title: 'PI Error Analysis',
 triggering: 'Analyzing...',
 triggerManual: 'Trigger Analysis',
 refresh: 'Refresh',
 statPending: 'Pending',
 statAnalyzed: 'Analyzed',
 statFalsePositiveRate: 'False Positive Rate',
 statAvgDuration: 'Avg Duration',
 tabQueue: 'Error Queue',
 tabResults: 'Analysis Results',
 filterAllStatus: 'All statuses',
 colErrorType: 'Error Type',
 colPriority: 'Priority',
 colTenant: 'Tenant',
 colErrorMessage: 'Error Message',
 colStatus: 'Status',
 colRetryCount: 'Retries',
 colCreatedAt: 'Created At',
 colActions: 'Actions',
 colAnalysisId: 'Analysis ID',
 colRootCause: 'Root Cause',
 colFalsePositive: 'False Positive',
 colConfidence: 'Confidence',
 colSeverity: 'Severity',
 colSuggestedFix: 'Suggested Fix',
 prevPage: 'Previous',
 nextPage: 'Next',
 detailButton: 'Details',
 yes: 'Yes',
 no: 'No',
 errorTypeLLMDetectionFailed: 'LLM detection failed',
 errorTypeRuleFalsePositive: 'Rule false positive',
 errorTypeCanaryDetectionFailed: 'Canary detection failed',
 errorTypeVectorMatchFailed: 'Vector match failed',
 errorTypeHeuristicMisfire: 'Heuristic misfire',
 errorTypePolicyLoadFailed: 'Policy load failed',
 errorTypeUnexpectedBehavior: 'Unexpected behavior',
 errorTypePerformanceIssue: 'Performance issue',
 statusPending: 'Pending',
 statusProcessing: 'Processing',
 statusAnalyzed: 'Analyzed',
 statusFailed: 'Failed',
 statusSkipped: 'Skipped',
 severityLow: 'Low',
 severityMedium: 'Medium',
 severityHigh: 'High',
 severityCritical: 'Critical',
 impactSession: 'Session',
 impactTenant: 'Tenant',
 impactGlobal: 'Global',
};
const ZH = {
 title: 'PI错误分析',
 triggering: '分析中...',
 triggerManual: '手动触发分析',
 refresh: '刷新',
 statPending: '待分析',
 statAnalyzed: '已分析',
 statFalsePositiveRate: '误报率',
 statAvgDuration: '平均耗时',
 tabQueue: '错误队列',
 tabResults: '分析结果',
 filterAllStatus: '全部状态',
 colErrorType: '错误类型',
 colPriority: '优先级',
 colTenant: '租户',
 colErrorMessage: '错误消息',
 colStatus: '状态',
 colRetryCount: '重试次数',
 colCreatedAt: '创建时间',
 colActions: '操作',
 colAnalysisId: '分析ID',
 colRootCause: '根本原因',
 colFalsePositive: '误报',
 colConfidence: '置信度',
 colSeverity: '严重程度',
 colSuggestedFix: '建议修复',
 prevPage: '上一页',
 nextPage: '下一页',
 detailButton: '详情',
 yes: '是',
 no: '否',
 errorTypeLLMDetectionFailed: 'LLM检测失败',
 errorTypeRuleFalsePositive: '规则误报',
 errorTypeCanaryDetectionFailed: 'Canary检测失败',
 errorTypeVectorMatchFailed: '向量匹配失败',
 errorTypeHeuristicMisfire: '启发式误触发',
 errorTypePolicyLoadFailed: '策略加载失败',
 errorTypeUnexpectedBehavior: '意外行为',
 errorTypePerformanceIssue: '性能问题',
 statusPending: '待处理',
 statusProcessing: '处理中',
 statusAnalyzed: '已分析',
 statusFailed: '失败',
 statusSkipped: '已跳过',
 severityLow: '低',
 severityMedium: '中',
 severityHigh: '高',
 severityCritical: '严重',
 impactSession: '会话',
 impactTenant: '租户',
 impactGlobal: '全局',
};
// TW/JA 由主会话直接给值（避免子代理往返）；其余4 语种占位待子代理
const TW = {
 title: 'PI錯誤分析',
 triggering: '分析中...',
 triggerManual: '手動觸發分析',
 refresh: '重新整理',
 statPending: '待分析',
 statAnalyzed: '已分析',
 statFalsePositiveRate: '誤報率',
 statAvgDuration: '平均耗時',
 tabQueue: '錯誤佇列',
 tabResults: '分析結果',
 filterAllStatus: '全部狀態',
 colErrorType: '錯誤類型',
 colPriority: '優先級',
 colTenant: '租戶',
 colErrorMessage: '錯誤訊息',
 colStatus: '狀態',
 colRetryCount: '重試次數',
 colCreatedAt: '建立時間',
 colActions: '動作',
 colAnalysisId: '分析ID',
 colRootCause: '根本原因',
 colFalsePositive: '誤報',
 colConfidence: '信心水準',
 colSeverity: '嚴重程度',
 colSuggestedFix: '建議修正',
 prevPage: '上一頁',
 nextPage: '下一頁',
 detailButton: '詳情',
 yes: '是',
 no: '否',
 errorTypeLLMDetectionFailed: 'LLM檢測失敗',
 errorTypeRuleFalsePositive: '規則誤報',
 errorTypeCanaryDetectionFailed: 'Canary檢測失敗',
 errorTypeVectorMatchFailed: '向量匹配失敗',
 errorTypeHeuristicMisfire: '啟發式誤觸發',
 errorTypePolicyLoadFailed: '策略載入失敗',
 errorTypeUnexpectedBehavior: '非預期行為',
 errorTypePerformanceIssue: '效能問題',
 statusPending: '待處理',
 statusProcessing: '處理中',
 statusAnalyzed: '已分析',
 statusFailed: '失敗',
 statusSkipped: '已跳過',
 severityLow: '低',
 severityMedium: '中',
 severityHigh: '高',
 severityCritical: '嚴重',
 impactSession: '工作階段',
 impactTenant: '租戶',
 impactGlobal: '全域',
};
const JA = {
 title: 'PIエラー分析',
 triggering: '分析中...',
 triggerManual: '手動で分析を実行',
 refresh: '更新',
 statPending: '未分析',
 statAnalyzed: '分析済み',
 statFalsePositiveRate: '誤検知率',
 statAvgDuration: '平均所要時間',
 tabQueue: 'エラーキュー',
 tabResults: '分析結果',
 filterAllStatus: 'すべてのステータス',
 colErrorType: 'エラー種別',
 colPriority: '優先度',
 colTenant: 'テナント',
 colErrorMessage: 'エラーメッセージ',
 colStatus: 'ステータス',
 colRetryCount: '再試行回数',
 colCreatedAt: '作成日時',
 colActions: 'アクション',
 colAnalysisId: '分析ID',
 colRootCause: '根本原因',
 colFalsePositive: '誤検知',
 colConfidence: '信頼度',
 colSeverity: '重大度',
 colSuggestedFix: '修正案',
 prevPage: '前へ',
 nextPage: '次へ',
 detailButton: '詳細',
 yes: 'はい',
 no: 'いいえ',
 errorTypeLLMDetectionFailed: 'LLM検出失敗',
 errorTypeRuleFalsePositive: 'ルール誤検知',
 errorTypeCanaryDetectionFailed: 'Canary検出失敗',
 errorTypeVectorMatchFailed: 'ベクトル照合失敗',
 errorTypeHeuristicMisfire: 'ヒューリスティック誤発火',
 errorTypePolicyLoadFailed: 'ポリシー読込失敗',
 errorTypeUnexpectedBehavior: '予期しない動作',
 errorTypePerformanceIssue: 'パフォーマンス問題',
 statusPending: '未処理',
 statusProcessing: '処理中',
 statusAnalyzed: '分析済み',
 statusFailed: '失敗',
 statusSkipped: 'スキップ済み',
 severityLow: '低',
 severityMedium: '中',
 severityHigh: '高',
 severityCritical: '重大',
 impactSession: 'セッション',
 impactTenant: 'テナント',
 impactGlobal: 'グローバル',
};
const VALUES = { 'en-US': EN, 'zh-CN': ZH, 'zh-TW': TW, 'ja-JP': JA };
const PENDING = ['ar-SA', 'de-DE', 'es-ES', 'fr-FR'];
const order = Object.keys(ZH);
function block(indent, vals) {
 const lines = [indent + 'page: {'];
 for (const k of order) {
 const v = String(vals[k]).replace(/\\/g, '\\\\').replace(/'/g, "\\'");
 lines.push(indent + ' ' + k + ": '" + v + "',");
 }
 lines.push(indent + '},');
 return lines;
}
let report = [];
for (const loc of ['en-US', 'zh-CN', 'zh-TW', 'ja-JP']) {
 const p = WEB + '/' + loc + '/common.ts';
 const lines = fs.readFileSync(p, 'utf8').split('\n');
 const idx = lines.findIndex(l => l.trim() === 'piAnalysis: {');
 if (idx <0) throw new Error(loc + ': piAnalysis not found');
 //已插入过则跳过
 if (lines.some(l => l.trim() === 'page: {' && lines.indexOf(l) > idx && lines.indexOf(l) < idx +5)) {
 report.push(loc + ': already has page'); continue;
 }
 const baseIndent = (lines[idx].match(/^(\s*)/) || ['', ''])[1];
 const lines2 = [...lines];
 lines2.splice(idx +1,0, ...block(baseIndent, VALUES[loc]));
 fs.writeFileSync(p, lines2.join('\n'));
 report.push(loc + ': inserted ' + order.length);
}
for (const loc of PENDING) {
 const p = WEB + '/' + loc + '/common.ts';
 const lines = fs.readFileSync(p, 'utf8').split('\n');
 const idx = lines.findIndex(l => l.trim() === 'piAnalysis: {');
 if (idx <0) throw new Error(loc + ': piAnalysis not found');
 if (lines.some((l, i) => l.trim() === 'page: {' && i > idx && i < idx +5)) {
 report.push(loc + ': already has page'); continue;
 }
 const baseIndent = (lines[idx].match(/^(\s*)/) || ['', ''])[1];
 //占位：暂用 zh值（子代理随后覆写为译文）——避免守卫因缺键而红
 const lines2 = [...lines];
 lines2.splice(idx +1,0, ...block(baseIndent, ZH));
 fs.writeFileSync(p, lines2.join('\n'));
 report.push(loc + ': placeholder(zh) ' + order.length);
}
console.log(report.join('\n'));
