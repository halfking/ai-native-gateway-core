// credentialMonitor.ts — 凭据监控页(CredentialMonitorView)文案。
// 命名空间：page / filter / summary / table / drawer / dialog / status / action / error / chart。
// 后端枚举字符串（ready/degraded/healthy/warning/broken 等）保持原样 — 翻译会破坏 API 调用。
// 复用 common/keys 已有的按钮/状态词汇；本页特有术语集中在本文件。
export default {
  page: {
    title: '凭据监控',
    autoRefresh: '自动刷新',
    refreshInterval: { s10: '10秒', s30: '30秒', s60: '60秒', s5: '5秒' },
    manualRefresh: '手动刷新',
  },
  filter: {
    availability: '可用性',
    health: '健康',
    all: '全部', // 重导出 key；与 common.table.all 重复确认
    quickNone: '全部',
    quickBroken: '只看 broken',
    quickLowRate: '成功率<50%',
    batchRestore: '批量恢复 ({n})',
    batchDemote: '批量降级 ({n})',
  },
  summary: {
    total: '总凭据',
    ready: '可用 (ready)',
    abnormal: '异常',
    unreachable: 'unreachable/cooling/rate_limited', // 纯枚举
    brokenModels: 'broken 模型',
    brokenModelsHint: 'probe 确认坏掉',
  },
  table: {
    header: {
      credential: '凭据',
      provider: '供应商',
      availability: '可用性',
      health: '健康',
      models: '模型 (可用/总数)',
      recentSuccessRate: '最近成功率',
      brokenModels: 'broken 模型',
      concurrency: '并发',
    },
    cell: {
      idPrefix: 'ID: ',
      manualPrefix: '手动: ',
      effectivePrefix: '生效: ',
    },
    loading: '加载中...', // 与 common.feedback.loading 同义
    empty: '暂无凭据',
  },
  drawer: {
    close: '关闭',
    refreshTooltip: '刷新详情',
    credentialIdPrefix: '凭据 ID: ',
    action: {
      clearManualDisabledTitle: '解除整凭据禁用',
      clearManualDisabled: '🔓 解除禁用',
      setManualDisabledTitle: '手动禁用此凭据 (路由时将不被选中)',
      setManualDisabled: '⛔ 手动禁用',
    },
    tab: {
      overview: '基础信息',
      models: '模型',
      requests: '请求数据',
    },
    overview: {
      sectionTitle: '基础信息 · 凭据统计',
      fields: {
        availability: '可用性',
        health: '健康',
        quota: '配额',
        consecutiveFailures: '连续失败',
        manualDisabled: 'manual_disabled', // 纯枚举
        yes: 'YES',
        no: 'NO',
        totalRequests: '总请求数',
        totalModels: '模型总数',
        availableModels: '可用模型数',
        aggregateSuccessRate: '聚合成功率',
        brokenModelCount: 'broken 模型数',
      },
    },
    concurrency: {
      sectionTitle: '并发限流',
      manual: '手动',
      auto: '自动',
      effective: '生效',
      notSet: '未设置',
      adjustAuto: '调整自动值',
      tempDemote: '临时降级',
      restore: '恢复上线',
    },
    modelsTab: {
      listTitle: '模型列表 ({n})',
      clickHint: '点击查看详情',
      empty: '无模型',
      sampleUnit: '样本',
      neverCalled: '未调用',
      unavail: 'unavail',
      // 手工状态图标 label + tooltip
      manualDisabledTitle: '当前为手工禁用状态，点击解除',
      manualDisabledNot: '手工禁用此模型',
      manualDisabled: '手工禁用',
      manualOnlineTitle: '当前为手工启动（offline）状态，点击切换为自动',
      manualOnlineNot: '将此模型手工设为 offline（自动探测将跳过）',
      manualOnline: '手工启动',
      autoTitle: '由自动探测控制（不可手动切换到该状态）',
      auto: '自动',
      // 模型统计卡片
      stats: {
        p95: 'P95 延迟',
        msUnit: 'ms',
        recentSuccessRate: '最近成功率',
        sampleCount: '样本数',
        last24hCalls: '24h 调用次数',
      },
      // 滑动窗口
      slidingTitle: '滑动窗口 (最近 1 小时)',
      redisSource: 'Redis',
      requestLogsSource: 'request_logs',
      loading: '加载中...',
      noData: '无数据',
      refresh: '↻ 刷新',
      // 错误分布图
      errorsTitle: '错误分布',
      // 槽位卡片
      slotInfoTitle: '双层槽位信息',
      emptyHint: '请从左侧选择模型查看详情',
    },
    requestsTab: {
      historySectionTitle: '请求数据 · 模型状态变化历史',
      historyEmpty: '点击「模型」tab 中的模型查看',
      historyRefresh: '↻ 刷新',
      historyLoading: '加载中...',
      historyNoEvents: '无状态变化记录',
      historyColTime: '时间',
      historyColSource: '来源',
      historyColEvent: '事件',
      historyColDetail: '详情',
      autoPrefix: '自动 · ', // 后接 scheduler/admin 等技术字段
      manualPrefix: '手动 · ',
      routingSectionTitle: '请求数据 · 最近路由决策 ({n}条)',
      routingRefresh: '↻ 刷新',
      routingLoading: '加载中...',
      routingNoEvents: '无路由决策记录',
      routingColTime: '时间',
      routingColRequestId: '请求ID',
      routingColModel: '模型',
      routingColTier: 'Tier',
      routingColResult: '结果',
      routingColLatency: '延迟',
      routingColError: '错误',
      msUnit: 'ms',
    },
  },
  dialog: {
    cancel: '取消', // 与 common.button.cancel 同义
    // 批量恢复/降级
    batchTitle: {
      promote: '批量恢复',
      demote: '批量降级',
    },
    batchTitleSuffix: '个凭据)', // 接在批量标题后
    batchReasonLabel: '原因',
    batchReasonPlaceholder: '请输入原因',
    batchHoursLabel: '自动恢复时间 (小时)',
    batchSubmit: {
      promote: '确认恢复',
      demote: '确认降级',
    },
    // 单条降级
    demoteTitle: '临时降级',
    demoteReasonLabel: '降级原因',
    demoteReasonPlaceholder: '请输入原因',
    demoteHoursLabel: '自动恢复时间 (小时)',
    demoteSubmit: '确认降级',
    // 单条恢复
    promoteTitle: '恢复上线',
    promoteReasonLabel: '恢复原因',
    promoteReasonPlaceholder: '请输入原因',
    promoteSubmit: '确认恢复',
    // 并发
    concurrencyTitle: '手动调整并发自动值',
    concurrencyLimitLabel: '并发上限',
    concurrencyReasonLabel: '调整原因',
    concurrencyReasonPlaceholder: '请输入原因',
    concurrencySubmit: '确认',
    // 单条 toggle offline/online
    toggleTitle: {
      offline: '确认下线',
      online: '确认上线',
    },
    toggleCredentialPrefix: '凭据 #',
    toggleCredentialSep: ' - ',
    toggleNoLabel: '无标签',
    toggleOfflineBody: '下线后自动探测将不再触碰该模型（原因 = {code}），需你手动恢复。',
    toggleOnlineBody: '恢复后下一轮自动探测（~10 min）会重新评估。',
    toggleReasonLabel: '原因（必填）',
    toggleReasonPlaceholder: '例如: 误判 broken / 紧急封禁 / 灰度验证',
    toggleSubmit: {
      offline: '确认下线',
      online: '确认上线',
    },
    // 清除 manual_disabled
    clearTitle: '清除 manual_disabled',
    clearWarning: '⚠️ 此操作将立即恢复凭据到正常路由池，manual_disabled 标志将被清除。请确认此凭据已经可以正常使用。',
    clearReasonLabel: '操作原因（必填）',
    clearReasonPlaceholder: '例如: 供应商恢复正常 / 误操作修正 / 灰度验证完成',
    clearSubmit: '确认清除',
    // 设置 manual_disabled
    setTitle: {
      disable: '禁用凭据',
      enable: '启用凭据',
    },
    setDisableBody: '⚠️ 此操作将设置 manual_disabled = true，凭据将从路由池移除，不再处理任何流量，直到手动恢复。',
    setEnableBody: '✓ 此操作将设置 manual_disabled = false，凭据将恢复到正常路由池。',
    setReasonLabel: '操作原因（必填）',
    setReasonPlaceholderDisable: '例如: 供应商维护 / 配额耗尽 / 临时下线',
    setReasonPlaceholderEnable: '例如: 供应商恢复 / 维护完成 / 测试通过',
    setSubmit: {
      disable: '确认禁用',
      enable: '确认启用',
    },
  },
  chart: {
    allHealthy: '全部健康',
    errorsWhenHealthy: '错误类型分布 · 全部健康',
    errorsTitle: '错误类型分布',
    slidingStatsTotal: '总计: ',
    slidingStatsSuccess: '成功: ',
    slidingStatsFailed: '失败: ',
    slidingStatsFailureRate: '失败率: ',
  },
  error: {
    selectFirst: '请先选择凭据',
    clearFailed: '清除失败: ', // 后面接 e.message
    setManualDisabledFailed: '操作失败: ',
    batchFailed: '批量操作失败: ',
    demoteFailed: '降级失败: ',
    promoteFailed: '升级失败: ',
    concurrencyFailed: '设置失败: ',
    offlineFailed: '下线失败: ',
    onlineFailed: '上线失败: ',
  },
}