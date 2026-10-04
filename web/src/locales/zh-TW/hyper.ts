// hyper.ts —— Hyper 殼層文案（底部導覽 / 帳戶面板 / 清單狀態 / 專注 / 桌面專屬提示）。
// 2026-10-04 隨 Hyper 執行階段新增（docs/UI规范/00 §5.4，H2/H3）。
export default {
  bottomNav: {
    ariaLabel: '主導覽',
    home: '首頁',
    dashboard: '儀表板',
    requests: '請求',
    models: '模型',
    more: '更多',
  },
  account: {
    title: '帳戶',
    profile: '個人中心',
    language: '語言',
    theme: '主題',
    help: '說明',
    health: '服務健康',
    healthy: '可連線',
    unhealthy: '無法連線',
    unknown: '狀態未知',
    logout: '登出',
    adminEntry: '管理入口',
    close: '關閉',
  },
  dataView: {
    switchTo: '切換到{mode}',
    table: '表格',
    cards: '卡片',
  },
  list: {
    refreshing: '正在重新整理…',
    loadMore: '載入更多',
    loadingMore: '載入中…',
    retry: '重試',
    allLoaded: '已全部載入 {count} 筆',
    loadFailed: '載入失敗，點擊重試',
    empty: '尚無紀錄',
  },
  focus: {
    enter: '專注檢視',
    exit: '結束專注',
  },
  desktopOnly: {
    banner: '此頁面依桌面版設計。這裡可以閱讀，大量編輯請在桌面版完成。',
  },
}
