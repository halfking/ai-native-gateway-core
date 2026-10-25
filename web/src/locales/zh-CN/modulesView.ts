// modulesView.ts — 模块管理页(ModulesView)文案。
export default {
  pageTitle: '模块管理',
  pageSubtitle: '企业级功能模块统一管理，按需开启/关闭各项能力',
  // 31 §2.3 / §6：模块管理三段硬约束提示
  modeBannerTitle: '本页仅做三件事：',
  modeBannerBody: '已装模块开关、已授权未开通模块下载；本页不提供价格、订单或套餐入口（采购通道另行公告）。',
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
    sectionDependencies: '依赖模块',
    dependenciesTitle: '依赖模块',
    labelKey: '模块标识',
    labelDanger: '危险级别',
    labelConfigCount: '配置项数',
    labelStatus: '当前状态',
    dependencyDisabled: '依赖模块未启用',
    notEnabled: '未启用',
    viewAllSettings: '查看所有系统设置',
    required: '必需',
    optional: '可选',
    enableAllDeps: '一键启用 {count} 个必需依赖',
    enableAllDepsHint: '后端会按稳定顺序开启所有未启用的必需依赖后再启用本模块',
    cascading: '正在开启依赖…',
    cascadeSuccess: '已自动开启 {count} 个依赖：{names}',
    cascadeEmpty: '所有依赖已启用，模块已正常开启',
    cascadeAlreadyEnabled: '模块已启用，无需级联',
    cascadeLogTitle: '本次级联开启的依赖：',
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
  },

  handoff: {
    groupMaster: '主开关',
    groupMasterHint: '总开关与触发模式：决定会话交接的启用方式与触发语义',
    groupTrigger: '触发阈值',
    groupTriggerHint: '4 种触发条件并行生效，达到任一即触发交接；多阈值同时触发时取最严格的',
    groupSummary: '摘要生成',
    groupSummaryHint: '摘要引擎与提示词模板：决定如何把旧会话压缩成新会话可读上下文',
    groupSafety: '安全限制与通知',
    groupSafetyHint: '冷却、单会话上限、失败重试与日志/Webhook 通知',
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
    wechatBotIntegration: '企业微信机器人集成',
    dingtalkBotIntegration: '钉钉机器人集成',
    wechatSteps: [
      '在企业微信管理后台创建自建应用',
      '（可选）在「接收消息」中配置回调 URL 和 Token',
      '复制企业 CorpID、AgentID、Secret 填入下方设置',
      '（可选）配置群机器人 Webhook URL 以使用群组消息推送',
      '（可选）配置 EncodingAESKey 以启用回调加密',
      '开启「企业微信机器人集成」开关',
      '确保前置模块（压缩管理、注入检测、会话缓存、会话审计）均已启用',
    ],
    dingtalkSteps: [
      '在钉钉群设置中添加「自定义机器人」，安全设置选「加签」并复制 Webhook',
      '将 Webhook URL 贴到下方设置，并填入加签 Secret',
      '（选用）在开发者后台创建企业内部应用，填入 AppKey/AppSecret/AgentID 启用工作通知',
      '将审批回调地址（/api/webhooks/dingtalk/approval-callback）设置为机器人接收消息',
      '开启「钉钉机器人集成」开关',
      '确保前置模块（压缩管理、注入检测、会话缓存、会话审计）均已启用',
    ],
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