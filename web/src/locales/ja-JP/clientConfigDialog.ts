// Japanese locale for ClientConfigDialog
export default {
  title: '{tool} 設定ジェネレーター',
  close: '閉じる',

  step1: {
    title: "① 利用可能な API キーを選択",
    refresh: '更新',
    refreshing: '更新中…',
    loading: '読み込み中…',
    empty: {
      title: 'API Key がありません',
      description: '現在のテナント配下に利用可能な API Key がありません。管理者は下のボタンで新しいキーを申請できます。',
      applyButton: '新しいキーを申請',
    },
    selected: '選択済み：',
  },

  step2: {
    title: '② オペレーティングシステム',
    pathHint: "設定場所：",
  },

  step3: {
    title: "④ モデルスコープを選択",
    featured: "注目モデル（選択したキーのテナントのルーティング設定）",
    all: 'すべての利用可能なモデル（ゲートウェイから取得）',
    featuredPreview: {
      empty: "選択したテナントに注目モデルが設定されていません",
      manageButton: '「ルーティングポリシー」に移動して設定 →',
      manage: "設定管理 →",
    },
    allModels: {
      selectAll: 'すべて選択',
      deselectAll: 'クリア',
      selected: '選択済み',
      filterHint: '（{count} 件が検索に一致）',
      searchPlaceholder: '🔍 モデル名 / ベンダー / family を検索…',
      noMatch: '一致するモデルがありません',
    },
  },

  retry: "再試行",
  otherFamily: "その他",
  gateway: {
    title: "③ ゲートウェイ API URL",
    hint: "既定では現在のゲートウェイのアドレスを使用します。生成したファイルとスクリプトには選択した API キーが含まれるため、安全に保管してください。",
  },
  provider: {
    label: "プロバイダー名",
    hint: "名前と URL が同じ項目は更新されます。URL が異なる場合、スクリプトは停止して名前の変更を求めます。",
  },
  notes: {
    cherryStudio: "クライアントの設定画面に選択したキーを入力してください。このファイルはパラメーター一覧です。",
    rooCode: "Roo Code の設定画面で OpenAI Compatible を設定してください。",
  },

  footer: {
    generated: '{count} 個のモデル設定を生成しました',
    generate: '設定を生成',
    generating: '生成中…',
    regenerate: '再生成',
  },

  results: {
    tabs: {
      yaml: "YAML スニペット",
      file: '設定ファイル',
      script: '設定スクリプト',
      manual: '手動手順',
    },
    actions: {
      scriptWarning: "スクリプトにはキーが含まれます。ダウンロードしてローカルで実行してください。内容はこのページには表示されません。",
      mergeHint: "スクリプトは既存の設定をバックアップし、{provider} をマージして他のプロバイダーを保持します",
      copyContent: 'コンテンツをコピー',
      downloadFile: 'ファイルをダウンロード',
      downloadScript: 'スクリプトをダウンロード',
    },
  },

  applyDialog: {
    title: '新しい API Key を申請',
    applicationCode: 'アプリケーションコード (application_code)',
    applicationCodePlaceholder: '例: default-app',
    description: '説明 (description)',
    descriptionPlaceholder: 'オプション: 理由 / 目的',
    cancel: 'キャンセル',
    submit: '申請を送信',
    submitting: '送信中…',
  },

  error: {
    invalidGatewayURL: "認証情報、クエリパラメーター、フラグメントを含まない有効な HTTP(S) API URL を入力してください。",
    invalidProviderName: "プロバイダー名は英字（A–Z）、数字、アンダースコア、ハイフンの1～64文字で、先頭は英字または数字にしてください。",
    featuredLoadFailed: "注目モデルの読み込みに失敗しました。再試行してください。",
    catalogLoadFailed: "モデル一覧の読み込みに失敗しました。再試行してください。",
    selectModel: "モデルを一つ以上選択してください。注目モデルがない場合は、先にルーティングポリシーで設定してください。",
    keyUnavailable: "選択したキーを読み取れません。選び直してください。",
    generateFailed: "設定の生成に失敗しました。再試行してください。",
    copyFailed: "コピーに失敗しました。ファイルをダウンロードしてください。",
    applyFailed: '申請に失敗しました',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor はファイル書き込みをサポートしていないため、Settings UI で手動設定してください',
}
