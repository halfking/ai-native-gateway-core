// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// login.ts — ログインダイアログ + 訪問者ヘッダーのログインエントリ。
export default {
  title: 'コントロールプレーンにログイン',
  hint: '初回ログイン、または管理者がパスワードをリセットした後は、先にパスワードを変更してから利用してください。',
  password: 'パスワード',
  submit: 'ログイン',
  submitting: 'ログイン中…',
  rememberMe: 'ログイン状態を保持',
  captcha: {
    label: '認証コード',
    placeholder: '認証コードを入力',
    required: 'ログインには認証コードの入力が必要です',
    refresh: 'コードを更新',
    loading: '読み込み中…',
    loadFailed: '認証コードの読み込みに失敗しました',
    imageAlt: '認証コード画像',
    hint: 'ログインに複数回失敗すると認証コードが必要になります',
  },
  error: {
    required: 'ユーザー名とパスワードを入力してください',
    failed: 'ログインできませんでした。ユーザー名とパスワードを確認して再試行してください。',
    apiKeyUnsupported: 'このアカウントは API key ログインに対応していません。ユーザー名/パスワードでログインしてください。ご不明な場合は管理者までお問い合わせください。',
    badResponse: 'ログイン応答の形式が不正です。再試行するか、管理者までお問い合わせください。',
  },
  checking: 'ログイン状態を確認中…',
}
