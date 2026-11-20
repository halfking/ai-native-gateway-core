//PIAnalysisView i18n化：模板硬编码中文 → t('common.piAnalysis.page.*')
import fs from 'fs';
const p = '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/web/src/views/admin/PIAnalysisView.vue';
let s = fs.readFileSync(p, 'utf8');
const P = "common.piAnalysis.page.";
//精确一对一替换（含上下文锚，防误伤）
const subs = [
 //页头
 ["<h1>PI错误分析</h1>", `<h1>{{ t('${P}title') }}</h1>`],
 ["{{ isTriggering ? '分析中...' : '手动触发分析' }}", `{{ isTriggering ? t('${P}triggering') : t('${P}triggerManual') }}`],
 ["刷新\n </button>", ` {{ t('${P}refresh') }}\n </button>`],
 //统计卡
 ['<div class="stat-label">待分析</div>', `<div class="stat-label">{{ t('${P}statPending') }}</div>`],
 ['<div class="stat-label">已分析</div>', `<div class="stat-label">{{ t('${P}statAnalyzed') }}</div>`],
 ['<div class="stat-label">误报率</div>', `<div class="stat-label">{{ t('${P}statFalsePositiveRate') }}</div>`],
 ['<div class="stat-label">平均耗时</div>', `<div class="stat-label">{{ t('${P}statAvgDuration') }}</div>`],
 //Tab
 ["错误队列\n </button>", ` {{ t('${P}tabQueue') }}\n </button>`],
 [" 分析结果\n </button>", ` {{ t('${P}tabResults') }}\n </button>`],
 //筛选
 ['<option value="">全部状态</option>', `<option value="">{{ t('${P}filterAllStatus') }}</option>`],
 ['<option value="pending">待处理</option>', `<option value="pending">{{ t('${P}statusPending') }}</option>`],
 ['<option value="processing">处理中</option>', `<option value="processing">{{ t('${P}statusProcessing') }}</option>`],
 ['<option value="analyzed">已分析</option>', `<option value="analyzed">{{ t('${P}statusAnalyzed') }}</option>`],
 ['<option value="failed">失败</option>', `<option value="failed">{{ t('${P}statusFailed') }}</option>`],
 //队列表头
 [" <th>错误类型</th>", ` <th>{{ t('${P}colErrorType') }}</th>`],
 [" <th>优先级</th>", ` <th>{{ t('${P}colPriority') }}</th>`],
 [" <th>租户</th>", ` <th>{{ t('${P}colTenant') }}</th>`],
 [" <th>错误消息</th>", ` <th>{{ t('${P}colErrorMessage') }}</th>`],
 [" <th>状态</th>", ` <th>{{ t('${P}colStatus') }}</th>`],
 [" <th>重试次数</th>", ` <th>{{ t('${P}colRetryCount') }}</th>`],
 [" <th>创建时间</th>", ` <th>{{ t('${P}colCreatedAt') }}</th>`],
 [" <th>操作</th>", ` <th>{{ t('${P}colActions') }}</th>`],
 //分页
 [":disabled=\"queueOffset ===0\">上一页</button>", `:disabled="queueOffset ===0">{{ t('${P}prevPage') }}</button>`],
 [":disabled=\"queueOffset + queueLimit >= queueTotal\">下一页</button>", `:disabled="queueOffset + queueLimit >= queueTotal">{{ t('${P}nextPage') }}</button>`],
 [":disabled=\"resultsOffset ===0\">上一页</button>", `:disabled="resultsOffset ===0">{{ t('${P}prevPage') }}</button>`],
 [":disabled=\"resultsOffset + resultsLimit >= resultsTotal\">下一页</button>", `:disabled="resultsOffset + resultsLimit >= resultsTotal">{{ t('${P}nextPage') }}</button>`],
 //结果表头
 [" <th>分析ID</th>", ` <th>{{ t('${P}colAnalysisId') }}</th>`],
 [" <th>根本原因</th>", ` <th>{{ t('${P}colRootCause') }}</th>`],
 [" <th>误报</th>", ` <th>{{ t('${P}colFalsePositive') }}</th>`],
 [" <th>置信度</th>", ` <th>{{ t('${P}colConfidence') }}</th>`],
 [" <th>严重程度</th>", ` <th>{{ t('${P}colSeverity') }}</th>`],
 [" <th>建议修复</th>", ` <th>{{ t('${P}colSuggestedFix') }}</th>`],
 //是/否 与 详情
 ["{{ result.is_false_positive ? '是' : '否' }}", `{{ result.is_false_positive ? t('${P}yes') : t('${P}no') }}`],
 [" 详情\n </button>", ` {{ t('${P}detailButton') }}\n </button>`],
 //script 字典 → t()
 ["'llm_detection_failed': 'LLM检测失败',", `'llm_detection_failed': t('${P}errorTypeLLMDetectionFailed'),`],
 ["'rule_false_positive': '规则误报',", `'rule_false_positive': t('${P}errorTypeRuleFalsePositive'),`],
 ["'canary_detection_failed': 'Canary检测失败',", `'canary_detection_failed': t('${P}errorTypeCanaryDetectionFailed'),`],
 ["'vector_match_failed': '向量匹配失败',", `'vector_match_failed': t('${P}errorTypeVectorMatchFailed'),`],
 ["'heuristic_misfire': '启发式误触发',", `'heuristic_misfire': t('${P}errorTypeHeuristicMisfire'),`],
 ["'policy_load_failed': '策略加载失败',", `'policy_load_failed': t('${P}errorTypePolicyLoadFailed'),`],
 ["'unexpected_behavior': '意外行为',", `'unexpected_behavior': t('${P}errorTypeUnexpectedBehavior'),`],
 ["'performance_issue': '性能问题'", `'performance_issue': t('${P}errorTypePerformanceIssue')`],
 ["'pending': '待处理',", `'pending': t('${P}statusPending'),`],
 ["'processing': '处理中',", `'processing': t('${P}statusProcessing'),`],
 ["'analyzed': '已分析',", `'analyzed': t('${P}statusAnalyzed'),`],
 ["'failed': '失败',", `'failed': t('${P}statusFailed'),`],
 ["'skipped': '已跳过'", `'skipped': t('${P}statusSkipped')`],
 ["'low': '低',", `'low': t('${P}severityLow'),`],
 ["'medium': '中',", `'medium': t('${P}severityMedium'),`],
 ["'high': '高',", `'high': t('${P}severityHigh'),`],
 ["'critical': '严重'", `'critical': t('${P}severityCritical')`],
 ["'session': '会话',", `'session': t('${P}impactSession'),`],
 ["'tenant': '租户',", `'tenant': t('${P}impactTenant'),`],
 ["'global': '全局'", `'global': t('${P}impactGlobal')`],
];
let missing = [];
for (const [old, nw] of subs) {
 if (!s.includes(old)) { missing.push(old.slice(0,50)); continue; }
 s = s.replace(old, nw);
}
fs.writeFileSync(p, s);
console.log('replaced:', subs.length - missing.length, 'missing:', missing.length);
if (missing.length) console.log(missing.join('\n'));
