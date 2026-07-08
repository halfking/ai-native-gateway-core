// modulesView.ts — 模块管理页(ModulesView)文案。
export default {
  pageTitle: '模块管理',
  pageSubtitle: '企业级功能模块统一管理，按需开启/关闭各项能力',
  modulesEnabled: '模块已启用',
  loading: '加载中…',

  category: {
    compression: '请求压缩',
    session: '会话管理',
    security: '安全防护',
    rate_limit: '流量控制',
    general: '通用能力',
    integration: '集成对接',
  },

  status: {
    enabled: '已启用',
    disabled: '已禁用',
    processing: '处理中…',
    enabledAction: '启用此模块',
    disabledAction: '禁用此模块',
  },

  dangerLevel: {
    safe: '安全',
    warn: '注意',
    danger: '危险',
    breaking: '高危',
    unknown: '未知',
  },

  tabs: {
    overview: '概览',
    config: '配置',
    integration: '集成',
  },

  overview: {
    sectionDescription: '模块描述',
    sectionCapabilities: '能力清单',
    sectionDependencies: '依赖模块',
    labelKey: '模块标识',
    labelDanger: '危险级别',
    labelConfigCount: '配置项数',
    labelStatus: '当前状态',
    viewAllSettings: '查看所有系统设置',
    dependencyDisabled: '此依赖模块未启用',
    notEnabled: '未启用',
  },

  config: {
    noSettings: '此模块没有可配置的设置项。',
    sourceDefault: '默认',
    switchOn: '启用',
    switchOff: '禁用',
    inputPlaceholder: '输入{description}',
  },

  integration: {
    docsLabel: '对接文档：',
    stepsTitle: '配置步骤',
    enabledStatus: '集成已启用',
    disabledHint: '集成未启用 — 请先开启此模块',
    prerequisitesTitle: '前置模块',
    prerequisitesHint: '请先启用以上前置模块，再开启此模块',
    feishuSteps: [
      '在飞书开放平台创建自定义机器人',
      '复制 Webhook URL 并粘贴到下方配置中',
      '（可选）配置签名验证令牌',
      '开启"飞书机器人集成"开关',
    ],
    feishuBotIntegration: '飞书机器人集成',
    wechatSteps: [
      '在企业微信管理后台创建自建应用',
      '（可选）在「接收消息」中配置回调 URL 和 Token',
      '复制企业 CorpID、AgentID、Secret 填入下方配置',
      '（可选）配置群机器人 Webhook URL 以使用群消息推送',
      '（可选）配置 EncodingAESKey 以启用回调加密',
      '开启"微信机器人集成"开关',
      '确保前置模块（压缩管理、注入检测、会话缓存、会话审计）均已启用',
    ],
    wechatBotIntegration: '微信机器人集成',
  },

  empty: {
    selectModule: '选择一个模块查看详情与配置',
  },

  error: {
    loadFailed: '加载模块列表失败',
    operationFailed: '操作失败',
    saveFailed: '保存配置失败',
  },
}