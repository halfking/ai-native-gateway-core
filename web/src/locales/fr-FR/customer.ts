// customer.ts — Customer UI (fr-FR, from en-US) (2026-07-13)
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
    expiredBanner: 'Licence expirée : certaines fonctions sont bloquées. Renouvelez et réactivez.',
    revokedBanner: 'Licence révoquée : demandez une nouvelle licence ou contactez l\'autorité de licence.',
    reactivate: 'Re-activate',
    loadFailed: 'Failed to load license info: {msg}',
  },
  versionsUpgradeConfirmBody: 'Le service sera mis à niveau vers {version} ({label}). Il sera brièvement indisponible. Vérifiez qu\'aucune tâche importante n\'est en cours.',
  versionsUpgradeConfirmTitle: 'Confirmer la mise à niveau',
  versionsUpgradeConfirmStart: 'Démarrer la mise à niveau',
}
