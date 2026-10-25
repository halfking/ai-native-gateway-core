// customer.ts — Customer UI (ja-JP, from en-US) (2026-07-13)
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
    summary: 'サイト情報、ワンクリック有効化、バージョンアップグレード、開封済みモジュール。',
  },
  versionsUpgradeConfirmBody: 'サービスを {version}（{label}）へアップグレードします。処理中は短期間利用できなくなります。重要なタスクが実行中でないことを確認してください。',
  versionsUpgradeConfirmTitle: 'アップグレードの確認',
  versionsUpgradeConfirmStart: 'アップグレード開始',
}
