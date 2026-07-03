export default {
  // 一般
  login: 'ログイン',
  logout: 'ログアウト',
  changePassword: 'パスワード変更',
  cancel: 'キャンセル',
  confirm: '確認',
  save: '保存',
  delete: '削除',
  edit: '編集',
  search: '検索',
  reset: 'リセット',
  submit: '送信',
  back: '戻る',
  next: '次へ',
  previous: '前へ',
  close: '閉じる',
  
  // ユーザーロール
  role: {
    super_admin: 'スーパー管理者',
    tenant_admin: 'テナント管理者',
  },
  
  // ナビゲーション
  nav: {
    collapseSidebar: 'メニューを折りたたむ',
    expandSidebar: 'サイドバーを展開',
  },
  
  // パスワード
  password: {
    changeSuccess: 'パスワードが正常に変更されました',
    changeFailed: 'パスワードの変更に失敗しました',
  },
  
  // バージョン情報
  version: 'バージョン',
  build: 'ビルド',

  // 2026-07-02 (request-logs 添付ファイル表示): 添付ファイル関連文言、
  // 参考ドキュメント §6 に整合。
  requests: {
    list: {
      table: {
        attachmentsTitle: '添付ファイル',
        attachmentCountTitle: '添付ファイル {n} 件',
        noAttachments: '添付ファイルなし',
      },
    },
    detail_extra: {
      attachmentsTab: '添付ファイル',
      attachmentsLoading: '添付ファイルを読み込み中…',
      noAttachments: '添付ファイルなし',
      clickToPreviewTitle: 'クリックで拡大表示',
      download: 'ダウンロード',
      downloadOriginal: '原画像をダウンロード',
      closePreview: '閉じる',
    },
  },
}