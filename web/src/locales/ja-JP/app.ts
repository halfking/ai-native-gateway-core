// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// app.ts — App.vue 外壳框架文案（トップバーの役割、サイドバーの折りたたみ、ログアウト、言語切替）。
export default {
  brand: 'AI-Native Org Gateway',
  role: {
    super_admin: 'スーパー管理者',
    tenant_admin: 'テナント管理者',
  },
  sidebar: {
    expand: 'サイドバーを展開',
    collapse: 'サイドバーを折りたたむ',
    collapseMenu: 'メニューを折りたたむ',
  },
  logout: 'ログアウト',
  lang: {
    switch: '言語を切り替え',
    label: '言語',
  },
  // 2026-07-22: 公開シェルフッターの文言（LifecycleShell と連動）。
  footer: {
    left: '© 2026 AI-Native ゲートウェイ',
    right: 'ヘルプが必要ですか？',
    feedbackLink: 'フィードバックを送信',
  },
  // 2026-07-22: ユーザーメニュー（UserMenuDropdown）+ プロフィールダイアログ（UserInfoDialog）。
  userMenu: {
    profile: 'プロフィール',
    changePassword: 'パスワードを変更',
    logout: 'サインアウト',
  },
  userInfo: {
    displayName: '表示名',
    username: 'ユーザー名',
    email: 'メールアドレス',
    role: '役割',
    tenant: 'テナント',
    close: '閉じる',
  },
  // 2026-07-22: スキップリンク（a11y）用文言。
  nav: {
    mainAria: 'メインナビゲーション',
    skip: 'メインコンテンツへスキップ',
  },

  theme: {
    switchToLight: '切到浅色',
    switchToDark: '切到深色',
    lightTitle: '浅色模式',
    darkTitle: '深色模式',
  },
}
