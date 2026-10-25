export default {
  title: 'Operations Platform',

  // License Management
  license: {
    title: 'License Management',
    create: 'Create License',
    createTitle: 'Create New License',
    createSuccess: 'License created successfully',
    createFailed: 'Failed to create license',
    fillRequired: 'Please fill in required fields',
    loadFailed: 'Failed to load licenses',
    loadDevicesFailed: 'Failed to load devices',
    revoke: 'Revoke',
    revokeConfirm: 'Voulez-vous vraiment révoquer la licence du client {customer} ?',
    revokeSuccess: 'License revoked successfully',
    revokeFailed: 'Failed to revoke license',
    licenseKey: 'License Key',
    customer: 'Customer',
    customerPlaceholder: 'Enter customer name',
    customerEmail: 'Email',
    customerEmailPlaceholder: 'Enter email address',
    subscriptionTier: 'Subscription Tier',
    tier: 'Tier',
    modules: 'Modules',
    modulesTitle: 'Module Management',
    features: 'Features',
    maxDevices: 'Max Devices',
    featuresPlaceholder: 'Comma-separated feature flags',
    devices: 'Devices',
    expiresAt: 'Expires At',
    selectDate: 'Select date and time',
    hostname: 'Hostname',
    deviceId: 'Device ID',
    activatedAt: 'Activated At',
    lastSeen: 'Last Seen',
    offlineRequests: 'Offline Activation Requests',
    offlineActivation: 'Offline Activation',
    requestCode: 'Request Code',
    activationCode: 'Activation Code',
    approve: 'Approve',
    reject: 'Reject',
    approveSuccess: 'Activation request approved',
    approveFailed: 'Failed to approve',
    rejectTitle: 'Reject Activation',
    rejectReason: 'Enter rejection reason',
    rejectSuccess: 'Activation request rejected',
    rejectFailed: 'Failed to reject',
    deactivate: 'Deactivate',
    deactivateConfirm: 'Deactivate Device',
    deactivateReason: 'Enter deactivation reason',
    deactivateSuccess: 'Device deactivated successfully',
    deactivateFailed: 'Failed to deactivate device',
    status: {
      active: 'Active',
      expired: 'Expired',
      revoked: 'Revoked',
      pending: 'Pending',
      approved: 'Approved',
      rejected: 'Rejected',
    },

    holdersTab: 'License Holders',

    licensesTab: 'Licenses',

    holderEmail: 'Holder Email',

    holderLicenses: 'Licenses',

    holderDevices: 'Devices',

    holderDonations: 'Total Donated',

    loadHoldersFailed: 'Failed to load holders',
  },

  // Fault Management
  fault: {
    title: 'Fault Management',
    loadFailed: 'Failed to load fault data',
    fillRequired: 'Please fill in required fields',
    events: 'Fault Events',
    rules: 'Fault Rules',
    createRule: 'Create Rule',
    createSuccess: 'Rule created successfully',
    createFailed: 'Failed to create rule',
    editRule: 'Edit Rule',
    updateSuccess: 'Rule updated successfully',
    saveFailed: 'Failed to save',
    deleteRuleConfirm: 'Voulez-vous vraiment supprimer la règle {name} ?',
    deleteSuccess: 'Rule deleted successfully',
    deleteFailed: 'Failed to delete',
    fix: 'Fix',
    fixConfirm: 'Voulez-vous vraiment déclencher la correction manuelle ?',
    fixTriggered: 'Fix triggered',
    fixFailed: 'Failed to trigger fix',
    totalEvents: 'Total Events',
    openEvents: 'Open',
    resolvedEvents: 'Resolved',
    avgResolutionTime: 'Avg Resolution Time',
    ruleName: 'Rule Name',
    ruleNamePlaceholder: 'Enter rule name',
    descriptionPlaceholder: 'Enter description',
    severityLabel: 'Severity',
    condition: 'Condition',
    conditionPlaceholder: 'Enter trigger condition',
    autoFix: 'Auto Fix',
    message: 'Message',
    detectedAt: 'Detected At',
    resolvedAt: 'Resolved At',
    status: {
      open: 'Open',
      resolving: 'Resolving',
      resolved: 'Resolved',

      new: 'New',

      acknowledged: 'Acknowledged',

      ignored: 'Ignored',
    },
    severity: {
      critical: 'Critical',
      warning: 'Warning',
      info: 'Info',

      error: 'Error',
    },

    acknowledge: 'Acknowledge',

    resolve: 'Resolve',

    resolvedEvents24h: 'Resolved (24h)',

    titleLabel: 'Title',

    source: 'Source',

    metric: 'Metric',

    operator: 'Operator',

    threshold: 'Threshold',

    duration: 'Duration',

    action: 'Action',

    actionConfig: 'Action Config',

    cooldown: 'Cooldown',

    ackedAt: 'Acknowledged At',

    metadata: 'Metadata',

    eventDetail: 'Event Detail',
  },

  // Auto Update
  autoupdate: {
    title: 'Auto Update',
    loadFailed: 'Failed to load releases',
    loadLogsFailed: 'Failed to load upgrade logs',
    fillRequired: 'Please fill in required fields',
    releases: 'Releases',
    createRelease: 'Create Release',
    createReleaseTitle: 'Create New Release',
    createSuccess: 'Release created successfully',
    createFailed: 'Failed to create release',
    publish: 'Publish',
    publishConfirm: 'Publish version {version}?',
    publishSuccess: 'Release published successfully',
    publishFailed: 'Failed to publish release',
    unpublish: 'Unpublish',
    unpublishConfirm: 'Unpublish version {version}?',
    unpublishSuccess: 'Release unpublished successfully',
    unpublishFailed: 'Failed to unpublish release',
    rollback: 'Rollback',
    rollbackTitle: 'Rollback to Version',
    rollbackConfirm: 'Voulez-vous vraiment revenir à la version {version} ?',
    rollbackSuccess: 'Rollback successful',
    rollbackFailed: 'Failed to rollback',
    rollbackWarning: 'Le retour arrière restaure la version cible. Vérifiez que des sauvegardes existent avant de continuer.',
    targetVersion: 'Target Version',
    targetVersionPlaceholder: 'e.g. v1.0.0',
    version: 'Version',
    versionPlaceholder: 'e.g. v2.0.0',
    buildSeq: 'Build #',
    releaseTitle: 'Release Title',
    releaseTitlePlaceholder: 'Enter release title',
    channelLabel: 'Channel',
    imageTag: 'Image Tag',
    imageTagPlaceholder: 'ex. registry.example.com/gateway:v2.0.0',
    imageDigest: 'Image Digest',
    minVersion: 'Min Version',
    mandatory: 'Mandatory',
    createdBy: 'Created By',
    createdByPlaceholder: 'Enter creator name',
    description: 'Description',
    changelog: 'Changelog',
    publishedAt: 'Published At',
    rolloutPercentage: 'Rollout %',
    gray: 'Gray Release',
    grayTitle: 'Create Gray Release Rule',
    grayPhase: 'Phase',
    grayCreate: 'Create Rule',
    grayCreateSuccess: 'Gray release rule created',
    grayCreateFailed: 'Failed to create gray release rule',
    upgradeLogs: 'Upgrade Logs',
    startedAt: 'Started At',
    completedAt: 'Completed At',
    errorMessage: 'Error',
    retryCount: 'Retries',
    channel: {
      stable: 'Stable',
      beta: 'Beta',
      canary: 'Canary',
      _unknown: 'Inconnu',
    },
    logStatus: {
      pending: 'Pending',
      downloading: 'Downloading',
      ready_to_restart: 'Ready',
      upgrading: 'Upgrading',
      success: 'Success',
      failed: 'Failed',
      rolled_back: 'Rolled Back',
      _unknown: 'Inconnu',
    },

    grayPhaseCanary: 'Canary',

    grayPhaseBatch1: 'Batch 1',

    grayPhaseBatch2: 'Batch 2',

    grayPhaseBatch3: 'Batch 3',

    grayPhaseFull: 'Full',

    rolloutGate: 'Rollout Gate',

    rolloutTitle: 'Rollout Gate: {version}',

    rolloutRuleStatus: 'Rule Status',

    rolloutGateAllowed: 'Advance Allowed',

    rolloutSuccessRate: 'Success Rate',

    rolloutRollbackRate: 'Rollback Rate',

    rolloutSamples: 'Upgrade Samples',

    rolloutSuccessCount: 'Success',

    rolloutFailedCount: 'Failed',

    rolloutRolledBackCount: 'Rolled Back',

    rolloutPause: 'Pause Rollout',

    rolloutResume: 'Resume Rollout',

    rolloutPauseSuccess: 'Rollout paused',

    rolloutResumeSuccess: 'Rollout resumed',

    rolloutLoadFailed: 'Failed to load rollout gate status',

    rolloutActionFailed: 'Rollout gate action failed',
  },

  // Center Operations
  center: {
    title: 'Center Operations',
    loadFailed: 'Failed to load instances',
    loadHeartbeatFailed: 'Failed to load heartbeat history',
    commandSent: 'Command sent',
    commandFailed: 'Failed to send command',
    instanceId: 'Instance ID',
    hostname: 'Hostname',
    version: 'Version',
    uptime: 'Uptime',
    lastHeartbeat: 'Last Heartbeat',
    onlineInstances: 'Online',
    degradedInstances: 'Degraded',
    offlineInstances: 'Offline',
    cpuUsage: 'CPU Usage',
    memoryUsage: 'Memory Usage',
    diskUsage: 'Disk Usage',
    heartbeatHistory: 'Heartbeat History (24h)',
    sendCommand: 'Send Command',
    sendCommandTitle: 'Send Command to Instance',
    command: 'Command',
    parameters: 'Parameters',
    parametersPlaceholder: 'Enter JSON parameters (optional)',
    paramsInvalidJSON: 'Invalid JSON in parameters',
    status: {
      online: 'Online',
      offline: 'Offline',
      degraded: 'Degraded',
    },

    lastCommand: 'Last command {id}: status: {status}',

    ipAddress: 'IP Address',

    region: 'Region',

    buildSeq: 'Build',

    startedAt: 'Started At',

    totalInstances: 'Total',

    cmd: {
      restart: 'Restart Service',
      upgrade: 'Upgrade Version',
      configUpdate: 'Update Config',
      healthCheck: 'Health Check',
      collectLogs: 'Collect Logs',
    },

    alerts: {
      title: 'Operational Alerts',
      openCount: 'Open {count}',
      empty: 'No alerts',
      severity: 'Severity',
      alertTitle: 'Title',
      message: 'Message',
      source: 'Source',
      detectedAt: 'Detected At',
      acknowledge: 'Acknowledge',
      resolve: 'Resolve',
      suppress: 'Suppress 24h',
      ackSuccess: 'Alert acknowledged',
      resolveSuccess: 'Alert resolved',
      suppressSuccess: 'Alert suppressed for 24 hours',
      actionFailed: 'Alert action failed',
      status: {
        triggered: 'Triggered',
        acknowledged: 'Acknowledged',
        resolved: 'Resolved',
        suppressed: 'Suppressed',
      },
    },
  },

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: 'Échec du chargement des données',
    fillRequired: 'Veuillez remplir les champs obligatoires',
    projects: 'Projets',
    sessions: 'Sessions',
    codeReviews: 'Revues de code',
    createProject: 'Créer un projet',
    createProjectTitle: 'Créer un nouveau projet',
    createProjectSuccess: 'Projet créé avec succès',
    createProjectFailed: 'Échec de la création du projet',
    newSession: 'Nouvelle session',
    createSessionTitle: 'Créer une nouvelle session',
    createSessionSuccess: 'Session créée avec succès',
    createSessionFailed: 'Échec de la création de la session',
    viewSessions: 'Voir les sessions',
    viewReviews: 'Voir les revues',
    showAll: 'Tout afficher',
    reviewDetail: 'Détail de la revue',
    projectName: 'Nom du projet',
    projectNamePlaceholder: 'Saisissez le nom du projet',
    language: 'Langage',
    languagePlaceholder: 'ex. TypeScript',
    framework: 'Framework',
    frameworkPlaceholder: 'ex. Vue 3',
    projectId: 'ID du projet',
    sessionName: 'Nom de la session',
    sessionNamePlaceholder: 'Enter session name',
    taskType: 'Type de tâche',
    taskTypePlaceholder: 'ex. revue de code, refactorisation, explication',
    duration: 'Duration',
    startedAt: 'Démarré le',
    endedAt: 'Terminé le',
    filePath: 'Chemin du fichier',
    score: 'Note',
    summary: 'Résumé',
    issues: 'Problèmes',
    suggestions: 'Recommandations',
    category: 'Catégorie',
    reviewedAt: 'Revu le',
    line: 'Ligne',
    severity: 'Gravité',
    message: 'Message',
    code: 'Code',
    suggestedCode: 'Suggested Code',
    status: {
      active: 'Active',
      archived: 'Archived',
      completed: 'Completed',
      _unknown: 'Inconnu',
    },
  },


  overview: {
    title: 'Vue d’ensemble des opérations',
    loadFailed: 'Échec du chargement de la vue d’ensemble',
    onlineInstances: 'Instances en ligne',
    totalLicenses: 'Licences totales',
    pendingApprovals: 'Activations en attente',
    todayUpgrades: 'Mises à niveau du jour',
    openFaults: 'Pannes ouvertes',
    recentUpgrades: 'Mises à niveau récentes',
    recentFaults: 'Alertes récentes',
    pendingOffline: 'Activations hors ligne en attente',
    viewAll: 'Tout voir',

    subtitle: 'État du cluster en un coup d\'œil. Ouvrez un nœud pour la charge, les appels et les erreurs',

    licenseSubsystem: 'License Subsystem',

    licenseModeNormal: 'Operational',

    licenseModeRestricted: 'Restricted',

    licenseModeGrace: 'Grace ({hours}h left)',

    lastRefresh: 'Last Refresh',

    consecutiveFailures: 'Consecutive Failures',

    totalCycles: '{n} cycles',

    lastError: 'Last Error',

    justNow: 'just now',

    minutesAgo: '{n}m ago',

    hoursAgo: '{n}h ago',

    daysAgo: '{n}d ago',

    todayDownloads: 'Downloads Today',

    weekDownloads: '7-Day Downloads',

    supporterCount: 'Supporters',

    donationTotal: 'Donations (CNY)',

    activationRate: '30d Activation Rate',

    publicPortal: 'Public Portal',

    deploymentNodes: 'Deployment Nodes',

    topologyTitle: 'Deployment Topology',

    topologyHint: 'Groupé par région. Les nœuds dispersés restent grands, les grilles denses passent à la ligne. Cliquez sur un nœud pour les détails.',

    regionEmpty: 'No registered nodes',

    nodesOnlineOf: '{online} / {total} online',

    noHeartbeat: 'no heartbeat',

    viewNode: 'Details',

    nodeDetailTitle: 'Node Detail',

    nodeDetailLoadFailed: 'Failed to load node detail',
    nodeDetailSuperAdminOnlyHint: 'Seuls les super administrateurs peuvent maintenir les nœuds ; affichage en lecture seule.',
    nodeDetailDefaultTenantOnlyHint: 'Seul le tenant default peut maintenir les nœuds ; affichage en lecture seule.',

    perfPressure: 'Performance Pressure',

    memory: 'Memory',

    concurrency: 'Concurrency',

    requestSummary: 'Request Summary',

    requestsTotal: 'Total',

    requestsOk: 'OK',

    requestsErr: 'Errors',

    successRate: 'Success Rate',

    avgLatency: 'Avg Latency',

    nodeErrors: 'Errors & Alerts',

    noNodeErrors: 'No open alerts',

    regionMissing: 'Not registered',

    regionOnline: 'Online',

    regionDegraded: 'Degraded',

    regionOffline: 'Offline',

    regionOnlineCount: '{n} online',

    dataPlaneTables: '252 data-plane row counts',
  }
,
  downloads: {
    title: 'Download Releases',
    subtitle: 'Compiler, vérifier, empaqueter les instalateurs hors ligne et publier sur download.kxpms.cn',
    publishBtn: 'Publier la version actuelle sur le site de téléchargement',
    publishOk: 'Published successfully',
    publishFailed: 'Publish failed',
    loadFailed: 'Failed to load download release data',
    todayDl: 'Downloads today',
    totalDl: 'Total downloads',
    supporters: 'Supporters',
    activationRate: 'Activation rate',
    currentVersion: 'Current release',
    version: 'Version',
    buildSeq: 'Build seq',
    gitRepo: 'Open-source repo',
    artifacts: 'Published artifacts',
    platform: 'Platform',
    file: 'File',
    size: 'Size',
    path: 'Storage path',
    publishHistory: 'Publish history',
    status: 'Status',
    artifactCount: 'Artifacts',
    tests: 'Tests',
    createdAt: 'Created',
    summary: 'Summary',
    storageHint: 'Les paquets hors ligne sont stockés sur 245 dans /var/www/download/llm-gateway-go/v{version}/. La publication lance les tests de distribution et écrit des lignes d\'audit dans download_publish_runs.',
    openPublicDownload: 'Open public download page',
    openActivate: 'Open activation wizard',
  },

  blocklist: {
    title: 'IP Blocklist',
    loadFailed: 'Failed to load blocklist',
    add: 'Add block',
    ip: 'IP/CIDR',
    ipPlaceholder: 'e.g. 203.0.113.10 or 10.0.0.0/8',
    ipRequired: 'IP or CIDR required',
    reason: 'Reason',
    scope: 'Scope',
    hits: 'Hits',
    total: '{n} entries',
    createSuccess: 'Added',
    createFailed: 'Add failed',
    updateFailed: 'Update failed',
    deleteConfirm: 'Delete {ip}?',
    deleteSuccess: 'Deleted',
    reloadCache: 'Reload Redis cache',
    reloadSuccess: 'Cache reloaded',
    reloadFailed: 'Cache reload failed',
  },
}
