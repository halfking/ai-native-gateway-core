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
  updateActivate: {
    summary: '站台資訊、一鍵啟用、版本升級與已開通模組。',
    catalogLoadFailed: '版本目錄沒能載入出來。請檢查中心是否可達',
    modulesLoadFailed: '模組清單沒能載入出來。請檢查中心是否可達',
    agreementRequired: '請先同意使用者協議，再繼續激活',
    activateFailed: '激活沒能完成，請重試',
  },
  versionsUpgradeConfirmBody: '即將把服務升級到 {version}（{label}）。過程中服務會短暫不可用，請確認無重要任務正在進行。',
  versionsUpgradeConfirmTitle: '確認升級',
  versionsUpgradeConfirmStart: '開始升級',
  versions: {
    switchFailedWithReason: '切換沒能啟動：{reason}。請重試',
  },
}
