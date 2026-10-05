// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// app.ts — App.vue 外殼框架文案（頂欄角色、側欄折疊、退出、語言切換）。
export default {
  brand: 'AI-Native 組織網關',
  role: {
    super_admin: '超級管理員',
    tenant_admin: '租戶管理員',
  },
  sidebar: {
    expand: '展開側欄',
    collapse: '收起側欄',
    collapseMenu: '收起選單',
  },
  logout: '登出',
  lang: {
    switch: '切換語言',
    label: '語言',
  },
  // 2026-07-22: 公開殼頁面底部文案（對齊 LifecycleShell）。
  footer: {
    left: '© 2026 AI-Native 組織閘道',
    right: '需要協助嗎？',
    feedbackLink: '提交意見',
  },
  // 2026-07-22: 使用者選單下拉（UserMenuDropdown）+ 使用者資訊對話框（UserInfoDialog）。
  userMenu: {
    profile: '個人資訊',
    changePassword: '修改密碼',
    logout: '退出登入',
  },
  userInfo: {
    displayName: '顯示名稱',
    username: '使用者名稱',
    email: '電子郵件',
    role: '角色',
    tenant: '租戶',
    close: '關閉',
  },
  // 2026-07-22: skip-link 無障礙文案。
  nav: {
    mainAria: '主導航',
    skip: '跳到主要內容',
  },

  theme: {
    switchToLight: '切到浅色',
    switchToDark: '切到深色',
    lightTitle: '浅色模式',
    darkTitle: '深色模式',
  },
  v1DataFrozen: {
    title: '流量資料已停更：本頁數字只反映停寫之前',
    titleUnconfirmed: '停寫開關未明確設定：無法判斷本頁數字是否已過期',
    titleUnavailable: '無法確認流量資料狀態：本頁數字可能已過期',
    affects: '受影響讀點檔位',
    gateKey: '控制開關',
    retry: '重新檢查',
    failedHint: '讀取資料狀態失敗。未確認狀態不等於狀態正常——本頁數字請按「可能已過期」對待。',
  },
  deploySeq: {
    bannerTitle: '發現新版本',
    bannerHint: '頁面已有更新，建議立即切換（重新載入頁面即完成升級）',
    updateNow: '立即更新',
    later: '稍後',
    currentSeq: '頁面版本序號',
    state: '更新狀態',
    stateLatest: '已是最新',
    stateAvailable: '有新版本',
    stateIndeterminate: '不可判定',
    stateChecking: '正在檢查…',
    reasonNoLocal: '本頁未攜帶部署序號（構建期未注入）',
    reasonNoRemote: '服務端未返回部署序號（舊構建或非本倉託管）',
    reasonNetwork: '檢查請求失敗，無法判定',
    checks: '已檢查次數',
    checkNow: '檢查更新',
    sourceUnknown: '未注入',
  },
}
