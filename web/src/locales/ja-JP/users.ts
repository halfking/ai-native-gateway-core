// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// users.ts — ユーザー管理ページの文案。common モジュールの汎用語（有効/無効、キャンセル、確認、作成、削除など）を再利用します。
export default {
  title: 'ユーザー管理',
  create: 'ユーザー作成',
  readOnlyNotice: '📖 あなたはテナント管理者で、現在は読み取り専用モードです。ユーザー管理は表示のみで、作成、編集、削除はできません。',
  filter: {
    byTenant: 'テナントでフィルター:',
    allTenants: 'すべてのテナント',
  },
  table: {
    username: 'ユーザー名',
    displayName: '表示名',
    email: 'メール',
    tenant: 'テナント',
    role: '役割',
    lastLogin: '最終ログイン',
  },
  action: {
    resetPassword: 'パスワードリセット',
  },
  role: {
    super_admin: 'スーパー管理者',
    tenant_admin: 'テナント管理者',
  },
  modal: {
    create: {
      title: 'ユーザー作成',
      username: 'ユーザー名 *',
      password: 'パスワード *',
      passwordPlaceholder: '8文字以上',
      displayName: '表示名',
      email: 'メール',
      tenant: 'テナント *',
      role: '役割',
    },
    reset: {
      title: 'パスワードリセット — {name}',
      newPassword: '新しいパスワード',
      passwordPlaceholder: '8文字以上',
    },
  },
  error: {
    usernamePasswordRequired: 'ユーザー名とパスワードは必須です',
    createFailed: '作成失敗',
    resetFailed: 'リセット失敗',
    passwordMinLength: 'パスワードは8文字以上必要です',
  },
  confirmDelete: 'ユーザー {name} を削除しますか？',
}
