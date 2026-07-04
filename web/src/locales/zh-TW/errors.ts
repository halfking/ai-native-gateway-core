// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// errors.ts — API 錯誤文案。優先按後端穩定的 ApiError.code 翻譯（errors.code.<code>），
// 無 code 時回退到 ApiError.message（後端中文兜底）。code 列表隨介面演進逐步補充。
export default {
  code: {
    unauthorized: '未授權，請重新登入',
    forbidden: '無權存取',
    not_found: '資源不存在',
    rate_limited: '請求過於頻繁，請稍後重試',
    internal: '伺服器內部錯誤',
  },
  byStatus: {
    400: '請求參數有誤',
    401: '未授權，請重新登入',
    403: '無權存取',
    404: '資源不存在',
    409: '操作衝突',
    429: '請求過於頻繁，請稍後重試',
    500: '伺服器內部錯誤',
    502: '閘道錯誤',
    503: '服務暫時無法使用',
    504: '閘道逾時',
  },
  network: '網路錯誤，請檢查連線',
  unknown: '發生未知錯誤',
}
