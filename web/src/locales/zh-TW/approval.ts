// Auto-synced from en-US (zh-TW)
export default {
  list: {
    title: 'Approval requests',
    description: 'Manage and process approval requests',
    refresh: 'Refresh',
    refreshing: 'Refreshing…',
    config: 'Approval settings',
    stats: {
      pending: 'Pending',
      todayTotal: 'New today',
      approved: 'Approved',
      rejected: 'Rejected',
      avgTime: 'Avg. processing time',
    },
    filter: {
      status: 'Status',
      riskLevel: 'Risk level',
      search: 'Search',
      searchPlaceholder: 'Search session ID or request ID…',
      reset: 'Reset',
      allStatus: 'All statuses',
      allRisk: 'All risk levels',
    },
    status: {
      pending: 'Pending',
      approved: 'Approved',
      rejected: 'Rejected',
      timeout: 'Timed out',
    },
    risk: {
      LOW: 'Low',
      MEDIUM: 'Medium',
      HIGH: 'High',
      CRITICAL: 'Critical',
    },
    table: {
      requestId: 'Request ID',
      sessionId: 'Session ID',
      riskLevel: 'Risk level',
      trigger: 'Trigger',
      cost: 'Est. cost',
      createdAt: 'Created',
      status: 'Status',
      actions: 'Actions',
    },
    timeLeft: 'Time left: {time}',
    actions: {
      approve: 'Approve',
      reject: 'Reject',
      viewDetail: 'View details',
    },
    empty: 'No approval requests',
    loading: 'Loading…',
    pagination: {
      previous: 'Previous',
      next: 'Next',
      info: 'Page {page} / {totalPages} ({total} total)',
    },
    errors: {
      loadFailed: 'Failed to load',
      approveFailed: 'Approve failed',
      rejectFailed: 'Reject failed',

      loadListFailed: '加载审批列表失败',
    },
    success: {
      approved: 'Approved',
      rejected: 'Rejected',
    },

    relativeTime: {
      justNow: '刚刚',
      minutesAgo: '{n} 分钟前',
      hoursAgo: '{n} 小时前',
      daysAgo: '{n} 天前',
    },

    confirm: {
      approve: '确认批准此请求？\n请求 ID: {id}',
      rejectPrompt: '请输入拒绝原因：',
    },
  },
  detail: {
    title: 'Approval request detail',
    back: 'Back to list',
    sections: {
      basic: 'Basic info',
      sensitive: 'Sensitive data detection',
      actions: 'Approval actions',
      history: 'Approval history',
    },
  },
  config: {
    title: '審批設定',
    sections: {
      basic: '基本設定',
      approvers: '審批人管理',
      channels: '通知渠道',
      rules: '審批規則',
    },
    save: '儲存設定',
    saving: '儲存中…',
    loading: '載入中…',
    description: '設定審批流程、審批人與通知渠道',
    enabled: {
      label: '啟用審批流程',
      hint: '開啟後，符合規則的請求將進入審批流程',
      on: '已啟用',
      off: '已停用',
    },
    mode: {
      label: '審批模式',
      hint: '選擇審批的工作模式',
      disabled: '停用',
      disabledDesc: '完全關閉審批功能',
      automatic: '自動審批',
      automaticDesc: '根據規則自動處理',
      manual: '人工審批',
      manualDesc: '需要審批人手動審批',
    },
    timeout: {
      label: '審批逾時時間',
      hint: '目前設定: {value}',
      suffix: '秒',
    },
    timeoutAction: {
      label: '逾時後行為',
      hint: '審批逾時後的處理方式',
      approve: '自動通過',
      approveDesc: '逾時後自動批准請求',
      reject: '自動拒絕',
      rejectDesc: '逾時後自動拒絕請求',
    },
    sectionsDesc: {
      approvers: '設定審批人員及其優先順序',
      channels: '設定審批通知的發送渠道',
      rules: '定義哪些請求需要審批',
    },
    errors: {
      loadFailed: '載入設定失敗',
      saveFailed: '儲存失敗',
    },
    success: {
      saved: '儲存成功',
    },
    format: {
      seconds: '{n} 秒',
      minutes: '{n} 分鐘',
      hours: '{n} 小時',
    },
  }
}
