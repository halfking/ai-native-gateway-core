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
    sourceSessionTurns: 'Session turns',
  },
  chart: {
    allHealthy: 'All healthy',
    errorsWhenHealthy: 'Error distribution: all healthy',
    errorsTitle: 'Error distribution',
    /**
     * Empty state for the credential heatmap (2026-10-27 round 14 — the
     * "5-point empty state" split).
     * ★ kind = **Filtered**: this page has a model filter + "exclude self-check"
     *   + "errors only" + a time range ⇒ empty means "filtered out",
     *   not "this page has no data".
     *   It used to be the generic key `common.feedback.noData`
     *   ("No content to display right now") plus a hardcoded Chinese hint
     *   ("please adjust the time range or filters") ⇒ now one complete,
     *   translatable sentence that also gives the next useful action.
     */
    emptyFiltered: 'No credential records match the current time range and filters. Widen the time range or relax the filters, then check again',
  },
  error: {
    selectFirst: 'Please select credentials first',
    clearFailed: 'Clear failed: ',
    batchFailed: 'Batch operation failed: ',
    superAdminOnlyHint: 'Only super admins can perform status corrections; currently read-only.',
    superAdminOnlyError: 'Only super admins can perform status corrections.',
  },
}