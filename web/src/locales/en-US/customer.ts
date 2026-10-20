// customer.ts — Customer-facing UI translations (2026-07-13)
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
  versionsUpgradeConfirmBody: 'The service will be upgraded to {version} ({label}). It will be briefly unavailable — make sure no important tasks are running.',
  versionsUpgradeConfirmTitle: 'Confirm upgrade',
  versionsUpgradeConfirmStart: 'Start upgrade',
}
