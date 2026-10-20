// customer.ts — 客戶端 UI 文案（zh-TW） (2026-07-13)
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
  versionsUpgradeConfirmBody: '即將把服務升級到 {version}（{label}）。過程中服務會短暫不可用，請確認無重要任務正在進行。',
  versionsUpgradeConfirmTitle: '確認升級',
  versionsUpgradeConfirmStart: '開始升級',
}
