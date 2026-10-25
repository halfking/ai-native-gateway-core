// customer.ts — Customer-facing UI translations (2026-07-13)
export default {
  info: {
    title: 'License 信息',
    refresh: '刷新',
    gotoActivate: '前往激活向导',
    notActivated: '本机尚未激活 License',
    states: {
      active: '已激活',
      grace: '宽限期',
      expired: '已过期',
      revoked: '已吊销',
      none: '未激活',
    },
    expiredBanner: 'License 已过期，部分功能受限。请续期后重新激活。',
    revokedBanner: 'License 已被吊销，请重新申请或联系 License Authority。',
    reactivate: '重新激活',
    loadFailed: '查询 License 信息失败: {msg}',
  },
  updateActivate: {
    summary: '站点信息、一键激活、版本升级与已开通模块。',
  },
  versionsUpgradeConfirmBody: '即将把服务升级到 {version}（{label}）。过程中服务会短暂不可用，请确认无重要任务正在进行。',
  versionsUpgradeConfirmTitle: '确认升级',
  versionsUpgradeConfirmStart: '开始升级',
}
