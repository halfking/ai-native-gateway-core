// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// users.ts — 使用者管理頁文案。複用 common 模組的通用詞（啟用/停用、取消、確認、建立、刪除等）。
export default {
  title: '使用者管理',
  create: '新增使用者',
  readOnlyNotice: '📖 您是租戶管理員，目前為唯讀模式。使用者管理僅限查看，不能建立、編輯或刪除使用者。',
  filter: {
    byTenant: '依租戶篩選：',
    allTenants: '全部租戶',
  },
  table: {
    username: '使用者名稱',
    displayName: '顯示名',
    email: '郵箱',
    tenant: '租戶',
    role: '角色',
    lastLogin: '最後登入',
  },
  action: {
    resetPassword: '重設密碼',
  },
  role: {
    super_admin: '超級管理員',
    tenant_admin: '租戶管理員',
  },
  modal: {
    create: {
      title: '新增使用者',
      username: '使用者名稱 *',
      password: '密碼 *',
      passwordPlaceholder: '至少8位',
      displayName: '顯示名',
      email: '郵箱',
      tenant: '租戶 *',
      role: '角色',
    },
    reset: {
      title: '重設密碼 — {name}',
      newPassword: '新密碼',
      passwordPlaceholder: '至少8位',
    },
  },
  error: {
    usernamePasswordRequired: '使用者名稱和密碼不能為空',
    createFailed: '建立失敗',
    resetFailed: '重設失敗',
    passwordMinLength: '密碼至少8個字元',
  },
  confirmDelete: '確認刪除使用者 {name}？',
}
