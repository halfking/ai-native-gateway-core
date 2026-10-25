// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// login.ts — 登入彈窗 + 訪客頭部登入入口。
export default {
  title: '登入控制面',
  subtitle: 'Kaixuan MaaS 管理後台', // "開軒" 品牌名跨語種保持不譯
  hint: '首次登入或管理員重設密碼後，需要先修改密碼才能繼續使用。',
  username: '使用者名稱',
  usernamePlaceholder: 'admin',
  password: '密碼',
  passwordPlaceholder: '••••••••',
  submit: '登入',
  submitting: '登入中…',
  cancel: '取消',
  close: '關閉',
  signIn: '登入',
  error: {
    required: '請輸入使用者名稱和密碼',
    failed: '登入失敗',
    apiKeyUnsupported: '此帳號不再支援 API key 登入，請使用使用者名稱/密碼登入。如需協助請聯絡管理員。',
    badResponse: '登入回應格式異常，請重試或聯絡管理員。',
  },
  changePassword: '修改密碼',
  passwordChangeSuccess: '密碼修改成功',
  passwordChangedReloading: '密碼已更新，正在登出並重新登入…',
  passwordChangedReLogin: '密碼已更新，請使用新密碼重新登入。',
  checking: '正在偵測登入狀態…',
}
