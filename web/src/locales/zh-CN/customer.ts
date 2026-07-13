// customer.ts — Customer-facing UI translations (2026-07-13)
//
// Used by ActivationWizard.vue, LicenseInfoView.vue, UpgradePanel.vue,
// UpgradeBanner.vue. Keep keys minimal — the customer UI is short.

export default {
  wizard: {
    states: {
      active: '已激活',
      grace: '宽限期',
      expired: '已过期',
      revoked: '已吊销',
      none: '未激活',
    },
    headerTitle: 'License 激活向导',
    headerSubtitle: '通过 4 步完成首次激活或重新激活',
  },
}