// credentialMonitor.ts — Credential monitor page (CredentialMonitorView).
// Mirrors zh-CN/credentialMonitor.ts structure. Backend enum values
// (ready/degraded/healthy/warning/broken/etc.) stay as-is in the UI —
// translating them would break the API contract.
export default {
  page: {
    title: 'Credential Monitor',
  },
  table: {
    cell: {
      modelsNotMeasured: 'This credential returned no model counts this time (not measured). Not "0 available"',
    },
  },
  drawer: {
    tab: {
      overview: 'Overview',
      models: 'Models',
      requests: 'Requests',
    },
    operationFailedWithReason: 'Operation failed: {reason}. Please retry.',
    offlineFailedWithReason: 'Offline failed: {reason}. Please retry.',
    onlineFailedWithReason: 'Online failed: {reason}. Please retry.',
    degradeFailedWithReason: 'Demote failed: {reason}. Please retry.',
    upgradeFailedWithReason: 'Upgrade failed: {reason}. Please retry.',
    settingFailedWithReason: 'Setting failed: {reason}. Please retry.',
    manualDisabledTooltip: 'Disabled manually, so auto-probe will not touch it. You can start it manually.',
    availableTooltip: 'Currently available. You can disable it manually.',
    autoControlledTooltip: 'Controlled by auto-probe (such as broken_confirmed). You can disable it manually (force offline).',
    rowSelectsMonitor: 'Select a row to view the linked monitoring on the right.',
    selectModelRow: 'Select a model row on the left to view it.',
  },
  chart: {
    allHealthy: 'All healthy',
    errorsWhenHealthy: 'Error distribution: all healthy',
    errorsTitle: 'Error distribution',
  },
  error: {
    selectFirst: 'Please select credentials first',
    clearFailed: 'Clear failed: ',
    batchFailed: 'Batch operation failed: ',
    superAdminOnlyHint: 'Only super admins can perform status corrections; currently read-only.',
    superAdminOnlyError: 'Only super admins can perform status corrections.',
  },
}