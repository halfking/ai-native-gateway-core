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
    enabledAction: '禁用此模块',
    disabledAction: '启用此模块',
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
    status: '运行状态',
  },

  overview: {
    sectionDescription: '模块描述',
    sectionCapabilities: '能力清单',
    sectionRequirements: '依赖模块',
    labelKey: '模块标识',
    labelDanger: '危险级别',
    labelConfigCount: '配置项数',
    labelStatus: '当前状态',
    viewAllSettings: '查看所有系统设置',
    requirementsMet: '所有依赖模块已启用',
    requirementsMissing: '以下依赖模块未启用，相关功能可能受限：',
    jumpToModule: '前往配置',
    testConnection: '测试连通性',
    testSuccess: '连通性测试成功',
    testFailed: '连通性测试失败',
    testInProgress: '正在发送测试消息…',
  },

  config: {
    noSettings: '此模块没有可配置的设置项。',
    sourceDefault: '默认',
    sourceEnv: '环境变量',
    sourceDb: '数据库',
    switchOn: '启用',
    switchOff: '禁用',
    inputPlaceholder: '输入{description}',
    sections: {
      connection: '连接',
      alerts: '告警转发',
      approvals: '审批通知',
      commands: '命令面板',
      security: '安全',
      general: '通用',
    },
  },

  feishu: {
    connectionHint: '在飞书开放平台创建自定义机器人后，将 Webhook 地址粘贴到下方。详细步骤参考飞书官方文档。',
    callbackUrlLabel: '回调地址（需在飞书机器人后台配置）',
    callbackUrlHelp: '将此 URL 填入飞书自定义机器人 → 回调配置，用于接收飞书端的审批操作。',
    whitelistHelp: '允许通过飞书执行命令的用户 OpenID，多个用英文逗号分隔。留空时按 commands.admin_only 决定是否全部允许。',
    quietHoursHelp: '免打扰时段内仅 critical 级别告警会推送，避免夜间打扰。跨夜时段支持（22:00 → 08:00）。',
    commandsHelp: '开启后管理员可在飞书对话中通过命令与系统交互（/status /help /stats /audit /test）。',
    signatureHelp: '生产环境务必开启。开启后飞书回调必须携带有效签名（HMAC-SHA256）且时间戳在窗口内。',
  },

  integration: {
    docsLabel: '对接文档：',
    stepsTitle: '配置步骤',
    enabledStatus: '集成已启用',
    disabledHint: '集成未启用 — 请先开启此模块',
    feishuSteps: [
      '在飞书开放平台创建自定义机器人',
      '复制 Webhook URL 并粘贴到下方配置中',
      '（可选）配置签名验证令牌和加密密钥',
      '配置完成后点击「测试连通性」验证',
      '开启「飞书机器人集成」开关',
    ],
    feishuBotIntegration: '飞书机器人集成',
  },

  empty: {
    selectModule: '选择一个模块查看详情与配置',
  },

  error: {
    loadFailed: '加载模块列表失败',
    operationFailed: '操作失败',
    saveFailed: '保存配置失败',
    testFailed: '测试失败',
  },
}