// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// login.ts — 登入彈窗 + 訪客頭部登入入口。
export default {
  title: '登入控制面',
  hint: '首次登入或管理員重設密碼後，需要先修改密碼才能繼續使用。',
  password: '密碼',
  submit: '登入',
  submitting: '登入中…',
  rememberMe: '保持登入',
  captcha: {
    label: '驗證碼',
    placeholder: '請輸入驗證碼',
    required: '請輸入驗證碼後再登入',
    refresh: '刷新驗證碼',
    loading: '載入中…',
    loadFailed: '驗證碼載入失敗',
    imageAlt: '驗證碼圖片',
    hint: '多次登入失敗後需要輸入驗證碼',
  },
  error: {
    required: '請輸入使用者名稱和密碼',
    failed: '沒能登入，請檢查使用者名稱和密碼後重試',
    apiKeyUnsupported: '此帳號不再支援 API key 登入，請使用使用者名稱/密碼登入。如需協助請聯絡管理員。',
    badResponse: '登入回應格式異常，請重試或聯絡管理員。',
  },
  checking: '正在偵測登入狀態…',
}
