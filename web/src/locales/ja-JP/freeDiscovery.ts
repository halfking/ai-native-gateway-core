// freeDiscovery.ts — free discovery page copy (ja-JP).
export default {
  page: {
      title: "無料リソース探索",
      desc: "プロバイダーテンプレートを設定 → 上流モデル一覧をワンクリック走査 → 人工レビュー → 無料リソースプールへ一括インポート（OmniFreequota 追跡と連携）。"
    },
  common: {
      refresh: "更新",
      loading: "処理中…",
      empty: "データなし",
      actions: "操作",
      enabled: "有効",
      disabled: "無効",
      hideForm: "閉じる"
    },
  tabs: {
      templates: "テンプレート管理",
      tasks: "タスクとレビュー",
      history: "インポート履歴"
    },
  presets: {
      title: "内蔵プロバイダープリセット",
      create: "ワンクリック作成",
      exists: "作成済み",
      createDone: "プリセットからテンプレートを作成しました：{name}",
      scannerPending: "このプロトコルのスキャナーは対応中のため、現在スキャンは失敗します",
      keyless: "keyless（キー不要）"
    },
  form: {
      show: "+ カスタムテンプレート",
      providerCode: "Provider Code *",
      displayName: "表示名",
      baseUrl: "Base URL *",
      apiType: "API プロトコル",
      apiKeyEnv: "API Key 環境変数",
      apiKeyEnvHint: "キー自体は保存されません：$VAR 形式の環境変数参照（例：$GROQ_API_KEY）を入力。空欄は keyless。",
      tosVerdict: "ToS 判定",
      submit: "テンプレート作成",
      created: "テンプレートを作成しました"
    },
  orbi: {
      show: "Orbi テンプレート JSON をインポート",
      hint: "Orbi pi-providers テンプレートファイルの内容（トップレベルに providers キーを持つ JSON）を貼り付け。1 ファイルに複数 provider を含められます。",
      import: "インポート",
      invalidJson: "JSON の解析に失敗しました。形式を確認してください",
      done: "インポート完了：成功 {created} 件、失敗 {failed} 件"
    },
  tpl: {
      listTitle: "テンプレート一覧（{n}）",
      name: "テンプレート",
      baseUrl: "Base URL",
      apiType: "API プロトコル",
      keyEnv: "Key 環境変数",
      tos: "ToS",
      enabled: "有効",
      createdAt: "作成日時",
      scan: "スキャン",
      delete: "削除",
      deleteConfirm: "テンプレート「{name}」を削除しますか？既存の探索タスクと結果には影響しません。",
      deleted: "テンプレートを削除しました：{name}"
    },
  scan: {
      title: "探索を実行",
      pickTemplate: "有効なテンプレートを選択…",
      start: "スキャン開始",
      running: "スキャン中…",
      done: "スキャン完了：{n} 件のモデルを発見。結果をレビューしてください",
      failed: "スキャン失敗"
    },
  task: {
      listTitle: "探索タスク（{n}）",
      provider: "プロバイダー",
      status: "状態",
      trigger: "トリガー",
      found: "発見数",
      imported: "インポート済み",
      by: "実行者",
      time: "作成日時",
      error: "エラー",
      review: "結果をレビュー"
    },
  res: {
      title: "結果 · タスク {id} · {provider}",
      pending: "レビュー待ち",
      all: "すべて",
      model: "モデル ID",
      displayName: "表示名",
      freeType: "無料タイプ",
      monthly: "月間 quota (tokens)",
      daily: "日次 quota (tokens)",
      tos: "ToS",
      importStatus: "インポート状態",
      none: "この絞り込み条件で結果はありません",
      policy: "競合ポリシー",
      policySkip: "skip：既存行を保持",
      policyOverwrite: "overwrite：上書きして再有効化",
      policyMerge: "merge：空欄のみ補完",
      importSelected: "選択をインポート（{n}）",
      importAllPending: "レビュー待ちをすべてインポート",
      importedToast: "インポート完了：成功 {imported}、スキップ {skipped}、競合 {conflicted}、失敗 {failed}"
    },
  status: {
      taskPending: "待機",
      running: "実行中",
      success: "成功",
      failed: "失敗",
      review: "レビュー待ち",
      imported: "インポート済み",
      skipped: "スキップ",
      conflict: "競合"
    },
  trigger: {
      manual: "手動",
      scheduled: "定時",
      webhook: "Webhook"
    },
  hist: {
      title: "インポート履歴（{n}）",
      desc: "実際にインポートが発生したタスク（インポート数 > 0）を表示。詳細をクリックすると各結果の最終状態を確認できます。",
      completed: "完了日時",
      detail: "詳細",
      none: "インポート記録はまだありません。タスクとレビュー画面で一括インポートするとここに表示されます。",
      detailTitle: "インポート詳細 · タスク {id} · {provider}",
      importedAt: "インポート日時"
    },
}
