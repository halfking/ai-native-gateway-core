#!/usr/bin/env node
import { writeFileSync, mkdirSync } from 'fs'
import { dirname, join } from 'path'
import { fileURLToPath } from 'url'

const OUT = dirname(fileURLToPath(import.meta.url))

function doc(p) {
  const pri = p.issues.some(i => i.startsWith('P0')) ? 'P0' : p.issues.some(i => i.startsWith('P1')) ? 'P1' : 'P2'
  return `# ${p.name} — UI 审计

- **路由**: \`${p.path}\`
- **组件**: \`${p.component}\`
- **审计时间**: 2026-07-14
- **视口**: 1920×1080（browser-use 实测 llmgo.kxpms.cn）

## 页面结构

\`\`\`mermaid
${p.mermaid}
\`\`\`

## 现状评估

| 维度 | 结论 | 优先级 |
|------|------|--------|
| 视觉/display | ${p.display} | ${pri} |
| 功能组织 | ${p.org} | P2 |
| 数据布局 | ${p.layout} | P2 |
| 多语言 | ${p.i18n} | ${p.i18nPri || 'OK'} |

## 发现的问题

${p.issues.map((x, i) => `${i + 1}. ${x}`).join('\n')}

## 优化建议

${p.suggestions.map((x, i) => `${i + 1}. ${x}`).join('\n')}

---
*浏览器实测 + 代码对照；详见 [99-i18n-汇总.md](../99-i18n-汇总.md)*
`
}

const PAGES = [
  { path:'/', file:'01-总览.md', name:'总览（仪表盘）', component:'web/src/views/DashboardViewV2.vue',
    mermaid:`flowchart TB\n  H[标题栏+日期筛选] --> T[Tab: 实时请求流/会话与统计/系统监测]\n  T --> S[10项KPI统计卡片]\n  S --> M[实时流表格或图表]`,
    display:'深色主题一致，统计卡片横向紧凑，无反色块。实时请求流空态占位偏大。',
    org:'KPI → Tab → 详情三层结构清晰。',
    layout:'自上而下：标题→Tab→统计→主内容，左到右统计带信息完整。',
    i18n:'zh-CN完整；切换en后部分Tab/标题仍中文；ar RTL已启用。',
    i18nPri:'P1',
    issues:['P1：延迟显示「4.8Kms」单位易误解','P2：版本号「vv2.4.3」重复v','P2：实时流空态区域过高','P1：非中文locale下仪表盘标题未切换'],
    suggestions:['统一延迟单位格式化','修复App.vue版本号拼接','空态max-height+skeleton','补全dashboard.ts各语言'] },

  { path:'/models', file:'02-模型与路由/01-模型与目录.md', name:'模型与目录', component:'web/src/views/ModelsView.vue',
    mermaid:`flowchart LR\n  H[标题+操作] --> TB[Tab: 规范模型/目录/名称映射]\n  TB --> F[左侧筛选] --> L[模型清单表格]`,
    display:'左筛右表合理；Tab带计数；无横向滚动。', org:'筛选与清单分离，操作按钮在标题行。', layout:'2109模型数据加载正常，筛选→表格。',
    i18n:'中文完整；状态枚举active/disabled为英文可接受。', i18nPri:'OK',
    issues:['P2：名称映射Tab零数据无引导','P2：厂家下拉项多缺搜索'],
    suggestions:['零数据Tab加Empty引导','combobox加filterable'] },

  { path:'/routing-v2', file:'02-模型与路由/02-路由全景.md', name:'路由全景', component:'web/src/views/RoutingDashboardView.vue',
    mermaid:`flowchart TB\n  NAV[5+子Tab导航] --> FLOW[L1→分类→评分 流向图]\n  FLOW --> TBL[路由策略表]`,
    display:'流向图层次清晰，信息密度高但可读。', org:'导航+流向+策略表分区合理。', layout:'全景→策略明细，流向左到右。',
    i18n:'中文完整。', i18nPri:'OK',
    issues:['P1：多Tab横向排列1366px可能折行','P2：刷新仅图标缺tooltip'],
    suggestions:['Tab scrollable或下拉聚合','图标按钮补aria-label'] },

  { path:'/routing-v2/credentials', file:'02-模型与路由/03-凭据监控.md', name:'凭据监控', component:'web/src/views/CredentialMonitorView.vue',
    mermaid:`flowchart TB\n  H[凭据监控标题] --> C[监控指标卡片] --> D[凭据详情/图表]`,
    display:'标题清晰，卡片紧凑。', org:'从路由全景独立，侧边栏exact高亮正确。', layout:'卡片→详情区。',
    i18n:'中文完整。', i18nPri:'OK', issues:['P2：与路由全景功能边界略模糊'], suggestions:['页头加说明文案'] },

  { path:'/probe-health', file:'02-模型与路由/04-探测健康度.md', name:'探测健康度', component:'web/src/views/ProbeHealthView.vue',
    mermaid:`flowchart TB\n  H[探测健康度] --> F[筛选] --> T[探测结果表格]`,
    display:'表格+状态色块克制。', org:'单表格为主简洁。', layout:'筛选→表格。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：暗色主题状态色对比度偏低'], suggestions:['badge用语义色并测contrast'] },

  { path:'/providers', file:'02-模型与路由/05-供应商.md', name:'供应商', component:'web/src/views/ProvidersView.vue',
    mermaid:`flowchart TB\n  H[供应商管理+全局操作] --> FIL[健康/可路由性筛选]\n  FIL --> T[供应商表格]`,
    display:'工具栏+筛选+表格，按钮尺寸统一。', org:'全局操作与行内操作分离。', layout:'筛选横排，表格完整。',
    i18n:'6外语标题已翻译；域名等技术字段保持原文合理。', i18nPri:'OK',
    issues:['P1：部分行含error状态文案','P2：筛选多时换行'],
    suggestions:['错误态统一ElAlert','筛选区collapsible'] },

  { path:'/pricing', file:'02-模型与路由/06-成本价格.md', name:'成本价格', component:'web/src/views/PricingManagementView.vue',
    mermaid:`flowchart TB\n  H[成本价格] --> CH[凭据覆盖率图表] --> D[明细]`,
    display:'图表无错位。', org:'覆盖率+明细主次分明。', layout:'图表上、明细下。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：大表可考虑虚拟滚动'], suggestions:['el-table-v2或分页优化'] },

  { path:'/model-pricing', file:'02-模型与路由/07-定价管理.md', name:'定价管理', component:'web/src/views/StandardModelPricingView.vue',
    mermaid:`flowchart TB\n  H[定价管理] --> B[全局基准积分] --> T[模型定价表]`,
    display:'基准+表格，列宽适中。', org:'基准与列表分区清楚。', layout:'基准卡片→表格。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：列多时应固定首列'], suggestions:['fixed第一列'] },

  { path:'/free-pool', file:'02-模型与路由/08-免费资源.md', name:'免费资源', component:'web/src/views/FreePoolView.vue',
    mermaid:`flowchart TB\n  S1[快速录入] --> S2[临时邮箱] --> S3[平台导航] --> S4[凭据区]`,
    display:'四区块纵向，间距均匀。', org:'录入/邮箱/导航/凭据分块清晰。', layout:'四段式从上到下。',
    i18n:'完整；外链域名保持原文。', i18nPri:'OK', issues:['P2：首屏需滚动看全'], suggestions:['Tab或锚点导航'] },

  { path:'/tenants', file:'03-租户用户/01-租户管理.md', name:'租户管理', component:'web/src/views/TenantsView.vue',
    mermaid:`flowchart TB\n  H[租户管理] --> A[+新建] --> F[状态筛选] --> T[租户表格]`,
    display:'标题🏬emoji与侧栏重复。', org:'标准CRUD布局。', layout:'简洁完整。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：标题emoji冗余'], suggestions:['统一PageHeader去emoji'] },

  { path:'/users', file:'03-租户用户/02-用户管理.md', name:'用户管理', component:'web/src/views/UsersView.vue',
    mermaid:`flowchart TB\n  H[用户管理] --> F[租户过滤] --> T[用户表格]`,
    display:'与租户页风格一致。', org:'租户过滤+表格合理。', layout:'筛选→表格。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：标题emoji冗余'], suggestions:['统一PageHeader'] },

  { path:'/keys', file:'03-租户用户/03-API密钥.md', name:'API 密钥', component:'web/src/views/KeysView.vue',
    mermaid:`flowchart TB\n  H[API密钥管理] --> O[默认限制+签发] --> T[密钥表格]`,
    display:'masked显示正常。', org:'限制→签发→列表流程清晰。', layout:'操作→筛选→表格。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：作废密钥切换不够明确'], suggestions:['el-tabs区分活跃/作废'] },

  { path:'/key-applications', file:'03-租户用户/04-密钥申请.md', name:'密钥申请', component:'web/src/views/KeyApplicationsView.vue',
    mermaid:`flowchart TB\n  H[密钥申请+待审数] --> TAB[待审/通过/拒绝] --> T[申请表]`,
    display:'动态待审计数直观。', org:'状态Tab清晰。', layout:'Tab→表格。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：计数在标题与Tab重复'], suggestions:['仅Tab badge显示'] },

  { path:'/audit-logs', file:'03-租户用户/05-审计日志.md', name:'审计日志', component:'web/src/views/AuditLogView.vue',
    mermaid:`flowchart TB\n  H[审计日志4413条] --> F[筛选] --> T[操作记录表]`,
    display:'表格正常，说明文案清晰。', org:'筛选+表格符合审计惯例。', layout:'说明→筛选→表格。',
    i18n:'操作类型显示raw key如authentication.login。', i18nPri:'P1',
    issues:['P1：操作类型列显示i18n key非翻译','P1：加载失败文案可能暴露英文error'],
    suggestions:['action字段t()映射','统一错误态组件'] },

  { path:'/request-logs', file:'04-请求与会话/01-请求日志.md', name:'请求日志', component:'web/src/views/RequestLogsView.vue',
    mermaid:`flowchart TB\n  H[请求日志] --> F[多维筛选] --> T[日志表格+详情抽屉]`,
    display:'复杂筛选+宽表格，1366下需横向滚动（预期）。', org:'筛选区功能多但分组可优化。', layout:'筛选→表格→详情。',
    i18n:'主体已i18n；部分列头英文。', i18nPri:'P2',
    issues:['P1：页面含error/Failed检测','P2：筛选条件过多占垂直空间'],
    suggestions:['筛选折叠面板','固定常用筛选项'] },

  { path:'/sessions', file:'04-请求与会话/02-会话列表.md', name:'会话列表', component:'web/src/views/SessionListView.vue',
    mermaid:`flowchart TB\n  H[会话列表+时间窗] --> W[⚠️ session manager not wired]\n  H --> T[会话表格/空态]`,
    display:'警告条醒目但破坏整体美观。', org:'时间窗+刷新合理。', layout:'说明→警告→内容。',
    i18n:'硬编码中文较多（见ui-audit P0）。', i18nPri:'P0',
    issues:['P0：显示「session manager not wired」','P0：大量硬编码中文未i18n','P1：警告条样式过于刺眼'],
    suggestions:['修复session manager wiring','迁移SessionListView至t()','警告改用ElAlert type=warning'] },

  { path:'/admin/sessions', file:'04-请求与会话/03-会话管理.md', name:'会话管理', component:'web/src/views/SessionManagementView.vue',
    mermaid:`flowchart TB\n  H[会话管理+刷新] --> E[HTTP 503错误]\n  H --> T[11列表格+操作列] --> M[详情模态框]`,
    display:'503错误条破坏页面；表头全硬编码中文。', org:'表格+模态详情结构合理。', layout:'表格列多，1366需滚动。',
    i18n:'仅en标题部分翻译，表头/按钮/模态全中文；ar仍显示中文。', i18nPri:'P0',
    issues:['P0：HTTP 503服务不可用','P0：全页硬编码中文（572行）','P0：confirm/alert原生对话框','P1：11列表格过宽'],
    suggestions:['修复session API 503','按ui-audit迁移i18n','替换ElMessageBox','隐藏次要列+列设置'] },

  { path:'/session-compare', file:'04-请求与会话/04-会话对比.md', name:'会话对比', component:'web/src/views/SessionCompareView.vue',
    mermaid:`flowchart TB\n  H[会话对比] --> P[双会话选择器] --> D[diff对比视图]`,
    display:'布局平衡，无反色块。', org:'选择→对比两步清晰。', layout:'左右或上下对比合理。',
    i18n:'基本完整。', i18nPri:'OK', issues:['P2：无会话时引导可加强'], suggestions:['Empty态加示例说明'] },

  { path:'/session-context', file:'04-请求与会话/05-会话上下文.md', name:'会话上下文', component:'web/src/views/session-context/SessionContextListView.vue',
    mermaid:`flowchart TB\n  H[会话上下文] --> L[任务列表] --> D[详情页/session-context/:id]`,
    display:'列表页简洁。', org:'列表→详情钻取合理。', layout:'卡片/列表从上到下。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：详情页需抽样审计'], suggestions:['见09-二级页面抽样.md'] },

  { path:'/admin/session-analytics', file:'04-请求与会话/06-会话分析中心.md', name:'会话分析中心', component:'web/src/views/SessionAnalyticsDashboardView.vue',
    mermaid:`flowchart TB\n  H[会话分析中心] --> K[分析KPI] --> CH[图表] --> L[钻取链接]`,
    display:'分析仪表盘风格统一。', org:'KPI→图表→钻取层次好。', layout:'紧凑直观。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：钻取页需单独审计'], suggestions:['见09-二级页面抽样'] },

  { path:'/admin/session-clusters', file:'04-请求与会话/07-会话聚类.md', name:'会话聚类', component:'web/src/views/SessionClustersView.vue',
    mermaid:`flowchart TB\n  H[会话分组聚类] --> C[聚类卡片/列表]`,
    display:'页面正常加载。', org:'聚类结果展示清晰。', layout:'卡片网格。',
    i18n:'完整。', i18nPri:'OK', issues:['P1：页面含error检测'], suggestions:['检查API错误提示'] },

  { path:'/admin/session-audit', file:'04-请求与会话/08-会话审计.md', name:'会话审计', component:'web/src/views/SessionAuditView.vue',
    mermaid:`flowchart TB\n  H[会话审计] --> F[筛选] --> T[审计表格/空态]`,
    display:'空态友好。', org:'筛选+表格标准。', layout:'简洁。',
    i18n:'泄漏app.current_tenant key。', i18nPri:'P0',
    issues:['P0：显示app.current_tenant未翻译key','P2：空态图标可统一'],
    suggestions:['补app.current_tenant翻译','统一Empty组件'] },

  { path:'/admin/settings', file:'05-数据运维/01-系统设置.md', name:'系统设置', component:'web/src/views/SettingsView.vue',
    mermaid:`flowchart TB\n  H[系统设置] --> G[配置分组] --> T[键值表格 security.* rate.*等]`,
    display:'配置键直接展示，偏技术向。', org:'按模块分组合理。', layout:'分组→键值表。',
    i18n:'配置key为英文dot notation（预期）；标签需i18n。', i18nPri:'P1',
    issues:['P1：配置项显示raw key如security.llm.intent_model','P1：hasError/空分组'],
    suggestions:['配置项label映射表','分组折叠+搜索'] },

  { path:'/admin/session-config', file:'05-数据运维/02-会话配置.md', name:'会话配置', component:'web/src/views/SessionConfigView.vue',
    mermaid:`flowchart TB\n  H[会话配置说明] --> TAB[审批规则/压缩策略/提示词注入]\n  TAB --> F[审批人/通知/规则表单]`,
    display:'Tab切换清晰。', org:'三Tab对应三类配置合理。', layout:'说明→Tab→表单。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：说明文字较长可折叠'], suggestions:['description collapsible'] },

  { path:'/admin/data-lifecycle', file:'05-数据运维/03-数据生命周期.md', name:'数据生命周期', component:'web/src/views/DataLifecycleView.vue',
    mermaid:`flowchart TB\n  H[数据生命周期] --> S[存储概览: PG/磁盘/列存]\n  S --> T1[表Top20] --> T2[Hot迁移] --> T3[分区管理]\n  S --> T4[降级恢复/日志/Blob/附件/文件系统]`,
    display:'6表+多卡片，信息密集但无反色块。', org:'8个内部Tab功能多，建议侧栏子导航。', layout:'概览→明细，运维向紧凑。',
    i18n:'表名public.*为数据库对象（预期）。', i18nPri:'OK',
    issues:['P1：信息密度过高新手难上手','P2：6表格同时出现滚动多'],
    suggestions:['Tab拆分降低单页密度','关键指标置顶卡片化'] },

  { path:'/format-anomalies', file:'05-数据运维/04-格式异常监控.md', name:'格式异常监控', component:'web/src/views/FormatAnomaliesView.vue',
    mermaid:`flowchart TB\n  H[格式异常监控] --> F[筛选] --> T[异常记录表]`,
    display:'标准监控页。', org:'筛选+表格。', layout:'清晰。',
    i18n:'已迁移i18n（见I18N_PAGES_IMPLEMENTATION）。', i18nPri:'OK', issues:['P2：异常类型色标可加强'], suggestions:['severity color coding'] },

  { path:'/admin/modules', file:'05-数据运维/05-模块管理.md', name:'模块管理', component:'web/src/views/ModulesView.vue',
    mermaid:`flowchart TB\n  H[模块管理] --> G[模块卡片网格 13/17 enabled]`,
    display:'卡片网格美观，状态badge清晰。', org:'模块卡片+详情抽屉合理。', layout:'网格从左到右从上到下。',
    i18n:'modulesView.*部分key缺失（i18n:check）。', i18nPri:'P1',
    issues:['P1：dependencyDisabled等310 missing keys之一','P1：hasError检测'],
    suggestions:['补modulesView locale','错误态优化'] },

  { path:'/admin/prompt-injection', file:'05-数据运维/06-提示词注入检测.md', name:'提示词注入检测', component:'web/src/views/PromptInjectionSettingsView.vue',
    mermaid:`flowchart TB\n  E[错误: promptInjectionFull.load*Failed]\n  E --> TAB[策略/引擎/严重度/规则/金丝雀/审批]`,
    display:'页面几乎空白，仅显示i18n key错误信息。', org:'设计为多Tab复杂页，当前不可用。', layout:'无法评估。',
    i18n:'P0：promptInjectionFull.* 全模块310 missing keys。', i18nPri:'P0',
    issues:['P0：loadStatsFailed/loadDetectionsFailed/loadRulesFailed key泄漏','P0：promptInjectionFull命名空间locale完全缺失','P0：8语言均显示key'],
    suggestions:['新增locales/*/promptInjectionFull.ts','API失败时显示友好错误非key','优先P0修复'] },

  { path:'/admin/compression', file:'05-数据运维/07-压缩管理.md', name:'压缩管理', component:'web/src/views/CompressionView.vue',
    mermaid:`flowchart TB\n  H[压缩概览] --> C1[策略分布] --> C2[压缩率趋势] --> T[会话压缩详情]`,
    display:'图表+表格，视觉平衡。', org:'概览→趋势→明细合理。', layout:'从上到下。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：图表legend中文'], suggestions:['图表i18n legend'] },

  { path:'/admin/modules?module=wechat_bot', file:'05-数据运维/08-微信机器人.md', name:'微信机器人', component:'web/src/views/ModulesView.vue',
    mermaid:`flowchart TB\n  H[模块管理-微信机器人预选] --> D[模块详情: 描述/能力/配置/依赖]`,
    display:'query预选模块，详情页清晰。', org:'依赖模块列表（压缩/注入/缓存）合理。', layout:'详情纵向排列。',
    i18n:'完整。', i18nPri:'OK', issues:['P1：hasError','P2：与模块管理页重复导航'], suggestions:['深度链接优化breadcrumb'] },

  { path:'/admin/agents', file:'05-数据运维/09-Agent-Registry.md', name:'Agent Registry', component:'web/src/views/AgentRegistryView.vue',
    mermaid:`flowchart TB\n  H[Agent Registry] --> F[筛选] --> T[Agent表格] --> TOP[拓扑视图]`,
    display:'表格+拓扑，专业感强。', org:'注册表+关系图分区好。', layout:'列表→详情/拓扑。',
    i18n:'agentRegistryView keys部分unused但页面正常。', i18nPri:'OK', issues:['P2：英文标题与中文侧栏混用'], suggestions:['标题i18n或统一英文品牌名'] },

  { path:'/ops/overview', file:'06-运维平台/01-运维概览.md', name:'运维概览', component:'web/src/views/ops/OpsOverviewView.vue',
    mermaid:`flowchart TB\n  H[运维概览] --> C[在线实例等KPI卡片]`,
    display:'卡片简洁。', org:'概览页定位清晰。', layout:'KPI卡片横排。',
    i18n:'非en语言标题仍显示Operations Overview英文。', i18nPri:'P1',
    issues:['P1：zh-TW/ja/de/fr/es/ar标题未本地化','P1：common.status等ops页missing keys'],
    suggestions:['补ops/overview locale','ops各页补common.*引用'] },

  { path:'/ops/licenses', file:'06-运维平台/02-License管理.md', name:'License 管理', component:'web/src/views/ops/LicenseManagementView.vue',
    mermaid:`flowchart TB\n  H[License管理] --> A[+创建] --> T[License列表]`,
    display:'🔑emoji标题，按钮合适。', org:'创建+列表标准。', layout:'简洁。',
    i18n:'common.updateSuccess等missing。', i18nPri:'P1', issues:['P1：toast可能显示key','P2：emoji标题'], suggestions:['补ops locale','去emoji'] },

  { path:'/ops/faults', file:'06-运维平台/03-故障管理.md', name:'故障管理', component:'web/src/views/ops/FaultManagementView.vue',
    mermaid:`flowchart TB\n  H[故障管理] --> T[故障列表/时间线]`,
    display:'页面加载正常，无h标题检测到。', org:'内容区可能缺PageHeader。', layout:'待确认标题组件。',
    i18n:'common.status/view missing。', i18nPri:'P1', issues:['P1：缺少明显页面标题','P1：i18n keys缺失'], suggestions:['加PageHeader','补fault locale'] },

  { path:'/ops/autoupdate', file:'06-运维平台/04-自动更新.md', name:'自动更新', component:'web/src/views/ops/AutoUpdateView.vue',
    mermaid:`flowchart TB\n  H[自动更新] --> L[发布列表 ops.autoupdate.channel.undefined]`,
    display:'列表正常但status显示undefined key。', org:'创建发布+列表合理。', layout:'标准。',
    i18n:'ops.autoupdate.channel.undefined泄漏。', i18nPri:'P0',
    issues:['P0：channel.undefined/logStatus.undefined key显示','P1：common.status missing'],
    suggestions:['AutoUpdateView fallback文案','补undefined枚举翻译'] },

  { path:'/ops/center', file:'06-运维平台/05-中心运维.md', name:'中心运维', component:'web/src/views/ops/CenterOpsView.vue',
    mermaid:`flowchart TB\n  H[中心运维] --> P[实例/节点面板]`,
    display:'加载正常。', org:'可能缺显式标题。', layout:'面板式。',
    i18n:'common.status missing。', i18nPri:'P1', issues:['P1：无检测到h标题','P1：i18n缺口'], suggestions:['PageHeader','补locale'] },

  { path:'/ops/vibecoding', file:'06-运维平台/06-VibeCoding.md', name:'VibeCoding', component:'web/src/views/ops/VibeCodingView.vue',
    mermaid:`flowchart TB\n  H[VibeCoding] --> P[项目列表 status.undefinedInvalid]`,
    display:'项目列表正常。', org:'创建+列表。', layout:'标准。',
    i18n:'ops.vibecoding.status.undefinedInvalid泄漏。', i18nPri:'P0',
    issues:['P0：无效status显示raw i18n key','P2：品牌名VibeCoding可保留英文'],
    suggestions:['status枚举fallback','补vibecoding locale'] },

  { path:'/examples', file:'07-接入示例.md', name:'接入示例', component:'web/src/views/ExamplesView.vue',
    mermaid:`flowchart TB\n  H[接入指南] --> C[客户端配置卡片] --> Code[API请求示例代码]`,
    display:'代码块+卡片，阅读体验好。', org:'客户端分类+示例代码分区清晰。', layout:'上到下：说明→配置→代码。',
    i18n:'完整；代码示例英文合理。', i18nPri:'OK', issues:['P2：代码块横向滚动条样式'], suggestions:['代码区max-width'] },

  { path:'/chat', file:'08-对话.md', name:'对话', component:'web/src/views/ChatView.vue',
    mermaid:`flowchart TB\n  H[对话] --> K[API密钥选择] --> M[消息区] --> I[输入框]`,
    display:'聊天布局标准，暗色协调。', org:'密钥→对话→输入流程合理。', layout:'经典IM三段式。',
    i18n:'完整。', i18nPri:'OK', issues:['P2：密钥下拉显示部分明文前缀','P2：侧栏「对话」与「微信机器人」图标重复💬'],
    suggestions:['密钥仅显示别名','差异化图标'] },
]

for (const p of PAGES) {
  const fp = join(OUT, p.file)
  mkdirSync(dirname(fp), { recursive: true })
  writeFileSync(fp, doc(p), 'utf8')
  console.log('OK', p.file)
}
console.log('Total:', PAGES.length)
