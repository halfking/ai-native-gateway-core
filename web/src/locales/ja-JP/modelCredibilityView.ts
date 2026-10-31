// modelCredibilityView.ts — モデル信頼性（ダミー化検出）ページ (ja-JP)。
export default {
  pageTitle: 'モデルの信頼性',
  pageSubtitle:
    '上流が小さめのモデルを大きめのモデルとして販売していないかを検出します（ダミー化・差し替え・劣化）。' +
    '判定は 4 つの独立した測定にもとづきます：上流の自己申告、同名モデルとの能力比較、' +
    'リクエスト署名が安定しているか、外部ベースラインとの照合。' +
    '結論を出せない場合は「証拠不足」と表示し、「問題なし」とは扱いません。',

  stats: {
    total: '判定済みノード',
    verified: '検証済み',
    suspected: '要確認',
    diluted: 'ダミー化',
    unknown: '証拠不足',
    providers: '対象のプロバイダー',
  },

  filter: {
    verdict: '判定',
    verdictPlaceholder: 'すべての判定',
    model: 'モデル',
    modelPlaceholder: 'モデル名で絞り込む',
    query: '検索',
    refresh: '更新',
    scanAll: '全件スキャン',
    scanning: 'スキャン中…',
    sort: '並び替え',
    sortTime: '検出時刻順（新しい順）',
    sortName: 'モデル名順',
    sortScore: '信頼度順（低い順）',
  },

  verdict: {
    verified: '検証済み',
    suspected: '要確認',
    diluted: 'ダミー化',
    unknown: '証拠不足',
  },

  dimension: {
    identity: '自己申告の整合',
    capability: '能力水準',
    consistency: '署名の安定性',
    reference: '外部基準',
  },

  table: {
    provider: 'プロバイダー',
    credential: '認証情報',
    model: 'モデル',
    credibility: '信頼度',
    verdict: '判定',
    dimensions: '証拠の観点数',
    measuredAt: '測定時刻',
    action: '操作',
  },

  status: {
    success: '正常',
    partial: '一部',
    failed: 'プローブ失敗',
    inconclusive: '判定不能',
    unknown: '未測定',
  },

  anomaly: {
    identity_model_name_mismatch: '上流の自己申告モデルが要求したモデルと異なる',
    identity_family_mismatch: '上流の自己申告モデルが別ベンダーのモデルである',
    identity_reported_model_absent: '上流がモデル名を返さないため自己申告を確認できない',
    capability_below_peer_best: '同名モデルで最も高い水準より能力が低い',
    capability_severely_below_peer_best: '同名モデルで最も高い水準を大きく下回る能力',
    capability_above_peer_best: '同名モデルで最も高い水準より能力が高い',
    consistency_prompt_token_drift: 'リクエスト署名が変動し、後端が差し替えられた可能性がある',
    consistency_system_fingerprint_drift: '上流のフィンガープリントが変化した',
    reference_below_expected_accuracy: '外部基準が期待する水準を下回る',
    capability_no_measurement: '利用できる能力の測定値がない',
    capability_peer_sample_too_small: '同名モデルの比較対象ノードが不足している',
    identity_probe_failed: '同一性プローブのリクエストが失敗した',
    identity_probe_empty_response: '同一性プローブが応答を得られなかった',
  },

  detail: {
    title: '判定の詳細',
    evidence: '判定の根拠',
    anomalies: '異常項目',
    history: '判定の履歴',
    noHistory: '判定履歴はまだありません',
    requestedModel: '要求モデル',
    reportedModel: '上流の自己申告モデル',
    promptSignature: 'リクエスト署名（prompt token 数）',
    fingerprint: '上流のフィンガープリント',
    measuredDimensions: '今回データが取得できた観点数',
    close: '閉じる',
  },

  empty: {
    noData: '信頼性の判定データはまだありません。',
    hint: '判定は定期スキャンが生成します（各ノードにつき 1 回の小さなプローブ要求）。「全件スキャン」で今すぐ実行することもできます。',
  },

  error: {
    loadFailed: 'モデルの信頼性データを読み込めませんでした。再試行してください',
    scanFailed: 'モデルの信頼性スキャンを完了できませんでした。再試行してください',
    detailFailed: 'モデルの信頼性判定の詳細を読み込めませんでした。再試行してください',
  },
}
