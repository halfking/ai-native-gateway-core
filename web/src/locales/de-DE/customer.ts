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
    expiredBanner: 'Lizenz abgelaufen. Einige Funktionen sind gesperrt. Erneuern und erneut aktivieren.',
    revokedBanner: 'Lizenz widerrufen. Beantragen Sie eine neue Lizenz oder wenden Sie sich an die Lizenzstelle.',
    reactivate: 'Re-activate',
    loadFailed: 'Failed to load license info: {msg}',
  },
  versionsUpgradeConfirmBody: 'Der Dienst wird auf {version} ({label}) aktualisiert. Dabei ist er kurzzeitig nicht verfügbar. Stellen Sie sicher, dass keine wichtigen Aufgaben laufen.',
  versionsUpgradeConfirmTitle: 'Upgrade bestätigen',
  versionsUpgradeConfirmStart: 'Upgrade starten',
}
