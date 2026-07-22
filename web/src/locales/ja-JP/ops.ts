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
    revokeConfirm: 'Are you sure you want to revoke the license for customer {customer}?',
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

    holdersTab: '持有人',

    licensesTab: 'License 列表',

    holderEmail: '持有人邮箱',

    holderLicenses: 'License 数',

    holderDevices: '设备数',

    holderDonations: '累计捐赠',

    loadHoldersFailed: '加载持有人失败',
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
    deleteRuleConfirm: 'Are you sure you want to delete rule {name}?',
    deleteSuccess: 'Rule deleted successfully',
    deleteFailed: 'Failed to delete',
    fix: 'Fix',
    fixConfirm: 'Are you sure you want to trigger manual fix?',
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

      new: '新建',

      acknowledged: '已确认',

      ignored: '已忽略',
    },
    severity: {
      critical: 'Critical',
      warning: 'Warning',
      info: 'Info',

      error: '错误',
    },

    acknowledge: '确认',

    resolve: '解决',

    resolvedEvents24h: '已解决 (24h)',

    titleLabel: '标题',

    source: '来源',

    metric: '指标',

    operator: '运算符',

    threshold: '阈值',

    duration: '持续时间',

    action: '动作',

    actionConfig: '动作配置',

    cooldown: '冷却时间',

    ackedAt: '确认时间',

    metadata: '元数据',

    eventDetail: '事件详情',
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
    rollbackConfirm: 'Are you sure you want to rollback to version {version}?',
    rollbackSuccess: 'Rollback successful',
    rollbackFailed: 'Failed to rollback',
    rollbackWarning: 'Rollback will revert to the target version. Ensure backups are available before proceeding.',
    targetVersion: 'Target Version',
    targetVersionPlaceholder: 'e.g. v1.0.0',
    version: 'Version',
    versionPlaceholder: 'e.g. v2.0.0',
    buildSeq: 'Build #',
    releaseTitle: 'Release Title',
    releaseTitlePlaceholder: 'Enter release title',
    channelLabel: 'Channel',
    imageTag: 'Image Tag',
    imageTagPlaceholder: 'e.g. registry.example.com/gateway:v2.0.0',
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
      _unknown: '不明',
    },
    logStatus: {
      pending: 'Pending',
      downloading: 'Downloading',
      ready_to_restart: 'Ready',
      upgrading: 'Upgrading',
      success: 'Success',
      failed: 'Failed',
      rolled_back: 'Rolled Back',
      _unknown: '不明',
    },

    grayPhaseCanary: '金丝雀',

    grayPhaseBatch1: '第一批',

    grayPhaseBatch2: '第二批',

    grayPhaseBatch3: '第三批',

    grayPhaseFull: '全量',

    rolloutGate: '灰度门禁',

    rolloutTitle: '灰度门禁 · {version}',

    rolloutRuleStatus: '规则状态',

    rolloutGateAllowed: '允许推进',

    rolloutSuccessRate: '升级成功率',

    rolloutRollbackRate: '回滚率',

    rolloutSamples: '升级样本',

    rolloutSuccessCount: '成功',

    rolloutFailedCount: '失败',

    rolloutRolledBackCount: '回滚',

    rolloutPause: '暂停灰度',

    rolloutResume: '恢复灰度',

    rolloutPauseSuccess: '灰度已暂停',

    rolloutResumeSuccess: '灰度已恢复',

    rolloutLoadFailed: '加载灰度门禁状态失败',

    rolloutActionFailed: '灰度门禁操作失败',
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

    lastCommand: '最近命令 {id} · 状态：{status}',

    ipAddress: 'IP 地址',

    region: '区域',

    buildSeq: '构建号',

    startedAt: '启动时间',

    totalInstances: '总数',

    cmd: {
      restart: '重启服务',
      upgrade: '升级版本',
      configUpdate: '更新配置',
      healthCheck: '健康检查',
      collectLogs: '收集日志',
    },

    alerts: {
      title: '运维告警',
      openCount: '未关闭 {count}',
      empty: '暂无告警',
      severity: '级别',
      alertTitle: '标题',
      message: '详情',
      source: '来源',
      detectedAt: '检测时间',
      acknowledge: '确认',
      resolve: '解决',
      suppress: '压制 24h',
      ackSuccess: '告警已确认',
      resolveSuccess: '告警已解决',
      suppressSuccess: '告警已压制 24 小时',
      actionFailed: '告警操作失败',
      status: {
        triggered: '待处理',
        acknowledged: '已确认',
        resolved: '已解决',
        suppressed: '已压制',
      },
    },
  },

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
    sessionNamePlaceholder: 'Enter session name',
    taskType: 'Task Type',
    taskTypePlaceholder: 'e.g. code review, refactor, explain',
    duration: 'Duration',
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
    code: 'Code',
    suggestedCode: 'Suggested Code',
    status: {
      active: 'Active',
      archived: 'Archived',
      completed: 'Completed',
      _unknown: '不明',
    },
  },


  overview: {
    title: '運用概要',
    loadFailed: '概要データの読み込みに失敗しました',
    onlineInstances: 'オンラインインスタンス',
    totalLicenses: 'ライセンス総数',
    pendingApprovals: '承認待ちアクティベーション',
    todayUpgrades: '本日のアップグレード',
    openFaults: '未処理の障害',
    recentUpgrades: '最近のアップグレード',
    recentFaults: '最新アラート',
    pendingOffline: '承認待ちオフラインアクティベーション',
    viewAll: 'すべて表示',

    subtitle: '一眼掌握集群健康；点进节点查看性能压力、请求汇总与错误详情',

    licenseSubsystem: '许可子系统',

    licenseModeNormal: '运行中',

    licenseModeRestricted: '已停服',

    licenseModeGrace: '宽容期（剩余 {hours} 小时）',

    lastRefresh: '上次刷新',

    consecutiveFailures: '连续失败次数',

    totalCycles: '共 {n} 轮',

    lastError: '最近错误',

    justNow: '刚刚',

    minutesAgo: '{n} 分钟前',

    hoursAgo: '{n} 小时前',

    daysAgo: '{n} 天前',

    todayDownloads: '今日下载',

    weekDownloads: '7 日下载',

    supporterCount: '支持者',

    donationTotal: '捐赠总额',

    activationRate: '30 日激活率',

    publicPortal: '公开门户',

    deploymentNodes: '部署节点',

    topologyTitle: '部署拓扑',

    topologyHint: '按区域分区；节点少时卡片放大，多时自动换行。点击节点查看详情。',

    regionEmpty: '未注册节点',

    nodesOnlineOf: '{online} / {total} 在线',

    noHeartbeat: '无心跳',

    viewNode: '详情',

    nodeDetailTitle: '节点详情',

    nodeDetailLoadFailed: '加载节点详情失败',

    perfPressure: '性能压力',

    memory: '内存',

    concurrency: '并发',

    requestSummary: '请求汇总',

    requestsTotal: '总请求',

    requestsOk: '成功',

    requestsErr: '错误',

    successRate: '成功率',

    avgLatency: '平均延迟',

    nodeErrors: '错误与告警',

    noNodeErrors: '暂无告警',

    regionMissing: '未注册',

    regionOnline: '在线',

    regionDegraded: '降级',

    regionOffline: '离线',

    regionOnlineCount: '{n} 在线',

    dataPlaneTables: '252 数据面表记录数',
  }
,
  downloads: {
    title: '下载发版',
    subtitle: '编译验证、打包离线安装包并发布到 download.kxpms.cn 版本目录',
    publishBtn: '发布当前版本到下载站',
    publishOk: '发布成功',
    publishFailed: '发布失败',
    loadFailed: '加载下载发版数据失败',
    todayDl: '今日下载',
    totalDl: '累计下载',
    supporters: '支持者',
    activationRate: '激活率',
    currentVersion: '当前发布版本',
    version: '版本号',
    buildSeq: '构建序号',
    gitRepo: '开源仓库',
    artifacts: '已发布产物',
    platform: '平台',
    file: '文件名',
    size: '大小',
    path: '存储路径',
    publishHistory: '发版记录',
    status: '状态',
    artifactCount: '产物数',
    tests: '测试',
    createdAt: '时间',
    summary: '摘要',
    storageHint: '离线包按版本号存放于 245:/var/www/download/llm-gateway-go/v{version}/，发版前自动跑 distribution 测试并写入 download_publish_runs 审计表。',
    openPublicDownload: '打开公开下载页',
    openActivate: '打开激活向导',
  },

  blocklist: {
    title: 'IP 黑名单',
    loadFailed: '加载黑名单失败',
    add: '添加封禁',
    ip: 'IP/CIDR',
    ipPlaceholder: '例: 203.0.113.10 或 10.0.0.0/8',
    ipRequired: '请输入 IP 或 CIDR',
    reason: '原因',
    scope: '作用域',
    hits: '命中次数',
    total: '共 {n} 条',
    createSuccess: '已添加',
    createFailed: '添加失败',
    updateFailed: '更新失败',
    deleteConfirm: '确认删除 {ip}？',
    deleteSuccess: '已删除',
    reloadCache: '刷新 Redis 缓存',
    reloadSuccess: '缓存已刷新',
    reloadFailed: '缓存刷新失败',
  },
}