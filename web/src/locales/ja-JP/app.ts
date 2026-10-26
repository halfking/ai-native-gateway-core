// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// app.ts — App.vue 外壳框架文案（トップバーの役割、サイドバーの折りたたみ、ログアウト、言語切替）。
export default {
  brand: {
    siteTitle: 'AI Native 組織コアゲートウェイ',
    siteTitleLineTwo: '組織コアゲートウェイ',
    alt: 'Kaixuan ロゴ',
  },
  role: {
    super_admin: 'スーパー管理者',
    tenant_admin: 'テナント管理者',
  },
  // 2026-07-22: 公開シェルフッターの文言（LifecycleShell と連動）。
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

  theme: {
    switchToLight: "ライトモードへ切替",
    switchToDark: "ダークモードへ切替",
    lightTitle: "ライトモード",
    darkTitle: "ダークモード",
  },
  dataHorizon: {
    title: 'トラフィックデータの記録が遅れています：ゲートウェイはリクエストを処理していますが、セッション記録が追いついていません',
    titleUnavailable: 'トラフィックデータの状態を確認できません：このページの数値は古い可能性があります',
    hintLabel: '調査',
    retry: '再確認',
    failedHint: 'データ状態の取得に失敗しました。状態が未確認であることは正常を意味しません——このページの数値は古い可能性があると扱ってください。',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: '新しいビルドがデプロイされています。ページを再読み込みすると切り替わります。',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'このページにはデプロイ番号がありません（ビルド時に埋め込まれません）',
    reasonNoRemote: 'サーバーがデプロイ番号を返しませんでした（古いビルド、またはリポジトリ外のホスティング）',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
