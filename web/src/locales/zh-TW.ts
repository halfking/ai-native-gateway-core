export default {
  // 通用
  login: '登入',
  logout: '登出',
  changePassword: '修改密碼',
  cancel: '取消',
  confirm: '確認',
  save: '儲存',
  delete: '刪除',
  edit: '編輯',
  search: '搜尋',
  reset: '重設',
  submit: '提交',
  back: '返回',
  next: '下一步',
  previous: '上一步',
  close: '關閉',
  
  // 使用者角色
  role: {
    super_admin: '超級管理員',
    tenant_admin: '租戶管理員',
  },
  
  // 導覽列
  nav: {
    collapseSidebar: '收起選單',
    expandSidebar: '展開側邊欄',
  },
  
  // 密碼
  password: {
    changeSuccess: '密碼修改成功',
    changeFailed: '密碼修改失敗',
  },
  
  // 版本資訊
  version: '版本',
  build: '構建',

  // 2026-07-02 (request-logs 附件展示): 附件相關文案，對齊參考文件 §6。
  requests: {
    list: {
      table: {
        attachmentsTitle: '附件',
        noAttachments: '無附件',
      },
    },
    detail_extra: {
      attachmentsTab: '附件',
      attachmentsLoading: '載入附件中…',
      noAttachments: '無附件',
      clickToPreviewTitle: '點擊查看大圖',
      download: '下載',
      downloadOriginal: '下載原圖',
      closePreview: '關閉',
    },
  },

  // 2026-07-03 (即時請求流): 儀表板 swim lane 文案。
  dashboard: {
    liveStream: {
      title: '即時請求流',
      connected: '已連線',
      connecting: '連線中…',
      reconnecting: '重新連線中…',
      disconnected: '已斷線',
      unsupported: '瀏覽器不支援',
      pause: '暫停',
      resume: '繼續',
      filterAll: '全部狀態',
      filterSuccess: '僅成功',
      filterInProgress: '進行中',
      filterGroupFailures: '失敗分類',
      filterFailure5xx: '伺服器 / 上游 (5xx)',
      filterFailure4xx: '用戶端 / 鑑權 (4xx)',
      filterFailureTimeout: '逾時 / 網路',
      filterFailureNotFound: '路由 / 模型未找到',
      filterFailureOther: '其它失敗',
      empty: '等待請求進入…',
      countTooltip: '緩衝區內 {buffer} / 螢幕可見 {visible}',
      countAria: '緩衝區內 {buffer} 個請求，螢幕可見 {visible}',
      legend: {
        model: '模型族',
        status: '狀態',
        openai: 'OpenAI',
        anthropic: 'Anthropic',
        domestic: '國產',
        oss: '開源',
        other: '其他',
        success: '成功',
        inProgress: '進行中',
        failure: '失敗',
      },
    },
  },
}