// public.ts — 公开门户文案（下载 / 捐赠 / 离线激活）
export default {
  download: {
    activateLink: '安装后激活向导',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: '下载',
    support: '支持我们',
  },
  agreement: {
    title: '用户协议与使用须知',
    intro: '欢迎使用 AI-Native 组织核心网关。本软件内核以开源方式分享，标准版按「现状提供（AS IS）」原则供您自行部署与使用。继续之前，请先看下面四条：',
    points: [
      '权利与义务：您负责在自有环境里的安装、配置、备份与合规使用。我们提供软件和文档，不对业务结果、模型输出质量或第三方服务的可用性作保证。',
      '数据采集：默认不采集业务内容。可选的运行遥测只包含版本、实例标识、License 状态和聚合指标，不采集提示词、会话原文、API Key 或可直接识别到个人的数据。',
      '开源分享：非定制版本以社区分享为目的，不含商业 SLA。需要定制时，请通过支持页联系我们另行约定。',
      '免责范围：软件按现状提供。我们不承担间接损失、业务中断、数据丢失或 AI 决策带来的后果，风险请您自己评估并做好防护。',
    ],
    disclaimer: '完整条款见用户协议全文。勾选即表示您已阅读并理解上述范围。',
    checkbox: '我已阅读并同意',
    fullLink: '《用户协议与数据处理授权》',
    accept: '同意并继续',
  },
  opAgreement: {
    downloadTitle: '下载前请阅读 {title}',
    activateTitle: '激活前请阅读 {title}',
  },
}
