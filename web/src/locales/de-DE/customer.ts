// customer.ts — Customer UI (de-DE, from en-US) (2026-07-13)
export default {

  info: {
    title: 'License information',
    refresh: 'Refresh',
    gotoActivate: 'Go to activation wizard',
    notActivated: 'This instance has no active license',
    states: {
      active: 'Activated',
      grace: 'Grace period',
      expired: 'Expired',
      revoked: 'Revoked',
      none: 'Not activated',
    },
    expiredBanner: 'License expired — some features are restricted. Renew and re-activate.',
    revokedBanner: 'License revoked — request a new license or contact License Authority.',
    reactivate: 'Re-activate',
    loadFailed: 'Failed to load license info: {msg}',
  },
  versionsUpgradeConfirmBody: 'Der Dienst wird auf {version} ({label}) aktualisiert. Dabei ist er kurzzeitig nicht verfügbar — stellen Sie sicher, dass keine wichtigen Aufgaben laufen.',
  versionsUpgradeConfirmTitle: 'Upgrade bestätigen',
  versionsUpgradeConfirmStart: 'Upgrade starten',
}
