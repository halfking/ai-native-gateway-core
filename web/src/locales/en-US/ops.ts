export default {


  overview: {
    // from the licence-management catalogue cards).
    nodeDetailSuperAdminOnlyHint: 'Only super admins can maintain nodes; showing read-only.',
    nodeDetailDefaultTenantOnlyHint: 'Only the default tenant can maintain nodes; showing read-only.',
    pingFailedWithReason: 'Session Ping failed: {reason}. Please retry.',
    saveSettingFailedWithReason: 'The setting could not be saved: {reason}. Please retry.',
    maintainFailed: 'The maintenance could not be completed. Please retry.',
    modelMaintainFailed: 'The model maintenance could not be completed. Please retry.',
    fpSlotLoadFailed: 'The fingerprint slots could not be loaded. Please retry.',
    fpSlotProviderMissing: 'provider_id is missing, so the fingerprint slots could not be loaded. Select a provider first, then try again.',
    accessErrorsHint: 'Failed calls + routing errors · Open the original request details',
    recentRequestsHint: 'Last 1 hour{source} · View request details',
  },


  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: 'Failed to load data',
    fillRequired: 'Please fill in required fields',
    projects: 'Projects',
    sessions: 'Sessions',
    codeReviews: 'Code Reviews',
    createProject: 'Create Project',
    createProjectTitle: 'Create New Project',
    createProjectSuccess: 'Project created successfully',
    createProjectFailed: 'Failed to create project',
    newSession: 'New Session',
    createSessionTitle: 'Create New Session',
    createSessionSuccess: 'Session created successfully',
    createSessionFailed: 'Failed to create session',
    viewSessions: 'View Sessions',
    viewReviews: 'View Reviews',
    showAll: 'Show All',
    reviewDetail: 'Review Detail',
    projectName: 'Project Name',
    projectNamePlaceholder: 'Enter project name',
    language: 'Language',
    languagePlaceholder: 'e.g. TypeScript',
    framework: 'Framework',
    frameworkPlaceholder: 'e.g. Vue 3',
    projectId: 'Project ID',
    sessionName: 'Session Name',
    taskType: 'Task Type',
    taskTypePlaceholder: 'e.g. code review, refactor, explain',
    startedAt: 'Started At',
    endedAt: 'Ended At',
    filePath: 'File Path',
    score: 'Score',
    summary: 'Summary',
    issues: 'Issues',
    suggestions: 'Suggestions',
    category: 'Category',
    reviewedAt: 'Reviewed At',
    line: 'Line',
    severity: 'Severity',
    message: 'Message',
    /**
     * Empty states (2026-10-27 round 14 — the "5-point empty state" split).
     * ★ The three tables used to share one `hyper.list.empty`
     *   ("The list has no records for now"), but they are **two different** states:
     *   · projects — uses the raw array ⇒ empty = no project was ever created
     *     (FirstUse)
     *   · sessions / reviews — use the **filtered** arrays ⇒ empty = the current
     *     filters hid everything (Filtered)
     *   ⇒ One sentence covered two states, i.e. it told the user "you have not
     *   started" and "your filters are too narrow" as if they were the same.
     */
    emptyFirstUseProjects: 'No projects have been created yet',
    emptyFilteredSessions: 'No session records match the current filters',
    emptyFilteredReviews: 'No code review records match the current filters',
  },
}
