// Japanese locale for ClientConfigDialog
export default {
  title: '{tool} 設定ジェネレーター',
  close: '閉じる',
  
  step1: {
    title: '① API Key を選択（現在のテナント配下のすべてのキー）',
    refresh: '更新',
    refreshing: '更新中…',
    loading: '読み込み中…',
    empty: {
      title: 'API Key がありません',
      description: '現在のテナント配下に利用可能な API Key がありません。管理者は下のボタンをクリックして新しいキーを申請できます。',
      applyButton: '新しいキーを申請',
    },
    selected: '選択済み：',
  },
  
  step2: {
    title: '② オペレーティングシステム',
    pathHint: '設定ファイルパス：',
  },
  
  step3: {
    title: '③ モデルスコープを選択',
    featured: '注目モデル（ルーティング featured 設定）',
    all: 'すべての利用可能なモデル（ゲートウェイから取得）',
    featuredPreview: {
      loading: '読み込み中…',
      empty: 'ルーティングに注目モデルが設定されていません',
      manageButton: '「ルーティングポリシー」に移動して設定 →',
      manage: '管理 →',
    },
    allModels: {
      selectAll: 'すべて選択',
      deselectAll: 'クリア',
      selected: '選択済み',
      filterHint: '（{count} 件が検索に一致）',
      searchPlaceholder: '🔍 モデル名 / ベンダー / family を検索…',
      loading: '読み込み中…',
      noMatch: '一致するモデルがありません',
    },
  },
  
  footer: {
    generated: '{count} 個のモデル設定を生成しました',
    generate: '設定を生成',
    generating: '生成中…',
    regenerate: '再生成',
  },
  
  results: {
    tabs: {
      file: '設定ファイル',
      script: '設定スクリプト',
      manual: '手動手順',
    },
    actions: {
      copyContent: 'コンテンツをコピー',
      downloadFile: 'ファイルをダウンロード',
      downloadScript: 'スクリプトをダウンロード',
      scriptHint: 'スクリプトは古い設定ファイルを自動的にバックアップします',
    },
  },
  
  applyDialog: {
    title: '新しい API Key を申請',
    close: '閉じる',
    applicationCode: 'アプリケーションコード (application_code)',
    applicationCodePlaceholder: '例: default-app',
    description: '説明 (description)',
    descriptionPlaceholder: 'オプション: 理由 / 目的',
    cancel: 'キャンセル',
    submit: '申請を送信',
    submitting: '送信中…',
  },
  
  error: {
    applyFailed: '申請に失敗しました',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor はファイル書き込みをサポートしていないため、Settings UI で手動設定してください',
}
