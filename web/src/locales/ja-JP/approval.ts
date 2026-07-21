// Auto-synced from en-US (ja-JP)
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
    title: '承認設定',
    sections: {
      basic: '基本設定',
      approvers: '承認者',
      channels: '通知チャネル',
      rules: '承認ルール',
    },
    save: '設定を保存',
    saving: '保存中…',
    loading: '読み込み中…',
    description: '承認フロー、承認者、通知チャネルを設定',
    enabled: {
      label: '承認フローを有効化',
      hint: '条件に合うリクエストは承認キューに入ります',
      on: '有効',
      off: '無効',
    },
    mode: {
      label: '承認モード',
      hint: '承認の処理方法',
      disabled: '無効',
      disabledDesc: '承認を完全にオフ',
      automatic: '自動',
      automaticDesc: 'ルールで自動処理',
      manual: '手動',
      manualDesc: '人手による承認が必要',
    },
    timeout: {
      label: '承認タイムアウト',
      hint: '現在: {value}',
      suffix: '秒',
    },
    timeoutAction: {
      label: 'タイムアウト時',
      hint: '期限切れ時の動作',
      approve: '自動承認',
      approveDesc: 'リクエストを自動承認',
      reject: '自動拒否',
      rejectDesc: 'リクエストを自動拒否',
    },
    sectionsDesc: {
      approvers: '承認者と優先度を設定',
      channels: '通知チャネルを設定',
      rules: '承認が必要なリクエストを定義',
    },
    errors: {
      loadFailed: '設定の読み込みに失敗',
      saveFailed: '保存に失敗',
    },
    success: {
      saved: '保存しました',
    },
    format: {
      seconds: '{n} 秒',
      minutes: '{n} 分',
      hours: '{n} 時間',
    },
  }
}
